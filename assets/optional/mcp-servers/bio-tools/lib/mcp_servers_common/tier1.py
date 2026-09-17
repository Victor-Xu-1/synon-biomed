"""Tier-1 drop-in MCP server runner.

Tier-1 servers must expose tool names, parameter names, and parameter
schemas that match the ORIGINAL hosted connectors exactly. Rather than
fighting a schema generator into byte-parity, each tier-1 server embeds the
original connector's tool schemas verbatim (``schemas.json``, captured live
from the hosted connector — see ``mcp-servers/_snapshots/``) and serves them
through the low-level MCP ``Server`` API. Handlers receive the validated
arguments and return the tool's text output (a string, usually
pretty-printed JSON, matching the original connector's wire format).

Handlers are plain sync callables ``(args: dict) -> str`` — fleet packages
are synchronous (requests/httpx sync clients), so calls run in a worker
thread via ``anyio.to_thread``.
"""

from __future__ import annotations

import importlib.resources
import json
from collections.abc import Callable, Mapping

import anyio
import anyio.to_thread
from mcp.server.stdio import stdio_server
from mcp.types import (
    CallToolRequestParams,
    CallToolResult,
    ListToolsResult,
    PaginatedRequestParams,
    TextContent,
    Tool,
    ToolAnnotations,
)
from jsonschema import Draft7Validator

from .errors import is_error_payload
from .mcp_compat import make_lowlevel_tools_server, mcp_error
from .ua import ContactEmailRequired

# The Go MCP client runs each stdio session under a request deadline
# (default 120s) and cancels the subprocess's command context when it fires,
# which kills this server process and loses every in-flight response
# ("closed stdout before tools/call response"). Bound handler execution well
# below that deadline so a slow upstream returns a clean, retryable error
# instead of taking the whole server down.
HANDLER_DEADLINE_S = 100.0

# All tier-1 tools are read-only retrieval (operon house rule: in-repo
# bundled servers annotate every tool explicitly).
READ_ONLY = ToolAnnotations(readOnlyHint=True)

# Handler: receives the raw (already JSON-decoded) arguments dict, returns
# the exact text payload for the single TextContent block.
Handler = Callable[[dict], str]


def load_schemas(package: str, resource: str = "schemas.json") -> list[dict]:
    """Load the embedded verbatim tool schemas from a server package."""
    with importlib.resources.files(package).joinpath(resource).open(
        "r", encoding="utf-8"
    ) as f:
        data = json.load(f)
    return data["tools"] if isinstance(data, dict) else data


def original_json(obj: object, indent: int = 2) -> str:
    """Serialize like the original connectors do (pretty JSON, insertion
    order preserved, non-ASCII kept)."""
    return json.dumps(obj, indent=indent, ensure_ascii=False)


async def invoke_handler(
    handler: Callable[[dict], str],
    args: dict,
    recoverable_exceptions: tuple[type[Exception], ...] = (),
) -> str:
    """Run one tier-1 handler under the server-side execution deadline.

    The Go MCP client cancels the subprocess's command context when its
    request deadline fires, killing this server and every in-flight call
    ("closed stdout before tools/call response"). Returning a clean,
    retryable error before that deadline keeps the process alive and lets
    the agent retry the same call after the upstream recovers.
    """
    try:
        with anyio.fail_after(HANDLER_DEADLINE_S):
            return await anyio.to_thread.run_sync(
                lambda: handler(args), abandon_on_cancel=True
            )
    except TimeoutError:
        return original_json({
            "ok": False,
            "code": "upstream_timeout",
            "error": f"upstream request exceeded the {HANDLER_DEADLINE_S:g}s server-side budget; retry later",
            "retryable": True,
            "sourceUnavailable": True,
        })
    except ContactEmailRequired as exc:
        return original_json({
            "ok": False,
            "code": "contact_email_required",
            "error": str(exc),
            "retryable": False,
            "sourceUnavailable": False,
        })
    except recoverable_exceptions:
        # A source connector that exhausted its own bounded transport retries
        # still completed its local operation deterministically. Return that
        # typed outcome as data so the agent can re-plan with another admitted
        # source; programming errors remain exceptions and therefore visible
        # tool failures.
        return original_json({
            "ok": False,
            "code": "upstream_unavailable",
            "error": "authoritative upstream source is temporarily unavailable after bounded retries",
            "retryable": True,
            "sourceUnavailable": True,
        })


