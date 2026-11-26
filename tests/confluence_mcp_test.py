import importlib.machinery
import importlib.util
import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

loader = importlib.machinery.SourceFileLoader("confluence_mcp", "hive/skills/confluence/confluence-mcp")
spec = importlib.util.spec_from_loader(loader.name, loader)
m = importlib.util.module_from_spec(spec)
loader.exec_module(m)

ENV = {"CONFLUENCE_URL": "https://example.atlassian.net/wiki", "CONFLUENCE_EMAIL": "a@example.com",
       "CONFLUENCE_TOKEN": "t"}


class ConfigTest(unittest.TestCase):
    def test_missing_config_fails_before_network(self):
        with patch.dict(os.environ, {}, clear=True), self.assertRaises(m.ToolError):
            m.call("GET", "/rest/api/user/current")

    def test_plain_http_site_is_refused(self):
        with patch.dict(os.environ, dict(ENV, CONFLUENCE_URL="http://example.atlassian.net/wiki")):
            with self.assertRaises(m.ToolError):
                m.config()

    def test_paths_stay_on_the_site(self):
        with patch.dict(os.environ, ENV):
            for path in ("https://evil.example/x", "//evil.example/x", "/api/../../x"):
                with self.subTest(path=path), self.assertRaises(m.ToolError):
                    m.call("GET", path)


class McpTest(unittest.TestCase):
    def test_tools_list_has_closed_schemas(self):
        tools = m.respond({"method": "tools/list"})["tools"]
        self.assertIn("confluence_update_page", [t["name"] for t in tools])
        for t in tools:
            self.assertFalse(t["inputSchema"]["additionalProperties"])
            for r in t["inputSchema"]["required"]:
                self.assertIn(r, t["inputSchema"]["properties"])

    def test_unknown_argument_is_an_error_not_a_call(self):
        with patch.object(m, "call") as call:
            out = m.respond({"method": "tools/call", "params": {"name": "confluence_get_page",
                                                                "arguments": {"page_id": "1", "path": "/x"}}})
        self.assertTrue(out["isError"])
        call.assert_not_called()

    def test_tool_error_is_reported_to_the_client(self):
        with patch.dict(os.environ, {}, clear=True):
            out = m.respond({"method": "tools/call", "params": {"name": "confluence_whoami", "arguments": {}}})
        self.assertTrue(out["isError"])
        self.assertIn("CONFLUENCE_URL", out["content"][0]["text"])

    def test_page_id_must_be_numeric(self):
        with self.assertRaises(m.ToolError):
            m.page_id("1/../2")


class UpdateTest(unittest.TestCase):
    def test_stale_version_is_refused(self):
        with patch.object(m, "call", return_value={"version": {"number": 5}, "title": "T"}) as call:
            with self.assertRaises(m.ToolError):
                m.update_page("12", 4, "<p>x</p>")
        self.assertEqual(call.call_count, 1)  # read only, no write


class AttachTest(unittest.TestCase):
    def test_only_allowed_folders_and_types(self):
        with tempfile.TemporaryDirectory() as d:
            good, script, hidden = os.path.join(d, "shot.png"), os.path.join(d, "x.sh"), os.path.join(d, ".h.png")
            for p in (good, script, hidden):
                Path(p).write_bytes(b"\x89PNG")
            with patch.dict(os.environ, {"CONFLUENCE_ATTACH_DIRS": d}):
                self.assertEqual(m.check_file(good)[1], "image/png")
                for bad in (script, hidden, os.path.expanduser("~/.ssh/id_ed25519"), "/etc/hosts"):
                    with self.subTest(bad=bad), self.assertRaises(m.ToolError):
                        m.check_file(bad)

    def test_symlink_out_of_folder_is_refused(self):
        with tempfile.TemporaryDirectory() as d, tempfile.TemporaryDirectory() as other:
            target = os.path.join(other, "secret.txt")
            Path(target).write_text("s")
            link = os.path.join(d, "link.txt")
            os.symlink(target, link)
            with patch.dict(os.environ, {"CONFLUENCE_ATTACH_DIRS": d}), self.assertRaises(m.ToolError):
                m.check_file(link)


class RelinkTest(unittest.TestCase):
    STORAGE = ('<p>a</p><ac:image><ri:attachment ri:filename="one.jpg" /></ac:image>'
               '<ac:image><ri:attachment ri:filename="two.jpg" /></ac:image>')

    def media(self, **attrs):
        return {"type": "mediaSingle", "content": [{"type": "media", "attrs": attrs}]}

    def test_broken_nodes_get_their_file_ids_in_page_order(self):
        doc = {"type": "doc", "content": [self.media(id="UNKNOWN_MEDIA_ID", collection="", type="file",
                                                     __fileMimeType="application/octet-stream"),
                                          self.media(id="UNKNOWN_MEDIA_ID", collection="", type="file")]}
        fixed = m.relink_media(doc, self.STORAGE, {"one.jpg": "f1", "two.jpg": "f2"}, "99")
        self.assertEqual(fixed, ["one.jpg", "two.jpg"])
        a1, a2 = (n["content"][0]["attrs"] for n in doc["content"])
        self.assertEqual((a1["id"], a1["collection"], a1["alt"]), ("f1", "contentId-99", "one.jpg"))
        self.assertNotIn("__fileMimeType", a1)
        self.assertEqual(a2["id"], "f2")

    def test_good_node_keeps_its_place_in_the_order(self):
        doc = {"type": "doc", "content": [self.media(id="f1", collection="contentId-99", type="file"),
                                          self.media(id="UNKNOWN_MEDIA_ID", collection="", type="file")]}
        self.assertEqual(m.relink_media(doc, self.STORAGE, {"one.jpg": "f1", "two.jpg": "f2"}, "99"), ["two.jpg"])
        self.assertEqual(doc["content"][0]["content"][0]["attrs"]["id"], "f1")

    def test_missing_attachment_is_left_alone(self):
        doc = {"type": "doc", "content": [self.media(id="UNKNOWN_MEDIA_ID", collection="", type="file")]}
        self.assertEqual(m.relink_media(doc, self.STORAGE, {}, "99"), [])
        self.assertEqual(doc["content"][0]["content"][0]["attrs"]["id"], "UNKNOWN_MEDIA_ID")


class CommentTest(unittest.TestCase):
    def test_comment_text_is_escaped(self):
        with patch.object(m, "call", return_value={"id": "c1"}) as call:
            m.add_comment("5", "<script>x</script> & y")
        sent = call.call_args[0][2]["body"]["value"]
        self.assertEqual(sent, "<p>&lt;script&gt;x&lt;/script&gt; &amp; y</p>")


if __name__ == "__main__":
    unittest.main()
