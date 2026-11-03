#!/usr/bin/env python3
"""Stage a believable set of Claude Code sessions for the site's recordings.

Nothing here touches the real ~/.claude or the real Smif install:

- every path lives under ROOT (default /tmp/smifdemo): a fake HOME with
  ~/.claude/sessions/<pid>.json, transcripts and a Smif config;
- each "session" is a copy of /bin/sleep named claude, running in its own
  window of a private tmux server (TMUX_TMPDIR=ROOT/tmux), so Smif sees a
  live claude process in tmux and can type into it;
- a private Smif daemon runs on SMIF_SOCKET=ROOT/s.sock.

When Smif types a prompt into a pane, the director notices it and plays the
session's work: busy, a few tool calls, then an answer.

Usage: director.py up [--root DIR]  (stays in the foreground; Ctrl+C tears down)
       director.py env [--root DIR] (prints the env for a VHS tape)
"""

import argparse
import json
import os
import shutil
import signal
import subprocess
import sys
import time
import uuid
from datetime import datetime, timedelta, timezone

SMIF = shutil.which("smif") or os.path.expanduser("~/.local/bin/smif")
TMUX = shutil.which("tmux") or "/opt/homebrew/bin/tmux"


def iso(t):
    return t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"


def now():
    return datetime.now(timezone.utc)


class Session:
    def __init__(self, root, name, folder):
        self.root, self.name, self.folder = root, name, folder
        self.sid = str(uuid.uuid4())
        self.cwd = os.path.join(root, "home", "code", folder)
        self.pid = 0
        self.tty = ""
        self.status = "idle"
        self.started = now() - timedelta(hours=2)
        self.n = 0
        self.typed = ""
        proj = self.cwd.replace("/", "-").replace(".", "-")
        self.transcript = os.path.join(root, "home", ".claude", "projects", proj, self.sid + ".jsonl")

    # transcript records
    def _write(self, rec, at=None):
        os.makedirs(os.path.dirname(self.transcript), exist_ok=True)
        rec = {"type": rec.pop("type"), "timestamp": iso(at or now()), "sessionId": self.sid, "cwd": self.cwd, **rec}
        with open(self.transcript, "a") as f:
            f.write(json.dumps(rec) + "\n")

    def prompt(self, text, at=None):
        self._write({"type": "user", "message": {"role": "user", "content": text}}, at)

    def tool(self, name, inp, note=None, at=None):
        self.n += 1
        tid = f"toolu_{self.sid[:8]}_{self.n}"
        content = ([{"type": "text", "text": note}] if note else []) + [{"type": "tool_use", "id": tid, "name": name, "input": inp}]
        self._write({"type": "assistant", "message": {"role": "assistant", "content": content}}, at)
        return tid

    def result(self, tid, out="ok", at=None):
        self._write({"type": "user", "message": {"role": "user", "content": [{"type": "tool_result", "tool_use_id": tid, "content": out}]}}, at)

    def answer(self, text, at=None):
        self._write({"type": "assistant", "message": {"role": "assistant", "content": [{"type": "text", "text": text}]}}, at)

    def set_status(self, status, at=None):
        self.status = status
        t = at or now()
        doc = {"pid": self.pid, "sessionId": self.sid, "cwd": self.cwd, "kind": "interactive", "name": self.name,
               "status": status, "startedAt": int(self.started.timestamp() * 1000), "statusUpdatedAt": int(t.timestamp() * 1000)}
        p = os.path.join(self.root, "home", ".claude", "sessions", f"{self.pid}.json")
        tmp = p + ".tmp"
        with open(tmp, "w") as f:
            json.dump(doc, f)
        os.replace(tmp, p)

    def age(self, minutes):
        """Make the transcript look last written `minutes` ago."""
        t = time.time() - minutes * 60
        os.utime(self.transcript, (t, t))


def env(root):
    home = os.path.join(root, "home")
    return {
        "HOME": home,
        "CLAUDE_CONFIG_DIR": os.path.join(home, ".claude"),
        "SMIF_SOCKET": os.path.join(root, "s.sock"),
        "TMUX_TMPDIR": os.path.join(root, "tmux"),
        "SMIF_NO_AUTOSTART": "1",
        # The daemon asks the real claude for --version only; the tapes never
        # press n (a new session would start the real claude).
        "PATH": os.path.dirname(SMIF) + ":/opt/homebrew/bin:/usr/bin:/bin",
        "TERM": "xterm-256color",
        "COLORTERM": "truecolor",
        "LANG": "en_US.UTF-8",
    }


