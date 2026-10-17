"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Check, Copy, Link2Off, RefreshCw } from "lucide-react";
import { api, type Photo } from "@/lib/api";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { isSharePath, onSharingChanged, sharingChanged, startCopy } from "@/lib/sharing";
import { Spinner } from "@/components/ui/spinner";

interface ShareState { active: boolean; generation: number; version: string; path: string }

async function requestShare(endpoint: string, options?: RequestInit): Promise<ShareState> {
  const raw = await api<unknown>(endpoint, options);
  const state = raw as Partial<ShareState> | null;
  if (!state || typeof state !== "object" || typeof state.active !== "boolean" ||
      typeof state.generation !== "number" || !Number.isSafeInteger(state.generation) || state.generation < 0 ||
      typeof state.version !== "string" || !/^[a-f0-9]{64}$/.test(state.version) || typeof state.path !== "string" ||
      (state.active ? !isSharePath(state.path) : state.path !== "")) {
    throw new Error("未能确认分享结果，请重新核对。");
  }
  return state as ShareState;
}

export function PhotoSharing({ photo, enabled, onStopped }: { photo: Photo; enabled: boolean; onStopped: () => void }) {
  const [state, setState] = useState<ShareState | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [manual, setManual] = useState("");
  const [copied, setCopied] = useState(false);
  const lifetime = useRef<AbortController | null>(null);
  const busy = useRef(false);
  const endpoint = `/api/photos/${photo.id}/share`;

  const load = useCallback(async () => {
    const signal = lifetime.current?.signal;
    if (!signal || signal.aborted) return;
    setError("");
    try {
      const current = await requestShare(endpoint, { signal });
      if (signal.aborted) return;
      if (current.version !== photo.version) throw new Error("原图已改变，请刷新图片后重新分享。");
      setState(current);
    } catch (err) { if (!signal.aborted) setError(err instanceof Error ? err.message : "暂时无法核对分享状态。"); }
  }, [endpoint, photo.version]);

  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    busy.current = false; setPending(false); setState(null); setManual(""); setCopied(false); setNotice("");
    if (enabled) load();
    const stopListening = onSharingChanged(() => { if (enabled && !busy.current) load(); });
    return () => { controller.abort(); stopListening(); };
  }, [enabled, load]);
  useEffect(() => {
    if (!copied) return;
    const timeout = setTimeout(() => setCopied(false), 3000);
    return () => clearTimeout(timeout);
  }, [copied]);

  async function change(stop: boolean) {
    const signal = lifetime.current?.signal;
    if (!enabled || !state || busy.current || !signal || signal.aborted) return;
    busy.current = true;
    setPending(true); setError(""); setNotice(""); setManual(""); setCopied(false);
    // Start the clipboard call during the click (Safari), before the answer.
    const copy = stop ? null : startCopy();
    try {
      const next = await requestShare(endpoint, {
        method: stop ? "DELETE" : "POST", signal,
        body: JSON.stringify({ generation: state.generation, version: photo.version }),
      });
      if (signal.aborted) { copy?.cancel(new Error("照片已关闭")); return; }
      setState(next); sharingChanged("photo");
      if (stop) { setNotice("已停止分享，旧链接不再接受新访问。"); onStopped(); return; }
      const address = new URL(next.path, location.origin).href;
      const success = await copy!.finish(address, () => !signal.aborted);
      if (signal.aborted) return;
      setCopied(success);
      if (!success) { setManual(address); setNotice("分享已开启。浏览器未允许复制，请选中下方地址手动复制。"); }
    } catch (err) {
      copy?.cancel(err);
      if (signal.aborted) return;
      setState(null);
      setError(err instanceof Error ? err.message : "暂时无法完成分享操作。");
      // A lost answer does not prove the write failed. Re-read the same photo
      // before allowing another action or reporting its current permission.
      try {
        const current = await requestShare(endpoint, { signal });
        if (signal.aborted || current.version !== photo.version) return;
        setState(current);
        if (stop && !current.active) { setError(""); setNotice("已核对：分享已停止。"); onStopped(); }
        else if (!stop && current.active) setManual(new URL(current.path, location.origin).href);
      } catch { /* keep actions disabled until the person retries verification */ }
    } finally {
      if (lifetime.current?.signal === signal) {
        busy.current = false;
        if (!signal.aborted) setPending(false);
      }
    }
  }

  return <div className="photo-share-bar" aria-label="原图分享">
    <div className="flex flex-wrap justify-center gap-2">
      <Button variant="overlay" disabled={!enabled || !state || pending} onClick={() => change(false)}>
        {pending ? <Spinner data-icon="inline-start" aria-hidden="true" /> : copied ? <Check data-icon="inline-start" aria-hidden="true" /> : <Copy data-icon="inline-start" aria-hidden="true" />}
        {copied ? "已复制" : "复制分享链接"}
      </Button>
      {state?.active && <Button variant="overlay" disabled={!enabled || pending} onClick={() => change(true)}><Link2Off data-icon="inline-start" aria-hidden="true" />停止分享</Button>}
    </div>
    <p className="photo-share-status" role="status">{notice || (state?.active ? "已分享 · 持有链接即可查看原图" : state ? "复制后开启这张原图的分享" : error ? "未能核对分享状态" : "正在核对分享状态…")}</p>
    {manual && <FieldGroup className="photo-share-manual"><Field><FieldLabel htmlFor="share-address">图片地址</FieldLabel><Input id="share-address" readOnly value={manual} onFocus={event => event.currentTarget.select()} /><span className="text-sm text-muted-foreground">选中地址后复制即可。</span></Field></FieldGroup>}
    {error && <Alert variant="destructive" className="photo-share-error"><AlertTitle>分享操作暂未完成</AlertTitle><AlertDescription>{error}<Button variant="outline" disabled={pending} onClick={load}><RefreshCw data-icon="inline-start" aria-hidden="true" />重新核对</Button></AlertDescription></Alert>}
  </div>;
}
