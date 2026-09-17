#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_PATH=$(realpath -e "${BASH_SOURCE[0]}") || {
  echo "[synon-install] unable to resolve the installer path" >&2
  exit 1
}
SOURCE_ROOT=$(cd "$(dirname "$SCRIPT_PATH")/../.." && pwd -P)
WRAPPER="$SOURCE_ROOT/scripts/dev/synon"
HOME_DIR=$(realpath -e "$HOME") || {
  echo "[synon-install] unable to resolve the user home directory" >&2
  exit 1
}
BIN_DIR="$HOME_DIR/.local/bin"
TARGET="$BIN_DIR/synon"

fail() {
  echo "[synon-install] $*" >&2
  exit 1
}

command -v realpath >/dev/null 2>&1 || fail "required command not found: realpath"
command -v ln >/dev/null 2>&1 || fail "required command not found: ln"
[[ -x "$WRAPPER" ]] || fail "source launcher is missing or not executable: $WRAPPER"
[[ "$BIN_DIR" != / && "$BIN_DIR" != "" ]] || fail "refusing an unsafe empty or root bin directory"

if [[ -e "$HOME_DIR/.local" || -L "$HOME_DIR/.local" ]]; then
  [[ -d "$HOME_DIR/.local" && ! -L "$HOME_DIR/.local" ]] || fail "refusing a non-directory user .local path"
else
  mkdir "$HOME_DIR/.local"
fi
if [[ -e "$BIN_DIR" || -L "$BIN_DIR" ]]; then
  [[ -d "$BIN_DIR" && ! -L "$BIN_DIR" ]] || fail "refusing a non-directory user bin path"
else
  mkdir "$BIN_DIR"
fi
BIN_DIR=$(realpath -e "$BIN_DIR") || fail "unable to resolve user bin directory: $BIN_DIR"
TARGET="$BIN_DIR/synon"

if [[ -e "$TARGET" || -L "$TARGET" ]]; then
  if [[ -L "$TARGET" && "$(realpath -e "$TARGET" 2>/dev/null || true)" == "$WRAPPER" ]]; then
    echo "[synon-install] already installed: $TARGET"
  else
    fail "refusing to replace an existing non-Synon command: $TARGET"
  fi
else
  ln -s "$WRAPPER" "$TARGET"
  echo "[synon-install] installed: $TARGET -> $WRAPPER"
fi

[[ "$(realpath -e "$TARGET")" == "$WRAPPER" ]] || fail "installed launcher target verification failed"
case ":${PATH:-}:" in
  *":$BIN_DIR:"*) echo "[synon-install] PATH already contains $BIN_DIR" ;;
  *)
    echo "[synon-install] add this once to the current shell:"
    echo "export PATH=\"$BIN_DIR:\$PATH\""
    ;;
esac
echo "[synon-install] next command: synon start"
