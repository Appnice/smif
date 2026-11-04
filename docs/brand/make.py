"""Builds the smif logo files in this folder from the site's fonts (fonttools + brotli needed).

python3 docs/brand/make.py  ->  mark.svg, logo-dark.svg (for dark backgrounds), logo-light.svg
The wordmark is Bricolage Grotesque ExtraBold (OFL, site/fonts), turned into paths so the SVGs need no font.
"""
import os
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen

HERE = os.path.dirname(os.path.abspath(__file__))
FONT = os.path.join(HERE, "..", "..", "site", "fonts", "bricolage.woff2")
YELLOW, INK, CREAM = "#fabd2f", "#1d2021", "#ebdbb2"

# The mark: the session list itself. The top row is selected, as in the UI: the session that needs you.
# Below it a working triangle and an idle ring, each with its row.
def mark(x=0, y=0, s=1.0):
    g = f'<g transform="translate({x} {y}) scale({s})">'
    g += f'<rect width="64" height="64" rx="15" fill="{YELLOW}"/>'
    g += f'<rect x="8" y="12.5" width="48" height="15" rx="5" fill="{INK}"/>'
    g += f'<circle cx="17" cy="20" r="3.8" fill="{YELLOW}"/>'
    g += f'<rect x="26" y="17.25" width="23" height="5.5" rx="2.75" fill="{YELLOW}"/>'
    g += f'<path d="M13.6 28.9 L21.2 33 L13.6 37.1 Z" fill="{INK}" stroke="{INK}" stroke-width="1.1" stroke-linejoin="round"/>'
    g += f'<rect x="26" y="30.25" width="18" height="5.5" rx="2.75" fill="{INK}"/>'
    g += f'<circle cx="17" cy="46" r="3.3" fill="none" stroke="{INK}" stroke-width="2.3"/>'
    g += f'<rect x="26" y="43.25" width="21" height="5.5" rx="2.75" fill="{INK}" opacity=".45"/>'
    return g + "</g>"

def word(text, size, x, base, color):
    f = TTFont(FONT)
    f = instantiateVariableFont(f, {"wght": 800})
    cmap, gs, hmtx = f.getBestCmap(), f.getGlyphSet(), f["hmtx"]
    upm = f["head"].unitsPerEm
    k = size / upm
    out, cx = [], 0.0
    for ch in text:
        gn = cmap[ord(ch)]
        pen = SVGPathPen(gs)
        gs[gn].draw(TransformPen(pen, (k, 0, 0, -k, x + cx, base)))
        out.append(pen.getCommands())
        cx += hmtx[gn][0] * k + size * 0.012
    return f'<path fill="{color}" d="{" ".join(out)}"/>', cx

def write(name, body, w, h):
    with open(os.path.join(HERE, name), "w") as fh:
        fh.write(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w:.0f} {h}" width="{w:.0f}" height="{h}" role="img" aria-label="smif">{body}</svg>\n')

write("mark.svg", mark(), 64, 64)
for name, color in (("logo-dark.svg", CREAM), ("logo-light.svg", INK)):
    p, wd = word("smif", 66, 82, 50, color)
    write(name, mark() + p, 82 + wd + 2, 64)