CONFIG = """\
# Smif config for the site's recordings (director.py)
[daemon]
session_notify = "none"
remind_waiting = "off"
alert_silent = "off"
alert_long_turn = "off"

[prompts]
tests = "run the tests and report what failed"
review = "review your last change for bugs, then list them"
ship = "commit with a clear message and open a PR"
"""


def tmux(e, *args, check=True):
    return subprocess.run([TMUX, *args], env=e, check=check, capture_output=True, text=True).stdout


def ago(m):
    return now() - timedelta(minutes=m)


def fake_claude():
    """Build the stand-in claude once, with the real HOME (Go's caches live there)."""
    here = os.path.dirname(os.path.abspath(__file__))
    out = os.path.join(here, "out", "claude")
    if not os.path.exists(out):
        subprocess.run(["go", "build", "-o", out, os.path.join(here, "fakeclaude", "main.go")], check=True)
    return out


def stage(root, e):
    """Create the sessions and their history. Returns them by name."""
    folders = [
        ("payments", "payments-api"),
        ("webhooks", "payments-api"),
        ("storefront", "storefront"),
        ("handbook", "handbook"),
        ("infra", "infra"),
        ("ios", "ios-app"),
        ("billing", "billing"),
        ("search", "search"),
    ]
    claude = os.path.join(root, "bin", "claude")
    os.makedirs(os.path.dirname(claude), exist_ok=True)
    shutil.copy(fake_claude(), claude)
    tmux(e, "-f", "/dev/null", "new-session", "-d", "-s", "work", "-x", "200", "-y", "50", "-n", "shell", "/bin/sleep 999999")
    tmux(e, "set-option", "-g", "automatic-rename", "off")
    tmux(e, "set-option", "-g", "allow-rename", "off")
    out = {}
    for name, folder in folders:
        s = Session(root, name, folder)
        os.makedirs(s.cwd, exist_ok=True)
        subprocess.run(["git", "init", "-q", s.cwd], check=True)
        tmux(e, "new-window", "-d", "-t", "work", "-n", name, "-c", s.cwd, f"exec {claude} 999999")
        out[name] = s
    panes = tmux(e, "list-panes", "-a", "-F", "#{window_name} #{pane_pid} #{pane_tty}")
    for line in panes.splitlines():
        w, pid, tty = line.split()
        if w in out:
            out[w].pid, out[w].tty = int(pid), tty

    s = out["payments"]
    s.prompt("make the refund endpoint idempotent and add a test", ago(9))
    for m, (tool, inp) in zip([8.5, 7, 5, 3, 1.2], [
        ("Grep", {"pattern": "func Refund"}), ("Read", {"file_path": s.cwd + "/internal/refund/refund.go"}),
        ("Edit", {"file_path": s.cwd + "/internal/refund/refund.go"}), ("Write", {"file_path": s.cwd + "/internal/refund/refund_test.go"}),
    ] + [("Bash", {"command": "go test ./... -race"})]):
        t = s.tool(tool, inp, at=ago(m))
        if tool != "Bash":
            s.result(t, at=ago(m - 0.2))
    s.set_status("busy", ago(9))

    s = out["webhooks"]
    s.prompt("retry failed webhooks with backoff, max 5 tries", ago(95))
    s.result(s.tool("Edit", {"file_path": s.cwd + "/internal/webhook/retry.go"}, at=ago(93)), at=ago(93))
    s.answer("Failed webhooks now retry 5 times with exponential backoff (1s to 16s) and land in the dead-letter table after that.", ago(88))
    s.set_status("idle", ago(88))

    s = out["storefront"]
    s.prompt("bump the checkout flow to the new API and push the branch", ago(14))
    for m, (tool, inp) in zip([13, 11, 6, 4], [
        ("Read", {"file_path": s.cwd + "/src/checkout/api.ts"}), ("Edit", {"file_path": s.cwd + "/src/checkout/api.ts"}),
        ("Bash", {"command": "npm test -- checkout"}), ("Bash", {"command": "git commit -am 'checkout: v2 API'"}),
    ]):
        s.result(s.tool(tool, inp, at=ago(m)), at=ago(m - 0.3))
    s.tool("Bash", {"command": "git push origin feat/checkout-v2"}, at=ago(3))
    s.set_status("waiting", ago(3))

    s = out["handbook"]
    s.prompt("rewrite the onboarding guide for new engineers, shorter", ago(12))
    s.result(s.tool("Read", {"file_path": s.cwd + "/onboarding.md"}, at=ago(11)), at=ago(11))
    s.result(s.tool("Edit", {"file_path": s.cwd + "/onboarding.md"}, at=ago(8)), at=ago(8))
    s.answer("Rewrote onboarding.md: 1,900 words down to 640. Day one is now three steps (laptop, access, first PR); "
             "the rest moved to a linked FAQ. I kept every command and checked each link.", ago(2))
    s.set_status("idle", ago(2))

    s = out["infra"]
    s.prompt("plan the move of the staging database to the new VPC", ago(31))
    s.result(s.tool("Read", {"file_path": s.cwd + "/staging/db.tf"}, at=ago(30)), at=ago(30))
    s.result(s.tool("Edit", {"file_path": s.cwd + "/staging/db.tf"}, at=ago(26)), at=ago(26))
    s.tool("Bash", {"command": "terraform plan -out=tfplan"}, at=ago(24))
    s.set_status("busy", ago(31))
    s.age(7)

    s = out["ios"]
    s.prompt("why does the settings screen flicker on iPad?", ago(70))
    s.result(s.tool("Read", {"file_path": s.cwd + "/Settings/SettingsView.swift"}, at=ago(69)), at=ago(69))
    s.answer("The list rebuilds on every timer tick: `@State var now` is read in the body. "
             "I moved the clock into its own small view, so only the label redraws.", ago(64))
    s.set_status("idle", ago(64))

    s = out["billing"]
    s.prompt("add VAT to invoices for EU customers", ago(6))
    s.result(s.tool("Read", {"file_path": s.cwd + "/invoice/total.go"}, at=ago(5.5)), at=ago(5.5))
    s.tool("AskUserQuestion", {"questions": [{"question": "Round VAT per line or once per invoice?"}]}, at=ago(1))
    s.set_status("waiting", ago(1))

    s = out["search"]
    s.prompt("rank exact title matches above fuzzy ones", ago(4))
    s.result(s.tool("Grep", {"pattern": "score :="}, at=ago(3.5)), at=ago(3.5))
    s.tool("Read", {"file_path": s.cwd + "/internal/index/ranker.go"}, at=ago(0.3))
    s.set_status("busy", ago(4))
    # What this viewer has seen: everything up to now, except the handbook answer.
    seen = {x.sid: iso(now()) for x in out.values()}
    seen[out["handbook"].sid] = iso(ago(60))
    data = os.path.join(root, "home", "Library", "Application Support", "smif")
    os.makedirs(data, mode=0o700, exist_ok=True)
    with open(os.path.join(data, "ui-seen.json"), "w") as f:
        json.dump(seen, f)
    return out


