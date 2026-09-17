# Frontend dependency notices

This directory preserves complete upstream notices for selected locked npm
dependencies. Each version directory contains `SOURCE.json`, the exact registry
archive URL and integrity digest, and the upstream license/copyright files.
The release packager copies this directory under `docs/licenses/`.

These are third-party grants, not claims of Synon ownership. The root AGPL and
optional commercial license do not replace the upstream terms. This curated
directory is supplemented by `../frontend-bundle/NOTICE.txt`, which preserves
the full notices for the browser bundle and copied RDKit runtime. Identical
notices are shared through a package/version index without dropping copyright
holders or terms. This directory is not the complete frontend dependency inventory: see
`frontend/THIRD_PARTY_LICENSES.json` for the lockfile-derived inventory. Local
workspace links are recorded separately and do not receive a third-party grant
merely by being excluded from npm dependency counts.

| Dependency | Version | Applicable distribution terms |
| --- | --- | --- |
| React | 19.2.7 | MIT; preserve copyright and permission notice |
| JSZip | 3.10.1 | MIT option selected; complete upstream dual-license file retained |
| DOMPurify | 3.4.14 | Apache-2.0 option selected; upstream alternative and copyright header retained |
| duck | 0.1.12 | BSD-2-Clause full text; upstream metadata abbreviates it as BSD |
| h5wasm | 0.10.1 | NIST notice and bundled HDF5 terms, not MIT; retain the entire notice |
| caniuse-lite | 1.0.30001809 | CC-BY-4.0 data license; retain attribution and identify changes if made |
| argparse | 1.0.10 | MIT |
| argparse | 2.0.1 | Python-2.0; complete historical license chain retained |
| tslib | 2.8.1 | 0BSD; upstream copyright notice also retained |

Source archives are the unmodified versioned registry packages; the notices
here retain the full text, normalized to UTF-8 LF without trailing line
whitespace or final blank lines, with one final newline. No package
implementation is modified by this notice bundle. Build transforms do not
waive attribution requirements. The h5wasm directory also retains the full
HDF5 2.0.0 source license corresponding to its pinned upstream build input,
including the laboratory acknowledgements. `HDF5-SOURCE.json` records that
source chain; it does not assert a reproducible match of the npm WebAssembly
binary. These notices do not cover separately downloaded plugins, datasets or
other scientific runtimes.
