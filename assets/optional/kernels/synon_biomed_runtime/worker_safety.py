"""Process startup hardening; namespace and network policy remain host-owned."""
import ctypes
import os
import sys


def harden_process():
    for name in os.environ.pop("OPERON_SECRET_VARS", "").replace(",", " ").split():
        os.environ.pop(name, None)
    if not sys.platform.startswith("linux"):
        return
    # Linux PR_SET_DUMPABLE is a public process API. Apply it after exec,
    # which can reset process dumpability even when the launcher disabled it.
    libc = ctypes.CDLL(None, use_errno=True)
    prctl = libc.prctl
    prctl.argtypes = [ctypes.c_int, ctypes.c_ulong, ctypes.c_ulong, ctypes.c_ulong, ctypes.c_ulong]
    prctl.restype = ctypes.c_int
    if prctl(4, 0, 0, 0, 0) != 0:
        raise OSError(ctypes.get_errno(), "kernel process hardening failed")
