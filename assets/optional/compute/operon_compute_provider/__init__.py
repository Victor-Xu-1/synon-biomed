"""SDK for BYOC compute providers — shared hardening + lifecycle for every
confined process. A provider is a `provider.py` that exports
`PROVIDER = <ByocProvider impl>`; the `__main__` entrypoint loads it and runs
either the per-op oneshot helper or the long-lived repl kernel.

Two entrypoints share one prologue:
  - run_oneshot() per-op helper (argv + stage/req.json → stage/reply.json),
    spawned per-op by cli/src/providers/ByocTransport.ts
  - run_repl()    long-lived compute_provider kernel (cell-by-cell, idle-timeout),
    spawned by core/src/kernels KernelManager.getOrCreateByocKernel

The prologue runs BEFORE any provider import and BEFORE reading the
credential (stdin for oneshot, fd-3 for repl), so /proc/<pid>/environ and
kern.procargs2 are empty by construction. run_oneshot self-enforces
confinement (exit 71) before touching stdin; run_repl reports it via
{ready,confined} for the host to gate.

Stdlib-only. Provider shims (the only files that import the third-party SDK)
live in core/skills/remote-compute-<id>/provider.py."""
from __future__ import annotations

import ctypes
import io
import json
import os
import re
import resource
import shlex
import signal
import sys
import threading
import time
import traceback
from typing import Any, Callable, Iterable, NoReturn, Protocol

__all__ = ["ByocError", "ByocProvider", "ByocResident", "ExecResult", "submit_staged"]

TAIL_BYTES = 8 * 1024
TAIL_RING_BYTES = 256 * 1024
CHUNK = 64 * 1024
IDLE_TIMEOUT_S = 15 * 60
# Paired with BYOC_STAGE_PREFIX in cli/src/providers/ByocTransport.ts mkStage().
STAGE_PREFIX = "/tmp/synon-biomed-provider-stage-"
# Remote workdir under the provider sandbox; wrapper.sh + harvest paths are
# all relative to this. __sim/provider.py rewrites it for local-subprocess tests.
WORK = "/work"
SUBMISSION_CONTROL = "/tmp/synon-submission-control-v1"
SUBMISSION_LOCK = "/tmp/synon-submission-control-v1.lock"
# Compressed-bytes default if the host omits output_cap_bytes (it normally
# doesn't). Intentionally 2× the host's 5 GiB decompressed cap.
COMPRESSED_CAP_DEFAULT = 10 * 2**30
EXIT_PROTOCOL = 70  # auth-handshake violation or signal — sysexits.h EX_SOFTWARE
EXIT_AUTH_EOF = 66  # retry-safe: provider code has not run before auth delivery
SUBMISSION_ID_RE = re.compile(r"^[A-Za-z0-9._~-]{16,256}$")


def _fmt_bytes(n: int) -> str:
    for unit, sh in (("GiB", 30), ("MiB", 20), ("KiB", 10)):
        if n >= 1 << sh:
            return f"{n / (1 << sh):.1f} {unit}"
    return f"{n} B"



# .job_env keys that look like forwarded credentials (the only values worth
# scrubbing from stdout/stderr tails — agent-supplied job_env values aren't).
_CRED_KEY_RE = re.compile(
    r"(?i)(?:^|_)(?:TOKEN|SECRET|KEY|PASS(?:WORD|WD)?|PWD|PW|PAT|CREDENTIAL|AUTH|BEARER|COOKIE)(?:_|$)"
)
BASE_ERROR_KINDS = frozenset({
    "not_found", "unauthorized", "rate_limited", "quota_exhausted",
    "invalid_request", "transient", "image_build_failed", "network_denied",
    "network_bridge_down", "ownership_mismatch", "provider_degraded",
    "result_rejected",
})

# ── fd-3 control channel (repl mode only) ────────────────────────────────────
# The per-op oneshot helper reads its credential from stdin (read_auth(fd=0))
# and writes nothing back; it has no fd-3. The repl kernel uses fd-3 because
# its stdin/stdout are the cell-execution channel, so the bidirectional
# ready/auth/event side-band needs a separate fd. fd 3 is a host-owned
# socketpair end (Node stdio:'pipe' on slots ≥3 is AF_UNIX/SOCK_STREAM, so
# read+write on the same fd works) carrying newline-framed JSON, 256 KiB
# line-capped on both sides.

try:
    FD_CTRL = int(os.environ.get("SYNON_PROVIDER_AUTH_FD", "3"))
except ValueError:
    FD_CTRL = -1
if FD_CTRL < 3 or FD_CTRL > 1024:
    raise RuntimeError("provider credential descriptor is invalid")
LINE_CAP = 256 * 1024


def _fd3_write(obj: dict[str, Any]) -> None:
    line = json.dumps(obj, separators=(",", ":")).encode("utf-8")
    os.write(FD_CTRL, line[:LINE_CAP] + b"\n")


def write_ready(*, confined: bool) -> None:
    _fd3_write({"ready": True, "confined": confined})


def write_event(kind: str, **extra: Any) -> None:
    _fd3_write({"event": True, "kind": kind, **extra})


def read_auth(*, fd: int = FD_CTRL) -> dict[str, str]:
    """Block for the single newline-terminated {op:"auth", ...} message the
    host writes then closes. Oneshot reads stdin (fd=0); repl reads fd-3.
    EOF before a complete line is retry-safe because no provider code has run.
    Any other invalid shape is a protocol violation."""
    buf = bytearray()
    while b"\n" not in buf:
        chunk = os.read(fd, 65536)
        if not chunk:
            sys.stderr.write(
                f"synon-provider: credential channel closed before delivery "
                f"(read {len(buf)} bytes on fd {fd})\n"
            )
            sys.exit(EXIT_AUTH_EOF)
        if len(buf) > LINE_CAP:
            sys.exit(EXIT_PROTOCOL)
        buf.extend(chunk)
    try:
        msg = json.loads(bytes(buf).split(b"\n", 1)[0])
    except ValueError:
        sys.stderr.write("synon-provider: credential handshake JSON is invalid\n")
        sys.exit(EXIT_PROTOCOL)
    if not isinstance(msg, dict) or msg.get("op") != "auth":
        sys.stderr.write("synon-provider: credential handshake shape is invalid\n")
        sys.exit(EXIT_PROTOCOL)
    return {k: v for k, v in msg.items() if k != "op"}


# ── public protocol ──────────────────────────────────────────────────────────


class ExecResult(Protocol):
    stdout: Iterable[bytes]
    stderr: Iterable[bytes]
    def wait(self) -> int: ...


