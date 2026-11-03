#!/usr/bin/env python3
# Usage: python3 docs/img/home.py internal/ui/testdata/home_session_busy_120x40.golden docs/img/home.svg
"""Draws docs/img/home.svg from a UI golden file: the home screen as the
tests render it (plain text), coloured the way the terminal colours it.
Regenerate after `go test ./internal/ui -update` changes the golden."""
import re
import sys
from xml.sax.saxutils import escape

# Gruvbox dark, as internal/ui/theme.go.
BG, FG, MUTED, FRAME = "#282828", "#ebdbb2", "#a89984", "#504945"
AMBER, CYAN, PALE, GREEN, RED, BLUE = "#fe8019", "#8ec07c", "#fabd2f", "#b8bb26", "#fb4934", "#83a598"
CW, LH, PAD = 7.8, 18, 20  # cell width, line height, padding (px)

# (pattern, colour, bold): the first match wins for each character.
RULES = [
    (r" mailroom ", BG, True),
    (r"[╭╮╰╯│─┼]+", FRAME, False),
    (r"! WAITING \S+|! \d+[smhd]\b|\d+ waiting|needs you(?= *│)|silent \d+[smhd]", AMBER, True),
    (r"▶ WORKING \S+|▶ \d+\S*|\d+ working|◇ fork", CYAN, True),
    (r"(?<=│ )Bash|(?<=  )Bash(?=  )|(?<=│ )Edit|(?<=│ )Read", CYAN, True),
    (r"● NEW ANSWER|● new|\d+ new\b", PALE, True),
    (r"● live|◆ answer|✓", GREEN, True),
    (r"⚑ \d+ asks you|✗ \d+ dead|⚑|✗|NEEDS YOU|DEAD", RED, True),
    (r"› you → fork|› you", BLUE, False),
    (r"SESSIONS · \d+|RUNNING · \d+|QUEUED · \d+|DONE TODAY · \d+", FG, True),
    (r"○ idle|\d+ quiet|≈ \$[\d.]+ at API prices today|working on · .*|now · .*|steps so far|"
     r"a copy of this session.*|\d+ tools|\d+[smhd]$|~?/r/\S+|ttys\d+", MUTED, False),
    (r"(?<=   )\d\d:\d\d  \S+  \S+", MUTED, False),
]


def colours(line):
    col = [None] * len(line)
    for pat, colour, bold in RULES:
        for m in re.finditer(pat, line):
            for i in range(m.start(), m.end()):
                if col[i] is None:
                    col[i] = (colour, bold)
    return [c or (FG, False) for c in col]


def main(src, dst):
    lines = open(src, encoding="utf-8").read().rstrip("\n").split("\n")
    # The list's empty tail adds nothing: keep the top, the box's bottom
    # edge and the key line.
    bottom = max(i for i, l in enumerate(lines) if l.startswith("╰"))
    lines = lines[:27] + [lines[bottom], lines[-1]]
    width = max(len(l) for l in lines)
    w, h = int(PAD * 2 + width * CW), PAD * 2 + len(lines) * LH
    out = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{w}" height="{h}" viewBox="0 0 {w} {h}" role="img" '
           'aria-label="The mailroom home screen: sessions with their state, a working session with the step that runs now, '
           'its steps so far and a fork\'s answer">',
           f'<rect width="100%" height="100%" rx="10" fill="{BG}"/>',
           '<style>text{font-family:"JetBrains Mono","SF Mono",Menlo,Consolas,monospace;font-size:13px;white-space:pre}</style>']
    for row, line in enumerate(lines):
        y = PAD + row * LH + 13
        m = line.find(" mailroom ")
        if row == 0 and m >= 0:
            out.append(f'<rect x="{PAD + m * CW:.1f}" y="{y - 12}" width="{10 * CW:.1f}" height="17" rx="2" fill="{PALE}"/>')
        spans, cols = [], colours(line)
        i = 0
        while i < len(line):
            j = i
            while j < len(line) and cols[j] == cols[i]:
                j += 1
            text = line[i:j]
            if text.strip():
                c, b = cols[i]
                weight = ' font-weight="700"' if b else ""
                spans.append(f'<tspan x="{PAD + i * CW:.1f}" fill="{c}"{weight}>{escape(text)}</tspan>')
            i = j
        if spans:
            out.append(f'<text y="{y}" xml:space="preserve">{"".join(spans)}</text>')
    out.append("</svg>")
    open(dst, "w", encoding="utf-8").write("\n".join(out) + "\n")


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
