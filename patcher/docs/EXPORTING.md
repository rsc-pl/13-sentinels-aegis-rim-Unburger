# Producing a patch-data bundle (and the translation dataset)

Two authoring artifacts are consumed by the builder:

1. the **translation dataset** — the neutral, public list of reviewed changes the
   tool consumes directly (`translations/changes.jsonl` + `manifest.json`; see
   [`../../translations/SCHEMA.md`](../../translations/SCHEMA.md));
2. the **patch-data bundle** — an advanced, lower-level format the tool can also
   consume instead of the dataset (see [`PATCH_DATA.md`](PATCH_DATA.md)).

Both are produced outside the normal user flow.

## Translation dataset

The translation dataset is prepared and published by the project's translation
maintainer from the reviewed working text. It is **not** produced by this module:
the patch builder is a read-only consumer and never regenerates, edits or
reconstructs the dataset from any private source. The format and the rules a
consumer must enforce are documented in
[`../../translations/SCHEMA.md`](../../translations/SCHEMA.md).

The builder verifies a published dataset like any other input. `preflight` and
`build` load `translations/` and check the dataset layout version, the declared
game identity, the record and bank counts, the per-bank digests and the digest of
the exact file bytes. Two layout identifiers are accepted — `13s-translations.2`
(the current published dataset) and `13s-fidelity-translations.2` (historical
canonical) — because they describe the same manifest and record structures; no
validation check is relaxed for either. A missing or modified dataset is
refused, not repaired.

## Patch-data bundle

An advanced, neutral authoring document can also be exported directly:

```sh
cd patcher
go run -mod=vendor ./tools/bundle-export \
  -in  my-authoring.json \
  -out dist/my-bundle
```

See [`examples/authoring.example.json`](../examples/authoring.example.json) for a
complete template. The important fields:

| field | meaning |
| --- | --- |
| `bundle_id`, `bundle_version`, `release_version` | identities for the bundle |
| `game` | title/region/base version the bundle targets |
| `format_version` | resource format family (`cpk-fmsb-1`) |
| `required_sources` | the exact input archive identity (size + SHA-256) |
| `operations[]` | one reviewed change each: `member`, `record_index`, `original`, `replacement`, `preserve_tokens` |
| `output_constraints` | what a successful build must satisfy |

## Verifying before shipping

```sh
13s-patcher preflight
13s-patcher build
```

The dataset (or bundle) is loaded, every change is resolved against the effective
archive, and the build is refused on any mismatch. `validate` additionally builds
into a temporary directory and inspects the result.

## What must be true before a payload ships

The software is complete and tested against synthetic data. Distributing an
actual payload additionally requires that:

1. the reviewed changes have been signed off by whoever is responsible for the
   content;
2. the dataset's `target_source` identity pins the exact archive revision the
   changes were reviewed against;
3. the dataset has been published and preflight-verified against that archive;
4. someone signs off on shipping the payload.

Steps 1 and 4 are content decisions, not software concerns.

## Boundary

- The user-facing tool consumes the dataset; it never generates or embeds content.
- Dataset publication is the translation maintainer's step, outside this module.
- No game data, keys or third-party content is stored in this repository.
