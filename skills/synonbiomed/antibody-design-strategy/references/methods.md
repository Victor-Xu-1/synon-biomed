# Reviewed antibody design methods

Verify current official releases, license, weights, input formats, and hardware
before use.

- RFantibody: antibody-finetuned RFdiffusion backbone design, ProteinMPNN
  sequence design, and antibody-finetuned RoseTTAFold2 filtering for de novo
  antibodies/nanobodies. <https://github.com/RosettaCommons/RFantibody>
- AbX/DiffAb-class complex-aware models: CDR sequence/structure co-design and
  optimization from antibody-antigen complexes. Example official source:
  <https://github.com/CarbonMatrixLab/AbX>
- IgLM: antibody sequence infilling. Its official repository is non-commercial;
  verify intended use and license before execution.
  <https://github.com/Graylab/IgLM>
- IgFold: fast antibody/nanobody structure prediction and antibody numbering
  support. <https://github.com/Graylab/IgFold>
- FLAb: public antibody fitness/developability benchmark evidence; the authors
  explicitly report that protein AI models are not consistently reliable for
  developability prediction, so model scores remain hypotheses.
  <https://github.com/Graylab/FLAb>

ANARCI/AbNumber or an equivalent validated numbering implementation may be used
for IMGT/Chothia/Kabat mapping. Structure prediction, sequence generation,
developability prediction, and experimental affinity are separate evidence
lanes.

## Resource facts for an informed choice

- RFantibody-style de novo campaigns combine antibody-finetuned backbone,
  sequence, and structure-filtering models and therefore require compatible
  GPU weights, more disk, and longer staged execution than sequence-only
  infilling. Verify the current release and one antibody-class pilot.
- Complex-aware CDR co-design requires a validated antibody-antigen complex and
  usually a GPU-capable model route; the scientific input contract is as
  important as hardware readiness.
- IgLM-class sequence infilling and antibody numbering are lighter than de novo
  structure design, but their output does not contain epitope-conditioned
  binding evidence. License restrictions can also determine route viability.
- Antibody and antigen-complex prediction can dominate GPU memory and runtime;
  compare the exact chain lengths, sample count, model limits, local fit, and
  remote data boundary before offering an option.

Every `ask_user` option uses observed or official requirements. Do not invent a
VRAM number, completion time, affinity gain, or developability success rate.
