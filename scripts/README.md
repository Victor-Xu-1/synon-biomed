# Scripts · 开发、验证与打包

本目录服务于源码安装、契约检查和可复现构建，不是第二套产品业务逻辑。
先从已有命令入口寻找脚本，不复制一条平行的安装、测试或发布链。

| 要做什么 | 入口 | 权威说明 |
| --- | --- | --- |
| 从源码安装和启动 | [dev/install-source-cli.sh](dev/install-source-cli.sh) · [dev/source-quickstart.sh](dev/source-quickstart.sh) | [运维手册](../docs/operations-runbook.md) |
| 运行已有开发目标 | [根 Makefile](../Makefile) | 先选受影响层，再调用对应目标 |
| 检查拓扑、依赖、身份与清单 | [quality](quality/) | [Quality tools](quality/README.md) |
| 检查源码、秘密、Web 契约与依赖来源 | [audit](audit/) | [源码卫生](../docs/engineering/source-hygiene.md) |
| 选择 PR 的受影响测试 | [quality/pr_fast_scope.py](quality/pr_fast_scope.py) | [验证范围](../docs/engineering/verification-scope.md) |
| 校验或生成 Skill 摘要清单 | [skills-manifest](skills-manifest/) | [Skills 目录说明](../skills/README.md) |
| 查找协议与兼容测试辅助程序 | [compat](compat/) | [兼容性目录](../docs/compatibility/README.md) |
| 打包、安装与升级验收 | [packaging](packaging/) 和根目录 release/install 脚本 | [发布验收契约](../docs/release-acceptance-contract.md) |

运行脚本前核对参数、目标地址、输出路径和副作用。尤其是 live smoke、安装、升级和清理脚本，
不能因为名字含有 test 就默认无副作用。临时日志、数据库、截图和候选制品放在外部或既有 ignored 目录；
不要将真实凭据、用户数据或当前机器的运行记录写入源码。

各命令的详细参数保留在上表对应说明中。本页只负责导航，不维护第二份质量或发布门清单。

[模块导航](../docs/modules/README.md) · [文档目录](../docs/README.md)