def play(sessions, e, stop):
    """The live part: scripted events, plus work for every prompt Smif types."""
    # The script's clock starts when the tape touches ROOT/go (just before
    # the recording shows), so the events land on camera.
    go = os.path.join(sessions["payments"].root, "go")
    t0 = None
    script = [
        (4.5, lambda s: finish_payments(s["payments"])),
        (9, lambda s: step(s["search"], "Edit", {"file_path": s["search"].cwd + "/internal/index/ranker.go"})),
        (12.5, lambda s: step(s["search"], "Bash", {"command": "go test ./internal/index"})),
        (16, lambda s: finish(s["search"], "Exact title matches now score 2x and sort first; fuzzy matches keep their order. "
                                           "Added 6 ranking tests, all pass.")),
    ]
    done = set()
    jobs = []  # (at, fn)
    while not stop["now"]:
        if t0 is None and os.path.exists(go):
            t0 = time.time()
        el = time.time() - t0 if t0 else -1
        for i, (at, fn) in enumerate(script):
            if i not in done and el >= at >= 0:
                done.add(i)
                fn(sessions)
        for s in sessions.values():
            text = typed(e, s)
            if text and text != s.typed:
                s.typed = text
                jobs += work_for(s, text)
        for j in [j for j in jobs if time.time() >= j[0]]:
            jobs.remove(j)
            j[1]()
        time.sleep(0.25)


def step(s, tool, inp):
    s.tool(tool, inp)


def finish(s, text):
    for i in range(1, s.n + 1):
        s.result(f"toolu_{s.sid[:8]}_{i}")
    s.answer(text)
    s.set_status("idle")


def finish_payments(s):
    finish(s, "Refunds are idempotent now: a repeat with the same key returns the first result. "
              "All 214 tests pass with -race; the new TestRefundTwice covers the repeat.")


