"""Synon host bridge adapter for the scientific kernel execution protocol.

This module contains only Synon host RPC protocol adaptation. Scientific API
preflight, package heuristics, and domain execution logic are prohibited here.
"""

import json
import io
import math
import os
import sys
import threading
import time

namespace = {}
_protocol_stdin = getattr(sys, "_operon_protocol_stdin", None)
_protocol_stdout = getattr(sys, "_operon_protocol_stdout", None)
_PROTOCOL_WRITE_LOCK = getattr(sys, "_operon_protocol_lock", threading.Lock())
_identity_suspect = None

# Bidirectional kernel host-call transport. The wire intentionally carries
# no frame, root, project, or user identity; the Go process binds those to
# the active execution and rejects calls whose cell id does not match.
import select as _host_select
import types as _host_types
import uuid as _host_uuid

_host_json_dumps = json.dumps
_host_json_loads = json.loads
_host_uuid4 = _host_uuid.uuid4
_host_select_read = _host_select.select
_host_monotonic = time.monotonic
_host_rpc_lock = threading.Lock()
_active_host_cell = {"id": None}
_host_mcp_catalog_cache = {"cell_id": None, "value": None, "methods": {}}
def _host_ack_timeout_seconds():
    raw = os.environ.get("OPERON_SDK_REPLY_DEADLINE_S")
    if raw:
        try:
            value = float(raw)
            if value > 0:
                return value
        except ValueError:
            pass
    return 120.0

def _host_read_frame(source, deadline, method):
    if deadline is not None:
        remaining = deadline - _host_monotonic()
        if remaining <= 0:
            raise RuntimeError(f"{method}: timed out waiting for host ACK")
        ready, _, _ = _host_select_read([source], [], [], remaining)
        if not ready:
            raise RuntimeError(f"{method}: timed out waiting for host ACK")
    # After ACK, do not select() the underlying fd again: TextIOWrapper
    # may already have prefetched a fast host_result into its user-space
    # buffer. The active cell's SIGINT/deadline interrupts this readline.
    line = source.readline(48 * 1024 * 1024 + 1)
    if not isinstance(line, str) or not line:
        raise RuntimeError(f"{method}: host transport closed while waiting")
    return line

def _host_rpc(method, args, kwargs):
    cell_id = _active_host_cell["id"]
    if not isinstance(cell_id, str) or not cell_id:
        raise RuntimeError(f"{method}: no active kernel cell")
    call_id = "hc-" + _host_uuid4().hex
    request = {
        "type": "host_call",
        "id": call_id,
        "cell_id": cell_id,
        "method": method,
        "args": list(args),
        "kwargs": dict(kwargs),
    }
    encoded = _host_json_dumps(
        request, ensure_ascii=False, separators=(",", ":")
    )
    if len(encoded.encode("utf-8")) > 48 * 1024 * 1024:
        raise RuntimeError(f"{method}: host call payload is too large")
    with _host_rpc_lock:
        with _PROTOCOL_WRITE_LOCK:
            _protocol_stdout.write(encoded + "\n")
            _protocol_stdout.flush()
        source = getattr(sys, "_operon_protocol_stdin", None)
        if source is None or _identity_suspect(source):
            raise RuntimeError(f"{method}: host transport is unavailable")
        stale_frames = 0
        ack_deadline = _host_monotonic() + _host_ack_timeout_seconds()
        while True:
            line = _host_read_frame(source, ack_deadline, method)
            try:
                response = _host_json_loads(line)
            except Exception:
                response = None
            if response == {"type": "host_ack", "for": call_id}:
                break
            stale_frames += 1
            if stale_frames > 8:
                raise RuntimeError(
                    f"{method}: too many stale frames before host ACK"
                )
        while True:
            if _active_host_cell["id"] != cell_id:
                raise RuntimeError(f"{method}: kernel cell is no longer active")
            line = _host_read_frame(source, None, method)
            try:
                response = _host_json_loads(line)
            except Exception:
                response = None
            if (
                isinstance(response, dict)
                and response.get("type") == "host_result"
                and response.get("id") == call_id
                and response.get("cell_id") == cell_id
                and isinstance(response.get("ok"), bool)
            ):
                break
            stale_frames += 1
            if stale_frames > 8:
                raise RuntimeError(
                    f"{method}: too many stale or mismatched host results"
                )
    if not response["ok"]:
        detail = response.get("error")
        message = detail.get("message") if isinstance(detail, dict) else None
        if not isinstance(message, str) or not message:
            message = "host call failed"
        if method == "mcp":
            raise RuntimeError(f"host_call 'mcp' failed: {message}")
        if method.startswith("host.compute.") and "_compute_error_from_host" in globals():
            raise _compute_error_from_host(method, detail)
        raise RuntimeError(f"{method}: {message}")
    result = response.get("result")
    if isinstance(result, dict) and len(result) == 1 and "error" in result:
        raise RuntimeError(f"{method}: {result['error']}")
    return result

class _RoutineAccessor:
    def configure(self, every_minutes, *, on_tick, label=None):
        if isinstance(every_minutes, bool) or not isinstance(every_minutes, int):
            raise TypeError(
                "host.routine.configure: every_minutes must be an int "
                f"(minutes, 5-1440), got {type(every_minutes).__name__}"
            )
        if not 5 <= every_minutes <= 1440:
            raise ValueError(
                "host.routine.configure: every_minutes must be between "
                f"5 and 1440 (inclusive), got {every_minutes!r}"
            )
        if not isinstance(on_tick, str) or not on_tick.strip():
            raise TypeError(
                "host.routine.configure: on_tick must be a non-empty "
                f"str, got {on_tick!r}"
            )
        if len(on_tick) > 2000:
            raise ValueError(
                "host.routine.configure: on_tick must be <= 2000 chars, "
                f"got {len(on_tick)}"
            )
        if label is not None and not isinstance(label, str):
            raise TypeError(
                "host.routine.configure: label must be a str or None, "
                f"got {type(label).__name__}"
            )
        if isinstance(label, str) and len(label) > 120:
            raise ValueError(
                "host.routine.configure: label must be <= 120 chars, "
                f"got {len(label)}"
            )
        return _host_rpc(
            "host.routine.configure",
            [every_minutes],
            {"on_tick": on_tick, "label": label},
        )

    def status(self):
        return _host_rpc("host.routine.status", [], {})

    def done(self, had_work, summary=""):
        if not isinstance(had_work, bool):
            raise TypeError(
                "host.routine.done: had_work must be a bool, got "
                f"{type(had_work).__name__}"
            )
        if not isinstance(summary, str):
            raise TypeError(
                "host.routine.done: summary must be a str, got "
                f"{type(summary).__name__}"
            )
        if len(summary) > 500:
            raise ValueError(
                "host.routine.done: summary must be <= 500 chars, "
                f"got {len(summary)}"
            )
        return _host_rpc("host.routine.done", [had_work, summary], {})

    def __repr__(self):
        return "<host.routine>"

_routine_accessor = _RoutineAccessor()

class _SkillsAccessor:
    def list(self):
        return _host_rpc("host.skills.list", [], {})

    def read(self, name, path="SKILL.md"):
        if not isinstance(name, str) or not name.strip():
            raise TypeError("host.skills.read: name must be a non-empty str")
        if not isinstance(path, str) or not path.strip():
            raise TypeError("host.skills.read: path must be a non-empty str")
        return _host_rpc("host.skills.read", [name, path], {})

    def edit(self, name, path, content, old_string=None):
        for label, value in (("name", name), ("path", path), ("content", content)):
            if not isinstance(value, str) or not value.strip():
                raise TypeError(f"host.skills.edit: {label} must be a non-empty str")
        if old_string is not None and not isinstance(old_string, str):
            raise TypeError("host.skills.edit: old_string must be a str or None")
        return _host_rpc(
            "host.skills.edit", [name, path, content], {"old_string": old_string or ""}
        )

    def publish(self, name, overwrite=False):
        if not isinstance(name, str) or not name.strip():
            raise TypeError("host.skills.publish: name must be a non-empty str")
        if not isinstance(overwrite, bool):
            raise TypeError("host.skills.publish: overwrite must be a bool")
        return _host_rpc("host.skills.publish", [name, overwrite], {})

    def install(self, repo, skills=None, sha=None):
        if not isinstance(repo, str) or not repo.strip():
            raise TypeError("host.skills.install: repo must be a non-empty str")
        if skills is not None and not isinstance(skills, (list, tuple)):
            raise TypeError("host.skills.install: skills must be a list or None")
        if sha is not None and not isinstance(sha, str):
            raise TypeError("host.skills.install: sha must be a str or None")
        config = {"repo": repo}
        if skills is not None:
            config["skills"] = list(skills)
        if sha is not None:
            config["sha"] = sha
        return _host_rpc("host.skills.install", [config], {})

    def delete(self, name):
        if not isinstance(name, str) or not name.strip():
            raise TypeError("host.skills.delete: name must be a non-empty str")
        return _host_rpc("host.skills.delete", [name], {})

    def __repr__(self):
        return "<host.skills>"

class _AgentsAccessor:
    def list(self):
        return _host_rpc("host.agents.list", [], {})

    def create(self, name, display_name, description, system_prompt="", skill_names=None):
        for label, value in (("name", name), ("display_name", display_name), ("description", description)):
            if not isinstance(value, str) or not value.strip():
                raise TypeError(f"host.agents.create: {label} must be a non-empty str")
        if not isinstance(system_prompt, str):
            raise TypeError("host.agents.create: system_prompt must be a str")
        if skill_names is not None and not isinstance(skill_names, (list, tuple)):
            raise TypeError("host.agents.create: skill_names must be a list or None")
        args = [name, display_name, description, system_prompt]
        if skill_names is not None:
            args.append(list(skill_names))
        return _host_rpc("host.agents.create", args, {})

    def update(self, name, patch):
        if not isinstance(name, str) or not name.strip():
            raise TypeError("host.agents.update: name must be a non-empty str")
        if not isinstance(patch, dict):
            raise TypeError("host.agents.update: patch must be a dict")
        return _host_rpc("host.agents.update", [name, dict(patch)], {})

    def delete(self, name):
        if not isinstance(name, str) or not name.strip():
            raise TypeError("host.agents.delete: name must be a non-empty str")
        return _host_rpc("host.agents.delete", [name], {})

    def attach_skill(self, name, skill):
        if not isinstance(name, str) or not name.strip() or not isinstance(skill, str) or not skill.strip():
            raise TypeError("host.agents.attach_skill: name and skill must be non-empty str values")
        return _host_rpc("host.agents.attach_skill", [name, skill], {})

    def detach_skill(self, name, skill):
        if not isinstance(name, str) or not name.strip() or not isinstance(skill, str) or not skill.strip():
            raise TypeError("host.agents.detach_skill: name and skill must be non-empty str values")
        return _host_rpc("host.agents.detach_skill", [name, skill], {})

    def attach_connector(self, name, connector, include_tools_pattern=None, exclude_tools_pattern=None):
        if not isinstance(name, str) or not name.strip() or not isinstance(connector, str) or not connector.strip():
            raise TypeError("host.agents.attach_connector: name and connector must be non-empty str values")
        kwargs = {}
        if include_tools_pattern is not None:
            if not isinstance(include_tools_pattern, str):
                raise TypeError("host.agents.attach_connector: include_tools_pattern must be a str or None")
            kwargs["include_tools_pattern"] = include_tools_pattern
        if exclude_tools_pattern is not None:
            if not isinstance(exclude_tools_pattern, str):
                raise TypeError("host.agents.attach_connector: exclude_tools_pattern must be a str or None")
            kwargs["exclude_tools_pattern"] = exclude_tools_pattern
        return _host_rpc("host.agents.attach_connector", [name, connector], kwargs)

    def detach_connector(self, name, connector):
        if not isinstance(name, str) or not name.strip() or not isinstance(connector, str) or not connector.strip():
            raise TypeError("host.agents.detach_connector: name and connector must be non-empty str values")
        return _host_rpc("host.agents.detach_connector", [name, connector], {})

    def list_connectors(self, connector_name=None):
        if connector_name is None:
            return _host_rpc("host.agents.list_connectors", [], {})
        if not isinstance(connector_name, str) or not connector_name.strip():
            raise TypeError("host.agents.list_connectors: connector_name must be a non-empty str or None")
        return _host_rpc("host.agents.list_connectors", [connector_name], {})

    def switch(self, name):
        if not isinstance(name, str) or not name.strip():
            raise TypeError("host.agents.switch: name must be a non-empty str")
        return _host_rpc("host.agents.switch", [name], {})

    def __repr__(self):
        return "<host.agents>"

