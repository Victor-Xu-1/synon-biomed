# Security policy

Help us protect research data, credentials, workspaces, and the people using
Synon Biomed. Please report exploitable security issues privately.

安全漏洞请私下报告，不要把利用细节、访问令牌、个人数据或未公开研究发到公开 Issue、
Pull Request 或截图中。

## Reporting a vulnerability

1. Open this repository's [Security page](https://github.com/Victor-Xu-1/synon-biomed/security).
   If **Report a vulnerability** is available, use it to submit a private
   advisory.
2. If private reporting is unavailable, contact the
   [repository owner](https://github.com/Victor-Xu-1) through a contact route
   on their public profile and request a private channel. Keep exploit details
   and sensitive material out of any public request.
3. Send the smallest safe reproduction using synthetic or sanitized inputs.
   Do not test against another person's instance or access data without permission.

Include:

- affected version or commit, OS, and deployment mode;
- the affected component and required access or permissions;
- reproduction steps, expected behavior, and observed behavior;
- potential impact and any known mitigation; and
- relevant redacted logs or a minimal proof of concept.

Do not include active credentials, private keys, personal data, or confidential
research material. Do not publish exploit details in issues or pull requests
before a fix is available and disclosure has been coordinated.

## Triage and maintenance

Maintainers will acknowledge reports when they can access them, investigate
the impact, and coordinate a fix or mitigation. Release notes should identify
affected versions and remediation without exposing private report contents.
This policy does not promise a response deadline.

The current development and release line is `v0.1.x`; see the
[versioning policy](docs/governance/versioning.md). A branch checkout is not a
published release. Any security backport must be verifiable without weakening
the current security boundary; do not assume that every historical revision
receives fixes.

## Security boundaries to understand

### Workspace file tools

File-tool mutations resolve paths within their configured workspace and perform
I/O through an opened directory handle. Relative and absolute symbolic links
are supported only when their targets remain inside that workspace. A path
component changed after validation must not redirect a mutation outside it.
Root-directory aliases and overlapping destructive directory transfers are
rejected. This file-tool boundary is not an operating-system sandbox for shell
commands, scientific kernels, or other explicitly authorized host processes.

### Deployment and external services

- Keep runtime state separate from source and release files, and back it up
  before upgrades or data migrations.
- There is no compiled-in Web password. The gateway rejects unauthenticated
  non-loopback listening. Configure either `SYNON_LINK_AUTH_PASSWORD` or a
  supported external authentication provider with `SYNON_AUTH_PUBLIC_BASE_URL`;
  follow the [operations runbook](docs/operations-runbook.md) for deployment configuration.
- Models, connectors, and scientific tools may involve external services or
  host processes. Review their permissions, credentials, and data destinations
  before giving them sensitive inputs. Workspace file-path checks alone do not
  constrain all of those execution paths.
- Treat retrieved documents, model output, and tool responses as untrusted
  input, not as authority to change permissions or expose data.

## Contributing security-sensitive changes

Changes to authentication, sandboxing, network access, file handling, or release
automation require focused regression tests and an explicit security review in
the pull request. Test the rejection and failure paths as well as success.
Never commit tokens, private keys, local databases, runtime caches, or user files.

See [Contributing](CONTRIBUTING.md) for the development workflow. For
interpersonal conduct concerns, follow the [Code of conduct](CODE_OF_CONDUCT.md)
instead of submitting a software vulnerability advisory.

---

[Project overview](README.md) · [Contributing](CONTRIBUTING.md) · [Code of conduct](CODE_OF_CONDUCT.md)
