# flow-map-compiler evals

Three test cases covering the supported frontend stacks. Each one points
the skill at a fixture under `tests/fixtures/sample-*/`, runs the full
compile, and verifies the produced `.flow-map/` identifies the correct
skills, flows, and capabilities.

## Files

- `evals.json` — eval definitions (id, prompt, files, expectations) in
  the schema skill-creator expects (`references/schemas.md` in the
  skill-creator plugin).
- `check_flow_map.py` — programmatic verifier. Reads a produced
  `.flow-map/` directory and emits per-expectation PASS/FAIL lines (or
  a `grading.json`-shaped JSON blob with `--json`). Requires PyYAML.

## Running the verifier directly

```bash
python evals/check_flow_map.py <path-to-.flow-map> --expect <eval-name>

# JSON output suitable for grading.json
python evals/check_flow_map.py <path-to-.flow-map> --expect <eval-name> --json
```

`<eval-name>` is one of `nextjs-update-profile`, `react-update-profile`,
`sveltekit-view-home`.

Exit code is `0` iff every expectation passes.

## Sanity check against the canonical fixtures

The `.flow-map/` directories committed inside each fixture *are* the
gold-standard outputs. Running the verifier against them should report
all expectations passing — useful as a smoke test when editing the
schema, lint contract, or templates:

```bash
python evals/check_flow_map.py tests/fixtures/sample-nextjs/.flow-map    --expect nextjs-update-profile
python evals/check_flow_map.py tests/fixtures/sample-react/.flow-map     --expect react-update-profile
python evals/check_flow_map.py tests/fixtures/sample-sveltekit/.flow-map --expect sveltekit-view-home
```

## How skill-creator uses these

When you run skill-creator's eval loop on flow-map-compiler:

1. The executor subagent gets the fixture source files (listed in
   `evals[].files`) and the prompt, and is expected to produce a
   `.flow-map/` directory.
2. The grader subagent invokes `check_flow_map.py --expect <name>
   --json` against the produced directory and writes the result to
   `<run>/grading.json`.
3. Aggregated benchmarks compare with-skill vs. baseline pass rates.

## What an expectation actually tests

The expectations in `evals.json` are deliberately structural, not
content-exact: they let the compiler pick reasonable IDs (`update-user-record`
vs `write-user-profile`) and prose, but pin down the things that matter
for a runtime agent —

- right number of skills/flows/capabilities (one each, in these tests)
- skill `proposed_tool` matches the capability's `tools[].tool`
- flow `entry` points at the actual route file
- flow body stays tool-name-free and HTTP-detail-free
- glossary is the thin pivot table, not the old "Intent anchors" page
- skill ↔ capability `flows_using_this[]` round-trips
