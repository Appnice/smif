# Safety

- **Your session.** No inbound message enters a session that a human drives. The only text that smif types
  into a session is your own prompt. It types only into an idle session, and only when claude is in the foreground
  of its tab. A prompt you queue for a busy session goes through the same checks, and it can go in when you are
  away. An agent cannot queue, and inbound mail never reaches the queue. `ctrl+f`, or an ask to a session's name,
  gets a read-only fork; the session is not touched. A background session (`claude --bg`) has no tab, so smif only
  reads it: `o` opens `claude attach` in a new tab, with an id from `claude agents`. `R` reopens a past session with
  `claude --resume`, with an id from smif's index of your transcripts. Only you can read that history: an agent token gets no history, search or resume. The session view reads Claude Code's session files and transcripts, which are not a documented
  interface: `smif doctor` checks their shape. Hooks print nothing. The status line
  shows counts only, for example `mail: shop 1 running · 1 needs you`.
- **Workers.** Claude Code flags lock each worker, not a prompt. `--tools` gives only the tools of the profile, so
  other tools do not exist. Hooks are off. No MCP server loads. Deny rules block the smif files. A worker
  that can write gets its own git worktree, and you merge its branch. These flags are tested against the
  real CLI ([compat.md](compat.md)).
- **Loops.** A hop limit, a run limit for each thread, ping-pong detection, dedupe and rate limits stop runaway traffic.
- **Data.** SQLite with `synchronous=FULL`. Each state change writes an audit row in the same transaction.
- **Access.** A Unix socket with mode 0600, a peer uid check and one token for each agent. Agents cannot read
  the mail of other agents or the stats. There is no server, no cloud and no telemetry.
- **What leaves the machine.** Nothing, unless you set `daemon.notify_url`. With that key, each notification also
  sends one POST to that URL. The POST holds a name and what happened, for example `shop waits for you`, and the
  headers `Title: smif` and `Tags: bell`. smif adds no prompt, answer, command, file path or message text.
- **What a name tells.** The name is the smif agent, else the folder, never a session title, because an agent can
  make a title from a prompt. Anyone who knows a public ntfy topic reads these names: use a long random topic or your
  own server.
- **The URL.** A topic in a URL works as a password, so smif keeps the URL out of logs, errors and `smif doctor`.
  `smif doctor` shows only that the ping is on. Nothing comes back: a phone cannot reply, approve or send a prompt.

## Questions

**Does it cost money?** Workers use your Claude Code login. Command handlers and the digest use no model.

**Can a worker break my repo?** A read-only profile cannot write. The `fix` profile writes in its own worktree on
a `smif/<agent>/…` branch. You examine the diff and merge it. `smif apply` does not merge into a
dirty tree.

**What if the daemon stops?** All accepted messages are on disk. At restart, the daemon stops orphan workers and
tries their messages again. Hooks and the status line fail open: they print nothing and return immediately.

**How do I remove it?** Run `smif uninstall`. Your settings come back byte for byte. The data stays in
`~/Library/Application Support/smif` (or `~/.local/share/smif`) until you delete it.
