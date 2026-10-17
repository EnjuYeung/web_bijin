import { api } from "@/lib/api";
import { isSharePath } from "@/lib/sharing";

export interface BatchItem { id: number; album: string; name: string; status: string; error?: string; path?: string }
// While a task runs, items lists only the failed photos; afterwards every photo.
export interface ShareBatch {
  id: string; mode: "open" | "stop"; albums: string[]; status: string; revision: number; created: number; finished: number;
  total: number; shared: number; changes: number; done: number; succeeded: number; failed: number; items: BatchItem[] | null;
}
export const batchLabels: Record<string, string> = { prepared: "等待确认", running: "正在处理", completed: "已完成", cancelled: "已取消剩余处理", interrupted: "已中断" };

const counts = (b: Partial<ShareBatch>) => [b.revision, b.created, b.finished, b.total, b.shared, b.changes, b.done, b.succeeded, b.failed];
export function checkBatch(raw: unknown): ShareBatch {
  const b = raw as Partial<ShareBatch> | null;
  const valid = !!b && typeof b === "object" && typeof b.id === "string" && /^[A-Za-z0-9_-]{32}$/.test(b.id)
    && (b.mode === "open" || b.mode === "stop") && Object.hasOwn(batchLabels, b.status || "")
    && Array.isArray(b.albums) && b.albums.every(a => typeof a === "string")
    && counts(b).every(n => Number.isSafeInteger(n) && Number(n) >= 0)
    && (b.items === null || Array.isArray(b.items));
  if (!valid) throw new Error("未能确认批量结果，请重新核对任务。");
  const itemValid = (i: BatchItem) => i && Number.isSafeInteger(i.id) && typeof i.name === "string" && typeof i.album === "string"
    && ["pending", "success", "failed"].includes(i.status) && (!i.path || isSharePath(i.path));
  if (b.items && !b.items.every(itemValid)) throw new Error("未能确认图片处理结果，请重新核对任务。");
  return b as ShareBatch;
}
export async function readBatch(id: string) {
  return checkBatch(await api<unknown>(`/api/share-batches/${id}`));
}
// start, retry and cancel answer with the task as it now is.
export async function changeBatch(id: string, action: "start" | "retry" | "cancel", revision: number) {
  return checkBatch(await api<unknown>(`/api/share-batches/${id}/${action}`, { method: "POST", body: JSON.stringify({ revision }) }));
}
export async function listBatches() {
  const data = await api<{ tasks?: unknown[] }>("/api/share-batches");
  if (!data || !Array.isArray(data.tasks)) throw new Error("未能确认批量任务列表。");
  return data.tasks.map(checkBatch);
}
