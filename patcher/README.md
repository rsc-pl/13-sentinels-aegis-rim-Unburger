# 13s-patcher — 13 Sentinels Unburger Patch Builder

`13s-patcher` builds the patched game archive for **13 Sentinels: Aegis Rim**
from the project's reviewed English text changes. It applies only the listed
changes, byte-verifies the result and writes the archive atomically. It never
modifies its inputs.

- **Inputs:** the user's own game containers — the US base NSP and the matching
  1.10 update NSP (plus the user's key files), the public
  [`translations/`](../translations) dataset and a small verified cache. The
  cache is populated from the containers: the update overlay, the base text
  banks and the game members the build consumes (the plate scripts and title
  atlas) are all extracted automatically.
- **Output:** a patched update overlay archive plus one installable archive per
  target (Atmosphère and/or emulator), written into a separate output
  directory.
- Everything is vendored, so it builds and runs **offline**.

The tool is fully integrated: it discovers the local containers, reuses or
prepares a verified extraction cache, reads the translation dataset directly and
builds — without asking for the same paths every run.

## Requirements

- Go 1.26+ to build (no network needed: dependencies are vendored).
- A legitimate copy of the game: the US base NSP and the matching 1.10 update
  NSP (and the key files in `.local/keys/` needed to rebuild the cache from
  them).
- `nstool` and `hactool` on `PATH` to rebuild the cache from the containers
  (both are required: the update carries a patch RomFS read against the
  decrypted base NCA), or a configured `extractor_command`.
- The public translation dataset (shipped in `translations/` as
  `changes.jsonl` + `manifest.json`).

## Build

```sh
make build              # -> bin/13s-patcher
make test               # unit + integration tests (synthetic data only)
make vet
make hygiene            # full public/release boundary scan + hygiene tests
make hygiene-changed    # fast after-edit scan of changed/untracked files
make release-check      # isolated release privacy boundary check
make isolation          # build & test a clean copy with no parent project
```

From the repository root, `../run-patcher.sh` builds the tool once (rebuilding
whenever the module content — sources, module files or the vendored tree —
changed) and forwards every argument,
so `./run-patcher.sh build` is equivalent to `./bin/13s-patcher build`.

## Quick start

```sh
./bin/13s-patcher setup        # discover inputs once, prepare the cache, write
                               # the settings
./bin/13s-patcher preflight    # validate everything without building
./bin/13s-patcher build        # build the patched archive + per-target archives
./bin/13s-patcher validate     # preflight + build + verify + inspect
```

Running the binary with **no arguments** opens the interactive dashboard when
stdin/stdout are a terminal; when piped it prints the plain-text status.

## Targets and outputs

`build` and `validate` accept `-target atmosphere|emulator|both` (default
`both`). One rebuild produces the same patched bytes; packaging only changes the
directory layout each loader expects. The rebuilt archive is an intermediate kept
in the work cache (`.local/13s-patcher/work/`); the installable archives are the
outputs, written into `.local/13s-patcher/out/`. `<release>` is the mod version
from the repository-root `version.txt` (see below) unless `release_version` is set
explicitly:

| archive | layout | install |
| --- | --- | --- |
| `13-Sentinels-Patch-<release>-Atmosphere.zip` | `atmosphere/contents/<title id>/romfs/ROBO_PATCH_US.CPK` + `atmosphere/exefs_patches/UnburgerMod/<build id>.ips` | copy `atmosphere/` to the SD-card root |
| `13-Sentinels-Patch-<release>-Emulators.zip` | `13 Sentinels Aegis Rim - Unburger Mod/romfs/ROBO_PATCH_US.CPK` + `13 Sentinels Aegis Rim - Unburger Mod/exefs/<build id>.ips` | extract `13 Sentinels Aegis Rim - Unburger Mod/` as a mod folder |

Both archives carry the identical rebuilt `ROBO_PATCH_US.CPK` and the identical
executable patch. `-force` replaces existing files; without it a build refuses
to overwrite. In the interactive dashboard a build that would overwrite an
existing archive instead opens a dialog with **Replace / Keep existing /
Cancel**; **Keep existing** reuses valid archives and builds only what is
missing, and nothing is overwritten until you choose.

## Storage modes

| mode | when | settings & state |
| --- | --- | --- |
| project | the tool runs inside a project checkout (a tree containing the project's private data directory) | everything lives in the project's private directory; no per-user file is created |
| user | a fresh checkout without that directory | the per-user settings file (XDG on Linux/macOS, AppData on Windows) with an optional `./.13s-patcher.json` override |

Exactly one settings file is authoritative per mode. A per-directory override
found on the first project run is adopted into the project settings once and not
used again.

## Commands

| command | purpose |
| --- | --- |
| `status` | detected inputs, translation data, cache, output, last results |
| `doctor` | environment, paths, storage mode and configuration |
| `setup` | discover local inputs and write the settings once |
| `sources` | locate and identify the local game containers |
| `preflight` | validate inputs, translation data, cache and output without building |
| `build` | rebuild the archive, then produce the per-target archives (`-force` to replace) |
| `package` | re-produce the per-target archives from an existing built archive |
| `validate` | preflight + build into a temp dir + verify + inspect |
| `inspect` | inspect a produced archive or the rebuilt archive |
| `version`, `help` | version / usage |

All commands accept `-json FILE` (or `-json -` for stdout) and `-target
atmosphere|emulator|both`.

## How the inputs are resolved

1. **Dataset** — [`translations/`](../translations) is loaded and verified: the
   dataset layout version, the declared game identity, the record and bank
   counts, the per-bank digests and the digest of `changes.jsonl` are all
   checked. A missing or modified dataset is refused; it is never reconstructed.
2. **Cache** — a small verified cache holds the effective update overlay archive,
   the base text banks it does not itself carry, and the base game members the
   build consumes (the plate scripts and the title atlas). The cache is reused
   when its digests match; otherwise it is rebuilt from an existing extraction
   or from the local containers with the built-in decoder. It never duplicates
   the multi-gigabyte containers.
3. **Plan** — every dataset record is resolved against the effective archive
   (or the base bank directory) and its baseline text and control tokens are
   checked. A mismatch aborts the build.
4. **Build** — the plan is applied, the output is re-parsed, every unmodified
   member is proven byte-identical, and only then is the archive written.

See [`docs/SOURCES.md`](docs/SOURCES.md) for the container and cache model, and
[`docs/EXPORTING.md`](docs/EXPORTING.md) for the lower-level patch-data bundle
format.

## Configuration

Settings are JSON. Relative paths resolve against the working directory. Only
paths are stored — key files are never read, copied or logged.

```json
{
  "roms_base": "/path/to/base",
  "roms_update": "/path/to/update",
  "keys_dir": "/path/to/keys",
  "translations_dir": "translations",
  "cache_dir": "/path/to/state/cache",
  "output_dir": "/path/to/out",
  "output_name": "",
  "encoder": "chain",
  "extractor_command": [],
  "target": "both",
  "release_version": "",
  "plate_base": "",
  "allow_missing_resources": false,
  "title_branding": true
}
```

`target` selects the installation layouts (`atmosphere`, `emulator` or `both`).
`release_version` labels the output file names; when it is empty the mod version
from the repository-root `version.txt` is used. The added presentation page uses
the project's own plate, shipped at
[`assets/images/end_route_plate.png`](assets/images/end_route_plate.png), so a
standard build needs no external image. `plate_base` is an optional replacement
(explicit path, or `plate_base.png`/`.jpg` in the tool's `resources/`
directory); when an explicit replacement is configured but absent the build
fails closed, and `allow_missing_resources=true` is the explicit opt-out that
records a partial build instead. See
[`docs/BOUNDARY.md`](docs/BOUNDARY.md).

The in-game title-screen footer is rebranded by a second added resource: the
patch rewrites only the copyright line inside the game's title texture and
renders `Unburger Mod v<version>` there. It is part of a normal build. The title
texture itself is game-owned artwork, so the tool never ships it: the logical
`GUI/title.ftx` member is extracted from your own base container into the
verified cache, and a `GUI__title.ftx` in the tool's `resources/` directory
overrides it. Set `title_branding` to `false` to leave the footer out.

The footer is drawn by the patch, not copied: the game's line is a
white-on-transparent coverage strip, so the label is composed on a fully
transparent canvas — white glyphs with a subtle translucent dark outline,
centred in the lower title area — and only the 4×4 BC7 blocks of the label's
rectangle are rewritten, so no opaque backing and no game artwork leaks into the
result. The label is drawn with the display face
[`assets/fonts/unburger.otf`](assets/fonts/unburger.otf) and the style in
[`assets/fonts/title_footer.toml`](assets/fonts/title_footer.toml), both read
from disk on every build: replace the `.otf` or edit the `.toml` and the next
build changes — no recompile and no dashboard restart. The shipped face is a
complete OpenType/CFF face whose character map covers the whole
`Unburger Mod v<version>` label, so the footer is drawn with it. One face is used
for the whole label, never a mixture; if a label ever contains a rune the face
lacks, the whole label falls back to the tool's built-in bitmap face and the
build report names the face and the missing runes — the substitution is never
silent. An unknown style key, a wrong type or an out-of-range value fails the
build, and a label that would not fit the footer rectangle fails rather than
clipping.

**Runtime status (2026-10-19).** A reference composition is confirmed working:
the base atlas stored **RAW** (`FileSize == ExtractSize`, exactly how the base
game stores this member) with the composed footer reached the title screen and
displayed the footer in an Eden run. That run used the face recorded as
`3c32dc0f…` with the built-in style (`letter_spacing 0`). The face and style
shipped today (`cf1e64aa…`, `letter_spacing 0.2`) have not been run in the game,
so a build of the shipped composition is reported as runtime-unverified. Earlier,
the CRILAYLA-compressed builds failed at the title transition (the intro played,
the menu was never displayed) while a build without the member booted to the
menu; a single-variable RAW build isolated the storage shape as the observed
lead, though the loader rule behind it is not proven. The build compares the live
face digest and style with the recorded reference and reports whether it is the
reference composition, the shipped composition or a user-modified one — never
silently assuming the confirmation. Any restyling through
`title_footer.toml`, or a replaced face, changes the artwork; every build report
records the renderer version, the face digest and the cache identity so the
exact inputs are provable.

The dashboard exposes the choice directly: the **Settings** pane has an
`in-game title branding` row (`enter` or `space` toggles it) and the **Build
Patch** pane toggles it with `T`. Enabling after it was turned off shows a
one-time warning and is written to the settings file; the Build Patch pane and
every build/validate result state `Enabled` or `Disabled`, the footer label
rendered from `version.txt`, the member's storage shape (RAW) and the runtime
state. A missing title atlas (cache and resources), face or style fails
preflight with an actionable message and is never silently disabled.

### Story-plate name order

Every protagonist plate (`The Story of / <given> / <surname>`) is a baked-sprite
composition, so the order is corrected in the per-character script rather than in
the text dataset. A normal build adds the transformed logical
`GUI/change_<name>.mbs` for all 13 protagonists automatically; no extra command
is needed.

- The transform is patch-owned and tiny: for each character the two 7-keyframe
  name groups of the FMBS S4 table exchange their `ty` floats (surname to the
  upper slot, given name to the lower one). A constant per-character `tx` offset
  on the given-name group then aligns the lower line's left edge with the
  surname's above it, so a word that started further left in the base plate does
  not hang outside the block. Zero is a deliberate value where the base lines are
  already flush or already inset; every value and its rationale is recorded in
  `internal/techdata/data/patch.json` (`given_tx_shift`, `given_tx_basis`).
  Fuyusaka's `+53 px` is the reference look; Hijiyama's `+140.5 px` is
  the measured rightward push.
- The same member also carries the progressed / mode-select presentation of the
  same two names (no `The Story of` caption): a second pair of 7-keyframe groups
  in the same S4 table, with its own fade/scale curve. The patch applies the
  identical slot exchange and left-edge alignment to that pair (`second_pair` in
  the descriptor); the two pairs are required to be disjoint. Hijiyama's
  progressed pair uses `+190.5 px`, derived from the captured progressed-flow
  frame. The progressed pair also carries a whole-block `pair_tx_shift` that is
  added to both groups on every keyframe: it moves the complete two-line block
  out of the black Japanese calligraphy without disturbing the lower-line
  alignment (`pair_tx_basis` records the capture-pixel to native conversion).
  It is refused on the story-introduction pair, which keeps the reference layout.
- Everything else in the member is preserved byte-for-byte and the build proves
  it: FTX atlases, UVs, local bounds, scale/alpha ramps, clip data, node mapping,
  header/footer and timing are untouched; only the intended `ty` and `tx` floats
  differ.
- The base scripts are game-derived and never shipped. They are extracted from
  your own base container into the verified cache (as `GUI__change_<name>.mbs`,
  the logical member path with `/` replaced by `__`); a file with that name in
  the tool's `resources/` directory overrides the extracted one. The build
  verifies each script against its pinned size and SHA-256 before patching it,
  so a wrong revision is refused instead of producing a corrupt plate.
- The production shape requires all 13 scripts: a missing one fails preflight and
  refuses the build, so a partial plate set is never shipped silently. The only
  exception is the explicit `allow_missing_resources=true` opt-out, which records
  the omission as a warning and in the per-resource status/build report. A present
  but mismatching or structurally invalid script always fails the build.
- Runtime state: the Fuyusaka order (`FUYUSAKA` above `IORI`) is confirmed on a
  running build (2026-10-31), and the `+53 px` alignment is the reference
  composition. The other 12 plates and the combined build are
  static/build-verified and pending a confirmed run.

## Mod version

The Unburger Mod version is the single line in **`version.txt`** at the
repository root (`../version.txt` from this module). It is the one authoritative
source: the dashboard shows `Unburger Mod v<version>` beneath the menu, the
`version` and `status` commands report it, and it is the default release label
used in the output file names. Edit the file and reopen or refresh the dashboard
to change the displayed version — no source edit or rebuild is needed.

The value is validated when read: a missing, empty or malformed file is reported
with an actionable diagnostic and never replaced by an invented number. The mod
version is deliberately separate from the patcher application version
(`internal/build`, set with `make VERSION=...`), from the translation dataset
schema and from the supported game update revision.

See [`examples/config.example.json`](examples/config.example.json).

## Guarantees

- The input containers, key files and the dataset are never modified.
- The output is written atomically and is never overwritten without `-force`.
- Success is reported only after the rebuilt archive re-parses, every untouched
  member is byte-identical and every output constraint passes.
- Any missing or inconsistent input fails closed with an actionable message.

## Documentation

- [`docs/USER_GUIDE.md`](docs/USER_GUIDE.md) — step-by-step usage.
- [`docs/SOURCES.md`](docs/SOURCES.md) — local containers, keys and the cache.
- [`docs/PATCH_DATA.md`](docs/PATCH_DATA.md) — patch-data bundle specification.
- [`docs/EXPORTING.md`](docs/EXPORTING.md) — authoring a patch-data bundle
  (advanced; the translation dataset itself is published by the maintainer).
- [`docs/BOUNDARY.md`](docs/BOUNDARY.md) — package layout and design notes.

## Licence / content

No game data, keys or third-party patch content is distributed with this tool.
The repository ships the project's own reviewed text changes and synthetic
fixtures only. Distributing a payload is the responsibility of whoever prepares
and signs off on it.
