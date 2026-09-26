# Getting started · 安装与首次使用

[首页](../README.md) · [文档导航](README.md) · [产品介绍](product/README.md) · [使用手册](user-guide.md)

本指南帮助你从一份源码启动自己的 Synon Biomed，完成首次配置，再安全地停止和更新。
它面向 Ubuntu / Windows WSL 中的源码使用方式；正式安装包另见下方说明。
已有实例、任务或数据时，请先读“已有安装与第二份源码”，不要直接重复首次安装步骤。

## 1. Choose your environment · 选择运行环境

| 当前环境 | 如何使用本指南 |
| --- | --- |
| Ubuntu，Linux x86_64 / amd64 | 在自己的普通用户终端执行下方 Bash 命令。 |
| Windows，已有 Ubuntu WSL | 打开现有 Ubuntu 终端执行命令；页面可在 Windows 浏览器打开。不要把 Bash 命令粘贴进 PowerShell。 |
| Windows 尚未准备 WSL，或其他平台 | 先确认系统及运行环境支持；本指南不负责安装操作系统、改动分区或迁移现有 WSL。 |
| 已有打包安装或共享实例 | 不使用源码流程覆盖它；先确认其安装方式、数据目录和负责人。 |

Windows 的 `C:\...` 路径和 Ubuntu 中的 `/home/...` 不是同一套路径写法。
建议将新源码放在 Ubuntu 用户目录下，例如 `$HOME/src/synon-biomed`。
本指南不会要求卸载、重装、注销或移动已有 WSL，也不要求停止其他服务。

当前科学 kernel 的隔离执行路径需要 Linux/WSL；原生 Windows/macOS 的 Python/R
安装能力不等于这些平台已经支持同一科学执行链。
进一步条件见[科学模块](modules/science.md)和[运行时契约](engineering/kernel-execution-contract.md)。

## 2. Check prerequisites · 检查前置条件

先在**准备启动应用的同一个 Ubuntu 终端**中检查：

```bash
git --version
go version
node --version
npm --version
command -v bash curl flock realpath setsid sha256sum timeout
```

| 项目 | 源码入口要求 |
| --- | --- |
| Git | 用于获取、检查和更新源码。 |
| Go | Go 1.26 或更新的 1.x 版本；版本权威见 [go.mod](../go.mod)。 |
| Node.js | `>=22.22.0 <25`；见 [frontend/package.json](../frontend/package.json)。不要直接用不在此范围的最新版本。 |
| npm | 在同一终端可执行；前端使用 npm 和已提交的 `package-lock.json`。 |
| Unix 工具 | 启动入口显式检查上方 `command -v` 列出的工具；还需正常的 Linux 基础命令环境。 |
| 网络与空间 | 源码依赖、首次 Python/R 准备及所选外部服务需要各自的下载/访问条件和可用磁盘空间。 |

