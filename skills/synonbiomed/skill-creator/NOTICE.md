# Source and modification notice

This Skill retains and adapts material from Anthropic's `skill-creator`:

- Source repository: https://github.com/anthropics/skills
- Reviewed revision: `34040c9c568585f6929bedeaad110ad08f079624`
- Source directory: `skills/skill-creator/`
- Copyright: 2026 Anthropic, PBC.
- License: Apache License 2.0; complete upstream text is in `LICENSE.txt`.

Synon Biomed adapts the workflow to its existing tool names, scoped execution,
managed environments, validation rules and review surfaces. The retained
evaluation helpers remain part of this licensed Skill, not a separately
installed upstream application. Modified files carry an adaptation statement.
Synon's additional validator regression test is maintained under the root
license; the upstream materials retain Apache-2.0 and are not offered as
exclusive proprietary content.

Upstream verification at the pinned revision found unchanged normalized text
in `eval-viewer/generate_review.py`, `scripts/__init__.py` and `scripts/utils.py`.
Other retained files contain adaptations or formatting changes. This record
does not claim that every current file is byte-identical to that revision.
