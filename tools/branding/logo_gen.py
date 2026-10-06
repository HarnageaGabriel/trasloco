"""Generate trasloco logo SVGs (clean isometric box + pipeline + arrow).

Usage: python tools/branding/logo_gen.py docs/assets
Also writes preview.html (do not commit it).
"""
import math, os, sys

OUT = sys.argv[1]
os.makedirs(OUT, exist_ok=True)

NAVY = "#1F2733"
ORANGE = "#F27A2E"
PAPER = "#F7F5F0"

L = 44.0                      # cube edge length
DX = L * math.cos(math.radians(30))
DY = L * 0.5
GAP = 3.6                     # width of the separation cuts


def cube(cx, top):
    A = (cx, top)
    B = (cx - DX, top + DY)
    D = (cx + DX, top + DY)
    E = (cx, top + 2 * DY)
    Bb = (B[0], B[1] + L)
    Eb = (E[0], E[1] + L)
    Db = (D[0], D[1] + L)
    return A, B, D, E, Bb, Eb, Db


def pts(*ps):
    return " ".join(f"{x:.2f},{y:.2f}" for x, y in ps)


def mid(p, q):
    return ((p[0] + q[0]) / 2, (p[1] + q[1]) / 2)


def box_group(cx, top, fill, mask_id, with_seam=True, cutout=""):
    A, B, D, E, Bb, Eb, Db = cube(cx, top)
    s1, s2 = mid(A, B), mid(D, E)
    cuts = [
        f'<polyline points="{pts(B, E, D)}"/>',
        f'<line x1="{E[0]:.2f}" y1="{E[1]:.2f}" x2="{Eb[0]:.2f}" y2="{Eb[1]:.2f}"/>',
    ]
    if with_seam:
        cuts.append(f'<line x1="{s1[0]:.2f}" y1="{s1[1]:.2f}" x2="{s2[0]:.2f}" y2="{s2[1]:.2f}"/>')
    mask = (
        f'<mask id="{mask_id}" maskUnits="userSpaceOnUse">'
        f'<rect x="-50" y="-50" width="400" height="300" fill="#fff"/>'
        f'<g fill="none" stroke="#000" stroke-width="{GAP}" stroke-linecap="square">{"".join(cuts)}</g>'
        f'<g fill="#000" stroke="#000" stroke-width="{GAP * 2}" stroke-linejoin="round">{cutout}</g>'
        f"</mask>"
    )
    body = (
        f'<g fill="{fill}" mask="url(#{mask_id})">'
        f'<polygon points="{pts(A, D, E, B)}"/>'
        f'<polygon points="{pts(B, E, Eb, Bb)}"/>'
        f'<polygon points="{pts(E, D, Db, Eb)}"/>'
        f"</g>"
    )
    return mask, body, (A, B, D, E, Bb, Eb, Db)


def arrow(x0, x1, y, shaft=8.0, head=24.0, fill=ORANGE):
    h = shaft / 2
    hx = x1 - head * 0.75
    p = pts((x0, y - h), (hx, y - h), (hx, y - head / 2), (x1, y), (hx, y + head / 2), (hx, y + h), (x0, y + h))
    return f'<polygon fill="{fill}" points="{p}"/>'


def pipelines(xs, xe, ys, fill, r=5.0, w=3.6):
    out = []
    for y in ys:
        out.append(f'<circle cx="{xs:.2f}" cy="{y:.2f}" r="{r}" fill="{fill}"/>')
        out.append(f'<rect x="{xs:.2f}" y="{y - w / 2:.2f}" width="{xe - xs:.2f}" height="{w}" fill="{fill}"/>')
    return "".join(out)


def svg(w, h, inner, title):
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} {h}" width="{w * 2}" height="{h * 2}" '
        f'role="img" aria-label="{title}"><title>{title}</title>{inner}</svg>\n'
    )


def mark(fill, suffix):
    # full mark: pipelines entering from the left, arrow leaving to the right
    arr = arrow(92 + GAP / 2, 150, 77)
    mask, body, (A, B, D, E, Bb, Eb, Db) = box_group(92, 18, fill, f"cut{suffix}", cutout=arr)
    lines = pipelines(12, B[0] + 0.5, [70, 84], fill)
    return svg(160, 128, f"<defs>{mask}</defs>{lines}{body}{arr}", "trasloco")


def icon(fill, suffix):
    # simplified icon, legible at 16-32 px: box + arrow only, no seam
    arr = arrow(56 + GAP / 2, 122, 77, shaft=10, head=30)
    mask, body, (A, B, D, E, Bb, Eb, Db) = box_group(56, 18, fill, f"cuti{suffix}", with_seam=False, cutout=arr)
    return svg(128, 128, f"<defs>{mask}</defs>{body}{arr}", "trasloco")


def logo(fill, text_fill, suffix):
    arr = arrow(56 + GAP / 2, 122, 77, shaft=10, head=30)
    mask, body, (A, B, D, E, Bb, Eb, Db) = box_group(56, 18, fill, f"cutl{suffix}", with_seam=False, cutout=arr)
    text = (
        f'<text x="140" y="88" fill="{text_fill}" font-size="52" font-weight="600" letter-spacing="-1" '
        f"font-family=\"'JetBrains Mono','SFMono-Regular',Menlo,Consolas,'Liberation Mono',monospace\">trasloco</text>"
    )
    return svg(420, 128, f"<defs>{mask}</defs>{body}{arr}{text}", "trasloco")


files = {
    "trasloco-mark.svg": mark(NAVY, "l"),
    "trasloco-mark-dark.svg": mark(PAPER, "d"),
    "trasloco-icon.svg": icon(NAVY, "l"),
    "trasloco-icon-dark.svg": icon(PAPER, "d"),
    "trasloco-logo.svg": logo(NAVY, NAVY, "l"),
    "trasloco-logo-dark.svg": logo(PAPER, PAPER, "d"),
}
for name, content in files.items():
    with open(os.path.join(OUT, name), "w", encoding="utf-8", newline="\n") as f:
        f.write(content)

import base64

cells = []
for name, content in files.items():
    src = "data:image/svg+xml;base64," + base64.b64encode(content.encode()).decode()
    dark = "dark" in name
    bg = NAVY if dark else "#ffffff"
    cells.append(
        f'<figure style="background:{bg};margin:0;padding:24px;border-radius:12px;display:flex;'
        f'flex-direction:column;align-items:center;gap:12px">'
        f'<img src="{src}" style="height:128px;max-width:100%">'
        f'<div style="display:flex;gap:16px;align-items:center">'
        f'<img src="{src}" style="height:32px"><img src="{src}" style="height:16px"></div>'
        f'<figcaption style="color:{"#ccc" if dark else "#555"};font:12px monospace">{name}</figcaption></figure>'
    )
html = (
    "<!doctype html><meta charset=utf-8><title>trasloco logo preview</title>"
    '<body style="margin:0;padding:24px;background:#e9e7e2;font-family:sans-serif">'
    '<div style="display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px">'
    + "".join(cells)
    + "</div></body>\n"
)
with open(os.path.join(OUT, "preview.html"), "w", encoding="utf-8", newline="\n") as f:
    f.write(html)
print("ok", list(files))
