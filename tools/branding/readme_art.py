"""Generate the animated README illustrations.

Usage: python tools/branding/readme_art.py docs/assets

Writes:
  readme-hero-light.svg / readme-hero-dark.svg  any-to-any architecture
  readme-retired-check.svg                       terminal run of the retired tasks check

Animations use SMIL, which GitHub renders in <img> SVGs. The terminal shows the
real output of the README commands run against a two-file sample repository
(azure-pipelines.yml and templates/publish.yml); regenerate it if the commands
or the catalog change.
"""
import os
import sys
from xml.sax.saxutils import escape

OUT = sys.argv[1]
SANS = "-apple-system,BlinkMacSystemFont,'Segoe UI','Noto Sans',Helvetica,Arial,sans-serif"
MONO = "ui-monospace,SFMono-Regular,'SF Mono',Menlo,Consolas,'Liberation Mono',monospace"
TAPE = "#F0A83A"

THEMES = {
    "light": dict(ink="#1B2433", muted="#59636E", faint="#66707A", box="#F6F8FA",
                  line="#D0D7DE", panel="#FFFFFF", planned="#F6F8FA"),
    "dark": dict(ink="#E6EDF3", muted="#9198A1", faint="#8B949E", box="#161B22",
                 line="#3D444D", panel="#0D1117", planned="#0D1117"),
}

W, H = 960, 360
CX, CY = 480, 186            # IR centre
SRC_X, DST_X, BOX_W, BOX_H = 40, 680, 240, 46
ROWS = [92, 150, 208, 266]   # box top y


def box(x, y, label, sub, t, planned, anim=""):
    dash = ' stroke-dasharray="5 4"' if planned else ""
    fill = t["planned"] if planned else t["box"]
    color = t["muted"] if planned else t["ink"]
    weight = "500" if planned else "600"
    out = [f'<rect x="{x}" y="{y}" width="{BOX_W}" height="{BOX_H}" rx="10" fill="{fill}" '
           f'stroke="{t["line"]}" stroke-width="1.5"{dash}>{anim}</rect>']
    ty = y + (BOX_H / 2) + (0 if sub else 5)
    out.append(f'<text x="{x + BOX_W / 2}" y="{ty - (4 if sub else 0)}" text-anchor="middle" '
               f'font-size="15" font-weight="{weight}" fill="{color}">{escape(label)}</text>')
    if sub:
        out.append(f'<text x="{x + BOX_W / 2}" y="{ty + 13}" text-anchor="middle" font-size="11" '
                   f'fill="{t["faint"]}">{escape(sub)}</text>')
    return "".join(out)


def left_curve(y):
    sy = y + BOX_H / 2
    return f"M{SRC_X + BOX_W},{sy} C{SRC_X + BOX_W + 70},{sy} {CX - 140},{CY} {CX - 58},{CY}"


def right_curve(y):
    ty = y + BOX_H / 2
    return f"M{CX + 58},{CY} C{CX + 140},{CY} {DST_X - 70},{ty} {DST_X},{ty}"


def route(sy, ty):
    s = SRC_X + BOX_W
    a, b = sy + BOX_H / 2, ty + BOX_H / 2
    return (f"M{s},{a} C{s + 70},{a} {CX - 140},{CY} {CX - 58},{CY} L{CX + 58},{CY} "
            f"C{CX + 140},{CY} {DST_X - 70},{b} {DST_X},{b}")


