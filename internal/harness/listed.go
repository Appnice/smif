package harness

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// The Listed level (ADR-0019): a process with one of these names is a
// coding agent, so it shows with its folder, state unknown, no typing.
// The names come from herdr's process table (detect/mod.rs, read at herdr
// commit ee5bfb5), not from a real session. Claude Code is left out: its
// provider reports it, and a `claude -p` run is no session.
var listedNames = map[string]string{
	"codex": "codex", "gemini": "gemini",
	"cursor": "cursor", "cursor-agent": "cursor",
	"devin": "devin", "devin-cli": "devin",
	"agy": "antigravity", "antigravity": "antigravity", "antigravity-cli": "antigravity",
	"cline": "cline", ".cline": "cline",
	"omp": "omp", "pi": "pi",
	"mastracode": "mastracode", "mastra-code": "mastracode",
	"opencode": "opencode", "opencode2": "opencode", "open-code": "opencode", ".opencode": "opencode", "opencode-node": "opencode",
	"copilot": "copilot", "github-copilot": "copilot", "ghcs": "copilot",
	"kimi": "kimi", "kimi-code": "kimi",
	"kiro": "kiro", "kiro-cli": "kiro", "kiro-cli-chat": "kiro",
	"droid": "droid",
	"amp":   "amp", "amp-local": "amp",
	"grok": "grok", "grok-build": "grok",
	"hermes": "hermes", "hermes-agent": "hermes",
	"kilo": "kilo", "kilo-code": "kilo",
	"qodercli": "qoder", "qoderclicn": "qoder", "qoder": "qoder", "qodercn": "qoder",
	"qwen": "qwen", "qwen-code": "qwen",
	"letta": "letta", "letta-code": "letta",
	"maki": "maki",
	"muse": "muse", "muse-code": "muse", "muse-cli": "muse",
}

// listedScripts name an agent that runs as a script under a wrapper, by a
// part of the script's path.
var listedScripts = []struct{ part, name string }{
	{"cursor-agent/versions/", "cursor"},
	{"@oh-my-pi/pi-coding-agent/dist/", "omp"},
	{"@earendil-works/pi-coding-agent/dist/", "pi"},
	{"node_modules/mastracode/dist/cli", "mastracode"},
	{"@moonshot-ai/kimi-code/dist/main.mjs", "kimi"},
	{"hermes_cli.main", "hermes"},
	{"@qwen-code/qwen-code/dist/index", "qwen"},
	{"@letta-ai/letta-code/letta", "letta"},
	{"@github/copilot/index.js", "copilot"},
}

// builtinCommands is the program that starts each agent of the table, for
// session.new (ADR-0021). The daemon builds argv from this table or the
// config, never from a call. Claude Code is left out: its command is the
// config's claude_command, and only it takes --name.
var builtinCommands = map[string]string{
	"codex": "codex", "gemini": "gemini", "cursor": "cursor-agent", "devin": "devin", "antigravity": "agy",
	"cline": "cline", "omp": "omp", "pi": "pi", "mastracode": "mastracode", "opencode": "opencode",
	"copilot": "copilot", "kimi": "kimi", "kiro": "kiro-cli", "droid": "droid", "amp": "amp", "grok": "grok",
	"hermes": "hermes", "kilo": "kilo", "qoder": "qodercli", "qwen": "qwen", "letta": "letta", "maki": "maki",
	"muse": "muse",
}

// builtinProviders are the agents with a provider in this repo. A test in
// internal/daemon keeps the list in step with the daemon's registry.
var builtinProviders = []string{ClaudeName, "qwen", "codex", "opencode", "copilot", "droid", "hermes"}

// IsBuiltinProvider reports a name that a provider of this repo owns.
func IsBuiltinProvider(name string) bool { return slices.Contains(builtinProviders, name) }

// BuiltinNames are the agents of the built-in table, sorted, Claude Code
// left out.
func BuiltinNames() []string { return slices.Sorted(maps.Keys(builtinCommands)) }

// BuiltinCommand is the program that starts a built-in agent ("" = none).
func BuiltinCommand(name string) string { return builtinCommands[name] }

// ProcessOwner names the built-in agent that owns a process name ("" =
// none). claude counts here: a process table entry never makes a Claude
// Code row, because a `claude -p` run is no session.
func ProcessOwner(process string) string {
	if process == ClaudeName {
		return ClaudeName
	}
	return listedNames[process]
}

// ListedProcess names the agent a process runs ("" = none), from its argv.
// argv[0] counts first (it is what the agent calls itself); under a node,
// bun, python or sh wrapper the script counts.
func ListedProcess(argv []string) string { return ListedProcessWith(argv, nil) }

// ListedProcessWith is ListedProcess plus the configured process names
// (process name → agent, [harnesses.<name>] in the config, ADR-0021).
func ListedProcessWith(argv []string, extra map[string]string) string {
	if len(argv) == 0 {
		return ""
	}
	lookup := func(base string) (string, bool) {
		if name, ok := listedNames[base]; ok {
			return name, true
		}
		name, ok := extra[base]
		return name, ok
	}
	base := filepath.Base(argv[0])
	if name, ok := lookup(base); ok {
		return name
	}
	if rest, ok := strings.CutPrefix(base, "muse-bin-"); ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
		return "muse"
	}
	if base != "node" && base != "bun" && base != "sh" && !strings.HasPrefix(base, "python") {
		return ""
	}
	// Hermes's launcher is a multi-line `python -I -c` script, and ps breaks
	// it at the first newline. Its own Python tells it.
	if strings.Contains(argv[0], "/.hermes/tools/python-") && slices.Contains(argv, "-c") {
		return "hermes"
	}
	for _, a := range argv[1:] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		for _, s := range listedScripts {
			if strings.Contains(a, s.part) {
				return s.name
			}
		}
		name, _ := lookup(filepath.Base(a))
		return name
	}
	return ""
}
