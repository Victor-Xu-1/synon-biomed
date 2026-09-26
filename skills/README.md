# Biomedical Skills

`skills/synonbiomed/` contains the user-facing biomedical capability bundles.
Each Skill is an explicit, versioned source of capability metadata and
instructions; it is discovered through the generated catalog and activated by
the product's capability and permission contracts.

Keep Skill content focused on the capability it declares. Runtime behavior,
provider transport, persistence, and UI presentation belong to their owning
modules under `internal/` or `frontend/`.

See the [capability map](../docs/product/capability-map.md),
[Harness conformance contract](../docs/engineering/synon-harness-conformance.md),
and repository topology before adding or relocating a Skill.
