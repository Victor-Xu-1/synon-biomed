#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
ENV_FILE="$ROOT_DIR/.env.example"

if [[ ! -f "$ENV_FILE" || -L "$ENV_FILE" ]]; then
  echo "ERROR: .env.example is missing or is a symbolic link" >&2
  exit 1
fi

invalid_lines=$(grep -Ev '^[[:space:]]*$|^#[^[:cntrl:]]*$|^[A-Z][A-Z0-9_]*=[^[:cntrl:]]*$' "$ENV_FILE" || true)
if [[ -n "$invalid_lines" ]]; then
  echo "ERROR: .env.example contains unsupported shell syntax" >&2
  printf '%s\n' "$invalid_lines" >&2
  exit 1
fi

duplicates=$(sed -n 's/^\([A-Z][A-Z0-9_]*\)=.*/\1/p' "$ENV_FILE" | sort | uniq -d)
if [[ -n "$duplicates" ]]; then
  echo "ERROR: .env.example contains duplicate variables" >&2
  printf '%s\n' "$duplicates" >&2
  exit 1
fi

required_variables=(
  SYNON_ADDRESS SYNON_HOME SYNON_CONFIG SYNON_RUNNER_ENABLED
  SYNON_RUNNER_PROVIDER SYNON_RUNNER_CHAT_ENDPOINT SYNON_RUNNER_CHAT_API_KEY
  SYNON_LINK_AUTH_PASSWORD SYNON_AUTH_PUBLIC_BASE_URL
  SYNON_AUTH_GOOGLE_CLIENT_ID SYNON_AUTH_GOOGLE_CLIENT_SECRET
  SYNON_AUTH_WECHAT_APP_ID SYNON_AUTH_WECHAT_APP_SECRET
  SYNON_MCP_CONFIG SYNON_LSP_CONFIG
  SYNON_ENABLED_ADAPTERS
  FEISHU_APP_ID FEISHU_APP_SECRET
  WECHAT_ACCOUNT_ID WECHAT_BOT_TOKEN
)
for variable in "${required_variables[@]}"; do
  if ! grep -Eq "^${variable}=" "$ENV_FILE"; then
    echo "ERROR: .env.example is missing $variable" >&2
    exit 1
  fi
done

secret_variables=(
  SYNON_RUNNER_CHAT_API_KEY SYNON_COMPACT_SUMMARIZER_API_KEY
  SYNON_LINK_AUTH_PASSWORD SYNON_AUTH_GOOGLE_CLIENT_SECRET SYNON_AUTH_WECHAT_APP_SECRET
  FEISHU_APP_SECRET FEISHU_VERIFICATION_TOKEN
  FEISHU_ENCRYPT_KEY WECHAT_BOT_TOKEN
  FEISHU_TENANT_ACCESS_TOKEN
  SYNON_FEEDBACK_TOKEN
)
for variable in "${secret_variables[@]}"; do
  if ! grep -Eq "^${variable}=$" "$ENV_FILE"; then
    echo "ERROR: secret field $variable must be present and empty" >&2
    exit 1
  fi
done

if grep -Eiq '(/home/[^$[:space:]]+|[A-Z]:\\Users\\|victor|sk-[A-Za-z0-9_-]{8,}|bearer[[:space:]]+[A-Za-z0-9._-]+)' "$ENV_FILE"; then
  echo "ERROR: .env.example contains a personal path or credential-like value" >&2
  exit 1
fi

env -i PATH="$PATH" bash -c '
  set -a
  source "$1"
  set +a
  [[ "$SYNON_ADDRESS" == "127.0.0.1:8765" ]]
  [[ "$SYNON_RUNNER_ENABLED" == "true" ]]
  [[ "$SYNON_RUNNER_PROVIDER" == "workspace" ]]
  [[ -z "$SYNON_RUNNER_CHAT_API_KEY" ]]
  [[ -z "$WECHAT_BOT_TOKEN" ]]
' bash "$ENV_FILE"

echo "env-example-test: ok"
