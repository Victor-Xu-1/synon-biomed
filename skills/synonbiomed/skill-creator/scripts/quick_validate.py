#!/usr/bin/env python3
# Modified for Synon Biomed. Source attribution and Apache-2.0 terms
# are retained in the Skill's NOTICE.md and LICENSE.txt.
"""
Quick validation script for skills - minimal version
"""

import sys
import os
import re
import yaml
from pathlib import Path

def validate_skill(skill_path):
    """Basic validation of a skill"""
    skill_path = Path(skill_path)

    # Check SKILL.md exists
    skill_md = skill_path / 'SKILL.md'
    if not skill_md.exists():
        return False, "SKILL.md not found"

    # Read and validate frontmatter
    content = skill_md.read_text()
    if not content.startswith('---'):
        return False, "No YAML frontmatter found"

    # Extract frontmatter
    match = re.match(r'^---\n(.*?)\n---', content, re.DOTALL)
    if not match:
        return False, "Invalid frontmatter format"

    frontmatter_text = match.group(1)

    # Parse YAML frontmatter
    try:
        frontmatter = yaml.safe_load(frontmatter_text)
        if not isinstance(frontmatter, dict):
            return False, "Frontmatter must be a YAML dictionary"
    except yaml.YAMLError as e:
        return False, f"Invalid YAML in frontmatter: {e}"

    # Define allowed properties
    ALLOWED_PROPERTIES = {
        'name', 'description', 'license', 'allowed-tools', 'metadata',
        'compatibility', 'fold_cue', 'required-capabilities',
        # Synon runtime extensions parsed by internal/skills/loader.go.
        'tags', 'keywords', 'tools', 'arguments', 'references',
        'implementation-identities',
        'critical-constraints',
        'required-environment-packages', 'preferred-execution-assets', 'setup-evidence-urls',
    }

    # Check for unexpected properties (excluding nested keys under metadata)
    unexpected_keys = set(frontmatter.keys()) - ALLOWED_PROPERTIES
    if unexpected_keys:
        return False, (
            f"Unexpected key(s) in SKILL.md frontmatter: {', '.join(sorted(unexpected_keys))}. "
            f"Allowed properties are: {', '.join(sorted(ALLOWED_PROPERTIES))}"
        )

    # Check required fields
    if 'name' not in frontmatter:
        return False, "Missing 'name' in frontmatter"
    if 'description' not in frontmatter:
        return False, "Missing 'description' in frontmatter"

    # Extract name for validation
    name = frontmatter.get('name', '')
    if not isinstance(name, str):
        return False, f"Name must be a string, got {type(name).__name__}"
    name = name.strip()
    if name:
        # Check naming convention (kebab-case: lowercase with hyphens)
        if not re.match(r'^[a-z0-9-]+$', name):
            return False, f"Name '{name}' should be kebab-case (lowercase letters, digits, and hyphens only)"
        if name.startswith('-') or name.endswith('-') or '--' in name:
            return False, f"Name '{name}' cannot start/end with hyphen or contain consecutive hyphens"
        # Check name length (max 64 characters per spec)
        if len(name) > 64:
            return False, f"Name is too long ({len(name)} characters). Maximum is 64 characters."

    # Extract and validate description
    description = frontmatter.get('description', '')
    if not isinstance(description, str):
        return False, f"Description must be a string, got {type(description).__name__}"
    description = description.strip()
    if description:
        # Check for angle brackets
        if '<' in description or '>' in description:
            return False, "Description cannot contain angle brackets (< or >)"
        # Check description length (max 1024 characters per spec)
        if len(description) > 1024:
            return False, f"Description is too long ({len(description)} characters). Maximum is 1024 characters."

    # Validate compatibility field if present (optional)
    compatibility = frontmatter.get('compatibility', '')
    if compatibility:
        if not isinstance(compatibility, str):
            return False, f"Compatibility must be a string, got {type(compatibility).__name__}"
        if len(compatibility) > 500:
            return False, f"Compatibility is too long ({len(compatibility)} characters). Maximum is 500 characters."

    # Closed scientific capability contract used by the Synon runtime. Keep
    # validation aligned with internal/skills so packaging cannot accept a
    # declaration that runtime loading later rejects (or reject a valid one).
    capabilities = frontmatter.get('required-capabilities')
    if capabilities is not None:
        if not isinstance(capabilities, list) or not capabilities:
            return False, "required-capabilities must be a non-empty string sequence"
        seen_capabilities = set()
        for capability in capabilities:
            if not isinstance(capability, str):
                return False, "required-capabilities must be a non-empty string sequence"
            capability = capability.strip()
            if not re.fullmatch(r'[a-z][a-z0-9]*(?:-[a-z0-9]+)*', capability) or len(capability) > 64:
                return False, f"Required capability '{capability}' is invalid"
            if capability in seen_capabilities:
                return False, f"Required capability '{capability}' is duplicated"
            seen_capabilities.add(capability)

    critical_constraints = frontmatter.get('critical-constraints')
    if critical_constraints is not None:
        if not isinstance(critical_constraints, list) or not critical_constraints:
            return False, "critical-constraints must be a non-empty string sequence"
        if len(critical_constraints) > 16:
            return False, "critical-constraints exceeds 16 entries"
        for constraint in critical_constraints:
            if not isinstance(constraint, str) or not constraint.strip():
                return False, "critical-constraints must be a non-empty string sequence"
            if len(constraint) > 512 or '\x00' in constraint or '\r' in constraint or '\n' in constraint:
                return False, "critical-constraints contains an invalid constraint"

    implementation_identities = frontmatter.get('implementation-identities')
    if implementation_identities is not None:
        if not isinstance(implementation_identities, list) or not implementation_identities:
            return False, "implementation-identities must be a non-empty string sequence"
        if len(implementation_identities) > 16:
            return False, "implementation-identities exceeds 16 entries"
        for identity in implementation_identities:
            if not isinstance(identity, str) or not identity.strip():
                return False, "implementation-identities must be a non-empty string sequence"
            if len(identity) > 160 or '\x00' in identity or '\r' in identity or '\n' in identity:
                return False, "implementation-identities contains an invalid identity"

    keywords = frontmatter.get('keywords')
    if keywords is not None:
        if isinstance(keywords, str):
            keywords = [part.strip() for part in keywords.split(',')]
        if not isinstance(keywords, list) or not keywords:
            return False, "keywords must contain one or more strings"
        if any(not isinstance(keyword, str) or not keyword.strip() for keyword in keywords):
            return False, "keywords must contain one or more strings"

    return True, "Skill is valid!"

if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("Usage: python quick_validate.py <skill_directory>")
        sys.exit(1)
    
    valid, message = validate_skill(sys.argv[1])
    print(message)
    sys.exit(0 if valid else 1)
