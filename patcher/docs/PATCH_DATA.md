# Patch-data bundle specification

A **patch-data bundle** is a self-contained directory that describes, in a
neutral and verifiable way, the changes `13s-patcher` should apply to a game
resource archive.

```
bundle/
  manifest.json          # the versioned manifest (this document)
  content/op-0001.txt    # one content file per operation that needs one
  content/op-0002.txt
```

The bundle never contains the game archive itself, and it never contains
authoring history: an operation pins the expected original text by hash and
supplies the replacement text. Nothing beyond that is required to build.

## Versions

| field | meaning |
| --- | --- |
| `spec_version` | manifest schema version; this tool implements **1** |
| `format_version` | resource format family; supported value: **`cpk-fmsb-1`** |
| `bundle_version` | the bundle's own version (managed by its author) |
| `release_version` | a human-facing release label (managed by its author) |

`bundle_version`/`release_version` are independent of the application version and
of the Unburger Mod version in the repository-root `version.txt`.

## manifest.json

```json
{
  "spec_version": 1,
  "bundle_id": "example-1",
  "bundle_version": "1.0.0",
  "release_version": "2026.1",
  "created_utc": "2026-01-01T00:00:00Z",
  "notes": "free-form human notes",
  "game": {
    "title": "Example Game",
    "title_id": "",
    "region": "US",
    "base_version": "1.10"
  },
  "format_version": "cpk-fmsb-1",
  "required_sources": [
    {
      "role": "patch_archive",
      "name": "Game patch archive",
      "path_hint": "GAME_PATCH.CPK",
      "size": 14512,
      "sha256": "76d1f80934b8f9a19a52c5a7bfda41d361c3a9647f6e361b90d7eb0334482fe1"
    }
  ],
  "operations": [
    {
      "id": "op-greeting-0",
      "kind": "replace_text_record",
      "member": "Fms/Greeting.fms",
      "record_index": 0,
      "original_sha256": "10df0ba2e6068a3621b0fddeeac4afa45715457b919af3df64b6b8ea6d1c9fc3",
      "original": "Hello, pilot.",
      "content": "content/op-greeting-0.txt",
      "content_sha256": "4369e7689c1b881a696036a0c40c6c031813e697a5c7807747e167e9032a9f5f",
      "preserve_tokens": true
    }
  ],
  "content_files": [
    {
      "path": "content/op-greeting-0.txt",
      "sha256": "4369e7689c1b881a696036a0c40c6c031813e697a5c7807747e167e9032a9f5f",
      "size": 17
    }
  ],
  "output_constraints": {
    "preserve_unmodified_members": true,
    "member_count_delta": 0,
    "expected_output_sha256": "",
    "expected_output_size": 0,
    "encoder": "chain"
  }
}
```

### `required_sources`

Every input this bundle accepts is pinned by size and SHA-256:

- `role` — a stable machine role. `patch_archive` is the game archive the tool
  reads. The role is the join key used by source auto-detection and validation.
- `name` — a human label shown in preflight output.
- `path_hint` — an optional file name used to auto-detect the archive.
- `size`, `sha256` — the exact accepted identity. A different size or digest is
  refused **before** any byte is written.

### `operations`

Each operation is one reviewed, individually validated change:

- `id` — unique, stable identifier.
- `kind` — the only kind this tool implements is `replace_text_record`.
- `member` — the archive member path, e.g. `Fms/Greeting.fms` (resolved
  case-insensitively). When the member is absent from the source archive the
  builder appends it as a new member, provided a base bank directory was given
  to the build (see the user guide); `member_count_delta` then accounts for it.
- `member_logical_sha256` — optional digest pinning the whole member's logical
  content at authoring time, so an unexpected source revision is refused before
  the edit.
- `record_index` — the zero-based string index inside the member's text bank.
- `original_sha256` — the SHA-256 of the expected current text. If the source
  text does not match, the build is refused (wrong source revision).
- `original` — optional inline copy of the expected text (audit only).
- `content` / `content_sha256` — the replacement text lives in a content file.
- `replacement` — inline replacement text (mutually exclusive with `content`).
- `preserve_tokens` — require the renderer control-token sequence to be
  unchanged. Control tokens drive branching/alternate selection and inline
  emphasis, so an operation that would drop or reorder them is refused.
- `allow_token_change` — explicitly permits a control-token change for this one
  operation (a deliberate, reviewed exception).

The tool applies only the listed operations. Every other member keeps its stored
bytes exactly, so audio and every unlisted resource are untouched by design.

### `content_files`

Every content file is listed with its `path` (relative, no upward traversal),
`sha256` and `size`. The loader verifies each file before use. A missing or
modified content file fails the load.

### `output_constraints`

- `preserve_unmodified_members` — must be `true`. The builder refuses to write an
  output in which an untouched member differs.
- `member_count_delta` — the expected change in member count (negative is
  rejected).
- `expected_output_sha256`, `expected_output_size` — optional exact pins. When
  set, the build fails unless the output matches.
- `encoder` — the codec encoder to use (`chain`, the default, or `old`).

## Validation performed on load

A bundle is rejected if any of these hold:

1. `spec_version` is not supported;
2. `bundle_id` or `bundle_version` is empty;
3. `format_version` is set and unsupported;
4. `required_sources` is empty, or any source has a malformed digest;
5. an operation has an empty id, a duplicate id, an unsupported kind, an empty
   member, a negative index, or a malformed `original_sha256`;
6. two operations target the same member and record index;
7. an operation has neither a usable replacement nor resolvable content;
8. a content path is absolute or traverses upward;
9. a content file's size or digest does not match the manifest;
10. `output_constraints.preserve_unmodified_members` is not `true`.

## Output verification

After applying the bundle the builder re-parses the output and checks:

- the output parses and every member decodes;
- every unmodified member is byte-identical and keeps its sizes;
- every edited member's logical content matches the intended text;
- the member-count delta matches `output_constraints`;
- the optional digest/size pins match.

Only when all checks pass is the output written to disk. A failed or cancelled
build leaves no output file behind.

## Hashing and determinism

- Digests are lowercase hexadecimal SHA-256.
- Builds are deterministic: identical inputs produce byte-identical output.
- Content is written atomically (temporary file + rename).
