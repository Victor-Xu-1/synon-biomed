"""Compatibility helpers across supported Python MCP SDK generations."""

from __future__ import annotations

import inspect

from mcp.server.lowlevel import Server
from mcp.types import CallToolRequestParams

try:
    from mcp.server import MCPServer
except ImportError:  # MCP SDK 1.x
    from mcp.server.fastmcp import FastMCP as MCPServer

try:
    from mcp import MCPError as _MCPError

    def mcp_error(code: int, message: str) -> BaseException:
        return _MCPError(code, message)

except ImportError:  # MCP SDK 1.x
    from mcp import McpError as _McpError
    from mcp.types import ErrorData

    def mcp_error(code: int, message: str) -> BaseException:
        return _McpError(ErrorData(code=code, message=message))


def make_lowlevel_tools_server(name, on_list_tools, on_call_tool):
    """Build a tool-only low-level server on MCP SDK 1.x or 2.x."""

    if "on_list_tools" in inspect.signature(Server).parameters:
        return Server(
            name,
            on_list_tools=on_list_tools,
            on_call_tool=on_call_tool,
        )

    server = Server(name)

    @server.list_tools()
    async def _legacy_list_tools():
        return (await on_list_tools(None, None)).tools

    @server.call_tool(validate_input=False)
    async def _legacy_call_tool(tool_name, arguments):
        return await on_call_tool(
            None,
            CallToolRequestParams(name=tool_name, arguments=arguments),
        )

    return server
