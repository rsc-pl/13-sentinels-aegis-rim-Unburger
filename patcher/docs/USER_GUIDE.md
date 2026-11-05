# User guide

This guide walks through building the patched archive with `13s-patcher`, using
your own legitimate game containers and the shipped translation dataset.

> The tool never modifies its inputs. It reads the game containers, the
> translation dataset and a small verified cache, and writes a patched archive
> into a separate output directory.

Running the binary with no arguments opens the interactive dashboard on a
terminal; every action below is also available as a subcommand.

## What you need

- Your own legal copy of the game as NSP containers: the US base title
  (`01003FC01670C000`) and the matching 1.10 US update (`01003FC01670C800`).
  Only that base and update revision are supported; anything else is refused.
- Your own key files in `.local/keys/` (`prod.keys`, and `title.keys` if your
  setup needs it). Only their presence is ever checked; contents are never read,
  copied or logged.
- `nstool` and `hactool` on your `PATH`, or a configured `extractor_command`,
  so the tool can rebuild its cache from the containers. Both helpers are used:
  the update carries a patch RomFS that is read against the decrypted base NCA.
- Go 1.26 or newer to build the tool (dependencies are vendored; no network).

## 1. Get the tool

```sh
make build          # -> patcher/bin/13s-patcher
# or, from the repository root:
./run-patcher.sh    # builds the tool once, then opens the dashboard
```

`run-patcher.sh` rebuilds the tool whenever the module content changed (Go
sources, module files and the vendored tree are hashed into a stamp next to the
binary), forwards every argument and preserves the exit code, so
`./run-patcher.sh build` runs `13s-patcher build`.

## 2. First run: discover your inputs

```sh
./bin/13s-patcher setup
```

`setup` looks for the supported title's containers and for the key files, records
what it found in the settings file, and reports anything still missing. It never
reads or copies key contents.

Inside a project checkout the settings and all derived state live in the
project's private directory; in a fresh checkout they live in the per-user
settings directory. Either way you only configure this once.

To see exactly what was found:

```sh
./bin/13s-patcher sources
```

Containers are recognised by size and SHA-256, never by file name.

## 3. Preflight

```sh
./bin/13s-patcher preflight
```

Preflight checks, without building:

- the translation dataset (`translations/changes.jsonl` + `manifest.json`) loads
  and verifies;
- the local containers are identified (their absence is a warning when a
  verified cache already exists);
- key files are present (presence only);
- the verified cache is ready — or what is needed to prepare it;
- every reviewed change resolves against the effective archive with matching
  baseline text and control tokens;
- the output is a separate file and its directory is usable.

Exit code is `0` only when everything is ready.

## 4. Build

```sh
./bin/13s-patcher build                              # both targets; refuses to overwrite
./bin/13s-patcher build -force                       # replace existing outputs
./bin/13s-patcher build --target emulator -force     # one target only
```

`--target` accepts `atmosphere`, `emulator` or `both` (default `both`). On the
first build the tool prepares the cache (reusing an existing extraction or the
configured extractor), then applies the dataset. Progress is printed; a
cancelled run leaves no partial archive.

On success you get each output path, its size and digest, how many changes and
members were applied, and confirmation that the unmodified members are
byte-identical. Add `-json FILE` for a machine-readable report.

### Outputs and installing them

One build produces the same patched bytes and packages them for each target. The
installable archives are the outputs, written into `.local/13s-patcher/out/`.
`<release>` is the mod version from the repository-root `version.txt` unless
`release_version` is set in the settings:

| archive | install |
| --- | --- |
| `13-Sentinels-Patch-<release>-Atmosphere.zip` | copy its `atmosphere/` folder to the root of your SD card. Atmosphère loads the `atmosphere/contents/<title id>/romfs/` archive and the `atmosphere/exefs_patches/UnburgerMod/<build id>.ips` patch. |
| `13-Sentinels-Patch-<release>-Emulators.zip` | extract the `13 Sentinels Aegis Rim - Unburger Mod/` folder into the emulator's mod directory. It holds `romfs/` and `exefs/` directly (no `atmosphere/` nesting). |

Both archives carry the identical rebuilt `ROBO_PATCH_US.CPK` and the identical
executable patch. `validate` proves this and also proves a second pass produces
byte-identical archives.

The rebuilt archive itself is an intermediate kept in
`.local/13s-patcher/work/`, never among the outputs, so a repeated build does not
collide with its own working file.

The added end-of-route presentation page is composed deterministically from the
project's own presentation plate, shipped in `assets/images/`, plus the patch's
own overlay artwork. No external image is required for a standard build; the
optional replacement is described under Settings.

## 5. Validate

```sh
./bin/13s-patcher validate
```

