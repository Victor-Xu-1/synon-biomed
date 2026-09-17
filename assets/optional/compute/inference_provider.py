"""Credential-scoped adapter for managed HTTP inference kernels."""

from __future__ import annotations

import builtins
import os
import re
from typing import Any, Callable, NoReturn


class InferenceProvider:
    secret_env_prefixes = ("INFER_", "NVIDIA_", "BASE_URL")
    token_scrub_regex = re.compile(r"(?i)bearer\s+[A-Za-z0-9._~+/-]+")
    _alias_name_re = re.compile(r"(?!LD_|DYLD_|MALLOC_)[A-Z][A-Z0-9_]{0,127}")
    _alias_denylist = frozenset({
        "PATH", "HOME", "USER", "SHELL", "TMPDIR", "LANG", "LC_ALL", "PWD",
        "OLDPWD", "TERM", "HOSTNAME", "DISPLAY", "SSH_AUTH_SOCK", "PYTHONPATH",
        "NODE_PATH", "CONDA_PREFIX", "VIRTUAL_ENV", "BASH_ENV", "ENV",
        "PROMPT_COMMAND", "IFS", "PYTHONSTARTUP", "NODE_OPTIONS", "GIT_SSH_COMMAND",
        "GIT_ASKPASS", "PYTHONHOME", "SHELLOPTS", "PS4", "ZDOTDIR", "MAKEFILES",
        "GCONV_PATH", "GLIBC_TUNABLES", "LOCPATH", "GETCONF_DIR", "HOSTALIASES",
        "LOCALDOMAIN", "NIS_PATH", "NLSPATH", "RESOLV_HOST_CONF", "RES_OPTIONS",
        "TZDIR", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "SSL_CERT_FILE",
        "SSL_CERT_DIR", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE",
        "NODE_EXTRA_CA_CERTS", "NODE_TLS_REJECT_UNAUTHORIZED", "AWS_CA_BUNDLE",
        "GIT_SSL_CAINFO", "GIT_SSL_NO_VERIFY", "HTTP_PROXY", "HTTPS_PROXY",
        "NO_PROXY", "ALL_PROXY",
    })

    def __init__(self, *, repl: bool = False):
        self._repl = repl
        self._base_url = ""

    def apply_auth(self, credentials: dict[str, str]) -> None:
        base_url = credentials.get("base_url", "").strip()
        if not base_url:
            raise RuntimeError("managed inference BASE_URL is unavailable")
        self._base_url = base_url
        os.environ["BASE_URL"] = base_url
        value = credentials.get("credential_value", "")
        name = credentials.get("credential_name", "").strip()
        if value:
            os.environ["INFER_API_KEY"] = value
            if self._alias_name_re.fullmatch(name) and name not in self._alias_denylist:
                os.environ[name] = value
            escaped = re.escape(value)
            self.token_scrub_regex = re.compile(
                rf"(?:{escaped}|(?i:bearer\s+[A-Za-z0-9._~+/-]+))"
            )

    def import_and_patch(self) -> None:
        builtins.BASE_URL = self._base_url

    def install_unauth_hook(self, callback: Callable[[], NoReturn]) -> None:
        self._unauth_callback = callback


PROVIDER = InferenceProvider
