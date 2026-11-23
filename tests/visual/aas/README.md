# Standards boundary storyboard (Hades-046)

`storyboard.json` drives `scripts/demo-aas` (see `docs/operations/visual-testing.md`).

`hostile-names.csv`, `hostile-display.csv` and `hostile-environment.json` **deliberately
contain raw control characters (ESC) and Unicode bidi overrides (U+202E, U+202C)**. They
test that Hades shows such input escaped and refuses to export it. Edit them with a tool
that shows invisible characters.