Validation runs preflight, builds into a temporary directory, re-parses and
verifies the result, and compares it with the source: changed, added, removed and
preserved members. Nothing is written to your output directory.

## 6. Inspect

```sh
./bin/13s-patcher inspect                    # inspect the configured output
./bin/13s-patcher inspect -archive out.CPK   # inspect a specific archive
```

Inspection lists the archive header, every member and the structural checks, and
(when the source is known) a per-member diff.

## Interactive dashboard

| key | action |
| --- | --- |
| `↑`/`↓` | move between actions |
| `enter` | run the selected action (or edit a Settings field) |
| `1`/`2`/`3` | choose the target: Atmosphère / Emulators / both |
| `T` | toggle in-game title branding (asks once when re-enabling it) on the build panes |
| `s` | run setup (discover inputs) |
| `L` | expand / focus the log (then `↑`/`↓` · `PgUp`/`PgDn` · `Home`/`End` scroll; `esc` closes) |
| `r` | refresh status |
| `esc` | cancel a running task; otherwise leave the pane and return to the overview |
| `?` | help |
| `q` / `ctrl+c` | quit |

The overview shows the storage mode, the dataset, the cache, the containers and
the key-file presence, together with the output location and the last results. A
running task can be cancelled with `esc`; a cancelled or failed task never leaves
a partial artifact.

### Overwriting existing outputs

The dashboard has no force key. When a build would overwrite an existing output
archive it opens a dialog first, listing each destination with its size, digest
and validation state:

| choice | effect |
| --- | --- |
| `Replace` | overwrite the existing archives with the new build |
| `Keep existing` | reuse every valid archive, build only the targets that are missing |
| `Cancel` | change nothing (the default) |

`←`/`→` or `tab` switch the choice, `enter` activates it and `esc` cancels. The
default is `Cancel`, so nothing is overwritten until you choose. Each archive is
written atomically, so a failed or cancelled build never truncates an existing
valid output.

## Settings

The **Settings** pane edits the settings file directly. `enter` starts editing
the focused field, `esc` stops editing, a second `esc` returns to the overview,
`tab` moves between fields and `ctrl+s` saves.

| field | meaning |
| --- | --- |
| `target` | installation layout: `atmosphere`, `emulator` or `both` |
| `roms_base`, `roms_update` | directories holding the containers |
| `keys_dir` | directory holding the key files |
| `translations_dir` | the public dataset directory |
| `cache_dir` | where the verified cache lives |
| `output_dir`, `output_name` | where (and as what name) the archive is written |
| `encoder` | codec encoder: `chain` (default) or `old` |
| `extractor_command` | argv template of the container decode helper |
| `plate_base` | optional replacement image for the added presentation page (the shipped project plate is the default) |
| `title_branding` | in-game title-screen footer: `Enabled` (default) or `Disabled` |

`release_version` labels the output file names (default: the mod version read
from the repository-root `version.txt`) and `allow_missing_resources`
(off by default, so an added resource is never dropped silently) can also be set
in the settings file. Everything can also be passed as a flag, e.g.
`-output /path -workdir /path -target emulator`.

### Presentation-page background

A complete build adds one end-of-route presentation page. The page is composed
by the patch over the presentation plate shipped in this repository,
`assets/images/end_route_plate.png` (relative to the module). A standard build
therefore needs no image from you.

A deliberate replacement is optional. Put `plate_base.png` (or `.jpg`) in the
tool's `resources/` directory, or set `plate_base` to its path. `doctor` prints
the state directory, so the exact `resources/` location is always visible.
Resolution order is: an explicit `plate_base`, then the `resources/` override,
then the shipped plate, then any matching image found in the search roots.

A configured replacement that does not exist fails closed with a message naming
the path; the tool never silently falls back to another image. The explicit
`allow_missing_resources=true` opt-out records the page as omitted and marks the
build as a partial build; it is never reported as a complete patch.

### In-game title branding

The footer is part of a normal build, so the `in-game title branding` row opens
as `Enabled`. `enter` or `space` toggles it; turning it back on after you turned
it off shows a one-time warning:

> The in-game title footer replaces the shipped copyright line on the title
> screen with "Unburger Mod v<version>". The 2026-10-19 Eden run confirmed a
> reference composition, not the face and title_footer.toml now shipped; a build
> of the shipped or a restyled composition is runtime-unverified until it is
> tested.

`enter` confirms and `esc` cancels (the selection stays `Disabled`). A missing
`title_branding` key means `Enabled`. The choice is written to the settings file
when you confirm it (or when you press `ctrl+s`); it is an ordinary settings
value, not a dashboard-only switch. The **Build Patch** pane shows the effective
selection, the footer text (`Unburger Mod v<version.txt>`) and whether the
composition is the confirmed default or a restyled, unverified one, and `T`
toggles the same setting there. If the base atlas, the display face or the style
is missing, preflight fails with a message naming the path — it is never
silently disabled.

