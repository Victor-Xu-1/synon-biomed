# Frontend · Web 工作台

React/TypeScript Web 应用。目录名称 `packages/desktop` 被保留，
实际 Web 入口由 [vite.config.ts](vite.config.ts)构建；npm 与
[package-lock.json](package-lock.json)是依赖权威。

## Source map · 源码导航

- [renderer](packages/desktop/src/renderer/)：页面、交互状态和科学预览。
- [common/adapter](packages/desktop/src/common/adapter/)：类型化传输 facade 与 API 客户端。
- [tests](tests/)：状态/组件、真实后端、打包资源及浏览器测试。
- 路由、跨层边界与按行为选测试：[Workbench 模块](../docs/modules/workbench.md)。

## Local checks · 本地检查

需要满足 [package.json](package.json) 的 Node.js 版本范围。以下命令从仓库根目录开始：

```sh
cd frontend
npm ci --ignore-scripts
npm run typecheck
npm run lint
npm run format:check
npm run build
npm run test:packaged
```

迭代时先运行 Workbench 页列出的相关单测，按改动范围扩展；不要每次都无条件跑整套浏览器矩阵。
上述构建命令只生成前端资产，不会自动启动 Go 后端。完整启动路径见[运维手册](../docs/operations-runbook.md)。

## Browser and backend tests · 浏览器与真实后端

`test:integration:real` 和 `test:e2e:web` 会访问真实服务，部分场景会创建或修改数据。
先准备隔离后端和专用测试账号，再显式设置 `SYNON_GO_WEB_URL`、
`SYNON_GO_WEB_USERNAME`、`SYNON_GO_WEB_PASSWORD`；不要依赖 fixture 的默认账号。
查看 [playwright.webui.config.ts](playwright.webui.config.ts)、
[globalSetup.ts](tests/web-e2e/globalSetup.ts)及所选测试的前置条件。
需要附着官方 Chrome 的场景还须配置其 CDP 与独立 profile 身份，不得接管用户现有会话。

`node`/`dom`、`real-integration`、`packaged` 项目由
[vitest.config.ts](vitest.config.ts)分开。单测通过、资源构建通过和真实页面可用是不同证据；
运行界面展示只使用实际浏览器截图。

[模块导航](../docs/modules/README.md) · [贡献指南](../CONTRIBUTING.md)
