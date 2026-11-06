# ADR-0024: The Machines screen

Status: accepted, 2026-11-06.

## Decision

Adding a machine is one setup in `internal/machines`, with two faces: the Machines screen (`m`, or `smif remote add`) and
`smif remote add <host>`.

- **One screen, not a wizard.** It lists this machine, the added ones and the found ones. It stays useful after the setup:
  it shows who is away, and removes a machine. Each setup runs in its row, so two can run at once.
- **Finding.** `~/.ssh/config` (git hosts left out), `tailscale status --json`, and `dns-sd` or `avahi-browse`. A typed
  address covers the rest.
- **A key of its own.** smif never copies your key and never depends on your ssh agent, which a service does not have. It
  makes `~/.ssh/smif_ed25519`, with no passphrase, and adds this line on the other machine:
  `restrict,command="PATH=<dir>:$PATH <smif> remote-read" ssh-ed25519 ... smif-daemon`. Whatever a holder of the key asks
  for, that machine runs `smif remote-read`, which gives the session list or one conversation by id, with no shell, port
  forward or terminal, and no way to type. A stolen key can read your sessions there and do nothing else.
- **The password.** It stays in the input box until enter, then goes to `ssh` or `sudo`. To ssh it travels through
  `SSH_ASKPASS` and a socket in a 0700 folder, so it is not in a file, an argument or the environment. To sudo it travels on
  stdin. One shared connection means you type it once.
- **Commands are constants.** Only the host name comes from you, and it passes config validation. A key line and a password
  go on stdin.
- **Writing is a choice for each machine, off by default.** The reason ADR-0023 gave was weak: the gates run on the
  machine with the session, so that machine's daemon checks each line as if you typed it there. The real cost is the key:
  with no passphrase, a key that can type can run commands through Claude. So `--write` sits in that machine's
  `authorized_keys` line, not in a request, turning it on or off takes a login there, and the text goes on stdin. A
  permission prompt stays out of reach. sshd reads that line at login, so the setup closes the daemon's shared connection:
  otherwise a machine turned read only would keep writing until the connection ended.
- **New sessions there start in tmux.** No program can type into a plain terminal window on Linux, so `n` on a session of
  a writable machine starts claude in a tmux that smif can reach, and starts tmux if none runs.
- **Rows do not travel twice.** A host's list also holds the machines it watches, so the poll keeps only its own rows. Two
  machines can watch each other. A target that is an address of this machine is refused, and Colima and Lima VMs stay out.
- **ssh off.** smif cannot turn it on from outside, so the screen shows the command and tries again every 3 s.
- **Linger.** On Linux the daemon's socket is in `/run/user/<uid>`, which goes away with the last login, and the old daemon
  keeps its lock. So the setup turns linger on, without sudo if it can, and you can skip that.

## Why not more

- **No host key prompt.** An unknown key is accepted once (`accept-new`), and a changed key stops the setup. Showing a
  fingerprint nobody compares adds a step and no safety. Anyone who can spoof a host on your network on the first login
  could take the password, as with a first `ssh` where you answer yes.
- **No removal on the other side.** `remote rm` leaves the line in `authorized_keys`: the key can only list sessions, and
  taking it out needs a login smif may not have.
- **No fork or files on another machine.** A fork would run there unasked, and a part is a path on this machine.

## Checked

`TestRemoteAdd` covers the whole CLI path with a stand-in ssh that calls the real askpass helper. On real ssh, from this Mac to
Debian containers with a real `sshd`, password login and `sudo` (a stand-in `loginctl`, no systemd there):

- both the CLI and the screen, driven in tmux, with a wrong and a right password and sudo;
- the v0.2.0 install from smif.dev, the daemon over ssh, and a session row through the restricted key;
- that key given `id` returned the session list, and a port forward through it was refused;
- a conversation read from the other machine through that key, and a line typed there;
- a machine with ssh off, where the screen went on by itself when `sshd` started;
- no password in the files of `~/.ssh` or the config.

Not checked: a second physical machine, a real `loginctl`, `dns-sd` finding a real machine, and Tailscale.
