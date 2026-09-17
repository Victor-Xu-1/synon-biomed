"""Resolve the production renderer import graph and its request paths."""
from __future__ import annotations

import collections
from pathlib import Path
import re
from typing import Any


SOURCE_EXTENSIONS = (
    ".ts",
    ".tsx",
    ".js",
    ".jsx",
    ".css",
    ".scss",
    ".json",
    ".html",
    ".svg",
    ".wasm",
)


def read_text(path: Path) -> str:
    return path.read_text(encoding="utf-8", errors="replace")


def extract_imports(source: str) -> list[str]:
    patterns = (
        r"(?:import|export)\s+(?:type\s+)?(?:[\s\S]*?\s+from\s+)?[\"']([^\"']+)[\"']",
        r"import\s*\(\s*[\"']([^\"']+)[\"']\s*\)",
    )
    found: set[str] = set()
    for pattern in patterns:
        found.update(re.findall(pattern, source))
    return sorted(found)


def strip_full_line_and_block_comments(source: str) -> str:
    source = re.sub(r"/\*[\s\S]*?\*/", "", source)
    return re.sub(r"(?m)^\s*//.*$", "", source)


def extract_literal_paths(source: str, prefix_pattern: str) -> set[str]:
    paths: set[str] = set()
    for quote in ("'", '"', "\x60"):
        escaped = re.escape(quote)
        pattern = re.compile(
            escaped + f"({prefix_pattern}(?:\\\\.|[^{escaped}\\\\])*)" + escaped,
            re.DOTALL,
        )
        for value in pattern.findall(source):
            if "\n" in value or "\r" in value:
                continue
            normalized = normalize_api_path(value)
            if any(character.isspace() for character in normalized):
                continue
            paths.add(normalized)
    return paths


def external_package(specifier: str) -> str:
    parts = specifier.split("/")
    if specifier.startswith("@") and len(parts) >= 2:
        return "/".join(parts[:2])
    return parts[0]


def resolve_local_import(frontend: Path, importer: Path, specifier: str) -> Path | None:
    desktop_source = frontend / "packages/desktop/src"
    renderer = desktop_source / "renderer"
    aliases = {
        "@/": desktop_source,
        "@common/": desktop_source / "common",
        "@renderer/": renderer,
    }
    base: Path | None = None
    suffix = ""
    for prefix, alias_root in aliases.items():
        if specifier.startswith(prefix):
            base = alias_root
            suffix = specifier[len(prefix) :]
            break
    if base is None and specifier.startswith("."):
        base = importer.parent
        suffix = specifier
    if base is None:
        return None
    candidate = (base / suffix.split("?", 1)[0]).resolve()
    candidates = [candidate]
    if not candidate.suffix:
        candidates.extend(Path(str(candidate) + extension) for extension in SOURCE_EXTENSIONS)
        candidates.extend(candidate / ("index" + extension) for extension in SOURCE_EXTENSIONS)
    for item in candidates:
        if item.is_file():
            try:
                item.relative_to(frontend.resolve())
            except ValueError:
                return None
            return item
    return Path()


def normalize_api_path(value: str) -> str:
    value = re.sub(r"\$\{[^}]+\}", "{param}", value)
    if "${" in value:
        value = value.split("${", 1)[0] + "{param}"
    value = value.replace("\\/", "/")
    return value.rstrip(".,; ")


def active_frontend_graph(frontend: Path) -> dict[str, Any]:
    entry = frontend / "packages/desktop/src/renderer/main.tsx"
    if not entry.is_file():
        raise FileNotFoundError(f"frontend entry is missing: {entry}")
    queue = collections.deque([entry.resolve()])
    visited: set[Path] = set()
    unresolved: set[tuple[str, str]] = set()
    external: set[str] = set()
    import_edges = 0
    while queue:
        path = queue.popleft()
        if path in visited:
            continue
        visited.add(path)
        if path.suffix.lower() not in {".ts", ".tsx", ".js", ".jsx", ".css", ".scss"}:
            continue
        source = strip_full_line_and_block_comments(read_text(path))
        for specifier in extract_imports(source):
            import_edges += 1
            resolved = resolve_local_import(frontend, path, specifier)
            if resolved is None:
                external.add(external_package(specifier))
            elif resolved == Path():
                unresolved.add((path.relative_to(frontend).as_posix(), specifier))
            elif resolved not in visited:
                queue.append(resolved)

    api_paths: set[str] = set()
    websocket_paths: set[str] = set()
    routes: set[str] = set()
    by_extension: collections.Counter[str] = collections.Counter()
    page_roots: set[str] = set()
    route_pattern = re.compile(r"""path\s*=\s*[\"']([^\"']+)[\"']""")
    for path in visited:
        relative = path.relative_to(frontend).as_posix()
        by_extension[path.suffix.lower() or "<none>"] += 1
        marker = "packages/desktop/src/renderer/pages/"
        if marker in relative:
            remainder = relative.split(marker, 1)[1]
            page_roots.add(remainder.split("/", 1)[0])
        if path.suffix.lower() not in {".ts", ".tsx", ".js", ".jsx"}:
            continue
        source = strip_full_line_and_block_comments(read_text(path))
        api_paths.update(extract_literal_paths(source, r"/api/"))
        websocket_paths.update(extract_literal_paths(source, r"/(?:api/)?(?:ws|websocket)"))
        routes.update(route_pattern.findall(source))

    return {
        "entry": entry.relative_to(frontend).as_posix(),
        "method": "recursive static and dynamic import resolution from the production renderer entry",
        "activeFiles": len(visited),
        "importEdges": import_edges,
        "filesByExtension": dict(sorted(by_extension.items())),
        "externalPackages": sorted(external),
        "unresolvedLocalImports": [
            {"importer": importer, "specifier": specifier}
            for importer, specifier in sorted(unresolved)
        ],
        "routes": sorted(routes),
        "pageRoots": sorted(page_roots),
        "apiPaths": sorted(api_paths),
        "websocketPaths": sorted(websocket_paths),
        "files": sorted(path.relative_to(frontend).as_posix() for path in visited),
    }
