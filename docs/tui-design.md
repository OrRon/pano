# `pano ui` — design notes

`pano ui` is the interactive terminal interface. This page records the visual
rules it follows so changes stay coherent. The implementation lives in
`internal/tui/` (theme in `theme.go`, list in `render.go`, detail in
`detail.go`, overlays in `overlay.go`).

## Principles

1. **Lines, not boxes.** Panes are separated by a single faint `│`/`─` and a
   title line. The only bordered box is a modal overlay (rules, held, help).
2. **Depth from tiers, not colour.** Three background tiers (base / raised /
   selected) and four text tiers (primary / secondary / muted / faint) carry
   hierarchy. The accent colour is spent on exactly one thing at a time: the
   cursor row and the focused pane.
3. **One accent, semantic colours elsewhere.** Status class, LLM, held, mock
   and rule glyphs are the only saturated colours in the list.
4. **Fixed columns, numbers right-aligned, host anchored right.** Hosts are
   left-truncated so the TLD survives; paths are right-truncated. Duration and
   download size use a log-scale gradient so outliers pop without reading
   digits.
5. **Motion means data changed.** Only live things animate: in-flight
   spinner, streaming byte counter, the "n new" pill, a one-frame flash on
   arrival, the mascot's eyes (see below). One 250 ms tick drives everything.
6. **Chrome ≤ 3 rows.** Header, footer hints, and a filter/path bar only when
   active. Column headers disappear below 25 rows.
7. **Usable at 16 colours.** Bubble Tea downsamples; selection falls back to
   reverse video.

Selection and bars are painted **edge to edge**: `Theme.paint` re-asserts
the background after every SGR reset a nested style emits, so a highlighted
row never stops at its first coloured cell. Leaving the detail view (`esc`,
`←`, `q`) closes the pane entirely — the list gets its width back; the pane
is a view you enter and leave, not a preview that lingers.

Anti-patterns deliberately avoided: rounded borders on every pane, rainbow
key hints, emoji in the list, centred text in panes, Nerd-Font-only glyphs,
decorative animation.

## Palette ("Panoptes")

| Token | Dark | Light | Use |
|---|---|---|---|
| bg.raised | `#1E1E26` | `#F3F3F7` | header, footer, pane title lines |
| bg.selected | `#2B2B38` | `#E6E7EF` | cursor row, selected drawer row |
| line.faint | `#33333F` | `#DADAE3` | separators, rules |
| fg.primary | `#E7E5EF` | `#1D1D26` | paths, values, body text |
| fg.secondary | `#A9A6B6` | `#4E4D5A` | hosts, header keys, tab labels |
| fg.muted | `#6F6D7B` | `#8A8896` | ids, times, bytes, static-asset rows |
| fg.faint | `#4A4955` | `#B9B8C3` | column headers, placeholders |
| accent | `#38C8E8` | `#0B8DB0` | cursor gutter, focused id, active tab, key hints, streaming |
| ok / redirect / warn / err | `#3FCF8E` / `#7FA1C9` / `#F2B544` / `#F0605A` | | 2xx / 3xx / 4xx / 5xx+errors; write methods use warn |
| llm | `#B692FF` | `#6E42D6` | `◆` LLM flows, explain provider |
| mock | `#F27DB4` | `#C23D86` | `◇` mocked responses |
| syn.str / syn.num / syn.bool | `#9ECE9A` / `#E6A96B` / `#8FB8E8` | `#2F7A45` / `#9C5A10` / `#3B6FB0` | body syntax only: strings, numbers, `true/false/null` — in summary, schema and pretty JSON views |
| held | `#15151A` on `#38C8E8` | | `‖ HELD` chip — the only inverted chip |
| brand | `#C44CE6` → `#6C8DF2` | `#A82ED0` → `#3F63D8` | logo gradient — mascot only, never semantic |

Light values are picked automatically from the terminal background
(`tea.RequestBackgroundColor`); dark is the fallback.

## Layout breakpoints

| Class | Trigger | Arrangement |
|---|---|---|
| S | width < 100 or height < 30 | list **or** detail; drawers and help take the full screen |
| M | 100–159 wide, ≥ 30 tall | list on top (40 %), detail below |
| L | ≥ 160 wide | list left (55 %), detail right; at ≥ 200 a third column shows explain / timing / rules for the selected flow |

Columns: narrow `ID TIME METH HOST/PATH ST DUR ⚑`; wide adds `HOST` (right-aligned),
`▲UP ▼DOWN TYPE FLAGS`. Static assets (js/css/img/font/media) render in
`fg.muted` as whole rows.

## Glyphs (single-cell, no emoji)

