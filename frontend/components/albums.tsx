"use client";

import { useEffect, useMemo, useState } from "react";
import { ArrowDownWideNarrow, ArrowUpNarrowWide, FolderHeart, RefreshCw, SearchX, X } from "lucide-react";
import { api, type AlbumPage, type Person } from "@/lib/api";
import { albumNames, defaultSort, directionLabel, emptyFilter, filtering, matchesFilter, nameKey, peopleText, readAlbumFilter, readAlbumSort, sortAlbums, sortFor, sortLabels, writeAlbumFilter, writeAlbumSort, type AlbumFilter, type AlbumSort, type AlbumSortKey } from "@/lib/albums";
import { GallerySkeleton } from "@/components/gallery-skeleton";
import { OptionSelect } from "@/components/option-select";
import { PersonPicker } from "@/components/person-picker";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";

const sortChoices = (Object.keys(sortLabels) as AlbumSortKey[]).map(key => ({ value: key, label: sortLabels[key] }));
// A remembered filter forgets names that are no longer used by any album.
const known = (chosen: string[], list: Person[]) => chosen.filter(name => list.some(person => nameKey(person.name) === nameKey(name)));

export function Albums({ onCount }: { onCount: (count: number) => void }) {
  const [data, setData] = useState<AlbumPage | null>(null);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [sort, setSort] = useState<AlbumSort>(defaultSort);
  const [filter, setFilter] = useState<AlbumFilter>(emptyFilter);
  useEffect(() => { setSort(readAlbumSort()); setFilter(readAlbumFilter()); }, []);
  useEffect(() => {
    const controller = new AbortController();
    setError("");
    api<AlbumPage>("/api/albums", { signal: controller.signal }).then(result => {
      setData(result);
      onCount(result.total);
    }).catch(err => { if (!controller.signal.aborted) setError(err.message); });
    return () => controller.abort();
  }, [onCount, revision]);
  const names = useMemo(() => albumNames(data?.albums ?? []), [data]);
  const active = useMemo(() => data ? { authors: known(filter.authors, names.authors), models: known(filter.models, names.models) } : filter, [data, filter, names]);
  const albums = useMemo(() => data ? sortAlbums(data.albums.filter(album => matchesFilter(album, active)), sort) : [], [data, active, sort]);
  function change(next: AlbumSort) { setSort(next); writeAlbumSort(next); }
  function changeFilter(next: AlbumFilter) { setFilter(next); writeAlbumFilter(next); }
  const flipped: AlbumSort = { ...sort, direction: sort.direction === "asc" ? "desc" : "asc" };
  const order = `顺序：${directionLabel(sort)}，点击改为${directionLabel(flipped)}`;

  if (error) return <Alert variant="destructive"><AlertTitle>相册暂时读不出来</AlertTitle><AlertDescription>{error}<Button variant="outline" onClick={() => setRevision(value => value + 1)}><RefreshCw aria-hidden="true" data-icon="inline-start" />重试</Button></AlertDescription></Alert>;
  if (!data) return <GallerySkeleton />;
  if (!data.total) return <Empty><EmptyHeader><EmptyMedia variant="icon"><FolderHeart aria-hidden="true" /></EmptyMedia><EmptyTitle>还没有相册</EmptyTitle><EmptyDescription>{data.status.scanning ? "正在整理照片，完成后刷新即可查看。" : "把照片放进本地或云端文件夹，扫描后会自动整理为相册。"}</EmptyDescription></EmptyHeader></Empty>;
  const filtered = filtering(active);
  return <>
    <div className="album-toolbar">
      <div className="album-filters">
        <PersonPicker label="按作者筛选" placeholder="全部作者" multiple create={false} people={names.authors} value={active.authors} onChange={authors => changeFilter({ ...active, authors })} />
        <PersonPicker label="按模特筛选" placeholder="全部模特" multiple create={false} people={names.models} value={active.models} onChange={models => changeFilter({ ...active, models })} />
      </div>
      <div className="album-sort">
        <span aria-hidden="true">排序</span>
        <OptionSelect id="album-sort" label="排序方式" value={sort.key} choices={sortChoices} onChange={key => change(sortFor(key))} />
        <Button id="album-sort-direction" variant="outline" size="icon" aria-label={order} title={order} onClick={() => change(flipped)}>
          {sort.direction === "asc" ? <ArrowUpNarrowWide aria-hidden="true" /> : <ArrowDownWideNarrow aria-hidden="true" />}
        </Button>
      </div>
    </div>
    {filtered && <p id="album-filter-note" className="album-filter-note" role="status">筛选后 {albums.length} / {data.total} 本<Button variant="ghost" onClick={() => changeFilter(emptyFilter)}><X aria-hidden="true" data-icon="inline-start" />清除筛选</Button></p>}
    {albums.length ? <div id="album-grid" className="album-grid">
      {albums.map(album => {
        const people = peopleText(album);
        return <a key={album.id} className="album-card" href={"/?album=" + encodeURIComponent(album.id)} title={album.id === "." ? album.name : album.id} aria-label={`${album.name}，${album.count} 张照片${people ? "，" + people : ""}`}>
          <img src={album.cover.thumb} alt="" width={album.cover.w} height={album.cover.h} loading="lazy" decoding="async" />
          <span className="album-meta"><strong>{album.name}</strong>{people && <span className="album-people">{people}</span>}<small>{album.count.toLocaleString("zh-CN")} 张照片</small></span>
        </a>;
      })}
    </div> : <Empty><EmptyHeader><EmptyMedia variant="icon"><SearchX aria-hidden="true" /></EmptyMedia><EmptyTitle>没有符合条件的相册</EmptyTitle><EmptyDescription>换一个作者或模特试试，或者清除筛选。</EmptyDescription></EmptyHeader></Empty>}
  </>;
}
