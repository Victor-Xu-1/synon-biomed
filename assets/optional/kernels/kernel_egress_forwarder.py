#!/usr/bin/env python3
"""Pass kernel-local proxy connections to the host policy over an inherited FD."""

from __future__ import annotations

import array
import ctypes
import os
import signal
import socket


CONTROL_FD_ENV = "SYNON_KERNEL_EGRESS_CONTROL_FD"
PORT_ENV = "SYNON_KERNEL_EGRESS_PORT"


def arm_parent_death_signal() -> None:
    parent = os.getppid()
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.prctl(1, signal.SIGTERM) != 0:
        raise OSError(ctypes.get_errno(), "prctl(PR_SET_PDEATHSIG) failed")
    if os.getppid() != parent:
        raise SystemExit("kernel parent exited before egress forwarder startup")


def main() -> None:
    arm_parent_death_signal()
    control_fd = int(os.environ.get(CONTROL_FD_ENV, "-1"))
    port = int(os.environ.get(PORT_ENV, "-1"))
    if control_fd < 3 or not 1024 <= port <= 65535:
        raise SystemExit("kernel egress forwarder configuration is invalid")
    control = socket.socket(fileno=control_fd)
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind(("127.0.0.1", port))
    listener.listen(64)
    while True:
        client, _ = listener.accept()
        descriptors = array.array("i", [client.fileno()])
        try:
            control.sendmsg(
                [b"C"],
                [(socket.SOL_SOCKET, socket.SCM_RIGHTS, descriptors.tobytes())],
            )
        finally:
            client.close()


if __name__ == "__main__":
    main()
