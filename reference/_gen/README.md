# reference/_gen

How `reference/inventory.json` and `reference/INVENTORY.md` are built. Both are
generated: nobody hand-edits them.

```
fragments/*.json  --merge.py-->  reference/inventory.json + reference/INVENTORY.md
```

## Changing the inventory

1. Edit the fragment that owns the item (schema: [SCHEMA.md](SCHEMA.md)):

   | fragment | layer(s) | lane |
   |---|---|---|
   | `fragments/z1-engine.json` | engine, swarm | Z1 |
   | `fragments/z2-compose.json` | compose | Z2 |
   | `fragments/z3-cli.json` | cli-only, buildx | Z3 |
   | `fragments/z3-cli-map.json` | (CLI command coverage map, copied into `cli_map`) | Z3 |
   | `fragments/z4-dockerfile.json` | dockerfile | Z4 |
   | `fragments/pass5.json` | types registered before Pass 5 that the harvest missed, docker_node (Q4) | P5 |

   A new fragment file also has to be added to `FRAGS` in `merge.py`.
2. From the repo root: `Invoke-Build Inventory`. merge.py validates every item
   (unique ids, layer/kind/status values, partial needs `status_note`, source
   files exist under `reference/`, every swagger operation has exactly one
   primary home), prints `ERR` lines and exits 1 on any error.
3. Stage the fragment and both generated files. `Invoke-Build InventoryCheck`
   (in the default task) rebuilds and fails if `git diff` of the two generated
   files is not empty.

merge.py needs Python 3 (stdlib only) and runs with `python -I`.

`fragments/dockerfile-sources.json` (Lane D, Pass 4.5) adds `data.docker_dockerfile`. New fragments go in
`EXTRA_FRAGS` in merge.py and are read in that order after the harvest fragments.

## Outputs (the contract in [../CONTRACT.md](../CONTRACT.md))

`fragments/outputs/<lane>.json` gives items their `outputs`, keyed by item id (schema: SCHEMA.md, "Outputs schema").
merge.py joins them and fails on an unknown or skipped id, an id in two lanes' files, or a malformed output.
Lanes: A engine, B swarm, C compose, D dockerfile + image data sources + the builder (`engine:image`),
E buildx + cli-only (incl. docker_stack), Z cross-lane questions only.

- `Invoke-Build InventoryValidate` runs `validate.py` (rules V1-V6 and L1; V3 reads the version registry in
  SCHEMA.md) on the merged inventory.json. Exceptions go
  in `allowed-violations.txt` with a reason; an entry that no longer matches a violation fails as STALE.
- Checking one lane without touching the repo:
  `python -I reference/_gen/merge.py --dry-run --outputs <lane> --emit <tmp>/inv.json`, then
  `python -I reference/_gen/validate.py <tmp>/inv.json --lane <lane>`.

## generators/

The scripts the harvest run used to write the first version of each fragment.
With paths fixed to run from here, each still reproduces its fragment byte for
byte (`python reference/_gen/generators/<script>`; z1_build.py needs PyYAML,
z3_items.py runs after z3_gen.py). They are provenance, not part of the build:
once a fragment is edited by hand, re-running its generator overwrites the edit.
Port the edit into the generator first if you want to keep using it.

`generators/lanez/` holds the Lane Z scripts that edit `fragments/outputs/*.json` in place. Each runs from
`fragments/outputs/` with the script folder as its first argument, in this order, starting from the outputs files
of commit 9df9acf (Lanes A-E, before Lane Z):

| script | pass | what it does |
|---|---|---|
| `frag.py` | - | load/save helper (keeps each file's indent) |
| `mech.py <dir> <inventory.json of 9df9acf>` | 4.5 Z | logs outputs, `result` category, version names |
| `sens.py <dir>` | 4.5 Z | per-field sensitivity (V5) |
| `desc.py <dir>` | 4.5 Z | description and note text for V5 |
| `qs.py <dir>` | 4.5 Z | closes the decided questions, Z notes |
| `p45c.py <dir>` | 4.5 Z corrections | `log_entries` + tail/since inputs, V6 results for 26 actions, `uses_cli`, docker_cli_version removed |

e.g. `cd reference/_gen/fragments/outputs; python -I -B ../../generators/lanez/mech.py ../../generators/lanez <inv>`.
Replayed in that order on 9df9acf's files, the first four reproduce the Lane Z commit (df79456) byte for byte, and
`p45c.py` then gives the current files. `p45c_docs.py` (run from the repo root) made the matching prose edits.
They are provenance: re-running one on the current files fails or doubles its edits.

## Pass 5

- Names follow [../DECISIONS.md](../DECISIONS.md). `generators/pass5/p5_decisions.py` applied the decisions to the fragments of
  97ec6bf (provenance, not re-runnable); `p5_lanes.py` writes `pass5/lanes.json` (item -> lane); `p5_markers.py` wrote the
  first `internal/provider/pass5_todo_<lane>.go` markers; `build_renames.py` (+ `attrs.py`) builds `renames.json` from a
  kreuzwerker v4.6.0 checkout, then `renames_post.py` applies `pass5/renames_overrides.json` (user shape decisions:
  `shape_mappings` for logging and networks; row patches; the gap rows the lanes implemented); `renames_summary.py` prints
  its counts. `p5_shape_fields.py` adds `input_attr` to the logging / networks fields of the structured-output maps.
- `status` is computed: merge.py reads `internal/provider/registry_gen.go` (generated from inventory.json by tools/registrygen)
  and the `STUB(pass5)` marker lines in `internal/provider/*.go`. After a name change run `Invoke-Build Inventory, RegistryGen, Inventory`.
- `renames.json`: every kreuzwerker v4.6.0 attribute and every Pass 5 rename of ours,
  `{type, kreuzwerker_attr, previous_attr, our_attr, transform, upstream, reason}`.
- `pass5/ownership.json`: which lane owns each file under internal/provider, examples and test during Pass 5.
