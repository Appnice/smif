# ADR-0023: Sessions on other machines

Status: accepted, 2026-11-06.

## Decision

`daemon.remotes` lists ssh hosts that run smif too. The daemon runs `smif sessions --json` on each one every 15 s over a
shared ssh connection, and adds the rows to `session.list`.

- A row is read only. Its id is `host/id`, its folder `host:path`, and `session.prompt` finds no such session.
- A session that starts to wait notifies here, so the point of the list holds for a machine you do not watch.
- A host that fails keeps its rows for one minute, then they leave. `smif doctor` shows a warning, never a failure.
- A host name starts with a letter or digit, so it can never read as an ssh option. `--` ends the options as well.
- The command is a constant. Nothing from a config value but the host, and nothing from a session, reaches ssh.
- ADR-0024 replaces the setup: `smif remote add` and the Machines screen.

## Why not more

- **No typing over ssh.** The gates for typing (the foreground check, the terminal) work on the machine where the
  session runs. A remote `smif prompt` would need a second gate and text in a remote command line.
- **No tunnel of the socket.** It would hand the other machine a human token.
- **No transcript view.** The row has the last answer and the steps. To read more, ssh there.
- **No new daemon on the other side.** The list comes from the smif that already runs there.

## Checked

`TestRemoteSessions` and `TestRemoteAdd` cover the rows, argv, refusals and setup with a stand-in. `smif remote add` also ran
with real `ssh` against a real `sshd` on this Mac, with a separate home: refused key, unknown host key, the v0.1.0 install
from smif.dev, a daemon started over ssh, and a session row in the list. Not checked: real `ssh-copy-id` with a password,
and a second physical machine.
