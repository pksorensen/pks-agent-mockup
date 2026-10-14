# pks-agent-mockup

Put an ad series into photographed ad locations, with no AI at render time.

The scenes are photos in which every ad surface is pure chroma green (`#00FF00`).
The compositor finds the green surfaces, fits a quad to each, and warps the creative
in with a homography. It uses the green channel's brightness as the lighting on the
ad and lets anything standing in front of the screen (people, poles, hair) show
through via the matte. Finally it adds a little grain and, for screens, light bloom.
18 images take ~2 s on 6 cores.

```
mockup detect examples/scenes/*.png     # writes <scene>.surfaces.json (hand-editable)
mockup render examples/series.yaml      # series × scenes → output/<series>/<scene>.jpg + index.html
```

## Series file

```yaml
scenes:
  - scenes/*.png                 # globs; a later entry for the same file wins
  - path: scenes/rooftop-led.png
    finish: led                  # print | backlit (default) | led
series:                          # e.g. one per campaign week
  - name: uge-82
    ads:                         # format name → creative; each surface gets
      wide: c82-wide.png         # the format whose aspect is closest
      portrait: c82-portrait.png
      landscape: c82-landscape.png
output: out
format: jpg                      # or png
quality: 90
look: { bloom: 1, grain: 0.012 }
key: { low: 0.12, high: 0.35, minArea: 0.0006 }
```

The sidecar `<scene>.surfaces.json` overrides detection per surface: `quad` (corners
clockwise from top-left), `aspect` (the real width/height when perspective fools
the estimate), `finish`, `ad` (pin a format) and `fit` (`contain`/`cover`; empty =
cover when the aspect mismatch is <10%, otherwise contain padded with the
creative's edge colour, so headlines are never cut).

## Scenes

`examples/scenes/` contains 8 AI-generated, unbranded scenes (Foundry gpt-image-2;
prompts in `*.prompt.txt`, list in `scenes.tsv`). They are generated once and
then reused for free by every series. New ones:

```
pks image --model gpt-image-2 --prompt-file x.prompt.txt --aspect-ratio 3:2 \
  --resolution 2048 --quality high -o x.png && mockup detect x.png
```

Keep the prompt's chroma clause: one uniform green, no text on the surface, and
nothing else in the image in that hue.
