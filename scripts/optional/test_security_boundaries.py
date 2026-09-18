import base64
import importlib.util
import pathlib
import unittest


ROOT = pathlib.Path(__file__).parents[2]


def load_module(relative: str, name: str):
    path = ROOT / relative
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


websearch = load_module("scripts/optional/websearch.py", "synon_websearch_security")
literature_kernel = load_module(
    "skills/synonbiomed/literature-review/kernel.py", "synon_literature_security"
)


class SecurityBoundaryTests(unittest.TestCase):
    def test_html_parser_removes_active_content_without_regex_overmatching(self):
        self.assertEqual(
            websearch.strip_tags("<b>Title</b><script>alert(1)</script><i>body</i>"),
            "Title body",
        )
        self.assertEqual(websearch.strip_tags('<a title="x > y">safe</a>'), "safe")

    def test_bing_redirect_requires_exact_host_boundary(self):
        target = "https://example.org/result"
        encoded = base64.urlsafe_b64encode(target.encode()).decode().rstrip("=")
        redirect = f"https://www.bing.com/ck/a?u=a1{encoded}"
        self.assertEqual(websearch.unwrap_bing_redirect(redirect), target)
        foreign = f"https://notbing.com/ck/a?u=a1{encoded}"
        self.assertEqual(websearch.unwrap_bing_redirect(foreign), foreign)

    def test_doi_extraction_bounds_each_candidate(self):
        text = "doi: 10.1234/valid.value, " + ("x" * 5000) + " 10.5678/second"
        values = literature_kernel.extract_dois(text)
        self.assertIn("10.1234/valid.value", values)
        self.assertIn("10.5678/second", values)
        self.assertLessEqual(max(map(len, values)), 512)


if __name__ == "__main__":
    unittest.main()
