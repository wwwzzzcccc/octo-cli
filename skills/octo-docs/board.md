# octo-docs — Whiteboard scenes & image export (`doc_type: board`)

Read this when the target is a **whiteboard** (`doc_type: board`) and you need to
read/edit its scene or render it to an image. A non-board target returns
`409 unsupported_doc_type`.

A whiteboard stores its Excalidraw scene on the Y.Doc — an ordered list of
`elements` (in fractional-index / z-order) plus a `files` map of image/file refs.
Same read-token-then-guarded-write discipline as the body and sheet surfaces.

## Semantic element commands

These commands perform the read/modify/write cycle for you: they GET the live
scene, locate ids fail-loud, preserve unrecognized element fields, increment
`version`, replace `versionNonce`, and PATCH with the returned `baseVersion` in
`If-Match`. `--dry-run` still performs the GET, then prints the complete final
PATCH without sending it.

```bash
octo-cli docs scene element create <docId> --type rectangle --x 40 --y 40 --width 240 --height 120
octo-cli docs scene element create <docId> --type ellipse   --x 320 --y 40
octo-cli docs scene element create <docId> --type diamond   --x 560 --y 40
octo-cli docs scene element create <docId> --type text --text "Decision" --x 80 --y 220 --width 80 --height 25 --baseline 20
octo-cli docs scene element create <docId> --type arrow --x 200 --y 260 --width 180 --height 0
octo-cli docs scene element create <docId> --type line  --x 200 --y 300 --width 180 --height 0
octo-cli docs scene element image <docId> --file ./photo.png --base-version "<token>" --x 40 --y 360 --width 480

octo-cli docs scene element transform <docId> <id> --x 100 --y 80 --width 300 --angle 0.1
octo-cli docs scene element style <docId> <id> --stroke-color '#1971c2' --background-color '#d0ebff' --opacity 90
octo-cli docs scene element text <docId> <id> --text "Ship it" --text-align center --runs '[{"start":0,"end":4,"bold":true}]'
octo-cli docs scene element layer <docId> <id> --position front
octo-cli docs scene element layer <docId> <id> --position before --relative-to <otherId>
octo-cli docs scene element update <docId> <id> --data '{"customData":{"status":"draft"}}'
octo-cli docs scene element delete <docId> <id>
```

`element image` accepts local PNG/JPEG/GIF files (10 MiB maximum), magic-checks
the file before upload, and sends its bytes as a raw request body. Image bytes
are not placed in argv, dry-run, or verbose output. The base-version is passed
explicitly via `--base-version`, so protect shell history/process listings as
appropriate; the CLI itself does not echo it in dry-run or verbose output. Pass
`--height` as well when an exact box is required; with one dimension the server
preserves the intrinsic aspect ratio. The response contains the canonical
server-generated `element`, `fileRef`, and the next `baseVersion`. The current
backend placement contract fixes new image opacity at `100`; change it afterward
with `docs scene element style <docId> <elementId> --opacity <0..100>`.

### Multi-selection & grouping

These commands operate atomically over a **set** of elements named by repeated
`--id`. Like the single-element commands they perform one guarded
read-modify-write: a single GET, all-or-nothing validation of the whole
selection, then **one** PATCH carrying every changed element under `If-Match`.
Each changed element gets its `version`/`versionNonce`/`updated` bumped and its
unknown fields preserved; a batch that would change nothing is rejected without a
PATCH. `--dry-run` performs the GET and prints the complete multi-element PATCH
body without sending it.

