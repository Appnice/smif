package harness

import "strings"

// herdrTellsIdle names the agents for which herdr's idle means idle
// (ADR-0019). The source is the table "State source and integration per
// agent" in the research report, read at herdr commit ee5bfb5, not a real
// session. herdr reads a pane's screen, and with no idle rule "no rule
// matched" reads as idle. So an approval dialog that no blocked rule knows
// also reads as idle, and `agent prompt` does not refuse it.
//
// Left out, because the table gives them no idle rule: gemini (B1 W1),
// copilot (B1 W2), cursor (B2 W3), droid (B2 W2), qoder (B1 W2), antigravity
// (B4 W2). Left out, because the table says only "self-report" and names no
// permission coverage: crush, command code, prime agent.
//
// The names are the ones herdr 0.9.5 prints in `agent list` for a fake
// process with that name (seen 2026-11-05; letta showed no row). A name that
// is not here counts as no rule, so a new agent never types until someone
// reads its rules.
var herdrTellsIdle = map[string]bool{
	// An idle screen rule.
	"claude": true, "codex": true, "devin": true, "qwen": true, "hermes": true, "letta": true,
	"grok": true, "amp": true, "cline": true, "maki": true, "kiro": true, "muse": true,
	// A plugin, extension or hook set that sets herdr's state and covers
	// permissions. herdr gives these six full authority, but only with the
	// integration in place: see HerdrNeedsIntegration.
	"opencode": true, "kilo": true, "pi": true, "omp": true, "mastracode": true, "kimi": true,
}

// HerdrTellsIdle reports whether herdr can tell idle from a dialog for this
// agent. Where it cannot, herdr's idle and done are unknown, never idle.
func HerdrTellsIdle(agent string) bool { return herdrTellsIdle[agent] }

// herdrNeedsIntegration names the agents whose idle rule is herdr's
// integration and nothing else: herdr has no screen rule for them. With no
// integration their idle is the fallback for "no rule matched" (herdr 0.9.5,
// seen 2026-11-05 on fake kimi and omp panes: `agent explain` gives
// default_known_agent_idle_fallback and matched_rule null). That output does
// not tell a reported state from the fallback, so the integration's status is
// the signal.
var herdrNeedsIntegration = map[string]bool{
	"opencode": true, "kilo": true, "pi": true, "omp": true, "mastracode": true, "kimi": true,
}

// HerdrNeedsIntegration reports whether herdr's idle for this agent counts
// only with herdr's integration for it.
func HerdrNeedsIntegration(agent string) bool { return herdrNeedsIntegration[agent] }

// HerdrIntegrationsCurrent reads the output of `herdr integration status`
// and names the agents whose integration is current. Each line reads
// "<agent>: <state> (<path>)", with the state `not installed`, `current (v9)`,
// `outdated (legacy < v9)` or `needs repair (...)` (herdr 0.9.5, docs/compat.md).
//
// Only current counts. A file with no herdr code at the right path reads
// outdated, so outdated does not prove a working integration, and any other
// line is an answer smif cannot read: the agent stays out of the result.
func HerdrIntegrationsCurrent(status string) map[string]bool {
	current := map[string]bool{}
	for line := range strings.Lines(status) {
		if agent, state, ok := strings.Cut(line, ": "); ok && strings.HasPrefix(state, "current (") {
			current[agent] = true
		}
	}
	return current
}