class ByocProvider(Protocol):
    secret_env_prefixes: tuple[str, ...]
    token_scrub_regex: re.Pattern[str]

    def import_and_patch(self) -> None: ...
    def apply_auth(self, creds: dict[str, str]) -> None: ...
    def install_unauth_hook(self, on_expired: Callable[[], NoReturn]) -> None: ...
    def create_sandbox(self, spec: dict[str, Any], install_id: str,
                       tags: dict[str, str] | None = None) -> str:
        """Provision a sandbox. ``tags`` are host-built identity tags
        (synonbiomed-session/synonbiomed-job/…, already sanitized
        to the provider's tag constraints); implementations that support
        tagging MUST apply them at create time and merge the
        ``synonbiomed-install-id=install_id`` owner tag LAST so an
        incoming entry can never override ownership."""
        ...
    def exec(self, sandbox_id: str, argv: list[str], *, stdin: Iterable[bytes] | None = None, env: dict[str, str] | None = None, timeout: int | None = None) -> ExecResult: ...
    def list_owned(self, install_id: str) -> list[dict[str, Any]]: ...
    def find_owned_submission(
        self, install_id: str, job_id: str, submission_id: str,
    ) -> list[dict[str, Any]]: ...
    def read_owner(self, sandbox_id: str) -> str | None: ...
    def terminate(self, sandbox_id: str) -> None: ...
    def list_dir(self, root: str, path: str, *, limit: int | None = None) -> list[dict[str, Any]]:
        """List ``path`` inside the provider's persistent store named ``root``
        (for Modal, a named Volume). Returns
        ``[{name, type: "file"|"dir", size, mtime}, ...]``. ``limit`` caps
        the entry count so very wide directories don't serialize the full
        iterator through the helper. Providers without a browsable store
        omit this method; ``_op_list_dir`` surfaces that as
        ``invalid_request``."""
        ...
    def list_volumes(self) -> list[dict[str, Any]]:
        """List the provider's persistent stores (for Modal, all Volumes in
        the workspace). Returns ``[{name, created_at}, ...]``. Backs the
        file browser's landing view at ``/``. Optional for the same reason
        as ``list_dir``."""
        ...
    def read_file(self, root: str, path: str) -> Iterable[bytes]:
        """Stream ``path`` from the provider's persistent store named
        ``root`` as a bytes iterator (for Modal, ``Volume.read_file``).
        Backs the file browser's Import and Download actions for byoc.
        Optional for the same reason as ``list_dir``."""
        ...


class ByocError(Exception):
    def __init__(self, kind: str, msg: str = ""):
        self.kind = kind
        self.msg = msg


class _ScrubWriter(io.TextIOBase):
    """Courtesy filter for naive print(token). Not a control on deliberate
    exfil — the kernel grant already accepts the agent has the token's
    capabilities."""

    def __init__(self, inner: Any, pattern: re.Pattern[str]):
        self._inner = inner
        self._pat = pattern

    def write(self, s: str) -> int:
        return self._inner.write(self._pat.sub("***", s))

    def flush(self) -> None:
        self._inner.flush()

    def fileno(self) -> int:
        return self._inner.fileno()


