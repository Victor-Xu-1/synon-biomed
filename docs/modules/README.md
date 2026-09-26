# Module guide · 模块导航

从需要理解或修改的行为开始，找到对应源码和验证入口。这五组是阅读导航，
不是五个独立服务，也不是新的目录划分。

| 想了解什么 | 从哪里开始 | 主要实现 |
| --- | --- | --- |
| 页面导航、对话输入、任务状态和文件预览 | [Workbench · 工作台](workbench.md) | React 页面、交互模型、类型化传输适配器 |
| 模型调用、工具执行、任务中断与恢复 | [Runtime · 任务运行](runtime.md) | 阶段机、server runner 适配器、工具流水线 |
| Python/R 环境、科学计算、执行结果校验 | [Science · 科研执行](science.md) | managed environment、kernel、compute、能力契约 |
| 产物版本、引用、来源关系和持久化事件 | [Evidence · 证据与产物](evidence.md) | workspace/transcript 存储、产物 API、outbox |
| Skills、MCP 连接器、模型服务和网络信任 | [Integrations · 扩展接入](integrations.md) | Skill loader、MCP directory/transport、providers |

## Reading boundaries · 阅读边界

- 安装、启动和排障以[运维手册](../operations-runbook.md)为准。
- 精确目录归属、依赖方向和规模限制以[工程拓扑](../engineering/module-topology.md)
  及其[机器契约](../governance/module-topology.json)为准。
- 各页的命令是定位相应契约的起点，不是完整产品或科研结果的验收。
  更改跨层行为时，按[验证范围](../engineering/verification-scope.md)追加直接受影响的测试。
- 可发现、已配置、运行就绪、实际任务成功是不同事实；模块介绍不声明某台机器的实时状态。

[返回文档目录](../README.md) · [返回仓库首页](../../README.md)
