# Integrations · 扩展接入

Skills 描述如何使用能力，MCP 连接器提供外部工具协议，模型 provider 负责模型服务通信。
三者相互配合但不是同一种插件；显示在目录中不代表已经安装、授权或可执行。

## Find the implementation · 实现入口

| 扩展边界 | 源码与内容入口 | 改动时注意 |
| --- | --- | --- |
| 随产品发布的 Skills | [skills/synonbiomed](../../skills/synonbiomed/) | `SKILL.md`、相关脚本及资料是内容源 |
| Skill 解析、检索和加载 | [skills/loader.go](../../internal/skills/loader.go) · [server/skill_catalog_loader.go](../../internal/server/skill_catalog_loader.go) | 只加载明确可信根目录；个人库与内置库有各自来源 |
| MCP 元数据和用户作用域的执行定义 | [mcpdirectory](../../internal/mcpdirectory/) | 启用状态、凭据授权、目录同步和实际运行身份不能混淆 |
| MCP 协议及进程/远程连接 | [tools/mcpstdio](../../internal/tools/mcpstdio/) | 传输不由目录页面或 Skill 文本实现 |
| 模型服务协议 | [providers](../../internal/providers/) | 请求、流事件、错误和中断处理；不绕过任务运行主链 |
| 网络策略和 TLS 信任 | [networkpolicy](../../internal/networkpolicy/) · [networktls](../../internal/networktls/) | 接入失败不能通过关闭校验或扩大出站权限掩盖 |

## Content and execution · 内容与执行

内置 Skill 的内容摘要清单是
[skills.manifest.json](../../assets/synonbiomed/skills.manifest.json)，生成器为
[scripts/skills-manifest](../../scripts/skills-manifest/)。内容变更后经审阅再重生成，
不要手工维护第二份清单。核心 `synon-runtime` Skill 由 `BuiltinRuntimeCatalog`
提供，不在 `skills/` 下再复制一份 `SKILL.md`。

Skill 的检索得分只是发现依据，不授予执行权限。MCP 的 runtime resolver
按用户解析连接器、禁用状态及授权；调用时仍须遵守已有工具和权限边界。
连接器目录缓存也不是永远有效的工具 schema，运行路径要使用相应的稳定目录检查。

模型和 MCP 服务可能依赖网络、凭据、外部额度或单独安装的运行时。
其 health 或工具列表成功不等于一次科研调用成功。配置操作见
[运维手册](../operations-runbook.md)，执行流程见 [Runtime](runtime.md)。
第三方作者、许可证、校验和与更新说明统一归入
[Third-party inventory](../THIRD_PARTY.md)，不要把“加载来源”写成“原始作者”。

## Focused verification · 验证入口

从仓库根目录执行：

```sh
go run ./scripts/skills-manifest --check
go test ./internal/skills ./internal/mcpdirectory -count=1
```

这检查发布内容完整性、Skill loader 和连接器目录行为，不会证明所有远端服务在线。
传输改动需追加 `internal/tools/mcpstdio` 的对应协议测试；模型 provider 改动需追加该 provider
的回归与一次实际请求，分别记录外部凭据或网络限制。

[模块导航](README.md) · [Skills 目录说明](../../skills/README.md)