class ByocResident:
    def __init__(self, provider: ByocProvider, *, idle_timeout_s: int = IDLE_TIMEOUT_S):
        self._p = provider
        self._idle_s = idle_timeout_s
        self._idle_timer: threading.Timer | None = None
        self._creds: dict[str, str] = {}
        self._scrub: list[str] = []

    # ── lifecycle ────────────────────────────────────────────────────────────

    def run_repl(self) -> NoReturn:
        self._prologue()
        self._handshake()
        # KernelManager.interrupt() sends SIGINT to abort a running cell —
        # restore the default handler so it raises KeyboardInterrupt inside
        # the cell instead of killing the kernel.
        signal.signal(signal.SIGINT, signal.default_int_handler)
        self._p.install_unauth_hook(self._on_auth_expired)
        sys.stdout = _ScrubWriter(sys.stdout, self._p.token_scrub_regex)
        sys.stderr = _ScrubWriter(sys.stderr, self._p.token_scrub_regex)
        # kernel_worker.main() moves protocol writes off fd 1 to a high-fd
        # wrapper (codon 187054f2 port) — publish the scrub hook so that
        # wrapper is filtered too. This also covers the stdout_chunk live
        # stream, which pre-port went via sys.__stdout__ and so was never
        # scrubbed; now every protocol byte passes the courtesy filter.
        sys._operon_protocol_stdout_wrap = (  # type: ignore[attr-defined]
            lambda s: _ScrubWriter(s, self._p.token_scrub_regex)
        )
        self._arm_idle()
        self._serve_repl()

    EXIT_UNCONFINED = 71

    def run_oneshot(self, argv: list[str]) -> NoReturn:
        signal.signal(signal.SIGINT, lambda *_: os._exit(EXIT_PROTOCOL))
        op, stage, expect_confined = argv[1], argv[2], argv[3] == "1"
        self._prologue()
        if expect_confined and not self._probe_confined():
            os._exit(self.EXIT_UNCONFINED)
        self._creds = read_auth(fd=0)
        self._scrub = [v for v in self._creds.values() if isinstance(v, str) and v]
        try:
            if op not in self._OPS:
                raise ByocError("invalid_request", f"unknown op {op!r}")
            self._p.apply_auth(self._creds)
            self._p.import_and_patch()
            with open(os.path.join(stage, "req.json"), encoding="utf-8") as f:
                req = json.load(f)
            # Host-configured app name (compute_providers.app_name) rides
            # every op's req.json beside install_id. Optional provider hook —
            # providers without an app concept simply don't define it.
            set_app = getattr(self._p, "set_app_name", None)
            if callable(set_app):
                set_app(req.get("app_name"))
            # Previously-configured app names (prior_app_names) — list_owned
            # scans them so a Default-app rename never strands sandboxes.
            set_prior = getattr(self._p, "set_prior_app_names", None)
            if callable(set_prior):
                set_prior(req.get("prior_app_names"))
            # Host-configured provider environment (for Modal, the Modal
            # Environment: compute_providers.modal_environment). Absent →
            # the provider omits the kwarg everywhere so the SDK resolves
            # its own default. Same optional-hook shape as set_app_name.
            set_env = getattr(self._p, "set_environment", None)
            if callable(set_env):
                set_env(req.get("environment"))
            reply = getattr(self, f"_op_{op}")(req)
        except ByocError as e:
            sys.stderr.write(self._redact(traceback.format_exc()))
            kind = e.kind if e.kind in BASE_ERROR_KINDS else "transient"
            reply = {"ok": False, "kind": kind, "msg": self._redact(e.msg)}
        except Exception as e:  # noqa: BLE001
            sys.stderr.write(self._redact(traceback.format_exc()))
            reply = {"ok": False, "kind": "transient", "msg": self._redact(repr(e))}
        fd = os.open(os.path.join(stage, "reply.json"),
                     os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(reply, f)
        os._exit(0)

    # ── prologue (runs before any third-party import) ────────────────────────

    def _prologue(self) -> None:
        resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
        for k in [k for k in os.environ if k.startswith(self._p.secret_env_prefixes)]:
            os.environ.pop(k, None)
        if sys.platform == "linux":
            try:
                ctypes.CDLL(None).prctl(4, 0, 0, 0, 0)
            except Exception:
                pass
        elif sys.platform == "darwin":
            try:
                ctypes.CDLL(None).ptrace(31, 0, 0, 0)
            except Exception:
                pass
        signal.signal(signal.SIGTERM, lambda *_: os._exit(EXIT_PROTOCOL))

    def _probe_confined(self) -> bool:
        if sys.platform == "darwin":
            # seatbelt has no netns; the byoc profile's strongest invariant is
            # `(deny file-read* (subpath $HOME))`. proc_listpids goes through
            # allowed sysctl and returns a positive count regardless, so probe
            # by attempting a $HOME listdir instead — EPERM ⇔ seatbelt applied.
            try:
                os.listdir(os.path.expanduser("~"))
                return False
            except PermissionError:
                return True
            except Exception:
                return False
        # Linux: confined ⇔ bwrap --unshare-net put us in a fresh netns.
        # Compare against the host's netns inode (passed via env at spawn) —
        # comparing against PID 1's fails under --unshare-pid (bwrap's reaper
        # is PID 1 in the *same* netns), and iface enumeration is brittle
        # (tunl0/sit0/ip6tnl0 auto-appear in every netns when loaded).
        try:
            mine = os.stat("/proc/self/ns/net").st_ino
        except OSError:
            return True
        host = os.environ.get("OPERON_HOST_NETNS_INO")
        if host:
            return mine != int(host)
        try:
            return mine != os.stat("/proc/1/ns/net").st_ino
        except OSError:
            return True

    def _handshake(self) -> None:
        write_ready(confined=self._probe_confined())
        self._creds = read_auth()
        self._p.apply_auth(self._creds)
        self._p.import_and_patch()

    # ── repl mode ────────────────────────────────────────────────────────────

    def _arm_idle(self) -> None:
        if self._idle_timer is not None:
            self._idle_timer.cancel()
        t = threading.Timer(self._idle_s, self._on_idle)
        t.daemon = True
        t.start()
        self._idle_timer = t

    def _on_idle(self) -> NoReturn:
        write_event("idle_exit")
        os._exit(0)

    def _on_auth_expired(self) -> NoReturn:
        write_event("auth_expired")
        os._exit(0)

    def _serve_repl(self) -> NoReturn:
        # The compute_provider kernel reuses kernel_worker.py's stdin/stdout
        # JSON cell loop verbatim — this entrypoint only owns prologue +
        # handshake + idle timer. Import and hand off so the cell protocol
        # stays single-sourced.
        # Staged layout (renderByocBootstrap) and dev layout both put
        # kernel_worker.py at <root>/kernels/ relative to <root>/compute/
        # operon_compute_provider/ — no env-var override needed.
        worker = os.path.join(
            os.path.dirname(os.path.abspath(__file__)),
            "..", "..", "kernels", "kernel_worker.py",
        )
        ns: dict[str, Any] = {"__name__": "__main__", "__file__": worker}
        with open(worker, "r", encoding="utf-8") as f:
            code = compile(f.read(), worker, "exec")

        # Execute the same modular worker entrypoint as local sessions.
        # Provider-only arguments must not enter the scientific cell namespace.
        sys.argv = [worker]

        def readline_with_idle() -> str:
            # Idle window is *between* cells (waiting for input), not during
            # one — so a 30-min image build doesn't trip the 15-min timer.
            # Lazy lookup: the worker publishes its private dup as
            # sys._operon_protocol_stdin inside main(), after we exec it.
            # NEVER fall back to a captured sys.stdin reader (#2599 r6
            # 3362707621): a second BufferedReader over fd 0 can consume
            # the host's next request — the interleave moved one layer up
            # into this shim. If the attr is absent/None, raise and let
            # the worker's readline-recovery republish it; this shim and
            # the worker ship from the same staged tree, so there is no
            # old-worker skew to accommodate.
            self._arm_idle()
            try:
                proto = getattr(sys, "_operon_protocol_stdin", None)
                if proto is None:
                    raise OSError(
                        "protocol stdin not published — worker recovery "
                        "will republish"
                    )
                line = proto.readline()
            finally:
                self._idle_timer.cancel()
            return line

        # The worker's main loop reads via sys._operon_protocol_readline
        # when present (set before exec so the worker picks it up at
        # startup). The sys.stdin.readline back-compat patch was dropped in
        # #3360: post fd-sanitize, fd 0 is /dev/null and a byoc cell's
        # input()/sys.stdin.readline() must raise EOFError immediately (per
        # specs/kernel-wire-protocol.md §3), not block on the command pipe
        # via this shim — and the shim-vs-worker co-staging noted above
        # means there's no old worker to accommodate.
        sys._operon_protocol_readline = readline_with_idle  # type: ignore[attr-defined]
        exec(code, ns)
        os._exit(0)

    # ── oneshot mode (per-job helper) ────────────────────────────────────────

    _OPS = ("create", "find_owned_submission", "submit", "wait", "probe_many", "reconcile", "terminate",
            "tail", "list_dir", "list_volumes", "read_file")

    def _op_create(self, req: dict[str, Any]) -> dict[str, Any]:
        sid = self._p.create_sandbox(
            req["spec"], req["install_id"], tags=req.get("tags") or {},
        )
        # Concurrency #1: write sid to stage/sandbox_id IMMEDIATELY so the TS
        # side can terminate it if abort (Stop → SIGTERM) lands between here
        # and reply.json being written. SIGTERM → os._exit (see _prologue), so
        # use low-level os.write (no Python-layer buffering to lose).
        try:
            stage = self._stage(req)
            fd = os.open(os.path.join(stage, "sandbox_id"),
                         os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
            try:
                os.write(fd, sid.encode("utf-8"))
            finally:
                os.close(fd)
        except Exception:
            pass  # best-effort; reply.json is the canonical path
        try:
            owner = self._p.read_owner(sid)
        except Exception:
            self._best_effort_terminate(sid)
            raise
        if owner != req["install_id"]:
            self._best_effort_terminate(sid)
            raise ByocError(
                "ownership_mismatch",
                f"created sandbox {sid} but its owner tag read back as {owner!r}, "
                f"not this Operon install — refusing to proceed (sandbox has been "
                f"best-effort terminated).",
            )
        return {"ok": True, "sandbox_id": sid}

    def _op_find_owned_submission(self, req: dict[str, Any]) -> dict[str, Any]:
        finder = getattr(self._p, "find_owned_submission", None)
        if not callable(finder):
            raise ByocError("invalid_request", "this provider cannot recover submissions")
        matches = finder(
            str(req["install_id"]), str(req["job_id"]), str(req["submission_id"])
        )
        sandbox_ids = []
        for item in matches:
            sandbox_id = item.get("sandbox_id") if isinstance(item, dict) else None
            if isinstance(sandbox_id, str) and sandbox_id not in sandbox_ids:
                sandbox_ids.append(sandbox_id)
            if len(sandbox_ids) == 2:
                break
        return {"ok": True, "sandbox_ids": sandbox_ids}

    def _best_effort_terminate(self, sid: str) -> None:
        try:
            self._p.terminate(sid)
        except Exception:
            pass

    @staticmethod
    def _drain_wait(r) -> int:
        # ContainerProcess.wait() blocks until captured stdout is drained.
        for _ in r.stdout:
            pass
        return r.wait()

    def _op_submit(self, req: dict[str, Any]) -> dict[str, Any]:
        sid = req["sandbox_id"]
        stage = self._stage(req)
        if self._p.read_owner(sid) != req["install_id"]:
            raise ByocError("ownership_mismatch", self._owner_msg(sid))
        submission_id = req.get("submission_id")
        if not isinstance(submission_id, str) or not SUBMISSION_ID_RE.fullmatch(submission_id):
            raise ByocError("invalid_request", "submission_id is invalid")
        quoted_submission_id = shlex.quote(submission_id)
        in_tgz = os.path.join(stage, "in.tar.gz")
        # Defensive fallback only — the host always sends `timeout`.
        job_timeout = int(req.get("timeout") or 14400)
        # Deadline plumbing for the wrapper's watchdog. All int()-coerced
        # before interpolation; 0 means "absent" and the wrapper falls back
        # to its own defaults (no watchdog without a deadline) — an old host
        # that sends none of these gets today's behavior.
        deadline_epoch = int(req.get("sandbox_deadline_epoch") or 0)
        remaining_s = int(req.get("sandbox_remaining_s") or 0)
        harvest_margin = int(req.get("harvest_margin_s") or 0)
        term_grace = int(req.get("term_grace_s") or 0)
        # In-place-upgrade guard (FMEA L6.1): the wrapper inside in.tar.gz
        # comes from the SUBMITTING host (its build may be old and frozen in
        # memory since boot), while this helper is re-read from disk per
        # spawn. An OLD wrapper has no watchdog and ignores the OPERON_* env
        # — pairing it with the new outer-timeout-free launch line would
        # leave NOTHING enforcing any deadline. Detect vintage by the env
        # knob in the staged wrapper text (first exact-name match wins;
        # RESERVED_INGRESS_NAMES guarantees exactly one member with this
        # name at staging, so this agrees with the last-entry-wins
        # in-sandbox extraction); unreadable → assume old, because the
        # legacy outer timeout(1) is tolerable to a NEW wrapper (it traps
        # and forwards the group TERM) while the new launch form is a
        # silent no-enforcement hole for an OLD one.
        wrapper_new = False
        try:
            import tarfile  # noqa: PLC0415
            with tarfile.open(in_tgz, "r:gz") as tf:
                # Stream members and STOP at the first exact-name match:
                # getmembers() would decompress the ENTIRE archive (multi-GB
                # for the very training workloads this redesign protects)
                # just to enumerate headers. _writeIngressTar writes the
                # wrapper FIRST and RESERVED_INGRESS_NAMES makes the name
                # unique, so the first match is the real wrapper — the scan
                # touches a few KB.
                for member in tf:
                    # Exact-match the wrapper name (optionally "./"-prefixed).
                    # NOT lstrip("./") — that strips a CHARACTER SET, so an
                    # agent input named "._operon_wrapper.sh" (legal: only
                    # exact reserved names are rejected at staging) would
                    # match with attacker-controlled content.
                    name = member.name
                    if name.startswith("./"):
                        name = name[2:]
                    if name != "_operon_wrapper.sh":
                        continue
                    fobj = tf.extractfile(member)
                    wrapper_new = (
                        fobj is not None
                        and b"OPERON_JOB_TIMEOUT_S" in fobj.read()
                    )
                    break
        except Exception:
            wrapper_new = False

        archive_sha256 = req.get("archive_sha256")
        if not isinstance(archive_sha256, str) or not re.fullmatch(r"[0-9a-f]{64}", archive_sha256):
            raise ByocError("invalid_request", "archive_sha256 is invalid")
        quoted_archive_sha256 = shlex.quote(archive_sha256)
        control = SUBMISSION_CONTROL
        lock = SUBMISSION_LOCK

        def _submission_state() -> str:
            command = (
                f"exec 9>{lock}; flock -x 9 || exit 78; "
                f"if [ ! -d {control} ]; then "
                f"if [ -d {WORK} ] && find {WORK} -mindepth 1 -print -quit | grep -q .; "
                "then printf legacy; else printf absent; fi; exit 0; fi; "
                f"[ -f {control}/submission_id ] && [ -f {control}/archive_sha256 ] && "
                f"[ -f {control}/state ] || {{ printf corrupt; exit 0; }}; "
                f"[ \"$(cat {control}/submission_id)\" = {quoted_submission_id} ] && "
                f"[ \"$(cat {control}/archive_sha256)\" = {quoted_archive_sha256} ] || "
                "{ printf conflict; exit 0; }; "
                f"cat {control}/state"
            )
            process = self._p.exec(sid, ["bash", "-c", command])
            output = bytearray()
            for chunk in process.stdout:
                if len(output) + len(chunk) > 64:
                    raise ByocError("provider_degraded", "submission state response is invalid")
                output.extend(chunk)
            if process.wait() != 0:
                raise ByocError("transient", "submission state could not be read")
            state = output.decode("ascii", "strict")
            if state not in {"absent", "staged", "launching", "running", "legacy", "corrupt", "conflict"}:
                raise ByocError("provider_degraded", "submission state response is invalid")
            return state

        submission_state = _submission_state()
        if submission_state in {"legacy", "corrupt", "conflict"}:
            raise ByocError("invalid_request", "sandbox is bound to a different submission")
        if submission_state == "launching":
            raise ByocError("transient", "submission launch state is ambiguous")
        if submission_state == "running":
            try:
                os.unlink(in_tgz)
            except OSError:
                pass
            return {"ok": True, "wrapper_deadline_aware": True}

        # Exec 1 of 2 — stream + untar the inputs. The upload is SPLIT from
        # the wrapper launch (review r10): env values are interpolated into
        # the command text before an exec starts, so a combined exec freezes
        # OPERON_SANDBOX_REMAINING_S at its pre-upload host snapshot — and
        # the wrapper re-anchors that value on the sandbox clock AFTER
        # untar, landing LATE by (actual − estimated) staging time whenever
        # the 2 MiB/s planning estimate understates the real transfer
        # (measured Modal throughput: ~0.65–0.81 MiB/s, so the late branch
        # was the common one). Uploading first lets the anchor be
        # recomputed fresh below, with no estimate at all.
        if submission_state == "absent":
            try:
                with open(in_tgz, "rb") as f:
                    r = self._p.exec(
                        sid,
                        ["bash", "-c",
                         "set -eu; archive=$(mktemp /tmp/synon-submission.XXXXXX.tar.gz); "
                         "next=$(mktemp -d /tmp/synon-work.XXXXXX); "
                         "trap 'rm -f \"$archive\"; rm -rf \"$next\"' EXIT; "
                         "cat >\"$archive\"; "
                         f"[ \"$(sha256sum \"$archive\" | cut -d' ' -f1)\" = {quoted_archive_sha256} ] || exit 77; "
                         f"exec 9>{lock}; flock -x 9 || exit 78; "
                         f"if [ -d {control} ]; then "
                         f"[ \"$(cat {control}/submission_id 2>/dev/null)\" = {quoted_submission_id} ] && "
                         f"[ \"$(cat {control}/archive_sha256 2>/dev/null)\" = {quoted_archive_sha256} ] || exit 76; "
                         "exit 0; fi; "
                         f"if [ -d {WORK} ] && find {WORK} -mindepth 1 -print -quit | grep -q .; then exit 76; fi; "
                         "tar -xzf \"$archive\" -C \"$next\"; "
                         "[ -f \"$next/_operon_wrapper.sh\" ] && [ -f \"$next/run.sh\" ] || exit 74; "
                         f"rmdir {WORK} 2>/dev/null || true; mv \"$next\" {WORK}; "
                         f"mkdir {control}; umask 077; "
                         f"printf %s {quoted_submission_id} >{control}/submission_id; "
                         f"printf %s {quoted_archive_sha256} >{control}/archive_sha256; "
                         f"printf staged >{control}/state.tmp; mv {control}/state.tmp {control}/state; "
                         "trap - EXIT; rm -f \"$archive\""],
                        stdin=iter(lambda: f.read(CHUNK), b""),
                    )
                rc = self._drain_wait(r)
            except Exception:
                raise
            finally:
                try: os.unlink(in_tgz)
                except OSError: pass
            if rc != 0:
                if rc == 78:
                    raise ByocError("transient", "submission upload lock is unavailable")
                raise ByocError(
                    "invalid_request",
                    f"submission staging failed (rc={rc})",
                )
        else:
            try:
                os.unlink(in_tgz)
            except OSError:
                pass
        # POST-upload recompute of the relative watchdog anchor: fresh
        # remaining life from the absolute deadline on this clock (the
        # helper runs on the submitting host, same clock that minted
        # deadline_epoch — no skew on this leg). The wrapper re-anchors it
        # on the SANDBOX clock within the launch exec's dispatch latency
        # (seconds), so the derived deadline now tracks the true wall no
        # matter how long staging really took. The host's pre-debited
        # estimate only governs on a one-release-older helper that still
        # runs the combined exec. max(1,·): a deadline already past arms
        # the watchdog immediately — staging margin enforcement, not an
        # error.
        if remaining_s > 0 and deadline_epoch > 0:
            remaining_s = max(1, deadline_epoch - int(time.time()))
        wrapper_env = f"OPERON_JOB_TIMEOUT_S={job_timeout}"
        # RELATIVE remaining-seconds is the authority (the wrapper anchors
        # it on the SANDBOX clock — a host-clock epoch comparison would
        # shrink the staging margin by any clock skew); the epoch rides
        # along as the wrapper's min() cross-check and as the fallback for
        # one release of wrapper skew.
        if remaining_s > 0:
            wrapper_env += f" OPERON_SANDBOX_REMAINING_S={remaining_s}"
        if deadline_epoch > 0:
            wrapper_env += f" OPERON_SANDBOX_DEADLINE_EPOCH={deadline_epoch}"
        if harvest_margin > 0:
            wrapper_env += f" OPERON_HARVEST_MARGIN_S={harvest_margin}"
        if term_grace > 0:
            wrapper_env += f" OPERON_TERM_GRACE_S={term_grace}"
        launch = (
            f"setsid env {wrapper_env} "
            if wrapper_new
            else f"setsid env {wrapper_env} "
                 f"timeout --kill-after=30 {job_timeout} "
        )
        # Exec 2 of 2 — detach the wrapper. setsid + fd redirects let it
        # survive this exec stream closing when the helper exits; the outer
        # shell returns as soon as it backgrounds. The deadline is
        # wrapper-owned: timeout(1) lives INSIDE the wrapper wrapping
        # `bash run.sh` only, so a job/deadline kill can never take the
        # tar→.phase staging down with it (the old outer timeout(1)
        # SIGKILLed the whole group, staging included).
        try:
            r2 = self._p.exec(
                sid,
                ["bash", "-c",
                 f"exec 9>{lock}; flock -x 9 || exit 78; "
                 f"[ \"$(cat {control}/submission_id 2>/dev/null)\" = {quoted_submission_id} ] && "
                 f"[ \"$(cat {control}/archive_sha256 2>/dev/null)\" = {quoted_archive_sha256} ] || exit 76; "
                 f"state=$(cat {control}/state 2>/dev/null); "
                 "[ \"$state\" = running ] && exit 0; [ \"$state\" = staged ] || exit 75; "
                 f"printf launching >{control}/state.tmp; mv {control}/state.tmp {control}/state; "
                 f"( printf running >{control}/state.tmp && mv {control}/state.tmp {control}/state && "
                 "flock -u 9 && exec 9>&- && "
                 f"cd {WORK} && {{ exec {launch}bash _operon_wrapper.sh; "
                 "rc=$?; printf 'failed:%s:0\\n' \"$rc\" >.phase; exit \"$rc\"; } ) "
                 "</dev/null >/dev/null 2>&1 & "
                 "exit 0"],
            )
            rc2 = self._drain_wait(r2)
        except Exception:
            raise
        if rc2 != 0:
            if rc2 in {75, 78}:
                raise ByocError("transient", "submission launch state is ambiguous")
            raise ByocError(
                "invalid_request",
                f"wrapper launch failed (rc={rc2}; the job did not start)",
            )
        launched_state = _submission_state()
        if launched_state == "launching":
            raise ByocError("transient", "submission launch state is ambiguous")
        if launched_state != "running":
            raise ByocError("provider_degraded", "submission did not enter running state")
        # Additive diagnostics field (old hosts ignore it): which launch
        # form ran — selftests pin the vintage-detection arms on it.
        return {"ok": True, "wrapper_deadline_aware": wrapper_new}

    def _probe_one(self, sid: str, *, poll_s: int,
                   flags: bool = True) -> dict[str, Any]:
        """Probe-only core shared by `_op_wait(probe_only=True)` and
        `_op_probe_many`. Ownership check + bounded `.phase` poll + tails;
        never streams `out.tar.gz`. Raises ByocError for ownership/not_found
        — callers map that into the per-slot error shape."""
        if self._p.read_owner(sid) != self._install_id:
            raise ByocError("ownership_mismatch", self._owner_msg(sid))
        # Bounded poll: wrapper.sh writes .phase last (after out.tar.gz mv),
        # so its presence means the harvest is complete and atomic.
        probe = self._p.exec(
            sid,
            ["bash", "-c",
             f"for i in $(seq {max(1, poll_s // 2)}); do "
             f"[ -f {WORK}/.phase ] && exit 0; sleep 2; done; exit 2"],
        )
        if self._drain_wait(probe) != 0:
            return {"ok": True, "ready": False}
        # Forwarded user creds (HF_TOKEN etc.) live only in $WORK/.job_env on
        # the sandbox; pull them into the scrub set so _tails() redacts them.
        # Source .job_env in a fresh shell and emit env -0 — NUL-delimited
        # K=V handles multiline values (SSH keys, JSON blobs) that line-wise
        # parsing of the shq()-quoted file cannot. Only credential-shaped keys
        # are scrubbed so benign agent job_env (e.g. SAMPLE=ACGT) survives.
        try:
            envcat = self._p.exec(
                sid,
                ["bash", "-c",
                 f"set -a; source {WORK}/.job_env 2>/dev/null; env -0"],
            )
            envdump = b"".join(envcat.stdout)
            envcat.wait()
            for entry in envdump.split(b"\0"):
                k, eq, v = entry.partition(b"=")
                if not eq:
                    continue
                ks = k.decode("utf-8", "replace")
                if not _CRED_KEY_RE.search(ks):
                    continue
                vs = v.decode("utf-8", "replace")
                if len(vs) >= 8:
                    self._scrub.append(vs)
        except Exception:
            pass
        # Read .phase first (poll already confirmed it exists) so job_rc /
        # job_wall_s survive a cap-exceeded break or stream error below.
        job_rc: int | None = None
        job_wall_s: int | None = None
        phase_err: str | None = None
        # EXEC failures are retried once and then surfaced as a RETRIABLE
        # probe error (review r13): the poll just proved .phase exists,
        # which proves out.tar.gz was atomically staged — a Modal blip on
        # this one cat must re-probe next tick, never flow into a terminal
        # null-rc classification whose skip-harvest arm would destroy the
        # staged outputs. PARSE failures (exec succeeded, content garbled
        # or mid-write) keep the structured phase_read_error path.
        phase: str | None = None
        cat_err: Exception | None = None
        for _ in range(2):
            try:
                phasecat = self._p.exec(sid, ["cat", f"{WORK}/.phase"])
                phase = b"".join(phasecat.stdout).decode("ascii", "replace").strip()
                phasecat.wait()
                cat_err = None
                break
            except Exception as e:
                cat_err = e
        if cat_err is not None:
            return {
                "ok": False, "kind": "transient",
                "msg": self._redact(f".phase read failed twice: {cat_err!r}"),
            }
        assert phase is not None
        tag, _, rest = phase.partition(":")
        parts = rest.split(":")
        if tag in ("done", "harvest_failed"):
            try:
                job_rc = int(parts[0])
                if len(parts) > 1:
                    job_wall_s = int(parts[1])
            except ValueError:
                phase_err = self._redact(
                    f"unparseable .phase fields: {phase!r}")
            if job_rc is not None and tag == "harvest_failed":
                phase_err = self._redact(
                    "tar/mv failed in wrapper (likely disk-full or "
                    f"read-only /work); job rc was {parts[0]}"
                )
        else:
            phase_err = self._redact(f"unrecognized .phase content: {phase!r}")
        # Deadline sentinels — additive fields; old hosts drop them at their
        # mapProbeReply allowlist. deadline_fired: the container-deadline
        # watchdog TERMed the workload (host classifies rc 143/137 +
        # sentinel as timed_out). job_timeout_fired: the per-job timeout(1)
        # escalated to KILL on a TERM-resistant workload (rc 137 + sentinel
        # → timed_out; without it, 137 stays failed — OOM killer). One exec
        # for both.
        #
        # Staleness gate: a sentinel is only honored when it is NOT newer
        # than .phase. The wrapper scrubs forgeries and writes .phase LAST,
        # so a legitimate sentinel always predates the marker — anything
        # re-created afterwards (a workload child that survived the
        # wrapper's session reap, e.g. via its own setsid) reads as newer
        # and is ignored. Residual: mtime backdating, which additionally
        # requires escaping the session reap; the workload can only
        # misclassify its OWN job either way.
        deadline_fired = False
        job_timeout_fired = False
        # Classification is one-shot at the terminal probe, so a swallowed
        # exec blip here (Modal 429/5xx) would silently read as
        # deadline_fired=false — committing a genuine watchdog kill as
        # `failed` and skipping terminate-after-harvest (review r10). Retry
        # once (the rc is already known terminal; a retry costs one exec),
        # then surface as a RETRIABLE probe error — mirroring the .phase
        # read's structured handling — so the poller re-probes next tick
        # instead of committing a wrong classification. Bounded by the
        # host's consecutive-retriable cap like every other transient.
        flags_err: Exception | None = None
        # Harvest path skips this exec entirely (flags=False, review r13):
        # classification was already committed by the earlier status()
        # probe, the sentinel fields in a harvest head are never consumed,
        # and a transient double-failure here would hard-fail the whole
        # harvest op for a fully-staged tarball — three such ticks stamp
        # harvest_failed on outputs that were one tick from streaming.
        for _ in range(2 if flags else 0):
            try:
                flags = self._p.exec(
                    sid, ["bash", "-c",
                          f"[ -f {WORK}/.deadline_fired ] && "
                          f"[ ! {WORK}/.deadline_fired -nt {WORK}/.phase ] && "
                          "printf D; "
                          f"[ -f {WORK}/.job_timeout_fired ] && "
                          f"[ ! {WORK}/.job_timeout_fired -nt {WORK}/.phase ] && "
                          "printf J; "
                          "true"])
                flag_bytes = b"".join(flags.stdout)
                flags.wait()
                deadline_fired = b"D" in flag_bytes
                job_timeout_fired = b"J" in flag_bytes
                flags_err = None
                break
            except Exception as e:
                flags_err = e
        if flags and flags_err is not None:
            return {
                "ok": False, "kind": "transient",
                "msg": self._redact(
                    f"deadline-sentinel read failed twice: {flags_err!r}"
                ),
            }
        tails = self._tails(sid)
        out: dict[str, Any] = {
            "ok": True, "ready": True,
            "job_exit_code": job_rc, "job_wall_s": job_wall_s,
            "deadline_fired": deadline_fired,
            "job_timeout_fired": job_timeout_fired,
            **tails,
        }
        if phase_err:
            out["phase_read_error"] = phase_err
        return out

    def _op_wait(self, req: dict[str, Any]) -> dict[str, Any]:
        sid = req["sandbox_id"]
        stage = self._stage(req)
        self._install_id = req["install_id"]
        poll_s = int(req.get("poll_seconds") or 30)
        head = self._probe_one(sid, poll_s=poll_s,
                               flags=bool(req.get("probe_only")))
        # probe_only: caller wants status (job_exit_code/tails/wall_s) without
        # streaming out.tar.gz — used by ByocAdapter.status() so the poller can
        # cheaply detect terminal state and only stream the tarball once, in
        # the separate harvest() call.
        if req.get("probe_only") or not head.get("ready"):
            return head
        job_rc = head.get("job_exit_code")
        job_wall_s = head.get("job_wall_s")
        phase_err = head.get("phase_read_error")
        tails = {k: head[k] for k in ("stdout_tail", "stderr_tail")}
        cap = int(req.get("output_cap_bytes") or COMPRESSED_CAP_DEFAULT)
        written = 0
        rc = -1
        stream_err: tuple[str, str] | None = None
        try:
            r = self._p.exec(sid, ["cat", f"{WORK}/out.tar.gz"])
            with open(os.path.join(stage, "out.tar.gz"), "wb") as out:
                for chunk in r.stdout:
                    out.write(chunk)
                    written += len(chunk)
                    if written > cap:
                        stream_err = (
                            "result_rejected",
                            f"compressed harvest exceeds {_fmt_bytes(cap)} cap",
                        )
                        break
            # ContainerProcess.wait() blocks until stdout is drained — discard
            # the rest so the cap path doesn't deadlock the helper. Guard the
            # drain itself so a stream-reset here doesn't clobber a cap-hit
            # diagnosis already in stream_err.
            try:
                for _ in r.stdout:
                    pass
                rc = r.wait()
            except Exception:
                pass
        except Exception as e:
            if stream_err is None:
                stream_err = (
                    "transient", self._redact(f"harvest stream failed: {e!r}")
                )
        if stream_err:
            kind, msg = stream_err
            return {
                "ok": False, "kind": kind, "msg": msg,
                "job_exit_code": job_rc, "job_wall_s": job_wall_s,
                "bytes_written": written, **tails,
            }
        out: dict[str, Any] = {
            "ok": True, "ready": True, "exit_code": rc,
            "job_exit_code": job_rc, "job_wall_s": job_wall_s,
            "bytes_written": written, **tails,
        }
        if phase_err:
            out["phase_read_error"] = phase_err
        return out

    def _op_probe_many(self, req: dict[str, Any]) -> dict[str, Any]:
        """Batched probe-only for `ByocAdapter.statusBatch()`: probe N
        sandboxes in one helper round-trip so the poller's 15s tick spawns
        one confined subprocess per provider, not one per sandbox. Per-slot
        errors are caught and returned as `{ok:False, kind, msg}` so one bad
        sandbox doesn't kill the batch — the host maps each slot through the
        same retriable/definitive/orphaned classification as single
        `probe()`."""
        self._install_id = req["install_id"]
        sids = req.get("sandbox_ids")
        if not isinstance(sids, list):
            raise ByocError("invalid_request", "sandbox_ids must be a list")
        # poll_s=2 keeps the per-sandbox latency the same as the single-probe
        # path (one `[ -f .phase ]` test, no sleep loop). The batch is
        # sequential — provider .exec() implementations open one channel per
        # sandbox, and N parallel channels under one helper would just push
        # the spawn cost into the provider SDK's own concurrency cap.
        results: list[dict[str, Any]] = []
        for sid in sids:
            if not isinstance(sid, str):
                results.append({"sandbox_id": str(sid), "ok": False,
                                "kind": "invalid_request",
                                "msg": "sandbox_id must be a string"})
                continue
            try:
                r = self._probe_one(sid, poll_s=2)
            except ByocError as e:
                kind = e.kind if e.kind in BASE_ERROR_KINDS else "transient"
                r = {"ok": False, "kind": kind, "msg": self._redact(e.msg)}
            except Exception as e:  # noqa: BLE001
                r = {"ok": False, "kind": "transient",
                     "msg": self._redact(repr(e))}
            results.append({"sandbox_id": sid, **r})
        return {"ok": True, "results": results}

    def _op_reconcile(self, req: dict[str, Any]) -> dict[str, Any]:
        return {"ok": True, "sandboxes": self._p.list_owned(req["install_id"])}

    def _op_list_dir(self, req: dict[str, Any]) -> dict[str, Any]:
        fn = getattr(self._p, "list_dir", None)
        if not callable(fn):
            raise ByocError(
                "invalid_request",
                "this byoc provider has no persistent store to browse",
            )
        limit = req.get("limit")
        entries = fn(
            str(req["root"]), str(req.get("path") or "/"),
            limit=int(limit) if limit is not None else None,
        )
        return {"ok": True, "entries": entries}

    def _op_list_volumes(self, req: dict[str, Any]) -> dict[str, Any]:
        fn = getattr(self._p, "list_volumes", None)
        if not callable(fn):
            raise ByocError(
                "invalid_request",
                "this byoc provider has no persistent store to browse",
            )
        return {"ok": True, "volumes": fn()}

    def _op_read_file(self, req: dict[str, Any]) -> dict[str, Any]:
        """Stream a file from the provider's persistent store to
        ``stage/out.bin`` so the host can import/download it. The host
        passes ``cap_bytes`` (the browser-download or import limit); the
        stream is cut off there with ``result_rejected`` so a misclick on a
        multi-GB blob doesn't fill the daemon's tmp."""
        fn = getattr(self._p, "read_file", None)
        if not callable(fn):
            raise ByocError(
                "invalid_request",
                "this byoc provider has no persistent store to read from",
            )
        stage = self._stage(req)
        cap = int(req.get("cap_bytes") or COMPRESSED_CAP_DEFAULT)
        written = 0
        fd = os.open(os.path.join(stage, "out.bin"),
                     os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        try:
            for chunk in fn(str(req["root"]), str(req.get("path") or "/")):
                if not isinstance(chunk, (bytes, bytearray)):
                    chunk = bytes(chunk)
                os.write(fd, chunk)
                written += len(chunk)
                if written > cap:
                    raise ByocError(
                        "result_rejected",
                        f"file exceeds the {_fmt_bytes(cap)} transfer cap",
                    )
        finally:
            os.close(fd)
        return {"ok": True, "size": written}

    def _op_tail(self, req: dict[str, Any]) -> dict[str, Any]:
        """Follow ``/work/{stdout,stderr}.log`` in the sandbox and stream each
        line to this process's own stdout as newline-delimited JSON
        ``{"s":"out"|"err","c":text}`` — one record per log line, with
        ``_redact()`` applied before it leaves the confined helper.

        This op is unlike every other in ``_OPS``: it does not return under
        normal operation. The host (``ByocTransport.tail()``) reads our stdout
        line-by-line for as long as a viewer is subscribed and SIGKILLs the
        helper when the last viewer disconnects, so ``run_oneshot`` never
        reaches its ``reply.json`` write. We only fall through and return
        ``{"ok": True}`` when both follows have ended on their own — which in
        practice means the sandbox is gone.
        """
        sid = req["sandbox_id"]
        if self._p.read_owner(sid) != req["install_id"]:
            raise ByocError("ownership_mismatch", self._owner_msg(sid))
        lock = threading.Lock()
        out = os.fdopen(sys.stdout.fileno(), "w", buffering=1, encoding="utf-8")

        def emit(tag: str, line: str) -> None:
            rec = json.dumps({"s": tag, "c": self._redact(line)},
                             separators=(",", ":"))
            with lock:
                out.write(rec + "\n")

        def follow(tag: str, log_path: str) -> None:
            try:
                r = self._p.exec(
                    sid, ["tail", "-c", str(TAIL_RING_BYTES), "-F", log_path])
                buf = b""
                for chunk in r.stdout:
                    buf += chunk
                    *lines, buf = buf.split(b"\n")
                    for ln in lines:
                        emit(tag, ln.decode("utf-8", "replace"))
            except Exception as e:  # noqa: BLE001 — sandbox teardown surfaces here
                emit(tag, f"[tail ended: {self._redact(repr(e))}]")

        threads = [
            threading.Thread(target=follow, args=("out", f"{WORK}/stdout.log"),
                             daemon=True),
            threading.Thread(target=follow, args=("err", f"{WORK}/stderr.log"),
                             daemon=True),
        ]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        return {"ok": True}

    def _op_terminate(self, req: dict[str, Any]) -> dict[str, Any]:
        sid = req["sandbox_id"]
        if self._p.read_owner(sid) != req["install_id"]:
            raise ByocError("ownership_mismatch", self._owner_msg(sid))
        self._p.terminate(sid)
        return {"ok": True}

    @staticmethod
    def _stage(req: dict[str, Any]) -> str:
        # Host-supplied via stage/req.json and bwrap confines writes anyway,
        # but reject anything outside the expected mkdtemp prefix so a
        # protocol bug can't be levered into an arbitrary-path open. realpath
        # both sides — on macOS /tmp is a symlink to /private/tmp.
        stage = req.get("stage")
        if not isinstance(stage, str):
            raise ByocError("invalid_request", "bad stage path")
        real = os.path.realpath(stage)
        prefix = os.path.join(
            os.path.realpath(os.path.dirname(STAGE_PREFIX)),
            os.path.basename(STAGE_PREFIX),
        )
        if not (real.startswith(prefix) and os.path.isdir(real)):
            raise ByocError("invalid_request", "bad stage path")
        return real

    @staticmethod
    def _owner_msg(sid: str) -> str:
        return (
            f"sandbox {sid} is not tagged for this Operon install — refusing to touch "
            f"it. Either it was created outside Operon or by another machine; "
            f"compute.create() will return a fresh one."
        )

    def _tails(self, sid: str) -> dict[str, str]:
        sep = b"\0---SEP---\0"
        try:
            r = self._p.exec(sid, [
                "bash", "-c",
                f"tail -c {TAIL_BYTES} {WORK}/stdout.log 2>/dev/null;"
                f" printf '\\0---SEP---\\0';"
                f" tail -c {TAIL_BYTES} {WORK}/stderr.log 2>/dev/null",
            ])
            buf = b"".join(r.stdout)
            r.wait()
        except Exception:
            return {"stdout_tail": "", "stderr_tail": ""}
        out, _, err = buf.partition(sep)
        return {
            "stdout_tail": self._redact(out.decode("utf-8", "replace")),
            "stderr_tail": self._redact(err.decode("utf-8", "replace")),
        }

    def _redact(self, s: str) -> str:
        s = self._p.token_scrub_regex.sub("***", s)
        for v in self._scrub:
            s = s.replace(v, "***")
        return "".join(c if c.isprintable() or c in "\n\t\r" else "?" for c in s)[:TAIL_BYTES]


def submit_staged(provider: ByocProvider, request: dict[str, Any]) -> dict[str, Any]:
    """Submit a trusted host-built ingress archive through the retained SDK.

    The public adapter deliberately delegates to the single mature two-phase
    upload and detached-wrapper implementation.  Callers never invoke private
    resident methods or duplicate the provider protocol.
    """
    return ByocResident(provider)._op_submit(request)
