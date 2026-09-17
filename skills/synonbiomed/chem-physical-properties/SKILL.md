---
name: chem-physical-properties
description: Chemical property lookup and prediction. Single-compound constants (Tm, Tb, Tc, Pc, Vc, omega, DeltaHf, flash point, logP, dipole, Hansen/Hildebrand). T/P-dependent properties (density, Psat, Cp, viscosity, thermal conductivity, Hvap). Multi-component VLE (Wilson/NRTL/UNIFAC). Flash (PR EOS). Solubility (Henry, van't Hoff, Hansen solvent selection).
allowed-tools: search_skills, skill, repl, manage_environments, manage_packages, python, read_file, edit_file, save_artifacts
---
# Chemical Physical Properties

Systematic property lookup: authoritative records, reproducible structure-derived
descriptors, experimental databases, group-contribution, and equation-of-state.

## Evidence and quality contract

1. Resolve each compound to one canonical identity before comparing properties.
   Prefer the connected chemistry/PubChem MCP method advertised by the current
   catalog (for example `host.mcp("chemistry", "pubchem_search_compounds", ...)`)
   and retain the returned CID, canonical/isomeric SMILES, formula, and source
   locator. Use a direct public API only when the matching connector is absent
   or cannot return the required field. Do not use generic web-search snippets
   as the numeric property authority.
2. Compute every structure-derived descriptor from that same resolved structure
   in one verified RDKit environment. Record the RDKit version, fingerprint
   family and parameters, and clustering distance/linkage. Never mix a retrieved
   value for one salt, tautomer, stereoisomer, or protonation state with a
   calculation on another without naming the difference.
   In a design set, preserve the upstream `candidate_id` in every table, SDF
   property, plot label, docking join, and report. A display name or temporary
   filename is not a join key.
3. Label values as **retrieved experimental**, **database curated**,
   **structure-derived**, or **model-estimated**. A first database hit is not a
   cross-source agreement. Preserve differing values with conditions and source
   rather than silently choosing one.
4. Do not infer aqueous solubility, permeability, oral absorption, tissue/BBB
   distribution, metabolic stability, safety, efficacy, or target binding from
   logP, TPSA, H-bond counts, rotatable bonds, or fingerprint similarity alone.
   Those descriptors may support a bounded hypothesis; measured or separately
   modelled evidence is required for a factual claim.
5. Before publishing, read the generated report and every machine-readable
   table back from disk. Verify identities, units, row counts, matrix symmetry,
   unit diagonal and [0,1] similarity bounds, figure labels, and agreement of
   repeated values across report/table/figure. Correct the canonical files once,
   then publish the verified set with `save_artifacts`.
   For generated medicinal-chemistry sets, use the canonical generator's `qed`
   and `lipinski_violations` fields directly. Do not replace a classic Lipinski
   criterion with rotatable bonds, and do not estimate counts by scanning rows.
6. Preflight the exact imports used by the planned computation before the main
   run. Treat optional render/export dependencies separately: for example,
   call `DataFrame.to_markdown()` only after `tabulate` imports successfully,
   otherwise install that declared dependency with `manage_packages` or build
   the user-facing Markdown table from already verified values. Do not let an
   optional presentation format turn an otherwise valid computation into a
   failed tool call.

## Routing

| Module | When | Properties | Method |
|--------|------|-----------|--------|
| M1 | Single compound, 25C | Tm, Tb, Tc, Pc, Vc, omega, DeltaHf, flash, logP, dipole, HHV | chemistry/PubChem MCP -> public API -> chemicals (DIPPR) -> Joback |
| M2 | Single compound at T/P | density, Psat, Cp, viscosity, k, Hvap, surface tension | thermo.Chemical + chemicals |
| M3 | Multi-component VLE | Bubble/dew T/P, y/x | IPDB Wilson/NRTL -> UNIFAC |
| M4 | Multi-component flash | Vapor fraction, y, x | PR EOS (thermo.FlashVL) |
| M5 | Solubility | Gas, solid in liquid, solvent ranking | Henry, van't Hoff, Hansen |

## M1 -- Standard Constants

### Source 1: connected chemistry/PubChem record

Inspect the live MCP catalog and call the exact advertised method from `repl`.
Batch independent compound names in one cell when the method supports it. Keep
the returned record identity and source locator with the values used downstream.

### Source 2: PubChem public API
```python
import requests
url = f"https://pubchem.ncbi.nlm.nih.gov/rest/pug/compound/name/{name}/property/MolecularFormula,MolecularWeight,CanonicalSMILES,IsomericSMILES,XLogP,TPSA/JSON"
r = requests.get(url, timeout=30)
r.raise_for_status()
props = r.json()["PropertyTable"]["Properties"][0]
```

### Source 3: chemicals library (DIPPR, ~340 compounds)
```python
from chemicals import Tc, Pc, Vc, omega, Tm, Tb, Hf
Tc_ethanol = Tc(CAS="64-17-5")
```

### Source 4: Joback group-contribution
```python
from rdkit import Chem
from thermo import Joback
mol = Chem.MolFromSmiles(SMILES)
j = Joback(mol)
Tc_j = j.Tc(); Tb_j = j.Tb(); hf_j = j.Hf() / 1000
```

### Source 5: Stenutz solvent database (Hansen/Hildebrand)
```python
import requests
url = f"https://www.stenutz.eu/chem/solv6.php?name={name.lower()}"
r = requests.get(url, timeout=30)
r.raise_for_status()
# Parse for Hansen delta d, p, h -- values in (cal/mL)^0.5, x2.0455 for MPa^0.5
```

Use this as a provenance order, not a silent first-hit selector. Resolve identity
through 1/2, prefer measured/curated values over estimates, and retain method,
conditions, units, and source for each reported field. Use Joback only where a
measured/curated value is unavailable and mark it as estimated.

## M2 -- T/P Properties
```python
from thermo import Chemical
chem = Chemical("ethanol", T=350)
density = chem.rho; vapor_p = chem.Psat; visc = chem.mu
k = chem.k; hv = chem.Hvap; st = chem.sigma
```

**Phase check**: T above Tc -> supercritical, Psat/Hvap/sigma undefined.

## M3 -- VLE
```python
from thermo.interaction_parameters import IPDB
if IPDB.has_ip_specific("ChemSep Wilson", CASs, "aij"):
    aij = IPDB.get_ip_asymmetric_matrix("ChemSep Wilson", CASs, "aij")
# UNIFAC fallback if no IPDB data
```

## M4 -- Flash (PR EOS)
```python
from thermo import Chemical, ChemicalConstantsPackage
from thermo.eos_mix import PRMIX
from thermo.phases import CEOSGas, CEOSLiquid
from thermo import FlashVL

CAS_list = ["64-17-5", "7732-18-5"]
Tcs = [Chemical(c).Tc for c in CAS_list]; Pcs = [Chemical(c).Pc for c in CAS_list]
omegas = [Chemical(c).omega for c in CAS_list]
constants = ChemicalConstantsPackage(CASs=CAS_list, Tcs=Tcs, Pcs=Pcs, omegas=omegas)
correlations = PropertyCorrelationsPackage(constants=constants)
kw = dict(Tcs=constants.Tcs, Pcs=constants.Pcs, omegas=constants.omegas)
gas = CEOSGas(PRMIX, eos_kwargs=kw, HeatCapacityGases=correlations.HeatCapacityGases)
liq = CEOSLiquid(PRMIX, eos_kwargs=kw, HeatCapacityGases=correlations.HeatCapacityGases)
flasher = FlashVL(constants, correlations, gas=gas, liquid=liq)
result = flasher.flash(T=373, P=101325, zs=[0.3, 0.7])
vf = result.VF; y = result.gas.zs if result.VF > 0 else None; x = result.liquids[0].zs
```

## M5 -- Solubility
- Gas in liquid: Henry's law with T-dependence
- Solid in liquid: van't Hoff (DeltaHfus from chemicals.Hfus(CAS), Tm from Tb database)
- Solvent selection: Hildebrand/Hansen delta matching from Stenutz

## Output

For a comparison task, publish at minimum the requested user-facing report and
the exact comparison table; publish requested figures and matrices as separate
artifacts. Mention only immutable references returned by `save_artifacts` in the
final answer. Working API payloads, environment inventories, validation JSON,
scripts, and notebooks remain working data unless the user explicitly requests
them.

Default data names when the user does not specify names:
`{compound}_M1.csv`, `{compound}_M2_T{T}.csv`, `VLE_{sys}_M3.csv`,
`flash_{sys}_M4.csv`.

## Dependencies
Inspect/reuse a compatible managed environment first. Install only missing
packages through `manage_packages`; do not run an ad-hoc installer. Typical
capabilities are `thermo`, `chemicals`, `rdkit`, `requests`, `beautifulsoup4`,
`scipy`, and `tabulate` when pandas Markdown export is planned. `pubchempy` is
optional when the MCP or public REST response already provides the required
record.
