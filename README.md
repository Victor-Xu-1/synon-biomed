# Synon Biomed

### From a research question to evidence you can inspect.
### 从研究问题出发，让证据、执行与交付留在同一处。

Synon Biomed is an open-source biomedical research workspace. Bring literature,
molecules, data, tools and project artifacts into one browser-based workflow,
with a Go backend and explicit execution controls.

面向生物医药研究者与研发团队：组织研究问题，连接文献与科学工具，
跟踪任务进展，检查文件、引用和结果来源。

**[Get started / 开始使用](#get-started--开始使用)** ·
**[Product guide / 产品指南](docs/product/README.md)** ·
**[Documentation / 文档](docs/README.md)** ·
**[Contribute / 参与贡献](CONTRIBUTING.md)**

*Concept illustration / AI 生成的概念图，不是实际界面、运行截图或科学结果。*

![Concept illustration of connected evidence and molecular research](docs/assets/synon-biomed-hero-v2.png)

## What you can do / 工作台能做什么

| A research need / 研究需求 | In the workspace / 对应体验 |
| --- | --- |
| Explore a target or literature question / 梳理靶点与文献 | Keep the brief, connected sources and follow-up questions in a project. 在项目中保留问题、来源和后续讨论。 |
| Inspect molecules, structures and files / 查看科学文件 | Open supported artifacts in their viewers; retain files and versions with the work. 查看支持的文件类型，并保留对应版本。 |
| Select scientific capabilities / 选择科研能力 | Browse Experts, Skills, Connectors and environments in Scientific Toolkit. 按需配置模型、连接器和计算环境。 |
| Execute and review / 执行与复核 | Follow task status, tool feedback and artifacts; inspect citations and limitations before using a result. 查看任务状态、工具反馈、产物与证据边界。 |

Examples and prerequisites: [research workflows](docs/product/README.md#research-workflows--研究工作流).
Catalog entries describe available integrations, not proof that every service is configured or every scientific method has been validated.

## Get started / 开始使用

The documented source workflow uses **Ubuntu or WSL** with Git, Go **>=1.26**,
Node.js **>=22.22 <25**, npm and the Unix utilities checked by the launcher.
Use a separate checkout and data directory from any existing installation.

```bash
git clone https://github.com/Victor-Xu-1/synon-biomed.git
cd synon-biomed
bash scripts/dev/install-source-cli.sh
export PATH="$HOME/.local/bin:$PATH"
synon start
```

Open the reported `READY_URL`, normally `http://127.0.0.1:8765/#/login`.
On first use you may be sent to onboarding. There is no default Web password.

首次启动需要按引导完成设置，并配置自己的模型提供方；凭据不要写入源码或公开问题。
Python/R 核心环境首次准备需要网络、时间和磁盘空间，可选科学环境按需选择。
服务可访问不等于科学环境已经就绪。

- **Stop / 停止：** press `Ctrl+C` in the launching terminal, or use `synon stop` for the hosts owned by that checkout.
- **Ports / 端口：** the source workflow uses loopback ports 8765/8766. If occupied, inspect the existing instance; do not kill a process merely to make startup pass.
- **Storage and recovery / 存储与恢复：** follow the [operations runbook](docs/operations-runbook.md#source-startup) and [.env.example](.env.example).

## Before relying on a result / 使用前须知

- This is actively developed **research software**, not evidence of clinical suitability.
- A configured model/provider and any required credentials remain necessary.
- Native Python/R preparation and scientific execution are separate: the current
  scientific kernel confinement path requires **Linux/WSL**.
- A connected service or installed environment does not prove that an analysis ran.
  Review the actual sources, parameters, outputs and limitations for each task.
- Source availability does not mean a prebuilt release exists. Check
  [GitHub Releases](https://github.com/Victor-Xu-1/synon-biomed/releases);
  if the desired version is absent, use the documented source workflow.

## Explore the repository / 深入了解

<details>
<summary>Actual application capture / 查看真实页面截图</summary>

![Real Synon Biomed login page captured from the running application](docs/assets/login-screen.png)

Real browser capture, 2026-09-26; source revision `8e18c838`. The default username
was cleared in the form; no login was submitted and no task was executed.
This is the login screen, not a research-result demonstration.
See [capture provenance](docs/assets/README.md#real-application-capture--真实页面截图).

</details>

| Reader / 读者 | Start here / 入口 |
| --- | --- |
| Researcher / 研究用户 | [Product guide: workflows, prerequisites and limitations](docs/product/README.md) |
| Developer / 开发者 | [Module index: source entry points and verification](docs/modules/README.md) |
| Operator / 运维者 | [Installation, providers, health and recovery](docs/operations-runbook.md) |
| Contributor / 贡献者 | [Setup, scoped tests and pull requests](CONTRIBUTING.md) |
| Community participant / 社区参与者 | [Code of Conduct](CODE_OF_CONDUCT.md) · [Security reporting](SECURITY.md) |

The [documentation index](docs/README.md) links the engineering contracts,
release rules, provenance and operational references. Implementation modules
are documented once; product pages do not redefine those contracts.

## License and provenance / 许可与来源

First-party code is **AGPL-3.0-only**, except where separate component terms apply.
Compliant commercial use is permitted; the complete terms remain in
[LICENSE](LICENSE). See [commercial licensing boundaries](COMMERCIAL-LICENSE.md)
and [third-party components and notices](docs/THIRD_PARTY.md).

许可证原文不作改写。第三方组件保留各自许可及来源声明；本项目的许可说明不替代它们。
