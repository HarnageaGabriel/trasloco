"""Generate the trasloco logo: a taped moving box in motion (isometric box + packing tape + speed lines).

Usage: python tools/branding/logo_gen.py docs/assets [preview.html]
The optional preview page must not be committed.
"""
import base64, math, os, sys

OUT = sys.argv[1]
INK, PAPER, TAPE = "#1B2433", "#F6F3EC", "#F0A83A"
L = 46.0
C30, S30 = math.cos(math.radians(30)), math.sin(math.radians(30))
DX, DY = L * C30, L * S30
GAP, TAPE_W = 3.4, 11.0
U = (C30, -S30)  # direction of edges A->B reversed / E->D


def pts(*ps):
    return " ".join(f"{x:.2f},{y:.2f}" for x, y in ps)


def add(p, v, k=1.0):
    return (p[0] + v[0] * k, p[1] + v[1] * k)


def box(cx, top, body, tape, uid, lines=True, line_color=None):
    A, B, D, E = (cx, top), (cx - DX, top + DY), (cx + DX, top + DY), (cx, top + 2 * DY)
    Bb, Eb, Db = (B[0], B[1] + L), (E[0], E[1] + L), (D[0], D[1] + L)
    M1, M2 = ((A[0] + B[0]) / 2, (A[1] + B[1]) / 2), ((D[0] + E[0]) / 2, (D[1] + E[1]) / 2)
    h = TAPE_W / 2
    tape_top = [add(M1, U, -h), add(M1, U, h), add(M2, U, h), add(M2, U, -h)]
    tape_side = [add(M2, U, -h), add(M2, U, h), add(add(M2, U, h), (0, L)), add(add(M2, U, -h), (0, L))]
    mask = (
        f'<mask id="m{uid}" maskUnits="userSpaceOnUse"><rect x="-20" y="-20" width="200" height="200" fill="#fff"/>'
        f'<g fill="none" stroke="#000" stroke-width="{GAP}" stroke-linecap="square">'
        f'<polyline points="{pts(B, E, D)}"/><line x1="{E[0]:.2f}" y1="{E[1]:.2f}" x2="{Eb[0]:.2f}" y2="{Eb[1]:.2f}"/>'
        f"</g></mask>"
    )
    g = (
        f'<g mask="url(#m{uid})"><g fill="{body}"><polygon points="{pts(A, D, E, B)}"/>'
        f'<polygon points="{pts(B, E, Eb, Bb)}"/><polygon points="{pts(E, D, Db, Eb)}"/></g>'
        f'<g fill="{tape}"><polygon points="{pts(*tape_top)}"/><polygon points="{pts(*tape_side)}"/></g></g>'
    )
    speed = ""
    if lines:
        lc = line_color or body
        x_end = B[0] - 6
        ys, lens = [B[1] + 10, B[1] + 22, B[1] + 34], [18, 26, 14]
        speed = "".join(
            f'<line x1="{x_end - n:.2f}" y1="{y:.2f}" x2="{x_end:.2f}" y2="{y:.2f}" stroke="{lc}" stroke-width="5" stroke-linecap="round"/>'
            for y, n in zip(ys, lens)
        )
    return f"<defs>{mask}</defs>{speed}{g}"


def svg(w, h, inner):
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} {h}" role="img" aria-label="trasloco"><title>trasloco</title>{inner}</svg>\n'


WORD = (
    "font-family=\"'JetBrains Mono','SFMono-Regular',Menlo,Consolas,'Liberation Mono',monospace\" "
    'font-size="54" font-weight="700" letter-spacing="-1.5"'
)
files = {
    "trasloco-icon.svg": svg(128, 128, box(80, 14, INK, TAPE, "il")),
    "trasloco-icon-dark.svg": svg(128, 128, box(80, 14, PAPER, TAPE, "id")),
    "trasloco-favicon.svg": svg(128, 128, box(64, 14, INK, TAPE, "fl", lines=False)),
    "trasloco-logo.svg": svg(440, 128, box(80, 14, INK, TAPE, "ll") + f'<text x="150" y="86" fill="{INK}" {WORD}>trasloco</text>'),
    "trasloco-logo-dark.svg": svg(440, 128, box(80, 14, PAPER, TAPE, "ld") + f'<text x="150" y="86" fill="{PAPER}" {WORD}>trasloco</text>'),
}
os.makedirs(OUT, exist_ok=True)
for n, c in files.items():
    with open(os.path.join(OUT, n), "w", encoding="utf-8", newline="\n") as f:
        f.write(c)


def img(c, h):
    return f'<img style="height:{h}px" src="data:image/svg+xml;base64,{base64.b64encode(c.encode()).decode()}">'


def panel(bg, items):
    return f'<div style="background:{bg};padding:22px;border-radius:12px;display:flex;align-items:flex-end;gap:22px;flex-wrap:wrap">{items}</div>'


i_l, i_d, fav = files["trasloco-icon.svg"], files["trasloco-icon-dark.svg"], files["trasloco-favicon.svg"]
html = (
    "<!doctype html><meta charset=utf-8><title>trasloco concept</title>"
    '<body style="margin:0;padding:22px;background:#e6e3dc;display:grid;gap:14px">'
    + panel("#fff", img(i_l, 160) + img(i_l, 64) + img(i_l, 32) + img(fav, 32) + img(fav, 16))
    + panel(INK, img(i_d, 160) + img(i_d, 64) + img(i_d, 32))
    + panel("#fff", img(files["trasloco-logo.svg"], 96) + img(files["trasloco-logo.svg"], 40))
    + panel(INK, img(files["trasloco-logo-dark.svg"], 96) + img(files["trasloco-logo-dark.svg"], 40))
    + "</body>"
)
if len(sys.argv) > 2:
    open(sys.argv[2], "w", encoding="utf-8").write(html)
print("ok")
