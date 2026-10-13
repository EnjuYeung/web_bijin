"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Copy, Link2, Link2Off, RefreshCw, X } from "lucide-react";
import { api } from "@/lib/api";
import { batchLabels, checkBatch, listBatches, readBatch, sharingChanged, type ShareBatch } from "@/lib/share-batches";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Textarea } from "@/components/ui/textarea";
import { Spinner } from "@/components/ui/spinner";
import { OptionSelect } from "@/components/option-select";

export function BatchSharing({ albums }: { albums: string[] }) {
  const [tasks, setTasks] = useState<ShareBatch[]>([]);
  const [current, setCurrent] = useState<ShareBatch | null>(null);
  const [preview, setPreview] = useState<ShareBatch | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [manual, setManual] = useState<string[]>([]);
  const alive = useRef(false);
  const busy = useRef(false);
  const selection = JSON.stringify(albums);
  const latestSelection = useRef(selection); latestSelection.current = selection;
  const refresh = useCallback(async (id?: string) => {
    const list = await listBatches(); if (!alive.current) return;
    setTasks(list);
    const chosen = id || list.find(t => t.status === "running")?.id || list[0]?.id;
    if (chosen) { const task = await readBatch(chosen); if (alive.current) setCurrent(task); }
    else setCurrent(null);
  }, []);
  useEffect(() => { alive.current = true; refresh().catch(err => { if (alive.current) setError(err.message); }); return () => { alive.current = false; }; }, [refresh]);
  useEffect(() => { setPreview(null); }, [selection]);
  useEffect(() => {
    if (current?.status !== "running") return;
    let disposed = false;
    const timeout = setTimeout(async () => {
      try { const task = await readBatch(current.id); if (disposed || !alive.current) return; setError(""); setCurrent(task); setTasks(list => list.map(t => t.id === task.id ? task : t)); if (task.done !== current.done || task.status !== current.status) sharingChanged(); }
      catch (err) { if (!disposed && alive.current) { setError(err instanceof Error ? err.message : "进度暂时无法读取，请重新核对。"); setCurrent({ ...current }); } }
    }, 2000);
    return () => { disposed = true; clearTimeout(timeout); };
  }, [current]);
  const running = tasks.some(t => t.status === "running") || current?.status === "running";

  async function prepare(mode: "open" | "stop") {
    if (busy.current) return; busy.current = true; setPending(true); setError(""); setNotice(""); setManual([]);
    const selected = selection;
    try { const task = checkBatch(await api<unknown>("/api/share-batches/prepare", { method: "POST", body: JSON.stringify({ mode, albums }) })); if (alive.current && selected === latestSelection.current) setPreview(task); }
    catch (err) { if (alive.current) setError(err instanceof Error ? err.message : "暂时无法核对范围。"); }
    finally { busy.current = false; if (alive.current) setPending(false); }
  }
  async function change(task: ShareBatch, action: "start" | "retry" | "cancel") {
    if (busy.current) return; busy.current = true; setPending(true); setError(""); setNotice(""); setManual([]);
    try { const next = await readBatch(task.id + "/" + action, { method: "POST", body: JSON.stringify({ revision: task.revision }) }); if (!alive.current) return; setPreview(null); setCurrent(next); setTasks(list => [next, ...list.filter(t => t.id !== next.id)]); sharingChanged(); }
    catch (err) {
      if (!alive.current) return;
      setError(err instanceof Error ? err.message : "未能确认操作结果。");
      // A lost response is not a failed write. Inspect the same persisted task.
      try { const next = await readBatch(task.id); if (!alive.current) return; if (next.status !== "prepared") { setPreview(null); setCurrent(next); setTasks(list => [next, ...list.filter(t => t.id !== next.id)]); } }
      catch { /* retain the known id and offer verification */ }
    } finally { busy.current = false; if (alive.current) setPending(false); }
  }
  async function copy() {
    if (!current || busy.current) return; busy.current = true; setPending(true); setError(""); setNotice(""); setManual([]);
    // Safari needs clipboard activation at the click, before fetching addresses.
    let resolve: ((blob: Blob) => void) | undefined, reject: ((err: unknown) => void) | undefined;
    let writing: Promise<boolean> | undefined;
    if (navigator.clipboard?.write && typeof ClipboardItem !== "undefined") {
      const data = new Promise<Blob>((yes, no) => { resolve = yes; reject = no; }); data.catch(() => {});
      try { writing = navigator.clipboard.write([new ClipboardItem({ "text/plain": data })]).then(() => true, () => false); } catch { writing = Promise.resolve(false); }
    }
    try {
      const data = await api<{ paths?: string[]; excluded?: number }>(`/api/share-batches/${current.id}/links`);
      if (!data || !Array.isArray(data.paths) || !data.paths.every(p => /^\/i\/[A-Za-z0-9_-]{43}\.(jpg|png|webp|gif)$/.test(p))) throw new Error("未能核对分享地址。");
      const addresses = data.paths.map(p => new URL(p, location.origin).href);
      if (!addresses.length) { reject?.(new Error("没有有效地址")); setNotice("本次结果中没有仍有效的分享链接。"); return; }
      resolve?.(new Blob([addresses.join("\n")], { type: "text/plain" }));
      let copied = writing ? await writing : false;
      if (!copied) { try { await navigator.clipboard.writeText(addresses.join("\n")); copied = true; } catch { /* manual addresses */ } }
      if (!alive.current) return;
      if (!copied) setManual(addresses);
      setNotice(`${copied ? "已复制" : "浏览器未允许复制，请选中下方地址手动复制"} ${addresses.length} 个分享链接${data.excluded ? `，${data.excluded} 项未能核对或已失效` : ""}。`);
    } catch (err) { reject?.(err); if (alive.current) setError(err instanceof Error ? err.message : "复制暂未完成。"); }
    finally { busy.current = false; if (alive.current) setPending(false); }
  }
  const failed = current?.items?.filter(i => i.status === "failed") ?? [];
  return <section className="batch-sharing" aria-label="批量分享">
    {albums.length > 0 && <div className="flex flex-wrap items-center gap-2">
      <span>已选 {albums.length} 本相册</span>
      <Button disabled={pending || running} onClick={() => prepare("open")}><Link2 data-icon="inline-start" aria-hidden="true" />批量开启分享</Button>
      <Button variant="outline" disabled={pending || running} onClick={() => prepare("stop")}><Link2Off data-icon="inline-start" aria-hidden="true" />批量停止分享</Button>
      {pending && <Spinner aria-label="正在核对" />}
    </div>}
    {current && <div className="batch-result">
      <div className="flex flex-wrap items-center gap-2"><Badge variant="secondary">{batchLabels[current.status]}</Badge><span>{current.mode === "open" ? "开启" : "停止"}分享 · {current.albums.length} 本相册</span></div>
      {tasks.length > 1 && <OptionSelect id="share-task" label="查看批量任务" value={current.id} choices={tasks.map(t => ({ value: t.id, label: `${new Date(t.created * 1000).toLocaleString("zh-CN")} · ${t.mode === "open" ? "开启" : "停止"} · ${batchLabels[t.status]}` }))} onChange={id => { setManual([]); setNotice(""); refresh(id).catch(err => setError(err.message)); }} />}
      <p role="status" aria-atomic="true">已处理 {current.done} / {current.total} 张 · 成功 {current.succeeded} · 失败 {current.failed}{current.status === "interrupted" ? "，应用重启后未自动续跑，请重试剩余项。" : ""}</p>
      <progress max={Math.max(1, current.total)} value={current.done} aria-label="批量分享进度" />
      <div className="flex flex-wrap gap-2">
        {current.status === "running" && <Button variant="outline" disabled={pending} onClick={() => change(current, "cancel")}><X data-icon="inline-start" aria-hidden="true" />取消剩余处理</Button>}
        {current.status !== "running" && (current.failed > 0 || current.done < current.total) && <Button variant="outline" disabled={pending || running} onClick={() => change(current, "retry")}><RefreshCw data-icon="inline-start" aria-hidden="true" />{current.failed ? "重试失败及剩余项" : "重试剩余项"}</Button>}
        {current.mode === "open" && current.succeeded > 0 && <Button variant="outline" disabled={pending} onClick={copy}><Copy data-icon="inline-start" aria-hidden="true" />复制全部分享链接</Button>}
      </div>
      {!!failed.length && <details><summary>{failed.length} 张处理失败，查看原因</summary><ul>{failed.slice(0, 50).map(i => <li key={i.id}>{i.name}：{i.error}</li>)}</ul>{failed.length > 50 && <p>先显示 50 项；可以重试全部失败项。</p>}</details>}
      <p className="text-sm text-muted-foreground">关闭页面后继续处理；任务结束后的记录保留 48 小时。取消剩余处理会保留已完成结果。</p>
    </div>}
    {notice && <p role="status">{notice}</p>}
    {!!manual.length && <FieldGroup><Field><FieldLabel htmlFor="batch-addresses">全部分享地址</FieldLabel><Textarea id="batch-addresses" readOnly rows={6} value={manual.join("\n")} onFocus={e => e.currentTarget.select()} /><Button variant="outline" onClick={() => { const node = document.getElementById("batch-addresses") as HTMLTextAreaElement | null; node?.focus(); node?.select(); }}>选中全部地址</Button></Field></FieldGroup>}
    {error && <Alert variant="destructive"><AlertTitle>批量操作需要核对</AlertTitle><AlertDescription>{error}<Button variant="outline" disabled={pending} onClick={() => { setError(""); refresh(current?.id || preview?.id).catch(err => setError(err.message)); }}><RefreshCw data-icon="inline-start" aria-hidden="true" />重新核对任务</Button></AlertDescription></Alert>}
    <Dialog open={!!preview} onOpenChange={open => { if (!open && !pending) setPreview(null); }}>
      <DialogContent className="batch-confirm"><DialogHeader><DialogTitle>{preview?.mode === "open" ? "开启所选相册的原图分享" : "停止所选相册的原图分享"}</DialogTitle><DialogDescription>只处理本次列出的图片，以后新增图片需要再次操作。</DialogDescription></DialogHeader>
        {preview && <><ul className="batch-albums">{preview.albums.map(id => <li key={id}>{id === "." ? "根目录" : id}</li>)}</ul><p>{preview.albums.length} 本相册 · 共 {preview.total} 张 · 已分享 {preview.shared} 张</p><p>预计{preview.mode === "open" ? "新增" : "停止"}分享 {preview.changes} 张{preview.mode === "open" ? "，已有有效分享链接复用。" : "，包含以前逐张手动开启的分享。"}</p><DialogFooter><Button variant="outline" disabled={pending} onClick={() => setPreview(null)}>返回</Button><Button disabled={pending} onClick={() => change(preview, "start")}>{pending && <Spinner data-icon="inline-start" aria-hidden="true" />}确认{preview.mode === "open" ? "开启" : "停止"}分享</Button></DialogFooter></>}
      </DialogContent>
    </Dialog>
  </section>;
}
