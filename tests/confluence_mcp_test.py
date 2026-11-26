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


class MixedContentTest(unittest.TestCase):
    def test_external_images_and_file_links_do_not_shift_the_order(self):
        storage = ('<ac:image><ri:url ri:value="https://x.example/a.png" /></ac:image>'
                   '<ac:link><ri:attachment ri:filename="spec.pdf" /></ac:link>'
                   '<ac:image ac:alt="Architecture"><ri:attachment ri:filename="a&amp;b.png" /></ac:image>')
        doc = {"type": "doc", "content": [
            {"type": "media", "attrs": {"type": "external", "url": "https://x.example/a.png"}},
            {"type": "media", "attrs": {"id": "UNKNOWN_MEDIA_ID", "type": "file", "alt": "Architecture"}}]}
        fixed = m.relink_media(doc, storage, {"spec.pdf": "p1", "a&b.png": "f9"}, "7")
        self.assertEqual(fixed, ["a&b.png"])
        self.assertEqual(doc["content"][0]["attrs"], {"type": "external", "url": "https://x.example/a.png"})
        self.assertEqual(doc["content"][1]["attrs"]["id"], "f9")
        self.assertEqual(doc["content"][1]["attrs"]["alt"], "Architecture")  # the caption stays


class FlowTest(unittest.TestCase):
    def test_create_with_attachments_returns_the_version_after_repair(self):
        with tempfile.TemporaryDirectory() as d, patch.dict(os.environ, dict(ENV, CONFLUENCE_ATTACH_DIRS=d)):
            shot = os.path.join(d, "shot.png")
            Path(shot).write_bytes(b"\x89PNG")
            created = {"id": "9", "title": "T", "version": {"number": 1}, "_links": {"webui": "/p/9"}}
            with patch.object(m, "call", return_value=created), \
                    patch.object(m, "fix_images", return_value={"fixed": ["shot.png"], "version": 2}) as fix:
                out = m.create_page("1", "T", '<ac:image><ri:attachment ri:filename="shot.png" /></ac:image>',
                                    attachments=[shot])
        fix.assert_called_once_with("9")
        self.assertEqual(out["version"], 2)
        self.assertEqual(out["attached"], ["shot.png"])

    def test_bad_attachment_fails_before_the_page_exists(self):
        with patch.object(m, "call") as call, self.assertRaises(m.ToolError):
            m.create_page("1", "T", "<p/>", attachments=["/etc/hosts"])
        call.assert_not_called()

    def test_quotes_in_file_name_are_refused(self):
        with tempfile.TemporaryDirectory() as d, patch.dict(os.environ, {"CONFLUENCE_ATTACH_DIRS": d}):
            bad = os.path.join(d, 'a"b.png')
            Path(bad).write_bytes(b"x")
            with self.assertRaises(m.ToolError):
                m.check_file(bad)


class TransportTest(unittest.TestCase):
    def test_dots_in_a_query_are_allowed(self):
        with patch.dict(os.environ, ENV), patch.object(m.urllib.request, "build_opener") as opener:
            opener.return_value.open.return_value.__enter__.return_value.read.return_value = b'{"results": []}'
            self.assertEqual(m.search('text ~ "v1..v2"'), [])

    def test_timeout_becomes_a_tool_error(self):
        with patch.dict(os.environ, ENV), patch.object(m.urllib.request, "build_opener") as opener:
            opener.return_value.open.side_effect = TimeoutError()
            with self.assertRaises(m.ToolError):
                m.whoami()

    def test_null_search_fields_do_not_crash(self):
        with patch.object(m, "call", return_value={"results": [{"content": None, "resultGlobalContainer": None}]}):
            self.assertEqual(m.search("x")[0]["id"], None)


class ProtocolTest(unittest.TestCase):
    def test_bad_params_and_arguments_are_errors(self):
        with self.assertRaises(ValueError):
            m.respond({"method": "tools/call", "params": []})
        out = m.respond({"method": "tools/call", "params": {"name": "confluence_whoami", "arguments": []}})
        self.assertTrue(out["isError"])

    def test_server_keeps_answering_after_an_unexpected_error(self):
        lines = [json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/list"}),
                 json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}),
                 json.dumps({"jsonrpc": "2.0", "id": 2, "method": "ping"})]
        printed = []
        with patch.object(m, "respond", side_effect=[RuntimeError("boom"), {}]), \
                patch.object(m.sys, "stdin", lines), patch("builtins.print", lambda s, **k: printed.append(s)):
            m.serve()
        self.assertEqual([json.loads(x)["id"] for x in printed], [1, 2])
        self.assertEqual(json.loads(printed[0])["error"]["code"], -32603)

    def test_unicode_digits_are_not_a_page_id(self):
        with self.assertRaises(m.ToolError):
            m.page_id("\u0661\u0662")


class CommentTest(unittest.TestCase):
    def test_comment_text_is_escaped(self):
        with patch.object(m, "call", return_value={"id": "c1"}) as call:
            m.add_comment("5", "<script>x</script> & y")
        sent = call.call_args[0][2]["body"]["value"]
        self.assertEqual(sent, "<p>&lt;script&gt;x&lt;/script&gt; &amp; y</p>")


if __name__ == "__main__":
    unittest.main()
