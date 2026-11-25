import test from "node:test";
import assert from "node:assert/strict";
import { createRequire } from "node:module";

const { changed } = createRequire(import.meta.url)("../hub/internal/web/static/reloadsign.js");

test("reload sign: only a known first version that changes", () => {
  assert.equal(changed("0.1.12", "0.1.13"), "0.1.13");
  assert.equal(changed("0.1.12", "0.1.12"), null);
  assert.equal(changed(null, "0.1.13"), null, "no first version yet");
  assert.equal(changed("0.1.12", undefined), null, "a failed or old reply");
  assert.equal(changed("0.1.12", ""), null);
});
