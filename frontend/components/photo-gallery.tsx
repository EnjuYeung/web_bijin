"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ChevronLeft, ChevronRight, Images, Info, RefreshCw, X } from "lucide-react";
import { api, humanSize, photoDate, type Photo, type PhotoPage } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { GallerySkeleton } from "@/components/gallery-skeleton";

function photoHash() { const match = /^#p\/(\d+)$/.exec(location.hash); return match ? Number(match[1]) : null; }

export function PhotoGallery({ album, onCount, onTitle }: { album: string | null; onCount: (total: number) => void; onTitle: (title: string) => void }) {
  const [photos, setPhotos] = useState<Photo[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [width, setWidth] = useState(0);
  const [viewport, setViewport] = useState({ y: 0, h: 800, top: 80 });
  const [activeID, setActiveID] = useState<number | null>(null);
  const [previewID, setPreviewID] = useState<number | null>(null);
  const [infoOpen, setInfoOpen] = useState(true);
  const grid = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement | null>(null);
  const scroll = useRef(0);
  const touch = useRef({ x: 0, until: 0 });
  const pendingStep = useRef(false);
  const paging = useRef({ next: "", seed: "", busy: false, done: false, controller: null as AbortController | null });

  const loadMore = useCallback(async () => {
    const state = paging.current;
    if (state.busy || state.done) return;
    state.busy = true;
    const controller = new AbortController();
    state.controller = controller;
    setError("");
    try {
      const query = new URLSearchParams({ limit: "40" });
      if (state.seed) query.set("seed", state.seed);
      if (state.next) query.set("after", state.next);
      if (album !== null) query.set("album", album);
      const data = await api<PhotoPage>("/api/photos?" + query, { signal: controller.signal });
      if (controller.signal.aborted) return;
      state.seed = data.seed;
      state.next = data.next || "";
      state.done = !state.next;
      setPhotos(previous => { const ids = new Set(previous.map(photo => photo.id)); return [...previous, ...data.photos.filter(photo => !ids.has(photo.id))]; });
      onCount(data.total);
      if (data.album?.name) onTitle(data.album.name);
      setNotice(data.status.scanning ? "正在整理新照片，完成后刷新即可查看。" : "");
      setLoaded(true);
    } catch (err) {
      if (!controller.signal.aborted) setError(err instanceof Error ? err.message : "暂时连不上相册服务。");
    } finally { if (state.controller === controller) state.busy = false; }
  }, [album, onCount, onTitle]);

  useEffect(() => { loadMore(); return () => { paging.current.controller?.abort(); paging.current.busy = false; }; }, [loadMore]);
  useEffect(() => {
    if (!grid.current) return;
    let frame = 0;
    const measure = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        if (!grid.current) return;
        setWidth(grid.current.clientWidth);
        setViewport({ y: scrollY, h: innerHeight, top: grid.current.getBoundingClientRect().top + scrollY });
      });
    };
    const observer = new ResizeObserver(measure);
    observer.observe(grid.current);
    addEventListener("scroll", measure, { passive: true });
    addEventListener("resize", measure);
    measure();
    return () => { observer.disconnect(); cancelAnimationFrame(frame); removeEventListener("scroll", measure); removeEventListener("resize", measure); };
  }, []);

  const masonry = useMemo(() => {
    const columns = width < 560 ? 2 : width < 900 ? 3 : 4;
    const cardWidth = Math.max(0, (width - 8 * (columns - 1)) / columns);
    const heights = Array<number>(columns).fill(0);
    const positions = photos.map(photo => {
      const column = heights.indexOf(Math.min(...heights));
      const height = photo.w > 0 ? cardWidth * photo.h / photo.w : cardWidth;
      const position = { photo, x: column * (cardWidth + 8), y: heights[column], w: cardWidth, h: height };
      heights[column] += height + 8;
      return position;
    });
    return { positions, height: Math.max(0, ...heights) - (photos.length ? 8 : 0) };
  }, [photos, width]);
  const visible = masonry.positions.filter(item => item.y + item.h + viewport.top >= viewport.y - viewport.h && item.y + viewport.top <= viewport.y + viewport.h * 2);
  const selected = photos.find(photo => photo.id === activeID);
  const preview = selected || photos.find(photo => photo.id === previewID);
  const selectedIndex = photos.findIndex(photo => photo.id === activeID);

  useEffect(() => {
    const last = masonry.positions[masonry.positions.length - 1];
    if (last && last.y + viewport.top < viewport.y + viewport.h * 3) loadMore();
    if (activeID && !selected && loaded && !paging.current.done) loadMore();
    if (activeID && !selected && loaded && paging.current.done) setNotice("找不到这张照片，它可能已经移走。可以继续浏览其他照片。");
  }, [masonry, viewport, activeID, selected, loaded, loadMore]);

  const closePhoto = useCallback(() => {
    if (activeID === null) return;
    setActiveID(null);
    if (history.state?.bijinPhoto) history.back();
    else history.replaceState(null, "", location.pathname + location.search);
  }, [activeID]);
  const openPhoto = useCallback((photo: Photo, button?: HTMLButtonElement, replace = false) => {
    if (button) {
      trigger.current = button;
      scroll.current = scrollY;
      setInfoOpen(matchMedia("(min-width:720px)").matches);
    }
    const state = { bijinPhoto: true, y: scroll.current };
    if (replace) history.replaceState(state, "", "#p/" + photo.id);
    else history.pushState(state, "", "#p/" + photo.id);
    setActiveID(photo.id);
    setPreviewID(photo.id);
  }, []);
  const step = useCallback((delta: number) => {
    const target = photos[selectedIndex + delta];
    if (target) openPhoto(target, undefined, true);
    else if (delta > 0 && !paging.current.done) { pendingStep.current = true; loadMore(); }
  }, [photos, selectedIndex, openPhoto, loadMore]);
  useEffect(() => {
    if (pendingStep.current && photos[selectedIndex + 1]) { pendingStep.current = false; openPhoto(photos[selectedIndex + 1], undefined, true); }
  }, [photos, selectedIndex, openPhoto]);
  useEffect(() => {
    const sync = () => {
      const id = photoHash();
      setActiveID(id);
      if (id) { setPreviewID(id); setInfoOpen(matchMedia("(min-width:720px)").matches); }
      if (!id) requestAnimationFrame(() => scrollTo(0, history.state?.y ?? scroll.current));
    };
    sync();
    addEventListener("popstate", sync);
    addEventListener("hashchange", sync);
    return () => { removeEventListener("popstate", sync); removeEventListener("hashchange", sync); };
  }, []);

  return <Dialog open={!!selected} onOpenChange={open => { if (!open) closePhoto(); }}>
    {error && <Alert variant="destructive"><AlertTitle>照片暂时读不出来</AlertTitle><AlertDescription>{error}<Button variant="outline" onClick={loadMore}><RefreshCw data-icon="inline-start" aria-hidden="true" />重试</Button></AlertDescription></Alert>}
    {notice && <p className="note" role="status">{notice}</p>}
    {!loaded && !error && <GallerySkeleton />}
    {loaded && !photos.length && <Empty><EmptyHeader><EmptyMedia variant="icon"><Images aria-hidden="true" /></EmptyMedia><EmptyTitle>还没有照片</EmptyTitle><EmptyDescription>添加本地或云端照片，扫描后就能在这里浏览。</EmptyDescription></EmptyHeader></Empty>}
    <div id="grid" className="photo-grid" ref={grid} style={{ height: Math.max(0, masonry.height) }}>
      {visible.map(({ photo, x, y, w, h }) => <button key={photo.id} className="sheet" data-id={photo.id} type="button" aria-label={photo.title || photo.name} style={{ transform: `translate(${x}px,${y}px)`, width: w, height: h }} onClick={event => openPhoto(photo, event.currentTarget)}>
        <img src={photo.thumb} width={photo.w} height={photo.h} alt="" decoding="async" />
        <span className="sheet-meta"><strong>{photo.title || photo.name}</strong><span>{photoDate(photo)}</span><small>{photo.format} · {photo.w}×{photo.h} · {humanSize(photo.size)}</small></span>
      </button>)}
    </div>
    <DialogContent className="photo-dialog" showCloseButton={false} finalFocus={() => trigger.current?.isConnected ? trigger.current : document.getElementById("main-content")} onKeyDown={event => {
      if (event.key === "ArrowLeft" || event.key === "ArrowRight") { event.preventDefault(); event.stopPropagation(); step(event.key === "ArrowLeft" ? -1 : 1); }
    }}>
      <DialogHeader className="sr-only"><DialogTitle>{preview?.title || preview?.name || "照片"}</DialogTitle><DialogDescription>点击照片或关闭按钮返回；左右方向键切换照片。</DialogDescription></DialogHeader>
      <DialogClose render={<Button variant="overlay" size="icon" className="photo-close" id="lb-close" aria-label="返回照片" title="返回照片" />}><X aria-hidden="true" /></DialogClose>
      {preview && <><div className="photo-stage" onTouchStart={event => { touch.current.x = event.changedTouches[0]?.clientX || 0; }} onTouchEnd={event => {
        const delta = (event.changedTouches[0]?.clientX || 0) - touch.current.x;
        if (Math.abs(delta) > 50) { touch.current.until = Date.now() + 400; step(delta > 0 ? -1 : 1); }
      }}>
        <Button variant="overlay" size="icon" className="photo-prev" id="lb-prev" disabled={selectedIndex === 0} aria-label="上一张" onClick={() => step(-1)}><ChevronLeft aria-hidden="true" /></Button>
        <img id="lb-img" src={preview.src} alt={preview.title || preview.name} onClick={() => { if (Date.now() >= touch.current.until) closePhoto(); }} />
        <Button variant="overlay" size="icon" className="photo-next" id="lb-next" disabled={selectedIndex === photos.length - 1 && paging.current.done} aria-label="下一张" onClick={() => step(1)}><ChevronRight aria-hidden="true" /></Button>
        <p className="photo-caption">{preview.title || preview.name}</p>
      </div>
      {infoOpen && <aside className="photo-info" id="lb-meta"><h2>照片信息</h2><dl>
        <div><dt>文件名</dt><dd>{preview.title || preview.name}</dd></div><div><dt>文件日期</dt><dd>{photoDate(preview)}</dd></div>
        <div><dt>格式</dt><dd>{preview.format}</dd></div><div><dt>文件大小</dt><dd>{humanSize(preview.size)}</dd></div>
        <div><dt>分辨率</dt><dd>{preview.w} × {preview.h}</dd></div><div><dt>像素</dt><dd>{(preview.w * preview.h).toLocaleString("zh-CN")}</dd></div>
      </dl></aside>}</>}
      <Button variant="overlay" size="icon" id="lb-meta-toggle" className="photo-info-toggle" aria-label={infoOpen ? "收起信息" : "显示信息"} title={infoOpen ? "收起信息" : "显示信息"} aria-expanded={infoOpen} aria-controls="lb-meta" onClick={() => setInfoOpen(value => !value)}><Info aria-hidden="true" /></Button>
    </DialogContent>
  </Dialog>;
}
