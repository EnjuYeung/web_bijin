"use client";

import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { ArrowLeft, Contrast, FolderHeart, Images, LogOut, Moon, SlidersHorizontal, Sun, Upload, UserRoundPen } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { GallerySkeleton } from "@/components/gallery-skeleton";
import { signedOutKey, signedOutSince } from "@/lib/api";
import { modeLabels, usePreferences } from "@/lib/preferences";
import { PhotoGallery } from "@/components/photo-gallery";
import { Albums } from "@/components/albums";

const UploadPanel = lazy(() => import("@/components/upload-panel"));
const SettingsPanel = lazy(() => import("@/components/settings-panel"));
const OrganizePanel = lazy(() => import("@/components/organize-panel"));
type View = "photos" | "albums" | "album" | "settings" | "upload" | "organize";
const titles: Record<Exclude<View, "album">, string> = { photos: "照片", albums: "相册", settings: "设置", upload: "上传", organize: "整理" };

export function AlbumApp() {
  const [view, setView] = useState<View>("photos");
  const [album, setAlbum] = useState<string | null>(null);
  const [title, setTitle] = useState("照片");
  const [count, setCount] = useState<number | null>(null);
  const [ready, setReady] = useState(false);
  const [timezone, setTimezone] = useState("Asia/Shanghai");
  const [leaving, setLeaving] = useState(false);
  const { expanded, mode, toggleSidebar, cycleMode } = usePreferences(timezone);
  const brand = useRef<HTMLButtonElement>(null);
  const updateCount = useCallback((value: number) => setCount(value), []);
  const updateTitle = useCallback((value: string) => setTitle(value), []);

  useEffect(() => {
    const query = new URLSearchParams(location.search);
    const folder = query.get("album");
    const page = query.get("view");
    const next: View = query.has("album") ? "album" : page === "albums" || page === "settings" || page === "upload" || page === "organize" ? page : "photos";
    setAlbum(folder);
    setView(next);
    setTitle(next === "album" ? folder === "." ? "根目录" : folder?.split("/").pop() || "相册" : titles[next]);
    setReady(true);
    const controller = new AbortController();
    fetch("/api/health", { signal: controller.signal }).then(r => r.json()).then(data => {
      if (data.tz) setTimezone(data.tz);
    }).catch(() => {});
    return () => controller.abort();
  }, []);
  useEffect(() => { document.title = title + " · Juen's"; }, [title]);
  useEffect(() => {
    // Logging out in another tab, or before Back brought this page back from
    // the browser's memory, sends this page to the login page too.
    const loadedAt = Date.now();
    const leave = () => location.replace("/login?out=1");
    const onStorage = (event: StorageEvent) => { if (event.key === signedOutKey && signedOutSince(loadedAt)) leave(); };
    const onShow = (event: PageTransitionEvent) => {
      if (!event.persisted) return;
      if (signedOutSince(loadedAt)) leave();
      else setLeaving(false); // the logout never got through, e.g. offline
    };
    addEventListener("storage", onStorage);
    addEventListener("pageshow", onShow);
    return () => { removeEventListener("storage", onStorage); removeEventListener("pageshow", onShow); };
  }, []);
  function logout() {
    // An upload in progress makes the browser ask before leaving. If it does,
    // the person may choose to stay, so the button must not stay "正在退出…".
    const ask = new Event("beforeunload", { cancelable: true });
    dispatchEvent(ask);
    if (!ask.defaultPrevented) setLeaving(true);
  }
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
        <a id="nav-organize" className="nav-item" href="/?view=organize" title="整理" aria-label="整理" aria-current={view === "organize" ? "page" : undefined}><UserRoundPen aria-hidden="true" /><span>整理</span></a>
        <a id="nav-upload" className="nav-item" href="/?view=upload" title="上传" aria-label="上传" aria-current={view === "upload" ? "page" : undefined}><Upload aria-hidden="true" /><span>上传</span></a>
        <a id="nav-settings" className="nav-item" href="/?view=settings" title="设置" aria-label="设置" aria-current={view === "settings" ? "page" : undefined}><SlidersHorizontal aria-hidden="true" /><span>设置</span></a>
      </nav>
      <form className="sidebar-logout" method="post" action="/logout" onSubmit={logout}>
        <button id="nav-logout" className="nav-item" type="submit" disabled={leaving} title="退出登录" aria-label={leaving ? "正在退出登录" : "退出登录"}>
          {leaving ? <Spinner aria-hidden="true" /> : <LogOut aria-hidden="true" />}<span>{leaving ? "正在退出…" : "退出登录"}</span>
        </button>
      </form>
      <p className="sidebar-foot"><span>家里的回忆</span></p>
    </aside>
    <div className="app-main">
      <header className="top">
        <div className="page-heading">
          {view === "album" && <Button id="album-back" variant="ghost" size="icon" render={<a href="/?view=albums" />} nativeButton={false} aria-label="返回相册" title="返回相册"><ArrowLeft aria-hidden="true" /></Button>}
          <h1 id="page-title">{title}</h1>
          {view !== "settings" && view !== "upload" && view !== "organize" && <p id="count" aria-live="polite" aria-atomic="true" aria-label={count === null ? "正在读取数量" : `${count} ${view === "albums" ? "个相册" : "张照片"}`}>{count === null ? "—" : count.toLocaleString("zh-CN")}</p>}
        </div>
        <Button id="theme-btn" variant="ghost" size="icon" aria-label={`显示模式：${modeLabels[mode]}；点击切换`} title={`显示模式：${modeLabels[mode]}；点击切换`} onClick={cycleMode}><ThemeIcon aria-hidden="true" /></Button>
      </header>
      <main id="main-content" tabIndex={-1}>
        {!ready ? <GallerySkeleton /> : view === "upload" ? <Suspense fallback={<GallerySkeleton />}><UploadPanel /></Suspense> : view === "organize" ? <Suspense fallback={<GallerySkeleton />}><OrganizePanel /></Suspense> : view === "settings" ? <Suspense fallback={<GallerySkeleton />}><SettingsPanel /></Suspense> : view === "albums" ? <Albums onCount={updateCount} /> : <PhotoGallery key={album ?? "all"} album={album} onCount={updateCount} onTitle={updateTitle} />}
      </main>
    </div>
  </>;
}