def hero(t):
    p = [f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" '
         f'role="img" aria-label="trasloco reads pipelines from any source platform into one common, '
         f'platform-neutral model and writes them out for any target platform">',
         f'<title>Any platform to any platform through one common model</title>',
         f'<g font-family="{SANS}">']
    for x, label in ((SRC_X, "SOURCE"), (DST_X, "TARGET")):
        p.append(f'<text x="{x + BOX_W / 2}" y="70" text-anchor="middle" font-size="12" '
                 f'font-weight="600" letter-spacing="2" fill="{t["faint"]}">{label}</text>')
    p.append(f'<text x="{CX}" y="70" text-anchor="middle" font-size="12" font-weight="600" '
             f'letter-spacing="2" fill="{t["faint"]}">TRASLOCO</text>')

    sources = [("Azure Pipelines", "", False), ("GitHub Actions", "", False),
               ("GitLab CI  ·  Bitbucket", "after 1.0", True), ("Jenkins  ·  Bamboo", "after 1.0", True)]
    targets = [("GitHub Actions", "", False), ("Azure Pipelines", "also: modernize in place", False),
               ("GitLab CI  ·  Bitbucket", "after 1.0", True)]
    # connectors
    for i, (_, _, planned) in enumerate(sources):
        dash = ' stroke-dasharray="5 5"' if planned else ""
        p.append(f'<path d="{left_curve(ROWS[i])}" fill="none" stroke="{t["line"]}" stroke-width="2"{dash}/>')
    for i, (_, _, planned) in enumerate(targets):
        dash = ' stroke-dasharray="5 5"' if planned else ""
        p.append(f'<path d="{right_curve(ROWS[i])}" fill="none" stroke="{t["line"]}" stroke-width="2"{dash}/>')

    # travelling dots: (source row, target row, colour, begin)
    trips = [(0, 0, TAPE, 0.0), (1, 1, TAPE, 1.5), (0, 1, TAPE, 3.0), (2, 0, t["faint"], 4.5),
             (1, 2, t["faint"], 6.0), (3, 1, t["faint"], 7.5)]
    src_anim = {i: "" for i in range(len(sources))}
    dst_anim = {i: "" for i in range(len(targets))}
    for s, d, color, begin in trips:
        motion = (f'path="{route(ROWS[s], ROWS[d])}" calcMode="spline" keyTimes="0;1" '
                  f'keySplines="0.45 0 0.55 1"')
        p.append(f'<circle r="12" fill="{color}" opacity="0">'
                 f'<animateMotion dur="3.2s" begin="trip{s}{d}.begin" {motion}/>'
                 f'<animate attributeName="opacity" values="0;0.22;0.22;0" keyTimes="0;0.08;0.92;1" '
                 f'dur="3.2s" begin="trip{s}{d}.begin"/></circle>')
        p.append(f'<circle r="6" fill="{color}" opacity="0">'
                 f'<animateMotion dur="3.2s" begin="{begin}s;trip{s}{d}.end+5.8s" id="trip{s}{d}" {motion}/>'
                 f'<animate attributeName="opacity" values="0;1;1;0" keyTimes="0;0.08;0.92;1" '
                 f'dur="3.2s" begin="trip{s}{d}.begin"/></circle>')
        flash = (f'<animate attributeName="stroke" values="{color};{color};{t["line"]}" keyTimes="0;0.5;1" '
                 f'dur="1.2s" begin="trip{s}{d}.%s"/>'
                 f'<animate attributeName="stroke-width" values="2.5;2.5;1.5" keyTimes="0;0.5;1" '
                 f'dur="1.2s" begin="trip{s}{d}.%s"/>')
        src_anim[s] += flash % ("begin", "begin")
        dst_anim[d] += flash % ("end", "end")

    # IR hub, drawn over the dots so they pass through it
    p.append(f'<circle cx="{CX}" cy="{CY}" r="58" fill="none" stroke="{TAPE}" stroke-width="2" opacity="0">'
             f'<animate attributeName="r" values="58;84" dur="3s" repeatCount="indefinite"/>'
             f'<animate attributeName="opacity" values="0.55;0" dur="3s" repeatCount="indefinite"/></circle>')
    p.append(f'<circle cx="{CX}" cy="{CY}" r="58" fill="{t["panel"]}" stroke="{TAPE}" stroke-width="3"/>')
    p.append(f'<text x="{CX}" y="{CY - 4}" text-anchor="middle" font-size="17" font-weight="700" '
             f'fill="{t["ink"]}">common</text>')
    p.append(f'<text x="{CX}" y="{CY + 16}" text-anchor="middle" font-size="17" font-weight="700" '
             f'fill="{t["ink"]}">model</text>')
    p.append(f'<text x="{CX}" y="{CY + 92}" text-anchor="middle" font-size="12.5" fill="{t["muted"]}">'
             f'read › expand templates › translate › check › write</text>')

    for i, (label, sub, planned) in enumerate(sources):
        p.append(box(SRC_X, ROWS[i], label, sub, t, planned, src_anim[i]))
    for i, (label, sub, planned) in enumerate(targets):
        p.append(box(DST_X, ROWS[i], label, sub, t, planned, dst_anim[i]))

    # legend
    ly = H - 14
    p.append(f'<line x1="{SRC_X}" y1="{ly - 4}" x2="{SRC_X + 28}" y2="{ly - 4}" stroke="{TAPE}" stroke-width="3"/>'
             f'<text x="{SRC_X + 36}" y="{ly}" font-size="12" fill="{t["muted"]}">first pair: Azure DevOps and GitHub</text>'
             f'<line x1="{SRC_X + 380}" y1="{ly - 4}" x2="{SRC_X + 408}" y2="{ly - 4}" stroke="{t["line"]}" '
             f'stroke-width="2" stroke-dasharray="5 5"/>'
             f'<text x="{SRC_X + 416}" y="{ly}" font-size="12" fill="{t["muted"]}">planned after 1.0</text>')
    p.append("</g></svg>")
    return "".join(p)


