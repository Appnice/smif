package claude

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Caps records which CLI features the installed Claude Code supports
// (RFC 10.2). Each has a fallback in the runner.
type Caps struct {
	Version            string
	StreamJSON         bool
	Resume             bool
	SessionID          bool
	AppendSystemPrompt bool
	AllowedTools       bool
	DisallowedTools    bool
	PermissionModes    []string
	PermissionPrompts  bool
	MCPConfig          bool
	StrictMCP          bool
	JSONSchema         bool
	SafeMode           bool
	Effort             bool
	MaxBudget          bool
	Settings           bool
	Name               bool
	Tools              bool
	Problems           []string
}

// VerifiedVersion is the Claude Code version the flags were checked against
// (docs/compat.md).
const VerifiedVersion = "2.1.316"

// AssumedCaps is the verified feature set, used when no probe ran.
func AssumedCaps() *Caps {
	return &Caps{Version: VerifiedVersion, StreamJSON: true, Resume: true, SessionID: true, AppendSystemPrompt: true,
		AllowedTools: true, DisallowedTools: true, PermissionModes: []string{"acceptEdits", "auto", "bypassPermissions", "manual", "dontAsk", "plan", "default"},
		PermissionPrompts: true, MCPConfig: true, StrictMCP: true, JSONSchema: true, SafeMode: true, Effort: true, MaxBudget: true,
		Settings: true, Name: true, Tools: true}
}

var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// Probe runs `claude --version` and `claude --help` and parses the flags.
// It never calls the API.
func Probe(ctx context.Context, command string) *Caps {
	c := &Caps{}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	v, err := exec.CommandContext(ctx, command, "--version").Output()
	if err != nil {
		c.Problems = append(c.Problems, fmt.Sprintf("`%s --version` failed: %v (is Claude Code installed and on the daemon's PATH?)", command, err))
		return c
	}
	c.Version = versionRe.FindString(string(v))
	h, err := exec.CommandContext(ctx, command, "--help").CombinedOutput()
	if err != nil {
		c.Problems = append(c.Problems, fmt.Sprintf("`%s --help` failed: %v", command, err))
		return c
	}
	help := string(h)
	has := func(f string) bool { return strings.Contains(help, f) }
	c.StreamJSON = has("stream-json")
	c.Resume = has("--resume")
	c.SessionID = has("--session-id")
	c.AppendSystemPrompt = has("--append-system-prompt")
	c.AllowedTools = has("--allowedTools") || has("--allowed-tools")
	c.DisallowedTools = has("--disallowedTools") || has("--disallowed-tools")
	c.PermissionPrompts = has("--permission-prompts")
	c.MCPConfig = has("--mcp-config")
	c.StrictMCP = has("--strict-mcp-config")
	c.JSONSchema = has("--json-schema")
	c.SafeMode = has("--safe-mode")
	c.Effort = has("--effort")
	c.MaxBudget = has("--max-budget-usd")
	c.Settings = has("--settings")
	c.Name = has("--name")
	c.Tools = has("--tools")
	if m := regexp.MustCompile(`--permission-mode[^\n]*\n?(?:[^\n]*\n){0,4}`).FindString(help); m != "" {
		for _, mode := range []string{"acceptEdits", "auto", "bypassPermissions", "manual", "dontAsk", "plan", "default"} {
			if strings.Contains(m, `"`+mode+`"`) {
				c.PermissionModes = append(c.PermissionModes, mode)
			}
		}
		// "default" is accepted but not listed since 2.1.x (verified in compat.md).
		if !strings.Contains(m, `"default"`) {
			c.PermissionModes = append(c.PermissionModes, "default")
		}
	}
	required := map[string]bool{"-p --output-format stream-json": c.StreamJSON, "--resume": c.Resume,
		"--allowedTools": c.AllowedTools, "--disallowedTools": c.DisallowedTools, "--mcp-config": c.MCPConfig}
	for f, ok := range required {
		if !ok {
			c.Problems = append(c.Problems, "required flag missing: "+f)
		}
	}
	return c
}

// OK reports whether worker runs can proceed.
func (c *Caps) OK() bool { return c != nil && c.Version != "" && len(c.Problems) == 0 }

// Lines describes each probed item for doctor and admin.health.
func (c *Caps) Lines() []string {
	mark := func(b bool) string {
		if b {
			return "ok"
		}
		return "MISSING"
	}
	return []string{
		"version " + c.Version + " (verified against " + VerifiedVersion + ")",
		"stream-json output: " + mark(c.StreamJSON),
		"--resume: " + mark(c.Resume),
		"--session-id: " + mark(c.SessionID),
		"--append-system-prompt: " + mark(c.AppendSystemPrompt) + fallback(c.AppendSystemPrompt, "policy prepended to the prompt"),
		"--allowedTools/--disallowedTools: " + mark(c.AllowedTools && c.DisallowedTools),
		"--permission-mode values: " + strings.Join(c.PermissionModes, ", "),
		"--permission-prompts none: " + mark(c.PermissionPrompts) + fallback(c.PermissionPrompts, "denied tools rely on the supervisor timeout"),
		"--mcp-config/--strict-mcp-config: " + mark(c.MCPConfig && c.StrictMCP) + fallback(c.StrictMCP, "user MCP servers load in workers"),
		"--json-schema (structured output): " + mark(c.JSONSchema) + fallback(c.JSONSchema, "last ```json block + repair turn"),
		"--safe-mode (minimal profiles): " + mark(c.SafeMode) + fallback(c.SafeMode, "minimal = true is rejected"),
		"--effort: " + mark(c.Effort) + fallback(c.Effort, "ignored with a warning"),
		"--max-budget-usd: " + mark(c.MaxBudget) + fallback(c.MaxBudget, "per-run cost cap not enforced by the CLI"),
		"--settings disableAllHooks (workers skip user/repo hooks): " + mark(c.Settings) + fallback(c.Settings, "hooks run inside workers"),
		"--name (worker sessions labelled in the resume picker): " + mark(c.Name),
		"--tools (built-in tool set limited to the profile): " + mark(c.Tools) + fallback(c.Tools, "unlisted tools stay available; only allow/deny rules apply"),
		"--max-turns: hidden flag, verified by a live call in compat.md",
	}
}

func fallback(ok bool, s string) string {
	if ok {
		return ""
	}
	return " (fallback: " + s + ")"
}

// CompareVersions compares two dotted versions ("2.1.316"): -1, 0 or 1.
// Missing or non-numeric parts count as 0.
func CompareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

// SameMinor reports whether two versions share major and minor ("2.1").
func SameMinor(a, b string) bool {
	pa, pb := strings.SplitN(a, ".", 3), strings.SplitN(b, ".", 3)
	return len(pa) >= 2 && len(pb) >= 2 && pa[0] == pb[0] && pa[1] == pb[1]
}
