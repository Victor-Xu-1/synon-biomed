# Evidence · 证据与产物

本层保留可追溯的文件版本、来源关系、执行关联和事件记录，使用户能够检查结果从何而来。
文件存在、引用格式正确、谱系完整和科学结论成立，是需要分别核实的四件事。

## Find the implementation · 实现入口

| 责任 | 源码入口 |
| --- | --- |
| 产物版本、内容与 SQLite 提交 | [workspace/artifact_write.go](../../internal/persistence/workspace/artifact_write.go) · [artifact_content.go](../../internal/persistence/workspace/artifact_content.go) |
| 产物与对话执行关联、来源读取 | [artifact_transcript_commit.go](../../internal/persistence/workspace/artifact_transcript_commit.go) · [artifact_lineage_read.go](../../internal/persistence/workspace/artifact_lineage_read.go) |
| 服务端产物控制与来源 API | [workspace_artifact_control_api.go](../../internal/server/workspace_artifact_control_api.go) · [workspace_artifact_lineage_api.go](../../internal/server/workspace_artifact_lineage_api.go) |
| 持久化事件及重启后的派发 | [workspace/outbox.go](../../internal/persistence/workspace/outbox.go) · [outbox](../../internal/outbox/) |
| DOI/PMID/PMCID 引用格式化 | [sourcecitation/citation.go](../../internal/sourcecitation/citation.go) |
| 研究目录的文件分类辅助 | [artifacts/research.go](../../internal/artifacts/research.go)；不是产物版本库 |

## Trust boundaries · 信任边界

`WriteArtifactVersionRealtime` 将版本元数据和对应产物事件放入同一 SQLite 事务，
并在事务内复核项目归属。流式内容、执行身份和来源关联有各自校验；
不能先修改数据库、再凭一个成功通知推断事务已经完整提交。

Outbox 使用至少一次投递，接收端必须按稳定事件 ID 去重。当前并非所有业务修改都已接入
事务 outbox；已接入范围及剩余清单由 [Transactional Outbox](../../internal/outbox/README.md)
维护，本页不声称“全产品事件均已原子化”。

引用格式化仅消费采集工具已提供的元数据，不推断缺失的文献标识，也不判断文献是否支持结论。
类似地，`ClassifyResearchArtifacts` 按文件路径和名称分类，不能作为内容质量证明。
真正的来源与完成检查还要结合执行记录、产物内容及相应科研方法。

任务历史与操作状态的存储位置、保留周期及保护范围见
[Runtime data lifecycle](../engineering/runtime-data-lifecycle.md)。不要直接删除底层数据库或文件来清理界面记录。

## Focused verification · 验证入口

从仓库根目录执行：

```sh
go test ./internal/sourcecitation -count=1
go test ./internal/persistence/workspace -run '^(TestWriteArtifactVersion|TestArtifactLineageRead)' -count=1
go test ./internal/outbox -run '^TestRealtimeOutbox' -count=1
```

这些测试覆盖引用规范化、真实临时 SQLite 中的产物提交/关联，以及 outbox 重启恢复和并发投递。
它们不代表某个用户报告已通过科学审阅。改动产物展示时还需验证
[Workbench](workbench.md) 中的预览、下载和刷新路径。

[模块导航](README.md) · [Runtime](runtime.md)
