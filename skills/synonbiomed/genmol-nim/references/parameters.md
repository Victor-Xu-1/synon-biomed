<!-- Modified for Synon Biomed. Upstream attribution and terms: docs/licenses/bionemo-agent-toolkit/NOTICE.md. -->

# GenMol Parameter Guidance

GenMol uses one `/generate` endpoint. The `smiles` field accepts null,
ordinary SMILES, or SAFE. Use null for v2 de novo generation and SAFE masks
when exact fragments or attachment points must condition generation.

## SAFE Patterns

- De novo: `[*{20-30}]`
- Scaffold decoration: `<scaffold_safe>.[*{10-15}]`
- Motif extension: `[*{5-10}].<core_safe>.[*{5-10}]`
- Lead optimization: encode the hit molecule, then replace one fragment with
  `[*{5-12}]`

Wider mask ranges increase diversity. Tight mask ranges keep analog size more
controlled.

## Request Parameters

- `num_molecules`: 1-1000. Request more than the desired display count when
  filtering may reduce the output count.
- `temperature`: float 0.01-10.0. Use `1.0` for baseline; increase for more
  diversity.
- `noise`: float 0.0-2.0. Use `1.0` for baseline; increase for more
  stochastic output.
- `gamma`: 0.0-1.0 classifier-free guidance strength; start at 0.0 unless the
  task has an evidence-backed reason to tune guidance.
- `min_add_len`: 1-128; controls the minimum mask tokens added by generation.
- `step_size`: deprecated and ignored in v2; omit it.
- `scoring`: `"QED"` for drug-likeness or `"LogP"` for lipophilicity.
- `unique`: set `True` when the user asks for non-duplicate analogs.
- `filter`: use for input-fragment preservation, then independently verify the
  required substructure because the official contract does not guarantee every
  SAFE motif case.

## SAFE Conversion

Use `safe-mol` for SMILES-to-SAFE conversion:

```python
import safe as sf

try:
    safe_str = sf.encode(scaffold_smiles)
except sf.SAFEFragmentationError:
    safe_str = scaffold_smiles
```

Mention that `safe-mol` is not needed for pure de novo generation.
