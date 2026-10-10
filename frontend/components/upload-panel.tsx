"use client";

import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { CircleAlert, Cloud, FolderPlus, HardDrive, ImagePlus, RefreshCw, Trash2, Upload, X } from "lucide-react";
import { api, humanSize } from "@/lib/api";
import { dropSelection, folderSelection, photoSelection, type UploadTarget } from "@/lib/upload";
import { createUploadBatch, type UploadPhase } from "@/lib/upload-batch";
import { uploadHTTP } from "@/lib/upload-http";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Spinner } from "@/components/ui/spinner";
import { OptionSelect } from "@/components/option-select";

interface Targets { targets: UploadTarget[]; maxBytes: number; maxFiles: number }
const labels: Record<UploadPhase, string> = { pending: "等待上传", uploading: "上传中", processing: "照片处理中", done: "处理完成", skipped: "已跳过", error: "失败", cancelled: "已停止", uncertain: "待确认", waiting: "原图已保存，等待处理", expired: "任务已失效" };
const accept = ".jpg,.jpeg,.png,.webp,.gif";

export default function UploadPanel() {
  const [settings, setSettings] = useState<Targets | null>(null);
  const [error, setError] = useState("");
  const loading = useRef<AbortController | null>(null);
  async function load() {
    loading.current?.abort();
    const controller = new AbortController(); loading.current = controller;
    setError("");
    try {
      const data = await api<Targets>("/api/upload-targets", { signal: AbortSignal.any([controller.signal, AbortSignal.timeout(30_000)]) });
      if (!Array.isArray(data.targets) || !(data.maxBytes > 0) || !(data.maxFiles > 0)) throw new Error("保存位置响应不完整，请重试");
      if (!controller.signal.aborted) setSettings(data);
    } catch (err) { if (!controller.signal.aborted) setError(err instanceof Error ? err.message : "无法读取上传目标"); }
  }
  useEffect(() => { void load(); return () => loading.current?.abort(); }, []);
  if (settings) return <UploadContents settings={settings} />;
  return <div className="upload-panel">
    {error && <Alert variant="destructive" id="upload-error"><CircleAlert aria-hidden="true" /><AlertDescription>{error}<Button variant="outline" onClick={() => void load()}>重试</Button></AlertDescription></Alert>}
    <Card className="source-card"><CardHeader><CardTitle><h2><Upload aria-hidden="true" />上传图片</h2></CardTitle></CardHeader><CardContent><p role="status"><Spinner aria-label="正在读取上传目标" />正在读取保存位置…</p></CardContent></Card>
  </div>;
}

