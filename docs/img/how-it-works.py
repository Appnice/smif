#!/usr/bin/env python3
# Usage: python3 docs/img/how-it-works.py docs/img/how-it-works.svg
"""Generates docs/img/how-it-works.svg: an animated diagram of three
messages for one agent while you work in its session. Pure CSS keyframes
(transform + opacity only), so it runs inside <img> on GitHub, and the
un-animated base state is a complete static picture."""
import sys

T = 17.0  # loop length, seconds
W, H = 720, 800
BG, PANEL, LINE = "#0f1117", "#161b22", "#30363d"
FG, MUTED = "#d6d9de", "#7d8590"
BLUE, GREEN, ORANGE, PINK = "#7aa2f7", "#9ece6a", "#e0af68", "#ff7b8f"
COLS = [120, 360, 600]
CHIP_Y, CHIP_H, CHIP_W = 20, 84, 204
BOX_Y, BOX_H = 160, 186
RULE_Y = [236, 276, 316]
BIN_Y, BIN_H = 400, 116
WALL_Y = 556
SES_Y, SES_H = 584, 140
CAP_Y = 772

css, body = [], []
_n = [0]


def pct(t):
    return f"{max(0.0, min(100.0, t / T * 100)):.3f}%"


def anim(frames, ease="ease-in-out"):
    """frames: list of (t, css-declarations). Returns a class name."""
    _n[0] += 1
    name = f"a{_n[0]}"
    ks = "".join(f"{pct(t)}{{{d}}}" for t, d in frames)
    css.append(f"@keyframes {name}{{{ks}}}")
    css.append(f".{name}{{animation:{name} {T}s {ease} infinite both}}")
    return name


def show(*spans, fade=0.25, base=0):
    """Opacity 1 inside each (start, end) span, 0 outside."""
    fr = [(0, "opacity:0")]
    for a, b in spans:
        fr += [(a, "opacity:0"), (a + fade, "opacity:1"), (b - fade, "opacity:1"), (b, "opacity:0")]
    fr.append((T, "opacity:0"))
    return anim(sorted(fr, key=lambda x: x[0]), "linear")


def move(points):
    """points: (t, x, y, opacity)."""
    return anim([(t, f"transform:translate({x}px,{y}px);opacity:{o}") for t, x, y, o in points])


def text(x, y, s, size=22, fill=FG, weight=400, anchor="start", cls="", mono=False, opacity=None):
    fam = ' class="m"' if mono else ""
    c = f' class="{cls}"' if cls else ""
    op = f' opacity="{opacity}"' if opacity is not None else ""
    wrap = f"<g{c}>" if cls else ""
    end = "</g>" if cls else ""
    return (f'{wrap}<text x="{x}" y="{y}" font-size="{size}" fill="{fill}" font-weight="{weight}" '
            f'text-anchor="{anchor}"{op}{fam if mono else ""}>{s}</text>{end}')


def rect(x, y, w, h, fill=PANEL, stroke=LINE, rx=12, sw=2, cls="", extra=""):
    c = f' class="{cls}"' if cls else ""
    return f'<rect{c} x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" fill="{fill}" stroke="{stroke}" stroke-width="{sw}"{extra}/>'


def envelope(color):
    return (f'<rect x="-19" y="-13" width="38" height="26" rx="4" fill="{color}"/>'
            f'<path d="M-17 -10 L0 3 L17 -10" fill="none" stroke="{BG}" stroke-width="2.5" stroke-linejoin="round"/>')


# --- timeline -------------------------------------------------------------
# msg: column, color, appear, at-rule, leave-rule, at-bin
MSG = [
    dict(c=0, color=GREEN, t0=0.8, t1=1.8, t2=2.5, t3=3.2, tag="is v2.3 live?"),
    dict(c=1, color=ORANGE, t0=5.0, t1=6.0, t2=6.7, t3=7.4, tag="crash: NPE"),
    dict(c=2, color=PINK, t0=9.9, t1=10.9, t2=11.6, t3=12.3, tag="ship v2.4?"),
]
END = T - 0.5

