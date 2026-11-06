<p align="center">
  <a href="https://smif.dev">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="docs/brand/logo-dark.svg">
      <img alt="smif" src="docs/brand/logo-light.svg" height="76">
    </picture>
  </a>
</p>

<p align="center">
  <b>All your Claude Code sessions. One calm terminal.</b><br>
  See which one waits for you, what each one runs now, and answer any of them without hunting for the tab.
</p>

<p align="center">
  <a href="https://smif.dev"><b>smif.dev</b></a> ·
  <a href="#install">Install</a> ·
  <a href="#docs">Docs</a>
</p>

<p align="center">
  <a href="https://github.com/appnice/smif/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/appnice/smif?color=fabd2f"></a>
  <a href="#install"><img alt="macOS and Linux" src="https://img.shields.io/badge/macOS%20%C2%B7%20Linux-single%20binary-555"></a>
  <a href="LICENSE"><img alt="MIT licence" src="https://img.shields.io/badge/licence-MIT-fabd2f"></a>
</p>

<p align="center">
  <img alt="smif with eight Claude Code sessions: one waits on a question, one just answered, one works on terraform plan. Tab jumps to the next session that needs you, a saved prompt is typed into an idle session, its tool calls show live, and its answer lands." src="docs/img/hero.gif" width="100%">
</p>

## Install

```sh
curl -fsSL https://smif.dev/install.sh | sh
smif
```

smif finds your open sessions on its own, with no config. It needs Claude Code 2.1 and git, on macOS or Linux.

<details>
<summary>Other ways to install</summary>
<br>

The script puts `smif` in `~/.local/bin` and checks its checksum ([read it](site/install.sh)). Set
`SMIF_INSTALL_DIR` for another folder.

```sh
go install github.com/appnice/smif/cmd/smif@latest   # Go 1.27.2 or newer
```

Binaries are on the [release page](https://github.com/appnice/smif/releases/latest). They are not notarized, so on
macOS a file saved from a browser needs `xattr -d com.apple.quarantine smif`.

</details>

## See every session

| Row | Meaning |
|---|---|
| `! 3m … needs you` | Stopped on a permission prompt or a question |
| `▶ 4m12s … Bash npm run build` | Working, and the step it runs now |
| `● new` | An answer you have not read |
| `○` | Idle |

`tab` jumps to the next session that needs you. `o` brings its tab to the front.

smif never answers a permission prompt for you.

## Answer from one place

<p align="center">
  <img alt="The chat with an idle session: a saved prompt is typed into its tab, its steps show as they run, and the answer arrives." src="docs/img/chat.gif" width="100%">
</p>

`enter` opens a chat with the selected session.

- **Idle:** smif types your prompt into its tab. This works in Terminal.app, iTerm2, tmux and herdr.
- **Busy or waiting:** the text waits in a queue and goes in when the session is idle. `ctrl+f` asks a read-only fork instead, which needs `smif init`.

A long paste stays whole, `ctrl+v` attaches an image, and smif warns you before it sends something that looks like a
secret.

| Key | Does |
|---|---|
| `tab` | Next session that needs you |
| `enter` | Chat |
| `o` | Go to the session's tab |
| `p` | Send a saved prompt |
| `n` | New session in the same folder |
| `m` | Machines |
| `y` | Copy the newest code block |
| `Y` | Copy the newest answer |
| mouse drag | Select inside one pane and copy |
| `?` | All keys |

## Other machines

<p align="center">
  <img alt="Two laptops joined by ssh. Sessions on the other device join the list on your laptop. One starts to wait and your laptop tells you. If you allowed writing, a line goes back over ssh. A permission prompt stays yours." src="docs/img/machines.gif" width="100%">
</p>

Press `m` in smif, or run `smif remote add`. Pick a machine from your network, `~/.ssh/config` or Tailscale, or type an
address, and smif sets it up. You type the password once.

You can read its sessions here, and a session that starts to wait notifies you. Writing to them is off until you turn it
on for that machine with `w`. See [docs/usage.md](docs/usage.md).

## Stop watching the screen

smif sends a notification when a session starts to wait, or answers after a long turn.

With `smif install`, the status line of each session names the others:

```
! shop waits · ● docs, scraper answered
```

`smif install` also adds hooks, `/smif`, the MCP server and a background service. It backs up every file it changes.

## Send messages between projects

Sessions can ask each other things, like mail between projects:

```
ask the web agent if v2.3 is live
```

The message waits on disk. Your rules decide what handles it, and your open sessions are not interrupted.

| Message | Handled by | You get |
|---|---|---|
| A question with a known answer | A script, no model | An answer in about 25 ms |
| A crash or a code question | A background `claude -p` with locked tools | A summary, and a fix as a branch to merge |
| A note, like a finished build | The digest | One line in `/smif` |
| A decision for you | You | A notification |

Set it up with `smif install` and `smif init`. See [docs/messages.md](docs/messages.md).

## Other agents

Claude Code and Codex are checked against a real agent. The rest come from reading their source, so expect faults.

| Agent | Support |
|---|---|
| Claude Code | Everything |
| Codex | See, type when idle, queue. Cannot see an approval prompt, so no `needs you` |
| Qwen Code, OpenCode, GitHub Copilot CLI, Factory Droid | Read only, not verified |
| 21 more, in a [herdr](https://herdr.dev) pane | Name, folder and state, not verified |
| Any other agent, with `[harnesses.<name>]` in the config | Name and folder, see [docs/agents.md](docs/agents.md#add-an-agent) |

Mail also works between Claude Code and Codex, so one can ask the other to review a diff
([docs/messages.md](docs/messages.md#codex)). Forks work with Claude Code only. See [docs/agents.md](docs/agents.md).

## How it works

<p align="center">
  <img alt="Claude Code's session files, transcripts and hooks feed the local smifd daemon (SQLite, a Unix socket with mode 0600, one per user), which feeds the smif view, the status line inside every session, notifications and your terminal tabs." src="docs/img/flow.gif" width="100%">
</p>

smif reads the files Claude Code already writes, and one local daemon keeps the state. Nothing leaves your machine:
no server, no cloud, no telemetry. The exceptions are the machines you add and a phone ping that you turn on with `daemon.notify_url`: it
sends the agent or folder name and what happened, with no prompt or answer ([docs/safety.md](docs/safety.md)).

## Uninstall

```sh
smif uninstall
```

This puts back every file byte for byte. Your data stays in `~/Library/Application Support/smif` or
`~/.local/share/smif` until you delete it.

## Docs

- [Usage](docs/usage.md): every key, notifications, saved prompts, stats
- [Messages](docs/messages.md): rules, workers, configuration
- [Agents](docs/agents.md): support levels per agent
- [Safety](docs/safety.md): what workers can do, and common questions
- [Numbers](docs/numbers.md): latency, tokens, failure tests
- [Spec](docs/rfc.md), [decisions](docs/adr), [compatibility](docs/compat.md)

To work on smif, run `make check`, or `make check-linux` for Docker.

## License

MIT
