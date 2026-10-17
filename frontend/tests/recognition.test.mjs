import test from "node:test";
import assert from "node:assert/strict";
import { createRecognition, recognizeBatch, recognizeLimit } from "../lib/recognition.ts";

const defer = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const tick = () => new Promise(resolve => setImmediate(resolve));
const ids = n => Array.from({ length: n }, (_, i) => `album-${i}`);

function harness() {
  const calls = [];
  const recognition = createRecognition({
    recognize(albums, signal) { const answer = defer(); calls.push({ albums, signal, ...answer }); return answer.promise; },
  });
  return { recognition, calls, state: () => recognition.getSnapshot() };
}

test("asks 20 albums at a time, one batch after another, at most 100", async () => {
  const { recognition, calls, state } = harness();
  const run = recognition.run([...ids(130), "album-0"]);
  await tick();
  assert.equal(calls.length, 1, "the second batch waits for the first");
  assert.deepEqual(calls[0].albums, ids(recognizeBatch));
  assert.deepEqual({ running: state().running, done: state().done, total: state().total }, { running: true, done: 0, total: recognizeLimit });
  for (let i = 0; i < recognizeLimit / recognizeBatch; i++) {
    calls[i].resolve({ recognized: calls[i].albums.length, skipped: 1, suggestions: [{ album: calls[i].albums[0], author: { name: "Studio", new: false } }] });
    await tick();
    assert.equal(state().done, (i + 1) * recognizeBatch);
  }
  await run;
  assert.equal(calls.length, 5);
  assert.deepEqual(calls.at(-1).albums, ids(100).slice(80));
  assert.equal(state().running, false);
  assert.equal(state().summary, "识别 100 本，给出 5 条建议，跳过 5 条。");
  assert.equal(Object.keys(state().suggestions).length, 5);
});

test("a failed batch keeps the others and says why", async () => {
  const { recognition, calls, state } = harness();
  const run = recognition.run(ids(45));
  await tick();
  calls[0].resolve({ recognized: 20, skipped: 0, suggestions: [{ album: "album-3", models: [{ name: "Min.E", new: false }, { name: "New", new: true }] }] });
  await tick();
  assert.ok(state().suggestions["album-3"], "the first batch shows before the next one ends");
  calls[1].reject(new Error("识别服务 50 秒内没有回答"));
  await tick();
  assert.deepEqual(state().failures, ["第 2 批（20 本）：识别服务 50 秒内没有回答"]);
  calls[2].resolve({ recognized: 5, skipped: 2, suggestions: [{ album: "album-44", author: { name: "X", new: true }, models: [{ name: "Y", new: true }] }, { album: "elsewhere", author: { name: "Z", new: false } }] });
  await run;
  assert.equal(state().summary, "识别 25 本，给出 3 条建议，跳过 2 条，20 本失败。");
  assert.deepEqual(Object.keys(state().suggestions).sort(), ["album-3", "album-44"], "answers about albums outside the batch are ignored");
});

test("a new run replaces old suggestions; dropping a field forgets it", async () => {
  const { recognition, calls, state } = harness();
  let run = recognition.run(["a", "b"]);
  await tick();
  calls[0].resolve({ recognized: 2, skipped: 0, suggestions: [{ album: "a", author: { name: "A", new: false }, models: [{ name: "M", new: false }] }, { album: "b", author: { name: "B", new: false } }] });
  await run;
  recognition.drop("a", "author");
  assert.deepEqual(state().suggestions.a, { models: [{ name: "M", new: false }] });
  recognition.drop("a", "models");
  assert.equal(state().suggestions.a, undefined);
  recognition.drop("a", "models");
  run = recognition.run(["b"]);
  await tick();
  calls[1].resolve({ recognized: 1, skipped: 0, suggestions: [] });
  await run;
  assert.deepEqual(state().suggestions, {}, "the newer answer had nothing for b");
});

test("stopping ignores the answer on the way and sends no more batches", async () => {
  const { recognition, calls, state } = harness();
  const run = recognition.run(ids(40));
  await tick();
  recognition.stop();
  assert.equal(calls[0].signal.aborted, true);
  assert.equal(state().running, false);
  calls[0].resolve({ recognized: 20, skipped: 0, suggestions: [{ album: "album-1", author: { name: "Late", new: false } }] });
  await run;
  await tick();
  assert.equal(calls.length, 1);
  assert.deepEqual(state().suggestions, {});
  assert.equal(state().summary, "");
});

test("an empty list or a second press while running does nothing", async () => {
  const { recognition, calls } = harness();
  await recognition.run([]);
  assert.equal(calls.length, 0);
  const run = recognition.run(["a"]);
  await recognition.run(["b"]);
  await tick();
  assert.equal(calls.length, 1);
  calls[0].resolve({ recognized: 1, skipped: 0, suggestions: [] });
  await run;
});

test("albums wait until their own batch ends, failed or not", async () => {
  const { recognition, calls, state } = harness();
  const run = recognition.run(ids(45));
  await tick();
  assert.deepEqual(state().waiting, ids(45));
  calls[0].resolve({ recognized: 20, skipped: 0, suggestions: [] });
  await tick();
  assert.deepEqual(state().waiting, ids(45).slice(20));
  calls[1].reject(new Error("识别服务暂时不可用"));
  await tick();
  assert.deepEqual(state().waiting, ids(45).slice(40));
  calls[2].resolve({ recognized: 5, skipped: 0, suggestions: [] });
  await run;
  assert.deepEqual(state().waiting, []);

  const again = recognition.run(ids(3));
  await tick();
  recognition.stop();
  assert.deepEqual(state().waiting, [], "a stopped run leaves nothing waiting");
  calls[3].resolve({ recognized: 3, skipped: 0, suggestions: [] });
  await again;
});
