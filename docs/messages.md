# Messages between projects

Sessions and scripts can send messages to a project. smif sorts them with your rules, and no message text enters a
session that you type in.

To set it up, run `smif install` and `smif init`. `smif init` finds your git repos and gives each one an agent id.

| Message | Handler | Result |
|---|---|---|
| An ask with a known answer | **command**: a script, no model | The asking session gets the answer in about 25 ms. |
| A crash, an error, a code question | **worker**: a headless `claude -p` with a locked tool set | You get a summary in `/smif`. A fix is a branch for you to merge. |
| An ask to a session's name | **fork** of that session | The asker gets an answer from the session's context. Nothing is typed into it, and nothing enters its queue. |
| Information (a build is done) | **digest** | You get one line in `/smif`. |
| A decision for a human | **escalate** | You get a count in the status line, an OS notification and an item in the UI. |

## Sending a message

| From | How |
|---|---|
| A Claude Code session | Say "ask the web agent if v2.3 is live". The MCP tools are `smif_ask`, `smif_send`, `smif_await`, `smif_agents` and `smif_status`. |
| A shell or a script | `smif ask --to shop --intent store.status --subject "Live?" --wait 30` |
| A crash monitor, CI, cron | `smif emit --as monitor --to shop --intent crash.report --subject "NPE" --dedupe-key npe-1 --body - < report.md` |

## Configuration

The file is `~/.config/smif/config.toml`. Each agent has its own rules. The first rule that matches
the message is used.

```toml
[agents.shop]
repo = "~/dev/shop"
accept_from = ["monitor", "web"]

[[agents.shop.rules]]                # a script answers, no model
match = { kind = "ask", intent = "store.status" }
handler = "command"
command = ["~/.config/smif/handlers/status.sh"]

[[agents.shop.rules]]                # a read-only worker examines crashes, one run for each problem a week
match = { kind = "event", intent = "crash.*" }
handler = "worker"
profile = "triage"
cooldown = "7d"

[[agents.shop.rules]]
match = { kind = "event", intent = "notice.*" }
handler = "digest"

[[agents.shop.rules]]                # all other messages wait for you
match = {}
handler = "escalate"
```

- **Agent of a session:** the repo of the session folder. For a different folder, start the session with
  `SMIF_AGENT=shop claude`.
- **Profiles** set the model, effort, tools, write scope (`none`, `worktree` or `direct`), turns and timeout.
  The built-in profiles are `triage`, `fix`, `answer` and `release`. `harness = "codex"` runs one on Codex
  ([below](#codex)).
- **Command handlers** get the message as JSON on stdin. They run in the agent repo. They print
  `{"status":"done","summary":"…","reply":"…"}`. `smif init` installs one that answers `status.get` from git.
- To apply a change: `smif config validate`, then `smif reload`. You do not restart the daemon.

## Codex

A Claude Code session can ask Codex to review a diff, and Codex can ask a Claude Code worker. The mail is the same, and
only the worker differs. This was checked on a real Codex CLI 0.166.0 ([ADR-0022](adr/0022-mail-between-harnesses.md)).

To send mail to Codex, add a profile and a rule. A message with `review.*` then runs `codex exec` in that repo:

```toml
[profiles.review]
harness = "codex"
timeout_s = 300

[[agents.shop.rules]]
match = { intent = "review.*" }
handler = "worker"
profile = "review"
```

```
ask the shop agent to review my uncommitted diff, intent review.diff
```

The Codex worker can only read: its sandbox is `read-only`, and a profile with another `write_scope` fails `smif config
validate`. It runs on your Codex login, keeps no session, and sends no followups. `model` is optional and goes to
`codex exec --model`. Set `[harnesses.codex] command` if `codex` is not on the daemon's PATH.

To send mail from Codex, add the MCP server. The agent comes from the folder:

```sh
codex mcp add smif -- smif mcp
```

Codex asks before each MCP call. To skip that, add this to `~/.codex/config.toml`:

```toml
[mcp_servers.smif]
default_tools_approval_mode = "approve"
```

Mail never goes into a live Codex session, as for Claude Code: a worker handles it.
