# Science · 科研执行

本层把“需要什么科学软件和输入”转为受控的执行环境、进程和输出记录。
它不把模型描述当作计算结果，也不保证目录中列出的每个可选引擎在当前机器上可用。

## Find the implementation · 实现入口

| 责任 | 源码入口 |
| --- | --- |
| 环境复用、资源预检与创建入口 | [agent_environment_management.go](../../internal/server/agent_environment_management.go) · [agent_environment_resource_preflight.go](../../internal/server/agent_environment_resource_preflight.go) |
| 不可变环境代际及就绪检查 | [kernel/managed_environment.go](../../internal/kernel/managed_environment.go) · [managed_environment_readiness.go](../../internal/kernel/managed_environment_readiness.go) |
| Python/R/Bash 调用与执行身份 | [server/agent_kernel.go](../../internal/server/agent_kernel.go) · [kernel](../../internal/kernel/) |
| 远程计算与 provider 生命周期辅助 | [compute](../../internal/compute/) |
| 能力、引擎、输入和输出见证的契约 | [sciencecapability/contract.go](../../internal/sciencecapability/contract.go) · [scientific-capabilities.v2.json](../../internal/sciencecapability/scientific-capabilities.v2.json) |
| 安装空间和运行目录用量 | [runtimecontrol](../../internal/runtimecontrol/)；此包不是科学能力目录 |

## Preparation is not a result · 环境就绪不等于任务成功

面向模型的环境流程是预检、复用或创建、按需安装，然后指定环境执行。
已有兼容环境应优先复用；新一代环境通过实际解释器和所需 import 校验后才能激活。
失败或取消不能把 staging 内容标成 ready。详细顺序与恢复规则只由
[Managed scientific execution](../engineering/managed-execution-runtime.md) 维护。

| 观察到的事实 | 可以说明什么 | 不能说明什么 |
| --- | --- | --- |
| 目录列出能力或软件 | 有对应配置或能力定义 | 已完成下载、获得凭据或执行成功 |
| 环境显示 ready | 该环境通过其安装和健康检查 | 某个研究结论已经验证 |
| 实际进程完成并保存输出 | 有一次具体执行记录 | 输入、方法和科学解释必然正确 |
| 能力见证校验通过 | 受信适配器提供了契约要求的身份和产物信息 | 可以跳过科学质量审阅 |

当前 native Windows/macOS 可包含科学运行时安装资产，但 kernel confinement 边界仍是
Linux/WSL。不要把 native 安装成功写成该平台已能执行科学任务。支持矩阵、配置及恢复方式见
[运维手册](../operations-runbook.md)和 [Kernel execution contract](../engineering/kernel-execution-contract.md)。

## Focused verification · 验证入口

从仓库根目录执行：

```sh
go test ./internal/sciencecapability -count=1
go test ./internal/kernel -run '^TestExecutionPreparation|^TestBashSessionSeparates' -count=1
```

前者检查能力目录与执行见证，后者检查准备阶段和 Bash 会话边界。
涉及安装发布、进程恢复或特定科学引擎时，还需按 managed execution 文档运行相应真实环境检查。
此处不自动下载模型权重、开通付费算力或对正在运行的任务做故障注入。

[模块导航](README.md) · [Runtime](runtime.md) · [Evidence](evidence.md)
