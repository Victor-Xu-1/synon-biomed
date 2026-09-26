# Skills · 科研能力内容

随产品分发的 Skill 源文件位于 [synonbiomed](synonbiomed/)。
每个 Skill 的 `SKILL.md` 描述使用范围和操作说明，相关脚本及参考资料保留在该 Skill 目录。
Skill 内容不是另一套模型传输、权限系统或环境安装器。

## Sources and generated content · 内容源与派生物

- 内容源：[synonbiomed](synonbiomed/)。
- 内容摘要清单：[skills.manifest.json](../assets/synonbiomed/skills.manifest.json)。
- 唯一清单生成器：[scripts/skills-manifest](../scripts/skills-manifest/)；`--check` 校验，
  内容变更经审阅后才使用 `--write` 更新。
- 加载与搜索：[internal/skills](../internal/skills/)；
  内置核心 `synon-runtime` 来自 `BuiltinRuntimeCatalog`，不要另建一份同名 Skill。
- 用户导入和个人 Skill 位于运行数据根目录，不是这个发布源码目录的可写副本。

新增内容需明确实际使用的工具、环境、输入和输出；不能以 Skill 文本声称依赖已安装、
连接器已授权或结果已经验证。接入边界及验证命令见
[Integrations](../docs/modules/integrations.md)，执行环境见
[Science](../docs/modules/science.md)。第三方内容沿用
[第三方清单](../docs/THIRD_PARTY.md)的来源和许可证记录。

[模块导航](../docs/modules/README.md) · [工程拓扑](../docs/engineering/module-topology.md)
