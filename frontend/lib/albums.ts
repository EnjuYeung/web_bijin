import type { Album, AlbumPeople, Person } from "@/lib/api";

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

// The server's rule: trimmed, single inner spaces, composed Unicode.
export function cleanName(value: string) {
  return value.trim().split(/\s+/).filter(Boolean).join(" ").normalize("NFC");
}
// Matching ignores letter case; the server folds English letters.
export function nameKey(value: string) {
  return cleanName(value).toLocaleLowerCase("en-US");
}

// Album filters: any of the chosen authors, and any of the chosen models.
export interface AlbumFilter { authors: string[]; models: string[] }
export const emptyFilter: AlbumFilter = { authors: [], models: [] };
// What is still missing; used by the organize page.
export type FillState = "all" | "empty" | "noAuthor" | "noModel";
export const fillLabels: Record<FillState, string> = { all: "全部相册", empty: "作者和模特都没填", noAuthor: "没填作者", noModel: "没填模特" };

export function filtering(filter: AlbumFilter, state: FillState = "all") {
  return state !== "all" || filter.authors.length > 0 || filter.models.length > 0;
}
export function matchesFilter(album: Album, filter: AlbumFilter, state: FillState = "all") {
  if (state === "empty" && (album.author || album.models.length)) return false;
  if (state === "noAuthor" && album.author) return false;
  if (state === "noModel" && album.models.length) return false;
  const authors = new Set(filter.authors.map(nameKey)), models = new Set(filter.models.map(nameKey));
  if (authors.size && !(album.author && authors.has(nameKey(album.author.name)))) return false;
  if (models.size && !album.models.some(model => models.has(nameKey(model.name)))) return false;
  return true;
}

// The authors and models used by the albums, with how many albums each has.
export function albumNames(albums: Album[]): { authors: Person[]; models: Person[] } {
  const count = (names: string[]) => {
    const found = new Map<string, Person>();
    for (const name of names) {
      const person = found.get(nameKey(name)) ?? { id: found.size + 1, name, albums: 0 };
      person.albums++;
      found.set(nameKey(name), person);
    }
    return [...found.values()].sort((a, b) => nameCollator.compare(a.name, b.name));
  };
  return {
    authors: count(albums.flatMap(album => album.author ? [album.author.name] : [])),
    models: count(albums.flatMap(album => album.models.map(model => model.name))),
  };
}

// The album page keeps its filter for this visit, so it survives opening an album and coming back.
const filterKey = "juens-album-filter";
export function readAlbumFilter(): AlbumFilter {
  try {
    const saved = JSON.parse(sessionStorage.getItem(filterKey) || "null");
    const names = (value: unknown) => Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
    if (saved && typeof saved === "object") return { authors: names(saved.authors), models: names(saved.models) };
  } catch { /* Start without a filter. */ }
  return emptyFilter;
}
export function writeAlbumFilter(filter: AlbumFilter) {
  try { sessionStorage.setItem(filterKey, JSON.stringify(filter)); } catch { /* The filter still applies on this page. */ }
}
