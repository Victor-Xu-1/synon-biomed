"""Client-side X.509 profile policy for bundled MCP connectors.

Python 3.13 and current urllib3 releases arm ``VERIFY_X509_STRICT`` by
default. Some trusted enterprise TLS-inspection roots are valid for chain,
signature, expiry, and hostname verification but do not satisfy every RFC 5280
profile detail. The Synon host therefore resolves one operator-gated posture
and passes ``SYNON_MCP_X509_STRICT=0`` only when relaxation is authorized.

Relaxation clears only the X509_STRICT profile bit immediately before a client
handshake. Server-side wraps and all other verification flags remain intact.
The wrappers are idempotent so repeated launcher initialization cannot stack
patches.
"""

from __future__ import annotations

import functools
import os
import ssl


POSTURE_ENV = "SYNON_MCP_X509_STRICT"
MARKER_ATTR = "_synon_x509_strict_relaxed"

RELAXED = "relaxed"
STRICT = "strict"
NOT_ARMED = "not-armed"

_RELAX_VALUES = frozenset({"0", "relaxed", "relax", "false", "off", "no"})


def relax_requested() -> bool:
    """Return whether the trusted host explicitly requested relaxation."""
    return os.environ.get(POSTURE_ENV, "").strip().lower() in _RELAX_VALUES


def _default_arms_strict(strict: int) -> bool:
    try:
        return bool(ssl.create_default_context().verify_flags & strict)
    except Exception:
        # Detection failure must never manufacture a relaxed posture.
        return True


def _client_side(args: tuple, kwargs: dict, server_side_pos: int) -> bool:
    if "server_side" in kwargs:
        return not kwargs["server_side"]
    if len(args) > server_side_pos:
        return not args[server_side_pos]
    return True


def relax_x509_strict() -> str:
    """Install client-only wrappers that clear ``VERIFY_X509_STRICT``."""
    strict = getattr(ssl, "VERIFY_X509_STRICT", 0)
    if getattr(ssl.SSLContext.wrap_socket, MARKER_ATTR, False) and getattr(
        ssl.SSLContext.wrap_bio, MARKER_ATTR, False
    ):
        return RELAXED
    if not strict or not _default_arms_strict(strict):
        return NOT_ARMED

    original_wrap_socket = ssl.SSLContext.wrap_socket
    original_wrap_bio = ssl.SSLContext.wrap_bio

    def relax(context: ssl.SSLContext) -> None:
        flags = context.verify_flags
        if flags & strict:
            context.verify_flags = flags & ~strict

    @functools.wraps(original_wrap_socket)
    def wrap_socket(self: ssl.SSLContext, *args, **kwargs):
        if _client_side(args, kwargs, server_side_pos=1):
            relax(self)
        return original_wrap_socket(self, *args, **kwargs)

    @functools.wraps(original_wrap_bio)
    def wrap_bio(self: ssl.SSLContext, *args, **kwargs):
        if _client_side(args, kwargs, server_side_pos=2):
            relax(self)
        return original_wrap_bio(self, *args, **kwargs)

    setattr(wrap_socket, MARKER_ATTR, True)
    setattr(wrap_bio, MARKER_ATTR, True)
    ssl.SSLContext.wrap_socket = wrap_socket
    ssl.SSLContext.wrap_bio = wrap_bio
    return RELAXED


def apply_posture() -> str:
    """Apply the host-gated posture, keeping strict defaults otherwise."""
    if not relax_requested():
        return STRICT
    return relax_x509_strict()