def typed(e, s):
    """The last line typed into the session's pane (sleep ignores it, the tty echoes it)."""
    out = tmux(e, "capture-pane", "-p", "-t", f"work:{s.name}", check=False)
    lines = [l.strip() for l in out.splitlines() if l.strip()]
    return lines[-1] if lines else ""


PLAYS = {
    "review your last change for bugs, then list them": [
        (0.6, "Bash", {"command": "git diff HEAD~1 --stat"}),
        (2.2, "Read", {"file_path": "Settings/SettingsView.swift"}),
        (4.0, "Grep", {"pattern": "Timer.publish"}),
        (6.5, None, "One bug: the timer in ClockLabel is never cancelled, so each visit to Settings adds another one. "
                    "Fix: store the AnyCancellable and cancel it in onDisappear. Nothing else found."),
    ],
    "also cover a refund of zero": [
        (0.5, "Read", {"file_path": "internal/refund/refund_test.go"}),
        (2.0, "Edit", {"file_path": "internal/refund/refund_test.go"}),
        (3.6, "Bash", {"command": "go test ./internal/refund -run TestRefund -v"}),
        (6.4, None, "Added TestRefundZero: a zero amount is refused with ErrNothingToRefund before any call to the "
                    "provider. 3 refund tests, all pass."),
    ],
    "run the tests and report what failed": [
        (0.6, "Bash", {"command": "xcodebuild test -scheme App -destination 'name=iPad Air' | xcpretty"}),
        (3.4, "Read", {"file_path": "AppTests/SettingsViewTests.swift"}),
        (5.0, "Bash", {"command": "xcodebuild test -only-testing:AppTests/SettingsViewTests"}),
        (7.6, None, "141 of 142 tests pass. SettingsViewTests.testClockUpdates fails: it waits for the whole list to "
                    "redraw, and after the flicker fix only the clock label does. The code is right; the test needs "
                    "the new rule. Shall I update it?"),
    ],
}
DEFAULT_PLAY = [
    (0.6, "Read", {"file_path": "README.md"}),
    (2.5, "Bash", {"command": "git status --short"}),
    (5.0, None, "Done. Nothing else changed."),
]


def work_for(s, text):
    t = time.time()
    s.prompt(text)
    s.set_status("busy")
    jobs = []
    for at, tool, arg in PLAYS.get(text, DEFAULT_PLAY):
        if tool is None:
            jobs.append((t + at, lambda s=s, a=arg: finish(s, a)))
        else:
            jobs.append((t + at, lambda s=s, tl=tool, a=arg: s.tool(tl, a)))
    return jobs


def up(root):
    if os.path.exists(root):
        teardown(root)
        shutil.rmtree(root, ignore_errors=True)
    e = {**os.environ, **env(root)}
    for d in ["home/.claude/sessions", "home/.config/smif", "tmux"]:
        os.makedirs(os.path.join(root, d), exist_ok=True)
    os.chmod(os.path.join(root, "tmux"), 0o700)
    with open(os.path.join(root, "home/.config/smif/config.toml"), "w") as f:
        f.write(CONFIG.replace("{root}", root))
    sessions = stage(root, e)
    log = open(os.path.join(root, "daemon.log"), "w")
    d = subprocess.Popen([SMIF, "daemon"], env=e, stdout=log, stderr=log, start_new_session=True)
    with open(os.path.join(root, "daemon.pid"), "w") as f:
        f.write(str(d.pid))
    for _ in range(50):
        if os.path.exists(e["SMIF_SOCKET"]):
            break
        time.sleep(0.1)
    print("ready", flush=True)
    stop = {"now": False}
    signal.signal(signal.SIGTERM, lambda *a: stop.update(now=True))
    signal.signal(signal.SIGINT, lambda *a: stop.update(now=True))
    try:
        play(sessions, e, stop)
    finally:
        teardown(root)


def teardown(root):
    e = {**os.environ, **env(root)}
    pidf = os.path.join(root, "daemon.pid")
    if os.path.exists(pidf):
        try:
            os.kill(int(open(pidf).read()), signal.SIGTERM)
        except (ProcessLookupError, ValueError):
            pass
        os.remove(pidf)
    subprocess.run([TMUX, "kill-server"], env=e, capture_output=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("cmd", choices=["up", "env", "down"])
    ap.add_argument("--root", default="/tmp/smifdemo")
    a = ap.parse_args()
    if a.cmd == "env":
        for k, v in env(a.root).items():
            if k != "PATH":  # the tape's shell keeps the caller's PATH
                print(f'Env {k} "{v}"')
    elif a.cmd == "down":
        teardown(a.root)
    else:
        up(a.root)


if __name__ == "__main__":
    main()