class _ArchiveAccessor:
    def search(self, query, limit=8):
        if not isinstance(query, str) or not query.strip():
            raise TypeError("host.archive.search: query must be a non-empty str")
        if not isinstance(limit, int) or isinstance(limit, bool) or limit < 1 or limit > 20:
            raise ValueError("host.archive.search: limit must be an int from 1 to 20")
        return _host_rpc("host.archive.search", [query], {"limit": limit})

    def page(self, archive_index, offset=0, limit=50):
        if not isinstance(archive_index, int) or isinstance(archive_index, bool) or archive_index < 0:
            raise ValueError("host.archive.page: archive_index must be a non-negative int")
        if not isinstance(offset, int) or isinstance(offset, bool) or offset < 0:
            raise ValueError("host.archive.page: offset must be a non-negative int")
        if not isinstance(limit, int) or isinstance(limit, bool) or limit < 1 or limit > 200:
            raise ValueError("host.archive.page: limit must be an int from 1 to 200")
        return _host_rpc("host.archive.page", [archive_index], {"offset": offset, "limit": limit})

    def __repr__(self):
        return "<host.archive>"

_archive_accessor = _ArchiveAccessor()

_skills_accessor = _SkillsAccessor()
_agents_accessor = _AgentsAccessor()


def _host_mcp_normalize_input(method, positional_input, keyword_input):
    """Normalize provider-generated MCP call shapes at the Python boundary.

    The canonical wire contract is always server, method, and one JSON
    object. Some providers emit that object as a third positional value;
    others emit a structured {method, input} envelope. Accept only those
    bounded shapes, then let the Go host enforce authority and schema.
    """
    if isinstance(method, dict):
        if positional_input or keyword_input:
            raise TypeError(
                "host.mcp: structured call cannot be combined with additional arguments"
            )
        envelope = method
        allowed = {"method", "name", "input", "arguments", "kwargs"}
        if any(key not in allowed for key in envelope):
            raise TypeError("host.mcp: structured call has unsupported keys")
        method_values = [
            envelope[key] for key in ("method", "name") if key in envelope
        ]
        if (
            len(method_values) != 1
            or not isinstance(method_values[0], str)
            or not method_values[0]
        ):
            raise TypeError(
                "host.mcp: structured call requires a non-empty method string"
            )
        method = method_values[0]
        input_values = []
        for key in ("input", "arguments", "kwargs"):
            if key in envelope and envelope[key] is not None:
                input_values.append(envelope[key])
        if len(input_values) > 1:
            raise TypeError(
                "host.mcp: structured call accepts one input mapping"
            )
        keyword_input = input_values[0] if input_values else {}
    elif positional_input:
        if len(positional_input) != 1:
            raise TypeError("host.mcp: expected one positional input mapping")
        if keyword_input:
            raise TypeError(
                "host.mcp: positional input cannot be combined with keyword arguments"
            )
        keyword_input = positional_input[0]

    if not isinstance(method, str) or not method:
        raise TypeError(
            "host.mcp: method must be a non-empty str, got "
            f"{type(method).__name__}"
        )
    if keyword_input is None:
        keyword_input = {}
    if not isinstance(keyword_input, dict):
        raise TypeError("host.mcp: positional input must be a mapping")
    return method, dict(keyword_input)

class _MCPPortableDict(dict):
    """Preserve mapping semantics while bridging one unambiguous record list."""

    retrieval = None
    evidence = None

    @property
    def records(self):
        """Return the one advertised record collection, or fail closed."""
        _, records = _mcp_retrieval_records(self)
        if records is None:
            raise AttributeError(
                "MCP result has no unambiguous advertised record collection"
            )
        return records

    def __getitem__(self, key):
        if isinstance(key, int) and not isinstance(key, bool):
            record_lists = []
            seen = set()
            for candidate in dict.values(self):
                if not isinstance(candidate, list) or id(candidate) in seen:
                    continue
                seen.add(id(candidate))
                record_lists.append(candidate)
            if len(record_lists) == 1:
                return record_lists[0][key]
        return dict.__getitem__(self, key)


class _MCPParameterList(list):
    """Expose compact parameter rows and a JSON-Schema-compatible view."""

    def _properties(self):
        properties = {}
        for parameter in self:
            if not isinstance(parameter, dict):
                continue
            name = parameter.get("name")
            if not isinstance(name, str) or not name:
                continue
            properties[name] = {
                key: value for key, value in parameter.items()
                if key not in ("name", "required")
            }
        return properties

    def as_input_schema(self):
        return {
            "type": "object",
            "properties": self._properties(),
            "required": [
                parameter["name"] for parameter in self
                if isinstance(parameter, dict) and
                isinstance(parameter.get("name"), str) and
                parameter.get("required") is True
            ],
        }

    def __getitem__(self, key):
        if key == "properties":
            return self._properties()
        if key == "required":
            return self.as_input_schema()["required"]
        if key == "type":
            return "object"
        return list.__getitem__(self, key)

    def get(self, key, default=None):
        if key in ("type", "properties", "required"):
            return self[key]
        return default


_MCP_RETRIEVAL_RETURNED_KEYS = (
    "returned_count", "retrieved_count", "n_retrieved", "rows_retrieved",
    "total_retrieved", "records_retrieved", "items_returned",
)
_MCP_RETRIEVAL_TOTAL_KEYS = (
    "total_count", "totalCount", "total_available", "total_records",
    "totalRecords", "provider_total", "total_hits", "totalHits", "total",
)
_MCP_RETRIEVAL_RECORD_KEYS = (
    "records", "results", "items", "data", "hits", "documents",
    "sources", "articles", "publications", "pmids", "patents",
    "trials", "studies", "compounds", "drugs", "activities",
    "mechanisms", "targets", "projects", "experiments", "entries",
    "datasets", "variants", "genes", "proteins", "associations",
    "annotations",
)
_MCP_RETRIEVAL_NEXT_KEYS = (
    "next_cursor", "nextCursor", "next_offset", "nextOffset",
    "next_page", "nextPage", "next_page_token", "nextPageToken",
    "next_retstart", "pageToken", "end_cursor", "endCursor",
)
_MCP_RETRIEVAL_ID_KEYS = (
    "doi", "pmid", "pmcid", "nct_id", "nctId", "trial_id",
    "publication_number", "patent_number", "accession", "accession_id",
    "pdb_id", "chembl_id", "molecule_chembl_id", "target_chembl_id",
    "id", "uid", "url", "uri",
)
_MCP_RETRIEVAL_QUERY_KEYS = (
    "query", "q", "term", "search_query", "searchQuery", "text",
    "keyword", "keywords", "name",
)
_MCP_RETRIEVAL_NEXT_INPUT_KEYS = {
    "next_cursor": ("cursor", "after_cursor", "page_cursor"),
    "nextCursor": ("cursor", "afterCursor", "pageCursor"),
    "end_cursor": ("after", "cursor", "after_cursor"),
    "endCursor": ("after", "cursor", "afterCursor"),
    "next_offset": ("offset", "start", "from", "skip"),
    "nextOffset": ("offset", "start", "from", "skip"),
    "next_page": ("page", "page_number"),
    "nextPage": ("page", "pageNumber"),
    "next_page_token": ("page_token", "pageToken"),
    "nextPageToken": ("page_token", "pageToken"),
    "pageToken": ("page_token", "pageToken"),
    "next_retstart": ("retstart",),
}

_MCP_RETRIEVAL_CONTAINER_KEYS = (
    "retrieval", "pagination", "page_info", "pageInfo", "meta", "_meta",
)


def _mcp_retrieval_views(value):
    """Return the provider result and common one-level metadata envelopes."""
    if not isinstance(value, dict):
        return []
    views = [("", value)]
    seen = {id(value)}
    for key in _MCP_RETRIEVAL_CONTAINER_KEYS:
        nested = value.get(key)
        if not isinstance(nested, dict) or id(nested) in seen:
            continue
        seen.add(id(nested))
        views.append((key + ".", nested))
    return views


def _mcp_retrieval_nonnegative_int(value):
    if isinstance(value, bool):
        return None
    if isinstance(value, int) and value >= 0:
        return value
    if isinstance(value, float) and value >= 0 and value.is_integer():
        return int(value)
    return None


def _mcp_retrieval_first_count(value, keys):
    for prefix, view in _mcp_retrieval_views(value):
        for key in keys:
            if key not in view:
                continue
            count = _mcp_retrieval_nonnegative_int(view.get(key))
            if count is not None:
                return count, prefix + key
    return None, None


def _host_mcp_retrieval_summary(value):
    """Describe one retrieval window without mutating provider-owned data."""
    if not isinstance(value, dict):
        return None
    returned, returned_key = _mcp_retrieval_first_count(
        value, _MCP_RETRIEVAL_RETURNED_KEYS
    )
    record_key = None
    if returned is None:
        for prefix, view in _mcp_retrieval_views(value):
            for key in _MCP_RETRIEVAL_RECORD_KEYS:
                records = view.get(key)
                if isinstance(records, list):
                    returned, returned_key, record_key = len(records), prefix + key, prefix + key
                    break
            if returned is not None:
                break
    if returned is None:
        count = _mcp_retrieval_nonnegative_int(value.get("count"))
        if count is not None:
            returned, returned_key = count, "count"
    total, total_key = _mcp_retrieval_first_count(
        value, _MCP_RETRIEVAL_TOTAL_KEYS
    )
    next_value, next_key = None, None
    for prefix, view in _mcp_retrieval_views(value):
        for key in _MCP_RETRIEVAL_NEXT_KEYS:
            candidate = view.get(key)
            if candidate is not None and candidate != "":
                next_value, next_key = candidate, key
                break
        if next_key is not None:
            break
    explicit_has_more = None
    explicit_truncated = None
    for _, view in _mcp_retrieval_views(value):
        if explicit_has_more is None:
            candidate = view.get("has_more")
            if not isinstance(candidate, bool):
                candidate = view.get("hasMore")
            if isinstance(candidate, bool):
                explicit_has_more = candidate
        if explicit_truncated is None and isinstance(view.get("truncated"), bool):
            explicit_truncated = view.get("truncated")
    if returned is None and total is None and next_key is None and \
            explicit_has_more is None and explicit_truncated is None:
        return None
    has_more = explicit_has_more
    if has_more is None and explicit_truncated is not None:
        has_more = explicit_truncated
    if has_more is None and next_key is not None:
        has_more = True
    if has_more is None and returned is not None and total is not None:
        has_more = returned < total
    summary = {}
    if returned is not None:
        summary["returned"] = returned
        summary["returned_field"] = returned_key
    if total is not None:
        summary["total"] = total
        summary["total_field"] = total_key
    if record_key is not None:
        summary["records_field"] = record_key
    if explicit_truncated is not None:
        summary["truncated"] = explicit_truncated
    if has_more is not None:
        summary["has_more"] = has_more
        summary["complete"] = not has_more
    if next_key is not None:
        summary[next_key] = next_value
    if returned == 0 and total is not None and total > 0:
        summary["empty_window_only"] = True
    return summary


