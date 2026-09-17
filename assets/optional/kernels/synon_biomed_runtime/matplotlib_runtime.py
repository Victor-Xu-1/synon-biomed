"""Process-wide Matplotlib defaults for multilingual scientific figures.

The kernel worker applies this module once before executing user cells. The
same policy is written to a private ``MPLCONFIGDIR`` so Python subprocesses
spawned by a cell inherit it as well. Individual analyses may still override
the style, but resetting Matplotlib to its defaults must not silently fall back
to a Latin-only font.
"""

from __future__ import annotations

import atexit
import os
import shutil
import tempfile
from pathlib import Path

_CJK_PROBE = "中文化合物阿司匹林对乙酰氨基酚"
_PREFERRED_FAMILIES = (
    "Noto Sans CJK SC",
    "Noto Sans CJK JP",
    "Droid Sans Fallback",
    "Microsoft YaHei",
    "PingFang SC",
    "Arial Unicode MS",
)
_PREFERRED_PATHS = (
    "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
    "/usr/share/fonts/truetype/droid/DroidSansFallbackFull.ttf",
    "/System/Library/Fonts/PingFang.ttc",
    "C:/Windows/Fonts/msyh.ttc",
    "C:/Windows/Fonts/simhei.ttf",
)
_runtime_config_dir: str | None = None


def _supports_probe(path: str, ft2font) -> bool:
    try:
        charmap = ft2font.FT2Font(path).get_charmap()
    except (OSError, RuntimeError, ValueError):
        return False
    return all(ord(character) in charmap for character in _CJK_PROBE)


def _registered_cjk_family(font_manager, ft2font) -> tuple[str | None, str | None]:
    for family in _PREFERRED_FAMILIES:
        try:
            path = font_manager.findfont(
                font_manager.FontProperties(family=[family]),
                fallback_to_default=False,
            )
        except (ValueError, RuntimeError):
            continue
        path = str(path)
        if _supports_probe(path, ft2font):
            return family, path

    candidates = list(_PREFERRED_PATHS)
    prefix = os.environ.get("CONDA_PREFIX", "").strip()
    if prefix:
        candidates.extend(
            str(path)
            for directory in (Path(prefix) / "fonts", Path(prefix) / "share" / "fonts")
            if directory.is_dir()
            for pattern in ("*.ttf", "*.otf", "*.ttc")
            for path in sorted(directory.rglob(pattern))
        )
    for candidate in candidates:
        path = Path(candidate)
        if not path.is_file() or not _supports_probe(str(path), ft2font):
            continue
        try:
            font_manager.fontManager.addfont(str(path))
            family = font_manager.FontProperties(fname=str(path)).get_name()
        except (OSError, RuntimeError, ValueError):
            continue
        if family:
            return family, str(path)
    return None, None


def _unique(values: list[str]) -> list[str]:
    seen: set[str] = set()
    result: list[str] = []
    for value in values:
        value = str(value).strip()
        if value and value not in seen:
            seen.add(value)
            result.append(value)
    return result


def _write_subprocess_config(families: list[str]) -> str:
    global _runtime_config_dir
    if _runtime_config_dir and Path(_runtime_config_dir).is_dir():
        return _runtime_config_dir
    directory = tempfile.mkdtemp(prefix="synon-matplotlib-")
    Path(directory, "matplotlibrc").write_text(
        "font.family: sans-serif\n"
        f"font.sans-serif: {', '.join(families)}\n"
        "axes.unicode_minus: False\n"
        "pdf.fonttype: 42\n"
        "ps.fonttype: 42\n"
        "svg.fonttype: path\n",
        encoding="utf-8",
    )
    _runtime_config_dir = directory
    atexit.register(shutil.rmtree, directory, ignore_errors=True)
    return directory


def configure_matplotlib_runtime() -> dict[str, str | None]:
    """Install the canonical multilingual figure policy for this process."""

    try:
        import matplotlib as mpl
        from matplotlib import font_manager, ft2font
    except ImportError:
        return {"family": None, "path": None, "config_dir": None}

    family, path = _registered_cjk_family(font_manager, ft2font)
    existing = list(mpl.rcParams.get("font.sans-serif", []))
    families = _unique(
        ([family] if family else [])
        + list(_PREFERRED_FAMILIES)
        + existing
        + ["DejaVu Sans"]
    )
    policy = {
        "font.family": ["sans-serif"],
        "font.sans-serif": families,
        "axes.unicode_minus": False,
        "pdf.fonttype": 42,
        "ps.fonttype": 42,
        "svg.fonttype": "path",
    }
    # rcdefaults() and style resets read these two maps. Updating all three
    # prevents a later style reset from reintroducing the Latin-only default.
    for target in (mpl.rcParamsDefault, mpl.rcParamsOrig, mpl.rcParams):
        target.update(policy)

    config_dir = _write_subprocess_config(families)
    os.environ["MPLCONFIGDIR"] = config_dir
    kernel_root = str(Path(__file__).resolve().parents[1])
    python_path = [kernel_root]
    python_path.extend(
        entry
        for entry in os.environ.get("PYTHONPATH", "").split(os.pathsep)
        if entry and entry != kernel_root
    )
    os.environ["PYTHONPATH"] = os.pathsep.join(python_path)
    return {"family": family, "path": path, "config_dir": config_dir}


__all__ = ["configure_matplotlib_runtime"]
