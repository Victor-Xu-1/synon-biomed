# A closer look at Synon Biomed / 走进研究工作台

[Home / 首页](../../README.md) · [Install / 安装](../getting-started.md) ·
[User guide / 使用手册](../user-guide.md) · [Documentation / 文档](../README.md)

Research rarely ends with a single answer. A paper raises another question;
a structure needs closer inspection; an analysis produces files that need review.
Synon Biomed brings those steps into one project workspace.

研究很少止于一个答案。一篇论文会引出新问题，一个结构需要进一步查看，
一次分析会留下待复核的文件。Synon Biomed 将这些环节组织到同一个项目工作台中。

**One workspace for research context, scientific tools and the work they produce.**

**让研究背景、科学工具与研究产物在同一个工作台中相互连接。**

它适合希望把 AI 辅助研究落实到来源、文件与计算过程的用户：从明确问题开始，
使用已配置的模型与工具推进工作，再查看实际留下的材料。你可以从一个小型调研任务入手，
也可以在准备好环境后进入数据与结构分析；无需把所有可选能力一次性装齐。

*Concept illustration / AI 生成概念插画，表达证据、科研工作与产物之间的联系，不是实验记录或运行截图。*

![Conceptual progression from layered evidence to scientific work and reviewable files](../assets/research-flow-concept.png)

## Why this workspace / 为什么值得使用

### Keep the research context together / 少一些材料之间的断层

问题、对话和产物以项目组织。前一次分析留下的文件，可以成为下一轮检查和追问的输入。
这尤其适合需要反复比较来源、调整问题、检查数据和修订结论的研究工作。

Keep questions and their supporting material within a project, so follow-up
work can begin from the relevant files and conversations.

### Inspect scientific material where you work / 在研究现场看清科学材料

工作台不只展示聊天文本，也提供支持格式的分子结构、序列、比对、Notebook 和表格预览。
查看具体对象再决定下一步，能够让讨论更明确：究竟是哪份输入、哪条序列、哪个结构文件？

Scientific previews make the underlying material inspectable alongside the
conversation. A preview is a way to examine an input or output, not an analysis
result by itself.

### See more than the final answer / 不只看最后一段回答

任务反馈、工具输出、文件版本与已记录的来源关系，为复核提供材料。
它们不能替代领域判断，但能帮助你区分“模型提出的解释”和“实际执行后得到的输出”。

Follow execution and return to the artifacts it produces. Use recorded versions
and source relationships when reviewing or continuing the work.

### Build around your research needs / 按研究需要配置和扩展

模型、Skills、MCP 连接器与科学环境有各自的配置入口和职责。可以从已有能力开始，
在具体任务需要时再增加工具或环境；开发者也可以沿现有模块边界扩展实现。

Choose supported model providers and configure capabilities for the task. The
open source and [module guides](../modules/README.md) expose how those pieces fit
together; they do not promise that every third-party integration is available.

## Who gets the most value / 哪些工作最适合

| Role / 用户 | A useful starting point / 从哪里开始 | What to review / 重点复核 |
| --- | --- | --- |
| Biomedical researcher / 生物医药研究人员 | 梳理一个靶点、机制或文献问题，把来源和待确认问题留在项目中 | 原始来源、证据类型、研究条件和冲突结果 |
| Computational researcher / 计算研究人员 | 围绕已有数据或结构文件选择环境，执行小范围分析 | 输入版本、方法参数、实际工具输出和生成文件 |
| Research platform builder / 科研平台开发者 | 配置模型和连接器，复用或扩展 Skills 与模块实现 | 权限、数据去向、依赖、部署和可维护性 |

如果只需要阅读一段文本，可以先从对话开始；如果需要精确数值或科学推断，
就应明确输入、方法、实际执行与复核要求，而不把自然语言回答当作计算证据。

## What is inside / 主要能力详解

### Projects and conversations / 项目与研究对话

用项目承载一个研究主题，把相关讨论和产物放在同一上下文中。
提交任务时写明研究对象、范围、已有材料、输出格式和不能做的操作。
对于较长工作，结合执行反馈和文件检查进度，而不是只等待最终文本。

### Scientific Toolkit / 科学工具集

在 **Settings → Scientific Toolkit / 设置 → 科学工具集** 中使用四类入口：

