#!/usr/bin/env python3
"""Synon local WebSearch tool. Returns JSON search results."""

import argparse
import base64
import html
from html.parser import HTMLParser
import json
import re
import sys
import urllib.parse
import urllib.request


DEFAULT_BACKEND_PLAN = [
    "auto",
    "duckduckgo,brave,mojeek,yahoo,wikipedia",
    "brave,mojeek",
    "duckduckgo,yahoo",
    "wikipedia",
]

USER_AGENT = "Mozilla/5.0 (X11; Linux x86_64) SynonBiomed/0.1 local websearch"


def result_url(result: dict) -> str:
    return str(result.get("url", result.get("href", result.get("link", "")))).strip()


def merge_unique(existing: list[dict], incoming: list[dict]) -> None:
    seen = {canonical_url(result_url(item)) for item in existing if result_url(item)}
    for item in incoming:
        url = result_url(item)
        key = canonical_url(url)
        if not key or key in seen:
            continue
        seen.add(key)
        existing.append(item)


def canonical_url(url: str) -> str:
    try:
        parsed = urllib.parse.urlsplit(url.strip())
        if parsed.scheme not in {"http", "https"} or not parsed.netloc:
            return ""
        return urllib.parse.urlunsplit(
            (
                parsed.scheme.lower(),
                parsed.netloc.lower(),
                parsed.path.rstrip("/") or "/",
                parsed.query,
                "",
            )
        )
    except Exception:
        return ""


def result_text(result: dict) -> str:
    return " ".join(
        str(result.get(key, ""))
        for key in ("title", "body", "snippet", "url", "href", "link")
    ).lower()


def query_terms(query: str) -> list[str]:
    stopwords = {"the", "and", "for", "with", "latest", "news", "最新", "消息"}
    terms = []
    for raw in query.lower().replace("-", " ").split():
        term = "".join(ch for ch in raw if ch.isalnum())
        if len(term) >= 3 and term not in stopwords and not term.isdigit():
            terms.append(term)
    return list(dict.fromkeys(terms))


