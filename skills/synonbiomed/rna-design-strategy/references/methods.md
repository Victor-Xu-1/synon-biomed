# Reviewed RNA design methods

Verify current official versions, licenses, material models, task limits, and
schemas at execution time.

- ViennaRNA: single-strand/cofold thermodynamics, partition functions, base-pair
  probabilities, and RNAinverse-style target-secondary-structure design.
  <https://viennarna.readthedocs.io/en/latest/inverse.html>
- NUPACK 4: multi-complex and multi-tube nucleic-acid analysis/design with hard
  and soft constraints, concentrations, and off-target ensembles.
  <https://docs.nupack.org/design/>
- gRNAde: single- and multi-state 3D RNA fixed-backbone inverse design.
  <https://github.com/chaitjo/geometric-rna-design>
- LinearDesign: synonymous mRNA coding-sequence design balancing folding free
  energy and codon adaptation; retain its patent/license and aging dependency
  constraints when selecting local execution.
  <https://github.com/LinearDesignSoftware/LinearDesign>
- RNAstructure/OligoWalk: RNA/DNA structure, accessibility, bimolecular and
  oligonucleotide-target thermodynamics for siRNA/ASO candidate analysis.
  <https://rna.urmc.rochester.edu/RNAstructure.html>

NUPACK web/API, RNAstructure web services, or a connected RNA design MCP may be
used when their live schema supports the selected route. Local deterministic
thermodynamics is preferable when the package fits and data must stay local;
GPU/API routes are appropriate for 3D or foundation-model work that does not
fit the host. Do not combine these tasks into one score or call one method a
complete RNA therapeutic design pipeline.

## Resource facts for an informed choice

- ViennaRNA, NUPACK, RNAstructure/OligoWalk, and LinearDesign-class workflows
  are normally CPU-capable for bounded designs; scale depends on sequence
  length, strand/complex count, ensemble size, and off-target search space.
- gRNAde-class 3D inverse design is GPU-oriented and additionally requires a
  complete compatible RNA backbone and model weights. Verify the checked model
  and one task-class pilot rather than guessing feasibility from GPU presence.
- Transcriptome-wide siRNA/ASO off-target work may be storage, indexing, and
  CPU intensive even when the thermodynamic model itself is light. State the
  reference build and index requirements.
- Remote/API routes trade local resources for credentials, service limits,
  latency, cost, and sequence/data-boundary considerations. Confirm that their
  live schema supports the exact material and modifications.

Use measured or official facts in `ask_user` options. Unknown capacity,
modified-nucleotide support, or runtime remains unresolved rather than being
silently filled with a plausible estimate.
