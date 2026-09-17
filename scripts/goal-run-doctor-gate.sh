#!/usr/bin/env bash
set -euo pipefail

DOCTOR_JSON="${1:-}"
STRICT="${SYNON_GOAL_STRICT:-0}"
ALLOWED_PARTIAL="${SYNON_GOAL_ALLOWED_PARTIAL:-}"

if [[ -z "$DOCTOR_JSON" || ! -f "$DOCTOR_JSON" ]]; then
  echo "ERROR: doctor json is required" >&2
  exit 2
fi

PASS=$(jq '.result.summary.pass // 0' "$DOCTOR_JSON")
PARTIAL=$(jq '.result.summary.partial // 0' "$DOCTOR_JSON")
MISSING=$(jq '.result.summary.missing // 0' "$DOCTOR_JSON")
FAIL=$(jq '.result.summary.fail // 0' "$DOCTOR_JSON")

if [[ "$FAIL" -gt 0 ]]; then
  echo "ERROR: AgentRuntimeDoctor reported failing areas." >&2
  jq -r '.result.areas[] | select(.status=="fail") | "  " + .name' "$DOCTOR_JSON" >&2
  exit 1
fi

if [[ "$MISSING" -gt 0 ]]; then
  echo "ERROR: AgentRuntimeDoctor reported missing areas." >&2
  jq -r '.result.areas[] | select(.status=="missing") | "  " + .name' "$DOCTOR_JSON" >&2
  exit 1
fi

case "$STRICT" in
  1|true|TRUE|yes|YES)
    if [[ "$PARTIAL" -gt 0 ]]; then
      IFS=',' read -r -a allowed_partial_names <<<"$ALLOWED_PARTIAL"
      unexpected_partial=()
      while IFS= read -r area; do
        allowed=false
        for candidate in "${allowed_partial_names[@]}"; do
          candidate="${candidate//[[:space:]]/}"
          if [[ -n "$candidate" && "$candidate" == "$area" ]]; then
            allowed=true
            break
          fi
        done
        if [[ "$allowed" != true ]]; then
          unexpected_partial+=("$area")
        fi
      done < <(jq -r '.result.areas[] | select(.status=="partial") | .name' "$DOCTOR_JSON")
      if [[ ${#unexpected_partial[@]} -gt 0 ]]; then
        echo "ERROR: strict goal run has non-allowlisted partial AgentRuntimeDoctor areas." >&2
        printf '  %s\n' "${unexpected_partial[@]}" >&2
        exit 1
      fi
    fi
    ;;
esac

echo "doctor-gate: pass=$PASS partial=$PARTIAL missing=$MISSING fail=$FAIL strict=$STRICT allowed_partial=${ALLOWED_PARTIAL:-none}"
