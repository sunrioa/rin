import io
import json
import sys
import unittest
from pathlib import Path


sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from rin_sdk import (  # noqa: E402
    CONTROL_CONTRACT_VERSION,
    CONTROL_MAX_RESPONSE_BYTES,
    RinAPIError,
    RinConfigurationError,
    RinControlClient,
    RinProtocolError,
    RinTransportError,
)


FIXTURES = json.loads(
    (Path(__file__).resolve().parents[3] / "api" / "control-v2-fixtures.json")
    .read_text(encoding="utf-8")
)
PLAN_FIXTURES = json.loads(
    (Path(__file__).resolve().parents[3] / "api" / "task-plan-v1-fixtures.json")
    .read_text(encoding="utf-8")
)
TOKEN = "control-fixture-token-32-bytes!!"


class _Response:
    def __init__(self, status, value, content_type="application/json"):
        self.status = status
        self.payload = json.dumps(value).encode("utf-8")
        self.headers = {
            "Content-Length": str(len(self.payload)),
            "Content-Type": content_type,
        }
        self.stream = io.BytesIO(self.payload)

    def getcode(self):
        return self.status

    def read(self, maximum=-1):
        return self.stream.read(maximum)

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return False

    def close(self):
        pass


class _Opener:
    def __init__(self):
        self.requests = []
        self.status = 200
        self.value = {"status": "ok"}
        self.content_type = "application/json"

    def open(self, request, timeout):
        del timeout
        path = request.full_url.split("7375", 1)[-1]
        body = json.loads(request.data.decode("utf-8")) if request.data else None
        self.requests.append(
            {
                "path": path,
                "method": request.get_method(),
                "authorization": request.get_header("Authorization", ""),
                "body": body,
            }
        )
        value = self.value
        if path == "/control/v2/info" and self.status == 200:
            value = {
                "contract_version": CONTROL_CONTRACT_VERSION,
                "principal": {"id": "player.fixture"},
            }
        return _Response(self.status, value, self.content_type)


class ControlClientTests(unittest.TestCase):
    def test_all_v2_client_routes_use_shared_fixture(self):
        client = RinControlClient(token=TOKEN)
        opener = _Opener()
        client._opener = opener
        cases = (
            (client.info, (), "GET", "/control/v2/info", None),
            (client.list_worlds, (), "POST", "/control/v2/worlds", {}),
            (client.list_actors, (FIXTURES["world_target"],), "POST", "/control/v2/actors", FIXTURES["world_target"]),
            (client.get_actor, (FIXTURES["actor_target"],), "POST", "/control/v2/actor", FIXTURES["actor_target"]),
            (client.wait_actor, (FIXTURES["wait_actor"],), "POST", "/control/v2/wait-actor", FIXTURES["wait_actor"]),
            (client.observe_actor, (FIXTURES["actor_target"],), "POST", "/control/v2/observe", FIXTURES["actor_target"]),
            (client.list_capabilities, (FIXTURES["actor_target"],), "POST", "/control/v2/capabilities", FIXTURES["actor_target"]),
            (client.describe_capability, (FIXTURES["describe_capability"],), "POST", "/control/v2/capability", FIXTURES["describe_capability"]),
            (client.acquire_controller, (FIXTURES["acquire_controller"],), "POST", "/control/v2/controllers/acquire", FIXTURES["acquire_controller"]),
            (client.renew_controller, (FIXTURES["renew_controller"],), "POST", "/control/v2/controllers/renew", FIXTURES["renew_controller"]),
            (client.release_controller, (FIXTURES["release_controller"],), "POST", "/control/v2/controllers/release", FIXTURES["release_controller"]),
            (client.get_controller, (FIXTURES["actor_target"],), "POST", "/control/v2/controllers/get", FIXTURES["actor_target"]),
            (client.submit_action, (FIXTURES["submit_action"],), "POST", "/control/v2/actions/submit", FIXTURES["submit_action"]),
            (client.confirm_action, (FIXTURES["operation_target"],), "POST", "/control/v2/actions/confirm", FIXTURES["operation_target"]),
            (client.get_operation, (FIXTURES["operation_target"],), "POST", "/control/v2/operations/get", FIXTURES["operation_target"]),
            (client.wait_operation, (FIXTURES["wait_operation"],), "POST", "/control/v2/operations/wait", FIXTURES["wait_operation"]),
            (client.get_task_timeline, (FIXTURES["task_timeline"],), "POST", "/control/v2/tasks/timeline/get", FIXTURES["task_timeline"]),
            (client.wait_task_timeline, (FIXTURES["wait_task_timeline"],), "POST", "/control/v2/tasks/timeline/wait", FIXTURES["wait_task_timeline"]),
            (client.cancel_operation, (FIXTURES["operation_target"],), "POST", "/control/v2/operations/cancel", FIXTURES["operation_target"]),
            (client.set_emergency_stop, (FIXTURES["emergency_stop"],), "POST", "/control/v2/emergency-stop", FIXTURES["emergency_stop"]),
            (client.create_task_plan, (PLAN_FIXTURES["create"],), "POST", "/plans/v1/create", PLAN_FIXTURES["create"]),
            (client.get_task_plan, (PLAN_FIXTURES["get"],), "POST", "/plans/v1/get", PLAN_FIXTURES["get"]),
            (client.wait_task_plan, (PLAN_FIXTURES["wait"],), "POST", "/plans/v1/wait", PLAN_FIXTURES["wait"]),
            (client.revise_task_plan, (PLAN_FIXTURES["create"],), "POST", "/plans/v1/revise", PLAN_FIXTURES["create"]),
            (client.set_task_plan_status, (PLAN_FIXTURES["status"],), "POST", "/plans/v1/status", PLAN_FIXTURES["status"]),
            (client.request_task_step_transition, (PLAN_FIXTURES["transition"],), "POST", "/plans/v1/transition", PLAN_FIXTURES["transition"]),
            (client.submit_task_step_action, (FIXTURES["submit_action"],), "POST", "/plans/v1/submit-step-action", FIXTURES["submit_action"]),
        )
        for method, args, expected_method, path, body in cases:
            method(*args)
            request = opener.requests[-1]
            self.assertEqual(request["method"], expected_method)
            self.assertEqual(request["path"], path)
            self.assertEqual(request["authorization"], "Bearer " + TOKEN)
            self.assertEqual(request["body"], body)

    def test_errors_and_local_security_boundary(self):
        self.assertEqual(CONTROL_MAX_RESPONSE_BYTES, 8 * 1024 * 1024)
        with self.assertRaises(RinConfigurationError):
            RinControlClient("http://example.com:7375", token=TOKEN)
        with self.assertRaises(RinConfigurationError):
            RinControlClient(token="short")

        client = RinControlClient(token=TOKEN)
        opener = _Opener()
        opener.status = 409
        opener.value = {"error": "Actor observation changed", "code": "stale"}
        client._opener = opener
        with self.assertRaises(RinAPIError) as caught:
            client.get_actor(FIXTURES["actor_target"])
        self.assertEqual(caught.exception.code, "stale")
        self.assertEqual(caught.exception.status, 409)

        opener.status = 200
        opener.content_type = "text/plain"
        with self.assertRaises(RinProtocolError):
            client.list_worlds()

        opener.status = 302
        opener.content_type = "application/json"
        with self.assertRaises(RinTransportError) as redirected:
            client.list_worlds()
        self.assertEqual(redirected.exception.code, "redirect_rejected")


if __name__ == "__main__":
    unittest.main()