缺少或版本不符时先修复前置环境，不要跳过启动检查。
可参考 [Go 官方安装说明](https://go.dev/doc/install)和
[Node.js 官方下载入口](https://nodejs.org/en/download)，选择符合本仓库范围的 Linux 版本。
Windows 已安装某个工具，不代表 Ubuntu 终端也已配置对应 Linux 工具。
保留其他项目依赖的版本，不为本项目盲目删除或替换共享安装。

这里不承诺固定最低内存、安装耗时或整套科研软件的统一容量。
首次编译、下载、缓存复用和所选科学环境都会影响资源需求。
模型权重、GPU 软件与付费远端计算不是默认全部安装的前置条件。

## 3. Fresh source checkout · 首次获取与启动

以下步骤用于**尚无本项目实例、源码目录和 `synon` 启动器的新安装**。
如果已有其中任意一项，先转到下一节核对归属。

### Obtain the source · 获取源码

```bash
mkdir -p "$HOME/src"
cd "$HOME/src"
git clone https://github.com/Victor-Xu-1/synon-biomed.git
cd synon-biomed
```

若 `synon-biomed` 目录已经存在，先检查内容；不要删除它来让克隆成功。
GitHub 的源码 ZIP、`make build` 输出和 Actions 制品都不自动等于可安装的正式 Release。

### Install the launcher · 安装用户级入口

```bash
bash scripts/dev/install-source-cli.sh
export PATH="$HOME/.local/bin:$PATH"
synon help
```

安装器只创建当前 Linux 用户的 `$HOME/.local/bin/synon` 符号链接，指向**这份源码**的
`scripts/dev/synon`；不安装系统服务，也不修改 shell 启动文件。
上面的 PATH 设置只影响当前终端。新终端找不到命令时，可重新设置该 PATH，
或在同一源码根目录直接使用 `bash scripts/dev/synon`。

### Start and wait for readiness · 启动并等待入口就绪

```bash
synon start
```

保持这个终端打开。启动器检查依赖，并构建/准备前后端源码宿主。

前端宿主按锁文件运行 `npm ci --ignore-scripts`，依赖位于源码下被 Git 忽略的
`frontend/node_modules`；后端构建在独立状态目录中完成。首次启动不必先手工跑一遍
完整测试或发布打包矩阵。具体行为由
[source quickstart](../scripts/dev/source-quickstart.sh)和
[frontend host](../scripts/dev/source-frontend-host.sh)定义。

默认使用两个回环地址：

| 地址 | 用途 |
| --- | --- |
| `127.0.0.1:8765` | 浏览器 Web 入口。 |
| `127.0.0.1:8766` | Go 后端；前端请求会转发到该后端。 |

任一端口被占用时，启动器会报错，不会寻找并杀死占用进程。
前后端健康响应和 Web 文档检查通过后，会打印：

```text
READY_URL=http://127.0.0.1:8765/#/login
```

在浏览器打开实际打印的 URL。Windows 浏览器优先使用数值地址 `127.0.0.1`，
不要擅自改成另一主机名、地址或端口。浏览器不会默认自动打开；如终端具备桌面访问能力，
可在下一次启动时显式使用 `synon start --open-browser`。

启动同时输出 `STATE_DIR`、`BACKEND_LOG`、`FRONTEND_LOG` 和 `BACKEND_HEALTH_URL`，
请保留这些位置用于排障。默认等待上限是 300 秒；这是有界超时，不是安装完成时间承诺。
失败后先看日志。确有首次构建耗时的证据时，可在确认旧启动已退出后使用
`synon start --timeout-seconds 600`，不要用延长等待掩盖依赖或网络错误。

## 4. Existing installations · 已有安装与第二份源码

**不要把“重新克隆”当作“更新原实例”。** 先在目标源码根目录做只读检查：

```bash
pwd
git status --short
git branch --show-current
git remote -v
command -v synon
```

如果 `$HOME/.local/bin/synon` 已存在，可查看它实际指向哪里：

```bash
readlink -f "$HOME/.local/bin/synon"
```

- 安装器再次遇到指向**同一份源码**的链接，会提示 `already installed`。
- 遇到现有文件、其他命令，或指向**另一份源码**的链接，会拒绝替换。
  即使错误包含 `non-Synon command`，也应核对链接目标，不要直接删除。
- 不要用 `sudo`、强制覆盖链接、清空数据或结束未知进程来绕过这些保护。
- 只想试用第二份源码时，无需改动全局 `synon` 链接；直接调用该 checkout 的脚本。

第二份实例必须有**独立状态目录和空闲端口**。以下为明确隔离的示例，先确认示例目录
不含其他实例的数据、示例端口没有被占用，再在目标源码根目录执行：

```bash
export SYNON_SOURCE_STATE_DIR="$HOME/.local/state/synon-biomed-preview"
bash scripts/dev/synon start --web-port 8785 --backend-port 8875
```

两端口须不同，且为 1024–65535 内的整数；示例数字不是“当前一定空闲”的保证。
共享或已被他人使用的实例不应被这个示例替代。新状态目录会呈现一个独立实例，
不会自动迁移旧账号、项目、模型设置或文件。

`SYNON_SOURCE_STATE_DIR` 控制源码启动器的状态根目录；`start --state-dir PATH` 也可指定它。
**`stop` 和 `status` 不接受 `--state-dir` 参数**：使用非默认目录时，在新终端先设置
同一个 `SYNON_SOURCE_STATE_DIR`，并调用同一份 checkout 的脚本。
直接设置 `SYNON_HOME` 不是这条 quickstart 链的状态目录切换方式。

## 5. First session · 首次进入与配置

### Account and model · 账号与模型

1. 打开 `READY_URL`。全新状态可能进入 onboarding；按页面完成首次设置。
   已有实例使用它已配置的登录方式，**不存在可从文档复制的默认 Web 密码**。
2. 在工作区模型设置中保存并启用所需提供方和模型。按所选提供方要求配置凭据、
   服务地址及模型；不要把 API Key 放入公开 Issue、截图或 Git 文件。
3. 先发送一个不含敏感数据的简单请求，确认真实模型响应；“配置已保存”和
   “页面可打开”都不等于模型调用已成功。远端额度、费用和网络权限由相应服务决定。

源码启动会继承当前终端环境。当前后端 watcher 还保留一处既有宿主模型环境文件的条件加载；
复用已有科研主机时，应先审查 [watcher 的启动配置](../scripts/dev/source-backend-watch.sh)，
不要仅凭新状态目录就认定模型配置和凭据已经完全隔离。[.env.example](../.env.example)用于解释配置，
不是登录凭据，也不是保存个人密钥的可提交模板。具体配置说明见
[Model authority](operations-runbook.md#model-authority)。

### Scientific Toolkit · 科研环境与连接器

- 在 Settings → Scientific Toolkit / 设置 → 科学工具集查看 Experts、Skills、Connectors 和科学环境。
  按当前任务选择需要的能力，不要因为目录列出了某个服务就认为它已经授权。
- Python/R 为所支持平台的核心运行时，会进行首次准备与健康检查；
  需要访问其锁定的软件源，且可能耗时。可选科学环境按需选择。
- `ready` 表示相应环境通过自己的检查；失败时查看具体错误及显式重试入口。
  不要直接删除缓存或环境目录。Web gateway 健康不代表全部科学环境 ready。
- 连接器还可能需要自己的账号、权限或网络；查看其设置和实际调用结果。
  先用不敏感的小输入验证你准备使用的路径，再投入正式数据。

完成首次可用性检查后，按[使用手册](user-guide.md)建立项目、明确问题、添加材料，
并检查真实产物与引用。此处没有预设的“成功科研结果”，也不替代方法和结论审阅。

## 6. State, stop and restart · 数据、停止与重启

默认源码状态根目录为：

```text
${XDG_STATE_HOME:-$HOME/.local/state}/synon-biomed-source-quickstart
```

其中 `data/` 是该源码实例的运行数据根，`logs/` 保存宿主日志，
`backend-build/` 和 `frontend-host/` 保存构建或宿主状态。
状态目录必须在源码之外，不能是 `/`、用户主目录本身或与源码相互包含的目录。
源码可重新获取，用户数据不能据此重新生成；两者需分别保管。

停止前先确认没有应保留运行中的任务：

- 原启动终端按 `Ctrl+C`，停止该次调用创建的源码宿主。
- 或在同一环境、同一状态目录下使用 `synon status` 确认归属，再使用 `synon stop`。
- 对上节的独立实例，在目标源码根目录的另一终端使用：

```bash
export SYNON_SOURCE_STATE_DIR="$HOME/.local/state/synon-biomed-preview"
bash scripts/dev/synon status
bash scripts/dev/synon stop
```

`status` 验证的是源码宿主归属，不是科学任务完成状态。
启动器拒绝向无法验证的 PID 发送停止信号；遇到此类错误应检查来源，而不是改用按端口杀进程。
停止不会删除项目数据，也不代表所有正在执行的科研任务已经正常完成。

重启时回到同一源码、使用同一状态目录和端口配置，重新执行对应 `start` 命令。
此源码工作流在前台运行，不会自动安装开机启动服务。

## 7. Update safely · 安全更新源码

仅在**你管理的实例**中更新；共享实例由其负责人安排。
先检查任务并安全停止，为运行数据做一致性备份，记录当前 `git rev-parse HEAD`，
再检查 `git status --short`、当前分支和远端。

只有 checkout 无未提交改动、处于你准备跟随的 `main`、远端确实是本公开仓库时，才执行：

```bash
git pull --ff-only
```

如有本地改动、分支分叉或 fast-forward 失败，先保留现场处理差异；不要 reset、clean、
删除目录或强行覆盖。查看版本变更和迁移要求后，用原状态目录重新启动，
核对健康、登录、模型可用性及一条代表性工作流。

还要检查本次 `BACKEND_LOG`：若出现 `starting the previous successful build`、
`keeping the accepted backend`，或新构建/资产校验失败的提示，watcher 可能仍在运行
上次成功的后端。此时 `git pull` 成功或再次打印 `READY_URL` 都不能证明新源码已经生效。
先处理具体构建或待确认迁移问题，再核对实际运行版本；不要为让更新通过而盲目接受迁移。

备份应在服务停止后覆盖实际 `STATE_DIR/data`，并单独保管你使用的外部配置。
不要把备份存进 Git checkout，也不要公开上传包含账号、凭据或研究数据的完整备份。
恢复旧代码不自动回退数据模式；恢复路径见[升级](operations-runbook.md#upgrade)和
[回滚说明](operations-runbook.md#rollback-and-uninstall)，不要将正式包的命令直接套到源码目录。

## 8. Packaged releases · 什么时候使用安装包

先查看 [GitHub Releases](https://github.com/Victor-Xu-1/synon-biomed/releases)
是否存在适合你的平台、确实发布的版本及安装资产。本指南不声称当前一定有可下载版本。

如果使用正式包：

1. 从可信发布渠道获取对应包、校验和及说明，核对平台、版本和完整性。
2. 使用可信源码或已受信安装中的安装管理脚本，不让待验证的压缩包提供它自己的信任根。
3. 按[安装包验证](operations-runbook.md#verify-an-archive)、
   [Linux/WSL 安装](operations-runbook.md#linux-or-wsl-installation)或
   [Windows 安装](operations-runbook.md#windows-installation)执行该版本的流程。
4. 将运行数据保留在安装目录之外；更新前分别备份安装文件和数据。

GitHub Packages 的 OCI 资产是安装包传输形式，不是可以直接 `docker run` 的镜像。
源码启动与打包部署是不同生命周期，不混用其安装、停止或升级命令。

## 9. Troubleshooting · 常见问题

| 现象 | 先检查与处理 |
| --- | --- |
| `go` / `node` / `npm` 找不到，或版本被拒绝 | 在同一个 Ubuntu 终端重跑版本检查，确认 PATH 和 Linux 工具版本；不要用 Windows 安装状态替代。 |
| `synon: command not found` | 设置 `$HOME/.local/bin` 的 PATH，或从该源码根目录使用 `bash scripts/dev/synon help`。 |
| 安装器拒绝替换现有命令 | 用 `readlink -f` 核对启动器目标；保留现有文件，第二份源码直接调用自己的脚本。 |
| `port is already in use` | 确认是否已有合法实例；不要结束未知进程，另一个实例需独立端口和数据。 |
| `another quickstart owns this state directory` | 确认已有启动归属；不要手动删锁来同时打开同一状态目录。 |
| 编译或依赖下载失败 | 查看实际后端/前端日志中的第一条错误；核对源码、工具版本、网络和磁盘空间，避免无差别重试。 |
| 已打印 URL，但 Windows 浏览器无法打开 | 原 Ubuntu 终端是否仍在运行？地址是否与 `READY_URL` 完全一致？先区分本机服务与 WSL 转发/浏览器访问问题。 |
| `status` 显示 STOPPED，但另一页面仍在运行 | 可能不是同一源码或状态目录；先核对两者，不据此停止其他实例。 |
| 页面可用但模型不响应 | 核对启用的提供方/模型、凭据、额度、权限与具体错误；健康检查不测试真实模型调用。 |
| Python/R 或某个科学环境失败 | 查看工具库状态和实际安装错误；检查软件源、网络、容量及宿主执行条件，再显式重试。 |
| 重启后看不到原项目 | 首先核对登录账号与 `STATE_DIR`，不要新建、覆盖或删除数据库来“找回”记录。 |

应用健康可在实例仍运行时用以下只读请求检查；修改了端口就使用你的实际端口：

```bash
curl --noproxy '*' --fail --silent --show-error http://127.0.0.1:8766/health
curl --noproxy '*' --fail --silent --show-error http://127.0.0.1:8765/api/health
```

期望看到该实例的结构化 `status: healthy`、`service: gateway`；
这只证明相应 gateway 路径，不证明登录、模型或科学结果正确。
提交问题时附版本或提交、系统环境、最小复现和脱敏错误，见[贡献指南](../CONTRIBUTING.md)。
不要贴出完整 `.env`、Token、数据库或私人研究文件；漏洞按[安全政策](../SECURITY.md)私下报告。
