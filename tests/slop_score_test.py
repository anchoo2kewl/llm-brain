#!/usr/bin/env python3
"""Tests for bin/slop-score: each test builds a throwaway git repo, commits a base and a change, and scores it.

  python3 tests/slop_score_test.py        (no network, no Jev: tier 1 only)
"""
import json, os, subprocess, tempfile, textwrap, unittest
from pathlib import Path

BRAIN = Path(__file__).resolve().parent.parent
SLOP = BRAIN / "bin/slop-score"
ENV = dict(os.environ, BRAIN_OVERLAY="/nonexistent", GIT_AUTHOR_NAME="t", GIT_AUTHOR_EMAIL="t@example.com",
           GIT_COMMITTER_NAME="t", GIT_COMMITTER_EMAIL="t@example.com")

GOOD_BODY = """Retries a failed upload once, because transient S3 errors were failing whole backups.

## How to verify
1. Run `make test`.
2. Upload a file with the network dropped for one second; the backup finishes.
"""


class Repo:
    def __init__(self):
        self.dir = Path(tempfile.mkdtemp(prefix="slop-test-"))
        self.git("init", "-q", "-b", "main")
        self.write("README.md", "# demo\n")
        self.commit("chore: init")
        self.git("branch", "base")

    def git(self, *a):
        return subprocess.run(["git", *a], cwd=self.dir, env=ENV, capture_output=True, text=True, check=True).stdout

    def write(self, path, text):
        p = self.dir / path
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(textwrap.dedent(text))

    def commit(self, msg):
        self.git("add", "-A")
        self.git("commit", "-q", "-m", msg)

    def score(self, title="fix(upload): retry a failed upload once", body=GOOD_BODY, extra=()):
        bf = self.dir.parent / f"{self.dir.name}.body.md"
        bf.write_text(body)
        r = subprocess.run([str(SLOP), "--base", "base", "--title", title, "--body-file", str(bf), "--no-jev", "--json",
                            "--profile", "default", *extra], cwd=self.dir, env=ENV, capture_output=True, text=True)
        self.assert_exit(r)
        return json.loads(r.stdout)

    @staticmethod
    def assert_exit(r):
        if r.returncode not in (0, 1):
            raise AssertionError(f"slop-score failed ({r.returncode}): {r.stderr}")


def ids(res):
    return {s["id"] for s in res["signals"]} | set(res["vetoes"])


