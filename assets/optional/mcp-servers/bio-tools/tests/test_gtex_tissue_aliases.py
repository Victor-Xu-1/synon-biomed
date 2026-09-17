from __future__ import annotations

import unittest

from gtex_expression.tool import GtexExpression


class _Client:
    def __init__(self) -> None:
        self.calls: list[tuple[str, dict]] = []

    def get_json(self, path: str, params: dict | None = None):
        query = dict(params or {})
        self.calls.append((path, query))
        if path == "/dataset/tissueSiteDetail":
            return {
                "paging_info": {"totalNumberOfItems": 1, "numberOfPages": 1},
                "data": [
                    {
                        "tissueSiteDetailId": "Breast_Mammary_Tissue",
                        "tissueSiteDetail": "Breast - Mammary Tissue",
                        "tissueSiteDetailAbbr": "BREAST",
                    }
                ],
            }
        if path == "/expression/medianGeneExpression":
            return {
                "paging_info": {"totalNumberOfItems": 1, "numberOfPages": 1},
                "data": [
                    {
                        "gencodeId": "ENSG00000012048.20",
                        "tissueSiteDetailId": "Breast_Mammary_Tissue",
                        "median": 1.5,
                    }
                ],
            }
        if path == "/association/dyneqtl":
            return {"data": [], "genotypes": []}
        if path in {
            "/dataset/sample",
            "/expression/geneExpression",
            "/expression/topExpressedGene",
            "/association/egene",
            "/association/singleTissueEqtl",
        }:
            return {
                "paging_info": {"totalNumberOfItems": 0, "numberOfPages": 1},
                "data": [],
            }
        raise AssertionError(f"unexpected path: {path}")


class GtexTissueAliasTests(unittest.TestCase):
    def test_display_label_is_resolved_to_the_official_tissue_id(self) -> None:
        client = _Client()
        expression = GtexExpression(client=client)

        result = expression.median_expression(
            ["ENSG00000012048.20"], ["Breast - Mammary Tissue"]
        )

        request = next(params for path, params in client.calls if path.endswith("medianGeneExpression"))
        self.assertEqual(request["tissueSiteDetailId"], ["Breast_Mammary_Tissue"])
        self.assertEqual(result["total"], 1)

    def test_unknown_tissue_fails_before_the_expression_request(self) -> None:
        client = _Client()
        expression = GtexExpression(client=client)

        with self.assertRaisesRegex(ValueError, "call gtex_tissue_sites"):
            expression.median_expression(
                ["ENSG00000012048.20"], ["not a real GTEx tissue"]
            )

        self.assertFalse(
            any(path.endswith("medianGeneExpression") for path, _ in client.calls)
        )

    def test_display_label_is_resolved_for_every_tissue_filtered_route(self) -> None:
        label = "Breast - Mammary Tissue"
        expected = "Breast_Mammary_Tissue"
        cases = [
            ("/dataset/sample", lambda tool: tool.sample_info(label, max_items=1), expected),
            (
                "/expression/geneExpression",
                lambda tool: tool.gene_expression("ENSG00000012048.20", [label]),
                [expected],
            ),
            (
                "/expression/topExpressedGene",
                lambda tool: tool.top_expressed(label, n=1),
                expected,
            ),
            ("/association/egene", lambda tool: tool.eqtl_genes(label, max_items=1), expected),
            (
                "/association/singleTissueEqtl",
                lambda tool: tool.single_tissue_eqtls(
                    gencode_id="ENSG00000012048.20", tissue_site_detail_id=label
                ),
                expected,
            ),
            (
                "/association/dyneqtl",
                lambda tool: tool.calculate_eqtl(
                    "ENSG00000012048.20", "chr17_43071077_G_A_b38", label
                ),
                expected,
            ),
        ]

        for route, invoke, expected_value in cases:
            with self.subTest(route=route):
                client = _Client()
                invoke(GtexExpression(client=client))
                request = next(params for path, params in client.calls if path == route)
                self.assertEqual(request["tissueSiteDetailId"], expected_value)


if __name__ == "__main__":
    unittest.main()
