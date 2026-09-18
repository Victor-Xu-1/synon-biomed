# Versioning and releases / 版本与发布

## Product version / 产品版本

[`product-identity.json`](../../product-identity.json) is the only product-version authority.
Code, packages, health responses, and user-visible version text must derive from it.
Do not add a second `VERSION` file or hard-coded product version.

[`product-identity.json`](../../product-identity.json) 是唯一的产品版本权威源。
代码、安装包、健康接口和界面版本必须由它派生，不再增加第二份 `VERSION` 文件或硬编码版本。

## GitHub release model / GitHub 发布方式

- Use Semantic Versioning: `MAJOR.MINOR.PATCH`.
- Tracked source defines only the static [release policy](release-policy.json);
  it can never grant current release or tag authorization.
- Build release candidates once in the full quality workflow. Bind the exact
  source commit/tree and every artifact name, size, and SHA-256 in
  `RELEASE_CANDIDATE.json`.
- Create an annotated tag named `vMAJOR.MINOR.PATCH` only after mandatory
  gates pass and an external signed authorization receipt matches the exact
  candidate manifest and artifact set.
- Publish one GitHub Release from that exact tag; generated release notes are configured in [`.github/release.yml`](../../.github/release.yml).
- Promote those exact candidate bytes without rebuilding. Create a draft,
  attach every asset, then publish only when GitHub Release immutability is
  verified enabled.
- Bind development evidence to a commit SHA. A branch, workflow artifact,
  candidate manifest, or build output is not a release.

- 使用语义化版本 `MAJOR.MINOR.PATCH`。
- 源码只保存静态 [发布策略](release-policy.json)，不能授予当前发布或 Tag 权限。
- 完整质量工作流只构建一次候选制品，并由 `RELEASE_CANDIDATE.json`
  绑定源码提交/tree 及每个文件的名称、大小和 SHA-256。
- 只有强制门禁通过，且仓库外签名授权收据与精确候选清单/制品集一致后，
  才创建 `vMAJOR.MINOR.PATCH` 注释标签。
- GitHub Release 必须来自该精确标签；自动发布说明由 [`.github/release.yml`](../../.github/release.yml) 管理。
- 发布只晋级这些字节，不重新构建；先创建 Draft、附加全部资产，再在确认
  GitHub Release immutability 已启用后发布。
- 开发证据绑定提交 SHA；分支、Actions 产物、候选清单或普通构建结果都不是正式发布。
- Installers impose no fixed archive-size ceiling. Archives must still pass safe-path, regular-file, checksum, release-manifest, and product-identity checks; operators must provide sufficient disk space.
- 安装器不设置固定包体积上限；包仍必须通过安全路径、普通文件类型、校验和、发布清单与产品身份验证，并由操作者保证目标磁盘空间充足。

## Container publication / 容器发布

The `release: published` event triggers
[`.github/workflows/packages.yml`](../../.github/workflows/packages.yml).
It checks the immutable release, exact source revision, successful full-quality
run and every candidate digest before wrapping the Linux archive. Application
binaries and Web assets are not rebuilt. The image adds a digest-pinned OS base
and runtime prerequisites, then passes real startup, authentication, persistence
and restart smoke checks before pushing to GHCR.

Only the publishing job receives `packages: write`; normal PR/main CI stays
read-only. Publication uses the short-lived `GITHUB_TOKEN`, not a personal token.
The OCI source label links the image to this repository. Dependabot proposes
base-image digest updates as ordinary PRs.

Use `ghcr.io/victor-xu-1/synon-biomed:vMAJOR.MINOR.PATCH` or a digest, not a
moving `latest` tag. An existing version is never overwritten. A failed job
before push can be rerun; after any partial push, inspect the package first.
Publishing a release through automation must account for GitHub's rule that
events created using `GITHUB_TOKEN` do not trigger another workflow. The release
operator therefore publishes through an explicitly authorized maintainer session.

For the first package, check its visibility and repository linkage in GitHub
Packages settings. GitHub may initially create it as private even for a public
source repository. A release and a container are separate delivery results:
verify an anonymous pull before announcing a public container.

## Paths and compatibility / 路径与兼容

Current source and documentation paths use stable responsibility-based names without a product-version suffix.
Versioned names are allowed only where the version is part of an external contract or immutable distribution identity:

- schema, migration, and compatibility boundaries under `internal/migration`, `internal/compat`, and `docs/compatibility`;
- third-party dependency versions and lock files;
- immutable release archives and installation directories.

当前源码和文档使用稳定、按职责命名且不带产品版本后缀的路径。只有版本属于外部契约或不可变分发身份时才保留版本名：

- `internal/migration`、`internal/compat`、`docs/compatibility` 中的模式、迁移和兼容边界；
- 第三方依赖版本与锁文件；
- 不可变发布包和安装目录。

Historical evidence is moved, not rewritten. Its recorded legacy paths and versions remain factual history.
历史证据只归档、不改写，其中记录的旧路径和版本仍作为事实保留。
