---
name: genmol-nim
description: >
  Generate novel drug-like molecules using the GenMol NIM microservice. Use for de novo generation, scaffold decoration, motif extension, lead optimization, SAFE notation, QED or LogP ranking, hosted NVIDIA API calls, or local Docker deployment. GenMol takes SAFE notation in the smiles field, not ordinary SMILES.
license: Apache-2.0 AND CC-BY-4.0
compatibility: "safe-mol>=0.1.14; requests>=2.28"
allowed-tools: search_skills, skill, ask_user
---

<!-- Modified for Synon Biomed. Upstream attribution and terms: docs/licenses/bionemo-agent-toolkit/NOTICE.md. -->

# GenMol NIM

Generate drug-like molecules with GenMol. Use this `SKILL.md` for first-pass
hosted/local usage; load supplemental files only when needed:

This skill is an instruction contract, not permission to bypass the admitted
agent tool surface. Use `search_skills` to discover the
current runtime route. If no registered GenMol endpoint is available, retain
that blocker and continue only with another explicitly available model-backed
generation method of the same scientific class. GenMol is ligand/scaffold
conditioned rather than protein-pocket conditioned; when the task depends on
protein-pocket geometry, load `structure-based-molecule-generation` instead.
Do not attempt an unadvertised shell, Docker, file, or network operation.

- `references/api.md`: endpoints, schema, Docker flags, response fields.
- `references/science.md`: use cases, strengths, limits, and handoffs.
- `references/parameters.md`: SAFE patterns and tuning effects.
- `references/validation.md`: chemical and artifact checks.
- `references/examples.md`: compact request patterns.

## Select an execution route

Do not begin with an uninformed hosted-versus-local question. Reuse the active
molecular-generation router's task and resource preflight, or inspect the
available endpoint, local GPU/cache/disk readiness, and credential presence
without printing secrets. If one route is viable, use it. If both are viable
and data boundary, cost, setup, or latency is a material user-owned trade-off,
call `ask_user` once in the conversation language and compare the principal
advantage, limitation, resource requirement, and expected output of each.

- Hosted: `https://health.api.nvidia.com/v1/biology/nvidia/genmol/generate`
- Local: `http://localhost:8000/generate`

Hosted requests use `Authorization: Bearer $NGC_API_KEY`. Supported local Docker
startup uses `NGC_API_KEY` (or `NVIDIA_API_KEY` via the preflight) for
registry login, entitlement checks, and first-run model downloads; pass it
into the container with `-e NGC_API_KEY`. Local inference requests use no
auth header after readiness. Warm-cache key-free startup varies by
image/version and should not be assumed.

Establish one usable endpoint before any SAFE conversion, environment creation,
or package mutation. If the hosted credential is absent and the local health
probe is not ready, return GenMol as unavailable and load another model-backed
generation Skill or `capability-acquisition`. Do not install `safe-mol`,
download a model, or prepare GenMol inputs for an endpoint that cannot run.
An algorithmic analog enumerator is a possible user-approved fallback, not an
equivalent GenMol execution; present the resource, credential, and scientific
trade-off through `ask_user` before taking that fallback when the request called
for AI or model-backed generation.

## Local Docker

Use shell env first; source repo-root `.env` only if present. Do not print keys.
For local setup answers, include this sequence: env preflight, `docker login`,
`docker run`, readiness loop, then a no-auth localhost request. Do not invent a
cache default or drop the `NVIDIA_API_KEY` fallback.

```bash
set -a
[ -f .env ] && . ./.env
set +a

if [ -z "${NGC_API_KEY:-}" ] && [ -n "${NVIDIA_API_KEY:-}" ]; then
  export NGC_API_KEY="$NVIDIA_API_KEY"
fi
: "${NGC_API_KEY:?Set NGC_API_KEY or NVIDIA_API_KEY}"
: "${LOCAL_NIM_CACHE:?Set LOCAL_NIM_CACHE}"

echo "$NGC_API_KEY" | docker login nvcr.io --username '$oauthtoken' --password-stdin

export NIM_TEST_GPU="${NIM_TEST_GPU:-0}"
install -d -m 0770 "${LOCAL_NIM_CACHE}"

docker run --rm -it --name genmol-nim \
  --user "$(id -u):0" \
  --runtime=nvidia --gpus=all \
  -e NVIDIA_VISIBLE_DEVICES="${NIM_TEST_GPU}" \
  --shm-size=2G \
  --ulimit memlock=-1 \
  --ulimit stack=67108864 \
  -e NGC_API_KEY \
  -v "${LOCAL_NIM_CACHE}:/opt/nim/.cache" \
  -p 8000:8000 \
  nvcr.io/nim/nvidia/genmol:2.0.0
```

