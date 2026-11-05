# Design and boundaries

This note describes how the module is layered and where its responsibilities end.

## Package layout

```
cmd/13s-patcher        entry point; delegates to internal/cli
internal/
  cli                  subcommands, flags, JSON/text output
  tui                  interactive dashboard (Bubble Tea + Lip Gloss + Bubbles)
  app                  shared service layer used by BOTH cli and tui; the
                       integrated workflow (discovery, preparation, plan,
                       build, validation, inspection)
  config               settings resolution and persistence (project vs user)
  modversion           the authoritative Unburger Mod version read from the
                       repository-root version.txt (validation, root discovery
                       and rendering); never invents a value
  project              project-root discovery and private storage layout
  localsources         locate/identify local containers by content (read-only)
  extraction           verified cache: resolve, populate and verify the effective
                       overlay archive, base text banks and the base game
                       members the build consumes
  translations         public dataset spec: read, validate, write (consumer only;
                       the dataset itself is published by the translation
                       maintainer outside this module)
  gameprofile          the supported title's accepted identities
  build                applies a bundle to an archive (including appended
                       members); verifies the result; writes atomically
  packaging            arranges one verified result into the per-target
                       archives (Atmosphère / emulator) and validates the layout
  techdata             the patch's own embedded technical data: the executable
                       patch (IPS records), the target identity and the
                       descriptors for members the patch adds
  texpack              composes the added manual page from the project's
                       shipped plate (or an optional user replacement) and the
                       patch's own overlay artwork
  source               identifies and validates game archives (read-only)
  patchdata            patch-data bundle spec, loader/validator, exporter
  cpk                  CRI CPK container reader/writer
  crilayla             CRILAYLA codec
  fmsb                 FMSB text-bank reader/writer
  utf                  CRI "@UTF" table codec
  markup               renderer control-token validation
  atomic               atomic file writes
  hashutil             SHA-256 helpers
  synth                synthetic archive/bank constructors (fixtures + demo)
  fixture              synthetic fixture generator used by tests and tools
  hygiene              public-boundary scanner (contents, names, metadata, Git)
tools/
  bundle-export        maintainer utility: authoring document -> bundle
  dataset-hygiene      marker scan for the shipped dataset
  public-hygiene       repository/release boundary scan command
  make-demo            writes the synthetic demo tree
```

## Layering rules

- **One implementation per operation.** The CLI and the TUI both call
  `internal/app`; neither re-implements pipeline logic or formatting of results.
- **Formats are leaf packages.** `utf`, `crilayla`, `cpk` and `fmsb` depend on
  each other only in that direction and never on `app`, `cli` or `tui`.
- **Read-only inputs.** `localsources`, `source`, `translations` (loading) and
  the builder open inputs read-only. The only writer is `atomic`, and it only
  ever writes the requested output/report/cache paths.
- **No secrets.** Keys are never read, hashed, copied or logged; only their
  presence is reported.
- **Fail closed.** Every structural assumption is checked before any byte is
  written; a failed or cancelled operation leaves no partial artifact.
- **Deterministic.** Encoders and serializers are deterministic, so identical
  inputs produce byte-identical output.

## Data boundary

- The public module is self-contained: its dependencies are vendored and it
  requires no parent project, no VCS metadata and no external service at build or
  run time.
- It ships the project's own reviewed text changes (`translations/`), the
  patch's overlay artwork, display face and presentation plate, and synthetic
  fixtures. It never ships game data, keys, the original script or the official
  localisation.
- The public translation dataset (`translations/changes.jsonl` + `manifest.json`)
  is prepared by the translation maintainer and consumed read-only. The module
  never regenerates, edits or reconstructs it, and it carries no build-time or
  run-time dependency on any private directory: a missing or invalid dataset
  fails closed with an explicit message.
- The project's private data directory (when present) holds only user-scoped
  settings and inputs (containers, keys, cache); it is discovered generically and
  never referenced by a hard-coded path.
- The patch's own technical data lives in `internal/techdata`: the executable
  patch is stored as an IPS delta (offsets plus new payload bytes only, never a
  copy of original code), together with the accepted build/title identity and the
  descriptors for the members the patch adds. Nothing there is game-derived; the
  one embedded image is the patch's own overlay artwork.