The footer itself is drawn by the patch on a fully transparent canvas — glyphs
in the configured colour with a subtle translucent outline, centred — so the
title artwork shows through wherever there is no text and nothing is left behind
as a solid backing. The label is drawn with the display face and the style read
from `patcher/assets/fonts/` on every build: **`unburger.otf`** is a complete
OpenType/CFF face whose character map covers the whole `Unburger Mod v<version>`
label, and **`title_footer.toml`** sets the font size, letter spacing, text
colour/opacity, outline width/colour/opacity, offsets and background opacity.
Replace the face or edit the style and the next build changes — no recompile, no
dashboard restart. One face is used for the whole label, never a mixture; if a
label ever contains a rune the face lacks (a non-ASCII character, say) the whole
label falls back to the tool's built-in bitmap face, and the `build`/`validate`
reports and the resource line name the face that was used and the runes the face
lacks. An unknown style key, a wrong type or an out-of-range value fails the
build, and a label that would not fit the footer rectangle fails rather than
being clipped.

## Mod version

The Unburger Mod version is the single line in `version.txt` at the repository
root. The dashboard shows `Unburger Mod v<version>` beneath the menu, and the
`version` and `status` commands report it. Edit the file and reopen (or press
`r` in the dashboard) to change it; there is no separate version constant to
update. A missing or malformed file is reported with a diagnostic instead of an
invented number. The same version is rendered into the in-game title-screen
footer. The title atlas (`GUI/title.ftx`) is extracted from your own base
container into the verified cache; a `GUI__title.ftx` in the tool's
`resources/` directory overrides the extracted member.

**The title-screen footer branding has a confirmed reference composition
(2026-10-19).** That run used the face recorded as `3c32dc0f…` with the built-in
style (`letter_spacing 0`) and stored the base atlas **RAW** (`FileSize ==
ExtractSize`, exactly how the base pack stores it); it reached the title screen
and displayed the footer in an Eden run. The face and style shipped today
(`cf1e64aa…`, `letter_spacing 0.2`) have not been run in the game, so a normal
build reports the composition as runtime-unverified. Earlier, the
CRILAYLA-compressed builds failed at the title transition (the intro played, the
menu was never displayed) while a build without the member booted to the menu,
and a single-variable RAW build isolated the storage shape as the observed lead —
the loader rule behind it is **not proven**. The build compares the live face
digest and style with the recorded reference and reports whether the build is
that reference, the shipped composition or a user-modified one; it is
runtime-unverified unless it matches the reference. The footer is
included by default: set `title_branding` to `false` to withhold it. Any
restyling through `title_footer.toml`, or a replaced face, changes the artwork. The renderer version,
the face digest, the cache identity, the recorded runtime status and the member's
storage shape travel with the resource into the build notes and reports, so the
exact inputs are always provable.

## Troubleshooting

| symptom | remedy |
| --- | --- |
| `translation dataset ...` errors | the dataset is missing or modified; re-obtain `translations/` |
| `extraction is required: ...` | point the tool at your containers (and keys), or at an existing cache/extraction; or configure `extractor_command` |
| `missing local containers: [...]` | run `setup` or set the container directories; only needed to prepare the cache |
| `key files ... none found` | add the key files to the key directory; only needed to prepare the cache |
| `title texture atlas not found` | the cache carries no extracted title atlas; rebuild the cache from your own containers (both containers and the key files are required), or place `GUI__title.ftx` in `resources/` |
| `presentation plate not found` | restore the shipped `assets/images/end_route_plate.png`, or remove a stale `plate_base` path / add `plate_base.png` to `resources/`; `allow_missing_resources` records an explicitly partial build |
| `title footer font not found` | the display face is missing; keep `unburger.otf` (or the `font_file` named in `title_footer.toml`) in `patcher/assets/fonts/` |
| `title_footer.toml ... unknown key` / `outside the allowed range` | fix the style file; only the documented keys with in-range values are accepted |
| `... is not present in the source archive` | the change targets a member the effective archive lacks and no base bank directory was supplied |
| `original text does not match` / `baseline mismatch` | the archive revision is not the one the dataset was reviewed against |
| `refusing to write inside read-only root` | choose an output directory outside the inputs, the dataset and the cache |
| `output path equals the input` | choose a different output directory |
| dashboard does not open | stdin/stdout is not a terminal; use the subcommands |

## Exit codes

| code | meaning |
| --- | --- |
| `0` | success |
| `1` | the command ran but the operation failed (preflight not ready, build refused, validation failed) |
| `2` | usage error (unknown command or flag) |