```bash
# Group two or more distinct live elements. A group id is generated unless
# --group-id is supplied; it is appended (outermost) to each element's groupIds,
# so any pre-existing nested group is preserved.
octo-cli docs scene element group <docId> --id E1 --id E2 [--id E3 ...] [--group-id G]

# Ungroup. With --group-id, that group is removed from every selected element
# (each must be a member). Without it, the OUTERMOST group the whole selection
# shares (the last in each element's innermost→outermost groupIds order) is
# removed; sharing no common group fails loud (pass --group-id to choose which
# to remove). Inner/nested and non-common groups are preserved.
octo-cli docs scene element ungroup <docId> --id E1 --id E2 [--group-id G]

# Apply one geometry / appearance change to every selected element.
octo-cli docs scene element transform-many <docId> --id E1 --id E2 --x 100 --width 300
octo-cli docs scene element style-many     <docId> --id E1 --id E2 --stroke-color '#1971c2' --opacity 90

# Move a set of elements together in z-order. Each selected structural component
# (group / frame / container-bound text) moves as one atomic block: components
# are ordered by their earliest original z-index and each keeps its members'
# original relative order, so interleaved source groups come out contiguous
# (never re-interleaved). Independent elements just keep their relative order.
octo-cli docs scene element layer-many <docId> --id E1 --id E2 --position front
octo-cli docs scene element layer-many <docId> --id E1 --id E2 --position before --relative-to E9
```

`group` requires at least **two** distinct live elements; the other batch verbs
accept one or more. A repeated, empty, missing, or tombstoned `--id`, a scene
with two live elements sharing an id, or an element with a malformed `groupIds`
(non-array, or containing an empty/duplicate/non-string entry) fails the whole
batch before any PATCH.

The selection is **fail-closed on complete structural units**: it is never
auto-expanded, and if any selected element belongs to a shared group, a frame
(the frame element plus every live child sharing its `frameId`), or a
container / bound-text pair (linked via `containerId` and `boundElements`)
whose other live members are not *all* in the selection, the whole batch is
rejected before any mutation — add every member of the unit to `--id`, or
operate on it separately. Structural references are also **validated strictly**
before any mutation, across every live element:

- `frameId`/`containerId` must be absent, null, or a nonempty string resolving
  to a live element of a compatible type. A `frameId` must point at a `frame`,
  and the `frameId` graph must be **acyclic** — a self-reference or any longer
  ancestry cycle is rejected (an acyclic nested-frame chain is fine). A
  `containerId` is carried only by `text` and must point at a valid text
  container — exactly `rectangle`, `ellipse`, `diamond`, or `arrow` (a labelled
  arrow); `line`, `freedraw`, `image`, `frame`, `embeddable`, or another `text`
  are rejected.
- `boundElements` must be absent, null, or an array of objects each carrying a
  nonempty string `id` and a supported `type` (`text` or `arrow`) resolving to a
  live element of the matching type; the same `id` may not be listed twice. The
  **owner** must be compatible with what it lists: a `text` entry is allowed only
  on a valid text container (the set above), and an `arrow` entry only on a valid
  arrow-binding target (`rectangle`/`ellipse`/`diamond`/`text`/`image`/`frame`/
  `embeddable`), where a `text` owner is accepted only when **unbound** (no
  `containerId`) — a bound label text may not own an arrow ref. A live arrow has
  two endpoints, so **at most two distinct owners** may claim the same arrow via
  `boundElements` (even when the arrow carries no endpoint bindings of its own); a
  third claimant is rejected.
- `startBinding`/`endBinding` are **arrow-only** fields. On any non-arrow element
  they must be absent or null; a non-arrow element carrying a non-null endpoint
  binding (of any shape) is corrupt and rejected. An `arrow`'s
  `startBinding`/`endBinding`, when present, must be an object whose
  `elementId` resolves to a live, non-arrow, **bindable** target and is never the
  arrow itself; an unbound `text` is a valid endpoint but a container-bound label
  `text` is not (bind the container, not its label). Arrow refs are validated but
  never pull the arrow into a shape's structural unit.

Malformed, dangling, or **contradictory** references fail the batch: a container
and its text disagreeing about their partner, two texts claiming one container,
two owners binding the same text (even when the text has no `containerId`), a
container binding more than one text, more than two owners claiming the same
arrow, or a container that lists an arrow whose endpoints do not bind back to it. A one-sided-but-consistent binding is accepted
and still unions the pair: `containerId` only, or the container's `boundElements`
entry only, and an arrow endpoint that names a shape the shape does not list back
(or vice-versa) are all legitimate. Every selected element must also **materially
change**: a member that the mutation would leave untouched (a no-op) or that
fails final validation fails the entire batch before any PATCH, so a selection
never half-applies. (`layer-many` is the one documented exception — see below.)

