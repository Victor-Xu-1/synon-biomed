# Versioning and releases / 版本与发布

## Product version / 产品版本

[`product-identity.json`](../../product-identity.json) is the only product-version authority.
Code, packages, health responses, and user-visible version text must derive from it.
Do not add a second `VERSION` file or hard-coded product version.

[`product-identity.json`](../../product-identity.json) 是唯一的产品版本权威源。
代码、安装包、健康接口和界面版本必须由它派生，不再增加第二份 `VERSION` 文件或硬编码版本。

## Automatic version proposals / 自动版本提案

The pinned Release Please action opens or updates one version PR after changes
land on `main`. It never merges that PR or publishes a release by itself.
The current release line is deliberately limited to `v0.1.x`: every automated
proposal advances only the patch component, regardless of whether the change
is a fix, feature, or marked incompatible. Use Conventional Commit titles when
squash-merging ordinary PRs:

- `fix:`, `feat:`, `feat!:` and a `BREAKING CHANGE:` footer are all recorded
  in the changelog and advance the `v0.1.x` patch version.
- `feat!:` or a `BREAKING CHANGE:` footer still requires maintainer review for
  compatibility, but does not silently move the project to `v0.2.0`.
- Documentation and maintenance commits do not automatically force a release.

Moving to a new minor line (for example `v0.2.0`) is a deliberate release
policy change and requires an explicit maintainer decision followed by a
reviewed configuration PR; it is not inferred from a commit type.

The version PR updates `product-identity.json`, its frontend projections and
`docs/CHANGELOG.md`. The file `.github/release-please-manifest.json` is bot
bookkeeping only; no runtime reads it. The identity gate prevents projection
drift. The version configuration and identity gate reject versions outside
`0.1.x`. `initial-version` applies only to the first release, not every release.
Published versions are never moved; fixes are delivered in a new version.

After proposing a version, trusted tooling from the exact main revision validates
the complete proposal delta. Only the registered version fields, changelog and
derived provenance outputs may differ. It refreshes the frontend migration
fingerprints and license report in a separate non-force commit on the version
PR. Dependencies and all non-version JSON fields must remain unchanged; proposal
code is never executed. Concurrent main/PR edits fail closed. Normal PR review
and CI still apply, and no tag, release or main-branch write is performed.

After reviewing and merging a version PR, run the existing full quality
workflow once for that exact revision, then promote its verified artifacts to
the matching immutable Release. Its publication triggers the Packages workflow.
PR/main affected checks and the release quality matrix have different purposes;
ordinary changes do not trigger a full release matrix.

### One-time bot setup

Register a private GitHub App and install it on **this repository only**.
Grant repository Contents, Pull requests and Issues read/write (Issues is needed
for release lifecycle labels); Metadata read is automatic. No organization,
administration or user-data permissions and no webhook are needed.
Create a `release-automation` GitHub Environment restricted to the `main`
branch, set repository variable `RELEASE_APP_ID` to the App's **Client ID**
(the value expected by `client-id`), and store
`RELEASE_APP_PRIVATE_KEY` as an **environment secret**, not a repository-wide
secret. Never paste the private key into issues, PRs or chats.

The workflow creates a short-lived token restricted to this repository and
these permissions. Using an App ensures the generated PR triggers normal CI;
the default `GITHUB_TOKEN` would suppress those follow-on workflow events.
Do not substitute a broad personal token. Missing bot configuration is a setup
error, not a reason to skip PR review or the required checks.

See the upstream [Release Please action](https://github.com/googleapis/release-please-action)
and [GitHub App token action](https://github.com/actions/create-github-app-token).

## Release promotion / 正式发布

- Use Semantic Versioning: `MAJOR.MINOR.PATCH`; the active policy currently
  permits only `0.1.PATCH` releases.
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

- 使用语义化版本 `MAJOR.MINOR.PATCH`；当前活动策略只允许 `0.1.PATCH` 发布。
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

## Package publication / 分发包发布

The `release: published` event triggers
[`.github/workflows/packages.yml`](../../.github/workflows/packages.yml).
It checks the immutable release, exact source revision, successful full-quality
run and every candidate digest before packaging the exact Linux/Windows archives
as an OCI distribution bundle. Binaries and Web assets are not rebuilt.
ORAS performs a local push/pull roundtrip and a digest-bound remote pull after
publication; every filename and checksum must match the original release.

Only the publishing job receives `packages: write`; normal PR/main CI stays
read-only. Publication uses the short-lived `GITHUB_TOKEN`, not a personal token.
The OCI source annotation links the package to this repository. The transport
action is pinned to a reviewed commit and the ORAS CLI has an explicit version.

Use `ghcr.io/victor-xu-1/synon-biomed:vMAJOR.MINOR.PATCH` or a digest, not a
moving `latest` tag. An existing version is never overwritten. A failed job
before push can be rerun; after any partial push, inspect the package first.
Publishing a release through automation must account for GitHub's rule that
events created using `GITHUB_TOKEN` do not trigger another workflow. The release
operator therefore publishes through an explicitly authorized maintainer session.

For the first package, check its visibility and repository linkage in GitHub
Packages settings. GitHub may initially create it as private even for a public
source repository. A release and a package are separate delivery results:
verify an anonymous pull before announcing a public package.

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