function UploadContents({ settings }: { settings: Targets }) {
  const [batch] = useState(() => createUploadBatch({ adapter: uploadHTTP, limits: settings, initialDestination: { target: settings.targets.find(item => item.kind === "s3")?.id || "local", directory: "" } }));
  const state = useSyncExternalStore(batch.subscribe, batch.getSnapshot, batch.getSnapshot);
  const { rows, destination: { target, directory }, running: busy, reading, configurationLocked, error, note, totalBytes, transferBytes, sentBytes, percent, ready, skipped, failed, canStart: pending } = state;
  const [dragging, setDragging] = useState(false);
  const [visible, setVisible] = useState(100);
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  useEffect(() => {
    const beforeLeave = (event: BeforeUnloadEvent) => {
      if (batch.getSnapshot().hasPending) {
        event.preventDefault(); event.returnValue = "";
        // Browsers cancel transport before pagehide when leaving is accepted.
        // A rendered frame resumes requests only if the person stays here.
        if (event.isTrusted) requestAnimationFrame(batch.hold());
      }
    };
    addEventListener("beforeunload", beforeLeave);
    // Native navigation does not run React's effect cleanup. Stop on pagehide
    // only after leaving is accepted, so choosing to stay keeps work running.
    addEventListener("pagehide", batch.stop);
    return () => { removeEventListener("beforeunload", beforeLeave); removeEventListener("pagehide", batch.stop); batch.stop(); };
  }, [batch]);
  async function selectFolder() {
    const picker = (window as Window & { showDirectoryPicker?: () => Promise<FileSystemDirectoryHandle> }).showDirectoryPicker;
    if (!picker) { folderInput.current?.click(); return; }
    await batch.select(async () => {
      const handle = await picker.call(window);
      return folderSelection(handle as FileSystemDirectoryHandle & { values(): AsyncIterable<FileSystemHandle> });
    });
  }
  function drop(event: React.DragEvent) {
    event.preventDefault(); setDragging(false);
    if (busy || reading) return;
    const files = Array.from(event.dataTransfer.files);
    const entries = Array.from(event.dataTransfer.items).map(item => item.webkitGetAsEntry?.()).filter((item): item is FileSystemEntry => !!item);
    void batch.select(() => dropSelection(entries, files));
  }

  return <div className="upload-panel">
    {error && <Alert variant="destructive" id="upload-error"><CircleAlert aria-hidden="true" /><AlertDescription>{error}</AlertDescription></Alert>}
    <Card className="source-card">
      <CardHeader><CardTitle><h2><Upload aria-hidden="true" />上传图片</h2></CardTitle><CardDescription>选择保存位置，图片保存后会自动整理进相册。</CardDescription></CardHeader>
      <CardContent className="upload-content">
        {!settings ? <p role="status"><Spinner aria-label="正在读取上传目标" />正在读取保存位置…</p> : <>
          <div className="upload-targets">
            <Field><FieldLabel htmlFor="upload-target">保存位置</FieldLabel><OptionSelect id="upload-target" label="保存位置" value={target} choices={settings.targets.map(item => ({ value: item.id, label: (item.kind === "local" ? "本地 · " : "S3 · ") + item.name }))} disabled={configurationLocked} onChange={value => batch.setDestination({ target: value, directory })} /><FieldDescription>{target === "local" ? "保存到 .env 配置的本地照片目录。" : "使用现有对象存储配置及 Key 上传。"}</FieldDescription></Field>
            <Field><FieldLabel htmlFor="upload-directory">目标子目录（可选）</FieldLabel><Input id="upload-directory" placeholder="例如 2026/旅行" value={directory} disabled={configurationLocked} onChange={event => batch.setDestination({ target, directory: event.target.value })} /><FieldDescription>留空保存到所选存储根目录。文件夹上传保留文件夹名。</FieldDescription></Field>
          </div>
          <input id="upload-file-input" ref={fileInput} type="file" accept={accept} multiple hidden aria-label="选择图片" onChange={event => { const files = Array.from(event.target.files || []); void batch.select(() => photoSelection(files)); event.target.value = ""; }} />
          <input id="upload-folder-input" ref={folderInput} type="file" multiple hidden aria-label="添加文件夹" {...({ webkitdirectory: "" } as React.InputHTMLAttributes<HTMLInputElement>)} onChange={event => { const files = Array.from(event.target.files || []); void batch.select(() => photoSelection(files, true)); event.target.value = ""; }} />
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
        <div className="upload-total"><label htmlFor="upload-total-progress">{transferBytes ? "传输进度 " + percent + "%" : "无需传输"}</label><progress id="upload-total-progress" max={100} value={percent} /><span>{humanSize(sentBytes)} / {humanSize(transferBytes)}</span></div>
        <div className="upload-actions"><Button id="upload-start" disabled={busy || reading || !pending} onClick={() => batch.start()}>{busy && <Spinner aria-label="正在上传或处理" data-icon="inline-start" />}<Upload aria-hidden="true" data-icon="inline-start" />{busy ? "上传与整理中…" : rows.some(row => row.phase !== "pending") ? "开始 / 继续未完成项" : "开始上传"}</Button>{busy ? <Button id="upload-cancel" variant="outline" onClick={batch.stop}><X aria-hidden="true" data-icon="inline-start" />停止本批</Button> : <Button id="upload-clear" variant="ghost" onClick={() => { batch.clear(); setVisible(100); }}><Trash2 aria-hidden="true" data-icon="inline-start" />清空列表</Button>}<Button variant="ghost" render={<a href="/?view=albums" />} nativeButton={false}>查看相册</Button></div>
        <ul id="upload-list" className="upload-list" aria-label="上传图片列表">{rows.slice(0, visible).map(row => <li key={row.id} data-phase={row.phase}>
          <div className="upload-file"><strong title={row.path}>{row.path}</strong><span>{humanSize(row.size)}</span></div>
          <div className="upload-state"><span>{labels[row.phase]}{row.phase === "uploading" ? " " + Math.round(row.percent) + "%" : ""}</span>{row.phase === "uploading" && <progress max={100} value={row.percent} aria-label={row.path + " 上传进度"} />}{row.message && <small>{row.message}</small>}</div>
          <div className="upload-row-actions">{row.canResume && row.phase !== "pending" && <Button variant="ghost" size="icon" aria-label={row.actionLabel + " " + row.path} title={row.actionLabel} onClick={() => batch.start(row.id)}><RefreshCw aria-hidden="true" /></Button>}{!busy && <Button variant="ghost" size="icon" aria-label={"移除 " + row.path} title="从列表移除" onClick={() => batch.remove(row.id)}><X aria-hidden="true" /></Button>}</div>
        </li>)}</ul>
        {rows.length > visible && <Button variant="outline" onClick={() => setVisible(value => value + 100)}>显示更多（还有 {rows.length - visible} 项）</Button>}
      </CardContent>
    </Card>}
  </div>;
}