A supplied `--group-id` must match `[A-Za-z0-9_-]+` (≤ 255 chars); it is
rejected when it collides with an existing element id (reserved) or already
occurs in **any** live element's `groupIds` — even one outside the selection —
so grouping never silently merges the selection into a pre-existing group. Note
that a supplied `--group-id` is used only by `group`/`ungroup`; the other batch
verbs (`transform-many`, `style-many`, `layer-many`) take no group id at all.
`layer-many` rejects an anchor (`--relative-to`) that is itself in the
selection, and refuses a scene with duplicate or corrupt z-order indices. For
`before`/`after`, the anchor's **whole structural component** is treated as one
boundary: the moving run splices just below the anchor component's earliest live
member (`before`) or just above its latest (`after`), never between two members
of that component even when it is noncontiguous in the current z-order.
`layer-many` is also the documented exception to the "every selected element must
materially change" rule: when the requested move would reproduce the current live
z-order exactly, it is a **semantic no-op** — no element is rewritten and **no
PATCH is issued**, but the command still **succeeds**, emitting a success
envelope whose `data` carries `noop:true` (and `dry_run:true` as well under
`--dry-run`, matching the dry-run PATCH envelope). The
batch verbs are explicit `*-many` forms so the single-element
`transform`/`style`/`layer` positional `<elementId>` UX is unchanged.

Create supports `rectangle`, `ellipse`, `diamond`, `text`, `arrow`, `line`, and
`freedraw`,
and emits complete Excalidraw base fields plus an automatically generated
fractional `index`. This create whitelist is intentionally **narrower** than the
element types the CLI will accept when reading an existing scene back
(`image`, `frame`, and `embeddable` are preserved on read/mutate but cannot be
minted by `create`, because the CLI cannot fabricate their required payloads).
Any other `--type` is rejected locally before the GET. Text-only
flags (`--text`, `--baseline`, `--font-size`, `--font-family`, `--text-align`,
`--vertical-align`, `--line-height`, `--bold`, `--italic`, `--underline`) are
rejected when set against a non-text `--type` rather than silently ignored. The
low-level `docs scene edit` command remains available as an escape hatch for
advanced batches.

### Friendly style, text, and native-shape options

`create` and `style` accept the same safe appearance and typography knobs; they
are validated locally and type-gated so a flag is never silently ignored:

- `--stroke-style solid|dashed|dotted`.
- `--roundness sharp|round` — maps to the Excalidraw contract: `sharp` → `null`,
  `round` → `{"type":3}` (ADAPTIVE_RADIUS) for rectangle/diamond and
  `{"type":2}` (PROPORTIONAL_RADIUS) for line/arrow. Rejected for ellipse/text.
- `--start-arrowhead` / `--end-arrowhead` (arrow/line only): `none` (→ `null`),
  `arrow`, `bar`, `dot`, `triangle`, `triangle_outline`, `diamond`,
  `diamond_outline`.
- Text typography (text only): `--font-family <positive int>`,
  `--text-align left|center|right` (there is intentionally **no** `justify`),
  `--vertical-align top|middle|bottom`, `--line-height` (finite, `0 < h ≤ 10`).
- `style` additionally takes the tri-state `--bold|--italic|--underline`, each a
  string `true|false` so an explicit `false` is representable. These are
  **deep-merged** into `customData` (unrelated `customData` keys, including
  nested objects, are preserved — never replaced).
- `create --type rectangle` accepts all 19 backend-native kinds via
  `--native-shape-kind`: `square`, `database`, `notched-dovetail`, `chevron`,
  `parallelogram`, `trapezoid`, `speech-bubble`, `speech-bubble-rounded`,
  `triangle`, `inverted-triangle`, `circle`, `right-triangle`, `star`,
  `hexagon`, `pentagon`, `octagon`, `left-arrow`, `right-arrow`, and
  `bidirectional-arrow`. The value is stored as `customData.nativeShapeKind` and
  rejected for every other `--type`.