# ---------------------------------------------------------------- terminal
TW, LINE, PAD, CHAR = 960, 22, 22, 7.23   # CHAR = advance of 12px monospace
URL = "https://raw.githubusercontent.com/HarnageaGabriel/trasloco/main/catalog/azure-pipelines/tasks.retirement.yaml"
SCRIPT = [  # (kind, text, typing seconds or pause before)
    ("cmd", f"url={URL}"),
    ("cmd", "curl -fsSL \"$url\" | sed -n 's/^ *- task: //p' > retired-tasks.txt"),
    ("cmd", "wc -l < retired-tasks.txt"),
    ("out", "73"),
    ("cmd", "grep -rniwF -f retired-tasks.txt --include='*.yml' --include='*.yaml' ."),
    ("hit", ("./azure-pipelines.yml", "11", "- task: ", "DotNetCoreCLI@1")),
    ("hit", ("./templates/publish.yml", "2", "- task: ", "CopyFiles@1")),
    ("hit", ("./templates/publish.yml", "5", "- task: ", "AzureCLI@0")),
]
CYCLE = 16.0


def terminal():
    th = 44 + PAD + LINE * (len(SCRIPT) + 1) + PAD
    p = [f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {TW} {th}" width="{TW}" height="{th}" '
         f'role="img" aria-label="Terminal: download the retired task list and grep a repository; '
         f'three retired tasks are found">',
         '<title>Find retired Azure Pipelines tasks from the shell</title>',
         f'<rect width="{TW}" height="{th}" rx="12" fill="#0D1117" stroke="#3D444D"/>',
         f'<rect width="{TW}" height="36" rx="12" fill="#161B22"/><rect y="24" width="{TW}" height="12" fill="#161B22"/>',
         '<circle cx="22" cy="18" r="6" fill="#FF5F57"/><circle cx="42" cy="18" r="6" fill="#FEBC2E"/>'
         '<circle cx="62" cy="18" r="6" fill="#28C840"/>',
         f'<text x="{TW / 2}" y="22" text-anchor="middle" font-family="{SANS}" font-size="12" fill="#9198A1">'
         'retired tasks check · sample repository</text>',
         # master clock
         f'<rect width="0" height="0"><animate id="clk" attributeName="x" from="0" to="0" dur="{CYCLE}s" '
         'begin="0s;clk.end"/></rect>',
         f'<g font-family="{MONO}" font-size="12" xml:space="preserve">']
    t = 0.6
    y = 44 + PAD
    defs = []
    for i, (kind, text) in enumerate(SCRIPT):
        if kind == "cmd":
            n = len(text) + 2
            dur = min(2.2, 0.35 + n * 0.018)
            defs.append(f'<clipPath id="c{i}"><rect x="{PAD - 2}" y="{y - 15}" height="{LINE}" width="{n * CHAR + 6:.0f}">'
                        f'<set attributeName="width" to="0" begin="clk.begin"/>'
                        f'<animate attributeName="width" from="0" to="{n * CHAR + 6:.0f}" dur="{dur:.2f}s" '
                        f'begin="clk.begin+{t:.2f}s" fill="freeze" calcMode="discrete" '
                        f'values="{";".join(f"{k * CHAR:.0f}" for k in range(0, n + 1))}"/></rect></clipPath>')
            p.append(f'<text x="{PAD}" y="{y}" clip-path="url(#c{i})">'
                     f'<tspan fill="{TAPE}">$ </tspan><tspan fill="#E6EDF3">{escape(text)}</tspan></text>')
            t += dur + 0.45
        else:
            if kind == "out":
                body = f'<tspan fill="#E6EDF3">{escape(text)}</tspan>'
            else:
                path, num, mid, task = text
                body = (f'<tspan fill="#D2A8FF">{escape(path)}</tspan><tspan fill="#9198A1">:</tspan>'
                        f'<tspan fill="#7EE787">{num}</tspan><tspan fill="#9198A1">:</tspan>'
                        f'<tspan fill="#E6EDF3">{escape(mid)}</tspan>'
                        f'<tspan fill="#FF7B72" font-weight="700">{escape(task)}</tspan>')
            p.append(f'<text x="{PAD}" y="{y}">{body}'
                     f'<set attributeName="opacity" to="0" begin="clk.begin"/>'
                     f'<set attributeName="opacity" to="1" begin="clk.begin+{t:.2f}s"/></text>')
            t += 0.25
        y += LINE
    # blinking cursor on the last prompt line
    p.append(f'<text x="{PAD}" y="{y}"><tspan fill="{TAPE}">$ </tspan>'
             f'<tspan fill="#E6EDF3">▍<animate attributeName="opacity" values="1;0;1" dur="1s" '
             f'repeatCount="indefinite"/></tspan>'
             f'<set attributeName="opacity" to="0" begin="clk.begin"/>'
             f'<set attributeName="opacity" to="1" begin="clk.begin+{t + 0.3:.2f}s"/></text>')
    assert t + 3 < CYCLE, t
    p.append("</g>")
    p.insert(1, "<defs>" + "".join(defs) + "</defs>")
    p.append("</svg>")
    return "".join(p)


for name, theme in THEMES.items():
    with open(os.path.join(OUT, f"readme-hero-{name}.svg"), "w", encoding="utf-8", newline="\n") as f:
        f.write(hero(theme) + "\n")
with open(os.path.join(OUT, "readme-retired-check.svg"), "w", encoding="utf-8", newline="\n") as f:
    f.write(terminal() + "\n")
