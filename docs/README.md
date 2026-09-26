# Documentation / 文档导航

Choose a path by what you need to do. This is the single documentation index;
the root README introduces the product, while detailed behavior stays with
its source-backed reference.

按使用目的进入文档，避免把产品介绍、操作步骤和工程契约混在一起。

## Use the product / 使用产品

| Need / 目的 | Guide / 文档 |
| --- | --- |
| Understand the product, its strengths and suitable research work | [Product overview](product/README.md) |
| Install from source and complete the first setup | [Installation guide / 安装指南](getting-started.md) |
| Configure a model, start a task and inspect its outputs | [User guide / 使用手册](user-guide.md) |
| Operate, secure, diagnose or recover an instance | [Operations runbook](operations-runbook.md) |
| Understand versioned packages and upgrades | [Versioning](governance/versioning.md) |
| Understand concept illustrations and image provenance | [Visual asset provenance](assets/README.md) |

## Change the code / 开发与维护

| Need / 目的 | Guide / 文档 |
| --- | --- |
| Set up a contribution and choose scoped checks | [Contributing](../CONTRIBUTING.md) |
| Locate a subsystem, its boundary and tests | [Module index](modules/README.md) |
| Check dependency direction and placement | [Module topology](engineering/module-topology.md) |
| Change long-task/tool lifecycle behavior | [Harness conformance](engineering/synon-harness-conformance.md) |
| Change scientific execution | [Managed execution runtime](engineering/managed-execution-runtime.md) · [Kernel contract](engineering/kernel-execution-contract.md) |
| Change memory or data retention | [Memory architecture](engineering/memory-architecture.md) · [Data lifecycle](engineering/runtime-data-lifecycle.md) |
| Change tool feedback in the UI | [Presentation contract](engineering/tool-stream-presentation-contract.md) |
| Maintain source cleanliness or verification scope | [Source hygiene](engineering/source-hygiene.md) · [Verification scope](engineering/verification-scope.md) |
| Check compatibility scenarios | [Compatibility contracts](compatibility/README.md) |

## Ship and participate / 交付与协作

[Release acceptance](release-acceptance-contract.md) ·
[Repository maintenance](governance/repository-maintenance.md) ·
[Third-party inventory](THIRD_PARTY.md) ·
[Security](../SECURITY.md) ·
[Code of Conduct](../CODE_OF_CONDUCT.md) ·
[License](../LICENSE) ·
[Commercial licensing](../COMMERCIAL-LICENSE.md)

## Documentation ownership / 文档职责

- Product overview: value, feature explanations, research scenarios and selection questions.
- Installation guide: a new user's source setup and readiness checkpoints.
- User guide: everyday operations, a first useful task and output review.
- Module pages: concrete source entry points, boundaries and focused checks.
- Operations runbook: advanced configuration, packaged deployment and recovery reference.
- Engineering and machine-readable governance files: exact implementation contracts.
- Asset notes: concept-versus-capture labels and image provenance.

更新行为时同步修改对应权威文档；导航页只更新链接和摘要，不再复制一套规则。
测试命令是验证方法，不代表它在所有平台或每个 revision 上都已经通过。