_MCP_DISCOVERY_METHOD_MARKERS = (
    "search", "find", "list", "query", "browse", "citations", "references",
)
_MCP_STRUCTURED_RECORD_METHOD_MARKERS = (
    "get", "fetch", "lookup", "details", "metadata", "record", "resolve",
)
_MCP_DERIVED_METHOD_MARKERS = (
    "analyze", "analyse", "aggregate", "compare", "summarize", "summarise",
)
_MCP_FULL_TEXT_KEYS = (
    "full_text", "fulltext", "fullText", "article_text", "document_text",
)


def _mcp_contains_substantive_text(value, keys, depth=0, visited=None):
    """Find an actual text body without treating a title or abstract as full text."""
    if depth > 8:
        return False
    if visited is None:
        visited = set()
    if isinstance(value, (dict, list)):
        identity = id(value)
        if identity in visited or len(visited) >= 512:
            return False
        visited.add(identity)
    if isinstance(value, dict):
        for key, item in value.items():
            if key in keys and isinstance(item, str) and len(item.strip()) >= 400:
                return True
            if _mcp_contains_substantive_text(item, keys, depth + 1, visited):
                return True
    elif isinstance(value, list):
        return any(
            _mcp_contains_substantive_text(item, keys, depth + 1, visited)
            for item in value
        )
    return False


def _mcp_explicit_evidence_depth(value, depth=0, visited=None):
    """Honor a connector's explicit evidence receipt before method-name inference."""
    if depth > 8:
        return None
    if visited is None:
        visited = set()
    if isinstance(value, (dict, list)):
        identity = id(value)
        if identity in visited or len(visited) >= 512:
            return None
        visited.add(identity)
    if isinstance(value, dict):
        for key in (
            "evidenceDepth", "evidence_depth", "recordDepth", "record_depth",
            "evidenceState", "evidence_state",
        ):
            explicit = value.get(key)
            if isinstance(explicit, str) and explicit.strip():
                return explicit.strip()
        for item in value.values():
            explicit = _mcp_explicit_evidence_depth(item, depth + 1, visited)
            if explicit:
                return explicit
    elif isinstance(value, list):
        for item in value:
            explicit = _mcp_explicit_evidence_depth(item, depth + 1, visited)
            if explicit:
                return explicit
    return None


def _host_mcp_evidence_summary(method, value):
    """Classify retrieval depth without changing the connector-owned payload.

    The summary is execution metadata on ``_MCPPortableDict``.  It prevents a
    discovery page from silently becoming claim-level evidence while keeping
    every MCP server free to preserve its native result contract.
    """
    normalized = "".join(
        character.lower() if character.isalnum() else "_"
        for character in (method or "")
    )
    method_tokens = tuple(token for token in normalized.split("_") if token)
    explicit = _mcp_explicit_evidence_depth(value)
    explicit_lower = explicit.lower().replace("-", "_") if explicit else ""
    full_text = _mcp_contains_substantive_text(value, _MCP_FULL_TEXT_KEYS)

    if full_text or "full_text" in explicit_lower or "fulltext" in explicit_lower:
        stage = "full_text_read"
        claim_scope = "full_text_sections_read"
        next_action = (
            "Screen the relevant methods, results, limitations, tables, or passages "
            "before synthesis; preserve exact identifiers and field paths."
        )
    elif any(marker in method_tokens for marker in _MCP_DISCOVERY_METHOD_MARKERS):
        stage = "discovery"
        claim_scope = "candidate_identification_only"
        next_action = (
            "Use returned stable identifiers with a connector detail, record, or "
            "full-text method before making claim-level conclusions."
        )
    elif any(marker in method_tokens for marker in _MCP_DERIVED_METHOD_MARKERS):
        stage = "derived_summary"
        claim_scope = "aggregate_or_derived_fields_only"
        next_action = (
            "Trace decision-relevant aggregates to their underlying structured "
            "records before causal, comparative, or absence claims."
        )
    elif explicit or any(
        marker in method_tokens for marker in _MCP_STRUCTURED_RECORD_METHOD_MARKERS
    ):
        stage = "structured_record_read"
        claim_scope = "returned_structured_fields"
        next_action = (
            "Screen the returned structured fields; obtain full text or the source "
            "document for central methods, results, limitations, or legal-scope claims."
        )
    else:
        stage = "unclassified"
        claim_scope = "returned_fields_only"
        next_action = (
            "Inspect the connector schema and result before deciding whether a "
            "source-specific detail or full-text read is required."
        )
    return {
        "stage": stage,
        "claim_scope": claim_scope,
        "explicit_depth": explicit,
        "next_action": next_action,
    }


def _mcp_retrieval_records(value):
    if not isinstance(value, dict):
        return None, None
    candidates = []
    seen = set()
    for prefix, view in _mcp_retrieval_views(value):
        for key in _MCP_RETRIEVAL_RECORD_KEYS:
            records = view.get(key)
            if not isinstance(records, list) or id(records) in seen:
                continue
            seen.add(id(records))
            candidates.append((prefix + key, records))
    if len(candidates) == 1:
        return candidates[0]
    return None, None


def _mcp_retrieval_identity(record):
    if isinstance(record, dict):
        for key in _MCP_RETRIEVAL_ID_KEYS:
            value = record.get(key)
            if isinstance(value, (str, int)) and not isinstance(value, bool):
                normalized = str(value).strip().lower()
                if normalized:
                    return key.lower() + ":" + normalized.rstrip("/")
    if isinstance(record, (str, int, float)) and not isinstance(record, bool):
        return type(record).__name__ + ":" + str(record).strip().lower()
    try:
        return "json:" + _host_json_dumps(
            record, ensure_ascii=False, sort_keys=True, allow_nan=False,
            separators=(",", ":"),
        )
    except (TypeError, ValueError):
        return "repr:" + repr(record)


def _mcp_method_parameters(server, method):
    for candidate in _host_mcp_list_methods(server):
        if candidate.get("name") != method:
            continue
        parameters = candidate.get("parameters")
        if not isinstance(parameters, list):
            return {}
        result = {}
        for parameter in parameters:
            if not isinstance(parameter, dict):
                continue
            name = parameter.get("name")
            if isinstance(name, str) and name:
                result[name] = parameter
        return result
    raise ValueError(
        f"host.mcp: method {method!r} is absent from connector {server!r}"
    )


def _mcp_next_input(summary, parameters, current_input):
    for output_key, input_candidates in _MCP_RETRIEVAL_NEXT_INPUT_KEYS.items():
        if output_key not in summary:
            continue
        for input_key in input_candidates:
            if input_key in parameters:
                return input_key, summary[output_key]
        return None, None

    if summary.get("has_more") is not True:
        return None, None
    returned = _mcp_retrieval_nonnegative_int(summary.get("returned"))
    if returned is None or returned == 0:
        return None, None
    for input_key in ("offset", "retstart", "start", "skip"):
        if input_key not in parameters:
            continue
        current = _mcp_retrieval_nonnegative_int(current_input.get(input_key)) or 0
        return input_key, current + returned
    for input_key in ("page", "page_number", "pageNumber"):
        if input_key not in parameters:
            continue
        current = _mcp_retrieval_nonnegative_int(current_input.get(input_key)) or 1
        return input_key, current + 1
    return None, None


def _mcp_collection_bounds(max_records, max_pages):
    if isinstance(max_records, bool) or not isinstance(max_records, int) or not 1 <= max_records <= 5000:
        raise ValueError("host.mcp.collect: max_records must be an int from 1 to 5000")
    if isinstance(max_pages, bool) or not isinstance(max_pages, int) or not 1 <= max_pages <= 100:
        raise ValueError("host.mcp.collect: max_pages must be an int from 1 to 100")


def _host_mcp_collect(server, method, input=None, *, max_records=500,
                      max_pages=20, **kwargs):
    """Walk one live MCP method's declared pagination into one record set."""
    _mcp_collection_bounds(max_records, max_pages)
    method, current_input = _host_mcp_normalize_input(
        method, (() if input is None else (input,)), kwargs
    )
    parameters = _mcp_method_parameters(server, method)
    records = []
    seen_records = set()
    seen_page_tokens = set()
    receipts = []
    provider_total = None
    stop_reason = "provider_exhausted"

    for page_index in range(max_pages):
        page = _host_mcp(server, method, current_input)
        if not isinstance(page, _MCPPortableDict):
            raise TypeError(
                "host.mcp.collect requires a mapping result with retrieval metadata"
            )
        summary = page.retrieval or {}
        record_key, page_records = _mcp_retrieval_records(page)
        if page_records is None:
            raise ValueError(
                "host.mcp.collect could not identify one unambiguous record list; "
                "use host.mcp.call and inspect the provider result directly"
            )
        added = 0
        for record in page_records:
            identity = _mcp_retrieval_identity(record)
            if identity in seen_records:
                continue
            seen_records.add(identity)
            records.append(record)
            added += 1
            if len(records) >= max_records:
                stop_reason = "max_records_reached"
                break
        page_total = _mcp_retrieval_nonnegative_int(summary.get("total"))
        if page_total is not None:
            provider_total = max(provider_total or 0, page_total)
        receipts.append({
            "page": page_index + 1,
            "records_field": record_key,
            "returned": len(page_records),
            "added": added,
            "coverage": dict(summary),
        })
        if len(records) >= max_records:
            break
        if summary.get("complete") is True or summary.get("has_more") is False:
            break
        next_key, next_value = _mcp_next_input(summary, parameters, current_input)
        if next_key is None:
            stop_reason = "pagination_not_declared"
            break
        token = (next_key, repr(next_value))
        if token in seen_page_tokens:
            stop_reason = "pagination_repeated"
            break
        seen_page_tokens.add(token)
        current_input = dict(current_input)
        current_input[next_key] = next_value
    else:
        stop_reason = "max_pages_reached"

    complete = stop_reason == "provider_exhausted"
    if provider_total is not None and len(records) < provider_total:
        complete = False
    retrieval = {
        "scope": "mcp_paginated_collection",
        "returned": len(records),
        "provider_total": provider_total,
        "pages": len(receipts),
        "duplicates_removed": sum(item["returned"] for item in receipts) - len(records),
        "complete": complete,
        "has_more": not complete,
        "truncated": not complete,
        "stop_reason": stop_reason,
    }
    result = _MCPPortableDict({
        "records": records,
        "page_receipts": receipts,
        "retrieval": retrieval,
    })
    result.retrieval = retrieval
    result.evidence = {
        "stage": "discovery",
        "claim_scope": "candidate_identification_only",
        "next_action": (
            "Use selected stable identifiers with a connector detail, record, or "
            "full-text method before making claim-level conclusions."
        ),
    }
    return result


