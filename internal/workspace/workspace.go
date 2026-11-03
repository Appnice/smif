// Package workspace manages per-agent git worktrees and thread branches
// (RFC 6.4, D5). Merging is always a human action.
package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/envelope"
)

// Manager owns worktrees under Root/<agent>.
type Manager struct {
	Root string
}

// Workspace is where a run executes.
type Workspace struct {
	Dir     string
	Scope   string
	Branch  string
	BaseSHA string
}

// Git runs git in dir and returns trimmed stdout.
func Git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String()), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// BranchName is smif/<agent>/<thread-ulid-short>.
func BranchName(agent, thread string) string {
	return "smif/" + agent + "/" + envelope.Short(thread)
}

// Prepare returns the workspace for a run.
//
//	none, direct: the main checkout.
//	worktree:     Root/<agent> on the thread branch, cleaned and verified.
//
// existingBranch is the branch already recorded for this thread, if any.
func (m *Manager) Prepare(a *config.Agent, scope, thread, existingBranch string) (*Workspace, error) {
	repo := a.RepoPath()
	if _, err := Git(repo, "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("agent %s: %s is not a git repository: %w", a.ID, repo, err)
	}
	if scope != "worktree" {
		return &Workspace{Dir: repo, Scope: scope}, nil
	}
	wt := filepath.Join(m.Root, a.ID)
	if err := m.ensureWorktree(repo, wt, a.BaseRef); err != nil {
		return nil, err
	}
	branch := existingBranch
	if branch == "" {
		branch = BranchName(a.ID, thread)
	}
	// Anything left over from an interrupted run is committed, not lost.
	if dirty(wt) {
		cur, _ := Git(wt, "rev-parse", "--abbrev-ref", "HEAD")
		if cur != "HEAD" && cur != "" {
			_ = commitAll(wt, "smif: recovered changes from an interrupted run")
		} else {
			_, _ = Git(wt, "reset", "--hard")
		}
	}
	if _, err := Git(wt, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		if _, err := Git(wt, "checkout", "--quiet", branch); err != nil {
			// Branch conflict (e.g. checked out elsewhere): suffixed branch.
			alt := branch + "-" + time.Now().Format("150405")
			if _, err2 := Git(wt, "checkout", "--quiet", "-b", alt, branch); err2 != nil {
				return nil, fmt.Errorf("checkout %s: %v; fallback %s: %v", branch, err, alt, err2)
			}
			branch = alt
		}
	} else {
		if _, err := Git(wt, "checkout", "--quiet", "-b", branch, a.BaseRef); err != nil {
			return nil, err
		}
	}
	if _, err := Git(wt, "clean", "-fd", "--quiet"); err != nil {
		return nil, err
	}
	if dirty(wt) {
		return nil, fmt.Errorf("worktree %s is not clean after checkout", wt)
	}
	base, err := Git(wt, "merge-base", "HEAD", a.BaseRef)
	if err != nil {
		base, _ = Git(wt, "rev-parse", "HEAD")
	}
	return &Workspace{Dir: wt, Scope: scope, Branch: branch, BaseSHA: base}, nil
}

func (m *Manager) ensureWorktree(repo, wt, base string) error {
	if _, err := os.Stat(filepath.Join(wt, ".git")); err == nil {
		if _, err := Git(wt, "rev-parse", "--is-inside-work-tree"); err == nil {
			return nil
		}
		// Broken worktree: recreate (RFC 8.4).
		os.RemoveAll(wt)
	}
	_, _ = Git(repo, "worktree", "prune")
	if err := os.MkdirAll(filepath.Dir(wt), 0o700); err != nil {
		return err
	}
	if _, err := Git(repo, "worktree", "add", "--detach", "--quiet", wt, base); err != nil {
		return fmt.Errorf("create worktree: %w", err)
	}
	return nil
}

func dirty(dir string) bool {
	out, err := Git(dir, "status", "--porcelain")
	return err == nil && out != ""
}

