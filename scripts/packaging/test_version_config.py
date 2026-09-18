"""Version proposals must update product projections without touching dependencies."""

import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]


class VersionConfigTest(unittest.TestCase):
    def test_single_product_release_strategy_preserves_review_boundary(self):
        config = json.loads((ROOT / ".github/release-please-config.json").read_text())
        self.assertEqual(set(config["packages"]), {"."})
        product = config["packages"]["."]
        self.assertEqual(product["release-type"], "go")
        self.assertEqual(product["versioning"], "always-bump-patch")
        self.assertRegex(product["initial-version"], r"^0\.1\.(?:0|[1-9][0-9]*)$")
        self.assertTrue(product["skip-github-release"])
        self.assertFalse(product["include-component-in-tag"])
        self.assertTrue(product["include-v-in-tag"])
        self.assertNotIn("release-as", product)
        self.assertNotIn("version-file", product)
        self.assertEqual(product["changelog-path"], "docs/CHANGELOG.md")
        self.assertNotIn("bump-minor-pre-major", product)
        self.assertNotIn("bump-patch-for-minor-pre-major", product)

    def test_all_json_version_projections_are_updated_and_dependencies_are_not(self):
        config = json.loads((ROOT / ".github/release-please-config.json").read_text())
        projections = json.loads((ROOT / "docs/governance/product-identity-consumer-matrix.json").read_text())
        expected = {("product-identity.json", "$.version")}
        for projection in projections["product_version_projections"]:
            if projection["kind"] != "json-pointer":
                continue
            segments = [s.replace("~1", "/").replace("~0", "~") for s in projection["pointer"].split("/")[1:]]
            jsonpath = "$" + "".join("." + s if s.isidentifier() else "['" + s + "']" for s in segments)
            expected.add((projection["path"], jsonpath))
        extras = config["packages"]["."]["extra-files"]
        self.assertEqual(len(extras), len(expected))
        self.assertTrue(all(item["type"] == "json" and not item.get("glob") for item in extras))
        self.assertEqual({(item["path"], item["jsonpath"]) for item in extras}, expected)

    def test_bot_bookkeeping_is_not_a_runtime_dependency(self):
        manifest = json.loads((ROOT / ".github/release-please-manifest.json").read_text())
        authority = json.loads((ROOT / "product-identity.json").read_text())
        self.assertRegex(authority["version"], r"^0\.1\.(?:0|[1-9][0-9]*)$")
        self.assertEqual(manifest, {".": authority["version"]})
        self.assertNotIn("release-please", (ROOT / "identity.go").read_text())


if __name__ == "__main__":
    unittest.main()
