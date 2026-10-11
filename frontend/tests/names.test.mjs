import test from "node:test";
import assert from "node:assert/strict";
import { cleanName, nameIdentity, searchKey } from "../lib/names.ts";

test("names are cleaned like the server does", () => {
  assert.equal(cleanName("  Min.E   (민이)\t"), "Min.E (민이)");
  assert.equal(cleanName("LEEHEE　EXPRESS"), "LEEHEE EXPRESS");
  assert.equal(cleanName("민"), "민");
});

test("identity folds English letters only, like SQLite NOCASE", () => {
  assert.equal(nameIdentity("LeeHee Express"), nameIdentity(" leehee  EXPRESS "));
  // The server keeps these as two people, so the page must not merge them.
  assert.notEqual(nameIdentity("Élise"), nameIdentity("élise"));
  assert.notEqual(nameIdentity("ＡＢ"), nameIdentity("ａｂ"));
});

test("searching ignores the case of every letter", () => {
  assert.ok(searchKey("Élise Studio").includes(searchKey("élise")));
});
