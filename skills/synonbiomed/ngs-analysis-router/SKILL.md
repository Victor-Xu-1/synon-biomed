---
name: ngs-analysis-router
description: >
  Inspect local BCL, FASTQ, BAM, CRAM, VCF, count-matrix, H5AD, and sequencing
  metadata; ask only the missing assay-specific questions; and route NGS work
  to Synon Biomed's existing Parabricks, genomics acceleration, scVI, scGPT,
  and public bio-tools MCP capabilities. Use for broad or ambiguous sequencing,
  RNA-seq, DNA variant, single-cell, epigenomics, amplicon, or metagenomics
  requests before choosing or installing a workflow. This Skill plans and
  validates; it never silently installs tools, downloads large references, or
  uploads human data.
license: MIT
allowed-tools: python, read_file, search_skills, skill, ask_user
---

# NGS Analysis Router

Use this Skill as the single intake and routing layer for sequencing work. It
adds the missing NGS entrypoint without copying assay runners already supplied
by Synon Skills, managed environments, external workflow systems, or MCP
connectors.

## Inspect before asking

Run the bundled read-only preflight from the current project workspace. The
runtime expands `${SYNON_SKILL_DIR}` to the verified read-only Skill copy:

```python
import json
import subprocess
import sys
from pathlib import Path

PREFLIGHT = Path(r"${SYNON_SKILL_DIR}") / "scripts" / "ngs_preflight.py"

completed = subprocess.run(
    [sys.executable, str(PREFLIGHT), "--root", ".", "--output", "ngs-readiness.json"],
    cwd=Path.cwd(),
    capture_output=True,
    text=True,
    timeout=120,
    check=False,
)
if completed.returncode != 0:
    raise RuntimeError(completed.stderr.strip() or completed.stdout.strip())
readiness = json.loads(completed.stdout)
```

The helper resolves the current working directory as the hard workspace root,
does not follow symlinks, reads only bounded filenames and file metadata, never
opens any sequence or document payload, never executes another program, never
uses the network, and writes only the optional JSON report requested inside the
workspace. It refuses to replace an existing report unless `--overwrite` is
explicitly supplied.

Inspect the report before asking questions. Recognize:

- Illumina run folders: `RunInfo.xml`, `RunParameters.xml`, `SampleSheet.csv`,
  and `Data/Intensities/BaseCalls`;
- reads: `.fastq`, `.fq`, `.fastq.gz`, `.fq.gz`;
- alignment and variants: `.bam`, `.cram`, `.sam`, `.vcf`, `.vcf.gz`, `.bcf`;
- matrices: `.mtx`, `.h5`, `.h5ad`, `.loom`, `.rds`, count-like `.csv` or
  `.tsv` plus features/barcodes/metadata;
- references/design: FASTA, FASTA index, GTF/GFF, BED, sample sheets, contrasts,
  whitelists, and primer files.

## Minimum intake contract

Ask only unresolved fields that change the route:

1. biological assay and platform or read technology;
2. desired output and whether the request is planning, execution, QC, or
   interpretation;
3. organism, reference assembly, and whether a verified matching reference
   bundle already exists;
4. sample design: paired/single end, replicates, groups/contrasts, tumor-normal,
   UMI/duplex, cell chemistry, or assay controls as applicable;
5. runtime target and limits: local/HPC/cloud, CPU/GPU, containers, scheduler,
   storage, wall time, and whether environment changes are authorized;
6. privacy/egress: whether human or proprietary data may leave the current
   machine or institution.

Never infer reference build, sample pairing, strandness, chemistry, control,
tumor-normal relationship, UMI structure, or cloud permission from filenames
alone.

## Route without duplicating existing capability

Use `search_skills` if a destination is not already loaded, then invoke `skill`
with the exact existing Skill name:

| Goal | Existing route | Boundary |
| --- | --- | --- |
| choose a `pbrun` tool, GPU-ready FASTQ/BAM processing, DNA/RNA/variant/QC command guidance | `parabricks` | version-aware guidance; no install or parity claim |
| add optional Parabricks branches to Nextflow, Snakemake, WDL, or Python | `genomics-workflow-acceleration` | retain CPU default; require A/B comparison |
| ordinary scRNA-seq QC, Harmony integration, clustering, annotation, markers, composition, pseudobulk, or response signatures | `single-cell-rna-analysis` | default lightweight post-count route; preserve patient/sample replication |
| probabilistic latent modeling, semi-supervised label transfer, or Bayesian DE from raw single-cell counts | `scvi-tools` | advanced post-count route only; verified raw integer counts and admitted GPU environment required |
| single-cell foundation-model embedding or cell-state representation | `scgpt` | post-count model workflow, not FASTQ processing |
| genomic sequence-to-functional-track prediction | `borzoi` | prediction, not primary NGS processing |
| public reference, archive, gene, variant, expression, regulation, or ontology evidence | existing bio-tools MCP connectors | use public metadata/evidence; do not upload local payloads |

For BCL demultiplexing, generic FASTQ QC, conventional CPU RNA-seq, ATAC/ChIP,
amplicon, shotgun metagenomics, or non-Parabricks variant execution, do not
pretend a local runner exists when none is admitted. Produce a pipeline plan
with exact input/reference requirements and recommend a pinned public workflow
family such as nf-core only after verifying current documentation and the
target runtime. Execution requires a separately approved, reproducibly managed
environment or external workflow integration; this router itself does not
install or run it.

## Reference, database, and privacy gates

- Treat executable readiness separately from reference/database readiness.
- Require assembly-consistent FASTA/index/annotation and record checksums or
  immutable versions before execution.
- For Kraken2/Bracken/HUMAnN, taxonomy classifiers, known-sites resources,
  whitelists, motif databases, and similar large assets, report expected scope,
  license/EULA, approximate storage, source, and existing root before proposing
  a download.
- Keep human germline, somatic, expression, and single-cell data local by
  default. Public metadata lookup is not permission to upload reads, matrices,
  variants, or identifiers.
- Surface proprietary/EULA boundaries such as BCL Convert, Cell Ranger,
  DRAGEN, Sentieon, and hosted clinical platforms before use.
- Do not provide clinical diagnosis or treatment recommendations from pipeline
  output. Separate analytical validity from clinical interpretation.

## Run envelope for an approved execution

Any downstream execution plan must define before launch:

- run ID, input manifest, sample relationships, reference/database lock, and
  checksums;
- pipeline name and immutable version/revision, tool/container/environment
  versions, command/config, and resource target;
- bounded logs, exit status, retries, cancellation, and intermediate/output
  retention;
- QC metrics and thresholds, artifact index and checksums, lineage, and known
  limitations;
- a distinct output directory; never overwrite inputs or a prior run.

Do not call a run complete from process exit alone. Required outputs must exist,
validate, and trace back to the declared inputs and reference lock.

## Output contract

Return:

1. detected local input inventory and any ambiguity;
2. routed assay/workflow and confidence;
3. only the missing essential parameters;
4. selected existing Synon Skill/MCP route, or an explicit execution gap;
5. executable readiness separated from reference/database readiness;
6. privacy, license, storage, and clinical-interpretation boundaries;
7. the next concrete read-only check, approval request, or execution plan.

Keep the preflight JSON as a diagnostic artifact only when it helps the user;
the default response is a readable Markdown summary, not raw JSON.