def relevant_count(query: str, results: list[dict]) -> int:
    terms = query_terms(query)
    if len(terms) < 2:
        return len(results)
    threshold = max(1, len(terms) // 2)
    count = 0
    for result in results:
        text = result_text(result)
        matches = sum(1 for term in terms if term in text)
        if matches >= threshold:
            count += 1
    return count


def should_try_fallback(query: str, results: list[dict], max_results: int) -> bool:
    if len(results) < max(3, min(max_results, 5)):
        return True
    return relevant_count(query, results) < max(2, min(max_results, 4))


def relevance_score(query: str, result: dict) -> int:
    terms = query_terms(query)
    if not terms:
        return 0
    text = result_text(result)
    matches = sum(1 for term in terms if term in text)
    return matches * 100 - len(str(result.get("title", ""))) // 20


def rank_results(query: str, results: list[dict]) -> list[dict]:
    return sorted(
        results,
        key=lambda item: (
            relevance_score(query, item),
            1 if result_url(item).startswith("https://") else 0,
        ),
        reverse=True,
    )


def fetch_text(url: str, timeout: int = 10) -> str:
    request = urllib.request.Request(
        url,
        headers={
            "User-Agent": USER_AGENT,
            "Accept": "text/html,application/xhtml+xml,application/json,text/plain,*/*",
            "Accept-Language": "en-US,en;q=0.8,zh-CN;q=0.6,zh;q=0.5",
        },
    )
    with urllib.request.urlopen(request, timeout=timeout) as response:
        raw = response.read()
        charset = response.headers.get_content_charset() or "utf-8"
        return raw.decode(charset, errors="replace")


class _VisibleTextParser(HTMLParser):
    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self._ignored_depth = 0
        self.parts: list[str] = []

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        if tag.lower() in {"script", "style"}:
            self._ignored_depth += 1

    def handle_endtag(self, tag: str) -> None:
        if tag.lower() in {"script", "style"} and self._ignored_depth:
            self._ignored_depth -= 1

    def handle_data(self, data: str) -> None:
        if not self._ignored_depth:
            self.parts.append(data)


def strip_tags(value: str) -> str:
    parser = _VisibleTextParser()
    try:
        parser.feed(value)
        parser.close()
    except (AssertionError, ValueError):
        return ""
    return re.sub(r"\s+", " ", " ".join(parser.parts)).strip()


def clean_url(url: str) -> str:
    url = html.unescape(url or "").strip()
    if url.startswith("//"):
        url = f"https:{url}"
    if url.startswith("/"):
        return ""
    url = unwrap_bing_redirect(url)
    try:
        parsed = urllib.parse.urlsplit(url)
    except ValueError:
        return ""
    return url if parsed.scheme in {"http", "https"} and parsed.hostname else ""


def _is_host_or_subdomain(hostname: str | None, domain: str) -> bool:
    host = (hostname or "").rstrip(".").casefold()
    domain = domain.casefold()
    return host == domain or host.endswith(f".{domain}")


def unwrap_bing_redirect(url: str) -> str:
    try:
        parsed = urllib.parse.urlsplit(url)
        if not _is_host_or_subdomain(parsed.hostname, "bing.com") or not parsed.path.startswith("/ck/"):
            return url
        encoded = urllib.parse.parse_qs(parsed.query).get("u", [""])[0]
        if encoded.startswith("a1"):
            encoded = encoded[2:]
        if not encoded:
            return url
        padded = encoded + "=" * (-len(encoded) % 4)
        decoded = base64.urlsafe_b64decode(padded.encode("ascii")).decode("utf-8", errors="replace")
        return decoded if decoded.startswith(("http://", "https://")) else url
    except Exception:
        return url


def search_ddgs(query: str, max_results: int, region: str, exhaustive: bool) -> list[dict]:
    ddgs_cls = None
    try:
        from ddgs import DDGS  # type: ignore

        ddgs_cls = DDGS
    except Exception:
        try:
            from duckduckgo_search import DDGS  # type: ignore

            ddgs_cls = DDGS
        except Exception:
            return []

    merged: list[dict] = []
    with ddgs_cls() as ddgs:
        for index, backend in enumerate(DEFAULT_BACKEND_PLAN):
            try:
                results = list(
                    ddgs.text(
                        query,
                        region=region,
                        max_results=max(max_results, 10),
                        backend=backend,
                    )
                )
                normalized = [
                    {
                        "title": item.get("title", ""),
                        "url": item.get("href", item.get("link", "")),
                        "snippet": item.get("body", item.get("snippet", "")),
                    }
                    for item in results
                    if item.get("href") or item.get("link")
                ]
                merge_unique(merged, normalized)
                if index == 0 and not exhaustive and not should_try_fallback(query, merged, max_results):
                    break
                if len(merged) >= max_results and index >= 1:
                    break
            except Exception:
                continue
    return merged[:max_results]


def search_bing_html(query: str, max_results: int) -> list[dict]:
    url = "https://www.bing.com/search?" + urllib.parse.urlencode(
        {"q": query, "count": str(max(max_results, 10))}
    )
    page = fetch_text(url)
    results: list[dict] = []
    for block in re.findall(
        r'<li[^>]+class="[^"]*\bb_algo\b[^"]*"[^>]*>([\s\S]*?)</li>',
        page,
        flags=re.I,
    ):
        link_match = re.search(
            r'<h2[^>]*>[\s\S]*?<a[^>]+href="([^"]+)"[^>]*>([\s\S]*?)</a>',
            block,
            flags=re.I,
        )
        if not link_match:
            continue
        url_value = clean_url(link_match.group(1))
        title = strip_tags(link_match.group(2))
        if not url_value or not title:
            continue
        snippet_match = re.search(r"<p[^>]*>([\s\S]*?)</p>", block, flags=re.I)
        results.append(
            {
                "title": title,
                "url": url_value,
                "snippet": strip_tags(snippet_match.group(1)) if snippet_match else "",
            }
        )
        if len(results) >= max_results:
            break
    return results


def search_duckduckgo_html(query: str, max_results: int) -> list[dict]:
    url = "https://html.duckduckgo.com/html/?" + urllib.parse.urlencode({"q": query})
    page = fetch_text(url)
    results: list[dict] = []
    blocks = re.findall(
        r'<div[^>]+class="[^"]*\bresult\b[^"]*"[^>]*>([\s\S]*?)</div>\s*</div>',
        page,
        flags=re.I,
    )
    if not blocks:
        blocks = page.split('<div class="result')
    for block in blocks:
        link_match = re.search(
            r'<a[^>]+class="[^"]*\bresult__a\b[^"]*"[^>]+href="([^"]+)"[^>]*>([\s\S]*?)</a>',
            block,
            flags=re.I,
        )
        if not link_match:
            continue
        url_value = clean_url(link_match.group(1))
        title = strip_tags(link_match.group(2))
        snippet_match = re.search(
            r'<a[^>]+class="[^"]*\bresult__snippet\b[^"]*"[^>]*>([\s\S]*?)</a>',
            block,
            flags=re.I,
        )
        if not url_value or not title:
            continue
        results.append(
            {
                "title": title,
                "url": url_value,
                "snippet": strip_tags(snippet_match.group(1)) if snippet_match else "",
            }
        )
        if len(results) >= max_results:
            break
    return results


def search_wikipedia(query: str, max_results: int) -> list[dict]:
    url = "https://en.wikipedia.org/w/api.php?" + urllib.parse.urlencode(
        {
            "action": "opensearch",
            "search": query,
            "limit": str(min(max_results, 8)),
            "namespace": "0",
            "format": "json",
        }
    )
    data = json.loads(fetch_text(url))
    titles = data[1] if len(data) > 1 else []
    snippets = data[2] if len(data) > 2 else []
    urls = data[3] if len(data) > 3 else []
    return [
        {
            "title": title,
            "url": urls[index],
            "snippet": snippets[index] if index < len(snippets) else "",
        }
        for index, title in enumerate(titles)
        if index < len(urls) and urls[index]
    ]


def search(
    query: str,
    max_results: int = 8,
    region: str = "wt-wt",
    exhaustive: bool = False,
) -> list[dict]:
    merged: list[dict] = []
    errors: list[str] = []
    providers = [
        ("bing_html", lambda: search_bing_html(query, max_results)),
        ("wikipedia", lambda: search_wikipedia(query, max_results)),
        ("duckduckgo_html", lambda: search_duckduckgo_html(query, max_results)),
        ("ddgs", lambda: search_ddgs(query, max_results, region, exhaustive)),
    ]
    for name, provider in providers:
        try:
            merge_unique(merged, provider())
            if not should_try_fallback(query, merged, max_results):
                break
        except Exception as error:
            errors.append(f"{name}: {error}")
    if merged:
        return rank_results(query, merged)[:max_results]

    details = "; ".join(errors) if errors else "no results"
    raise RuntimeError(f"all general web backends failed or returned no results: {details}")


def backend_plan() -> str:
    return ";".join(DEFAULT_BACKEND_PLAN)


def main() -> None:
    parser = argparse.ArgumentParser(description="Synon local WebSearch")
    parser.add_argument("query", nargs="?", default="", help="Search query")
    parser.add_argument("-n", "--max-results", type=int, default=8, help="Max results")
    parser.add_argument("-r", "--region", default="wt-wt", help="Search region")
    parser.add_argument(
        "--exhaustive",
        action="store_true",
        help="Always run the first fallback backend group",
    )
    parser.add_argument("--print-backends", action="store_true", help="Print backend plan")
    parser.add_argument("--no-title", action="store_true", help="Exclude titles")
    args = parser.parse_args()

    try:
        if args.print_backends:
            print(backend_plan())
            return
        if not args.query.strip():
            parser.error("query is required unless --print-backends is used")
        results = search(args.query, args.max_results, args.region, args.exhaustive)
        output = []
        for i, result in enumerate(results, 1):
            item = {"rank": i}
            if not args.no_title:
                item["title"] = result.get("title", "")
            item["url"] = result_url(result)
            item["snippet"] = result.get("body", result.get("snippet", ""))
            output.append(item)
        print(json.dumps(output, ensure_ascii=False, indent=2))
    except Exception as error:
        print(json.dumps({"error": str(error)}, ensure_ascii=False), file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
