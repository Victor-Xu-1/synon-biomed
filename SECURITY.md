# Security policy

## Supported line

The actively maintained community line is `v0.1.x`. Security fixes are
backported only when they can be verified without weakening the current
security boundary.

## Reporting a vulnerability

Please use a private GitHub Security Advisory for this repository when that
channel is available. Include a concise description, affected revision or
version, reproduction steps, impact, and any suggested mitigation. Do not
include credentials, personal data, or confidential research material.

If the private advisory channel is unavailable, contact the repository owner
through the public GitHub profile and request a private reporting channel.
Do not publish exploit details in an issue or pull request before a fix is
available.

## Response

The maintainers will acknowledge a report when they can access it, reproduce
or triage the issue, assign a severity, and coordinate a fix or mitigation.
Release notes will describe the affected versions and remediation without
exposing sensitive report contents.

## Safe development

File-tool mutations resolve paths within their configured workspace and perform
I/O through an opened directory handle. Relative and absolute symbolic links
are supported only when their targets remain inside that workspace. A path
component changed after validation must not redirect a mutation outside it.
Root-directory aliases and overlapping destructive directory transfers are
rejected. This file-tool boundary is not an operating-system sandbox for shell
commands, scientific kernels, or other explicitly authorized host processes.

Never commit tokens, private keys, local databases, runtime caches, or user
files. Changes to authentication, sandboxing, external network access, file
handling, or release automation require focused tests and an explicit security
review in the pull request.
