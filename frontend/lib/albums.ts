import type { Album, AlbumPeople } from "@/lib/api";

export type AlbumSortKey = "name" | "author" | "model" | "added";
export type SortDirection = "asc" | "desc";
export interface AlbumSort { key: AlbumSortKey; direction: SortDirection }

export const sortLabels: Record<AlbumSortKey, string> = { name: "名称", author: "作者", model: "模特", added: "添加日期" };
const firstDirection: Record<AlbumSortKey, SortDirection> = { name: "asc", author: "asc", model: "asc", added: "desc" };
const storageKey = "juens-album-sort";
export const defaultSort: AlbumSort = { key: "name", direction: "asc" };

// Chinese, Korean and English names alike; numbers inside names compare by value.
export const nameCollator = new Intl.Collator("zh-CN", { numeric: true, sensitivity: "base" });

export function readAlbumSort(): AlbumSort {
  try {
    const [key, direction] = (localStorage.getItem(storageKey) || "").split(":");
    if ((Object.keys(sortLabels) as string[]).includes(key) && (direction === "asc" || direction === "desc")) return { key: key as AlbumSortKey, direction };
  } catch { /* Use the default order. */ }
  return defaultSort;
}
export function writeAlbumSort(sort: AlbumSort) {
  try { localStorage.setItem(storageKey, sort.key + ":" + sort.direction); } catch { /* The order still applies for this visit. */ }
}
// A new sort key starts in its natural direction: names A→Z, dates newest first.
export function sortFor(key: AlbumSortKey): AlbumSort {
  return { key, direction: firstDirection[key] };
}
export function directionLabel({ key, direction }: AlbumSort) {
  if (key === "added") return direction === "desc" ? "从新到旧" : "从旧到新";
  return direction === "asc" ? "正序" : "倒序";
}

const byName = (a: Album, b: Album) => nameCollator.compare(a.name, b.name) || nameCollator.compare(a.id, b.id);

// Albums without the sorted name come last in either direction; albums with
// the same name keep the newest first.
export function sortAlbums(albums: Album[], { key, direction }: AlbumSort): Album[] {
  const sign = direction === "asc" ? 1 : -1;
  const field = (album: Album) => key === "author" ? album.author?.name : album.models[0]?.name;
  return [...albums].sort((a, b) => {
    if (key === "name") return sign * byName(a, b);
    if (key === "added") return sign * (a.added - b.added) || byName(a, b);
    const x = field(a), y = field(b);
    if (!x || !y) return x ? -1 : y ? 1 : byName(a, b);
    return sign * nameCollator.compare(x, y) || b.added - a.added || byName(a, b);
  });
}

export function peopleText(album: AlbumPeople) {
  return [album.author?.name, album.models.map(model => model.name).join("、")].filter(Boolean).join(" · ");
}
