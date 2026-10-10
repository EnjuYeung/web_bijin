import test from "node:test";
import assert from "node:assert/strict";
import { createAlbumOrganizer, OrganizeRejected } from "../lib/album-organizer.ts";

const defer = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const tick = () => new Promise(resolve => setImmediate(resolve));
const refs = { A: { id: 1, name: "A" }, B: { id: 2, name: "B" }, C: { id: 3, name: "C" } };
const authors = [{ id: 10, name: "Studio A", albums: 0 }, { id: 11, name: "Studio B", albums: 0 }];
const people = (models = Object.values(refs)) => ({ authors, models: models.map(ref => ({ ...ref, albums: 0 })), stale: 0 });
const album = (id, models = [], author = null) => ({ id, name: id, models, author, cover: {}, added: 1, addedDate: "2026-01-01", count: 1 });

async function harness(initial = { albums: [album("a"), album("b")], people: people() }) {
  const writes = [], confirmations = [];
  let read = async () => structuredClone(initial);
  let counter = 0;
  const organizer = createAlbumOrganizer({
    read: () => read(),
    write(change) { const completion = defer(); writes.push({ change: structuredClone(change), ...completion }); return completion.promise; },
    confirm(id) { const completion = defer(); confirmations.push({ id, ...completion }); return completion.promise; },
  }, () => `operation-${++counter}`);
  await organizer.load();
  const answer = (index, albums, models = Object.values(refs), extra = {}) => writes[index].resolve({ operationId: writes[index].change.operationId, albums, people: people(models), ...extra });
  const models = id => organizer.getSnapshot().albums.find(item => item.id === id).models.map(ref => ref.name);
  return { organizer, writes, confirmations, answer, models, setRead: value => { read = value; } };
}

test("continuous A/B/C selections stay visible through old responses and save only at the end", async () => {
  const h = await harness();
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["A"] });
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["A", "B"] });
  assert.deepEqual(h.models("a"), ["A", "B"]);
  assert.equal(h.writes.length, 1);
  h.answer(0, [album("a", [refs.A]), album("b")]); await tick();
  assert.deepEqual(h.models("a"), ["A", "B"]);
  assert.equal(h.organizer.getSnapshot().rows.a.kind, "saving");
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["A", "B", "C"] });
  h.answer(1, [album("a", [refs.A, refs.B]), album("b")]); await tick();
  assert.deepEqual(h.models("a"), ["A", "B", "C"]);
  assert.equal(h.organizer.getSnapshot().rows.a.kind, "saving");
  h.answer(2, [album("a", [refs.A, refs.B, refs.C]), album("b")]); await tick();
  assert.deepEqual(h.writes.map(write => write.change.models), [["A"], ["A", "B"], ["A", "B", "C"]]);
  assert.equal(h.organizer.getSnapshot().rows.a.kind, "saved");
  assert.equal(h.organizer.getSnapshot().hasPending, false);
});

test("batch and row modifications share order, targets are captured, unrelated rows finish independently", async () => {
  const h = await harness();
  const targets = ["a", "b"];
  h.organizer.submit({ kind: "assign", albums: targets, author: "Studio A", batch: true });
  targets.pop();
  h.organizer.submit({ kind: "assign", albums: ["a"], author: "Studio B" });
  assert.equal(h.organizer.getSnapshot().albums[0].author.name, "Studio B");
  assert.equal(h.writes.length, 1);
  h.answer(0, [album("a", [], authors[0]), album("b", [], authors[0])]); await tick();
  assert.deepEqual(h.writes[0].change.albums, ["a", "b"]);
  assert.equal(h.writes[1].change.author, "Studio B");
  assert.equal(h.organizer.getSnapshot().rows.b.kind, "saved");
  assert.equal(h.organizer.getSnapshot().rows.a.kind, "saving");
  h.answer(1, [album("a", [], authors[1]), album("b", [], authors[0])]); await tick();
  assert.equal(h.organizer.getSnapshot().albums[0].author.name, "Studio B");
});

test("rename is immediate; following selections use the new name and every operation stays in order", async () => {
  const h = await harness({ albums: [album("a", [refs.A]), album("b")], people: people() });
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["A", "B"] });
  h.organizer.submit({ kind: "rename", role: "model", personId: 1, name: "Renamed" });
  h.organizer.submit({ kind: "assign", albums: ["b"], models: ["A"] }); // a previously prepared selection
  assert.deepEqual(h.models("a"), ["Renamed", "B"]);
  assert.deepEqual(h.models("b"), ["Renamed"]);
  h.answer(0, [album("a", [refs.A, refs.B]), album("b")]); await tick();
  assert.equal(h.writes[1].change.kind, "rename");
  const renamed = { id: 1, name: "Renamed" };
  h.answer(1, [album("a", [renamed, refs.B]), album("b")], [renamed, refs.B, refs.C], { person: renamed }); await tick();
  assert.deepEqual(h.writes[2].change.models, ["Renamed"]);
  h.answer(2, [album("a", [renamed, refs.B]), album("b", [renamed])], [renamed, refs.B, refs.C]); await tick();
  assert.deepEqual(h.models("b"), ["Renamed"]);
});

