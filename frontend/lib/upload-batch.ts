import type { PreparedUpload, Selection, UploadTask } from "./upload";

export type UploadPhase = "pending" | "uploading" | "processing" | "done" | "skipped" | "error" | "cancelled" | "uncertain" | "waiting" | "expired";
export interface UploadDestination { target: string; directory: string }
export interface UploadVerification { state: "saved" | "absent" | "pending" | "skipped" | "rejected"; task: UploadTask }
export interface UploadAdapter {
  prepare(input: UploadDestination & { path: string; size: number }, signal: AbortSignal): Promise<PreparedUpload>;
  transfer(plan: PreparedUpload, file: File, signal: AbortSignal, progress: (percent: number) => void): Promise<UploadTask | undefined>;
  status(id: string, signal: AbortSignal): Promise<UploadTask>;
  confirm(id: string, signal: AbortSignal): Promise<UploadTask>;
  verify(id: string, signal: AbortSignal): Promise<UploadVerification>;
  retryProcessing(id: string, signal: AbortSignal): Promise<UploadTask>;
  now(): number;
  wait(signal: AbortSignal, ms: number): Promise<void>;
}
export class UploadExpired extends Error {}
export class UploadRejected extends Error {}
export interface UploadRowView {
  id: string; path: string; size: number; phase: UploadPhase; percent: number; saved: boolean;
  message: string; canResume: boolean; actionLabel: string;
}
export interface UploadBatchView {
  rows: UploadRowView[]; destination: UploadDestination; running: boolean; reading: boolean;
  configurationLocked: boolean; hasPending: boolean; canStart: boolean;
  error: string; note: string; totalBytes: number; transferBytes: number; sentBytes: number; percent: number;
  ready: number; skipped: number; failed: number;
}
interface Row {
  id: string; file: File; path: string; phase: UploadPhase; percent: number; saved: boolean; message: string;
  task?: UploadTask; plan?: PreparedUpload; preparedAt: number;
  absent: boolean; blocked: boolean; active: boolean; queued: boolean; transferStarted: boolean;
}
interface Options { adapter: UploadAdapter; limits: { maxBytes: number; maxFiles: number }; initialDestination: UploadDestination; newID?: () => string }

