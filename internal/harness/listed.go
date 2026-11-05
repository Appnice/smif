package harness

import (
	"path/filepath"
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

// ListedProcess names the agent a process runs ("" = none), from its argv.
// argv[0] counts first (it is what the agent calls itself); under a node,
// bun, python or sh wrapper the script counts.
func ListedProcess(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	base := filepath.Base(argv[0])
	if name, ok := listedNames[base]; ok {
		return name
	}
	if rest, ok := strings.CutPrefix(base, "muse-bin-"); ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
		return "muse"
	}
	if base != "node" && base != "bun" && base != "sh" && !strings.HasPrefix(base, "python") {
		return ""
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
		return listedNames[filepath.Base(a)]
	}
	return ""
}
