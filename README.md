# Synon Biomed

### Research with the evidence in view.
### 让研究推进，让证据始终可见。

An open-source workspace for biomedical research. Bring research conversations,
scientific tools and project files together—from exploring a question to
inspecting the work it produces.

面向生物医药研究者与研发团队的开源工作台。把研究讨论、科学工具与项目文件组织在一起，
让研究过程更连贯，也更便于复核。

**[Get started / 开始使用](#get-started--开始使用)** ·
**[Explore workflows / 研究工作流](docs/product/README.md)** ·
**[Documentation / 文档](docs/README.md)**

*Concept illustration / AI 生成概念插画，不是实际界面、测量数据或科学结果。*

![Editorial concept illustration connecting scientific material through a teal ribbon form](docs/assets/research-workspace-concept.png)

## A workspace for the way research unfolds / 围绕研究过程展开

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

## The pieces work together / 将研究环节连接起来

| In the workspace | Role in your work |
| --- | --- |
| **Projects & conversations** | Organize questions, files and follow-up work around a research context. |
| **Scientific Toolkit** | Discover and configure Experts, Skills, Connectors and scientific environments. |
| **Task execution** | Follow work through its model, tool and compute boundaries. |
| **Artifacts & sources** | Inspect outputs, supported previews, versions and recorded evidence. |

这些是同一工作台内的不同环节，不是相互孤立的功能清单。
可用工作流取决于当前实例的模型、连接器、环境与权限配置；科研结论仍需复核。

## Get started / 开始使用

The source workflow uses **Ubuntu or WSL**, Git, Go **>=1.26**,
Node.js **>=22.22 <25**, npm and the Unix utilities checked by the launcher.

```bash
git clone https://github.com/Victor-Xu-1/synon-biomed.git
cd synon-biomed
bash scripts/dev/install-source-cli.sh
export PATH="$HOME/.local/bin:$PATH"
synon start
```

Open the reported `READY_URL`, normally `http://127.0.0.1:8765/#/login`.
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
[Operations runbook](docs/operations-runbook.md#source-startup) ·
[Environment example](.env.example) ·
[Product prerequisites](docs/product/README.md#configure-before-running--执行前准备)

</details>

## Go deeper / 深入了解

- **For researchers / 研究用户：** [Product guide](docs/product/README.md)
- **For developers / 开发者：** [Module guide](docs/modules/README.md) · [Contributing](CONTRIBUTING.md)
- **For operators / 运维者：** [Operations](docs/operations-runbook.md) · [Security](SECURITY.md)
- **For the community / 社区参与者：** [Code of Conduct](CODE_OF_CONDUCT.md) · [Documentation index](docs/README.md)

## License / 许可

First-party code is **AGPL-3.0-only**, except where separate component terms apply.
Compliant commercial use is permitted. See the unchanged [LICENSE](LICENSE),
[commercial licensing boundaries](COMMERCIAL-LICENSE.md) and
[third-party inventory](docs/THIRD_PARTY.md).

第三方组件保留各自许可与来源声明；本项目的许可说明不替代它们。
