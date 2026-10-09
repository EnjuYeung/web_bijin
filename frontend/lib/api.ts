export class APIError extends Error {
  constructor(message: string, public status: number) { super(message); }
}

export function loginURL() {
  return "/login?next=" + encodeURIComponent(location.pathname + location.search + location.hash);
}

// The login page notes the time here after a logout. Other tabs of the same
// browser hear the change, and a page that Back restores checks it.
export const signedOutKey = "juens-signed-out";
export function markSignedOut() {
  try { localStorage.setItem(signedOutKey, String(Date.now())); } catch { /* Other tabs find out on their next request. */ }
}
export function signedOutSince(time: number) {
  try { return Number(localStorage.getItem(signedOutKey)) > time; } catch { return false; }
}

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    ...options,
    headers: { Accept: "application/json", ...(options.body ? { "Content-Type": "application/json" } : {}), ...options.headers },
  });
  if (response.status === 401 && path !== "/api/login") {
    location.assign(loginURL());
    throw new APIError("需要重新登录", 401);
  }
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new APIError(data.error || `请求失败（${response.status}）`, response.status);
  return data as T;
}

export interface ScanState { scanning: boolean; queued: boolean; lastAt?: string; lastErr?: string; seen: number; failed: number; ready: number }
export interface SourceStatus { at: string; seen: number; err?: string }
export interface Photo { id: number; w: number; h: number; name: string; title: string; format: string; size: number; thumb: string; src: string; mtime: number; date: string; year: number }
export interface PersonRef { id: number; name: string }
export interface Person extends PersonRef { albums: number }
export interface People { authors: Person[]; models: Person[]; stale: number }
export interface AlbumPeople { author: PersonRef | null; models: PersonRef[] }
export interface Album extends AlbumPeople { id: string; name: string; count: number; cover: Photo; added: number; addedDate: string }
export interface PhotoPage { photos: Photo[]; total: number; next?: string; seed: string; tz: string; status: ScanState; album?: { id: string; name: string } }
export interface AlbumPage { albums: Album[]; total: number; tz: string; status: ScanState }
export interface StorageInput {
  id: number; name: string; endpoint: string; region: string; bucket: string; prefix: string;
  accessKey: string; secretKey: string; addressing: string; listV1: boolean;
  directOriginal: boolean; publicEndpoint: string;
}
export interface Storage extends Omit<StorageInput, "secretKey"> { hasSecret: boolean; photos: number; broken: number; status?: SourceStatus }
export interface EventState { enabled: boolean; received: number; lastAt?: string; lastErr?: string }
export interface WallpaperStats { photos: number; ready: number; landscape: number; portrait: number; square: number; bytes: number }
export interface Settings {
  local: { hostDir: string; containerDir: string; photos: number; broken: number; status?: SourceStatus };
  storages: Storage[]; scan: ScanState; scanEvery: number; events: EventState; wallpapers: WallpaperStats;
}

export function humanSize(value: number) {
  if (value >= 1073741824) return (value / 1073741824).toFixed(2) + " GB";
  if (value >= 1048576) return (value / 1048576).toFixed(1) + " MB";
  if (value >= 1024) return Math.round(value / 1024) + " KB";
  return value + " B";
}
export function formatTime(value?: string) {
  if (!value) return "尚未扫描";
  const date = new Date(value);
  if (isNaN(date.getTime()) || date.getFullYear() < 2001) return "尚未扫描";
  return date.toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });
}
export function photoDate(photo: Photo) {
  return photo.date ? photo.date.replace(/^(\d+)-(\d+)-(\d+)$/, (_, y, m, d) => `${y}年${Number(m)}月${Number(d)}日`) : String(photo.year || "");
}
