import { api, APIError } from "./api";
import { sendPhoto, uploadDelay, type PreparedUpload, type UploadTask } from "./upload";
import { UploadExpired, UploadRejected, type UploadAdapter, type UploadVerification } from "./upload-batch";

function task(value: UploadTask, id?: string): UploadTask {
  if (!value || typeof value.id !== "string" || !value.id || (id && value.id !== id) ||
      !["waiting", "uploading", "processing", "done", "skipped", "error"].includes(value.phase) ||
      typeof value.saved !== "boolean" || typeof value.retryable !== "boolean" || typeof value.transferRetryable !== "boolean") {
    throw new Error("上传响应不完整，请继续确认");
  }
  return value;
}
async function read<T>(path: string, signal: AbortSignal, post = false): Promise<T> {
  try {
    return await api<T>(path, { ...(post ? { method: "POST", body: "{}" } : {}), signal: AbortSignal.any([signal, AbortSignal.timeout(30_000)]) });
  } catch (err) {
    if (err instanceof APIError && err.status === 404) throw new UploadExpired("任务已失效");
    throw err;
  }
}
async function readTask(id: string, signal: AbortSignal, suffix = "") {
  const data = await read<{ task: UploadTask }>("/api/uploads/" + encodeURIComponent(id) + suffix, signal, !!suffix);
  return task(data.task, id);
}
export const uploadHTTP: UploadAdapter = {
  async prepare(input, signal) {
    let plan: PreparedUpload;
    try {
      plan = await api<PreparedUpload>("/api/uploads", { method: "POST", body: JSON.stringify(input), signal: AbortSignal.any([signal, AbortSignal.timeout(30_000)]) });
    } catch (err) {
      if (err instanceof APIError && [400, 403, 422].includes(err.status)) throw new UploadRejected(err.message);
      throw err;
    }
    task(plan.task);
    if (!plan.task.saved && !["skipped", "error"].includes(plan.task.phase) && (!plan.url || plan.method !== "PUT")) throw new Error("上传准备响应不完整，请重试");
    return plan;
  },
  async transfer(plan, file, signal, progress) {
    const receipt = await sendPhoto(plan, file, signal, progress);
    return receipt ? task(receipt, plan.task.id) : undefined;
  },
  status: (id, signal) => readTask(id, signal),
  confirm: (id, signal) => readTask(id, signal, "/complete"),
  retryProcessing: (id, signal) => readTask(id, signal, "/retry"),
  async verify(id, signal) {
    const result = await read<UploadVerification>("/api/uploads/" + encodeURIComponent(id) + "/verify", signal, true);
    task(result.task, id);
    if (!["saved", "absent", "pending", "skipped", "rejected"].includes(result.state)) throw new Error("保存核对响应不完整，请继续确认");
    return result;
  },
  now: () => Date.now(),
  wait: uploadDelay,
};
