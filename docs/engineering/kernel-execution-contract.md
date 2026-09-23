# Scientific kernel execution contract

The Go Kernel Manager owns session identity, generation, authorization,
execution deadlines, cancellation, process containment and artifact accounting.
The Python worker is an interpreter boundary, not another scheduler.

## Modules and protocol

The single entrypoint is `assets/optional/kernels/kernel_worker.py`.
Modules under `synon_biomed_runtime` separate transport, output capture,
compilation, execution and process hardening. Local and provider sessions use
this same entrypoint. There is no source-string patcher or alternate worker.

Requests remain newline-delimited JSON objects containing `id`, `code`,
`origin`, `tool_name`, `workspace_dir`, optional `working_dir`,
`host_enabled` and `fresh`. The protocol is specific to this application,
not JSON-RPC or a notebook protocol.

- Successful admission emits `execution_started` before executing user code.
- Syntax and unavailable unconditional import-prefix checks return
  `code_preflight_required`, `executed=false`, without that acknowledgement.
- Standard Python statements share a namespace and working directory until the
  host restarts the session. A fresh session is a separate host-owned process.
- Stdout chunks and exactly one terminal response retain the request identity.
  Terminal fields are `stdout`, `stderr`, `error`, `interrupted`,
  `preflight`, `trace` and `usage`.
- Python, native-library and child-process output use cell capture descriptors,
  separate from the protocol. Guest stdin is EOF, not the command stream.
- Cancellation before acknowledgement remains host-owned pending state.
  During execution SIGINT interrupts the cell while preserving earlier state.
  Large protocol writes use buffered full writes so a repeated signal cannot
  silently discard the tail of a terminal response.

The existing live presentation boundary is 10 MiB with a truncation notice.
Each terminal output stream is bounded to 16 MiB of JSON-encoded text, with an
explicit notice when truncated; both streams and diagnostics fit the 48 MiB
host frame limit. Scientific datasets should be saved as artifacts, not dumped
into a console stream.

Provider sessions retain their output-scrubbing and between-cell idle callbacks.
Host RPC is implemented by the existing `synon_host_bridge` adapter; Go binds
each call to the active cell and its allowed methods. Interpreter helpers and
audit hooks do not replace the OS sandbox or server authorization.

## Process policy

The Linux/amd64 syscall filter is generated from named Linux ABI constants.
It validates the architecture, rejects unsupported syscall ABIs, and denies
process tracing, cross-process memory/fd access, keyrings, asynchronous syscall
submission, and new Unix/netlink sockets. The host's isolated network namespace
and explicit egress relay govern IP transport. The interpreter applies
`PR_SET_DUMPABLE=0` after startup.

Native macOS runtime assets may be installed, but that does not make a kernel
executable. Until a native boundary is verified, the Darwin kernel manager
reports `kernel_confinement_unavailable` and refuses to launch Python, R, Bash,
or provider workers. Requests for kernel egress also fail rather than silently
dropping their domain policy. Removing this refusal requires a real macOS
probe and regression coverage for per-task read/write and protected paths,
direct network denial with approved egress through a host broker, inherited
restrictions for Python/R subprocesses, and termination of descendants that
detach from the initial process group. Cross-compilation alone proves none of
these runtime properties. The separately supervised detached executor and
backend are Linux-only; native macOS kernel confinement alone would not
establish parity for their durable background-execution semantics. The native
in-process manager is a separate path.

See the public [Linux seccomp interface](https://www.kernel.org/doc/html/latest/userspace-api/seccomp_filter.html)
and [Python signal semantics](https://docs.python.org/3/library/signal.html).
These describe platform APIs, not an imported execution implementation.

## Integrity and verification

`python3 scripts/quality/kernel_manifest.py --write` reproducibly updates the
asset inventory; omit `--write` to verify it. The generated manifest includes
all execution modules and retains component metadata without granting licenses.
Agent definitions use the existing `agent_manifest.py` generator.

`python3 scripts/quality/test_kernel_worker_protocol.py -v` checks actual
interpreter pipes, persistence, Unicode, exceptions, stdin isolation, native
output, interruption, and optional installed plotting support. The targeted
Go kernel tests separately exercise real confinement, host calls, provider
credentials, session identity, cancellation and restart through the manager.
Full application acceptance and distribution licensing are separate gates;
passing these tests is not permission to publish unreviewed third-party assets.