- Database shapes additionally accept `--database-rim-ratio <0.06..0.4>` on
  create. To change it later, use `element update ... --database-rim-ratio R`;
  this strictly validates the range, requires a database native shape, and
  deep-merges `customData` instead of discarding unrelated keys.

### Linear geometry and arrow routing

`create --type line|arrow` and `element linear <docId> <elementId>` accept
`--points` as a JSON array of two or more finite `[x,y]` local points. The input
may be inline JSON, `@file`, or `@-` (stdin); trailing JSON, non-finite/extreme
coordinates, malformed tuples, and fewer than two points are rejected before any
GET/PATCH. Inputs are canonicalized to Excalidraw's `points[0] == [0,0]`
invariant: the original first-point offset is added to element `x/y`, and all
local companion coordinates (including fixed segment endpoints) are translated
with the points, preserving world geometry. Width/height are then derived from
the normalized local bounds.

For arrows, `--arrow-type sharp|round|elbow` maps to straight sharp, proportional
rounding, or Excalidraw elbow routing. An elbow's points must form non-zero,
axis-aligned segments. `--fixed-segments` is a safe JSON/`@file`/`@-` array and is
accepted only together with `--arrow-type elbow`; each entry is exactly
`{"start":[x,y],"end":[x,y],"index":n}`, with a unique segment index in
`[1, point-count-1]`. Its endpoints must exactly equal `points[index-1]` and
`points[index]`, and the segment must be non-zero and axis-aligned. Switching an
arrow back to sharp/round clears stale elbow segments. The mutation remains one GET plus one CAS PATCH and preserves every
unknown element field.

`element bind <docId> <arrowId> --endpoint start|end --element-id <target>
--focus <-1..1> --gap <non-negative>` and `element unbind ... --endpoint
start|end` safely maintain both sides of an arrow binding. Each command performs
exactly one scene GET and one `If-Match` PATCH containing the arrow and target;
it updates `startBinding`/`endBinding` together with the target's reciprocal
`boundElements` arrow entry, bumps both element versions, and preserves unknown
fields. Malformed/dangling/conflicting references, a non-arrow source, a
non-bindable target, bound label text, duplicate ownership, and rebinding an
occupied endpoint are fail-closed. Unbind also requires the reciprocal entry to
exist and removes both sides atomically.

### Freedraw geometry

`create --type freedraw` and `element freedraw <docId> <elementId>` expose the
canonical Excalidraw stroke fields: `--points`, `--pressures`,
`--simulate-pressure`, and `--last-committed-point`. JSON values accept inline,
`@file`, or `@-`; points are finite `[x,y]` tuples (at least one, at most 100000,
coordinates bounded to ±10000000), pressures are finite samples in `[0,1]` and
must be empty or exactly match the point count, and the last committed point is
`[x,y]` or `null`. Points are normalized to start at `[0,0]`; the first-point
offset moves into element `x/y`, and a non-null `lastCommittedPoint` is translated
with them so world geometry is unchanged. Create requires points and emits a
completed stroke (`lastCommittedPoint:null`); update preserves every unknown
field and uses the same one-GET/one-CAS-PATCH version/index framework as other
semantic mutations.

### Text content & rich runs (`element text`)

`element text <docId> <elementId>` updates a standalone text element's content,
base typography, and rich-text runs in one guarded read-modify-write. Runs are
stored under **`customData.textRuns`** (schema v3), never as a top-level field. It
refuses a non-text target (so runs can never land on a shape) and preserves
unrelated `customData` keys. An element read back with a legacy top-level
`textRuns` field is rejected loudly rather than re-emitted as malformed dual state.

```bash
# Replace content + base typography (geometry is NOT recomputed — the CLI cannot
# reproduce Excalidraw measurement; adjust width/height/baseline via transform).
octo-cli docs scene element text <docId> <id> --text "New label" --font-family 2 --text-align center

# Rich runs: inline JSON array or @file / @- (stdin).
octo-cli docs scene element text <docId> <id> --runs '[{"start":0,"end":5,"bold":true},{"start":5,"end":9,"color":"#e03131"}]'
octo-cli docs scene element text <docId> <id> --runs @runs.json
octo-cli docs scene element text <docId> <id> --runs '[]'   # clears customData.textRuns
```

