import { api } from "@/lib/api";
export interface BatchItem { id: number; album: string; name: string; status: string; error?: string; path?: string }
export interface ShareBatch { id: string; mode: "open" | "stop"; albums: string[]; status: string; revision: number; created: number; finished: number; total: number; shared: number; changes: number; done: number; succeeded: number; failed: number; items: BatchItem[] | null }
export const batchLabels: Record<string, string> = { prepared: "等待确认", running: "正在处理", completed: "已完成", cancelled: "已取消剩余处理", interrupted: "已中断" };
export function checkBatch(raw: unknown): ShareBatch {
  const b = raw as Partial<ShareBatch> | null;
  if (!b || typeof b !== "object" || typeof b.id !== "string" || !/^[A-Za-z0-9_-]{32}$/.test(b.id) || !["open", "stop"].includes(b.mode || "") || !Object.hasOwn(batchLabels, b.status || "") || !Array.isArray(b.albums) || !b.albums.every(a => typeof a === "string") || ![b.revision, b.created, b.finished, b.total, b.shared, b.changes, b.done, b.succeeded, b.failed].every(n => Number.isSafeInteger(n) && Number(n) >= 0) || !(b.items === null || Array.isArray(b.items))) throw new Error("未能确认批量结果，请重新核对任务。");
  if (b.items && !b.items.every(i => i && Number.isSafeInteger(i.id) && typeof i.name === "string" && typeof i.album === "string" && ["pending", "success", "failed"].includes(i.status) && (!i.path || /^\/i\/[A-Za-z0-9_-]{43}\.(jpg|png|webp|gif)$/.test(i.path)))) throw new Error("未能确认图片处理结果，请重新核对任务。");
  return b as ShareBatch;
}
export async function readBatch(id: string, options?: RequestInit) { return checkBatch(await api<unknown>(`/api/share-batches/${id}`, options)); }
export async function listBatches(options?: RequestInit) {
  const data = await api<{ tasks?: unknown[] }>("/api/share-batches", options);
  if (!data || !Array.isArray(data.tasks)) throw new Error("未能确认批量任务列表。");
  return data.tasks.map(checkBatch);
}
export function sharingChanged() { dispatchEvent(new Event("bijin-sharing-changed")); }
