# A closer look at Synon Biomed / 走进研究工作台

[Home / 首页](../../README.md) · [Documentation / 文档](../README.md) · [Modules / 模块](../modules/README.md)

Research rarely ends with a single answer. A paper raises another question;
a structure needs closer inspection; an analysis produces files that need review.
Synon Biomed brings those steps into one project workspace.

研究很少止于一个答案。一篇论文会引出新问题，一个结构需要进一步查看，
一次分析会留下待复核的文件。Synon Biomed 将这些环节组织到同一个项目工作台中。

*Concept illustration / AI 生成概念插画，表达证据、科研工作与产物之间的联系，不是实验记录或运行截图。*

![Conceptual progression from layered evidence to scientific work and reviewable files](../assets/research-flow-concept.png)

## Research workflows / 研究工作流

The situations below describe ways to use the workspace—not precomputed answers
or demonstrations of successful experiments.

### 01 / Explore a research question · 梳理问题与证据

**Bring / 准备什么：** a target, disease, publication or research brief, together
with the scope and source types you want to examine.

**Work through / 如何推进：** configure a model in Models, then choose relevant
Skills/Connectors in Scientific Toolkit. Create or choose a project and start
a conversation. Use cited material to refine the question and decide what to
investigate next.

从问题和已有资料出发，在同一项目中保留来源线索、讨论与后续追问。
先明确要回答什么，再决定需要哪些外部数据与科研能力。

### 02 / Inspect scientific material · 查看分子、结构与文件

**Bring / 准备什么：** supported scientific files or identifiers and a specific
inspection question.

**Work through / 如何推进：** keep the inputs as project artifacts, open a
supported viewer, and choose a method and environment if computation is needed.

把“看清输入”与“执行计算”区分开：先核对文件、对象和版本，再选择下一步方法。
环境及执行前提见 [Science](../modules/science.md)。

### 03 / Follow an analysis · 推进分析并跟踪执行

**Bring / 准备什么：** input data, an objective, expected outputs and permitted
compute/network access.

**Work through / 如何推进：** select the needed capabilities, follow progress
and tool feedback, and respond to missing-input or approval requests.
Inspect the artifacts emitted by the task.

执行过程不仅有最后一句回答，也有状态、工具反馈、权限请求和输出文件。
将这些信息结合起来，才能判断任务实际做了什么。

### 04 / Review and continue · 复核产物，继续研究

**Bring / 准备什么：** files and outputs from previous project work.

**Work through / 如何推进：** revisit supported previews, artifact versions,
source references and execution lineage where recorded. Keep the next question
with the project and the material it depends on.

分享结果前回看对应文件与版本；继续研究时把新问题与相关材料连接起来。
具体的记录与存储边界见 [Evidence](../modules/evidence.md)。

## Configure before running / 执行前准备

| Requirement | What to check |
| --- | --- |
| Workspace access | Complete onboarding/login; there is no built-in default Web password. |
| Model provider | Save a supported provider/model and its required credential; configuration alone does not prove a successful model call. |
| Scientific Toolkit | Select the appropriate Expert, Skill or Connector and confirm setup and permissions. |
| Local environments | Required Python/R and the task's optional environments report ready. |
| Execution host | Scientific kernel confinement currently requires Linux/WSL; native installation alone is insufficient. |
| External services | Network access, credentials, availability, capacity and external usage charges remain separate prerequisites. |

完整步骤以[运维手册](../operations-runbook.md)为准。
目录中可发现一项能力，不等于它在当前实例中已配置、已授权或已执行成功。

## Review before relying on a result / 使用结果前的复核

1. **Sources / 来源：** distinguish a search hit from a source that was actually
   read. Check study type, publication date, conflicting evidence and unavailable material.
2. **Inputs / 输入：** confirm file identity and version, molecular objects,
   chains/ligands and analysis parameters.
3. **Execution / 执行：** examine actual tool output, errors and recorded lineage.
   A connected service, ready environment or completed task state is not a scientific conclusion.
4. **Interpretation / 解释：** inspect the outputs and limitations for the chosen
   method. A rendered structure does not establish affinity, a validated pose or a successful simulation.
5. **Sharing / 分享：** select the intended artifact version and remove credentials,
   private research and personal data before publishing.

工程检查帮助确认软件行为，领域审阅决定科学解释是否成立；两者不能互相替代。

<details>
<summary>Readiness terminology / 如何理解状态</summary>

| State | What it establishes | What it does not establish |
| --- | --- | --- |
| Discoverable | A catalog or UI lists the capability. | Installed, authorized or usable on this machine. |
| Installed | Required components exist. | Runtime health or permission to execute. |
| Ready | The relevant live readiness check passed. | A completed and correct scientific workflow. |
| Task completed | The task reached its completion state. | Domain correctness or user acceptance. |
| Result reviewed | Task-specific sources and outputs were inspected. | Universal method validation or clinical suitability. |

</details>

## Actual application capture / 真实页面截图

<details>
<summary>Open the real login-page capture / 展开真实登录页</summary>

![Actual Synon Biomed login screen](../assets/login-screen.png)

Captured on 2026-09-26 from source revision `8e18c838` in a fresh unauthenticated
browser context. The default username was cleared through the form;
no login or task was submitted. The image is unmodified.

这张图只展示真实登录入口。没有采集到的任务界面和运行结果，不用生成图补位。
[Image provenance](../assets/README.md)

</details>

## Explore the implementation / 探索实现

[Workbench](../modules/workbench.md) · [Runtime](../modules/runtime.md) ·
[Science](../modules/science.md) · [Evidence](../modules/evidence.md) ·
[Integrations](../modules/integrations.md)

These guides point to the implementation and its contracts, rather than
creating a second specification of product behavior.