class Tier1Server:
    """A drop-in MCP server: verbatim schemas + per-tool handlers."""

    def __init__(self, name: str, schemas: list[dict],
                 handlers: Mapping[str, Handler], *,
                 recoverable_exceptions: tuple[type[Exception], ...] = ()) -> None:
        schema_names = {s["name"] for s in schemas}
        missing = schema_names.symmetric_difference(handlers)
        if missing:
            raise ValueError(f"handler/schema mismatch: {sorted(missing)}")
        self.name = name
        self.schemas = schemas
        self.handlers = dict(handlers)
        if any(not isinstance(item, type) or not issubclass(item, Exception)
               for item in recoverable_exceptions):
            raise TypeError("recoverable_exceptions must contain Exception types")
        self.recoverable_exceptions = tuple(recoverable_exceptions)
        self._output_schema = {s["name"]: s.get("output_schema") for s in schemas}
        self._input_validator = {}
        for schema in schemas:
            input_schema = schema["input_schema"]
            Draft7Validator.check_schema(input_schema)
            self._input_validator[schema["name"]] = Draft7Validator(input_schema)
        async def _list_tools(_ctx, _params: PaginatedRequestParams | None) -> ListToolsResult:
            return ListToolsResult(tools=[
                Tool(name=s["name"], description=s["description"],
                     inputSchema=s["input_schema"],
                     outputSchema=s.get("output_schema"),
                     annotations=READ_ONLY)
                for s in self.schemas
            ])

        async def _call_tool(_ctx, params: CallToolRequestParams) -> CallToolResult:
            tool = params.name
            arguments = params.arguments
            handler = self.handlers.get(tool)
            if handler is None:
                raise mcp_error(-32602, f"Unknown tool: {tool}")
            args = dict(arguments or {})
            validation_error = self.input_validation_error(tool, args)
            if validation_error is not None:
                text = original_json(validation_error)
                return CallToolResult(
                    content=[TextContent(type="text", text=text)], isError=True)
            text = await invoke_handler(
                handler, args, self.recoverable_exceptions)
            content = [TextContent(type="text", text=text)]
            # structuredContent is required when outputSchema is advertised
            # (MCP spec; both the python server and the TS client enforce it).
            # Handlers return JSON text — parse it. Returning CallToolResult
            # directly bypasses the python server's own schema validation (our
            # inferred schemas are deliberately lenient hints, not strict
            # contracts).
            try:
                parsed = json.loads(text)
            except Exception:
                parsed = None
            structured = parsed if self._output_schema.get(tool) is not None \
                else None
            # Error-shaped payloads ({"error": ...}) surface as isError=True
            # so the MCP client's try/except fires — see
            # mcp_servers_common.errors (06-25 probe, cross-cutting #1).
            return CallToolResult(content=content, structuredContent=structured,
                                  isError=is_error_payload(parsed))

        self.server = make_lowlevel_tools_server(
            name, _list_tools, _call_tool)

    def input_validation_error(self, tool: str, arguments: dict) -> dict | None:
        """Validate the exact published input contract before any handler I/O.

        Error details are structural only: no argument values are echoed.
        """
        validator = self._input_validator.get(tool)
        if validator is None:
            return {"ok": False, "code": "unknown_tool",
                    "error": "tool is not in the published schema"}
        errors = sorted(
            validator.iter_errors(arguments),
            key=lambda item: (tuple(str(part) for part in item.absolute_path),
                              str(item.validator)),
        )
        if not errors:
            return None
        first = errors[0]
        path = "/" + "/".join(str(part) for part in first.absolute_path)
        return {
            "ok": False,
            "code": "invalid_tool_arguments",
            "error": "tool arguments do not match the published schema",
            "path": path,
            "keyword": str(first.validator),
            "retryable": False,
        }

    def run(self) -> None:
        """Serve on stdio (blocking)."""

        async def _main() -> None:
            async with stdio_server() as (read, write):
                await self.server.run(
                    read, write, self.server.create_initialization_options()
                )

        anyio.run(_main)
