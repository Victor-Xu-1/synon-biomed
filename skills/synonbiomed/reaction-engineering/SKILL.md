---
name: reaction-engineering
description: Chemical reaction and process engineering correlations. Reaction heat and formation enthalpy estimation, Arrhenius and Langmuir-Hinshelwood kinetics fitting, batch/CSTR reactor ODE solving, liquid-liquid extraction stage count (Kremser), distillation shortcut sizing (FUG: Fenske-Underwood-Gilliland), diffusion coefficient estimation (Wilke-Chang / Stokes-Einstein), adsorption kinetics fitting (PFO/PSO). For single-compound physical properties or VLE, use chem-physical-properties instead.
---
# Reaction Engineering

Chemical process correlations for reactor design and separation unit sizing.

## Dependencies

```bash
pip install thermo chemicals chempy scipy numpy matplotlib
```

## 1. Reaction Heat and Formation Enthalpy

| Goal | Method | Accuracy | Source |
|------|--------|----------|--------|
| DeltaHrxn (common) | Hess law via chemicals.Hfg(CAS) (gas) or chemicals.Hfl(CAS) (liquid) | +/- 1-3 kJ/mol | chem-physical-properties M1 |
| DeltaHf (novel) | Joback: thermo.Joback(rdkit_mol).Hf() / 1000 | +/- 15-20 kJ/mol | Joback & Reid 1987 |
| DeltaHf (high accuracy) | DFT calculation (XTB or pySCF) | +/- 2-5 kJ/mol | quantum chemistry |

Do NOT use `thermo.Chemical.Hf` -- it returns EOS reference enthalpy, not DeltaHf.

**Joback check**: spread > 20 kJ/mol across methods -> unreliable; escalate to DFT.

## 2. Diffusion Coefficient

| Goal | Method | Formula | Inputs |
|------|--------|---------|--------|
| Liquid D (small molecule) | Wilke-Chang | D = 7.4e-12 * (phi*M)^0.5 * T / (mu * Vb^0.6) | phi (H2O=2.6, MeOH=1.9, EtOH=1.5); mu, Vb from chem-properties |
| Liquid D (colloidal) | Stokes-Einstein | D = kB*T / (6*pi*eta*r) | r = hydrodynamic radius |
| Gas D | Chapman-Enskog | from chemicals.lennard_jones | sigma, epsilon |

Wilke-Chang inherent error +/- 20%. For precision use MD (LAMMPS) -> MSD method.

## 3. Reaction Kinetics

### Arrhenius fitting
```python
import numpy as np
from scipy.optimize import curve_fit

def arrhenius(T, A, Ea):
    return A * np.exp(-Ea / (8.314 * T))

T_data = np.array([300, 310, 320, 330])  # K
k_data = np.array([0.01, 0.025, 0.06, 0.14])  # 1/s
popt, pcov = curve_fit(arrhenius, T_data, k_data)
A_fit, Ea_fit = popt
```

### Langmuir-Hinshelwood kinetics
```python
def LH_2site(C_A, C_B, k, K_A, K_B, K_inert):
    return k * K_A * C_A * K_B * C_B / (1 + K_A*C_A + K_B*C_B + K_inert)**2
```

### Batch reactor ODE
```python
from scipy.integrate import solve_ivp

def batch_ode(t, C, k):
    A, B, C_prod = C
    r = k * A * B
    dAdt = -r; dBdt = -r; dCdt = r
    return [dAdt, dBdt, dCdt]

sol = solve_ivp(batch_ode, [0, 3600], [1.0, 1.5, 0.0], args=(0.01,))
```

### Eyring equation (from TS)
```python
from scipy.constants import k_B, h, R
k_Eyring = k_B * T / h * np.exp(-dG_dagger / (R * T))
```

## 4. Liquid-Liquid Extraction (Kremser)

1. Get partition coefficient K from logP
2. Calculate extraction factor: E = K * V_org / V_aq
3. Feasibility: E >= 1 -> countercurrent feasible
   E < 1 -> max recovery = E * 100%, regardless of stages
4. Kremser equation (E >= 1 only):
   r = (x_in - y_in/K) / (x_out - y_in/K)
   N = ln(r * (1 - 1/E) + 1/E) / ln(E)

## 5. Distillation Shortcut (FUG)

Sequence: Fenske (N_min) -> Underwood (R_min, brentq for theta) -> Gilliland (N_actual) -> HETP * N = column height

Pitfalls:
- R = 1.3*Rmin gives N >> Nmin -- use R = 1.5-2*Rmin in practice
- Gilliland unreliable for xD > 0.99 or near azeotropes
- Check azeotrope via chem-physical-properties M3 for non-ideal systems

## 6. Adsorption Kinetics

```python
from scipy.optimize import curve_fit

def PFO(t, qe, k1):
    return qe * (1 - np.exp(-k1 * t))

def PSO(t, qe, k2):
    return k2 * qe**2 * t / (1 + k2 * qe * t)

# Fit both, report RMSE/R2
# PSO qe > 1.2 * q_max experimental -> overfitting
```

## Output
Save figures and tables as artifacts: `kinetics_fit.png`, `reactor_profile.png`, `extraction_design.csv`, `distillation_summary.csv`
