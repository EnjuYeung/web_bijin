"use client";

import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { ArrowLeft, Contrast, FolderHeart, Images, Moon, SlidersHorizontal, Sun, Upload } from "lucide-react";
import { Button } from "@/components/ui/button";
import { GallerySkeleton } from "@/components/gallery-skeleton";
import { modeLabels, usePreferences } from "@/lib/preferences";
import { PhotoGallery } from "@/components/photo-gallery";
import { Albums } from "@/components/albums";

const UploadPanel = lazy(() => import("@/components/upload-panel"));
const SettingsPanel = lazy(() => import("@/components/settings-panel"));
type View = "photos" | "albums" | "album" | "settings" | "upload";

export function AlbumApp() {
  const [view, setView] = useState<View>("photos");
  const [album, setAlbum] = useState<string | null>(null);
  const [title, setTitle] = useState("照片");
  const [count, setCount] = useState<number | null>(null);
  const [ready, setReady] = useState(false);
  const [timezone, setTimezone] = useState("Asia/Shanghai");
  const { expanded, mode, toggleSidebar, cycleMode } = usePreferences(timezone);
  const brand = useRef<HTMLButtonElement>(null);
  const updateCount = useCallback((value: number) => setCount(value), []);
  const updateTitle = useCallback((value: string) => setTitle(value), []);

  useEffect(() => {
    const query = new URLSearchParams(location.search);
    const folder = query.get("album");
    const page = query.get("view");
    const next: View = query.has("album") ? "album" : page === "albums" || page === "settings" || page === "upload" ? page : "photos";
    setAlbum(folder);
    setView(next);
    setTitle(next === "album" ? folder === "." ? "根目录" : folder?.split("/").pop() || "相册" : next === "albums" ? "相册" : next === "settings" ? "设置" : next === "upload" ? "上传" : "照片");
    setReady(true);
    const controller = new AbortController();
    fetch("/api/health", { signal: controller.signal }).then(r => r.json()).then(data => {
      if (data.tz) setTimezone(data.tz);
    }).catch(() => {});
    return () => controller.abort();
  }, []);
  useEffect(() => { document.title = title + " · Juen's"; }, [title]);
  useEffect(() => {
    const onEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented && expanded && matchMedia("(max-width:719px)").matches) {
        toggleSidebar(false);
        brand.current?.focus();
      }
    };
    addEventListener("keydown", onEscape);
    return () => removeEventListener("keydown", onEscape);
  }, [expanded, toggleSidebar]);
  const albumsActive = view === "albums" || view === "album";
  const ThemeIcon = mode === "night" ? Moon : mode === "day" ? Sun : Contrast;

  return <>
    <a className="skip-link" href="#main-content">跳到主要内容</a>
    <button id="nav-scrim" className="nav-scrim" tabIndex={-1} aria-label="收起导航" onClick={() => toggleSidebar(false)} />
    <aside id="sidebar" className="sidebar" aria-label="图库导航">
      <div className="sidebar-head">
        <Button ref={brand} variant="brand" id="sidebar-brand" className="brand" aria-expanded={expanded} aria-controls="sidebar-nav"
          aria-label={`Juen's，${expanded ? "收起" : "展开"}导航`} title={`点击标题${expanded ? "收起" : "展开"}导航`} onClick={() => toggleSidebar()}>
          <span className="brand-full">Juen&apos;s</span><span className="brand-short" aria-hidden="true">J</span>
        </Button>
      </div>
      <nav id="sidebar-nav" className="sidebar-nav">
        <a id="nav-photos" className="nav-item" href="/" title="照片" aria-label="照片" aria-current={view === "photos" ? "page" : undefined}><Images aria-hidden="true" /><span>照片</span></a>
        <a id="nav-albums" className="nav-item" href="/?view=albums" title="相册" aria-label="相册" aria-current={albumsActive ? "page" : undefined}><FolderHeart aria-hidden="true" /><span>相册</span></a>
        <a id="nav-upload" className="nav-item" href="/?view=upload" title="上传" aria-label="上传" aria-current={view === "upload" ? "page" : undefined}><Upload aria-hidden="true" /><span>上传</span></a>
        <a id="nav-settings" className="nav-item" href="/?view=settings" title="设置" aria-label="设置" aria-current={view === "settings" ? "page" : undefined}><SlidersHorizontal aria-hidden="true" /><span>设置</span></a>
      </nav>
      <p className="sidebar-foot"><span>家里的回忆</span></p>
    </aside>
    <div className="app-main">
      <header className="top">
        <div className="page-heading">
          {view === "album" && <Button id="album-back" variant="ghost" size="icon" render={<a href="/?view=albums" />} nativeButton={false} aria-label="返回相册" title="返回相册"><ArrowLeft aria-hidden="true" /></Button>}
          <h1 id="page-title">{title}</h1>
          {view !== "settings" && view !== "upload" && <p id="count" aria-live="polite" aria-atomic="true" aria-label={count === null ? "正在读取数量" : `${count} ${view === "albums" ? "个相册" : "张照片"}`}>{count === null ? "—" : count.toLocaleString("zh-CN")}</p>}
        </div>
        <Button id="theme-btn" variant="ghost" size="icon" aria-label={`显示模式：${modeLabels[mode]}；点击切换`} title={`显示模式：${modeLabels[mode]}；点击切换`} onClick={cycleMode}><ThemeIcon aria-hidden="true" /></Button>
      </header>
      <main id="main-content" tabIndex={-1}>
        {!ready ? <GallerySkeleton /> : view === "upload" ? <Suspense fallback={<GallerySkeleton />}><UploadPanel /></Suspense> : view === "settings" ? <Suspense fallback={<GallerySkeleton />}><SettingsPanel /></Suspense> : view === "albums" ? <Albums onCount={updateCount} /> : <PhotoGallery key={album ?? "all"} album={album} onCount={updateCount} onTitle={updateTitle} />}
      </main>
    </div>
  </>;
}