def _host_mcp_search(server, method, query, *, query_variants=None,
                     input=None, query_field=None, max_records=500,
                     max_pages=20):
    """Run multilingual query variants and merge paginated MCP records."""
    _mcp_collection_bounds(max_records, max_pages)
    if not isinstance(query, str) or not query.strip():
        raise TypeError("host.mcp.search: query must be a non-empty str")
    if query_variants is None:
        query_variants = []
    if not isinstance(query_variants, (list, tuple)) or any(
        not isinstance(item, str) for item in query_variants
    ):
        raise TypeError("host.mcp.search: query_variants must be a list of strings")
    base_input = {} if input is None else input
    if not isinstance(base_input, dict):
        raise TypeError("host.mcp.search: input must be a mapping or None")
    parameters = _mcp_method_parameters(server, method)
    if query_field is None:
        matches = [key for key in _MCP_RETRIEVAL_QUERY_KEYS if key in parameters]
        if len(matches) != 1:
            raise ValueError(
                "host.mcp.search requires one unambiguous query parameter; "
                f"declared parameters are {sorted(parameters)!r}"
            )
        query_field = matches[0]
    if not isinstance(query_field, str) or query_field not in parameters:
        raise ValueError("host.mcp.search: query_field must name a declared input parameter")

    queries = []
    seen_queries = set()
    for candidate in [query, *query_variants]:
        candidate = candidate.strip()
        normalized = candidate.lower()
        if not candidate or normalized in seen_queries:
            continue
        seen_queries.add(normalized)
        queries.append(candidate)
        if len(queries) == 8:
            break

    collections = []
    for candidate in queries:
        candidate_input = dict(base_input)
        candidate_input[query_field] = candidate
        collections.append((candidate, _host_mcp_collect(
            server, method, candidate_input,
            max_records=max_records, max_pages=max_pages,
        )))

    records = []
    seen_records = set()
    rank = 0
    while len(records) < max_records:
        progressed = False
        for _, collection in collections:
            source_records = collection["records"]
            if rank >= len(source_records):
                continue
            progressed = True
            record = source_records[rank]
            identity = _mcp_retrieval_identity(record)
            if identity in seen_records:
                continue
            seen_records.add(identity)
            records.append(record)
            if len(records) >= max_records:
                break
        if not progressed:
            break
        rank += 1

    candidates = sum(len(collection["records"]) for _, collection in collections)
    unique_candidate_ids = {
        _mcp_retrieval_identity(record)
        for _, collection in collections for record in collection["records"]
    }
    unique_candidates = len(unique_candidate_ids)
    upstream_complete = all(
        collection["retrieval"].get("complete") is True
        for _, collection in collections
    )
    overflow = max(0, unique_candidates - len(records))
    complete = upstream_complete and overflow == 0
    retrieval = {
        "scope": "mcp_multi_query_collection",
        "query_variants": len(queries),
        "candidates": candidates,
        "unique_candidates": unique_candidates,
        "returned": len(records),
        "duplicates_removed": max(0, candidates - unique_candidates),
        "overflow": overflow,
        "complete": complete,
        "has_more": not complete,
        "truncated": not complete,
        "selection_mode": "query_interleave_stable_identifier_deduplicate",
        "stop_reason": (
            "provider_exhausted" if complete else
            "max_records_reached" if overflow else "upstream_incomplete"
        ),
    }
    result = _MCPPortableDict({
        "records": records,
        "query_receipts": [
            {"query": candidate, "retrieval": collection["retrieval"],
             "page_receipts": collection["page_receipts"]}
            for candidate, collection in collections
        ],
        "retrieval": retrieval,
    })
    result.retrieval = retrieval
    result.evidence = {
        "stage": "discovery",
        "claim_scope": "candidate_identification_only",
        "next_action": (
            "Use selected stable identifiers with a connector detail, record, or "
            "full-text method before making claim-level conclusions."
        ),
    }
    return result


def _host_mcp_portable_result(value):
    """Add bounded Python-only aliases without changing audited source bytes."""
    if isinstance(value, list):
        for index, item in enumerate(value):
            value[index] = _host_mcp_portable_result(item)
        return value
    if not isinstance(value, dict):
        return value
    if not isinstance(value, _MCPPortableDict):
        value = _MCPPortableDict(value)
    for key, item in list(value.items()):
        value[key] = _host_mcp_portable_result(item)
    if "gene_symbol" in value and "gene" not in value:
        value["gene"] = value["gene_symbol"]
    components = value.get("components")
    if isinstance(components, list):
        symbols = []
        for component in components:
            if not isinstance(component, dict):
                continue
            symbol = component.get("gene_symbol")
            if isinstance(symbol, str) and symbol and symbol not in symbols:
                symbols.append(symbol)
        if symbols and "gene_symbol" not in value:
            value["gene_symbol"] = symbols[0] if len(symbols) == 1 else symbols
        if "gene_symbol" in value and "gene" not in value:
            value["gene"] = value["gene_symbol"]
    return value

def _host_mcp(server=None, method=None, *positional_input, **kwargs):
    if server is None and method is None:
        if positional_input or kwargs:
            raise TypeError("host.mcp: catalog discovery takes no arguments")
        cell_id = _active_host_cell["id"]
        if _host_mcp_catalog_cache["cell_id"] != cell_id:
            _host_mcp_catalog_cache["cell_id"] = cell_id
            _host_mcp_catalog_cache["value"] = _host_rpc("mcp.catalog", [], {})
            _host_mcp_catalog_cache["methods"] = {}
        return _host_mcp_catalog_cache["value"]
    if not isinstance(server, str) or not server:
        raise TypeError(
            "host.mcp: server must be a non-empty str, got "
            f"{type(server).__name__}"
        )
    # Keep the documented attribute API canonical while accepting the
    # provider-common method spelling as the same catalog operation. This is
    # discovery, never an MCP server invocation, and therefore cannot collide
    # with an advertised executable method.
    if method == "list_methods":
        if positional_input or kwargs:
            raise TypeError(
                "host.mcp: list_methods discovery does not accept tool input"
            )
        return _host_mcp_list_methods(server)
    # Provider tool snapshots expose direct methods as
    # mcp__<server>__<method>. Accept that exact advertised identity at the
    # Python boundary when a provider copies it into host.mcp. The Go host
    # remains the single authority for connector admission and input schema.
    if server.startswith("mcp__") and isinstance(method, dict):
        direct_parts = server[len("mcp__"):].split("__", 1)
        if len(direct_parts) != 2 or not all(direct_parts):
            raise TypeError(
                "host.mcp: direct MCP tool name must be mcp__<server>__<method>"
            )
        if positional_input or kwargs:
            raise TypeError(
                "host.mcp: direct MCP tool call cannot combine input shapes"
            )
        server, direct_method = direct_parts
        method, positional_input = direct_method, (method,)
    method, input_kwargs = _host_mcp_normalize_input(
        method, positional_input, kwargs
    )
    try:
        probe = _host_json_dumps(
            {"server": server, "method": method, "input": input_kwargs},
            ensure_ascii=False,
            allow_nan=False,
            separators=(",", ":"),
        )
    except (TypeError, ValueError) as exc:
        raise ValueError(f"host.mcp: arguments must be JSON serializable: {exc}") from None
    if len(probe.encode("utf-8")) > 15 * 1024 * 1024:
        raise ValueError("host.mcp: serialized request exceeds 15 MiB")
    result = _host_rpc("mcp", [server, method, input_kwargs], {})
    if isinstance(result, str) and result[:1] in ("{", "["):
        try:
            result = _host_json_loads(result)
        except (TypeError, ValueError):
            pass
    result = _host_mcp_portable_result(result)
    if isinstance(result, _MCPPortableDict):
        result.retrieval = _host_mcp_retrieval_summary(result)
        result.evidence = _host_mcp_evidence_summary(method, result)
    return result

def _host_mcp_list():
    return _host_rpc("host.mcp.list", [], {})

def _host_mcp_list_servers():
    catalog = _host_mcp()
    advertised = catalog.get("servers", []) if isinstance(catalog, dict) else []
    if isinstance(advertised, list):
        servers = sorted({
            server for server in advertised
            if isinstance(server, str) and server
        })
        if servers:
            return servers
    servers = set()
    for tool in catalog.get("tools", []) if isinstance(catalog, dict) else []:
        if not isinstance(tool, dict):
            continue
        server = tool.get("server")
        if not isinstance(server, str) or not server:
            continue
        servers.add(server)
    return sorted(servers)

def _host_mcp_list_methods(server):
    if not isinstance(server, str) or not server:
        raise TypeError("host.mcp.list_methods: server must be a non-empty str")
    _host_mcp()
    cached = _host_mcp_catalog_cache["methods"].get(server)
    if cached is not None:
        return list(cached)
    offset = 0
    seen_offsets = set()
    tools = []
    resolved_server = None
    while offset not in seen_offsets:
        seen_offsets.add(offset)
        catalog = _host_rpc(
            "mcp.catalog", [],
            {"server": server, "offset": offset, "max_results": 256},
        )
        if not isinstance(catalog, dict):
            break
        identity = catalog.get("server_filter") or server
        if resolved_server is not None and identity != resolved_server:
            raise RuntimeError("MCP catalog identity changed while reading pages")
        resolved_server = identity
        page = catalog.get("tools", [])
        if isinstance(page, list):
            tools.extend(page)
        if not catalog.get("has_more"):
            break
        next_offset = catalog.get("next_offset")
        if not isinstance(next_offset, int) or isinstance(next_offset, bool) or next_offset <= offset:
            break
        offset = next_offset
    methods = []
    for tool in tools:
        if not isinstance(tool, dict) or tool.get("server") != resolved_server:
            continue
        parameters = _MCPParameterList(tool.get("parameters") or [])
        methods.append({
            "server": resolved_server,
            "name": tool.get("method"),
            "description": tool.get("description") or "",
            "parameters": parameters,
            "input_schema": parameters.as_input_schema(),
            "output_schema": tool.get("output_schema") or {},
            "read_only": tool.get("read_only") is True,
        })
    methods = sorted(methods, key=lambda item: str(item.get("name") or ""))
    _host_mcp_catalog_cache["methods"][server] = tuple(methods)
    return list(methods)

def _host_mcp_install(config=None, **kwargs):
    if config is None:
        config = dict(kwargs)
    elif kwargs:
        raise TypeError("host.mcp.install: use one config dict or keyword fields, not both")
    if not isinstance(config, dict):
        raise TypeError("host.mcp.install: config must be a dict")
    allowed = {
        "connector_id", "name", "description", "transport", "url", "command", "args", "headers", "env"
    }
    unknown = sorted(set(config) - allowed)
    if unknown:
        raise TypeError(f"host.mcp.install: unsupported fields {unknown!r}")
    if "args" in config and not isinstance(config["args"], (list, tuple)):
        raise TypeError("host.mcp.install: args must be a list or tuple")
    return _host_rpc("host.mcp.install", [dict(config)], {})

def _host_mcp_authorize(connector_id):
    if not isinstance(connector_id, str) or not connector_id.strip():
        raise TypeError("host.mcp.authorize: connector_id must be a non-empty str")
    return _host_rpc("host.mcp.authorize", [connector_id], {})

def _host_mcp_remove(connector_id):
    if not isinstance(connector_id, str) or not connector_id.strip():
        raise TypeError("host.mcp.remove: connector_id must be a non-empty str")
    return _host_rpc("host.mcp.remove", [connector_id], {})

_host_mcp.list = _host_mcp_list
_host_mcp.call = _host_mcp
_host_mcp.collect = _host_mcp_collect
_host_mcp.search = _host_mcp_search
_host_mcp.list_servers = _host_mcp_list_servers
_host_mcp.list_methods = _host_mcp_list_methods
_host_mcp.install = _host_mcp_install
_host_mcp.authorize = _host_mcp_authorize
_host_mcp.remove = _host_mcp_remove

def _host_read_file(version_id, encoding="utf-8", max_bytes=16 * 1024 * 1024):
    if not isinstance(version_id, str) or not version_id:
        raise TypeError("read_file: version_id must be a non-empty str")
    if not isinstance(max_bytes, int) or isinstance(max_bytes, bool) or max_bytes < 1 or max_bytes > 16 * 1024 * 1024:
        raise ValueError("read_file: max_bytes must be between 1 and 16777216")
    path = _host_artifact_path(version_id)
    size = os.path.getsize(path)
    if size > max_bytes:
        raise ValueError(f"read_file: artifact is {size} bytes, exceeding max_bytes={max_bytes}")
    with open(path, "r", encoding=encoding) as handle:
        return handle.read(max_bytes + 1)