| Entry / 入口 | Use it for / 用途 | Before using / 使用前 |
| --- | --- | --- |
| Experts / 专家 | 选择已有的专家配置作为任务起点 | 阅读适用范围，不把角色名称当作专业资质 |
| Skills / 技能 | 为具体研究方法加载可复用的操作说明和资源 | 核对来源、内容、依赖与启用状态 |
| Connectors / 连接器 | 连接已配置的外部 MCP 服务和工具 | 完成配置、凭据授权和权限检查 |
| Scientific environments / 科学环境 | 准备科学软件及任务所需运行环境 | 区分安装中、失败和就绪，确认执行主机支持 |

Skills 库支持筛选、查看详情与添加入口；连接器也有独立的安装、配置和启用步骤。
浏览目录、安装组件、连接成功与真实调用成功，是不同阶段。

### Models / 模型配置

**Settings → Models / 设置 → 模型** 是独立页面，不在科学工具集的四个标签中。
配置支持的模型提供方、模型标识及所需凭据，并选择当前使用的配置。
服务额度、网络、上下文能力和输出质量取决于实际提供方与模型；保存配置后，
应先用一个不含敏感数据的小任务确认真实调用。

### Scientific viewers and artifact previews / 科学查看器与产物预览

代表性支持格式包括 PDB/mmCIF、SDF/MOL2 等结构文件，FASTA 等序列文件、
支持的多序列比对文件、Jupyter Notebook、CSV/TSV、Markdown 和 PDF。
不同格式进入对应的预览路径，受文件内容、大小、解析器和当前部署影响；
这不是“所有科学格式都可完整编辑”的承诺。

科学与二进制查看器主要用于检查和下载；支持的文本型产物有各自的编辑路径。
Notebook 预览不等于执行全部单元格，结构显示也不等于完成对接、亲和力预测或模拟。
具体操作见[使用手册](../user-guide.md)，格式路由见
[预览实现](../../frontend/packages/desktop/src/renderer/services/synonBiomedArtifactPreview.ts)。

### Computation and environment preparation / 计算与环境准备

Python/R 核心环境在首次使用时按受控安装流程准备，可选科学软件按需配置。
任务还可能需要数据、权重、外部服务或远程算力，不能仅凭目录列出的能力名称判断可用性。
当前科学 kernel 的隔离执行要求 Linux/WSL；原生平台的环境安装能力与科研执行能力需分别判断。

### Outputs you can return to / 可继续使用的研究产物

在产物中检查任务留下的文件，查看支持的预览和版本，下载需要保留的结果，
并结合已记录的来源或执行关联继续工作。一个文件“存在”不代表内容完整；
一个引用“格式正确”也不代表它支持结论。复核清单见本页下方。

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

**A useful deliverable / 可要求的产物：** 一份带来源的证据表，区分已读材料、
发现但未读的线索、相互冲突的结果和下一步问题；不要只要求一个肯定结论。

### 02 / Inspect scientific material · 查看分子、结构与文件

**Bring / 准备什么：** supported scientific files or identifiers and a specific
inspection question.

**Work through / 如何推进：** keep the inputs as project artifacts, open a
supported viewer, and choose a method and environment if computation is needed.

把“看清输入”与“执行计算”区分开：先核对文件、对象和版本，再选择下一步方法。
环境及执行前提见 [Science](../modules/science.md)。

**A useful deliverable / 可要求的产物：** 输入文件清单、对象与版本核对记录、
可见结构特征，以及仍需要计算或实验才能回答的问题。

### 03 / Follow an analysis · 推进分析并跟踪执行

**Bring / 准备什么：** input data, an objective, expected outputs and permitted
compute/network access.

**Work through / 如何推进：** select the needed capabilities, follow progress
and tool feedback, and respond to missing-input or approval requests.
Inspect the artifacts emitted by the task.

执行过程不仅有最后一句回答，也有状态、工具反馈、权限请求和输出文件。
将这些信息结合起来，才能判断任务实际做了什么。

**A useful deliverable / 可要求的产物：** 分析脚本或 Notebook、参数说明、结果表和限制说明。
输出格式需要在任务中明确，不能假定每次对话都会自动产生全部文件。

### 04 / Review and continue · 复核产物，继续研究

**Bring / 准备什么：** files and outputs from previous project work.

