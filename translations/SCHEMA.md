# Translation dataset — schema

This directory is the public, neutral source of the reviewed English text
changes shipped by the patch builder. It contains **only the corrections made
by this project**, expressed as *bank path + record index + replacement text*.
It does **not** contain the original game script, the official English text,
the Japanese reference corpus, game data, keys or resources.

Two files are published together:

```
changes.jsonl   one JSON object per reviewed change
manifest.json   dataset metadata and content digests
```

Both are produced deterministically by the project's maintainer publication step
(outside this repository). The patch builder only reads them; it never writes or
regenerates this directory. The same input always yields byte-identical files.

## `changes.jsonl`

One UTF-8 JSON object per line (no BOM, no NUL bytes), sorted by `bank_key`
(case-insensitive) and then by `record_index`.

| field | type | required | meaning |
| --- | --- | --- | --- |
| `bank_key` | string | yes | resource-archive bank path, resolved case-insensitively, e.g. `fms/scripttext.fms` |
| `record_index` | int | yes | zero-based string index inside the bank's text record list |
| `text` | string | yes | the replacement string, exactly as it must appear in the bank |
| `tokens_changed` | bool | no | present and `true` only for a change whose renderer control-token difference was explicitly reviewed and declared |

Example:

```json
{"bank_key":"fms/scripttext.fms","record_index":21378,"text":"He's been missing, ever\nsince the battle."}
{"bank_key":"fms/scripttext.fms","record_index":22389,"text":"...Forget him. That two-timer.","tokens_changed":true}
```

A consumer must:

1. refuse a `bank_key` it does not know;
2. refuse a `record_index` outside the target bank's record count;
3. refuse a duplicate `(bank_key, record_index)` pair;
4. refuse text that is not valid UTF-8, contains NUL, or starts with a BOM;
5. refuse a no-op change (the replacement equals the current text);
6. refuse an undeclared control-token change; a change marked
   `tokens_changed` may alter tokens.

## `manifest.json`

| field | meaning |
| --- | --- |
| `schema` | dataset layout identifier. Accepted values: `13s-translations.2` (current published dataset) and `13s-fidelity-translations.2` (historical canonical identifier; the two name the same layout). Any other value fails closed. |
| `format` | resource format family (`cpk-fmsb-1`) |
| `game` | title, title id, region and base/update version labels |
| `record_count` | number of objects in `changes.jsonl` |
| `bank_count` | number of distinct banks |
| `changes_sha256` | SHA-256 of the exact `changes.jsonl` bytes |
| `banks[]` | per bank: `bank_key`, `records`, `sha256` |
| `target_source` | the archive revision the changes were reviewed against: `role`, `size`, `sha256` |

`banks[].sha256` is the SHA-256 of that bank's replacement texts concatenated in
ascending `record_index` order, each followed by one newline byte (`0x0A`). It
lets a consumer detect any per-bank drift without re-reading the record list.

Both accepted `schema` identifiers describe the same manifest and record
structures; consumers validate every field identically regardless of which of
the two names is present. The identifier is not used to relax any check.

## Determinism

* records are sorted before rendering;
* JSON field order is fixed by the writer's struct layout;
* the manifest lists banks in bank-key order;
* `changes_sha256` pins the record bytes.

Re-running the publication step over unchanged input reproduces both files
exactly.