class _AppProxy:
    def __init__(self, server):
        self._server = server

    def __repr__(self):
        return f"<host.app({self._server!r}) - .tools() for live handlers>"

    def tools(self):
        return _host_rpc(
            "host.app_tools_list", [{"server": self._server}], {}
        )

    def __getattr__(self, name):
        if name.startswith("_"):
            raise AttributeError(name)
        server = self._server

        def call(artifact_id=None, **kwargs):
            if artifact_id is not None and (
                not isinstance(artifact_id, str) or not artifact_id
            ):
                raise TypeError(
                    f"host.app({server!r}).{name}: artifact_id must be "
                    f"a non-empty str or None, got {artifact_id!r}"
                )
            try:
                return _host_rpc(
                    "host.app_tool",
                    [{
                        "server": server,
                        "tool": name,
                        "artifact_id": artifact_id,
                        "args": kwargs,
                    }],
                    {},
                )
            except RuntimeError as error:
                message = str(error)
                marker = f"host.app_tool: {name}: "
                if message.startswith(marker + "unexpected argument"):
                    raise TypeError(
                        f"host.app({server!r}).{message[len(marker):]}"
                    ) from None
                if message.startswith(marker + "unknown tool"):
                    raise AttributeError(
                        f"host.app({server!r}).{message[len(marker):]}"
                    ) from None
                raise

        call.__name__ = name
        call.__qualname__ = f"host.app({server!r}).{name}"
        return call


def _host_app(server):
    if not isinstance(server, str) or not server:
        raise TypeError(
            f"host.app: server must be a non-empty str, got {server!r}"
        )
    return _AppProxy(server)

_llm_fields = {
    "prompt", "messages", "images", "model", "system", "max_tokens",
    "tools", "tool_choice", "temperature",
}

def _llm_encode_pil(req):
    import base64 as _llm_base64

    encoded = dict(req)
    image_count = 0
    image_bytes = 0

    def encode_content(content):
        nonlocal image_count, image_bytes
        if isinstance(content, str):
            return content
        if not isinstance(content, (list, tuple)):
            raise TypeError("messages[].content must be a str or block list")
        blocks = []
        for block in content:
            if not isinstance(block, dict):
                raise TypeError("messages[].content[] blocks must be dicts")
            item = dict(block)
            kind = item.get("type")
            if kind == "text":
                if not isinstance(item.get("text"), str) or not item["text"]:
                    raise TypeError("text blocks require a non-empty text str")
            elif kind == "image":
                image_count += 1
                if image_count > 20:
                    raise ValueError("max 20 images per request")
                if "pil" in item:
                    image = item.pop("pil")
                    try:
                        from PIL import Image as _PILImage
                    except Exception as exc:
                        raise TypeError("PIL is required to encode image blocks") from exc
                    if not isinstance(image, _PILImage.Image):
                        raise TypeError("image block pil must be a PIL.Image.Image")
                    buffer = io.BytesIO()
                    try:
                        image.save(buffer, format="PNG")
                    except Exception as exc:
                        raise TypeError(f"failed to encode PIL image: {exc}") from exc
                    data = buffer.getvalue()
                    item["source"] = {
                        "type": "base64", "media_type": "image/png",
                        "data": _llm_base64.b64encode(data).decode("ascii"),
                    }
                source = item.get("source")
                if not isinstance(source, dict) or source.get("type") != "base64":
                    raise TypeError("image blocks require pil or a base64 source")
                media_type = source.get("media_type")
                data = source.get("data")
                if media_type not in (
                    "image/png", "image/jpeg", "image/gif", "image/webp"
                ) or not isinstance(data, str):
                    raise TypeError("image block has invalid media_type or base64 data")
                try:
                    decoded = _llm_base64.b64decode(data, validate=True)
                except Exception as exc:
                    raise TypeError("image block data is not valid base64") from exc
                if not decoded or len(decoded) > 32 * 1024 * 1024:
                    raise ValueError("image block exceeds the 32 MiB per-image limit")
                image_bytes += len(decoded)
                if image_bytes > 32 * 1024 * 1024:
                    raise ValueError("image blocks exceed the 32 MiB request limit")
            else:
                raise TypeError(f"unsupported content block type {kind!r}")
            blocks.append(item)
        return blocks

    messages = encoded.get("messages")
    if isinstance(messages, (list, tuple)):
        output = []
        for message in messages:
            if not isinstance(message, dict):
                raise TypeError("messages[] entries must be dicts")
            item = dict(message)
            item["content"] = encode_content(item.get("content"))
            output.append(item)
        encoded["messages"] = output
    return encoded

def _llm_guard(req, label="host.llm"):
    unknown = set(req) - _llm_fields
    if unknown:
        raise TypeError(
            f"{label}: unknown request field(s) {sorted(unknown)!r}; "
            f"valid fields are {sorted(_llm_fields)!r}"
        )
    if req.get("prompt") is not None and req.get("messages") is not None:
        raise TypeError(f"{label}: both 'prompt' and 'messages' given")
    model = req.get("model")
    if model is not None:
        if not isinstance(model, str) or not model.lstrip("\ufeff").strip():
            raise TypeError(f"{label}: model must be a non-empty str or None")
    max_tokens = req.get("max_tokens")
    if max_tokens is not None and (
        isinstance(max_tokens, bool) or not isinstance(max_tokens, int)
        or not 1 <= max_tokens <= 32768
    ):
        raise TypeError(
            f"{label}: max_tokens must be an int between 1 and 32768"
        )
    system = req.get("system")
    if system is not None and not isinstance(system, str):
        raise TypeError(f"{label}: system must be a str or None")
    if isinstance(system, str) and len(system) > 65536:
        raise ValueError(f"{label}: system exceeds 65536 chars")
    messages = req.get("messages")
    if messages is not None:
        if not isinstance(messages, (list, tuple)):
            raise TypeError(f"{label}: messages must be a list")
        for item in messages:
            if not isinstance(item, dict):
                raise TypeError(
                    f"{label}: messages[] entries must be {{role, content}} objects"
                )
            role = item.get("role")
            if role not in ("user", "assistant"):
                raise TypeError(
                    f'{label}: messages[].role must be "user" or "assistant"; '
                    "use the top-level system field instead"
                )
    images = req.get("images")
    if images is not None:
        if not isinstance(images, (list, tuple)) or len(images) > 20:
            raise TypeError(f"{label}: images must be a list of at most 20 paths")
        if any(not isinstance(path, str) or not path for path in images):
            raise TypeError(f"{label}: images[] entries must be non-empty strings")

def _llm_single_request(request, system, model, max_tokens, options):
    if isinstance(request, dict):
        if system is not None or model is not None or max_tokens is not None or options:
            raise TypeError(
                "host.llm: request dict cannot be mixed with top-level options"
            )
        req = dict(request)
    elif isinstance(request, str):
        req = {"prompt": request, **options}
        if system is not None:
            req["system"] = system
        if model is not None:
            req["model"] = model
        if max_tokens is not None:
            req["max_tokens"] = max_tokens
    elif request is None:
        req = dict(options)
        if system is not None:
            req["system"] = system
        if model is not None:
            req["model"] = model
        if max_tokens is not None:
            req["max_tokens"] = max_tokens
    else:
        raise TypeError(
            "host.llm: expected a prompt str, a request dict, or a list "
            f"of request dicts; got {type(request).__name__}"
        )
    _llm_guard(req)
    if req.get("messages") is None:
        prompt = req.get("prompt")
        if not isinstance(prompt, str) or not prompt.strip():
            raise ValueError("host.llm: prompt must be a non-empty str")
    return req

def _host_llm(
    request=None, system=None, model=None, max_tokens=None,
    max_concurrency=None, **options
):
    if isinstance(request, (list, tuple)):
        if system is not None or model is not None or max_tokens is not None or options:
            raise TypeError(
                "host.llm: batch requests must put options in each request"
            )
        if max_concurrency is not None and (
            isinstance(max_concurrency, bool)
            or not isinstance(max_concurrency, int)
            or max_concurrency < 1
        ):
            raise TypeError(
                "host.llm: max_concurrency must be a positive int or None"
            )
        if len(request) > 512:
            raise ValueError("host.llm: max 512 requests per batch")
        batch = []
        batch_indexes = []
        local_results = [None] * len(request)
        for index, item in enumerate(request):
            req = {"prompt": item} if isinstance(item, str) else dict(item) if isinstance(item, dict) else None
            if req is None:
                raise TypeError(
                    f"host.llm: batch request {index} must be a str or dict"
                )
            if req.get("system") is None:
                req.pop("system", None)
            _llm_guard(req, f"host.llm batch request {index}")
            if req.get("messages") is None:
                prompt = req.get("prompt")
                if not isinstance(prompt, str) or not prompt.strip():
                    raise ValueError(
                        f"host.llm batch request {index}: prompt must be non-empty"
                    )
            try:
                batch.append(_llm_encode_pil(req))
                batch_indexes.append(index)
            except Exception as exc:
                local_results[index] = {"error": str(exc)}
        if not batch:
            return local_results
        remote_results = _host_rpc(
            "host.llm_batch", [batch, max_concurrency], {}
        )
        for index, result in zip(batch_indexes, remote_results):
            local_results[index] = result
        return local_results
    if max_concurrency is not None:
        raise TypeError("host.llm: max_concurrency is only valid for batches")
    req = _llm_single_request(request, system, model, max_tokens, options)
    extended = ("tools", "tool_choice", "images", "messages", "temperature")
    if any(req.get(key) is not None for key in extended):
        result = _host_rpc("host.llm_batch", [[_llm_encode_pil(req)], 1], {})
        item = result[0] if isinstance(result, list) and result else {}
        if isinstance(item, dict) and "error" in item and "text" not in item:
            raise RuntimeError(f"host.llm: {item['error']}")
        return item
    return _host_rpc("host.llm", [req], {})

def _host_current_model():
    return _host_rpc("host.current_model", [], {})

def _host_list_models():
    return _host_rpc("host.list_models", [], {})

def _inspection_time(value, label):
    if value is None:
        return None
    if isinstance(value, str):
        if not value.strip():
            raise ValueError(f"{label} must not be empty")
        return value
    isoformat = getattr(value, "isoformat", None)
    if callable(isoformat):
        return isoformat()
    raise TypeError(f"{label} must be an ISO string, date, datetime, or None")

def _host_artifacts(
    frame_id=None, project_id=None, filename=None, exact=False,
    content=None, content_type=None, after=None, before=None,
    include_intermediate=False, limit=200, offset=0, search=None,
    version_id=None,
):
    for label, value in (
        ("frame_id", frame_id), ("project_id", project_id),
        ("filename", filename), ("content", content),
        ("content_type", content_type), ("search", search),
        ("version_id", version_id),
    ):
        if value is not None and not isinstance(value, str):
            raise TypeError(f"host.artifacts: {label} must be a str or None")
    if not isinstance(exact, bool):
        raise TypeError("host.artifacts: exact must be a bool")
    if not isinstance(include_intermediate, bool):
        raise TypeError("host.artifacts: include_intermediate must be a bool")
    if isinstance(limit, bool) or not isinstance(limit, int) or not 1 <= limit <= 1000:
        raise TypeError("host.artifacts: limit must be an int from 1 to 1000")
    if isinstance(offset, bool) or not isinstance(offset, int) or offset < 0:
        raise TypeError("host.artifacts: offset must be a non-negative int")
    if exact and not filename:
        raise ValueError("host.artifacts: exact=True requires filename")
    if search and (filename or content):
        raise ValueError("host.artifacts: search and filename/content are mutually exclusive")
    payload = {
			"version_id": version_id,
        "project_id": project_id, "frame_id": frame_id,
        "filename": filename, "exact": exact, "content": content,
        "content_type": content_type,
        "after": _inspection_time(after, "host.artifacts: after"),
        "before": _inspection_time(before, "host.artifacts: before"),
        "include_intermediate": include_intermediate,
        "limit": limit, "offset": offset, "search": search,
    }
    return _host_rpc("host.artifacts", [payload], {})