Each run is a half-open `[start,end)` range in **UTF-16 code units** (an emoji or
other non-BMP character counts as two), with optional `fontFamily` (positive
integer), `fontSize` (positive finite), `color` (a bounded CSS color of at most 32
characters — hex `#rgb`/`#rgba`/`#rrggbb`/`#rrggbbaa`, a functional
`rgb()`/`rgba()`/`hsl()`/`hsla()`, or a color keyword such as `transparent`,
matching the shared whiteboard-schema contract), and `bold`/`italic`/`underline`
booleans (an explicit `false` is honoured). Unknown run fields are
rejected. Runs are normalized deterministically before the PATCH: ranges are
**clamped** to the current text length (against the *new* content when `--text`
is also set), **sorted** by start, made **non-overlapping** with **later-wins**
semantics (a run later in the array overrides earlier runs on the shared span),
and **adjacent runs with identical styling are merged**. A non-empty array whose
runs all fall outside the text is an error; an empty array clears
`customData.textRuns` (and drops `customData` entirely when nothing else remains).

Mutations fail loud on tombstones and corruption: `transform`, `style`,
`update`, `layer`, and `delete` reject a target whose `isDeleted` is `true`
(a repeated `delete` therefore does not issue a PATCH), and any command refuses a
scene that contains two live (non-deleted) elements sharing one id. `layer
--relative-to` cannot reference the element being moved.

Text creation is fail-closed: `--width`, `--height`, and `--baseline` are all
required because this CLI cannot reproduce Excalidraw's font measurement
exactly. Supply geometry measured by the caller/renderer; the CLI does not claim
to calculate it. `--font-size` (on both `style` and `text`) is supported only for
standalone, single-line text with `autoResize=true`, no `containerId`, and no
`boundElements`; in that narrow case width, height, and baseline are scaled by
the font-size ratio. Other text is rejected rather than assigned unreliable
geometry. Changing text content with `element text --text` likewise does **not**
recompute geometry — use `transform` to resize afterwards. Resizing a line/arrow
is rejected when it has bindings, bound elements, elbow/fixed-point/fixed-segment
state, or more than two points.

`element update` accepts only a non-empty JSON object containing `customData`,
`link`, and/or `locked`. It cannot mutate structural or managed fields, and a
no-op update is rejected without version bump or PATCH.

## Web embeds

Create a native Excalidraw `embeddable` from an absolute HTTP(S) URL. This uses
the ordinary scene element/CAS path—no duplicate embed API:

```bash
octo-cli docs scene element create BOARD --type embeddable \
  --url 'https://example.com' --x 100 --y 100 --width 560 --height 315
```

The CLI rejects non-HTTP(S), relative, empty, and over-2048-byte URLs before any
write. Whether a remote site permits iframe display remains browser/site policy.

## Board comment anchors

The generic comments API is reused, with friendly flags that encode the Web
board's canonical version-1 anchors:

```bash
# Point comment
octo-cli docs comments add-board BOARD --body 'Check this area' --point 120,240

# Single or multi-element comment (repeat --element-id)
octo-cli docs comments add-board BOARD --body 'Review this group' \
  --element-id box1 --element-id arrow2
```

Element IDs are resolved against the live scene. Bound text normalizes to its
container; a single element anchors at its top-right corner, while a multi-
selection anchors at the common bounding-box centre, matching the Web board.

## Portable `.excalidraw` save

```bash
octo-cli docs save-excalidraw BOARD -o board.excalidraw
```

This reads the live scene and writes a version-2 Excalidraw envelope. Referenced
board attachments are resolved to fresh signed URLs, size-bounded, downloaded,
and embedded as portable `dataURL` file entries, so `docs import BOARD --file
board.excalidraw` can round-trip the scene. The destination is written with mode
`0600` because it may contain embedded image bytes.

