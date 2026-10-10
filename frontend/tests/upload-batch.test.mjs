import { test } from "node:test";
import assert from "node:assert/strict";
import { createUploadBatch, UploadExpired, UploadRejected } from "../lib/upload-batch.ts";

const photo = (name = "a.jpg", size = 8) => ({ path: name, file: new File([new Uint8Array(size)], name, { type: "image/jpeg" }) });
const selection = (...items) => ({ items, skippedFiles: 0, skippedFolders: 0 });
const later = () => { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; };
const ticks = async () => { for (let n = 0; n < 12; n++) await new Promise(resolve => setImmediate(resolve)); };
const idle = async batch => { for (let n = 0; n < 100 && batch.getSnapshot().running; n++) await ticks(); assert.equal(batch.getSnapshot().running, false, "batch did not settle"); };
const task = (id, extra = {}) => ({ id, target: "local", path: "a.jpg", phase: "waiting", saved: false, retryable: true, transferRetryable: true, ...extra });
const done = id => task(id, { phase: "done", saved: true, transferRetryable: false });
function rig(overrides = {}, destination = { target: "local", directory: "旅行" }) {
  let clock = 0, next = 0, row = 0;
  const calls = { prepare: [], transfer: [], status: [], confirm: [], verify: [], retryProcessing: [] };
  const base = {
    prepare: async input => ({ task: task("task-" + ++next, { target: input.target, path: input.path }), url: "/put", method: "PUT", headers: {} }),
    transfer: async plan => done(plan.task.id),
    status: async id => done(id), confirm: async id => done(id),
    verify: async id => ({ state: "saved", task: done(id) }), retryProcessing: async id => done(id),
    now: () => clock, wait: async (_signal, ms) => { clock += ms; }, ...overrides,
  };
  const adapter = Object.fromEntries(Object.entries(base).map(([name, fn]) => [name, (...args) => { calls[name]?.push(args); return fn(...args); }]));
  const batch = createUploadBatch({ adapter, limits: { maxBytes: 50_000_000, maxFiles: 10_000 }, initialDestination: destination, newID: () => "row-" + ++row });
  return { batch, calls, advance: ms => { clock += ms; } };
}
async function add(batch, ...names) { await batch.select(() => selection(...names.map(name => photo(name)))); }

test("leaving holds recovery until staying releases it, without repeating transmission", async () => {
  const transfer = later();
  const { batch, calls } = rig({ transfer: () => transfer.promise });
  await add(batch, "a.jpg"); batch.start(); await ticks();
  const stay = batch.hold(); transfer.reject(new Error("browser cancelled transport")); await ticks();
  assert.equal(calls.verify.length, 0); assert.equal(batch.getSnapshot().running, true);
  stay(); await idle(batch);
  assert.equal(batch.getSnapshot().ready, 1); assert.equal(calls.verify.length, 1);
  assert.equal(calls.prepare.length, 1); assert.equal(calls.transfer.length, 1);
});
test("accepted leaving cancels held recovery and never schedules the fifth image", async () => {
  const transfers = [];
  const { batch, calls } = rig({ transfer: () => { const gate = later(); transfers.push(gate); return gate.promise; } });
  await add(batch, "1.jpg", "2.jpg", "3.jpg", "4.jpg", "5.jpg");
  const first = batch.hold(); batch.start(); await ticks(); assert.equal(calls.prepare.length, 0);
  first(); await ticks(); assert.equal(calls.transfer.length, 4);
  const leave = batch.hold(); transfers.forEach(gate => gate.reject(new Error("leaving"))); await ticks();
  batch.stop(); leave(); await ticks();
  assert.equal(batch.getSnapshot().running, false); assert.equal(calls.verify.length, 0);
  assert.equal(calls.prepare.length, 4); assert.equal(batch.getSnapshot().rows[4].phase, "pending");
});