def _host_artifacts_rename(artifact_id, filename):
    if not isinstance(artifact_id, str) or not artifact_id.strip():
        raise TypeError(
            "host.artifacts.rename: artifact_id must be a non-empty str "
            "(the 'id' field from host.artifacts())"
        )
    if not isinstance(filename, str) or not filename.strip():
        raise TypeError("host.artifacts.rename: filename must be a non-empty str")
    return _host_rpc(
        "host.artifacts.rename", [artifact_id, filename], {}
    )

def _host_artifacts_delete(artifact_ids, reason=None):
    if isinstance(artifact_ids, str):
        values = [artifact_ids]
    elif isinstance(artifact_ids, (list, tuple)):
        values = list(artifact_ids)
    else:
        raise TypeError(
            "host.artifacts.delete: artifact_ids must be a str or non-empty list/tuple"
        )
    if not values:
        raise TypeError(
            "host.artifacts.delete: artifact_ids must be a str or non-empty list/tuple"
        )
    unique = []
    seen = set()
    for value in values:
        if not isinstance(value, str) or not value.strip():
            raise TypeError(
                "host.artifacts.delete: every artifact id must be a non-empty str"
            )
        if value not in seen:
            seen.add(value)
            unique.append(value)
    if len(unique) > 200:
        raise ValueError(
            "host.artifacts.delete: max 200 unique artifact ids per call"
        )
    if reason is not None and not isinstance(reason, str):
        raise TypeError("host.artifacts.delete: reason must be a str or None")
    return _host_rpc(
        "host.artifacts.delete", [unique, reason if reason and reason.strip() else None], {}
    )

_host_artifacts.rename = _host_artifacts_rename
_host_artifacts.delete = _host_artifacts_delete

def _host_artifact_path(version_id):
    if not isinstance(version_id, str) or not version_id.strip():
        raise TypeError("host.artifact_path: version_id must be a non-empty str")
    return _host_rpc("host.artifact_path", [version_id], {})

def _host_artifact_marker(version_id):
    if not isinstance(version_id, str) or not version_id.strip():
        raise TypeError("host.artifact_marker: version_id must be a non-empty str")
    return "{{artifact:" + version_id.strip().lower() + "}}"

class _LineageAccessor:
    def __init__(self):
        self._cache = {}

    def __getitem__(self, version_id):
        if not isinstance(version_id, str) or not version_id.strip():
            raise TypeError("host.lineage[...]: version_id must be a non-empty str")
        version_id = version_id.strip().lower()
        cached = self._cache.get(version_id)
        if cached is not None:
            return cached
        result = _host_rpc("host.lineage", [version_id], {})
        if not (isinstance(result, dict) and result.get("extraction_pending")):
            self._cache[version_id] = result
        return result

    def __contains__(self, version_id):
        return version_id in self._cache

    def graph(self, version_id, direction="up", max_depth=None, max_nodes=None):
        if not isinstance(version_id, str) or not version_id.strip():
            raise TypeError("host.lineage.graph: version_id must be a non-empty str")
        if direction not in ("up", "down"):
            raise ValueError("host.lineage.graph: direction must be 'up' or 'down'")
        for label, value, maximum in (
            ("max_depth", max_depth, 256), ("max_nodes", max_nodes, 1000),
        ):
            if value is not None and (
                isinstance(value, bool) or not isinstance(value, int)
                or value < (0 if label == "max_depth" else 1) or value > maximum
            ):
                raise TypeError(f"host.lineage.graph: {label} is outside the supported range")
        return _host_rpc("host.lineage_graph", [{
            "version_id": version_id.strip().lower(),
            "direction": direction,
            "max_depth": max_depth,
            "max_nodes": max_nodes,
        }], {})

    def clear(self):
        self._cache.clear()

_lineage_accessor = _LineageAccessor()

def _host_frames(
    frame_id=None, pattern=None, project_id=None, status=None,
    roots_only=True, has_task=False, after=None, before=None,
    max_results=None, offset=0, include_tool_results=True,
):
    for label, value in (
        ("frame_id", frame_id), ("pattern", pattern),
        ("project_id", project_id), ("status", status),
    ):
        if value is not None and not isinstance(value, str):
            raise TypeError(f"host.frames: {label} must be a str or None")
    for label, value in (
        ("roots_only", roots_only), ("has_task", has_task),
        ("include_tool_results", include_tool_results),
    ):
        if not isinstance(value, bool):
            raise TypeError(f"host.frames: {label} must be a bool")
    if max_results is not None and (
        isinstance(max_results, bool) or not isinstance(max_results, int)
        or not 1 <= max_results <= 500
    ):
        raise TypeError("host.frames: max_results must be an int from 1 to 500 or None")
    if isinstance(offset, bool) or not isinstance(offset, int) or offset < 0:
        raise TypeError("host.frames: offset must be a non-negative int")
    return _host_rpc("host.frames", [{
        "frame_id": frame_id, "pattern": pattern, "project_id": project_id,
        "status": status, "roots_only": roots_only, "has_task": has_task,
        "after": _inspection_time(after, "host.frames: after"),
        "before": _inspection_time(before, "host.frames: before"),
        "max_results": max_results,
        "offset": offset, "include_tool_results": include_tool_results,
    }], {})

_delegate_fields = frozenset(
    ("task", "name", "context_summary", "profile", "output_schema", "model")
)

def _delegate_request(value, where):
    if isinstance(value, str):
        if not value.strip():
            raise ValueError(f"host.delegate: {where} task must be a non-empty str")
        return {"task": value}
    if not isinstance(value, dict):
        raise TypeError(
            f"host.delegate: {where} must be a task str or request dict, "
            f"got {type(value).__name__}"
        )
    unknown = set(value) - _delegate_fields
    if unknown:
        raise TypeError(
            f"host.delegate: {where} has unknown field(s) {sorted(unknown)!r}; "
            f"valid fields are {sorted(_delegate_fields)!r}"
        )
    task = value.get("task")
    if not isinstance(task, str) or not task.strip():
        raise TypeError(
            f"host.delegate: {where} 'task' must be a non-empty str, "
            f"got {type(task).__name__}"
        )
    schema = value.get("output_schema")
    if schema is not None and not isinstance(schema, dict):
        raise TypeError(
            f"host.delegate: {where} 'output_schema' must be a dict "
            "(JSON Schema) or None"
        )
    for field in ("name", "context_summary", "profile", "model"):
        item = value.get(field)
        if item is not None and not isinstance(item, str):
            raise TypeError(
                f"host.delegate: {where} {field!r} must be a str or None, "
                f"got {type(item).__name__}"
            )
    model_value = value.get("model")
    if model_value is not None and not model_value.lstrip("\ufeff").strip():
        raise ValueError(
            f"host.delegate: {where} 'model' must be a non-empty str or None"
        )
    return dict(value)

def _emit_child_frame_ids(results):
    try:
        slots = results if isinstance(results, list) else [results]
        for result in slots:
            if isinstance(result, dict) and isinstance(result.get("frame_id"), str):
                label = result.get("name") or result.get("agent_name") or ""
                suffix = f" {label}" if label else ""
                print(
                    f"[delegate] child{suffix} frame_id={result['frame_id']} "
                    "(steer: host.send_message/host.stop_child; retrieve: host.collect)",
                    flush=True,
                )
    except Exception:
        pass

def _host_delegate(
    request=None, task=None, name=None, context_summary=None,
    profile=None, output_schema=None, model=None, max_concurrency=None,
    timeout=None, wait=True
):
    """Spawn real child agent frame(s); input and return shape mirror each other."""
    if timeout is not None:
        if isinstance(timeout, bool) or not isinstance(timeout, (int, float)):
            raise TypeError(
                "host.delegate: timeout must be a positive number of seconds or None"
            )
        if not math.isfinite(timeout) or timeout <= 0:
            raise ValueError("host.delegate: timeout must be a positive number of seconds")
        if timeout > 86400:
            raise ValueError("host.delegate: timeout is capped at 86400s")
    if not isinstance(wait, bool):
        raise TypeError(f"host.delegate: wait must be a bool, got {type(wait).__name__}")
    if wait is False and timeout is not None:
        raise ValueError(
            "host.delegate: timeout= is meaningless with wait=False; use host.collect"
        )
    options = {}
    if timeout is not None:
        options["timeout"] = float(timeout)
    if wait is False:
        options["wait"] = False
    wire_options = [options] if options else []

    if isinstance(request, (list, tuple)):
        if any(value is not None for value in (
            task, name, context_summary, profile, output_schema, model
        )):
            raise TypeError(
                "host.delegate: list requests must put per-request options in each dict"
            )
        if len(request) > 48:
            raise ValueError("host.delegate: max 48 requests per call")
        requests = [
            _delegate_request(value, f"requests[{index}]")
            for index, value in enumerate(request)
        ]
        if not requests:
            return []
        results = _host_rpc("host.delegate", [requests] + wire_options, {})
        _emit_child_frame_ids(results)
        return results

    if isinstance(request, dict):
        if any(value is not None for value in (
            task, name, context_summary, profile, output_schema, model
        )):
            raise TypeError(
                "host.delegate: pass options inside the request dict OR as keyword arguments"
            )
        item = _delegate_request(request, "request")
    elif isinstance(request, str) or request is None:
        item = {}
        if isinstance(request, str):
            if task is not None:
                raise TypeError("host.delegate: task given positionally and by keyword")
            item["task"] = request
        elif task is not None:
            item["task"] = task
        for key, value in (
            ("name", name), ("context_summary", context_summary),
            ("profile", profile), ("output_schema", output_schema),
            ("model", model),
        ):
            if value is not None:
                item[key] = value
        item = _delegate_request(item, "request")
    else:
        raise TypeError(
            "host.delegate: expected a task str, request dict, or list; "
            f"got {type(request).__name__}"
        )
    results = _host_rpc("host.delegate", [[item]] + wire_options, {})
    if not isinstance(results, list) or not results:
        raise RuntimeError("host.delegate: host returned no result for single request")
    _emit_child_frame_ids(results)
    return results[0]

def _child_slots(value, label):
    def one(item):
        if isinstance(item, str) and item:
            return ("ok", item)
        if isinstance(item, dict):
            frame_id = item.get("frame_id") or item.get("child_frame_id")
            if isinstance(frame_id, str) and frame_id:
                return ("ok", frame_id)
            return ("bad", f"{label}: dict entry has no usable frame_id")
        return ("bad", f"{label}: each entry must be a frame-id str or descriptor dict")
    if isinstance(value, (list, tuple)):
        if not value:
            raise TypeError(f"{label}: list must be non-empty")
        return [one(item) for item in value], True
    return [one(value)], False

def _host_collect(frame_ids, timeout=30.0):
    slots, _ = _child_slots(frame_ids, "host.collect")
    if len(slots) > 1000:
        raise ValueError("host.collect: max 1000 frame ids per call")
    if timeout is None:
        raise ValueError("host.collect: timeout=None (unbounded) is not allowed")
    if isinstance(timeout, bool) or not isinstance(timeout, (int, float)):
        raise TypeError("host.collect: timeout must be a positive number of seconds")
    if timeout <= 0:
        raise ValueError("host.collect: timeout must be positive")
    if timeout > 1800:
        raise ValueError("host.collect: timeout is capped at 1800s")
    good = [value for tag, value in slots if tag == "ok"]
    remote = _host_rpc(
        "host.collect", [{"frame_ids": good, "timeout": float(timeout)}], {}
    ) if good else []
    iterator = iter(remote)
    output = []
    for tag, value in slots:
        output.append(next(iterator) if tag == "ok" else {
            "status": "failed", "error": value
        })
    return output

