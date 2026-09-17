"""Shared handling for explicitly blank optional search criteria."""

from __future__ import annotations

__all__ = ["blank", "none_if_blank", "reject_blank"]


def blank(value: object) -> bool:
    return isinstance(value, str) and not value.strip()


def none_if_blank(value):
    if value is None or blank(value):
        return None
    if isinstance(value, str) and value.strip().casefold() in {"null", "none"}:
        return None
    return value


def reject_blank(_mapping=None, **criteria) -> None:
    invalid = sorted({
        key
        for values in (_mapping or {}, criteria)
        for key, value in values.items()
        if blank(value)
    })
    if invalid:
        raise ValueError(
            f"{', '.join(invalid)} must be non-empty when provided \u2014 "
            "an empty search criterion is not a search (omit the parameter "
            "for the tool's documented default, or provide a value)"
        )