**Work through / 如何推进：** revisit supported previews, artifact versions,
source references and execution lineage where recorded. Keep the next question
with the project and the material it depends on.

分享结果前回看对应文件与版本；继续研究时把新问题与相关材料连接起来。
具体的记录与存储边界见 [Evidence](../modules/evidence.md)。

**A useful deliverable / 可要求的产物：** 一份针对指定版本文件的复核说明，
列出可支持的结论、尚未解决的问题以及建议的下一轮检查。

以上是任务设计示例，不是预先生成的研究结果。实际输出取决于输入、配置与执行情况。
从[首次任务操作示例](../user-guide.md)开始，可以先验证最小的一条使用路径。

## Configure before running / 执行前准备

| Requirement | What to check |
| --- | --- |
| Workspace access | Complete onboarding/login; there is no built-in default Web password. |
| Model provider | Save a supported provider/model and its required credential; configuration alone does not prove a successful model call. |
| Scientific Toolkit | Select the appropriate Expert, Skill or Connector and confirm setup and permissions. |
| Local environments | Required Python/R and the task's optional environments report ready. |
| Execution host | Scientific kernel confinement currently requires Linux/WSL; native installation alone is insufficient. |
| External services | Network access, credentials, availability, capacity and external usage charges remain separate prerequisites. |

首次部署按[安装指南](../getting-started.md)，日常操作按[使用手册](../user-guide.md)；
高级配置、部署和恢复细节见[运维手册](../operations-runbook.md)。
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

## Questions before you start / 开始前的常见问题

### Do I need to install a server? / 每位用户都要安装吗？

不需要。如果你已获得一个正在运行的实例地址与账号，按使用手册登录和配置即可。
如果准备自己部署，当前文档提供 Ubuntu/WSL 源码启动路径。
需要预构建包时，以 [GitHub Releases](https://github.com/Victor-Xu-1/synon-biomed/releases)
实际发布的资产与说明为准；源码压缩包不是安装包。

### Is everything local and offline? / 数据都在本地吗？

不能这样假定。自部署使你可以选择工作台和状态存储的主机，但模型调用、外部连接器、
下载与远程计算可能把相关输入发送到对应服务。使用真实科研或个人数据之前，
先了解模型和工具的数据处理条款、权限与任务需要发送的内容。

### Does the license include model or compute access? / 还需要支付什么费用？

仓库许可不包含第三方模型 API、数据库授权、托管工具或算力额度，这些取决于你的配置。
一方代码采用 AGPL-3.0-only，另有说明的组件除外；具体使用和分发义务以
[许可证](../../LICENSE)及[商业许可说明](../../COMMERCIAL-LICENSE.md)为准。

### Which optional tools should I install? / 是否需要把所有环境都装上？

不需要。先完成核心环境，再根据任务的方法与依赖选择可选环境和连接器。
优先用非敏感的小输入验证所需路径；不要为了试用而直接下载全部权重或开通付费算力。

### What does “completed” mean? / 任务完成能直接当作研究结论吗？

完成状态表示该任务结束，不代表领域结论已经验证。仍需核对来源、输入、方法、
实际输出与适用条件；软件不提供临床适用性或实验成功保证。

### Where do I get help? / 遇到问题怎么办？

先检查[安装排障](../getting-started.md)与[运维排障](../operations-runbook.md#failure-triage)。
提交普通问题时提供版本、系统、复现步骤、预期与实际结果以及已脱敏的错误摘要，
不要上传密钥、完整用户数据库或保密研究材料。安全问题按 [Security](../../SECURITY.md)
中的报告路径处理。社区参与及支持边界见[贡献指南](../../CONTRIBUTING.md)。

## Start with one useful task / 从一个有价值的小任务开始

1. [Install and prepare / 安装与首次准备](../getting-started.md)
2. [Configure, run and inspect / 配置、运行与检查产物](../user-guide.md)
3. [Operate and extend / 运维与扩展](../operations-runbook.md)

## Explore the implementation / 探索实现

[Workbench](../modules/workbench.md) · [Runtime](../modules/runtime.md) ·
[Science](../modules/science.md) · [Evidence](../modules/evidence.md) ·
[Integrations](../modules/integrations.md)

These guides point to the implementation and its contracts, rather than
creating a second specification of product behavior.