def _host_children():
    return _host_rpc("host.children", [], {})

def _host_delegation_stats():
    return _host_rpc("host.delegation_stats", [], {})

def _host_stop_child(child_frame_id, reason=None):
    if reason is not None and not isinstance(reason, str):
        raise TypeError("host.stop_child: reason must be a str or None")
    slots, was_list = _child_slots(child_frame_id, "host.stop_child")
    if len(slots) > 1000:
        raise ValueError("host.stop_child: max 1000 frame ids per call")
    good = [value for tag, value in slots if tag == "ok"]
    if not was_list:
        if not good:
            return {"status": "failed", "error": slots[0][1]}
        return _host_rpc("host.stop_child", [{
            "child_frame_id": good[0], "reason": reason
        }], {})
    remote = _host_rpc("host.stop_child", [{
        "child_frame_id": good, "reason": reason
    }], {}) if good else []
    iterator = iter(remote if isinstance(remote, list) else [])
    output = []
    for tag, value in slots:
        output.append(next(iterator, {"status": "failed", "error": "no result"})
                      if tag == "ok" else {"status": "failed", "error": value})
    return output

def _host_send_message(target=None, message=None, kind="info", *, child_frame_id=None):
    if target is None:
        target = child_frame_id
    if isinstance(target, (list, tuple)):
        raise TypeError("host.send_message: one target per call")
    if not isinstance(message, str) or not message.strip():
        raise TypeError("host.send_message: message must be a non-empty str")
    if kind not in ("info", "question"):
        raise ValueError("host.send_message: kind must be 'info' or 'question'")
    if target == "parent":
        frame_id = "parent"
    else:
        slots, _ = _child_slots(target, "host.send_message")
        if slots[0][0] == "bad":
            return {"status": "failed", "error": slots[0][1]}
        frame_id = slots[0][1]
    return _host_rpc("host.send_message", [{
        "target": frame_id, "message": message, "kind": kind
    }], {})

class CredentialUnavailable(RuntimeError):
    pass

class CredentialDeclined(CredentialUnavailable):
    pass

class ContactEmailUnavailable(RuntimeError):
    pass

class ContactEmailDeclined(ContactEmailUnavailable):
    pass

class _CredentialsAccessor:
    def list(self):
        return _host_rpc("host.credentials.list", [], {})

    def get(self, name):
        if not isinstance(name, str) or not name.strip():
            raise TypeError("host.credentials.get: name must be a non-empty str")
        try:
            return _host_rpc("host.credentials.get", [name], {})
        except RuntimeError as exc:
            raise CredentialUnavailable(str(exc)) from None

    def request(self, provider):
        if not isinstance(provider, str) or not provider.strip():
            raise TypeError("host.credentials.request: provider must be non-empty")
        try:
            return _host_rpc("host.credentials.request", [provider], {})
        except RuntimeError as exc:
            raise CredentialUnavailable(str(exc)) from None

    def __repr__(self):
        return "<host.credentials>"

class _QueryAccessor:
    def __call__(self, sql, params=None, limit=None, df=False, scope="project"):
        if not isinstance(sql, str) or not sql.strip():
            raise TypeError("host.query: sql must be a non-empty str")
        if params is None:
            params = []
        if not isinstance(params, (list, tuple)):
            raise TypeError("host.query: params must be a list or tuple")
        if limit is not None and (
            isinstance(limit, bool) or not isinstance(limit, int) or limit < 1
        ):
            raise TypeError("host.query: limit must be a positive int or None")
        if not isinstance(df, bool):
            raise TypeError("host.query: df must be a bool")
        if scope not in ("project", "global"):
            raise TypeError("host.query: scope must be project or global")
        return _host_rpc(
            "host.query", [sql, list(params), limit, df, scope], {}
        )

    def schema(self, scope="project"):
        if scope not in ("project", "global"):
            raise TypeError("host.query.schema: scope must be project or global")
        return _host_rpc("host.query.schema", [], {"scope": scope})

    def __repr__(self):
        return "<host.query>"

class _ModelEndpointsAccessor:
    def free_port(self):
        return _host_rpc("host.model_endpoints.free_port", [], {})["port"]

    def register(
        self, name, url, skill, start=None, stop=None, live=None,
        credential="NVIDIA_API_KEY",
    ):
        for label, value in (("name", name), ("url", url), ("skill", skill)):
            if not isinstance(value, str) or not value.strip():
                raise TypeError(
                    f"host.model_endpoints.register: {label} must be non-empty"
                )
        hosted = url.strip().lower().startswith("https://")
        if hosted:
            if any(value is not None for value in (start, stop, live)):
                raise TypeError(
                    "host.model_endpoints.register: remote endpoints take no "
                    "start, stop, or live values"
                )
        else:
            for label, value in (("start", start), ("stop", stop), ("live", live)):
                if not isinstance(value, str) or not value.strip():
                    raise TypeError(
                        f"host.model_endpoints.register: {label} is required "
                        "for a local endpoint"
                    )
        config = {"name": name, "url": url, "skill": skill}
        if not hosted:
            config.update({"start": start, "stop": stop, "live": live})
        if credential is not None:
            config["credential"] = credential
        return _host_rpc("host.model_endpoints.register", [config], {})

    def __repr__(self):
        return "<host.model_endpoints>"

def _host_capabilities():
    return _host_rpc("host.capabilities", [], {})

def _host_exec_peek(exec_id):
    if not isinstance(exec_id, str) or not exec_id.strip():
        raise TypeError("host.exec_peek: exec_id must be a non-empty str")
    return _host_rpc("host.exec_peek", [exec_id], {})

def _host_exec_interrupt(exec_id):
    if not isinstance(exec_id, str) or not exec_id.strip():
        raise TypeError("host.exec_interrupt: exec_id must be a non-empty str")
    return _host_rpc("host.exec_interrupt", [exec_id], {})

def _host_findings():
    return _host_rpc("host.findings", [], {})

def _host_findings_mark_addressed(ids, note):
    if isinstance(ids, str):
        ids = [ids]
    if not isinstance(ids, (list, tuple)) or not all(
        isinstance(item, str) and item.strip() for item in ids
    ):
        raise TypeError("host.findings.mark_addressed: ids must be strings")
    if not isinstance(note, str) or not note.strip():
        raise TypeError("host.findings.mark_addressed: note must be non-empty")
    return _host_rpc("host.findings.mark_addressed", [list(ids), note], {})

_host_findings.mark_addressed = _host_findings_mark_addressed

def _host_get_local_compute_stats(time_range=None, sample_frequency=None):
    return _host_rpc(
        "host.get_local_compute_stats", [],
        {"time_range": time_range, "sample_frequency": sample_frequency},
    )

def _host_get_user_email():
    try:
        return _host_rpc("host.get_user_email", [], {})
    except RuntimeError as exc:
        raise ContactEmailUnavailable(str(exc)) from None

def _host_reasoning_model():
    return _host_rpc("host.reasoning_model", [], {})

def _host_submit_output(output, completion_bullets=None):
    if not isinstance(output, dict):
        raise TypeError("host.submit_output: output must be a dict")
    if completion_bullets is None:
        completion_bullets = []
    if not isinstance(completion_bullets, (list, tuple)):
        raise TypeError("host.submit_output: completion_bullets must be a list")
    return _host_rpc(
        "host.submit_output", [dict(output), list(completion_bullets)], {}
    )

_credentials_accessor = _CredentialsAccessor()
_query_accessor = _QueryAccessor()
_model_endpoints_accessor = _ModelEndpointsAccessor()

class ComputeError(RuntimeError):
    kind = "provider_error"

    def __init__(self, message=None, *, kind=None, retryable=False,
                 next_step=None, **attributes):
        self.kind = kind or type(self).kind
        self.error_kind = self.kind
        self.retryable = bool(retryable)
        self.next_step = next_step
        for key, value in attributes.items():
            setattr(self, key, value)
        text = message or type(self).__name__
        if next_step and "next:" not in text:
            text += f"\n  next: {next_step}"
        super().__init__(text)

class ComputeNotFound(ComputeError):
    kind = "not_found"

class ComputeNotSupported(ComputeError):
    kind = "not_supported"

class ComputeInvalidResource(ComputeError):
    kind = "invalid_resource"

class ComputeConcurrencyFull(ComputeError):
    kind = "concurrency_full"

    def __init__(self, message=None, *, live=None, limit=None, **kwargs):
        kwargs.setdefault(
            "next_step",
            "end this cell and wait for a compute_done notification when this frame owns a live job",
        )
        super().__init__(message, live=live, limit=limit, **kwargs)

class ComputeBusy(ComputeError):
    kind = "busy"

class ComputeJobPending(ComputeError):
    kind = "job_pending"

    def __init__(self, message=None, **kwargs):
        kwargs.setdefault("retryable", True)
        kwargs.setdefault(
            "next_step",
            "end this cell and use wait_for_notification; do not poll job.result()",
        )
        super().__init__(message, **kwargs)

class ComputeJobTimedOut(ComputeError):
    kind = "job_timed_out"

class ComputeJobFailed(ComputeError):
    kind = "job_failed"

class ComputeProviderError(ComputeError):
    kind = "provider_error"

_COMPUTE_ERROR_CLASSES = {
    "not_found": ComputeNotFound,
    "job_not_found": ComputeNotFound,
    "not_supported": ComputeNotSupported,
    "invalid_arguments": ComputeInvalidResource,
    "invalid_argument": ComputeInvalidResource,
    "invalid_resource": ComputeInvalidResource,
    "session_concurrency_full": ComputeConcurrencyFull,
    "concurrency_full": ComputeConcurrencyFull,
    "provider_concurrency_full": ComputeBusy,
}

def _compute_error_from_host(method, detail):
    detail = detail if isinstance(detail, dict) else {}
    kind = detail.get("code") or "provider_error"
    message = detail.get("message") or "host compute call failed"
    cls = _COMPUTE_ERROR_CLASSES.get(kind, ComputeProviderError)
    return cls(f"{method}: {message}", kind=kind)

class _ComputeJob:
    def __init__(self, job_id, provider):
        self.job_id = job_id
        self.provider = provider

    def result(self):
        result = _host_rpc("host.compute.job_result", [self.job_id], {})
        state = result.get("state") if isinstance(result, dict) else None
        if state in {"pending", "staging", "queued", "running", "harvesting"}:
            raise ComputeJobPending(
                f"compute job {self.job_id} is still {state}", job_id=self.job_id
            )
        if state == "timed_out":
            details = dict(result)
            details.pop("job_id", None)
            raise ComputeJobTimedOut(
                f"compute job {self.job_id} timed out; partial outputs may be available",
                job_id=self.job_id, **details,
            )
        if state in {"failed", "orphaned"}:
            details = dict(result)
            details.pop("job_id", None)
            raise ComputeJobFailed(
                f"compute job {self.job_id} ended {state}",
                job_id=self.job_id, **details,
            )
        return result

    def cancel(self):
        return _host_rpc("host.compute.job_cancel", [self.job_id], {})

    @property
    def status(self):
        return _host_rpc("host.compute.attach_job", [self.job_id], {}).get("state")

    def __repr__(self):
        return f"<host.compute job {self.job_id}>"