class SlopScoreTest(unittest.TestCase):
    def test_clean_change_passes(self):
        r = Repo()
        r.write("src/upload.go", """\
            package upload

            // Upload retries once: S3 returns transient 503s during failover.
            func Upload(c Client, b []byte) error {
            	if err := c.Put(b); err != nil {
            		return c.Put(b)
            	}
            	return nil
            }
            """)
        r.write("src/upload_test.go", "package upload\n\nfunc TestUpload(t *testing.T) { if Upload(fake{}, nil) != nil { t.Fatal(1) } }\n")
        r.commit("fix(upload): retry a failed upload once")
        res = r.score()
        self.assertEqual(res["score"], 0, res["signals"])
        self.assertEqual(res["verdict"], "pass")

    def test_code_slop_is_found(self):
        r = Repo()
        r.write("src/app.ts", """\
            export function load(id: string): any {
              console.log("loading", id);
              // @ts-ignore
              const x = fetchIt(id) as any;
              debugger;
              return x;
            }
            function helperNobodyCalls(a: number) {
              return a + 1;
            }
            """)
        r.write("src/lib.rs", """\
            pub fn parse(s: &str) -> u32 {
                let v: u32 = s.parse().unwrap();
                dbg!(v);
                todo!()
            }

            #[cfg(test)]
            mod tests {
                #[test]
                fn t() { assert_eq!(super::parse("1").checked_add(0).unwrap(), 1); }
            }
            """)
        r.write("pkg/store.go", """\
            package pkg

            func Save(db DB, id string) {
            	q := fmt.Sprintf("SELECT * FROM users WHERE id = '%s'", id)
            	_ = db.Exec(q)
            	if err := db.Ping(); err != nil {
            	}
            }
            """)
        r.write("tools/run.py", """\
            def go():
                try:
                    work()
                except Exception:
                    pass
                # TODO: handle retries
            """)
        r.write("bin/deploy.sh", "#!/usr/bin/env bash\nrm -rf /tmp/x || true\n")
        r.commit("feat(app): load things")
        res = r.score(title="feat(app): load things")
        found = ids(res)
        for want in ["K01", "K02", "K03", "K04", "K05", "K06", "K07", "K08", "K09", "D01", "S02"]:
            self.assertIn(want, found, f"{want} not found in {sorted(found)}")
        ev = {(e["path"], e["line"]) for s in res["signals"] if s["id"] == "K04" for e in s["evidence"]}
        self.assertEqual(ev, {("src/lib.rs", 2)}, "unwrap inside #[cfg(test)] must not count")

    def test_copy_paste_commented_code_and_no_tests(self):
        r = Repo()
        block = "".join(f"    total = total + item.price * {i} + offset_value\n" for i in range(12))
        r.write("src/a.py", "def a(item, offset_value):\n    total = 0\n" + block + "    return total\n\n"
                "def b(item, offset_value):\n    total = 0\n" + block + "    return total\n\n"
                "# old = compute(x)\n# if old > 3:\n#     return old;\n"
                + "".join(f"def g{i}(x):\n    return x * {i}\n\n" for i in range(30)))
        r.commit("feat(a): add pricing")
        found = ids(r.score(title="feat(a): add pricing"))
        for want in ["S01", "D05", "T01"]:
            self.assertIn(want, found, f"{want} not found in {sorted(found)}")

    def test_strings_are_not_comments_or_directives(self):
        r = Repo()
        r.write("tools/gen.py", 'PATTERN = r"@ts-ignore|eslint-disable"\nDOC = """\n# TODO: handle retries\n# old = f(x);\n# y = g(x);\n# return y;\n"""\n')
        r.commit("feat(tools): patterns")
        found = ids(r.score(title="feat(tools): patterns"))
        for bad in ["K02", "D01", "D05"]:
            self.assertNotIn(bad, found)

    def test_secret_is_a_veto(self):
        r = Repo()
        r.write("config/dev.env", "TOKEN=ghp_" + "a1B2c3D4e5F6g7H8i9J0k1L2m3N4o5P6q7R8\n")
        r.commit("chore(config): dev settings")
        res = r.score(title="chore(config): dev settings")
        self.assertIn("C02", res["vetoes"])
        self.assertEqual(res["verdict"], "block")
        self.assertNotIn("ghp_", json.dumps(res), "the secret itself must never be echoed")

    def test_pr_description_slop(self):
        r = Repo()
        r.write("src/a.py", "".join(f"def f{i}(x):\n    return x + {i}\n\n" for i in range(40)))
        r.commit("fix: stuff")
        body = ("Follow-up per §5.4 and AC2 (Vitalii's 194/229). In this session the browser extension was not connected, "
                "so this comprehensive and robust change was not possible to eyeball. Fixes #1. Fixes #2.\n\n" + "word " * 500)
        res = r.score(title="fix(ui): Type chip, full Description, facet labels, unclipped Asset, keyboard, timeline diagnosis (#9)", body=body)
        found = ids(res)
        for want in ["P03", "P10", "P12", "P14", "P15", "P16", "P19", "Z02", "C04"]:
            self.assertIn(want, found, f"{want} not found in {sorted(found)}")

    def test_secrets_never_leave_the_process(self):
        r = Repo()
        token = "ghp_" + "Zx9Yw8Vu7Ts6Rq5Po4Nm3Lk2Ji1Hg0FeDcBa99"
        body_lines = ["MIIEowIBAAKCAQEAsecretkeybodyline%02dAAAAAAAAAAAAAAAAAAAAAAAAAAAA" % i for i in range(3)]
        r.write("src/a.ts", f'export function f() {{\n  console.log("{token}");\n}}\n')
        pem = "-----BEGIN RSA " + "PRIVATE KEY-----"            # split so this file doesn't look like a key itself
        r.write("deploy/key.pem", pem + "\n" + "\n".join(body_lines) + "\n" + pem.replace("BEGIN", "END") + "\n")
        r.commit("fix(a): log")
        res = r.score(title="fix(a): log", body=GOOD_BODY + "\nkey: " + token)
        dump = json.dumps(res)
        self.assertNotIn(token, dump)
        for b in body_lines:
            self.assertNotIn(b, dump)
        self.assertIn("C02", res["vetoes"])
        self.assertEqual(sum(1 for e in res["signals"][0]["evidence"] if e["path"] == "deploy/key.pem"), 5)

    def test_quoted_and_non_utf8_paths(self):
        r = Repo()
        r.write('src/we"ird name.ts', "export function f() {\n  console.log(1);\n}\n")
        (r.dir / "src/latin1.py").write_bytes("# caf\xe9\nx = 1\n".encode("latin-1"))
        r.commit("fix(a): odd files")
        res = r.score(title="fix(a): odd files")
        paths = {e["path"] for s in res["signals"] if s["id"] == "K01" for e in s["evidence"]}
        self.assertEqual(paths, {'src/we"ird name.ts'})

    def test_deleted_files_count_toward_size(self):
        r = Repo()
        r.write("src/big.py", "".join(f"x{i} = {i}\n" for i in range(2000)))
        r.commit("feat: big")
        r.git("branch", "-f", "base")
        (r.dir / "src/big.py").unlink()
        r.commit("refactor: drop big")
        res = r.score(title="refactor(src): drop the old module")
        self.assertIn("Z01", ids(res))

    def test_head_inside_base_is_an_error(self):
        r = Repo()
        bf = r.dir.parent / "b.md"; bf.write_text(GOOD_BODY)
        out = subprocess.run([str(SLOP), "--base", "HEAD", "--title", "x", "--body-file", str(bf), "--no-jev"], cwd=r.dir, env=ENV,
                             capture_output=True, text=True)
        self.assertEqual(out.returncode, 2)
        self.assertIn("nothing to score", out.stderr)

    def test_lockfile_volume_is_not_scored(self):
        r = Repo()
        r.write("package-lock.json", "{\n" + "".join(f'  "pkg{i}": "1.0.{i}",\n' for i in range(3000)) + '  "end": 1\n}\n')
        r.write("src/x.ts", "export const limit = 10;\n")
        r.commit("build(deps): bump the lockfile")
        res = r.score(title="build(deps): bump the lockfile")
        self.assertEqual(res["score"], 0, res["signals"])
        self.assertTrue(any(e["path"] == "package-lock.json" for e in res["assessed"]["excluded"]))

    def test_one_debug_call_in_a_small_pr_cannot_block(self):
        r = Repo()
        r.write("src/x.ts", "export function f() {\n  console.log('x');\n  return 1;\n}\n")
        r.commit("fix(x): return one")
        res = r.score(title="fix(x): return one")
        self.assertIn("K01", ids(res))
        self.assertLess(res["score"], 5)
        self.assertEqual(res["verdict"], "pass")

    def test_waiver(self):
        r = Repo()
        r.write("src/x.ts", "export function f() {\n  // slop:allow K01 the CLI prints its result to stdout\n  console.log('x');\n}\n")
        r.commit("fix(x): print")
        res = r.score(title="fix(x): print")
        self.assertNotIn("K01", ids(res))
        self.assertEqual(res["waived"][0]["signal"], "K01")

    def test_short_waiver_reason_is_ignored(self):
        r = Repo()
        r.write("src/x.ts", "export function f() {\n  console.log('x'); // slop:allow K01 ok\n}\n")
        r.commit("fix(x): print")
        self.assertIn("K01", ids(r.score(title="fix(x): print")))

    def test_conditional_skip_with_reason_is_fine(self):
        r = Repo()
        r.write("e2e/a.e2e.ts", 'test.describe("a", () => {\n  test.skip(\n    !USER,\n    "set E2E_USER to run against a real tenant",\n  );\n});\n')
        r.write("e2e/b.e2e.ts", 'test.skip("opens the drawer", async () => {\n  await go();\n});\n')
        r.commit("test(e2e): gate on creds")
        res = r.score(title="test(e2e): gate on creds")
        ev = [e["path"] for s in res["signals"] if s["id"] == "T03" for e in s["evidence"]]
        self.assertEqual(ev, ["e2e/b.e2e.ts"])

    def test_release_prs_are_skipped(self):
        r = Repo()
        r.write("src/x.ts", "export const a = 1;\n")
        r.commit("release")
        res = r.score(title="Release-candidate 16.10.2026", body="")
        self.assertEqual(res["verdict"], "skipped")

    def test_rules_markdown(self):
        out = subprocess.run([str(SLOP), "rules", "--profile", "default"], env=ENV, capture_output=True, text=True, check=True).stdout
        self.assertIn('| <a name="rule-p12"></a>P12 | Long description |', out)
        self.assertIn("blocks approval", out)

    def test_profiles_validate(self):
        for p in (BRAIN / "slop-profiles").glob("*.json"):
            r = subprocess.run([str(SLOP), "validate", str(p)], env=ENV, capture_output=True, text=True)
            self.assertEqual(r.returncode, 0, f"{p.name}: {r.stdout}{r.stderr}")


if __name__ == "__main__":
    unittest.main(verbosity=1)