## Canvas background

Get, set, or reset the authoritative canvas background color
(`appState.viewBackgroundColor`) of a board. The value lives in the same Y.Doc
`appState` the live collaborative editor writes, so a change made here appears in
`docs scene get`, survives reopen and collaboration, and — when the backend
honors it — is reflected in SVG/PNG export (Excalidraw draws a full-canvas
background rect from `viewBackgroundColor` when `exportBackground` is on).

```bash
# Show the current color (falls back to the default #ffffff when unset).
octo-cli docs scene background get <docId>

# Set a color. Accepts a bounded CSS color (<= 32 chars): hex
# #RGB/#RGBA/#RRGGBB/#RRGGBBAA, a functional rgb()/rgba()/hsl()/hsla(), or a
# color keyword such as `transparent`. Anything else is rejected locally.
octo-cli docs scene background set <docId> '#f8f9fa'
octo-cli docs scene background set <docId> 'rgba(255,0,0,0.2)'

# Reset to the default #ffffff (writes the explicit default, not transparent/null,
# matching the frontend color picker's clear behavior so all peers converge).
octo-cli docs scene background reset <docId>
```

`set`/`reset` read the scene for its base version and write under the same
optimistic-concurrency guard as `docs scene edit`: the base version is sent as
`If-Match`, and a stale write is rejected with `412 base_version_stale` (re-run
to retry). Use `--dry-run` to inspect the PATCH request (`appState.viewBackgroundColor`
+ `If-Match`) without applying it. An invalid color exits non-zero (validation)
before any request is sent.

## Find text

Search a board's text content, matching the frontend board search (Excalidraw
SearchMenu, Ctrl/Cmd+F):

```bash
octo-cli docs scene find <docId> "decision"
octo-cli docs scene find <docId> "TODO" --limit 20
```

Semantics (aligned with the editor, not a fake id-only lookup):

- Only **live** (non-deleted) elements of `type: "text"` are searched — this
  includes **bound labels** (a text element bound to a shape/arrow via
  `containerId`); the container shape itself is not matched.
- The query is trimmed, then matched against each element's **`originalText`**
  (the pre-wrap source) as a **case-insensitive literal substring**—no Unicode
  normalization, fuzzy matching, or regex.
- Every non-overlapping occurrence is returned, ordered by element `y`, then by
  scene order. `index`/`length` use JavaScript UTF-16 code units, matching Web
  offsets for emoji and other non-BMP characters.
- Each occurrence carries `textElementId`, `x`, `y`, and—when present—
  `containerId` (`bound: true`) and `frameId`.
- `--limit` (1..1000, default 100) bounds occurrences; `totalMatches` and
  `truncated` remain exact.

## Raw scene API

```bash
# Read the LIVE scene + baseVersion (reader). Response:
#   { docId, elements: [ <element> ], files: { <fileId>: {attachId} }, appState: { viewBackgroundColor }, schemaVersion, baseVersion }
octo-cli docs scene get <docId>

# Batch-edit the scene (writer). --base-version is REQUIRED and is sent as the
# If-Match header; the batch goes through --data as a JSON object.
#   elements          — full elements to upsert (CAS: higher `version` wins)
#   deletedElementIds — element ids to soft-delete (tombstone)
#   files             — file refs to upsert
# Every element MUST carry a valid fractional-index `index` (z-order key); the
# CLI rejects an index-less or malformed-index element locally before sending.
octo-cli docs scene edit <docId> --base-version "<token>" \
  --data '{"elements":[{"id":"e1","type":"rectangle","version":4,"index":"a0"}],"deletedElementIds":["e2"],"files":{}}'
```

> **`index` is mandatory on every upserted element.** It must be a valid
> fractional-index (z-order) key — the same jitterbug / Excalidraw key format the
> board uses, generated with the `fractional-indexing` rules (e.g. `a0`, `a1`,
> `a0V`). Never omit `index`, and never fabricate one as an `r`+digits string, a
> plain integer, or a timestamp: an index-less element used to slip through to a
> buggy backend repair path that rewrote it into an invalid key (like
> `r00000003`) and broke the board. The CLI now rejects such elements with a
> non-zero exit before the request is sent.

