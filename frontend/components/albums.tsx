"use client";

import { useEffect, useState } from "react";
import { FolderHeart, RefreshCw } from "lucide-react";
import { api, type AlbumPage } from "@/lib/api";
import { GallerySkeleton } from "@/components/gallery-skeleton";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";

export function Albums({ onCount }: { onCount: (count: number) => void }) {
  const [data, setData] = useState<AlbumPage | null>(null);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setError("");
    api<AlbumPage>("/api/albums", { signal: controller.signal }).then(result => {
      setData(result);
      onCount(result.total);
    }).catch(err => { if (!controller.signal.aborted) setError(err.message); });
    return () => controller.abort();
  }, [onCount, revision]);
  if (error) return <Alert variant="destructive"><AlertTitle>相册暂时读不出来</AlertTitle><AlertDescription>{error}<Button variant="outline" onClick={() => setRevision(value => value + 1)}><RefreshCw aria-hidden="true" data-icon="inline-start" />重试</Button></AlertDescription></Alert>;
  if (!data) return <GallerySkeleton />;
  if (!data.total) return <Empty><EmptyHeader><EmptyMedia variant="icon"><FolderHeart aria-hidden="true" /></EmptyMedia><EmptyTitle>还没有相册</EmptyTitle><EmptyDescription>{data.status.scanning ? "正在整理照片，完成后刷新即可查看。" : "把照片放进本地或云端文件夹，扫描后会自动整理为相册。"}</EmptyDescription></EmptyHeader></Empty>;
  return <div id="album-grid" className="album-grid">
    {data.albums.map(album => <a key={album.id} className="album-card" href={"/?album=" + encodeURIComponent(album.id)} title={album.id === "." ? album.name : album.id} aria-label={`${album.name}，${album.count} 张照片`}>
      <img src={album.cover.thumb} alt="" width={album.cover.w} height={album.cover.h} loading="lazy" decoding="async" />
      <span className="album-meta"><strong>{album.name}</strong><small>{album.count.toLocaleString("zh-CN")} 张照片</small></span>
    </a>)}
  </div>;
}
