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