GenMol is single-GPU; `NIM_TEST_GPU` defaults to `0`. Wait for readiness:

```bash
for attempt in $(seq 1 60); do
  if curl -fsS --max-time 5 http://localhost:8000/v1/health/ready >/dev/null; then
    break
  fi
  if [ "${attempt}" -eq 60 ]; then
    echo "NIM readiness check timed out after 60 attempts" >&2
    exit 1
  fi
  sleep 5
done
```

## SAFE Input

The API field is named `smiles`, but GenMol expects SAFE notation. Masked
positions use `[*{min-max}]`.

- De novo: `safe_input = "[*{20-30}]"`
- Scaffold decoration: `safe_input = scaffold_to_safe("C1CC(=O)NC1", 10, 15)`
- Motif extension: `safe_input = f"[*{{5-10}}].{motif_safe}.[*{{5-10}}]"`
- Lead optimization: encode the hit, then replace a fragment with `.[*{5-12}]`

Use `safe-mol` for conditioned generation. Simple ring scaffolds may raise
`SAFEFragmentationError`; fall back to the original SMILES plus a SAFE mask.

```python
import safe as sf

def scaffold_to_safe(smiles: str, frag_min: int, frag_max: int) -> str:
    try:
        safe_str = sf.encode(smiles)
    except sf.SAFEFragmentationError:
        safe_str = smiles
    return f"{safe_str}.[*{{{frag_min}-{frag_max}}}]"
```

Wider masks increase diversity; tight masks keep analog size more predictable.

## Request Pattern

```python
import os
import requests

HOSTED = True
url = (
    "https://health.api.nvidia.com/v1/biology/nvidia/genmol/generate"
    if HOSTED else "http://localhost:8000/generate"
)
headers = {"Content-Type": "application/json"}
if HOSTED:
    headers["Authorization"] = f"Bearer {os.environ['NGC_API_KEY']}"

payload = {
    "smiles": None,          # v2 de novo; use SAFE/SMILES for conditioned generation
    "num_molecules": 30,
    "temperature": 1.0,
    "noise": 1.0,
    "gamma": 0.0,
    "scoring": "QED",        # or "LogP"
    "unique": True,
    "filter": False,
}

response = requests.post(url, headers=headers, json=payload, timeout=180)
response.raise_for_status()
result = response.json()
```

Gotchas:

- GenMol v2 uses floating-point `temperature` (0.01-10.0) and `noise` (0.0-2.0).
- `step_size` is deprecated and ignored by v2; do not send it in new requests.
- `smiles: null` performs de novo generation. Supply SAFE or ordinary SMILES
  only when the task needs fragment/scaffold conditioning.
- `filter=True` requests fragment-preservation filtering, but the official v2
  contract warns that it is a mitigation rather than a universal guarantee;
  verify the fixed substructure independently.
- `num_molecules` is 1-1000; invalid/duplicate molecules may be filtered, so
  request extra when the user needs a minimum count.
- `scoring` is `"QED"` for drug-likeness or `"LogP"` for lipophilicity.
- Set `unique=True` for deduplicated analog lists.

## Save And Report Output

```python
if result.get("status") != "success":
    raise RuntimeError(result.get("error", "GenMol failed"))

molecules = sorted(result["molecules"], key=lambda m: m["score"], reverse=True)
for rank, mol in enumerate(molecules[:30], start=1):
    print(f"{rank:3d} {mol['score']:8.4f} {mol['smiles']}")

with open("generated_molecules.smi", "w", encoding="utf-8") as handle:
    handle.write("smiles\tscore\n")
    for mol in molecules:
        handle.write(f"{mol['smiles']}\t{mol['score']:.4f}\n")
```

For chemical validity, uniqueness, PAINS/alerts, and visualization with RDKit,
read `references/validation.md`.

## Limits And Troubleshooting

- Fewer molecules than requested is expected after filtering.
- Invalid SAFE strings cause `status: "failed"` or validation errors.
- Install `safe-mol` only for scaffold, motif, or lead-optimization workflows;
  de novo masks work without conversion.
- GenMol NIM v2.0.0 uses the `NV-GenMol-89M-v2` checkpoint. Verify
  `/v1/models` reports the expected model and follow the current official
  support matrix instead of assuming an older image or hardware profile.
- Container issues: confirm `nvidia-smi`, NVIDIA Container Toolkit, and
  `--runtime=nvidia`; use `NIM_TEST_GPU` to choose the single visible GPU.