export function createUploadBatch({ adapter, limits, initialDestination, newID = () => crypto.randomUUID() }: Options) {
  let rows: Row[] = [], destination = { ...initialDestination }, locked = false;
  let error = "", note = "", reading: AbortController | undefined;
  let epoch = 0, active = 0, queue: Row[] = [];
  const controllers = new Map<Row, AbortController>();
  const listeners = new Set<() => void>();
  const holds = new Set<symbol>(), releases = new Set<() => void>();
  const resumable = (row: Row) => !row.blocked && !row.active && !row.queued && !["done", "skipped", "expired"].includes(row.phase);
  const actionLabel = (row: Row) => row.phase === "uncertain" ? "继续确认" : row.phase === "waiting" ? "继续查看" : row.phase === "pending" || row.phase === "cancelled" ? "继续上传" : "重试";
  function snapshot(): UploadBatchView {
    const running = active > 0 || queue.length > 0;
    const transferRows = rows.filter(row => row.phase !== "skipped");
    const transferBytes = transferRows.reduce((sum, row) => sum + row.file.size, 0);
    const sentBytes = transferRows.reduce((sum, row) => sum + row.file.size * row.percent / 100, 0);
    return {
      rows: rows.map(row => ({ id: row.id, path: row.path, size: row.file.size, phase: row.phase, percent: row.percent, saved: row.saved, message: row.message, canResume: resumable(row), actionLabel: actionLabel(row) })),
      destination: { ...destination }, running, reading: !!reading,
      configurationLocked: locked || running || !!reading,
      hasPending: running || !!reading || rows.some(row => !row.saved && row.phase !== "done" && row.phase !== "skipped"),
      canStart: !reading && rows.some(resumable), error, note,
      totalBytes: rows.reduce((sum, row) => sum + row.file.size, 0), transferBytes, sentBytes,
      percent: transferBytes ? Math.round(sentBytes / transferBytes * 100) : 100,
      ready: rows.filter(row => row.phase === "done").length, skipped: rows.filter(row => row.phase === "skipped").length, failed: rows.filter(row => row.phase === "error").length,
    };
  }
  let view = snapshot();
  function emit() { view = snapshot(); listeners.forEach(listener => listener()); }
  function setTask(row: Row, task: UploadTask) {
    row.task = task;
    if (task.saved) { row.saved = true; row.absent = false; row.percent = 100; }
  }
  function showTask(row: Row, task: UploadTask) {
    setTask(row, task);
    row.message = task.message || "";
    if (task.phase === "skipped") { row.phase = "skipped"; row.percent = 0; row.blocked = true; }
    else if (task.saved && task.phase === "done") { row.phase = "done"; row.blocked = true; }
    else if (task.saved && task.phase === "processing") { row.phase = "processing"; row.blocked = false; }
    else if (task.phase === "error") {
      row.phase = "error";
      row.blocked = row.saved ? !task.retryable : !task.transferRetryable;
      if (!row.saved) { row.absent = true; row.plan = undefined; }
    }
  }
  function expired(row: Row) {
    row.phase = "expired"; row.blocked = true;
    row.message = (row.saved ? "原图已保存；" : "原图保存结果仍待确认；") + "任务已失效，请查看相册或扫描恢复。需要再上传时，移除后重新选择。";
  }
  function unknown(row: Row, message: string) {
    row.phase = row.saved ? "waiting" : "uncertain"; row.blocked = false;
    row.message = row.saved ? "原图已保存，暂时无法查询照片处理；可继续查看。" : "原图保存结果待确认；可继续确认。";
    if (message) row.message += " " + message;
  }

  async function run(row: Row, controller: AbortController, runEpoch: number) {
    const signal = controller.signal;
    const valid = () => epoch === runEpoch && !signal.aborted && rows.includes(row);
    const previousPhase = row.phase;
    let recoveryAttempted = false;
    async function request<T>(action: () => Promise<T>): Promise<T> {
      while (holds.size) await new Promise<void>((resolve, reject) => {
        const resume = () => { releases.delete(resume); signal.removeEventListener("abort", abort); resolve(); };
        const abort = () => { releases.delete(resume); reject(new DOMException("已取消任务", "AbortError")); };
        releases.add(resume); signal.addEventListener("abort", abort, { once: true });
        if (signal.aborted) abort();
      });
      if (!valid()) throw new DOMException("已取消任务", "AbortError");
      return action();
    }
    async function finish(task: UploadTask) {
      const deadline = adapter.now() + 120_000;
      while (valid()) {
        showTask(row, task); emit();
        if (task.phase !== "processing") return;
        const remaining = deadline - adapter.now();
        if (remaining <= 0) { row.phase = "waiting"; row.message = "原图已保存，等待处理；可继续查看。"; emit(); return; }
        await adapter.wait(signal, Math.min(600, remaining));
        try { task = await request(() => adapter.status(task.id, AbortSignal.any([signal, AbortSignal.timeout(Math.max(1, deadline - adapter.now()))]))); }
        catch (err) {
          if (valid() && adapter.now() >= deadline) { row.phase = "waiting"; row.message = "原图已保存，等待处理；可继续查看。"; emit(); return; }
          throw err;
        }
      }
    }
    async function recover() {
      if (!row.task) return;
      recoveryAttempted = true;
      if (row.saved) {
        const task = await request(() => adapter.status(row.task!.id, signal));
        if (valid()) await finish(task);
        return;
      }
      const result = await request(() => adapter.verify(row.task!.id, signal));
      if (!valid()) return;
      setTask(row, result.task);
      if (row.saved) { await finish(result.task); return; }
      if (result.state === "skipped" || result.state === "rejected") { showTask(row, result.task); return; }
      if (result.state === "absent") {
        row.absent = true; row.phase = "error";
        row.blocked = result.task.transferRetryable === false;
        if (result.task.phase !== "waiting") row.plan = undefined;
        row.message = result.task.message || "已确认原图尚未保存，可以重试上传。";
      } else unknown(row, "服务器尚未确认保存结果。");
    }
    try {
      if (row.saved && row.task) {
        let task = await request(() => adapter.status(row.task!.id, signal));
        if (!valid()) return;
        if (previousPhase === "error" && task.phase === "error" && task.retryable) task = await request(() => adapter.retryProcessing(task.id, signal));
        if (valid()) await finish(task);
        return;
      }
      if (row.task && !row.absent) { await recover(); return; }
      if (!row.plan || (row.task?.target !== "local" && adapter.now() - row.preparedAt >= 14 * 60_000)) {
        row.phase = "uploading"; row.percent = 0; row.message = "正在准备上传"; locked = true; emit();
        const plan = await request(() => adapter.prepare({ ...destination, directory: destination.directory.trim(), path: row.path, size: row.file.size }, signal));
        if (!valid()) return;
        row.plan = plan; row.preparedAt = adapter.now(); setTask(row, plan.task);
        if (plan.task.phase === "skipped" || plan.task.phase === "error" || plan.task.saved) { await finish(plan.task); return; }
      }
      row.phase = "uploading"; row.percent = 0; row.message = "";
      row.transferStarted = true; row.absent = false; emit();
      const receipt = await request(() => adapter.transfer(row.plan!, row.file, signal, percent => {
        if (valid()) { row.percent = Math.max(row.percent, Math.min(100, percent)); emit(); }
      }));
      if (!valid()) return;
      if (receipt) {
        setTask(row, receipt);
        if (receipt.saved || receipt.phase === "skipped" || receipt.phase === "error") { await finish(receipt); return; }
      }
      row.message = "正在确认原图保存结果"; emit();
      const task = await request(() => adapter.confirm(row.task!.id, signal));
      if (valid()) await finish(task);
    } catch (err) {
      if (!valid()) return;
      if (err instanceof UploadExpired) { expired(row); return; }
      const message = err instanceof Error ? err.message : "暂时无法完成上传";
      if (!row.task) { row.phase = "error"; row.message = message; row.absent = true; row.blocked = err instanceof UploadRejected; return; }
      if (recoveryAttempted) { unknown(row, message); return; }
      // Exactly one recovery attempt per failed execution. A failure of this
      // attempt stays visible and never starts another automatic recovery loop.
      try { await recover(); }
      catch (confirmationError) {
        if (!valid()) return;
        if (confirmationError instanceof UploadExpired) expired(row);
        else unknown(row, message);
      }
    }
  }
  function pump() {
    const runEpoch = epoch;
    while (!holds.size && active < 4 && queue.length) {
      const row = queue.shift()!;
      row.queued = false; row.active = true; active++;
      const controller = new AbortController(); controllers.set(row, controller);
      void run(row, controller, runEpoch).finally(() => {
        if (epoch !== runEpoch) return;
        controllers.delete(row); row.active = false; active--;
        pump();
        if (!active && !queue.length) note = "本批任务已结束，请查看处理结果。";
        emit();
      });
    }
    emit();
  }
  function stop() {
    epoch++;
    holds.clear();
    reading?.abort(); reading = undefined;
    queue.forEach(row => { row.queued = false; }); queue = [];
    for (const [row, controller] of controllers) {
      row.active = false;
      if (["done", "skipped", "error", "expired"].includes(row.phase)) { /* Preserve known terminal results. */ }
      else if (row.saved) { row.phase = "waiting"; row.message = "原图已保存，后台继续照片处理；可继续查看。"; }
      else if (row.transferStarted && !row.absent) unknown(row, "已停止浏览器工作。");
      else { row.phase = "cancelled"; row.message = "已停止，可继续上传。"; }
      controller.abort();
    }
    controllers.clear(); active = 0;
    note = "已停止本批。已保存的原图继续后台照片处理；未确认项可继续确认。";
    emit();
  }
  return {
    getSnapshot: () => view,
    subscribe(listener: () => void) { listeners.add(listener); return () => { listeners.delete(listener); }; },
    setDestination(next: UploadDestination) { if (view.configurationLocked) return; destination = { ...next }; emit(); },
    async select(read: (signal: AbortSignal) => Selection | Promise<Selection>) {
      if (view.running || reading) return;
      const controller = new AbortController(); reading = controller; emit();
      try {
        const selection = await read(controller.signal);
        if (controller.signal.aborted || reading !== controller) return;
        let tooLarge = 0, duplicates = 0;
        const existing = new Set(rows.map(row => row.path));
        const additions: Row[] = [];
        for (const item of selection.items) {
          if (!item.file.size || item.file.size > limits.maxBytes) { tooLarge++; continue; }
          if (existing.has(item.path)) { duplicates++; continue; }
          existing.add(item.path);
          additions.push({ id: newID(), ...item, phase: "pending", percent: 0, saved: false, message: "", absent: true, blocked: false, active: false, queued: false, transferStarted: false, preparedAt: 0 });
        }
        if (rows.length + additions.length > limits.maxFiles) { error = "每批最多添加 " + limits.maxFiles.toLocaleString("zh-CN") + " 张图片"; return; }
        rows.push(...additions); error = "";
        const skipped = [selection.skippedFolders ? "跳过 " + selection.skippedFolders + " 个子文件夹或隐藏文件夹" : "", selection.skippedFiles ? "跳过 " + selection.skippedFiles + " 个非图片或隐藏文件" : "", tooLarge ? "跳过 " + tooLarge + " 个空文件或超过上限的图片" : "", duplicates ? "忽略 " + duplicates + " 个重复选择" : ""].filter(Boolean);
        note = "新增 " + additions.length + " 张图片" + (skipped.length ? "；" + skipped.join("，") : "");
      } catch (err) { if (!controller.signal.aborted && !(err instanceof Error && err.name === "AbortError")) error = err instanceof Error ? err.message : "无法读取所选图片，请重试"; }
      finally { if (reading === controller) { reading = undefined; emit(); } }
    },
    remove(id: string) { if (view.running) return; rows = rows.filter(row => row.id !== id); if (!rows.length) { locked = false; note = ""; } emit(); },
    clear() { if (view.running) return; stop(); rows = []; locked = false; error = ""; note = ""; emit(); },
    start(id?: string) {
      if (reading) return;
      const pending = rows.filter(row => (!id || row.id === id) && resumable(row));
      if (!pending.length) return;
      error = "";
      pending.forEach(row => { row.queued = true; queue.push(row); });
      pump();
    },
    stop,
    // The view holds new requests while the browser decides whether to leave.
    // Releasing keeps existing work; stopping cancels it and invalidates holds.
    hold() {
      const token = Symbol(); holds.add(token);
      return () => { if (!holds.delete(token) || holds.size) return; [...releases].forEach(release => release()); pump(); };
    },
    dispose() { stop(); listeners.clear(); },
  };
}
