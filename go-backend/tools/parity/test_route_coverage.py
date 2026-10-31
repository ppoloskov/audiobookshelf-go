"""Keep route inventory tied to the executable core case catalog."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("route_coverage", Path(__file__).with_name("build_route_coverage.py"))
coverage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(coverage)


class CaseCatalogTests(unittest.TestCase):
    def test_uses_executable_catalog(self):
        self.assertEqual(coverage.CASES_FILE, coverage.ROOT / "go-backend/tools/parity/cases/core.json")

    def test_reads_coverage_metadata_from_executable_record(self):
        with tempfile.TemporaryDirectory() as directory:
            catalog = Path(directory) / "core.json"
            catalog.write_text(json.dumps([{
                "id": "new-workflow", "group": "inline",
                "coverage": {"method": "POST", "route": "/api/playlists/collection/{collectionId}", "category": "stateful"},
            }]))
            cases = coverage.load_cases(catalog)
        self.assertEqual(cases[0]["id"], "new-workflow")
        self.assertEqual(cases[0]["method"], "POST")
        route = {"method": "post", "route": "/playlists/collection/:collectionId", "route_kind": "string_literal"}
        self.assertTrue(coverage.case_matches(route, cases[0]))

    def test_rejects_duplicate_ids_instead_of_double_counting(self):
        with tempfile.TemporaryDirectory() as directory:
            catalog = Path(directory) / "core.json"
            catalog.write_text(json.dumps([{"id": "same"}, {"id": "same"}]))
            with self.assertRaisesRegex(ValueError, "duplicate"):
                coverage.load_cases(catalog)

    def test_unannotated_requests_use_the_actual_method_and_route(self):
        with tempfile.TemporaryDirectory() as directory:
            catalog = Path(directory) / "core.json"
            catalog.write_text(json.dumps([{"id": "request", "route": "/api/items/{{itemID}}", "options": {"method": "PATCH"}}]))
            cases = coverage.load_cases(catalog)
        self.assertEqual(cases[0]["method"], "PATCH")
        self.assertEqual(cases[0]["route"], "/api/items/{itemID}")


if __name__ == "__main__":
    unittest.main()
