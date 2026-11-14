import test from "node:test";
import assert from "node:assert/strict";
import { createRequire } from "node:module";

const { relTime, baseName } = createRequire(import.meta.url)("../ui/relative.js");
const now = Date.parse("2026-11-13T10:00:00Z");

test("relTime", () => {
  assert.equal(relTime("2026-11-13T09:59:50Z", now), "just now");
  assert.equal(relTime("2026-11-13T09:48:00Z", now), "12m ago");
  assert.equal(relTime("2026-11-13T07:00:00Z", now), "3h ago");
  assert.equal(relTime("2026-11-10T10:00:00Z", now), "3d ago");
  assert.equal(relTime("2026-10-23T10:00:00Z", now), "3w ago");
  assert.equal(relTime("2026-08-13T10:00:00Z", now), "3mo ago");
  assert.equal(relTime("2024-11-13T10:00:00Z", now), "2y ago");
  assert.equal(relTime("2026-11-13T10:05:00Z", now), "just now", "clock skew never shows a negative time");
  assert.equal(relTime("not a date", now), "");
  assert.equal(relTime("", now), "");
});

test("baseName", () => {
  assert.equal(baseName("/Users/x/projects/app"), "app");
  assert.equal(baseName("/Users/x/projects/app/"), "app");
  assert.equal(baseName("app"), "app");
  assert.equal(baseName(""), "");
  assert.equal(baseName(undefined), "");
});