func identityArgs(dir string) []string {
	if e, _ := Git(dir, "config", "user.email"); e == "" {
		return []string{"-c", "user.name=Smif", "-c", "user.email=smif@localhost"}
	}
	return nil
}

func commitAll(dir, msg string) error {
	if _, err := Git(dir, "add", "-A"); err != nil {
		return err
	}
	args := append(identityArgs(dir), "commit", "--quiet", "--no-verify", "-m", msg)
	_, err := Git(dir, args...)
	return err
}

// Finish commits a dirty worktree ("smif: <summary> [run <id>]") and
// returns the head SHA and the branch diffstat against its base.
func (m *Manager) Finish(ws *Workspace, summary, runID string) (head, diffstat string, err error) {
	if ws == nil || ws.Scope != "worktree" {
		return "", "", nil
	}
	if dirty(ws.Dir) {
		s := strings.TrimSpace(strings.ReplaceAll(summary, "\n", " "))
		if s == "" {
			s = "worker changes"
		}
		if len(s) > 120 {
			s = s[:120]
		}
		if err := commitAll(ws.Dir, fmt.Sprintf("smif: %s [run %s]", s, runID)); err != nil {
			return "", "", err
		}
	}
	head, _ = Git(ws.Dir, "rev-parse", "HEAD")
	if ws.BaseSHA != "" && head != ws.BaseSHA {
		diffstat, _ = Git(ws.Dir, "diff", "--stat", ws.BaseSHA+".."+head)
	}
	return head, diffstat, nil
}

// Diff shows a branch against the merge base with the repo's current HEAD.
func Diff(repo, branch string) (string, error) {
	return Git(repo, "diff", "HEAD..."+branch)
}

// ErrDirty is returned when apply finds uncommitted changes.
var ErrDirty = errors.New("the main checkout has uncommitted changes; commit or stash them first")

// ErrMerged is returned when the branch is already in the current branch.
var ErrMerged = errors.New("the branch is already merged into the main checkout's current branch")

// Apply merges branch into the main checkout's current branch. It refuses
// on a dirty tree.
func Apply(repo, branch, runID string) (string, error) {
	out, err := Git(repo, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return "", err
	}
	if out != "" {
		return "", ErrDirty
	}
	if _, err := Git(repo, "merge-base", "--is-ancestor", branch, "HEAD"); err == nil {
		return "", ErrMerged
	}
	args := append(identityArgs(repo), "merge", "--no-ff", "--no-edit", "-m", fmt.Sprintf("Merge %s (smif run %s)", branch, runID), branch)
	if _, err := Git(repo, args...); err != nil {
		_, _ = Git(repo, "merge", "--abort")
		return "", err
	}
	return Git(repo, "rev-parse", "HEAD")
}

// GC deletes smif/<agent>/* branches whose last commit is older than
// retention, detaching the worktree first when needed. Returns how many.
func (m *Manager) GC(a *config.Agent, retention time.Duration, now time.Time) int {
	repo := a.RepoPath()
	out, err := Git(repo, "for-each-ref", "--format=%(refname:short) %(committerdate:unix)", "refs/heads/smif/"+a.ID+"/")
	if err != nil || out == "" {
		return 0
	}
	wt := filepath.Join(m.Root, a.ID)
	cur, _ := Git(wt, "rev-parse", "--abbrev-ref", "HEAD")
	n := 0
	for _, line := range strings.Split(out, "\n") {
		var name string
		var ts int64
		if _, err := fmt.Sscanf(line, "%s %d", &name, &ts); err != nil {
			continue
		}
		if now.Sub(time.Unix(ts, 0)) < retention {
			continue
		}
		if name == cur {
			if _, err := Git(wt, "checkout", "--quiet", "--detach"); err != nil {
				continue
			}
		}
		if _, err := Git(repo, "branch", "-D", name); err == nil {
			n++
		}
	}
	return n
}