test("merge keeps the target position and pending selections cannot duplicate or revive the source", async () => {
  const h = await harness({ albums: [album("a", [refs.A, refs.C, refs.B]), album("b")], people: people() });
  h.organizer.submit({ kind: "rename", role: "model", personId: 1, name: "B" });
  assert.deepEqual(h.models("a"), ["C", "B"]);
  h.organizer.submit({ kind: "assign", albums: ["b"], models: ["A", "B"] });
  assert.deepEqual(h.models("b"), ["B"]);
  h.answer(0, [album("a", [refs.C, refs.B]), album("b")], [refs.B, refs.C], { person: refs.B, merged: true }); await tick();
  assert.deepEqual(h.writes[1].change.models, ["B"]);
  h.answer(1, [album("a", [refs.C, refs.B]), album("b", [refs.B])], [refs.B, refs.C]); await tick();
  assert.deepEqual(h.models("b"), ["B"]);
});

test("delete is immediate and an old prepared batch choice does not re-create the name", async () => {
  const h = await harness({ albums: [album("a", [refs.A]), album("b")], people: people() });
  h.organizer.submit({ kind: "remove", role: "model", personId: 1 });
  h.organizer.submit({ kind: "assign", albums: ["b"], models: ["A", "B"], batch: true });
  assert.deepEqual(h.models("a"), []);
  assert.deepEqual(h.models("b"), ["B"]);
  h.answer(0, [album("a"), album("b")], [refs.B, refs.C]); await tick();
  assert.deepEqual(h.writes[1].change.models, ["B"]);
  h.answer(1, [album("a"), album("b", [refs.B])], [refs.B, refs.C]); await tick();
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["A"] });
  assert.deepEqual(h.writes[2].change.models, []);
});

test("a newly typed person's local identity supports rename and following selection before its save answers", async () => {
  const h = await harness({ albums: [album("a"), album("b")], people: people([]) });
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["New"] });
  const id = h.organizer.getSnapshot().people.models[0].id;
  h.organizer.submit({ kind: "rename", role: "model", personId: id, name: "Changed" });
  h.organizer.submit({ kind: "assign", albums: ["b"], models: ["Changed"] });
  assert.deepEqual(h.models("a"), ["Changed"]);
  const created = { id: 50, name: "New" }, renamed = { id: 50, name: "Changed" };
  h.answer(0, [album("a", [created]), album("b")], [created]); await tick();
  assert.equal(h.writes[1].change.personId, 50);
  h.answer(1, [album("a", [renamed]), album("b")], [renamed], { person: renamed }); await tick();
  assert.deepEqual(h.writes[2].change.models, ["Changed"]);
  h.answer(2, [album("a", [renamed]), album("b", [renamed])], [renamed]); await tick();
  assert.deepEqual(h.models("b"), ["Changed"]);
});

test("lost write answer pauses later sends; a committed receipt preserves the latest preview and resumes", async () => {
  const h = await harness();
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["A"] });
  h.writes[0].reject(new Error("connection lost")); await tick();
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["A", "B"] });
  assert.equal(h.writes.length, 1);
  assert.deepEqual(h.models("a"), ["A", "B"]);
  assert.equal(h.organizer.getSnapshot().rows.a.kind, "uncertain");
  h.confirmations[0].resolve({ state: "committed", result: { operationId: h.writes[0].change.operationId, albums: [album("a", [refs.A]), album("b")], people: people() } }); await tick();
  assert.equal(h.writes.length, 2);
  assert.deepEqual(h.models("a"), ["A", "B"]);
  h.answer(1, [album("a", [refs.A, refs.B]), album("b")]); await tick();
  assert.equal(h.organizer.getSnapshot().hasPending, false);
});