The upsert is element-level: only the ids named in `elements` / `deletedElementIds`
are touched (the rest of the board is left as-is). On a key collision the element
with the higher `version` (then smaller `versionNonce`) wins, and a delete is a
soft-delete tombstone with a superseding version so it converges under CAS.

## Excalidraw file import

Import a standard `.excalidraw` JSON envelope (maximum 25 MiB) directly into an
existing board. The CLI validates `type: "excalidraw"`, `version: 2`, an
`elements` array, and a `files` object before sending the request.

```bash
# Safe default: merge imported elements while preserving the existing board.
octo-cli docs import <docId> --file ./diagram.excalidraw

# Explicit overwrite: replace the board with the imported scene.
octo-cli docs import <docId> --file ./diagram.excalidraw --mode replace
```

`--mode` is only valid for `.excalidraw` imports and accepts `merge` (default) or
`replace`. Merge preserves existing board elements. Replace explicitly overwrites
the board; the backend first creates a safety snapshot and enforces concurrency
protection. Use `--dry-run` to inspect the mode, endpoint, and these safety
semantics without applying the import.

**Concurrency / errors.** The base version is optimistic-concurrency: if the scene
changed since your `docs scene get`, an edit is rejected with
`412 base_version_stale` — re-read for a fresh token. Other gates:
`409 unsupported_doc_type` (target is a doc/sheet), `409 board_snapshot_invalid`
(the live scene decodes to a wrong-kind/corrupt blob, on read),
`413 too_many_elements` / `413 element_too_large` / `413 doc_too_large` (size
caps), `400 invalid_body` (missing base version or malformed shape),
`422 board_element_invalid` (an element fails the whitelist), and
`422 board_file_invalid` (a file ref carries no usable `attachId`).

## Whiteboard image export

Render a whiteboard's **live** Excalidraw scene to an image on the server. The
response body is binary, so `--output`/`-o` is required and saves it atomically
to the matching `.png` or `.svg` destination.

```bash
# PNG. -o writes the bytes to disk and the envelope echoes the saved path.
octo-cli docs export <docId> --export-format png -o board.png

# SVG (vector).
octo-cli docs export <docId> --export-format svg -o board.svg
```

> `--export-format` is `png` or `svg`; any other value is rejected locally.
> (The flag is named `--export-format`, not `--format`, so it
> does not collide with the global `--format` output-envelope flag; the wire
> query parameter is still `format`.) The export reflects the scene as it is live
> right now (shapes, text, and embedded images), not a persisted snapshot.
> `-o` overwrites an existing destination file.

## Schema lookup

```bash
octo-cli schema docs.scene.get
octo-cli schema docs.scene.edit
```

### Web toolbar presets, frames, and structural text

`create --preset` exactly covers every persistable slot exposed by the current
Web shape and line flyouts (21 shape slots + 4 line slots; the primary buttons
repeat rectangle/arrow rather than adding distinct persisted shapes):
`rounded-rectangle`, `ellipse`, `diamond`, `square`, `circle`, `triangle`,
`parallelogram`, `database`, `notched-dovetail`, `chevron`, `trapezoid`,
`speech-bubble`, `speech-bubble-rounded`, `right-triangle`, `star`, `hexagon`,
`pentagon`, `octagon`, `left-arrow`, `right-arrow`, and `bidirectional-arrow`;
plus line presets `curved-arrow`, `elbow-arrow`, `straight-arrow`, and
`straight-line`. Without explicit `--points`, curved arrows use the Web toolbar's
three-point normal-offset curve and elbow arrows use a four-point orthogonal
route; explicit points remain authoritative. `rectangle` (toolbar primary) and
`inverted-triangle` remain
accepted compatibility aliases. All 19 native kinds persist in
`customData.nativeShapeKind`. The native polygon presets persist as a
rectangle plus `customData.nativeShapeKind`; aliases are strictly validated and
cannot be mixed with lower-level `--type`, `--roundness`, `--arrow-type`, or
`--native-shape-kind`.

