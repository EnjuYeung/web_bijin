import { APIError, loginURL } from "@/lib/api";

export interface SelectedPhoto { file: File; path: string }
export interface Selection { items: SelectedPhoto[]; skippedFiles: number; skippedFolders: number }
export interface UploadTarget { id: string; name: string; kind: "local" | "s3" }
export interface UploadTask { id: string; target: string; path: string; phase: string; message?: string; saved: boolean; retryable: boolean }
export interface PreparedUpload { task: UploadTask; url?: string; method?: string; headers?: Record<string, string> }

export function photoSelection(files: Iterable<File>, folder = false): Selection {
  const result: Selection = { items: [], skippedFiles: 0, skippedFolders: 0 };
  const folders = new Set<string>();
  for (const file of files) {
    const rel = folder ? file.webkitRelativePath || file.name : file.name;
    const parts = rel.split("/");
    if (folder && parts.length > 2) {
      folders.add(parts.slice(0, 2).join("/"));
      continue;
    }
    if (parts.some(part => part.startsWith(".")) || !/\.(jpe?g|png|webp|gif)$/i.test(file.name)) {
      result.skippedFiles++;
      continue;
    }
    result.items.push({ file, path: rel });
  }
  result.skippedFolders = folders.size;
  return result;
}

type FolderHandle = FileSystemDirectoryHandle & { values(): AsyncIterable<FileSystemHandle> };
export async function folderSelection(handle: FolderHandle): Promise<Selection> {
  const result: Selection = { items: [], skippedFiles: 0, skippedFolders: 0 };
  if (handle.name.startsWith(".")) { result.skippedFolders++; return result; }
  for await (const entry of handle.values()) {
    if (entry.kind === "directory") { result.skippedFolders++; continue; }
    const file = await (entry as FileSystemFileHandle).getFile();
    const selected = photoSelection([file]);
    result.skippedFiles += selected.skippedFiles;
    for (const item of selected.items) result.items.push({ file: item.file, path: handle.name + "/" + item.path });
  }
  return result;
}

function entryFile(entry: FileSystemFileEntry): Promise<File> {
  return new Promise((resolve, reject) => entry.file(resolve, reject));
}
function entryChildren(entry: FileSystemDirectoryEntry): Promise<FileSystemEntry[]> {
  const reader = entry.createReader();
  const children: FileSystemEntry[] = [];
  return new Promise((resolve, reject) => {
    function read() {
      reader.readEntries(batch => {
        if (!batch.length) { resolve(children); return; }
        children.push(...batch);
        read();
      }, reject);
    }
    read();
  });
}

// Read exactly one directory level, including every readEntries batch.
// The drop entries must be captured synchronously while the browser exposes them.
export async function dropSelection(entries: FileSystemEntry[], files: File[]): Promise<Selection> {
  if (!entries.length) return photoSelection(files);
  const result: Selection = { items: [], skippedFiles: 0, skippedFolders: 0 };
  for (const entry of entries) {
    if (entry.isDirectory) {
      if (entry.name.startsWith(".")) { result.skippedFolders++; continue; }
      for (const child of await entryChildren(entry as FileSystemDirectoryEntry)) {
        if (child.isDirectory) { result.skippedFolders++; continue; }
        if (!child.isFile) continue;
        const file = await entryFile(child as FileSystemFileEntry);
        const selected = photoSelection([file]);
        result.skippedFiles += selected.skippedFiles;
        for (const item of selected.items) result.items.push({ file: item.file, path: entry.name + "/" + item.path });
      }
    } else if (entry.isFile) {
      const selected = photoSelection([await entryFile(entry as FileSystemFileEntry)]);
      result.items.push(...selected.items);
      result.skippedFiles += selected.skippedFiles;
    }
  }
  return result;
}

export function sendPhoto(prepared: PreparedUpload, file: File, signal: AbortSignal, progress: (value: number) => void): Promise<void> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    const abort = () => xhr.abort();
    const finish = (error?: Error) => {
      signal.removeEventListener("abort", abort);
      if (error) reject(error); else resolve();
    };
    xhr.open("PUT", prepared.url!);
    for (const [name, value] of Object.entries(prepared.headers || {})) xhr.setRequestHeader(name, value);
    xhr.timeout = 15 * 60 * 1000;
    let reported = 0;
    xhr.upload.onprogress = event => {
      if (event.lengthComputable && (performance.now() - reported > 200 || event.loaded === event.total)) {
        reported = performance.now();
        progress(Math.min(100, event.loaded / event.total * 100));
      }
    };
    xhr.onload = () => {
      if ((xhr.status >= 200 && xhr.status < 300) || xhr.status === 412) { progress(100); finish(); return; }
      if (xhr.status === 401 && prepared.url?.startsWith("/")) location.assign(loginURL());
      let message = "上传失败（" + xhr.status + "），请重试";
      try { message = JSON.parse(xhr.responseText).error || message; } catch {}
      if (xhr.status === 403 && !prepared.url?.startsWith("/")) message = "存储拒绝上传，请检查现有 Key 的上传权限";
      finish(new APIError(message, xhr.status));
    };
    xhr.onerror = () => finish(new Error("网络中断或跨域配置不正确，请重试"));
    xhr.ontimeout = () => finish(new Error("上传超时，请重试"));
    xhr.onabort = () => finish(new DOMException("已取消上传", "AbortError"));
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) { finish(new DOMException("已取消上传", "AbortError")); return; }
    xhr.send(file);
  });
}

export function uploadDelay(signal: AbortSignal, ms = 600): Promise<void> {
  return new Promise((resolve, reject) => {
    const abort = () => { clearTimeout(timer); reject(new DOMException("已取消等待", "AbortError")); };
    const timer = setTimeout(() => { signal.removeEventListener("abort", abort); resolve(); }, ms);
    if (signal.aborted) { clearTimeout(timer); reject(new DOMException("已取消等待", "AbortError")); return; }
    signal.addEventListener("abort", abort, { once: true });
  });
}
