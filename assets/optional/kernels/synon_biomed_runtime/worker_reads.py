"""Bounded Python input-open observations; not a filesystem security boundary."""
import os
import sys
import threading


class ExecutionReadWitness:
    def __init__(self):
        self._lock = threading.Lock()
        self._active = None
        sys.addaudithook(self._observe)

    def begin(self, workspace, working):
        roots = tuple(dict.fromkeys(os.path.abspath(p) for p in (workspace, working) if p))
        with self._lock:
            self._active = {"roots": roots, "paths": set(), "truncated": False}

    def finish(self):
        with self._lock:
            state, self._active = self._active, None
        if state is None:
            return {"paths": [], "truncated": False}
        return {"paths": sorted(state["paths"]), "truncated": state["truncated"]}

    def _observe(self, event, args):
        if event != "open" or not args or not isinstance(args[0], (str, bytes)):
            return
        flags = args[2] if len(args) > 2 else 0
        if isinstance(flags, int) and flags & os.O_ACCMODE == os.O_WRONLY:
            return
        with self._lock:
            state = self._active
            if state is None:
                return
            try:
                path = os.path.abspath(os.fsdecode(args[0]))
                for index, root in enumerate(state["roots"]):
                    if os.path.commonpath((root, path)) != root:
                        continue
                    reported = os.path.relpath(path, root) if index == 0 else path
                    if len(reported) > 4096 or len(state["paths"]) >= 512:
                        state["truncated"] = True
                    else:
                        state["paths"].add(reported.replace(os.sep, "/"))
                    break
            except (ValueError, OSError):
                state["truncated"] = True
