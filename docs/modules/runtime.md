# Runtime · 任务运行

Runtime 将一次用户请求组织为模型交互、工具调用、验证和结果提交。
逻辑任务可以跨执行周期继续；单次模型、工具或进程操作仍有自身边界。
暂停、取消、进程退出和任务完成不是同一个状态。

## Find the implementation · 实现入口

| 责任 | 源码入口 | 边界 |
| --- | --- | --- |
| 阶段顺序与合法转换 | [sessionrunner/machine.go](../../internal/sessionrunner/machine.go) | 校验阶段转换，不独自实现调度或存储 |
| 一次执行周期的组合 | [server/runner_cycle.go](../../internal/server/runner_cycle.go) | 连接 claim、恢复、上下文、模型、工具及提交适配器 |
| 模型执行引擎 | [agentruntime](../../internal/agentruntime/) | 模型循环、工具批次和结果物化；服务商传输由 [providers](../../internal/providers/) 承担 |
| 固定顺序的工具流水线 | [toolgateway/pipeline.go](../../internal/toolgateway/pipeline.go) | 统一执行前检查、权限、执行后处理和审计 |
| 原生工具定义与注册 | [tools/registry](../../internal/tools/registry/) | 区分模型可见工具与服务操作；动态 MCP 仍由 MCP transport 执行 |
| 持久化对话与运行记录 | [persistence/transcript](../../internal/persistence/transcript/) · [persistence/workspace](../../internal/persistence/workspace/) | server 消费存储接口，不以 UI 状态代替 durable authority |

## State and recovery · 状态与恢复

正常周期的阅读顺序为：

```text
claim → recovery → context → snapshot → provider ⇄ tool → verify → complete
```

这只是正常路径示意，不是完整枚举。验证可以要求继续模型修正；`paused`、`terminal`
及其他合法转换以 [machine.go](../../internal/sessionrunner/machine.go) 为准。
不要把阶段名直接当成用户可见的任务终态。

工具处理的顺序由 `NewPipeline` 检查。权限拒绝等提前结束路径仍需进入审计，
不能用新的 HTTP 路由或恢复回调绕开原有执行入口。详细不变量见
[Harness conformance](../engineering/synon-harness-conformance.md) 和
[架构机器契约](../governance/harness-architecture.json)。

恢复会检查任务归属、执行身份和已有回执；可持久化的文件与检查点不等同于解释器内存快照。
进程已退出时不能仅凭最近心跳声称仍在运行。科学进程的创建、取消与资源约束见
[Science](science.md)，产物及事件提交见 [Evidence](evidence.md)。

## Focused verification · 验证入口

从仓库根目录执行：

```sh
go test ./internal/sessionrunner ./internal/toolgateway -count=1
```

这验证阶段转换和工具顺序，包括架构契约一致性；它不覆盖所有 server 恢复适配器，
也不是一次真实模型任务。修改恢复、取消或完成逻辑时，继续选择
`internal/server` 下直接相关的回归，再用实际模型或工具协议复测同一行为。
凭据、网络或运行环境不可用时，应分别记录，不能用模拟响应宣称真实链路通过。

[模块导航](README.md) · [工具流式呈现契约](../engineering/tool-stream-presentation-contract.md)
