# Product guide / 产品指南

[Home / 首页](../../README.md) · [Documentation / 文档](../README.md) · [Modules / 模块](../modules/README.md)

Synon Biomed connects research conversations, scientific capabilities and project
artifacts. The value is a reviewable path through the work—not a guarantee that
an answer is correct because a model or tool produced it.

## Research workflows / 研究工作流

These are usage patterns, not fabricated execution records or success claims.

### 1. Investigate a question / 研究问题与证据

**Start with:** a target, disease, publication or explicit research question;
state the scope and sources you need.

**In the workspace:** create or choose a project, start a conversation, configure
the model and select relevant Skills/Connectors through Scientific Toolkit.
Review cited source material before making follow-up requests.

**Review before use:** can each important statement be traced to a source?
Are dates, study type, conflicting evidence and unavailable sources explicit?
A source locator or search hit is not equivalent to reading the source.

### 2. Inspect a molecule or structure / 分子与结构分析

**Start with:** supported files, identifiers and a specific inspection question.

**In the workspace:** keep the files as project artifacts, open a supported viewer,
and choose the scientific method and environment when computation is needed.

**Review before use:** verify file identity, chains/ligands, inputs, method and
parameters. A rendered structure is not proof of binding affinity, a validated
docking pose, or a successful simulation. Read the [science module](../modules/science.md)
for execution prerequisites.

### 3. Run an analysis / 数据分析与科研执行

**Start with:** input data, a stated objective, expected outputs and permitted
compute/network access.

**In the workspace:** follow task progress and tool feedback, respond to approval
or missing-input requests, and inspect emitted artifacts. Required and optional
environments use their documented readiness states.

**Review before use:** check the actual output, provenance, parameters and failure
messages. A task being complete does not replace domain review; an environment
being installed does not prove that the task executed.

### 4. Organize and revisit results / 整理与复核产物

**Start with:** project files and results from previous work.

**In the workspace:** inspect supported previews, artifact versions, source
references and execution lineage where recorded. Keep follow-up questions with
the project rather than relying only on a final chat message.

**Review before sharing:** confirm the right artifact version, reproduction inputs
and stated limitations. Remove credentials, private research and personal data
from anything published to an issue, pull request or screenshot.

## Configure before running / 执行前准备

| Requirement / 条件 | What to verify / 要检查什么 |
| --- | --- |
| Workspace access | Complete onboarding/login. There is no built-in default Web password. |
| Model provider | Save a supported provider/model and its required credential; configuration alone is not proof of a successful model call. |
| Scientific Toolkit | Select the appropriate Expert, Skill or Connector; confirm its setup and permission requirements. |
| Local scientific runtimes | Required Python/R and any optional environment needed by the task report ready. |
| Execution host | Current scientific kernel confinement requires Linux/WSL; native runtime installation alone is insufficient. |
| External/remote services | Credentials, network permission, service availability, compute capacity and any external usage charges remain separate prerequisites. |

See [operations](../operations-runbook.md) for exact configuration and recovery.
当前能力以实例实际状态与相应工程契约为准，不以目录数量、效果图或旧测试记录推断。

## Read states precisely / 正确理解状态

| State / 状态 | Meaning / 含义 | Does not establish / 不代表 |
| --- | --- | --- |
| Discoverable / 可发现 | A catalog or UI lists the capability. | Installed, authorized or usable on this machine. |
| Installed / 已安装 | The required components exist. | Runtime health or permission to execute. |
| Ready / 就绪 | The relevant live readiness check passed. | A scientific workflow completed correctly. |
| Task completed / 任务完成 | The task reached its completion state. | Domain correctness or user acceptance. |
| Result reviewed / 结果已复核 | Someone inspected the task-specific sources and outputs. | Universal validation of the method or clinical suitability. |

## Where the pieces live / 能力与模块

- [Workbench](../modules/workbench.md): pages, interactions and typed transport.
- [Runtime](../modules/runtime.md): agent turns, tools and recovery.
- [Science](../modules/science.md): kernels, environments and compute.
- [Evidence](../modules/evidence.md): artifacts, sources, persistence and events.
- [Integrations](../modules/integrations.md): Skills, connectors and provider boundaries.

For images, see [asset provenance](../assets/README.md). Concept artwork is
labeled and never used as a substitute for a running UI, execution state or result.

## Actual application capture / 真实页面截图

![Actual Synon Biomed login screen](../assets/login-screen.png)

Captured from the running login page on 2026-09-26 at revision `8e18c838`,
using a fresh unauthenticated browser context. The default username was cleared
in the form; no login or task was submitted. The image is unmodified.

这张图只展示真实登录入口，不代表已完成科研任务或业务验收。
没有采集到的任务界面和运行结果，不用生成图补位。
