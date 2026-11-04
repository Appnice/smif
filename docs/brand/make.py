"""Builds the smif logo files in this folder from the site's fonts (fonttools + brotli needed).

python3 docs/brand/make.py  ->  mark.svg, logo-dark.svg (for dark backgrounds), logo-light.svg, icon.svg
(the macOS app icon; docs/brand/icns.sh turns it into internal/notify/smif.icns)
The mark: three stacked windows, the front one yellow with a status dot (many sessions, the one that needs
you in front). The wordmark is Geist Bold (OFL, site/fonts), turned into paths so the SVGs need no font.
"""
import os
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen

HERE = os.path.dirname(os.path.abspath(__file__))
FONT = os.path.join(HERE, "..", "..", "site", "fonts", "geist.woff2")
YELLOW, INK, CREAM, DIM, MUTE = "#fabd2f", "#1d2021", "#ebdbb2", "#504945", "#7c6f64"


def mark():
    return (
        f'<rect width="64" height="64" rx="15" fill="{INK}"/>'
        f'<rect x=".75" y=".75" width="62.5" height="62.5" rx="14.25" fill="none" stroke="{CREAM}" stroke-opacity=".16" stroke-width="1.5"/>'
        f'<rect x="20" y="10" width="34" height="26" rx="6" fill="none" stroke="{DIM}" stroke-width="3.5"/>'
        f'<rect x="15" y="18" width="34" height="26" rx="6" fill="{INK}" stroke="{MUTE}" stroke-width="3.5"/>'
        f'<rect x="10" y="26" width="34" height="28" rx="6" fill="{YELLOW}"/>'
        f'<circle cx="20" cy="40" r="4" fill="{INK}"/>'
    )


def word(text, size, x, base, color, tracking=-0.04):
    f = instantiateVariableFont(TTFont(FONT), {"wght": 700})
    cmap, gs, hmtx = f.getBestCmap(), f.getGlyphSet(), f["hmtx"]
    k = size / f["head"].unitsPerEm
    out, cx = [], 0.0
    for ch in text:
        gn = cmap[ord(ch)]
        pen = SVGPathPen(gs)
        gs[gn].draw(TransformPen(pen, (k, 0, 0, -k, x + cx, base)))
        out.append(pen.getCommands())
        cx += hmtx[gn][0] * k + size * tracking
    return f'<path fill="{color}" d="{" ".join(out)}"/>', cx


def write(name, body, w, h):
    with open(os.path.join(HERE, name), "w") as fh:
        fh.write(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w:.0f} {h}" width="{w:.0f}" height="{h}" role="img" aria-label="smif">{body}</svg>\n')


write("mark.svg", mark(), 64, 64)
# macOS app icon (notifications): the tile on the 1024 grid, 824 wide with the usual margin.
with open(os.path.join(HERE, "icon.svg"), "w") as fh:
    fh.write('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1024 1024" width="1024" height="1024">'
             f'<g transform="translate(100 100) scale(12.875)">{mark()}</g></svg>\n')
for name, color in (("logo-dark.svg", CREAM), ("logo-light.svg", INK)):
    p, wd = word("smif", 62, 80, 51, color)
    write(name, mark() + p, 80 + wd + 4, 64)