`▌` cursor · `▍` marked · `●/◐/○` on/partial/off (also decrypt all/only/off) · `⇄` proxy/tunnel · `✓/✕` ok/error ·
`◆` llm · `≈` stream · `▸` live · `‖` held · `◇` mock · `↻` replay · `⊘` block ·
`◔` delay · `≋` throttle · `✎` rewrite · `▲/▼` up/down · `⠋…` spinner.

## Keys

List: `j/k` move · `g/G` top/bottom · `⏎` open · `x` explain · `m` mark · `d` diff ·
`R` replay · `/` filter · `f` follow · `space` pause list · `c` toggle capture ·
`r` rules · `h` held · `D` decrypt · `o` options for the selected flow ·
`n` never-decrypt the selected tunnel's host · `?` help · `q` quit.

Options menu (`o` in the list or detail): every action for that entry with
its key on screen — `o` decrypt only this host (adds it and switches mode
to only), `n` never decrypt it (or undo), `/` filter to the host, `R`
replay, `m` mark, `x` explain. `j/k` + `⏎` also work. The last line names
the current decrypt state and whether the host is on a list.

Drawer (one drawer, three tabs — `⇥` cycles rules → held → decrypt): `⏎`
toggle rule / resume held / host → only · `x` remove · `n` host → never ·
`+` type a host · `1/2/3` decrypt all/only/off · `esc` close.

Detail: `1-5` tabs (Summary/Request/Response/Explain/Diff; the strip shows the
number next to each name) · `⇥` next tab ·
`v` cycle view (summary→schema→pretty→raw) · `/` JSON path · `S` reveal secrets
(audited) · `H` toggle headers · `J/K` next/previous flow · `esc` back.

Filter syntax matches the CLI flags: `host=*.openai.com path=/v1/* method=POST
status=!2xx since=15m type=json errors=1 state=held` plus bare words for
full-text search.

The footer hints change with the tab: on Explain they lead with `1/2/3
summary/request/response`, on Request/Response with `v view · H headers`;
with a tunnel selected the list footer leads with `n never decrypt <host>`.

## Decrypt state is always named

The header carries a decrypt chip painted by meaning — `● decrypt all`
(ok green), `◐ decrypt only api.anthropic.com localhost` (accent; the hosts
themselves, shrinking to `·N` only below 120 columns), `○ decrypt off`
(warn) — and, whenever a client refused pano's certificate in the last hour,
a red `✕ rejected mmg.whatsapp.net +1` chip. The Decrypt tab lists every
entry of `only`, `never` and `rejected` one per row (accent / secondary /
error) and scrolls rather than elides; section headers carry the count on
the right. Rejected hosts are suggestions: `n` adds one to `never`, nothing
happens on its own.

## Mascot

The logo's rounded rectangle given a pair of eyes — Panoptes, the watcher
(`internal/tui/mascot.go`). It is a status indicator first and a character
second: the eyes are read, not admired.

| Eyes | Meaning |
|---|---|
| `• •` | capturing, quiet — glances left/right and blinks once per 10 s cycle |
| `◉ ◉` (accent) | a flow arrived in the last 600 ms |
| `─ ─` (muted) | capture paused, by the daemon or by `space` |
| `✕ ✕` (err) | daemon unreachable |

Two forms: the one-row `▐ • • ▌` at the far left of the header, and the
three-row rounded box in the empty state. Both take the logo gradient
(`brand.a` violet → `brand.b` blue, left→right in the header, top→bottom in
the box) — the only non-semantic colour in the UI, reserved for the mascot.
Everything derives from model state and the shared tick, so snapshots are
stable (frame 0 is centred, open eyes). `pano on` / `pano off` print the same
character in the terminal; `on` plays a short wake-up on a colour TTY.

## Body colouring

The daemon sends plain text; `internal/tui/style.go` colours it without
changing a character, so the TUI, CLI and MCP always agree on content.

- **Explain** (`styleExplainText`): `key:` labels bold secondary; provider in
  `llm`, model bold, status by class; `request:`/`usage:` segments with numbers
  in `syn.num` and parentheses muted; `final:` items keyed by kind —
  `[text]` ok, `[thinking]` llm, `[tool_use]` warn, `[refusal]` err — with the
  quoted answer in primary and `(n chars)` muted; stop reasons by meaning
  (`end_turn` ok, `tool_use` llm, `max_tokens` warn); errors err; chat roles
  user accent / assistant llm / system warn / tool ok.
- **Summary / pretty JSON** (`styleKVLine`, `styleJSONLine`): keys secondary,
  type words faint, values by type (`syn.*`), punctuation faint, redacted
  values muted with a faint `redacted` tag.
- **Schema** (`styleSchemaLine`): keys secondary, optional `?` warn, types by
  kind, enum values `syn.str`, brackets faint.