class _ComputeHandle:
    def __init__(self, target, provider_params=None, handle_id=None):
        self.target = target
        self.provider_params = dict(provider_params or {})
        self.handle_id = handle_id

    def call_command(
        self, command, *, intent, timeout_seconds=60, login_shell=False
    ):
        if not isinstance(command, str) or not command.strip():
            raise TypeError("host.compute.call_command: command must be non-empty")
        if not isinstance(intent, str) or not intent.strip():
            raise TypeError("host.compute.call_command: intent must be non-empty")
        return _host_rpc(
            "host.compute.call_command",
            [self.target, command, intent, timeout_seconds, login_shell],
            {},
        )

    def submit_job(self, *, command, intent, **options):
        if not isinstance(command, str) or not command.strip():
            raise TypeError("host.compute.submit_job: command must be non-empty")
        if not isinstance(intent, str) or not intent.strip():
            raise TypeError("host.compute.submit_job: intent must be non-empty")
        request = {
            "provider": self.target,
            "provider_params": self.provider_params,
            **({"handle_id": self.handle_id} if self.handle_id else {}),
            "command": command,
            "intent": intent,
            **options,
        }
        result = _host_rpc("host.compute.submit_job", [request], {})
        job_id = result.get("job_id") if isinstance(result, dict) else None
        if not isinstance(job_id, str) or not job_id:
            raise RuntimeError("host.compute.submit_job: host returned no job id")
        return _ComputeJob(job_id, self.target)

    def attach_job(self, job_id):
        if not isinstance(job_id, str) or not job_id.strip():
            raise TypeError("host.compute.attach_job: job_id must be non-empty")
        result = _host_rpc("host.compute.attach_job", [job_id], {})
        provider = result.get("target") if isinstance(result, dict) else None
        if provider and provider != self.target:
            raise RuntimeError("host.compute.attach_job: job belongs to another provider")
        return _ComputeJob(job_id, self.target)

    def close(self):
        return _host_rpc("host.compute.close", [self.target, self.handle_id], {})

    def __repr__(self):
        return f"<host.compute {self.target}>"

class _ComputeAccessor:
    def create(self, target, provider_params=None):
        if not isinstance(target, str) or not target.strip():
            raise TypeError("host.compute.create: target must be a non-empty str")
        if provider_params is not None and not isinstance(provider_params, dict):
            raise TypeError("host.compute.create: provider_params must be a dict or None")
        resolved = _host_rpc(
            "host.compute.create", [target, dict(provider_params or {})], {}
        )
        return _ComputeHandle(
            resolved["target"], resolved.get("provider_params"), resolved.get("handle_id")
        )

    def ledger(self):
        return _host_rpc("host.compute.ledger", [], {})

    def status(self):
        return _host_rpc("host.compute.status", [], {})

    def config_get(self, provider):
        if not isinstance(provider, str) or not provider.strip():
            raise TypeError("host.compute.config_get: provider must be non-empty")
        return _host_rpc("host.compute.config_get", [provider], {})

    def set_concurrency_limit(self, max_concurrent):
        if isinstance(max_concurrent, bool) or not isinstance(max_concurrent, int):
            raise TypeError("host.compute.set_concurrency_limit: expected an int")
        return _host_rpc(
            "host.compute.set_concurrency_limit", [max_concurrent], {}
        )

    def __repr__(self):
        return "<host.compute>"

_compute_accessor = _ComputeAccessor()
_compute_accessor.Error = ComputeError
_compute_accessor.NotFound = ComputeNotFound
_compute_accessor.NotSupported = ComputeNotSupported
_compute_accessor.InvalidResource = ComputeInvalidResource
_compute_accessor.ConcurrencyFull = ComputeConcurrencyFull
_compute_accessor.Busy = ComputeBusy
_compute_accessor.JobPending = ComputeJobPending
_compute_accessor.JobTimedOut = ComputeJobTimedOut
_compute_accessor.JobFailed = ComputeJobFailed
_compute_accessor.ProviderError = ComputeProviderError

_view_image_counter = [0]


def _host_view_image(source, crop=None, *, max_size=1568, out=None):
    """Save an image or crop for automatic attachment to the next model turn."""
    import os as _os
    import re as _re

    if max_size is not None and (
        not isinstance(max_size, int)
        or isinstance(max_size, bool)
        or max_size < 1
    ):
        raise TypeError(
            "host.view_image: max_size must be a positive int or None, "
            f"got {max_size!r}"
        )
    try:
        from PIL import Image as _Image
    except ImportError:
        raise RuntimeError(
            "host.view_image requires Pillow; use a managed Python "
            "environment that includes pillow"
        ) from None

    region = None
    base = "image"
    if isinstance(source, _Image.Image):
        image = source
        width, height = image.size
        base = _os.path.splitext(
            _os.path.basename(getattr(source, "filename", "") or "image")
        )[0] or "image"
    else:
        if not isinstance(source, str) or not source:
            raise TypeError(
                "host.view_image: source must be a PIL.Image, a file path, "
                "or an artifact version_id"
            )
        if _re.fullmatch(r"[0-9a-fA-F-]{36}", source):
            path = _host_artifact_path(source)
        else:
            path = source
        if not _os.path.exists(path):
            raise FileNotFoundError(
                f"host.view_image: {path!r} not found; pass a workspace path, "
                "PIL.Image, or artifact version_id"
            )
        previous_limit = _Image.MAX_IMAGE_PIXELS
        _Image.MAX_IMAGE_PIXELS = None
        try:
            image = _Image.open(path)
            width, height = image.size
            if width * height > 500_000_000:
                raise ValueError(
                    f"host.view_image: image is {width}x{height}, exceeding "
                    "the 500 MP safety cap"
                )
            image.load()
        finally:
            _Image.MAX_IMAGE_PIXELS = previous_limit
        base = _os.path.splitext(_os.path.basename(path))[0] or "image"

    if crop is not None:
        if not hasattr(crop, "__len__") or len(crop) != 4:
            raise TypeError(
                "host.view_image: crop must be (x0, y0, x1, y1) in pixels "
                "or fractions"
            )
        x0, y0, x1, y1 = (float(value) for value in crop)
        if max(abs(x0), abs(y0), abs(x1), abs(y1)) <= 1.0:
            x0, y0, x1, y1 = (
                x0 * width, y0 * height, x1 * width, y1 * height
            )
        x0 = max(0, min(width, int(round(x0))))
        y0 = max(0, min(height, int(round(y0))))
        x1 = max(0, min(width, int(round(x1))))
        y1 = max(0, min(height, int(round(y1))))
        if x1 <= x0 or y1 <= y0:
            raise ValueError(
                "host.view_image: crop has zero or negative area after "
                f"clamping to {width}x{height}"
            )
        image = image.crop((x0, y0, x1, y1))
        region = (x0, y0, x1, y1)

    current_width, current_height = image.size
    if image.mode not in ("RGB", "RGBA", "L"):
        image = image.convert("RGB")
    if max_size and max(current_width, current_height) > max_size:
        scale = max_size / max(current_width, current_height)
        image = image.resize((
            max(1, int(current_width * scale)),
            max(1, int(current_height * scale)),
        ))

    _view_image_counter[0] += 1
    saved = out or f"_view_{_view_image_counter[0]:03d}_{base}.png"
    image.save(saved)
    crop_label = (
        f"crop x={region[0]}..{region[2]} y={region[1]}..{region[3]} "
        if region else ""
    )
    print(
        f"[view_image] {base!r} [{width}x{height}px] -> {crop_label}"
        f"[{image.size[0]}x{image.size[1]}px] -> {saved!r}"
    )
    return {
        "source": source if isinstance(source, str) else "<PIL.Image>",
        "original_size": (width, height),
        "crop": region,
        "output_size": image.size,
        "saved_to": saved,
    }

def _install_host_module(fresh=False):
    module = _host_types.ModuleType("host")
    module.routine = _routine_accessor
    module.compute = _compute_accessor
    module.capabilities = _host_capabilities
    module.credentials = _credentials_accessor
    module.query = _query_accessor
    module.model_endpoints = _model_endpoints_accessor
    module.exec_peek = _host_exec_peek
    module.exec_interrupt = _host_exec_interrupt
    module.findings = _host_findings
    module.get_local_compute_stats = _host_get_local_compute_stats
    module.get_user_email = _host_get_user_email
    module.reasoning_model = _host_reasoning_model
    module.submit_output = _host_submit_output
    module.CredentialUnavailable = CredentialUnavailable
    module.CredentialDeclined = CredentialDeclined
    module.ContactEmailUnavailable = ContactEmailUnavailable
    module.ContactEmailDeclined = ContactEmailDeclined
    module.llm = _host_llm
    module.current_model = _host_current_model
    module.list_models = _host_list_models
    module.artifacts = _host_artifacts
    module.archive = _archive_accessor
    module.skills = _skills_accessor
    module.agents = _agents_accessor
    module.artifact_path = _host_artifact_path
    module.artifact_marker = _host_artifact_marker
    module.lineage = _lineage_accessor
    module.frames = _host_frames
    if fresh:
        def _fresh_delegate(*args, **kwargs):
            raise RuntimeError(
                "host.delegate is unavailable in a fresh repl kernel because "
                "the kernel is destroyed after execution and cannot reliably track children"
            )
        module.delegate = _fresh_delegate
    else:
        module.delegate = _host_delegate
    module.collect = _host_collect
    module.children = _host_children
    module.delegation_stats = _host_delegation_stats
    module.stop_child = _host_stop_child
    module.send_message = _host_send_message
    module.mcp = _host_mcp
    module.mjson = json
    module.app = _host_app
    module.view_image = _host_view_image
    module.__all__ = (
        "routine", "compute", "capabilities", "credentials", "query", "model_endpoints", "llm", "current_model", "reasoning_model", "list_models", "delegate",
        "collect", "children", "delegation_stats", "stop_child", "send_message",
        "artifacts", "artifact_path", "artifact_marker", "lineage", "frames", "archive", "findings", "mcp", "mjson", "app", "view_image",
        "exec_peek", "exec_interrupt", "get_local_compute_stats", "get_user_email", "submit_output",
    )
    read_file_module = _host_types.ModuleType("read_file")
    read_file_module.read_file = _host_read_file
    read_file_module.__all__ = ("read_file",)
    sys.modules["read_file"] = read_file_module
    sys.modules["host"] = module
    namespace["host"] = module

def _remove_host_module():
    sys.modules.pop("read_file", None)
    sys.modules.pop("host", None)
    namespace.pop("host", None)


def bind_cell(cell_id, fresh, target_namespace, identity_validator):
    global namespace, _protocol_stdin, _protocol_stdout, _PROTOCOL_WRITE_LOCK, _identity_suspect
    namespace = target_namespace
    _protocol_stdin = getattr(sys, "_operon_protocol_stdin", None)
    _protocol_stdout = getattr(sys, "_operon_protocol_stdout", None)
    _PROTOCOL_WRITE_LOCK = getattr(sys, "_operon_protocol_lock", _PROTOCOL_WRITE_LOCK)
    _identity_suspect = identity_validator
    if _protocol_stdin is None or _protocol_stdout is None or not callable(_identity_suspect):
        raise RuntimeError("Synon host bridge protocol is unavailable")
    _active_host_cell["id"] = cell_id
    _host_mcp_catalog_cache["cell_id"] = None
    _host_mcp_catalog_cache["value"] = None
    _host_mcp_catalog_cache["methods"] = {}
    _install_host_module(bool(fresh))


def disable_cell(target_namespace):
    global namespace
    namespace = target_namespace
    _active_host_cell["id"] = None
    _remove_host_module()


def finish_cell():
    _active_host_cell["id"] = None
    _host_mcp_catalog_cache["cell_id"] = None
    _host_mcp_catalog_cache["value"] = None
    _host_mcp_catalog_cache["methods"] = {}
