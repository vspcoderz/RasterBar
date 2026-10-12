#!/usr/bin/env python3
"""Generate docs/logo.svg — the RasterBar mark.

An "R" whose strokes are rising spectrum bars, drawn from the same hue
arithmetic src/palette.go uses (spectrumHue: hue 0..0.78, so bass red through to
treble violet and the sweep stays monotonic). Generating it rather than
hand-writing the SVG means the logo cannot drift away from the colours the
program actually paints with, and re-running it is the whole update process.

    python3 tools/mklogo.py > docs/logo.svg
    rsvg-convert -w 512 docs/logo.svg -o /tmp/opencode/logo-preview.png
"""

import math

W = H = 512

# --- palette (mirrors spectrumHue in src/palette.go) ------------------------


def hsv_to_rgb(h, s, v):
    h = h - math.floor(h)
    i = math.floor(h * 6)
    f = h * 6 - i
    p = v * (1 - s)
    q = v * (1 - f * s)
    t = v * (1 - (1 - f) * s)
    return [(v, t, p), (q, v, p), (p, v, t), (p, q, v), (t, p, v), (v, p, q)][int(i) % 6]


def spectrum_hue(band, val, beat):
    hue = max(0.0, min(1.0, band)) * 0.78
    s = 0.55 + 0.35 * val
    v = 0.45 + 0.45 * val + 0.20 * beat
    return hsv_to_rgb(hue, s, v)


def ramp_spectrum(band, val, beat):
    """The visualiser's own ramp, unmodified."""
    return spectrum_hue(band, val, beat)


def ramp_ember(band, val, beat):
    """Red through magenta to blue, skipping yellow and green.

    A logo wants a ramp with no muddy middle. spectrumHue spends most of its
    0.78-turn sweep on yellow-green-cyan, which reads as noise at avatar size.
    Running the wheel *backwards* from red covers the same width in colours that
    all sit at full saturation together -- and hsvToRGB wraps negatives, so this
    is the same arithmetic, not a second colour space.
    """
    hue = -0.20 * band
    return hsv_to_rgb(hue, 0.78 + 0.16 * val, 0.52 + 0.42 * val + 0.06 * beat)


def ramp_viridis(band, val, beat):
    """The scientific one: dark purple through teal to yellow."""
    hue = 0.34 + 0.24 * val - 0.10 * band
    s = 0.80 - 0.30 * band
    v = 0.28 + 0.66 * val + 0.10 * beat
    return hsv_to_rgb(hue, s, v)


RAMPS = {"spectrum": ramp_spectrum, "ember": ramp_ember, "viridis": ramp_viridis}


def hexc(rgb):
    return "#%02X%02X%02X" % tuple(int(c * 255 + 0.5) for c in rgb)


# --- the letterform ---------------------------------------------------------
#
# The R is a union of rectangles plus one diagonal band (the leg). Bars are cut
# from it by sampling x, so changing the letterform means editing these numbers
# rather than redrawing paths by hand.
#
# Inner box, in a 512 tile with 88/104 padding.
L, R = 104.0, 408.0
T, B = 88.0, 424.0

STEM_W = 76.0                       # the stem's width
BOWL_R = 312.0                      # the bowl's right edge
ARM = 62.0                          # thickness of the bowl's horizontal arms
MID_T = 238.0                       # top of the middle arm

# The leg: a band sweeping from under the bowl's middle arm down to the baseline.
LEG_X0, LEG_X1 = 168.0, 408.0
LEG_TOP0, LEG_TOP1 = 252.0, 372.0
LEG_BOT0, LEG_BOT1 = 300.0, 424.0

N_BARS = 32
PITCH = (R - L) / N_BARS
BAR_W = PITCH * 0.62


def rect_spans(x0, x1):
    """Vertical spans of the R's rectangular parts, for a bar at [x0, x1]."""
    spans = []

    def add(ax0, ay0, ax1, ay1):
        lo, hi = max(x0, ax0), min(x1, ax1)
        if hi > lo:
            spans.append((ay0, ay1))

    add(L, T, L + STEM_W, B)                          # stem
    add(L, T, BOWL_R, T + ARM)                        # top arm
    add(L, MID_T, BOWL_R, MID_T + ARM)                # middle arm
    add(BOWL_R - ARM, T, BOWL_R, MID_T + ARM)         # bowl right side
    return spans


def leg_span(x0, x1):
    """Vertical span of the diagonal leg, sampled across the bar's width."""
    if x1 < LEG_X0 or x0 > LEG_X1:
        return None
    lo = max(x0, LEG_X0)
    hi = min(x1, LEG_X1)
    if hi <= lo:
        return None
    # Sample the band's two edges at both ends and keep the outer bounds. The
    # band is monotonic over this range, so the union is one interval.
    def edge(e0, e1, x):
        return e0 + (e1 - e0) * (x - LEG_X0) / (LEG_X1 - LEG_X0)
    top = min(edge(LEG_TOP0, LEG_TOP1, lo), edge(LEG_TOP0, LEG_TOP1, hi))
    bot = max(edge(LEG_BOT0, LEG_BOT1, lo), edge(LEG_BOT0, LEG_BOT1, hi))
    return (top, bot) if bot > top else None


def merge(spans):
    if not spans:
        return []
    spans = sorted(spans)
    out = [list(spans[0])]
    for a, b in spans[1:]:
        if a <= out[-1][1]:
            out[-1][1] = max(out[-1][1], b)
        else:
            out.append([a, b])
    return [tuple(s) for s in out]


def emit(ramp="ember"):
    colour_at = RAMPS[ramp]
    out = []
    out.append(
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" '
        'width="%d" height="%d" role="img" aria-label="RasterBar">' % (W, H, W, H)
    )
    out.append("  <title>RasterBar</title>")
    # The tile is the spectrum palette's own background (palette.bg) rather than
    # black, so the mark sits on the same floor the visualiser draws on.
    out.append(
        '  <defs><linearGradient id="bg" x1="0" y1="0" x2="0" y2="1">'
        '<stop offset="0" stop-color="#1A081F"/>'
        '<stop offset="1" stop-color="#0C0511"/>'
        "</linearGradient></defs>"
    )
    out.append('  <rect width="%d" height="%d" rx="112" fill="url(#bg)"/>' % (W, H))

    out.append('  <g fill="none" stroke-linecap="round">')
    for i in range(N_BARS):
        x0 = L + i * PITCH
        x1 = x0 + PITCH
        band = i / (N_BARS - 1)
        # val rises left to right, so the bars read as climbing as well as
        # shifting hue — "rising" in both senses of the word.
        val = 0.62 + 0.30 * band
        colour = hexc(colour_at(band, val, 0.0))
        spans = rect_spans(x0, x1)
        leg = leg_span(x0, x1)
        if leg:
            spans.append(leg)
        for top, bot in merge(spans):
            # Inset by half the stroke width so the round caps sit inside the
            # letterform instead of pushing the silhouette outwards.
            out.append(
                '    <path d="M%.2f %.2fV%.2f" stroke="%s" stroke-width="%.2f"/>'
                % (x0 + BAR_W / 2, top + BAR_W / 2, bot - BAR_W / 2, colour, BAR_W)
            )
    out.append("  </g>")
    out.append("</svg>")
    return "\n".join(out) + "\n"


if __name__ == "__main__":
    import sys

    name = sys.argv[1] if len(sys.argv) > 1 else "ember"
    if name not in RAMPS:
        raise SystemExit("unknown ramp %r (have: %s)" % (name, ", ".join(sorted(RAMPS))))
    print(emit(name), end="")