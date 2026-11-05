# Local containers, keys and the build cache

This note describes where the builder gets the game resources it needs, how they
are identified, and how the small verified cache is prepared and reused.

## Containers

The builder recognises the supported title's containers by **content**, not by
file name. For each candidate it compares the file size against the accepted
revisions and, when the size matches, verifies the SHA-256. A file with the
right name but the wrong digest is refused.

Accepted inputs:

| role | what it is | required to build |
| --- | --- | --- |
| `base` | the base region archive | only to rebuild the cache |
| `update` | the update container (revision 1.10) | only to rebuild the cache |
| `extra` | a second-region base archive (reference) | never |

The title id is a hint only; it is read from the container's (unencrypted)
ticket entry name so an unknown file can still be described. Acceptance always
requires the digest.

Default search locations inside a project checkout:

```
<private>/roms/base-us       base container
<private>/roms/update-us     update container
<private>/roms/base-jp       reference container
<private>/keys               key files
```

Outside a project checkout the configured directories are scanned, and the
working directory is also examined for containers (it is never treated as
read-only, so an output there stays possible).

## Keys

Keys are **never read, hashed, copied or logged**. The builder only reports
whether the conventional key file names are present, because decoding a
container requires them. Key contents never appear in a report, a log line or an
error message.

## The cache

The build consumes three things that must come from the containers:

1. the **effective update overlay archive** — the single added resource the
   update ships, applied over the base archive;
2. the **base text banks** — logical text banks that the overlay does not itself
   carry, needed when a reviewed change targets one of them; and
3. the **base game members** the build consumes directly — the thirteen
   `GUI/change_<name>.mbs` plate scripts and the `GUI/title.ftx` title atlas.
   They are game-owned data and are never shipped.

All three are small (a few megabytes together), so a verified copy is kept in
the cache. The cache:

- is regenerable at any time;
- is validated by digest on every run before it is used;
- lives inside the project's private state directory (or the configured
  `cache_dir`);
- separates the authoritative containers from the derived data — the originals
  are never modified, and the multi-gigabyte containers are never duplicated.

Layout:

```
<cache>/
  cache.json                       identity of the overlay and every cached file
  overlay/<archive name>           the effective overlay archive
  base_banks/<Fms__X.fms>          logical base text banks
  base_members/<Dir__Name>         logical base members the build consumes
```

Base member names use the same flattening convention as the banks:
`GUI/change_kurabe.mbs` is `GUI__change_kurabe.mbs`.

The background of the added presentation page is **not** a cache input. The
project ships its own plate at `assets/images/end_route_plate.png`, and a normal
build uses it directly. An explicit `plate_base` path or a
`plate_base.png`/`.jpg` in `resources/` can replace it deliberately; neither is
required to build from a clean clone.

### Resolution order

1. an explicitly configured overlay archive, base bank directory and/or base
   member directory;
2. an existing extraction found by digest anywhere in the search roots;
3. the built-in container decoder (`nstool` + `hactool`) or the configured
   extractor command (`extractor_command`);
4. otherwise the build stops with a message naming exactly what is missing.

`preflight` verifies an existing cache but never populates one. `setup`, `build`
and `validate` may populate it. Long operations report progress and can be
cancelled; a cancelled or failed preparation leaves no partial cache behind.

### Decoding the containers

The built-in decoder uses two helpers on `PATH`:

- `nstool` unpacks each NSP into its NCAs and reports the program content key;
- `hactool` decrypts the base program NCA to a scratch plaintext copy and
  reconstructs the update's patch RomFS over it.

The update ships a patch RomFS, so the base NCA is required even when only the
update overlay is missing from the cache. Both helpers read the key file
directly; this tool passes only its path, and no key content is read, copied or
logged. `doctor` reports which helpers are present.

A custom toolchain can be supplied instead with `extractor_command`, an argv
template whose placeholders are substituted before it runs:

| placeholder | value |
| --- | --- |
| `{base}` | identified base container path |
| `{update}` | identified update container path |
| `{keys}` | key directory path |
| `{dest}` | a scratch directory the helper may use |
| `{overlay}` | directory the helper must write the overlay archive into |
| `{banks}` | directory the helper must write the base bank files into |
| `{members}` | directory the helper must write the requested base members into |

Example (illustrative):

```json
{ "extractor_command": ["/path/to/extract-sources.sh", "{base}", "{update}", "{keys}", "{overlay}", "{banks}", "{members}"] }
```

The helper must write the overlay archive into `{overlay}` (any file name), the
base bank files into `{banks}` (named `<Dir>__<Name>.fms`), and every logical
member listed in the request into `{members}` under its flattened name. A helper
that leaves the unpacked game tree in `{dest}` also works: the tool finds the
base archive there and stages the banks and members itself. The cache verifies
every product by digest before it is used, so a helper that produces the wrong
revision is rejected rather than trusted.

## Fail-closed behaviour

| situation | result |
| --- | --- |
| no verified cache and no usable containers | build stops, message names what is missing |
| containers present but no key files | build stops, message says which key names are absent |
| a container with the right name but the wrong digest | refused |
| an unsupported update revision | refused (the overlay identity does not match) |
| corrupt or partially written cache | refused; the cache is rebuilt from a verified source |
| tampered base bank, base member or overlay in the cache | refused |
| base archive lacks a required plate script or title atlas | refused (wrong container revision) |
| interrupted preparation | scratch is discarded; nothing partial is kept |
| output pointing at an input or the dataset | refused |