test("local saved receipt completes without a redundant confirmation or upload", async () => {
  const { batch, calls } = rig(); await add(batch, "a.jpg");
  assert.equal(batch.getSnapshot().hasPending, true);
  batch.start(); await idle(batch);
  assert.equal(batch.getSnapshot().rows[0].phase, "done");
  assert.equal(batch.getSnapshot().hasPending, false);
  assert.equal(calls.transfer.length, 1); assert.equal(calls.confirm.length, 0);
  batch.setDestination({ target: "other", directory: "elsewhere" });
  assert.equal(batch.getSnapshot().destination.target, "local");
  batch.remove("row-1"); assert.equal(batch.getSnapshot().configurationLocked, false);
});
test("S3 transmission confirms the same task before showing completion", async () => {
  const { batch, calls } = rig({ transfer: async () => undefined }, { target: "s3-1", directory: "" });
  await add(batch, "a.jpg"); batch.start(); await idle(batch);
  assert.equal(calls.confirm[0][0], "task-1"); assert.equal(batch.getSnapshot().ready, 1);
});
test("lost PUT response recovers the saved original with no new preparation", async () => {
  const { batch, calls } = rig({ transfer: async () => { throw new Error("response lost"); } });
  await add(batch, "a.jpg"); batch.start(); await idle(batch);
  assert.equal(batch.getSnapshot().ready, 1); assert.equal(calls.prepare.length, 1);
  assert.equal(calls.transfer.length, 1); assert.equal(calls.verify.length, 1);
});
test("lost S3 confirmation recovers storage facts without another PUT", async () => {
  const { batch, calls } = rig({ transfer: async () => undefined, confirm: async () => { throw new Error("confirmation lost"); } }, { target: "s3-1", directory: "" });
  await add(batch, "a.jpg"); batch.start(); await idle(batch);
  assert.equal(batch.getSnapshot().ready, 1); assert.equal(calls.confirm.length, 1);
  assert.equal(calls.verify.length, 1); assert.equal(calls.transfer.length, 1);
});
test("an uncertain item verifies once while independent files finish, then resumes confirmation", async () => {
  let verifies = 0;
  const { batch, calls } = rig({
    transfer: async plan => { if (plan.task.path === "a.jpg") throw new Error("offline"); return done(plan.task.id); },
    verify: async id => { if (++verifies === 1) throw new Error("still offline"); return { state: "saved", task: done(id) }; },
  });
  await add(batch, "a.jpg", "b.jpg"); batch.start(); await idle(batch);
  assert.equal(batch.getSnapshot().rows[0].phase, "uncertain"); assert.equal(batch.getSnapshot().ready, 1);
  assert.equal(verifies, 1); assert.equal(batch.getSnapshot().hasPending, true);
  batch.start("row-1"); await idle(batch);
  assert.equal(batch.getSnapshot().ready, 2); assert.equal(verifies, 2); assert.equal(calls.transfer.length, 2);
});
test("failed manual confirmation stays uncertain after one request until the next action", async () => {
  const { batch, calls } = rig({ transfer: async () => { throw new Error("offline"); }, verify: async () => { throw new Error("still offline"); } });
  await add(batch, "a.jpg"); batch.start(); await idle(batch);
  assert.equal(calls.verify.length, 1);
  batch.start("row-1"); await idle(batch);
  assert.equal(calls.verify.length, 2); assert.equal(batch.getSnapshot().rows[0].phase, "uncertain");
  assert.equal(calls.prepare.length, 1); assert.equal(calls.transfer.length, 1);
});
test("a failed consumed local PUT can prepare anew despite processing retryable=false", async () => {
  let puts = 0;
  const { batch, calls } = rig({
    transfer: async plan => { if (++puts === 1) throw new Error("incomplete"); return done(plan.task.id); },
    verify: async id => ({ state: "absent", task: task(id, { phase: "error", retryable: false, transferRetryable: true, code: "transfer_failed" }) }),
  });
  await add(batch, "a.jpg"); batch.start(); await idle(batch);
  assert.equal(batch.getSnapshot().rows[0].canResume, true);
  batch.start("row-1"); await idle(batch);
  assert.equal(batch.getSnapshot().ready, 1); assert.equal(calls.prepare.length, 2);
  assert.deepEqual(calls.prepare.map(call => call[0].directory), ["旅行", "旅行"]);
});
test("an unconsumed local task retries its original PUT after verified absence", async () => {
  let puts = 0;
  const { batch, calls } = rig({ transfer: async plan => { if (++puts === 1) throw new Error("never arrived"); return done(plan.task.id); }, verify: async id => ({ state: "absent", task: task(id) }) });
  await add(batch, "a.jpg"); batch.start(); await idle(batch); batch.start(); await idle(batch);
  assert.equal(calls.prepare.length, 1); assert.equal(calls.transfer[1][0].task.id, "task-1");
  assert.equal(batch.getSnapshot().ready, 1);
});
test("saved processing failures retry processing once on explicit action, never transfer", async () => {
  let recovered = false;
  const failed = id => task(id, { phase: "error", saved: true, retryable: true, transferRetryable: false, message: "wallpaper failed" });
  const { batch, calls } = rig({ transfer: async plan => failed(plan.task.id), status: async id => recovered ? done(id) : failed(id), retryProcessing: async id => { recovered = true; return done(id); } });
  await add(batch, "a.jpg"); batch.start(); await idle(batch);
  assert.equal(batch.getSnapshot().rows[0].saved, true); assert.equal(calls.retryProcessing.length, 0);
  batch.start("row-1"); await idle(batch);
  assert.equal(batch.getSnapshot().ready, 1); assert.equal(calls.retryProcessing.length, 1); assert.equal(calls.transfer.length, 1);
});
test("invalid saved originals show the reason and reject pointless processing retries", async () => {
  const { batch, calls } = rig({ transfer: async plan => task(plan.task.id, { phase: "error", saved: true, retryable: false, transferRetryable: false, message: "invalid image" }) });
  await add(batch, "a.jpg"); batch.start(); await idle(batch); batch.start(); await ticks();
  assert.equal(batch.getSnapshot().rows[0].message, "invalid image"); assert.equal(batch.getSnapshot().rows[0].canResume, false);
  assert.equal(calls.transfer.length, 1); assert.equal(calls.retryProcessing.length, 0);
});
test("processing wait ends at two minutes with no idle polling and continues on demand", async () => {
  let ready = false;
  const processing = id => task(id, { phase: "processing", saved: true, transferRetryable: false });
  const { batch, calls } = rig({ transfer: async plan => processing(plan.task.id), status: async id => ready ? done(id) : processing(id) });
  await add(batch, "a.jpg"); batch.start(); await idle(batch);
  assert.equal(batch.getSnapshot().rows[0].phase, "waiting"); assert.equal(batch.getSnapshot().rows[0].saved, true);
  const reads = calls.status.length; await ticks(); assert.equal(calls.status.length, reads);
  ready = true; batch.start("row-1"); await idle(batch);
  assert.equal(batch.getSnapshot().ready, 1); assert.equal(calls.transfer.length, 1);
});
test("stop cancels four active items, starts no fifth, and ignores late progress and completions", async () => {
  const transfers = [];
  const { batch, calls } = rig({ transfer: (plan, _file, signal, progress) => { const gate = later(); transfers.push({ gate, signal, progress, id: plan.task.id }); return gate.promise; } });
  await add(batch, "a.jpg", "b.jpg", "c.jpg", "d.jpg", "e.jpg"); batch.start(); batch.start(); await ticks();
  assert.equal(transfers.length, 4); assert.equal(calls.prepare.length, 4);
  batch.stop(); assert.equal(batch.getSnapshot().running, false);
  assert.equal(batch.getSnapshot().rows.filter(row => row.phase === "uncertain").length, 4);
  assert.equal(batch.getSnapshot().rows[4].phase, "pending");
  for (const transfer of transfers) { assert.equal(transfer.signal.aborted, true); transfer.progress(100); transfer.gate.resolve(done(transfer.id)); }
  await ticks(); assert.equal(calls.prepare.length, 4); assert.equal(calls.verify.length, 0);
  assert.equal(batch.getSnapshot().ready, 0); assert.equal(batch.getSnapshot().rows[0].percent, 0);
});
test("stopping saved processing preserves the original and resumes only its status", async () => {
  const gate = later();
  const { batch, calls } = rig({ transfer: async plan => task(plan.task.id, { saved: true, phase: "processing", transferRetryable: false }), wait: signal => { signal.addEventListener("abort", () => gate.reject(new Error("stopped")), { once: true }); return gate.promise; } });
  await add(batch, "a.jpg"); batch.start(); await ticks(); batch.stop(); await ticks();
  assert.equal(batch.getSnapshot().rows[0].phase, "waiting"); assert.equal(batch.getSnapshot().rows[0].saved, true);
  batch.start(); await idle(batch); assert.equal(batch.getSnapshot().ready, 1);
  assert.equal(calls.transfer.length, 1); assert.equal(calls.verify.length, 0);
});
test("clear cancels pending directory reads and stale results cannot repopulate a new batch", async () => {
  const { batch } = rig(); await add(batch, "a.jpg");
  const gate = later(); let signal;
  const reading = batch.select(value => { signal = value; return gate.promise; });
  assert.equal(batch.getSnapshot().reading, true); batch.clear(); assert.equal(signal.aborted, true);
  batch.setDestination({ target: "other", directory: "new" }); await add(batch, "fresh.jpg");
  gate.resolve(selection(photo("old.jpg"))); await reading;
  assert.deepEqual(batch.getSnapshot().rows.map(row => row.path), ["fresh.jpg"]);
  assert.equal(batch.getSnapshot().destination.directory, "new");
});
test("running freezes selection, removal, clearing and the destination", async () => {
  const gate = later(); const { batch } = rig({ transfer: () => gate.promise });
  await add(batch, "a.jpg"); batch.start(); await ticks();
  await add(batch, "b.jpg"); batch.remove("row-1"); batch.clear(); batch.setDestination({ target: "other", directory: "new" });
  assert.equal(batch.getSnapshot().rows.length, 1); assert.equal(batch.getSnapshot().destination.target, "local");
  batch.stop(); gate.resolve(done("task-1")); await ticks();
});
for (const saved of [false, true]) test(`expired tasks preserve saved=${saved} without automatic recreation`, async () => {
  const { batch, calls } = rig({
    transfer: async plan => { if (!saved) throw new Error("lost"); return task(plan.task.id, { saved: true, phase: "processing", transferRetryable: false }); },
    status: async () => { throw new UploadExpired(); }, verify: async () => { throw new UploadExpired(); },
  });
  await add(batch, "a.jpg"); batch.start(); await idle(batch); batch.start(); await ticks();
  assert.equal(batch.getSnapshot().rows[0].phase, "expired"); assert.equal(batch.getSnapshot().rows[0].saved, saved);
  assert.equal(batch.getSnapshot().hasPending, !saved); assert.equal(calls.prepare.length, 1); assert.equal(calls.transfer.length, 1);
});
test("explicit preparation rejection reports its reason and remains protected before leaving", async () => {
  const { batch, calls } = rig({ prepare: async () => { throw new UploadRejected("invalid path"); } });
  await add(batch, "a.jpg"); batch.start(); await idle(batch);
  assert.equal(batch.getSnapshot().rows[0].message, "invalid path"); assert.equal(batch.getSnapshot().rows[0].canResume, false);
  assert.equal(batch.getSnapshot().hasPending, true); assert.equal(calls.transfer.length, 0);
});
test("selection validates size and duplicates, and an overfull addition preserves the list", async () => {
  const { batch } = rig();
  await batch.select(() => selection(photo("a.jpg"), photo("a.jpg"), photo("empty.jpg", 0), photo("huge.jpg", 50_000_001)));
  assert.equal(batch.getSnapshot().rows.length, 1); assert.match(batch.getSnapshot().note, /重复选择/);
  const small = createUploadBatch({ adapter: {}, limits: { maxBytes: 8, maxFiles: 1 }, initialDestination: { target: "local", directory: "" }, newID: () => "only" });
  await add(small, "first.jpg"); await add(small, "second.jpg");
  assert.equal(small.getSnapshot().rows.length, 1); assert.match(small.getSnapshot().error, /最多添加 1/);
});