# --- static parts -----------------------------------------------------------
body.append(rect(0, 0, W, H, fill=BG, stroke="none", rx=16, sw=0))

# routes (always visible: they carry the static picture)
for i, cx in enumerate(COLS):
    body.append(f'<path d="M{cx} {CHIP_Y + CHIP_H} V{BIN_Y}" stroke="{LINE}" stroke-width="2" stroke-dasharray="4 6"/>')

# senders
SENDERS = [("web", "another session"), ("crash-monitor", "a script"), ("CI", "a build job")]
for (name, sub), cx in zip(SENDERS, COLS):
    x = cx - CHIP_W / 2
    body.append(rect(x, CHIP_Y, CHIP_W, CHIP_H))
    body.append(text(cx, CHIP_Y + 37, name, 24, FG, 600, "middle"))
    body.append(text(cx, CHIP_Y + 66, sub, 20, MUTED, 400, "middle"))
# the asking session gets its answer
x = COLS[0] - CHIP_W / 2
body.append(f'<g class="{show((4.4, 7.0))}">' + rect(x, CHIP_Y, CHIP_W, CHIP_H, fill=PANEL, stroke=GREEN)
            + text(COLS[0], CHIP_Y + 37, "web", 24, FG, 600, "middle")
            + text(COLS[0], CHIP_Y + 66, "✓ yes, it's live", 20, GREEN, 600, "middle") + "</g>")

# smif box
BOX_IDX = len(body)
body.append(rect(20, BOX_Y, W - 40, BOX_H, fill="#131a2a", stroke=BLUE))
body.append(text(44, BOX_Y + 40, "smif", 26, BLUE, 700))
body.append(text(W - 44, BOX_Y + 40, "rules for shop · first match wins", 20, MUTED, 400, "end"))
RULES = [("ask", "store.status", "command", GREEN), ("event", "crash.*", "worker", ORANGE),
         ("any", "release.*", "you", PINK)]
for i, ((kind, intent, handler, col), y, m) in enumerate(zip(RULES, RULE_Y, MSG)):
    hl = show((m["t1"] - 0.1, m["t2"] + 0.5))
    body.append(f'<g class="{hl}">' + rect(36, y - 25, W - 72, 36, fill=col, stroke="none", rx=8, extra=' fill-opacity="0.16"') + "</g>")
    body.append(text(56, y, kind, 21, MUTED, 400, mono=True))
    body.append(text(156, y, intent, 21, FG, 400, mono=True))
    body.append(text(420, y, "→", 21, MUTED, 400, mono=True))
    body.append(text(460, y, handler, 21, col, 700, mono=True))

# handler bins
BINS = [("command", "a script · 0 tokens", GREEN), ("worker", "claude -p · read-only", ORANGE),
        ("you", "only you decide", PINK)]
for (name, sub, col), cx in zip(BINS, COLS):
    x = cx - CHIP_W / 2
    body.append(rect(x, BIN_Y, CHIP_W, BIN_H))
    body.append(text(cx, BIN_Y + 38, name, 24, col, 700, "middle"))
    body.append(text(cx, BIN_Y + 70, sub, 20, MUTED, 400, "middle"))

# bin states (animated overlays)
def bin_overlay(i, spans, lines, stroke):
    cx = COLS[i]
    x = cx - CHIP_W / 2
    name, _, col = BINS[i]
    g = rect(x, BIN_Y, CHIP_W, BIN_H, fill=PANEL, stroke=stroke, sw=3)
    g += text(cx, BIN_Y + 38, name, 24, col, 700, "middle")
    for k, (s, fill, w) in enumerate(lines):
        g += text(cx, BIN_Y + 70 + k * 28, s, 20, fill, w, "middle")
    return f'<g class="{show(*spans)}">{g}</g>'


