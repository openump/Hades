# Visual verification of the operator views (Hades-044)

The operator views are tested for what an operator **sees**, not only for the data
behind them:

| View | Captures | Checked by |
| --- | --- | --- |
| `hades-inspector` console | desktop (1440 px) and narrow (390 px) width; hostile content | pixel comparison with a reviewed baseline |
| `hades top` | `-once -color`; each of the four interactive panes; the filter prompt; hostile content | pixel comparison with a reviewed baseline |
| `vmui` history chart | the real `hades up` demo stack | content checks (series, labels, a drawn plot, statistics); not pixel-compared |

```sh
scripts/screenshot-stack              # Linux: pinned browser, compared with the baselines
scripts/screenshot-stack --preview    # any OS: local Chrome, captures only
scripts/screenshot-stack --refresh    # accept the captures as new baselines, after review
```

Screenshots, and a diff image for every mismatch, are written to
`artifacts/visual/`; the CI `visual` job uploads them.

## How it is deterministic

- **Fixtures, not a live plant.** `tests/visual` serves the real console
  (`inspect.NewHandler`) and renders `hades top` with its real renderer from a model
  with a **fixed clock**. The console takes every time from the snapshot (it never reads
  the browser clock), so the same commit renders the same pixels and no masks are
  needed. The normal fixture shows every visual state: all four quality badges, a
  producer status, executed, gate-rejected and awaiting receipts, quarantine counts,
  certificates in each expiry band, and verified, mismatched and unreachable services.
- **One pinned browser.** Baselines come from the digest-pinned
  `chromedp/headless-shell` image running as `linux/amd64`, the CI runner's
  architecture, so fonts and rasterization match. Another browser (for example on a
  Mac) is a preview; its captures are not compared. That image hangs under amd64
  emulation on Apple silicon, which is why baselines are produced on CI.
- **Ready means rendered.** The capture drives Chromium over the DevTools protocol and
  waits for a rendered-content condition (connected, every section filled, fonts
  loaded), never for network idle or a virtual-time budget: the console holds a
  Server-Sent Events stream open, so those never settle.
- **Comparison.** A pixel differs if any channel differs by more than 16; a view fails
  above 0.05 % differing pixels or on any size change (a layout change). Regions that
  must vary can be masked in `tests/visual/baselines/masks.json`; none are needed today.
  Baselines change only through `--refresh`, and are committed so a PR shows them.

## The `vmui` chart

The chart is not pixel-compared: VictoriaMetrics rejects samples older than its one-day
retention, so the data, and with it the time axis, is always recent. The script waits
until the series is queryable (VictoriaMetrics hides the newest 30 s by default), then
checks that the page shows the `hades_observation_value` series for the press, a drawn
plot and its statistics, and keeps the screenshot as an artifact.

## What the first captures found

The hostile-content capture showed that a right-to-left override in plant text
**reordered what the operator saw** (`payload` rendered as `daolyap`) in both the
console and `hades top`; both now replace Unicode bidirectional formatting characters,
as they already did control characters. It also showed long unbroken text overflowing
panels and squeezing columns, and the console's service status was a raw JSON blob; all
are fixed and covered by baselines.

## The standards boundary (Hades-046)

`scripts/demo-aas` verifies what an engineer sees when using `hades import`,
`hades check` and `hades aas export` ([standards boundary guide](aas.md)), and what an
independent AAS tool makes of the export. It also produces the demo video.

```sh
scripts/demo-aas              # Linux (CI): pinned browser, compared with baselines, video built
scripts/demo-aas --preview    # any OS: local Chrome, not compared
scripts/demo-aas --refresh    # CI only: accept captures as baselines and refresh docs/assets/aas-demo.*
```

The script:
- builds `hades` and the `uns-snapshot` test helper;
- starts Eclipse BaSyx (the digest-pinned test fixture in `deploy/basyx`) and a
  `hades up` demo stack;
- runs the committed storyboard, `tests/visual/aas/storyboard.json`.

Each storyboard step is one of the following:

| Step | What happens | How it is checked |
|---|---|---|
| `terminal` | a command is typed and run in a work directory with fixed inputs and relative paths | exit code and output patterns; the rendered page is compared with a baseline (`aas-<step>.png`) |
| `files` | generated files are rendered side by side: the CSV row next to the metadata and mapping it became, with JSON highlighted | compared with a baseline |
| `viewer` | a page of the BaSyx web UI, opened on one shell or submodel element | **content-checked**: every expected text (for example `0173-1#02-AAB991#008`, `-40 Cel`, the live value) must be rendered. It is not pixel-compared, because the UI is not ours (the same rule as `vmui`) |
| `live` | steps against the running demo stack: the snapshot from the broker, the live export and the live value in the viewer | content-checked, because the values change from run to run |

Output contains no temporary paths or wall-clock times, so terminal and file pages
reproduce exactly. Control and bidi characters are shown as marked escapes, never
interpreted.

**Hostile input.** Further steps check hostile input:
- tag tables and AAS input carrying escape sequences, bidi overrides, a line break and
  long names;
- the diagnostics (escaped, on one line);
- the file pages (escaped);
- the export refusals;
- BaSyx with long names and a line break.

The captures found that BaSyx displays bidi overrides as reordered text: an asset name
stored as `Press ‹RLO›40-sserp‹PDF›` appeared as "Press press-04". `hades aas export`
therefore refuses bidi formatting characters, as well as the control characters AAS
forbids.

**Demo video.** [`docs/assets/aas-demo.mp4`](../assets/aas-demo.mp4) (about 2½ minutes)
tells the story with captions:
1. a broken tag table and its diagnostics;
2. the corrected table and the files it produced;
3. reconciliation passes, and a mismatched revision is refused;
4. a vendor AAS imports;
5. a deterministic export opens in BaSyx;
6. snapshots from another revision, and overwriting a catalog, are refused;
7. a round trip gives identical files;
8. the live value from `hades up --demo` appears in the twin.

[`docs/assets/aas-demo.gif`](../assets/aas-demo.gif) is a short highlight reel.

The video is built from the same pinned-browser frames: the typing is rendered character
by character. A digest-pinned ffmpeg image assembles it with deterministic encoder
settings. It contains live values, so CI rebuilds it as an artifact on every run, and the
committed copy changes only through `--refresh`.

On macOS, previews show numbers in the system's format (for example `67,6`), because
Chrome on macOS ignores `--lang`. The CI captures use en-US.
