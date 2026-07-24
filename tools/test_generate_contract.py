import json
import tempfile
import unittest
from pathlib import Path

from tools import generate_contract


class ContractAuthorityTests(unittest.TestCase):
    def setUp(self):
        self.document = json.loads(
            generate_contract.CONTRACT_PATH.read_text(encoding="utf-8")
        )

    def assert_contract_rejected(self, mutate, message):
        mutate(self.document)
        with tempfile.TemporaryDirectory() as directory:
            contract_path = Path(directory) / "openapi.json"
            contract_path.write_text(
                json.dumps(self.document),
                encoding="utf-8",
            )
            original_path = generate_contract.CONTRACT_PATH
            generate_contract.CONTRACT_PATH = contract_path
            try:
                with self.assertRaisesRegex(generate_contract.ContractError, message):
                    contract = generate_contract.load_contract()
                    generate_contract.contract_operations(contract)
            finally:
                generate_contract.CONTRACT_PATH = original_path

    def test_health_release_version_must_match_info_version(self):
        self.assert_contract_rejected(
            lambda document: document["components"]["schemas"]["HealthData"][
                "properties"
            ]["release_version"].update({"const": "9.9.9"}),
            r"release_version\.const must equal info\.version",
        )

    def test_health_release_status_must_match_extension(self):
        self.assert_contract_rejected(
            lambda document: document["components"]["schemas"]["HealthData"][
                "properties"
            ]["release_status"].update({"const": "stable"}),
            r"release_status\.const must equal x-rin-release-status",
        )

    def test_operation_id_must_be_lower_snake_case(self):
        self.assert_contract_rejected(
            lambda document: document["paths"]["/health"]["get"].update(
                {"operationId": "HealthCheck"}
            ),
            r"operationId 'HealthCheck' must use lower_snake_case",
        )


if __name__ == "__main__":
    unittest.main()
