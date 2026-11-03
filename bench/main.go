// Command bench is Smif's benchmark (docs/rfc-benchmark.md, tiers S, R
// and T). It costs nothing: every scenario runs a throw-away daemon with
// the fake claude. Only -real calls the real claude, on haiku, through the
// existing login.
//
//	go run ./bench                 # full run, about 3 minutes
//	go run ./bench -quick          # smaller counts, about 1 minute
//	go run ./bench -real           # also the actual-token sample (subscription)
//	go run ./bench -only S1,R3     # some scenarios
//
// It writes bench/results/<date>-<commit>/report.md and results.json.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type section struct {
	ID   string `json:"id"`
	Text string `json:"-"`
	Data any    `json:"data"`
}

type bench struct {
	bin, fake string
	quick     bool
	sections  []section
}

func (b *bench) env(daemonExtra, profiles string, agents ...agentSpec) (*env, error) {
	e, err := newEnv(b.bin, b.fake, daemonExtra, profiles, agents...)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(profiles, "[profiles.ro]") { // -real starts its own daemon after swapping claude
		if err := e.start(); err != nil {
			e.close()
			return nil, err
		}
	}
	return e, nil
}

func (b *bench) add(id, text string, data any) {
	b.sections = append(b.sections, section{ID: id, Text: text, Data: data})
	fmt.Fprintf(os.Stderr, "  %s done\n", id)
}

func (b *bench) dur(full time.Duration) time.Duration {
	if b.quick {
		return full / 4
	}
	return full
}

func (b *bench) count(full int) int {
	if b.quick {
		return max(5, full/5)
	}
	return full
}

func main() {
	quick := flag.Bool("quick", false, "smaller counts and shorter loads")
	real := flag.Bool("real", false, "also measure actual tokens with the real claude on haiku (uses the existing login)")
	trials := flag.Int("trials", 2, "trials per arm for -real")
	only := flag.String("only", "", "comma-separated scenario ids (S1,S2,S3,S4,R1,R2,R3)")
	out := flag.String("out", "", "output dir (default bench/results/<date>-<commit>)")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fail(err)
	}
	tmp, err := os.MkdirTemp("", "smif-bench-bin-")
	if err != nil {
		fail(err)
	}
	defer os.RemoveAll(tmp)
	b := &bench{quick: *quick}
	fmt.Fprintln(os.Stderr, "building smif and the fake claude…")
	if b.bin, b.fake, err = buildBinaries(root, tmp); err != nil {
		fail(err)
	}
	want := map[string]bool{}
	for _, id := range strings.Split(*only, ",") {
		if id = strings.TrimSpace(strings.ToUpper(id)); id != "" {
			want[id] = true
		}
	}
	run := func(id string, f func() error) {
		if len(want) > 0 && !want[id] {
			return
		}
		fmt.Fprintf(os.Stderr, "%s…\n", id)
		if err := f(); err != nil {
			b.add(id+"!", fmt.Sprintf("### %s · FAILED\n\n```\n%v\n```\n", id, err), map[string]any{"error": err.Error()})
		}
	}
	start := time.Now()
	run("S1", b.s1CommandAsk)
	run("S2", b.s2IngestS5Hooks)
	run("S3", b.s3WorkerOverheadT1)
	perRun := 0
	for _, s := range b.sections {
		if s.ID == "T1" {
			if m, ok := s.Data.(map[string]any); ok {
				perRun, _ = m["total_bytes"].(int)
			}
		}
	}
	run("S4", func() error { return b.s4Storm(perRun) })
	run("R1", b.r1Kill9)
	run("R2", b.r2Loop)
	run("R3", b.r3CanaryT2)
	if *real {
		run("TR", func() error { return b.realTokens(*trials) })
	}

	commit := strings.TrimSpace(sh(root, "git", "rev-parse", "--short", "HEAD"))
	if *out == "" {
		*out = filepath.Join(root, "bench", "results", time.Now().Format("2006-01-02")+"-"+commit)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# Smif benchmark %s\n\n", time.Now().Format("2006-01-02 15:04"))
	fmt.Fprintf(&md, "- commit `%s`%s, %s, Go %s\n- machine: %s, %d CPUs, %s\n- mode: %s; took %v; money spent: $0%s\n\n",
		commit, dirty(root), runtime.GOOS+"/"+runtime.GOARCH, strings.TrimPrefix(runtime.Version(), "go"), cpuName(),
		runtime.NumCPU(), osVersion(), mode(*quick), time.Since(start).Round(time.Second), realNote(*real))
	md.WriteString("Every scenario starts a fresh daemon in /tmp with the fake claude (`testdata/fakeclaude`), so the numbers are " +
		"Smif's own cost, not the model's. Plan and method: `docs/rfc-benchmark.md`. Token counts marked ≈ are bytes/4 " +
		"estimates; T-real (with `-real`) has actual counts.\n\n")
	order := []string{"S1", "S2", "S5", "S3", "S4", "R1", "R2", "R3", "T1", "T2", "TR"}
	heads := map[string]string{"S1": "## S · system overhead\n\n", "R1": "## R · robustness and safety\n\n", "T1": "## T · tokens\n\n"}
	done := map[string]bool{}
	for _, id := range order {
		for _, s := range b.sections {
			if s.ID == id || s.ID == id+"!" {
				if h, ok := heads[id]; ok {
					md.WriteString(h)
				}
				md.WriteString(s.Text + "\n")
				done[s.ID] = true
			}
		}
	}
	for _, s := range b.sections {
		if !done[s.ID] {
			md.WriteString(s.Text + "\n")
		}
	}
	os.WriteFile(filepath.Join(*out, "report.md"), []byte(md.String()), 0o644)
	raw, _ := json.MarshalIndent(map[string]any{"commit": commit, "quick": *quick, "sections": b.sections}, "", "  ")
	os.WriteFile(filepath.Join(*out, "results.json"), raw, 0o644)
	fmt.Println(filepath.Join(*out, "report.md"))
}

func mode(q bool) string {
	if q {
		return "quick (-quick: a quarter of the load, a fifth of the counts)"
	}
	return "full"
}

func realNote(r bool) string {
	if r {
		return " (T-real used the existing Claude login on haiku; no API key)"
	}
	return ""
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("run from the smif repo: %v", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func sh(dir string, name string, args ...string) string {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, _ := cmd.Output()
	return string(out)
}

func dirty(root string) string {
	if strings.TrimSpace(sh(root, "git", "status", "--porcelain", "--untracked-files=no")) != "" {
		return " (uncommitted changes)"
	}
	return ""
}

func cpuName() string {
	if runtime.GOOS == "darwin" {
		if s := strings.TrimSpace(sh("", "sysctl", "-n", "machdep.cpu.brand_string")); s != "" {
			return s
		}
	}
	b, _ := os.ReadFile("/proc/cpuinfo")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "model name") {
			_, v, _ := strings.Cut(l, ":")
			return strings.TrimSpace(v)
		}
	}
	return "unknown CPU"
}

func osVersion() string {
	if runtime.GOOS == "darwin" {
		return "macOS " + strings.TrimSpace(sh("", "sw_vers", "-productVersion"))
	}
	return strings.TrimSpace(sh("", "uname", "-sr"))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "bench:", err)
	os.Exit(1)
}