```bash
octo-cli docs scene element create BOARD --preset triangle --x 10 --y 10 --width 120 --height 80
octo-cli docs scene element create BOARD --preset elbow-arrow --points '[[0,0],[80,0],[80,60]]'
octo-cli docs scene element create BOARD --preset database --database-rim-ratio 0.3
octo-cli docs scene element update BOARD DATABASE_ID --database-rim-ratio 0.35
octo-cli docs scene element frame-create BOARD --id F --x 0 --y 0 --width 800 --height 600
octo-cli docs scene element frame-add BOARD F --id E1 --id E2
octo-cli docs scene element frame-remove BOARD F --id E1
octo-cli docs scene element unframe BOARD --id E2
octo-cli docs scene element bind-text BOARD TEXT CONTAINER
octo-cli docs scene element unbind-text BOARD TEXT
```

`bind-text` uses Excalidraw's native reciprocal semantic binding for rectangle,
ellipse, diamond, and arrow containers. When the target is a plain `line`, the
command atomically converts it to a visually identical headless arrow
(`type:"arrow"`, null start/end arrowheads) and binds the text through the same
`containerId`/`boundElements` contract. Conversion fails closed if the source
line carries arrowheads, elbow/fixed routing, endpoint-special state,
`polygon:true` (or malformed polygon state), or other unsupported routing data;
no PATCH is sent in that case. Linear-label display geometry is owned by the
patched Web renderer because it depends on browser path layout and font metrics.
The CLI therefore preserves the text's existing `x/y`, does not invent a
midpoint or mutate the text/version during `linear` and `transform` operations,
and updates only the container. `unbind-text` from a linear container fails
closed before PATCH: without browser-resolved display geometry, retaining stale
persisted coordinates would make the standalone text jump. Shape-container
binding and unbinding remain supported. Unlabelled plain lines remain
`type:"line"`.

Frame and bound-text operations validate the full live structural graph and use
one CAS PATCH. Text binding maintains both `text.containerId` and the reciprocal
`container.boundElements[{type:"text"}]`; unbinding clears both.

Transforms also accept relative `--dx/--dy`, relative degree rotation via
`--rotate-deg`, and positive proportional `--scale`. The same flags work with
`transform-many`; each selected structural unit must still be complete.

Board text `--font-family` uses the Web catalogue directly: stable ids
`2001..2026` or names such as `arial`, `simsun`/`宋体`, `pingfang-sc`,
`courier-new`, and `calibri`. New text defaults to `arial` (`2001`). Legacy ids
`1/2/3` may survive in imported/raw scene data but are rejected for new friendly
CLI writes.

### Explicit contract blockers

- Image placement is supported by `POST .../scene/images`. No backend image-edit
  route exists, and the shared board contract has no validated crop/flip/replace
  operation (nor binary replacement transaction), so the CLI does not pretend to
  provide image crop/scale/flip/replace. Generic scene transforms can resize an
  image element, but are not advertised as intrinsic image editing.
- The frozen shared board `textRuns` style key set is exactly
  `fontFamily,fontSize,color,bold,italic,underline`; it has no `strike`. The Web
  renderer also only consumes those run keys. Board text alignment accepts only
  left/center/right in the renderer contract. Therefore board element/run
  strike and justify remain blocked despite similarly named rich-document
  (ProseMirror) features in the Docs backend.
- The CLI exposes the Mermaid import **transport contract** through
  `docs scene import-mermaid <docId>`. Pass exactly one of `--file <path>`
  (`--file -` reads stdin) or `--source <text>`, plus optional
  `--mode merge|replace` (default `merge`). The CLI validates non-empty UTF-8
  source up to 100,000 characters before POSTing it as `text/vnd.mermaid`;
  `--dry-run` reports source kind and size without exposing source text. Actual
  conversion remains backend-dependent: deployments without a Mermaid converter
  cannot complete the import. The CLI assumes no specific converter or Chromium.
