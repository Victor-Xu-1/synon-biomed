# Internal · Go 实现入口

`internal/` 包含服务端、任务运行、工具、科研执行和存储实现。
从[模块导航](../docs/modules/README.md)按行为定位；精确目录归属与依赖约束以
[工程拓扑](../docs/engineering/module-topology.md)为准。

| 需要修改的行为 | 阅读入口 |
| --- | --- |
| API 组合和跨层调度 | [server](server/)；先找到具体路由和领域适配器，避免向聚合文件继续堆积业务逻辑 |
| 模型、工具及任务阶段 | [Runtime](../docs/modules/runtime.md) |
| 环境准备和科学计算 | [Science](../docs/modules/science.md) |
| 产物、对话记录及持久化事件 | [Evidence](../docs/modules/evidence.md) |
| Skill/MCP/模型服务接入 | [Integrations](../docs/modules/integrations.md) |

`server` 连接领域模块；`sessionrunner` 校验阶段顺序，`toolgateway` 校验工具流水线，
持久化接口分别位于 `persistence/` 的对应子包。不要把这些接口包当作已包含全部业务适配器的独立服务。

每个模块页给出可直接运行的窄范围测试起点。跨层改动应沿调用与存储消费者追加验证，
选择规则见[验证范围](../docs/engineering/verification-scope.md)，不以单个包通过代表全链路可用。
