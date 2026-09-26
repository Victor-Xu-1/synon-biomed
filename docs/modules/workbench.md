# Workbench · 工作台

工作台负责把研究问题、对话、任务进展、项目文件和科学预览放在同一界面中。
它是 Web 应用；源码目录保留 `packages/desktop` 名称，不代表此目录是另一个后端。

## Find the implementation · 实现入口

| 行为 | 源码入口 |
| --- | --- |
| 登录保护、页面路由和重定向 | [Router.tsx](../../frontend/packages/desktop/src/renderer/components/layout/Router.tsx) |
| 对话页面、消息投影和运行状态 | [pages/conversation](../../frontend/packages/desktop/src/renderer/pages/conversation/) |
| 输入框及 Skill、MCP、文件引用组合 | [components/chat/SendBox](../../frontend/packages/desktop/src/renderer/components/chat/SendBox/) |
| 项目和产物页面 | [pages/project](../../frontend/packages/desktop/src/renderer/pages/project/) · [pages/artifact](../../frontend/packages/desktop/src/renderer/pages/artifact/) |
| 设置与科研工具库 | [pages/settings](../../frontend/packages/desktop/src/renderer/pages/settings/) |
| 类型化跨层调用 | [ipcBridge.ts](../../frontend/packages/desktop/src/common/adapter/ipcBridge.ts) 及其同目录客户端 |

### Navigation · 导航

路由使用 `HashRouter`，浏览器地址包含 `/#/`：

- `/#/login`、`/#/onboarding`：登录及首次配置。
- `/#/guid`：新任务入口。
- `/#/conversation/:id`：具体对话。
- `/#/projects/:projectId`：项目；不是 `/project/:id`。
- `/#/artifacts/:artifactId`：产物预览。
- `/#/settings/:section`：设置；旧入口可由路由重定向到对应页。

## Follow a change · 追踪一次改动

输入框先在前端形成类型化的组合输入，随后经传输适配器交给服务端。
[web_composer_capabilities.go](../../internal/server/web_composer_capabilities.go)
及 [web_composer_runtime_context.go](../../internal/server/web_composer_runtime_context.go)
负责对话授权、能力身份和实际输入材料的服务端处理。
显示某个选项不等于获得执行权限；界面不能自行决定模型、连接器或环境已经可用。

加载、空数据、失败、等待用户和已完成应分别呈现。运行投影也不能替代
[Runtime](runtime.md) 的任务权威或 [Evidence](evidence.md) 的持久化记录。

## Focused verification · 验证入口

已在 `frontend/` 安装锁定依赖后，从该目录执行：

```sh
npm run test:unit -- tests/unit/renderer/routeModules.test.ts tests/unit/renderer/conversation/composerCompositionModel.test.ts tests/unit/renderer/conversationRuntimeViewStore.test.ts
```

这组测试检查路由模块、输入组合和运行视图状态，不启动真实后端。
交互或传输改动还需选取 [tests/web-e2e](../../frontend/tests/web-e2e/) 中对应场景，
在隔离后端、明确测试账号及 Google Chrome 下验证；配置和前置条件见
[frontend README](../../frontend/README.md)。不要把默认测试 URL 当作当前部署地址。

真实界面图片必须来自实际运行的浏览器，不能由生成图片替代。
概念视觉只能说明设计意图，不能证明页面、任务或科学结果已实现。

[模块导航](README.md) · [工程拓扑](../engineering/module-topology.md)