m1, m2, m3 = MSG
body.append(bin_overlay(0, [(m1["t3"], 4.9)], [("answered in 25 ms", GREEN, 600), ("no LLM", MUTED, 400)], GREEN))
# worker: running, then done
wx = COLS[1] - CHIP_W / 2
run_span = (m2["t3"], 10.3)
body.append(bin_overlay(1, [run_span], [("Read CheckoutView.kt", FG, 400)], ORANGE))
body.append(f'<g class="{show((8.9, 10.3), fade=0.15)}">' + rect(wx + 6, BIN_Y + 52, CHIP_W - 12, 26, fill=PANEL, stroke="none", rx=4)
            + text(COLS[1], BIN_Y + 70, "Bash git log -5", 20, FG, 400, "middle") + "</g>")
# progress bar
pb = anim([(0, "transform:scaleX(0)"), (m2["t3"], "transform:scaleX(0)"), (10.1, "transform:scaleX(1)"),
           (T, "transform:scaleX(1)")], "linear")
body.append(f'<g class="{show(run_span)}">' + rect(wx + 20, BIN_Y + 90, CHIP_W - 40, 6, fill=LINE, stroke="none", rx=3)
            + f'<rect class="{pb} bar" x="{wx + 20}" y="{BIN_Y + 90}" width="{CHIP_W - 40}" height="6" rx="3" fill="{ORANGE}"/></g>')
body.append(bin_overlay(1, [(10.3, END)], [("✓ summary ready", ORANGE, 600), ("read it in /smif", MUTED, 400)], ORANGE))
body.append(bin_overlay(2, [(m3["t3"], END)], [("⚑ ship v2.4?", PINK, 600), ("waits in one queue", MUTED, 400)], PINK))

# the wall
body.append(f'<path d="M20 {WALL_Y} H{W - 20}" stroke="{BLUE}" stroke-width="2" stroke-dasharray="10 8" opacity="0.7"/>')
body.append(rect(W / 2 - 200, WALL_Y - 17, 400, 34, fill=BG, stroke="none", rx=0))
body.append(text(W / 2, WALL_Y + 7, "no message text crosses this line", 20, BLUE, 600, "middle"))

# your session
body.append(rect(20, SES_Y, W - 40, SES_H))
body.append(f'<path d="M20 {SES_Y + 40} H{W - 20}" stroke="{LINE}" stroke-width="2"/>')
for k, c in enumerate(["#ff5f57", "#febc2e", "#28c840"]):
    body.append(f'<circle cx="{46 + k * 22}" cy="{SES_Y + 20}" r="6" fill="{c}"/>')
body.append(text(W / 2, SES_Y + 27, "shop · your Claude Code session", 20, MUTED, 400, "middle"))
TYPED = "> refactor the checkout flow"
CH = 12.6  # monospace advance at 21px
tx, ty = 44, SES_Y + 80
body.append(text(tx, ty, TYPED, 21, FG, 400, mono=True))
n = len(TYPED)
t_type0, t_type1 = 0.6, 14.5
cover = anim([(0, "transform:translateX(0px)"), (t_type0, "transform:translateX(0px)"),
              (t_type1, f"transform:translateX({n * CH:.1f}px)"), (T, f"transform:translateX({n * CH:.1f}px)")],
             f"steps({n},end)")
css.append(f".{cover}{{animation-timing-function:steps({n},end)}}")
body.append(f'<clipPath id="ses"><rect x="22" y="{SES_Y + 42}" width="{W - 44}" height="{SES_H - 44}"/></clipPath>')
body.append(f'<g clip-path="url(#ses)"><rect class="{cover}" x="{tx}" y="{ty - 22}" width="{n * CH + 30:.1f}" height="30" fill="{PANEL}"/></g>')
caret = anim([(0, "transform:translateX(0px)"), (t_type0, "transform:translateX(0px)"),
              (t_type1, f"transform:translateX({n * CH:.1f}px)"), (T, f"transform:translateX({n * CH:.1f}px)")],
             f"steps({n},end)")