- The added manual page is composed at build time, deterministically, from the
  project's presentation plate shipped at `assets/images/end_route_plate.png`
  plus the patch-owned overlay. The shipped asset is the standard default, so a
  clean clone builds without any external image. An optional replacement is
  resolved before it: an explicit `plate_base` path, then
  `plate_base.png`/`.jpg` in the tool's `resources/` directory, then a matching
  name in the search roots; an explicit replacement that is absent fails closed.
  `allow_missing_resources=true` is the explicit opt-out that records a partial
  build. Identical inputs yield byte-identical output.
- The in-game title-screen footer is rebranded the same way: the patch never
  ships the game's title texture. The logical `GUI/title.ftx` member is extracted
  from the user's own base container into the verified cache; a `GUI__title.ftx`
  in the tool's `resources/` directory or a search root overrides it. Only the
  4×4 BC7 blocks that carry the shipped copyright line are rewritten; the
  version is read from `version.txt` at build time, so editing that file and
  rebuilding changes the rendered footer.
  The shipped line is a white-on-transparent coverage strip, so the patch draws
  its own label on a fully transparent canvas — white glyphs over a subtle
  translucent dark outline, centred and slightly larger than the earlier label —
  rather than over any backing; the alpha endpoints therefore survive the BC7
  mode-6 fit instead of being pinned opaque.
  Every other byte of the atlas (the logo, the menu words, the other five
  sub-textures) is preserved verbatim, and the composition re-verifies that claim
  from its own output before the build is allowed to continue.
  The label is drawn with the patch's own display face and style, both read from
  disk on **every build** (`assets/fonts/unburger.otf` and
  `assets/fonts/title_footer.toml`; the file name comes from the style). There is
  no embedded copy and no cached face or style, so replacing the `.otf` or
  editing the `.toml` changes the next build without recompiling the tool or
  restarting the dashboard. The directory is resolved from the actual project
  root (discovered like `version.txt`, with an upward walk from the executable as
  a fallback), never from the shell working directory. The face is a complete
  OpenType/CFF face whose character map covers the whole `Unburger Mod v<version>`
  label; one face is used for the whole label, never a mixture, and if a label
  contains a rune the face lacks the whole label falls back to the patch's
  built-in bitmap face, with the face and runes named in the resource status. The
  style file is strict and schema-limited: unknown keys, wrong types and
  out-of-range values fail closed, a label that would not fit the footer
  rectangle fails rather than clipping, and a missing or invalid face fails with
  the expected path named rather than falling back to an embedded one.
  **Runtime status (2026-10-19): a reference composition is confirmed
  working.** A build carrying this member was run on a Switch emulator: the base
  atlas stored **RAW** (`FileSize == ExtractSize`, exactly how the base pack
  stores it) with the composed footer reached the title screen and displayed the
  footer. That run used the face recorded as `3c32dc0f…` with the built-in style
  (`letter_spacing 0`). The face and style shipped today (`cf1e64aa…`,
  `letter_spacing 0.2`) have not been run in the game, so the shipped composition
  is reported as runtime-unverified. Earlier, the CRILAYLA-compressed builds
  failed at the title transition (the intro played, the menu was never displayed)
  while a build without the member booted to the menu, and a single-variable RAW
  build isolated the storage shape as the observed lead; the loader rule behind
  it is **not proven**. The build compares the live face digest and style with the
  recorded reference and reports whether the build is that reference, the shipped
  composition or a user-modified one — never silently assuming the confirmation.
  Any restyling through `title_footer.toml`, or a replaced face, changes the
  artwork. The resource is included in a
  normal build — an omitted `title_branding` key means enabled — and an explicit
  `title_branding=false` withholds it. The selection is exposed as an ordinary
  settings value rather than a dashboard-only flag: the Settings pane toggles it
  (with a one-time warning when re-enabling) and the Build Patch pane toggles it
  with `T`. The effective selection, the footer label read from `version.txt`,
  the renderer version, the face digest, the cache identity, the member's storage
  shape, whether the member is present and the recorded runtime status are carried
  in every build/validate/inspect summary and report; a preflight step fails
  closed, naming the face, the style, `GUI__title.ftx`, the resources directory
  and the search roots, when the resource is enabled but an input is missing. It
  is never silently disabled.
