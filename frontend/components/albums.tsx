"use client";

import { useEffect, useMemo, useState } from "react";
import { ArrowDownWideNarrow, ArrowUpNarrowWide, FolderHeart, RefreshCw } from "lucide-react";
import { api, type AlbumPage } from "@/lib/api";
import { defaultSort, directionLabel, peopleText, readAlbumSort, sortAlbums, sortFor, sortLabels, writeAlbumSort, type AlbumSort, type AlbumSortKey } from "@/lib/albums";
import { GallerySkeleton } from "@/components/gallery-skeleton";
import { Button } from "@/components/ui/button";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";

export function Albums({ onCount }: { onCount: (count: number) => void }) {
  const [data, setData] = useState<AlbumPage | null>(null);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [sort, setSort] = useState<AlbumSort>(defaultSort);
  useEffect(() => setSort(readAlbumSort()), []);
  useEffect(() => {
    const controller = new AbortController();
    setError("");
    api<AlbumPage>("/api/albums", { signal: controller.signal }).then(result => {
      setData(result);
      onCount(result.total);
    }).catch(err => { if (!controller.signal.aborted) setError(err.message); });
    return () => controller.abort();
  }, [onCount, revision]);
  const albums = useMemo(() => data ? sortAlbums(data.albums, sort) : [], [data, sort]);
  function change(next: AlbumSort) { setSort(next); writeAlbumSort(next); }
  const flipped: AlbumSort = { ...sort, direction: sort.direction === "asc" ? "desc" : "asc" };
  const order = `顺序：${directionLabel(sort)}，点击改为${directionLabel(flipped)}`;

  if (error) return <Alert variant="destructive"><AlertTitle>相册暂时读不出来</AlertTitle><AlertDescription>{error}<Button variant="outline" onClick={() => setRevision(value => value + 1)}><RefreshCw aria-hidden="true" data-icon="inline-start" />重试</Button></AlertDescription></Alert>;
  if (!data) return <GallerySkeleton />;
  if (!data.total) return <Empty><EmptyHeader><EmptyMedia variant="icon"><FolderHeart aria-hidden="true" /></EmptyMedia><EmptyTitle>还没有相册</EmptyTitle><EmptyDescription>{data.status.scanning ? "正在整理照片，完成后刷新即可查看。" : "把照片放进本地或云端文件夹，扫描后会自动整理为相册。"}</EmptyDescription></EmptyHeader></Empty>;
  return <>
    <div className="album-toolbar">
      <label htmlFor="album-sort">排序</label>
      <NativeSelect id="album-sort" value={sort.key} onChange={event => change(sortFor(event.target.value as AlbumSortKey))}>
        {(Object.keys(sortLabels) as AlbumSortKey[]).map(key => <NativeSelectOption key={key} value={key}>{sortLabels[key]}</NativeSelectOption>)}
      </NativeSelect>
      <Button id="album-sort-direction" variant="outline" size="icon" aria-label={order} title={order} onClick={() => change(flipped)}>
        {sort.direction === "asc" ? <ArrowUpNarrowWide aria-hidden="true" /> : <ArrowDownWideNarrow aria-hidden="true" />}
      </Button>
    </div>
    <div id="album-grid" className="album-grid">
      {albums.map(album => {
        const people = peopleText(album);
        return <a key={album.id} className="album-card" href={"/?album=" + encodeURIComponent(album.id)} title={album.id === "." ? album.name : album.id} aria-label={`${album.name}，${album.count} 张照片${people ? "，" + people : ""}`}>
          <img src={album.cover.thumb} alt="" width={album.cover.w} height={album.cover.h} loading="lazy" decoding="async" />
          <span className="album-meta"><strong>{album.name}</strong>{people && <span className="album-people">{people}</span>}<small>{album.count.toLocaleString("zh-CN")} 张照片</small></span>
        </a>;
      })}
    </div>
  </>;
}
