# Pull-request verification scope

`scripts/quality/pr_fast_scope.py` selects changed Go packages and their production and test import consumers using the real Go package graph. Dependency manifests and unknown package-external inputs retain conservative whole-graph coverage.

Verification-only scripts can declare exact ownership in `scripts/quality/verification_scope.json`. Each group lists its files, mandatory executable checks, and whether changes also require the frontend provenance, test, and build gate. The selector executes the declared checks and propagates any failure before running the Go scope. A Go package's embedded input or testdata ownership always takes precedence over a declaration. Wildcards, Go source and module manifests cannot be declared verification-only.

Frontend migration audit scripts belong to the frontend provenance group. Their changes run the two audit regression suites and the existing complete frontend gate; they do not by themselves trigger unrelated Go tests. Scope selection changes run the real Git/Go graph and verification-contract regressions. Unregistered scripts still select the conservative Go graph, so adding a declaration requires review of its actual consumers.

Run the selector regressions with:

```sh
python3 -B -m unittest scripts.quality.test_pr_fast_scope scripts.quality.test_verification_scope
```

Run an exact change's checks with:

```sh
python3 -B scripts/quality/pr_fast_scope.py --base BASE_SHA --head HEAD_SHA --log-dir /absolute/external/evidence
python3 -B scripts/quality/pr_fast_scope.py --frontend --base BASE_SHA --head HEAD_SHA
```

Use immutable full commit IDs and keep evidence outside the source checkout.
