import tempfile
import unittest
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
from source_inventory import (
    build_inventory,
    extract_models,
    extract_routes,
    extract_socket_events,
)


class SourceInventoryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        (self.root / "server" / "routers").mkdir(parents=True)
        (self.root / "server" / "models").mkdir(parents=True)

    def tearDown(self):
        self.temp.cleanup()

    def write(self, relative, content):
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        return path

    def test_routes_capture_literal_multiline_and_unresolved_syntax(self):
        source = """\
const express = require('express')
const router = express()
router.get('/books/:id', auth, controller.show)
this.router.patch(
  `/users/${userType}`,
  middleware.one,
  middleware.two,
  handler
)
router.get(/^\\/internal/, handler)
router.post(pathFromConfig, handler)
"""
        self.write("server/routers/ApiRouter.js", source)

        routes, hashes = extract_routes(self.root)

        self.assertEqual([r["method"] for r in routes], ["GET", "PATCH", "GET", "POST"])
        self.assertEqual(routes[0]["route"], "/books/:id")
        self.assertEqual(routes[0]["route_kind"], "string_literal")
        self.assertEqual(routes[0]["line"], 3)
        self.assertEqual(routes[1]["route_kind"], "template_expression")
        self.assertEqual(routes[2]["route_kind"], "regex_literal")
        self.assertEqual(routes[3]["route_kind"], "dynamic_expression")
        self.assertFalse(routes[0]["middleware_behavior_resolved"])
        self.assertEqual(routes[0]["prefix_context"]["known_mount"], "/api")
        self.assertIn("server/routers/ApiRouter.js", hashes)

    def test_socket_registrations_and_emissions_keep_dynamic_names(self):
        self.write(
            "server/SocketAuthority.js",
            """\
socket.on('auth', handler)
socket.on(eventName, handler)
client.socket.emit('init', payload)
io.emit(`room_${roomId}`, payload)
""",
        )
        events, hashes = extract_socket_events(self.root)

        self.assertEqual([e["direction"] for e in events], ["incoming_registration", "incoming_registration", "emission", "emission"])
        self.assertEqual(events[0]["event"], "auth")
        self.assertEqual(events[0]["event_kind"], "string_literal")
        self.assertEqual(events[1]["event_kind"], "dynamic_expression")
        self.assertEqual(events[3]["event_kind"], "template_expression")
        self.assertFalse(events[0]["scope_resolved"])
        self.assertIn("server/SocketAuthority.js", hashes)

    def test_socket_authority_wrapper_signatures_select_the_event_argument(self):
        self.write(
            "server/managers/SocketCalls.js",
            """\
SocketAuthority.emitter('global_update', makePayload(a, b))
SocketAuthority.clientEmitter(userId, 'private_update', { nested: [1, 2] })
SocketAuthority.adminEmitter(eventName, payload)
SocketAuthority.libraryItemEmitter(`item_${kind}`, item)
SocketAuthority.libraryItemsEmitter('items_updated', items)
""",
        )
        self.write(
            "server/SocketAuthority.js",
            "this.clientEmitter(user.id, 'nested_private_update', payload)\n",
        )
        events, _ = extract_socket_events(self.root)
        wrapper_events = [event for event in events if event["direction"] == "emission_via_wrapper"]

        self.assertEqual(
            [(event["wrapper"], event["event"]) for event in wrapper_events],
            [
                ("clientEmitter", "nested_private_update"),
                ("emitter", "global_update"),
                ("clientEmitter", "private_update"),
                ("adminEmitter", "eventName"),
                ("libraryItemEmitter", "`item_${kind}`"),
                ("libraryItemsEmitter", "items_updated"),
            ],
        )
        self.assertEqual(wrapper_events[0]["wrapper_event_argument_index"], 1)
        self.assertEqual(wrapper_events[0]["scope_classification"], "specific_user_clients")
        self.assertEqual(wrapper_events[3]["event_kind"], "dynamic_expression")
        self.assertEqual(wrapper_events[4]["event_kind"], "template_expression")
        self.assertFalse(wrapper_events[0]["scope_resolved"])

    def test_models_are_filenames_with_content_hashes(self):
        self.write("server/models/Book.js", "module.exports = Book\n")
        self.write("server/models/User.js", "module.exports = User\n")
        models, hashes = extract_models(self.root)
        self.assertEqual([model["filename"] for model in models], ["Book.js", "User.js"])
        self.assertEqual(models[0]["source"], "server/models/Book.js")
        self.assertEqual(len(hashes["server/models/Book.js"]["sha256"]), 64)

    def test_complete_inventory_is_machine_readable_and_labeled(self):
        self.write("server/routers/PublicRouter.js", "router.get('/share/:slug', handler)\n")
        self.write("server/models/Book.js", "// model\n")
        inventory = build_inventory(self.root)
        self.assertEqual(inventory["counts"]["routes"], 1)
        self.assertEqual(inventory["counts"]["model_files"], 1)
        self.assertEqual(inventory["inventory_kind"], "conservative_source_syntax_inventory")
        self.assertTrue(any("not a complete API" in item for item in inventory["limitations"]))


if __name__ == "__main__":
    unittest.main()
