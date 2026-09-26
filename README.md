# Synon Biomed

### Research with the evidence in view.
### 让研究推进，让证据始终可见。

An open-source workspace that connects research conversations, scientific tools
and project artifacts. Move from a question to source material, from a structure
to analysis, and from an answer to the files that support it—all within a
project-centered browser workspace.

面向生物医药研究者与研发团队的开源工作台。将研究讨论、文献线索、分子结构、计算工具与
项目产物连接起来：不仅得到回答，还能查看输入、跟踪执行、检查文件，带着证据继续研究。

**[Get started / 开始使用](#get-started--开始使用)** ·
**[Why Synon Biomed / 产品介绍](docs/product/README.md)** ·
**[User guide / 使用手册](docs/user-guide.md)** ·
**[Documentation / 文档](docs/README.md)**

*Concept illustration / AI 生成概念插画，不是实际界面、测量数据或科学结果。*

![Editorial concept illustration connecting scientific material through a teal ribbon form](docs/assets/research-workspace-concept.png)

## A workspace for the way research unfolds / 围绕研究过程展开

Research moves between papers, structures, datasets and code. The useful part is
not just handling each separately—it is keeping the question, the work and its
supporting material connected.

科研工作的难点之一，是把散落的论文、结构、数据、代码和讨论重新连起来。Synon Biomed
以项目组织这些材料，让“为什么做、怎么做、产出了什么”有一个共同的工作上下文。

### Explore a question · 梳理问题与证据

Investigate a target, disease or publication. Keep source references and
follow-up questions with the project.

围绕靶点、疾病或论文展开研究，让来源线索与后续讨论留在项目中。

### Inspect scientific material · 看清分子、结构与数据

Open supported scientific files in their viewers, then choose the tools and
environments needed for the next step.

查看支持的科学文件，再按研究需要选择工具与计算环境。

### Follow the work · 跟踪分析与执行

Follow task progress, tool feedback and output artifacts. Respond when
additional input or approval is needed.

跟踪任务进展、工具反馈与输出文件，在需要时补充输入或作出授权。

### Return to the evidence · 回到证据，继续研究

Revisit files, versions and recorded sources before sharing results or asking
the next question.

分享结果或继续追问前，回看文件、版本与已记录的来源。

**[See the research workflows →](docs/product/README.md#research-workflows--研究工作流)**

## Who it is for / 适合谁

| You want to… / 你的工作 | Start with… / 适合的入口 |
| --- | --- |
| Investigate a target or research question / 靶点调研与文献梳理 | Project conversations, relevant Skills and configured data connectors / 项目讨论、研究技能与数据连接器 |
| Inspect molecules, structures or sequences / 分子、结构与序列查看 | Scientific file previews and the original inputs / 科学文件查看器与原始输入 |
| Analyze data and review computational outputs / 计算分析与产物复核 | Configured Python/R environments, task feedback and artifact files / 已配置环境、执行反馈与产物文件 |
| Build and operate a research workspace / 科研平台开发与运维 | Open source, configuration references and module boundaries / 开放源码、配置说明与模块文档 |

## The pieces work together / 将研究环节连接起来

| In the workspace | Role in your work |
| --- | --- |
| **Projects & conversations** | Organize questions, files and follow-up work around a research context. |
| **Scientific Toolkit** | Discover and configure Experts, Skills, Connectors and scientific environments. |
| **Task execution** | Follow work through its model, tool and compute boundaries. |
| **Artifacts & sources** | Inspect outputs, supported previews, versions and recorded evidence. |

Scientific previews include supported molecular coordinate files, sequences,
alignments, notebooks and tabular data. Model profiles have their own **Models**
settings; Scientific Toolkit brings **Experts, Skills, Connectors and scientific
environments** together. Choose the capabilities needed for the task instead of
assuming every listed service is already available.

在工作台中查看支持的结构、序列、比对、Notebook 与表格文件；在独立的“模型”页面配置模型，
在“科学工具集”中选择专家、技能、连接器与科学环境。具体格式和操作见[使用手册](docs/user-guide.md)。

这些是同一工作台内的不同环节，不是相互孤立的功能清单。
可用工作流取决于当前实例的模型、连接器、环境与权限配置；科研结论仍需复核。

## Get started / 开始使用

**New here? Follow the [installation guide](docs/getting-started.md), then the
[first-task walkthrough](docs/user-guide.md).** Already have an account on a
running instance? Start with the user guide; you do not need to install a server.

**首次使用：先读[安装指南](docs/getting-started.md)，再按[使用手册](docs/user-guide.md)完成第一次任务。**
如果已有管理员提供的地址和账号，直接进入使用手册，无需重新部署服务。

The source workflow uses **Ubuntu or WSL**, Git, Go **>=1.26**,
Node.js **>=22.22 <25**, npm and the Unix utilities checked by the launcher.

The commands below are for a **new installation with free ports 8765/8766**.
If you already have an instance or a `synon` command, inspect it first; the
installer refuses to replace a command owned by another checkout.

```bash
git clone https://github.com/Victor-Xu-1/synon-biomed.git
cd synon-biomed
bash scripts/dev/install-source-cli.sh
export PATH="$HOME/.local/bin:$PATH"
synon start
```

After startup checks pass, the default configuration reports:

```text
READY_URL=http://127.0.0.1:8765/#/login
```

Open the URL reported by your instance.
A fresh installation may open onboarding. There is no default Web password.

首次启动按引导完成设置，并配置自己的模型提供方。
Python/R 核心环境首次准备需要网络、时间和磁盘空间；可选环境按需选择。
当前科学 kernel 的隔离执行路径要求 Linux/WSL。

<details>
<summary>Setup, operation and research boundaries / 配置、运行与科研边界</summary>

- Use a separate checkout and state directory from an existing installation.
  Runtime data and credentials belong outside source.
- Model and connector access may require your own credentials and external
  usage allowance. Do not publish keys or confidential research data.
- Press `Ctrl+C` in the launching terminal, or use `synon stop` for the hosts
  owned by that checkout.
- Source startup uses loopback ports 8765/8766. If occupied, inspect the existing
  instance; do not kill a process merely to make startup pass.
- Native Python/R installation and scientific execution are separate capabilities.
  A healthy gateway or an installed environment is not proof that an analysis ran.
- This is actively developed research software. Review task-specific sources,
  methods, parameters, outputs and limitations; no clinical suitability is implied.
- A source checkout is not a prebuilt release. Check
  [GitHub Releases](https://github.com/Victor-Xu-1/synon-biomed/releases);
  use the source workflow when the desired package has not been published.

Installation, configuration and recovery:
[Installation guide](docs/getting-started.md) ·
[Operations runbook](docs/operations-runbook.md#source-startup) ·
[Environment example](.env.example) ·
[Product prerequisites](docs/product/README.md#configure-before-running--执行前准备)

</details>

## Go deeper / 深入了解

- **For researchers / 研究用户：** [Product overview](docs/product/README.md) · [User guide](docs/user-guide.md)
- **For developers / 开发者：** [Module guide](docs/modules/README.md) · [Contributing](CONTRIBUTING.md)
- **For operators / 运维者：** [Operations](docs/operations-runbook.md) · [Security](SECURITY.md)
- **For the community / 社区参与者：** [Code of Conduct](CODE_OF_CONDUCT.md) · [Documentation index](docs/README.md)

## Before you choose / 选用前了解

- **Deployment and data:** you choose the host and configure its storage. Model
  providers, connectors and remote compute may receive task data; self-hosted
  does not mean offline or that all data stays local.
- **Cost:** model APIs, hosted tools and compute can have separate charges.
  The repository license does not include these services or guarantee support.
- **Scientific use:** this is research software under active development, not a
  substitute for expert review or clinical validation. Start with a small,
  non-sensitive task and inspect the actual output before expanding the workflow.

部署、数据去向和外部费用由实际配置决定；软件提供研究工具与可检查的工作过程，
不承诺自动得出可靠科学结论。常见选型问题见[产品 FAQ](docs/product/README.md#questions-before-you-start--开始前的常见问题)。

## License / 许可

First-party code is **AGPL-3.0-only**, except where separate component terms apply.
Compliant commercial use is permitted. See the unchanged [LICENSE](LICENSE),
[commercial licensing boundaries](COMMERCIAL-LICENSE.md) and
[third-party inventory](docs/THIRD_PARTY.md).

第三方组件保留各自许可与来源声明；本项目的许可说明不替代它们。