- The story-introduction name order is corrected in place, on the user's own
  plate scripts: for each protagonist the patch adds the transformed logical
  `GUI/change_<name>.mbs` member. The transform is a small patch-owned recipe
  (`internal/plateorder`, described per character in `internal/techdata`): the
  two 7-keyframe name groups of the FMBS S4 table exchange their `ty` (vertical
  centre) floats, so the surname occupies the upper slot and the given name the
  lower one. A per-character constant `tx` offset on the given-name group then
  left-aligns the lower line with the surname line above it; the offset is zero
  where the base plate is already flush or already inset, and each value carries
  its rationale in the descriptor. The same resource carries a second 7-keyframe
  name pair for the progressed / mode-select presentation (no `The Story of`
  caption); it is transformed with the identical operation and its own
  per-character offset (`second_pair`), and the pairs must be disjoint. That
  pair can also receive one whole-block `pair_tx_shift` added to both name
  groups on all keyframes; the story pair refuses it, so the reference
  story-introduction layout cannot be moved by a progressed-plate value.
  Textures
  (`.ftx`), UVs, local bounds, scale/alpha ramps, clip data, node mapping, header,
  footer, timing and every unrelated record stay byte-identical; the build
  re-proves that only the intended `ty` and `tx` fields differ and refuses a base
  script whose pinned size or digest does not match. The base scripts are
  game-derived and never shipped: the extractor stages them from the user's own
  base container as `GUI__change_<name>.mbs` in the verified cache; a file with
  that name in the tool's `resources/` directory or a `base_members/` directory
  under a search root overrides the extracted one. All 13 scripts are required in
  the production shape: a missing one fails preflight and refuses the build so a
  partial 13-character plate set is never shipped silently. The explicit
  `allow_missing_resources=true` opt-out records the omission, marks the build as
  partial and carries a per-resource status instead; a present but mismatching or
  structurally invalid script always fails the build.
- Packaging is a pure layout transform over one verified result: both targets
  carry the identical rebuilt archive bytes and the identical executable patch,
  arranged for the loader that consumes them (Atmosphère's `atmosphere/…` tree
  and the emulator's single mod folder). A produced archive is re-opened and its
  entry set, payload identity and target-specific layout are checked before it is
  reported valid.
- The Unburger Mod version lives in one place: `version.txt` at the repository
  root, read and validated by `internal/modversion`. It is shown by the dashboard
  and the CLI and is used as the default release label for output file names. It
  is separate from the patcher application version, the translation dataset
  schema and the supported game update revision; a missing or malformed file
  yields a diagnostic rather than an invented version.
- The `hygiene` package, `scripts/check_public_hygiene.sh`, its `dataset-hygiene`
  helper, `tools/public-hygiene` and the corresponding tests scan the whole
  public boundary for internal markers and fail on any match: repository-root
  contents, file and directory names, structured metadata of shipped binary
  assets, the vendored tree for project-specific markers, the Git index for
  accidentally tracked private files, and the staged release. The dataset's
  record *text* is authored English and may legitimately contain ordinary words,
  so only its structural fields are marker-scanned there. The scanner does not
  read private `.local/` contents; it only verifies that private state is
  ignored and never tracked or packaged.

## Verification model

A build is successful only when all of the following hold:

1. the dataset loads and verifies (counts, per-bank digests, record digest);
2. the effective archive and base banks are resolved and verified by digest;
3. every change's expected original text matches the source exactly;
4. control tokens are preserved unless the change is declared as a token
   exception;
5. the rebuilt archive re-parses, every member decodes, and every unmodified
   member is byte-identical;
6. the expected member-count delta (including appended members) is satisfied;
7. every produced target archive re-opens, holds exactly the expected entries,
   carries the identical archive and patch bytes, matches its target layout,
   contains no foreign payload (containers, keys, caches, reports), and a second
   packaging pass is byte-identical.

Only then is the output written. Inspection (`inspect`) reconstructs the same
facts from the bytes alone and can be run independently of a build.
