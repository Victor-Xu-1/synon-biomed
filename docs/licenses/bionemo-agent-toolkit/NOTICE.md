# BioNeMo Agent Toolkit source attribution

Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES.
Source: https://github.com/NVIDIA-BioNeMo/bionemo-agent-toolkit
Pinned source revision: `0e67a612e4045f007e38fa77adc8f3ebfc5616b6`.

The upstream LICENSE and NOTICE, full Apache-2.0 text, and full CC-BY-4.0
text are retained alongside this file. Upstream code is Apache-2.0;
Skill/documentation content is additionally available under CC-BY-4.0.
Existing file-level declarations and third-party attributions remain in force.

The following directories retain or adapt identified upstream material.
Modified Markdown files carry a short modification notice. Synon changes
include runtime/tool routing, scientific handoffs, source-field conventions
and environment-specific guidance. The table below identifies the upstream
directories at the pinned source revision.
This does not license model weights, hosted services, external runtimes, or
local additions merely because they appear in the same Skill directory.

| Local Skill | Upstream directory |
| --- | --- |
| cuequivariance | library-skills/cuequivariance |
| genomics-workflow-acceleration | library-skills/genomics-workflow-acceleration |
| nvmolkit-usage | library-skills/nvmolkit-usage |
| parabricks | library-skills/parabricks |
| boltz2-nim | nim-skills/boltz2-nim |
| diffdock-nim | nim-skills/diffdock-nim |
| evo2-nim | nim-skills/evo2-nim |
| genmol-nim | nim-skills/genmol-nim |
| drug-discovery-pipeline | nim-skills/meta-skills/drug-discovery-pipeline |
| msa-structure-prediction-pipeline | nim-skills/meta-skills/msa-structure-prediction-pipeline |
| molmim-nim | nim-skills/molmim-nim |
| msa-search-nim | nim-skills/msa-search-nim |
| openfold2-nim | nim-skills/openfold2-nim |
| openfold3-nim | nim-skills/openfold3-nim |
| proteinmpnn-nim | nim-skills/proteinmpnn-nim |
| rfdiffusion-nim | nim-skills/rfdiffusion-nim |
| kermt-add-cmim-pretrain | open-models-skills/kermt/kermt-add-cmim-pretrain |
| kermt-continue-pretrain | open-models-skills/kermt/kermt-continue-pretrain |
| kermt-embed | open-models-skills/kermt/kermt-embed |
| kermt-finetune | open-models-skills/kermt/kermt-finetune |
| kermt-infer | open-models-skills/kermt/kermt-infer |
| kermt-monitor | open-models-skills/kermt/kermt-monitor |
| kermt-pretrain-scratch | open-models-skills/kermt/kermt-pretrain-scratch |
| kermt-setup | open-models-skills/kermt/kermt-setup |
| complexa-design | open-models-skills/proteina-complexa/complexa-design |
| complexa-evaluate-pdbs | open-models-skills/proteina-complexa/complexa-evaluate-pdbs |
| complexa-setup | open-models-skills/proteina-complexa/complexa-setup |
| complexa-sweep | open-models-skills/proteina-complexa/complexa-sweep |
| complexa-target | open-models-skills/proteina-complexa/complexa-target |

The local docking-results assembler is not present in this upstream tree;
it is excluded from this source mapping. Highly adapted workflow content
is not claimed to be byte-identical to upstream.

The additional `complexa-slurm` Skill is retained from the earlier upstream
revision `4e8fda769bd773538cb7168c849bd712c1b51b7b`, directory
`open-models-skills/proteina-complexa/complexa-slurm`. That revision's four
license/notice texts match this retained bundle. Its five source files and
Synon adaptations are covered by the scoped notice at
`skills/synonbiomed/complexa-slurm/THIRD_PARTY_NOTICES.md`; the default revision
above is not asserted as the source of this additional directory.