css.append(f"@keyframes blink{{0%,49%{{opacity:1}}50%,100%{{opacity:0}}}}")
body.append(f'<g class="{caret}"><rect class="blink" x="{tx + 2}" y="{ty - 19}" width="11" height="24" fill="{FG}"/></g>')
css.append(".blink{animation:blink 1s steps(1,end) infinite}")
# status line
sy = SES_Y + 122
body.append(text(44, sy, "mail:", 20, MUTED, 400, mono=True))
STATUS = [  # (spans, label, color) ; the base (static) one shows everything
    ([(0, m2["t3"]), ], "·", MUTED),
    ([(m2["t3"], 10.3)], "shop 1 running", ORANGE),
    ([(10.3, m3["t3"])], "·", MUTED),
    ([(m3["t3"], END)], "shop ⚑ 1 needs you", PINK),
]
for spans, label, col in STATUS:
    body.append(f'<g class="{show(*spans, fade=0.12)}">' + text(116, sy, label, 20, col, 600, mono=True) + "</g>")
body.append(text(W - 44, sy, "counts only", 20, MUTED, 400, "end"))

# moving envelopes
_before = len(body)
for m in MSG:
    cx = COLS[m["c"]]
    ry = RULE_Y[m["c"]] - 7
    pts = [(0, cx, 110, 0), (m["t0"], cx, 112, 0), (m["t0"] + 0.2, cx, 124, 1), (m["t1"], cx, ry, 1),
           (m["t2"], cx, ry, 1), (m["t3"], cx, BIN_Y - 14, 1), (m["t3"] + 0.2, cx, BIN_Y + 10, 0), (T, cx, BIN_Y + 10, 0)]
    cls = move(pts)
    tag = (f'<g transform="translate(28,6)"><rect x="0" y="-18" width="{len(m["tag"]) * 11 + 18}" height="28" rx="6" '
           f'fill="{BG}" stroke="{m["color"]}" stroke-width="1.5"/>'
           f'<text x="9" y="2" font-size="19" fill="{m["color"]}" font-weight="600">{m["tag"]}</text></g>')
    if m["c"] == 2:  # keep the tag inside the frame
        tag = tag.replace('translate(28,6)', f'translate({-28 - len(m["tag"]) * 11 - 18},6)')
    tag = f'<g class="{show((m["t0"], m["t1"] - 0.15), fade=0.15)}">{tag}</g>'
    body.append(f'<g class="{cls}">{envelope(m["color"])}{tag}</g>')
# the reply goes back up
cx = COLS[0] + 0
rp = move([(0, cx, BIN_Y - 14, 0), (3.5, cx, BIN_Y - 14, 0), (3.7, cx, BIN_Y - 24, 1), (4.4, cx, CHIP_Y + CHIP_H + 14, 1),
           (4.6, cx, CHIP_Y + CHIP_H - 6, 0), (T, cx, CHIP_Y + CHIP_H - 6, 0)])
body.append(f'<g class="{rp}">{envelope(GREEN)}</g>')

# envelopes go behind the smif box: they enter it and come out
_env = body[_before:]; del body[_before:]; body[BOX_IDX:BOX_IDX] = _env
# captions
CAPS = [
    ((0.0, 4.9), "An ask: a script answers it. No LLM."),
    ((4.9, 9.8), "A crash: a read-only worker looks into it."),
    ((9.8, 13.6), "A decision: it waits for you, in one queue."),
    ((13.6, T), "Your session saw none of it."),
]
for k, (span, s) in enumerate(CAPS):
    a, b = span
    cls = show((a, b), fade=0.3)
    body.append(f'<g class="{cls}{" last" if k == 3 else ""}">' + text(W / 2, CAP_Y, s, 26, FG, 600, "middle") + "</g>")

# base (no animation) state: the last frame is the static picture
style = f"""
text{{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif}}
.m{{font-family:"JetBrains Mono","SF Mono",Menlo,Consolas,monospace}}
.bar{{transform-box:fill-box;transform-origin:left center}}
{''.join(css)}
@media (prefers-reduced-motion:reduce){{*{{animation:none!important}}}}
"""

out = (f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}" role="img" '
       f'aria-label="Three messages arrive for the shop agent while you work in its session: an ask that a script answers, '
       f'a crash that a read-only worker looks into, and a decision that waits for you. Your session only sees counts.">'
       f"<style>{style}</style>" + "\n".join(body) + "</svg>\n")
open(sys.argv[1], "w").write(out)