test("a missing receipt requires retry with the original ID and frozen content", async () => {
  const h = await harness();
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["A"] });
  h.writes[0].reject(new Error("lost")); await tick();
  h.confirmations[0].resolve({ state: "missing" }); await tick();
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["A", "B"] });
  assert.equal(h.writes.length, 1);
  assert.equal(h.organizer.getSnapshot().uncertain.canRetry, true);
  h.organizer.retry();
  assert.deepEqual(h.writes[1].change, h.writes[0].change);
  h.answer(1, [album("a", [refs.A]), album("b")]); await tick();
  assert.equal(h.writes.length, 3);
  assert.deepEqual(h.writes[2].change.models, ["A", "B"]);
  h.answer(2, [album("a", [refs.A, refs.B]), album("b")]); await tick();
  assert.equal(h.organizer.getSnapshot().hasPending, false);
});

test("failed confirmation and expired receipts keep changes and prevent blind retries", async () => {
  const h = await harness();
  h.organizer.submit({ kind: "rename", role: "model", personId: 1, name: "Renamed" });
  h.writes[0].reject(new Error("lost")); await tick();
  h.confirmations[0].reject(new Error("offline")); await tick();
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["Renamed"] });
  const verification = h.organizer.verify();
  h.confirmations[1].resolve({ state: "expired" }); await verification;
  h.organizer.retry();
  assert.equal(h.writes.length, 1);
  assert.deepEqual(h.models("a"), ["Renamed"]);
  assert.equal(h.organizer.getSnapshot().hasPending, true);
  assert.equal(h.organizer.getSnapshot().uncertain.canRetry, false);
});

test("explicit rejection withdraws the invalid item, preserves the newer addition and reports the reason", async () => {
  const h = await harness({ albums: [album("a", [refs.A]), album("b")], people: people() });
  const bad = "名".repeat(41);
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["A", bad] });
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["A", bad, "C"] });
  h.writes[0].reject(new OrganizeRejected("名字最多 40 个字")); await tick();
  assert.deepEqual(h.models("a"), ["A", "C"]);
  assert.deepEqual(h.writes[1].change.models, ["A", "C"]);
  assert.match(h.organizer.getSnapshot().rejection, /40/);
  h.answer(1, [album("a", [refs.A, refs.C]), album("b")]); await tick();
  assert.equal(h.organizer.getSnapshot().rows.a.kind, "saved");
  assert.equal(h.confirmations.length, 0);
});

test("a rejected rename restores that identity while retaining a newer selection", async () => {
  const h = await harness({ albums: [album("a", [refs.A]), album("b")], people: people() });
  h.organizer.submit({ kind: "rename", role: "model", personId: 1, name: "名".repeat(41) });
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["名".repeat(41), "B"] });
  h.writes[0].reject(new OrganizeRejected("名字最多 40 个字")); await tick();
  assert.deepEqual(h.models("a"), ["A", "B"]);
  assert.deepEqual(h.writes[1].change.models, ["A", "B"]);
});

test("an earlier read cannot replace facts confirmed by a later save", async () => {
  const h = await harness();
  const oldRead = defer(); h.setRead(() => oldRead.promise);
  const loading = h.organizer.load();
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["C"] });
  h.answer(0, [album("a", [refs.C]), album("b")]); await tick();
  oldRead.resolve({ albums: [album("a"), album("b")], people: people() }); await loading;
  assert.deepEqual(h.models("a"), ["C"]);
});

test("stale cleanup shares ordering and stopping the page does not send unsent changes", async () => {
  const h = await harness();
  h.organizer.submit({ kind: "cleanStale" });
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["A"] });
  assert.equal(h.writes.length, 1);
  h.organizer.stop();
  h.answer(0, [album("a"), album("b")], Object.values(refs), { removed: 2 }); await tick();
  assert.equal(h.writes.length, 1);
});

test("Unicode whitespace and composition bind a typed name to the saved identity", async () => {
  const h = await harness({ albums: [album("a"), album("b")], people: people([]) });
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["  \u1106\u1175\u11ab\u0085Name  "] });
  assert.deepEqual(h.writes[0].change.models, ["민 Name"]);
  const id = h.organizer.getSnapshot().people.models[0].id;
  h.organizer.submit({ kind: "rename", role: "model", personId: id, name: "Changed" });
  const saved = { id: 40, name: "민 Name" };
  h.answer(0, [album("a", [saved]), album("b")], [saved]); await tick();
  assert.equal(h.writes[1].change.personId, 40);
});

test("identity folding matches ASCII NOCASE without merging distinct Unicode names", async () => {
  const upper = { id: 4, name: "Å" }, lower = { id: 5, name: "å" };
  const h = await harness({ albums: [album("a"), album("b")], people: people([refs.A, upper, lower]) });
  h.organizer.submit({ kind: "assign", albums: ["a"], models: ["a", "A", "Å", "å"] });
  assert.deepEqual(h.writes[0].change.models, ["A", "Å", "å"]);
  assert.deepEqual(h.models("a"), ["A", "Å", "å"]);
});
