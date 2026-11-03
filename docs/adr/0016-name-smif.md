# ADR-0016: The name is Smif

**Status:** accepted, 2026-11-03.

**Context.** The project started as Mailroom: agents in the background write to the human, rules sort the mail,
handlers answer, and what no one can deliver goes to dead letters. Since ADR-0012 the main use is a live view of
every Claude Code session on the machine:

- which one works, which one waits for you and on what;
- reminders, saved prompts and new sessions (ADR-0014, ADR-0015).

In the two days before this decision the human typed into sessions 12 times. Only 6 messages came in, most of
them from tests. "Mailroom" names the smaller part and reads as an email client. The repo was still private
with a single install, so a rename was cheap now and costly after a public release.

**Decision.** The product, the binary, the Go module and the repo are `smif`:

- module `github.com/appnice/smif`, binary `cmd/smif`;
- config `~/.config/smif`, data `~/Library/Application Support/smif` (Linux: `~/.local/share/smif`);
- `smif.db`, `smifd.sock`, `logs/smifd.jsonl`;
- environment `SMIF_*`;
- launchd label `local.smif`, systemd unit `smif.service`;
- MCP server `smif`, its tools `smif_*`, the slash command `/smif`.

The name is a short, typeable word with few clashes (no Homebrew formula, only small unrelated projects on
GitHub). The mail model inside (envelopes, rules, handlers, dead letters, the digest) keeps its terms.

**Migration.**

- `smif install` removes a launchd agent left under `local.mailroom` or `dev.appnice.mailroom`.
- There is no automatic move of the old folders. The one install was moved by hand:
  1. `mailroom uninstall`;
  2. move the config and data folders and rename the files in them;
  3. `smif install`.
- The old `MAILROOM_*` variables are not read.

**Tests.** The suite runs unchanged under the new names (`make check`).
