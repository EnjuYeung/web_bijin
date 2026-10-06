"use client";

import { useEffect, useRef, useState } from "react";
import { CircleAlert, Cloud, FolderPlus, HardDrive, ImagePlus, RefreshCw, Trash2, Upload, X } from "lucide-react";
import { api, humanSize } from "@/lib/api";
import { dropSelection, folderSelection, photoSelection, sendPhoto, uploadDelay, type PreparedUpload, type Selection, type UploadTask, type UploadTarget } from "@/lib/upload";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Spinner } from "@/components/ui/spinner";
import { OptionSelect } from "@/components/option-select";

type Phase = "pending" | "uploading" | "processing" | "done" | "skipped" | "error" | "cancelled";
interface Row { id: string; file: File; path: string; phase: Phase; percent: number; message?: string; task?: UploadTask }
interface Targets { targets: UploadTarget[]; maxBytes: number; maxFiles: number }
const labels: Record<Phase, string> = { pending: "等待上传", uploading: "上传中", processing: "图库处理中", done: "处理完成", skipped: "已跳过", error: "失败", cancelled: "已停止" };
const accept = ".jpg,.jpeg,.png,.webp,.gif";

export default function UploadPanel() {
  const [settings, setSettings] = useState<Targets | null>(null);
  const [target, setTarget] = useState("");
  const [directory, setDirectory] = useState("");
  const [rows, setRows] = useState<Row[]>([]);
  const [busy, setBusy] = useState(false);
  const [reading, setReading] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [error, setError] = useState("");
  const [note, setNote] = useState("");
  const [visible, setVisible] = useState(100);
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const controllers = useRef(new Set<AbortController>());
  const stopped = useRef(false);
  const busyRef = useRef(false);
  const alive = useRef(true);
  const rowsRef = useRef<Row[]>([]);
  useEffect(() => { rowsRef.current = rows; }, [rows]);

  const load = async (signal?: AbortSignal) => {
    try {
      setError("");
      const data = await api<Targets>("/api/upload-targets", { signal });
      if (!alive.current) return;
      setSettings(data);
      setTarget(previous => previous || data.targets.find(item => item.kind === "s3")?.id || "local");
    } catch (err) {
      if (!signal?.aborted && alive.current) setError(err instanceof Error ? err.message : "无法读取上传目标");
    }
  };
  useEffect(() => {
    alive.current = true;
    const controller = new AbortController();
    void load(controller.signal);
    return () => { alive.current = false; controller.abort(); controllers.current.forEach(item => item.abort()); };
  }, []);
  useEffect(() => {
    if (!busy) return;
    const beforeUnload = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    addEventListener("beforeunload", beforeUnload);
    return () => removeEventListener("beforeunload", beforeUnload);
  }, [busy]);

  function update(id: string, change: Partial<Row>) {
    if (!alive.current) return;
    setRows(previous => {
      const next = previous.map(item => item.id === id ? { ...item, ...change } : item);
      rowsRef.current = next;
      return next;
    });
  }
  function add(selection: Selection) {
    if (!settings || busyRef.current || !alive.current) return;
    let tooLarge = 0, duplicates = 0;
    const existing = new Set(rowsRef.current.map(item => item.path));
    const additions: Row[] = [];
    for (const item of selection.items) {
      if (!item.file.size || item.file.size > settings.maxBytes) { tooLarge++; continue; }
      if (existing.has(item.path)) { duplicates++; continue; }
      existing.add(item.path);
      additions.push({ ...item, id: crypto.randomUUID(), phase: "pending", percent: 0 });
    }
    if (rowsRef.current.length + additions.length > settings.maxFiles) {
      setError("每批最多添加 " + settings.maxFiles.toLocaleString("zh-CN") + " 张图片");
      return;
    }
    setRows(previous => { const next = [...previous, ...additions]; rowsRef.current = next; return next; });
    setError("");
    const skipped = [
      selection.skippedFolders ? "跳过 " + selection.skippedFolders + " 个子文件夹或隐藏文件夹" : "",
      selection.skippedFiles ? "跳过 " + selection.skippedFiles + " 个非图片或隐藏文件" : "",
      tooLarge ? "跳过 " + tooLarge + " 个空文件或超过 50 MB 的图片" : "",
      duplicates ? "忽略 " + duplicates + " 个重复选择" : "",
    ].filter(Boolean);
    setNote("新增 " + additions.length + " 张图片" + (skipped.length ? "；" + skipped.join("，") : ""));
  }
  async function selectFolder() {
    const picker = (window as Window & { showDirectoryPicker?: () => Promise<FileSystemDirectoryHandle> }).showDirectoryPicker;
    if (!picker) { folderInput.current?.click(); return; }
    try {
      const handle = await picker.call(window);
      setReading(true);
      add(await folderSelection(handle as FileSystemDirectoryHandle & { values(): AsyncIterable<FileSystemHandle> }));
    } catch (err) {
      if ((err as Error).name !== "AbortError") setError("无法读取文件夹，请重试或拖入文件夹");
    } finally { if (alive.current) setReading(false); }
  }
  async function drop(event: React.DragEvent) {
    event.preventDefault();
    setDragging(false);
    if (busyRef.current || reading || !settings) return;
    const files = Array.from(event.dataTransfer.files);
    const entries = Array.from(event.dataTransfer.items).map(item => item.webkitGetAsEntry?.()).filter((item): item is FileSystemEntry => !!item);
    setReading(true);
    try { add(await dropSelection(entries, files)); } catch { setError("无法读取拖入的文件夹，请重试"); }
    finally { if (alive.current) setReading(false); }
  }

  async function finishProcessing(row: Row, task: UploadTask, signal: AbortSignal) {
    const deadline = Date.now() + 120_000;
    while (task.phase === "processing") {
      update(row.id, { phase: "processing", percent: 100, task });
      await uploadDelay(signal);
      task = (await api<{ task: UploadTask }>("/api/uploads/" + task.id, { signal })).task;
      if (Date.now() > deadline && task.phase === "processing") throw new Error("图片已保存，后台仍在整理；可以稍后重试查看进度");
    }
    update(row.id, { task });
    if (task.phase === "error") throw new Error(task.message || "图库处理失败");
    update(row.id, { phase: task.phase === "skipped" ? "skipped" : "done", percent: 100, message: task.message, task });
  }
  async function process(row: Row) {
    const controller = new AbortController();
    const signal = controller.signal;
    controllers.current.add(controller);
    try {
      if (row.task?.saved) {
        let task = (await api<{ task: UploadTask }>("/api/uploads/" + row.task.id, { signal })).task;
        if (task.phase === "error" && task.retryable) task = (await api<{ task: UploadTask }>("/api/uploads/" + task.id + "/retry", { method: "POST", body: "{}", signal })).task;
        await finishProcessing(row, task, signal);
        return;
      }
      update(row.id, { phase: "uploading", percent: 0, message: undefined });
      const prepared = await api<PreparedUpload>("/api/uploads", { method: "POST", body: JSON.stringify({ target, directory: directory.trim(), path: row.path, size: row.file.size }), signal });
      update(row.id, { task: prepared.task });
      if (prepared.task.phase === "skipped") { update(row.id, { phase: "skipped", percent: 100, message: prepared.task.message }); return; }
      if (prepared.task.phase === "error") throw new Error(prepared.task.message);
      await sendPhoto(prepared, row.file, signal, percent => update(row.id, { percent }));
      update(row.id, { phase: "processing", percent: 100 });
      const completed = await api<{ task: UploadTask }>("/api/uploads/" + prepared.task.id + "/complete", { method: "POST", body: "{}", signal });
      update(row.id, { task: completed.task });
      await finishProcessing(row, completed.task, signal);
    } catch (err) {
      const current = rowsRef.current.find(item => item.id === row.id);
      if (signal.aborted) {
        update(row.id, { phase: "cancelled", message: current?.task?.saved ? "图片已保存，后台继续整理" : "已停止，可重新开始" });
      } else {
        update(row.id, { phase: "error", message: err instanceof Error ? err.message : "上传失败，请重试" });
      }
    } finally { controllers.current.delete(controller); }
  }
  async function start(only?: string) {
    if (busyRef.current || !settings) return;
    const pending = rowsRef.current.filter(row => (!only || row.id === only) && ["pending", "error", "cancelled"].includes(row.phase) && row.task?.retryable !== false);
    if (!pending.length) return;
    stopped.current = false;
    busyRef.current = true;
    setBusy(true);
    setError("");
    let cursor = 0;
    await Promise.all(Array.from({ length: Math.min(4, pending.length) }, async () => {
      while (!stopped.current && cursor < pending.length) await process(pending[cursor++]);
    }));
    busyRef.current = false;
    if (alive.current) {
      setBusy(false);
      setNote(stopped.current ? "已停止本批上传。已保存的图片会继续在后台整理。" : "本批任务已结束，请查看处理结果。");
    }
  }
  function cancel() { stopped.current = true; controllers.current.forEach(controller => controller.abort()); }
  const totalBytes = rows.reduce((sum, row) => sum + row.file.size, 0);
  const sentBytes = rows.reduce((sum, row) => sum + row.file.size * row.percent / 100, 0);
  const ready = rows.filter(row => row.phase === "done").length;
  const skipped = rows.filter(row => row.phase === "skipped").length;
  const failed = rows.filter(row => row.phase === "error").length;
  const pending = rows.some(row => ["pending", "error", "cancelled"].includes(row.phase) && row.task?.retryable !== false);
  const percent = totalBytes ? Math.round(sentBytes / totalBytes * 100) : 0;

  return <div className="upload-panel">
    {error && <Alert variant="destructive" id="upload-error"><CircleAlert aria-hidden="true" /><AlertDescription>{error}{!settings && <Button variant="outline" onClick={() => void load()}>重试</Button>}</AlertDescription></Alert>}
    <Card className="source-card">
      <CardHeader><CardTitle><h2><Upload aria-hidden="true" />上传图片</h2></CardTitle><CardDescription>选择保存位置，图片保存后会自动整理进相册。</CardDescription></CardHeader>
      <CardContent className="upload-content">
        {!settings ? <p role="status"><Spinner aria-label="正在读取上传目标" />正在读取保存位置…</p> : <>
          <div className="upload-targets">
            <Field><FieldLabel htmlFor="upload-target">保存位置</FieldLabel><OptionSelect id="upload-target" label="保存位置" value={target} choices={settings.targets.map(item => ({ value: item.id, label: (item.kind === "local" ? "本地 · " : "S3 · ") + item.name }))} disabled={busy || reading || rows.some(row => !!row.task)} onChange={setTarget} /><FieldDescription>{target === "local" ? "保存到 .env 配置的本地照片目录。" : "使用现有对象存储配置及 Key 上传。"}</FieldDescription></Field>
            <Field><FieldLabel htmlFor="upload-directory">目标子目录（可选）</FieldLabel><Input id="upload-directory" placeholder="例如 2026/旅行" value={directory} disabled={busy || reading || rows.some(row => !!row.task)} onChange={event => setDirectory(event.target.value)} /><FieldDescription>留空保存到所选存储根目录。文件夹上传保留文件夹名。</FieldDescription></Field>
          </div>
          <input id="upload-file-input" ref={fileInput} type="file" accept={accept} multiple hidden aria-label="选择图片" onChange={event => { add(photoSelection(Array.from(event.target.files || []))); event.target.value = ""; }} />
          <input id="upload-folder-input" ref={folderInput} type="file" multiple hidden aria-label="添加文件夹" {...({ webkitdirectory: "" } as React.InputHTMLAttributes<HTMLInputElement>)} onChange={event => { add(photoSelection(Array.from(event.target.files || []), true)); event.target.value = ""; }} />
          <div id="upload-drop" className={"upload-drop" + (dragging ? " is-dragging" : "")} onDrop={event => void drop(event)} onDragOver={event => { event.preventDefault(); if (!busy && !reading) setDragging(true); }} onDragLeave={event => { if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDragging(false); }}>
            {target === "local" ? <HardDrive aria-hidden="true" /> : <Cloud aria-hidden="true" />}
            <p>拖入图片或多个文件夹</p>
            <span>只取所选文件夹的直属图片，子文件夹会跳过。支持 JPG、JPEG、PNG、WebP、GIF，每张最多 50 MB。</span>
            <div className="upload-buttons"><Button id="upload-select-files" variant="outline" disabled={busy || reading} onClick={() => fileInput.current?.click()}><ImagePlus aria-hidden="true" data-icon="inline-start" />选择图片</Button><Button id="upload-select-folder" variant="outline" disabled={busy || reading} onClick={() => void selectFolder()}><FolderPlus aria-hidden="true" data-icon="inline-start" />添加文件夹</Button></div>
          </div>
        </>}
        {reading && <p role="status"><Spinner aria-label="正在读取文件" />正在读取所选文件夹…</p>}
        <p id="upload-note" className="upload-note" role="status" aria-live="polite">{note || "可连续添加图片或文件夹，最后统一上传。GIF 不生成壁纸。"}</p>
      </CardContent>
    </Card>
    {rows.length > 0 && <Card className="source-card">
      <CardHeader><CardTitle><h2>上传任务</h2></CardTitle><CardDescription id="upload-summary" aria-live="polite">{rows.length.toLocaleString("zh-CN")} 张 · {humanSize(totalBytes)} · 处理完成 {ready} · 跳过 {skipped}{failed > 0 ? " · 失败 " + failed : ""}</CardDescription></CardHeader>
      <CardContent className="upload-content">
        <div className="upload-total"><label htmlFor="upload-total-progress">传输进度 {percent}%</label><progress id="upload-total-progress" max={100} value={percent} /><span>{humanSize(sentBytes)} / {humanSize(totalBytes)}</span></div>
        <div className="upload-actions"><Button id="upload-start" disabled={busy || reading || !pending} onClick={() => void start()}>{busy && <Spinner aria-label="正在上传或处理" data-icon="inline-start" />}<Upload aria-hidden="true" data-icon="inline-start" />{busy ? "上传与整理中…" : rows.some(row => row.phase === "error") ? "上传 / 重试未完成项" : "开始上传"}</Button>{busy ? <Button id="upload-cancel" variant="outline" onClick={cancel}><X aria-hidden="true" data-icon="inline-start" />停止本批</Button> : <Button id="upload-clear" variant="ghost" onClick={() => { setRows([]); rowsRef.current = []; setVisible(100); setNote(""); }}><Trash2 aria-hidden="true" data-icon="inline-start" />清空列表</Button>}<Button variant="ghost" render={<a href="/?view=albums" />} nativeButton={false}>查看相册</Button></div>
        <ul id="upload-list" className="upload-list" aria-label="上传图片列表">{rows.slice(0, visible).map(row => <li key={row.id} data-phase={row.phase}>
          <div className="upload-file"><strong title={row.path}>{row.path}</strong><span>{humanSize(row.file.size)}</span></div>
          <div className="upload-state"><span>{labels[row.phase]}{row.phase === "uploading" ? " " + Math.round(row.percent) + "%" : ""}</span>{row.phase === "uploading" && <progress max={100} value={row.percent} aria-label={row.path + " 上传进度"} />}{row.message && <small>{row.message}</small>}</div>
          <div className="upload-row-actions">{!busy && row.phase === "error" && row.task?.retryable !== false && <Button variant="ghost" size="icon" aria-label={"重试 " + row.path} title="重试" onClick={() => void start(row.id)}><RefreshCw aria-hidden="true" /></Button>}{!busy && <Button variant="ghost" size="icon" aria-label={"移除 " + row.path} title="从列表移除" onClick={() => setRows(previous => { const next = previous.filter(item => item.id !== row.id); rowsRef.current = next; return next; })}><X aria-hidden="true" /></Button>}</div>
        </li>)}</ul>
        {rows.length > visible && <Button variant="outline" onClick={() => setVisible(value => value + 100)}>显示更多（还有 {rows.length - visible} 项）</Button>}
      </CardContent>
    </Card>}
  </div>;
}
