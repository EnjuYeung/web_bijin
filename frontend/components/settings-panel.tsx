"use client";

import { useCallback, useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import { Check, CircleAlert, Cloud, Copy, FolderHeart, Pencil, Plus, RefreshCw, Shuffle, Sparkles, Trash2, type LucideIcon } from "lucide-react";
import { api, formatTime, humanSize, type EventState, type Settings, type SourceStatus, type Storage, type StorageInput, type WallpaperStats } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardAction, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field";
import { Checkbox } from "@/components/ui/checkbox";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Spinner } from "@/components/ui/spinner";
import { OptionSelect } from "@/components/option-select";
import { RecognizerCard } from "@/components/recognizer-settings";
import { Skeleton } from "@/components/ui/skeleton";
import { Separator } from "@/components/ui/separator";

const blank: StorageInput = { id: 0, name: "", endpoint: "", region: "", bucket: "", prefix: "", accessKey: "", secretKey: "", addressing: "auto", listV1: false, directOriginal: false, publicEndpoint: "" };
type TextKey = Exclude<keyof StorageInput, "id" | "listV1" | "directOriginal">;
const labels: Partial<Record<TextKey, string>> = { endpoint: "Endpoint", bucket: "Bucket", accessKey: "Access Key", secretKey: "Secret Key" };
const addressingChoices = [{ value: "auto", label: "自动（推荐）" }, { value: "path", label: "路径：endpoint/bucket" }, { value: "virtual", label: "虚拟主机：bucket.endpoint" }];

function Count({ photos, broken = 0 }: { photos: number; broken?: number }) {
  return <>{photos.toLocaleString("zh-CN")} 张照片{broken > 0 && <span className="source-broken"> · {broken} 个文件无法显示</span>}</>;
}
function scanInterval(seconds: number) {
  return seconds % 60 === 0 ? seconds / 60 + " 分钟" : seconds + " 秒";
}
function EventsNote({ events, scanEvery }: { events?: EventState; scanEvery: number }) {
  if (!events?.enabled) return <p className="scan-note" id="events-note">新照片在每 {scanInterval(scanEvery)}一次的自动扫描中加入。</p>;
  const last = events.lastAt && new Date(events.lastAt).getFullYear() > 2000 ? `，最近一次 ${formatTime(events.lastAt)}` : "，还没有收到";
  return <p className="scan-note" id="events-note">上传通知已开启{last}；另每 {scanInterval(scanEvery)}核对一次。{events.lastErr && <span className="source-broken">最近一次处理失败：{events.lastErr}</span>}</p>;
}
function SourceState({ status }: { status?: SourceStatus }) {
  return <div className="source-state">
    <Badge variant={status?.err ? "destructive" : "outline"}>{status ? status.err ? <CircleAlert aria-hidden="true" /> : <Check aria-hidden="true" /> : null}{status ? status.err ? "扫描失败" : "扫描正常" : "等待扫描"}</Badge>
    {status && <span>{formatTime(status.at)}</span>}
    {status?.err && <p>{status.err}。已有照片会保留。</p>}
  </div>;
}
function StorageField({ name, label, value, onChange, helper, error, required, password, disabled }: {
  name: TextKey; label: string; value: string; onChange: (name: TextKey, value: string) => void;
  helper?: string; error?: string; required?: boolean; password?: boolean; disabled: boolean;
}) {
  const id = "s3-" + name;
  return <Field data-invalid={!!error} data-disabled={disabled}>
    <FieldLabel htmlFor={id}>{label}{required && <span className="required-mark" aria-label="必填">*</span>}</FieldLabel>
    <Input id={id} name={name} type={password ? "password" : "text"} value={value} onChange={event => onChange(name, event.target.value)}
      autoComplete={password ? "new-password" : "off"} autoCapitalize="off" spellCheck={false} disabled={disabled} maxLength={name === "name" ? 40 : undefined}
      inputMode={name === "endpoint" || name === "publicEndpoint" ? "url" : undefined} aria-required={required} aria-invalid={!!error}
      aria-describedby={[helper ? id + "-hint" : "", error ? id + "-error" : ""].filter(Boolean).join(" ") || undefined}
      placeholder={name === "endpoint" || name === "publicEndpoint" ? "https://s3.example.com" : name === "prefix" ? "例如 photos/2026" : name === "name" ? "例如 家庭照片" : undefined} />
    {helper && <FieldDescription id={id + "-hint"}>{helper}</FieldDescription>}
    {error && <FieldError id={id + "-error"}>{error}</FieldError>}
  </Field>;
}

const wallLinks = [
  { id: "landscape", label: "横屏", query: "?orientation=landscape" },
  { id: "portrait", label: "竖屏", query: "?orientation=portrait" },
  { id: "any", label: "不限", query: "" },
];
function WallpaperCard({ stats, busy }: { stats: WallpaperStats; busy: boolean }) {
  const [origin, setOrigin] = useState("");
  const [copied, setCopied] = useState<{ id: string; ok: boolean } | null>(null);
  useEffect(() => setOrigin(location.origin), []);
  async function copy(id: string, url: string) {
    try { await navigator.clipboard.writeText(url); setCopied({ id, ok: true }); }
    catch { setCopied({ id, ok: false }); }
  }
  return <Card className="source-card" id="wall-card">
    <CardHeader><CardTitle><h2><Shuffle aria-hidden="true" />随机壁纸</h2></CardTitle><CardAction><Badge variant="outline">公开</Badge></CardAction></CardHeader>
    <CardContent>
      {busy && stats.ready < stats.photos && <p className="scan-note" role="status"><Spinner aria-hidden="true" />正在生成壁纸，完成后状态会自动更新。</p>}
      <dl className="source-facts">
        <div><dt>已生成</dt><dd id="wall-ready">{stats.ready.toLocaleString("zh-CN")} / {stats.photos.toLocaleString("zh-CN")} 张<span className="wall-split">横 {stats.landscape} · 竖 {stats.portrait} · 方 {stats.square}</span></dd></div>
        <div><dt>占用空间</dt><dd id="wall-size">{humanSize(stats.bytes)}</dd></div>
        <div><dt>接口地址</dt><dd><ul className="wall-links">
          {wallLinks.map(link => {
            const url = origin + "/v1/backgrounds/random" + link.query;
            const done = copied?.id === link.id && copied.ok;
            return <li key={link.id}>
              <span className="wall-link-label">{link.label}</span>
              <code id={"wall-url-" + link.id}>{url}</code>
              <Button variant="outline" aria-label={"复制" + link.label + "地址"} onClick={() => copy(link.id, url)}>{done ? <Check data-icon="inline-start" aria-hidden="true" /> : <Copy data-icon="inline-start" aria-hidden="true" />}{done ? "已复制" : "复制"}</Button>
            </li>;
          })}
        </ul>{copied && !copied.ok && <p className="source-broken" role="status">复制失败，请手动选中地址复制。</p>}</dd></div>
      </dl>
    </CardContent>
  </Card>;
}

// One sub-page per card. The address keeps the open one; the page always starts
// on 本地图库 otherwise.
type Tab = "local" | "storage" | "recognizer" | "wallpaper";
const tabs: { id: Tab; label: string; icon: LucideIcon }[] = [
  { id: "local", label: "本地图库", icon: FolderHeart },
  { id: "storage", label: "对象存储", icon: Cloud },
  { id: "recognizer", label: "识别服务", icon: Sparkles },
  { id: "wallpaper", label: "随机壁纸", icon: Shuffle },
];
const isTab = (value: string | null): value is Tab => tabs.some(item => item.id === value);

export default function SettingsPanel() {
  const [tab, setTab] = useState<Tab>("local");
  const [settings, setSettings] = useState<Settings | null>(null);
  const [loadError, setLoadError] = useState("");
  const [editing, setEditing] = useState<Storage | null | undefined>(undefined);
  const [values, setValues] = useState<StorageInput>(blank);
  const [errors, setErrors] = useState<Partial<Record<TextKey, string>>>({});
  const [message, setMessage] = useState<{ text: string; kind: "ok" | "err" | "wait" | "warn" } | null>(null);
  const [pending, setPending] = useState<"save" | "test" | "delete" | null>(null);
  const [advanced, setAdvanced] = useState(false);
  const trigger = useRef<HTMLButtonElement | null>(null);
  const addButton = useRef<HTMLButtonElement>(null);
  const editor = useRef<HTMLFormElement>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const alive = useRef(true);
  const pollingUntil = useRef(Date.now() + 15 * 60 * 1000);
  const controller = useRef<AbortController | null>(null);

  const load = useCallback(async function refresh() {
    controller.current?.abort();
    const current = new AbortController();
    controller.current = current;
    try {
      const data = await api<Settings>("/api/settings", { signal: current.signal });
      if (!alive.current) return;
      setSettings(data); setLoadError("");
      if (timer.current) clearTimeout(timer.current);
      if ((data.scan.scanning || data.scan.queued) && Date.now() < pollingUntil.current) timer.current = setTimeout(refresh, 2000);
    } catch (err) { if (!current.signal.aborted && alive.current) setLoadError(err instanceof Error ? err.message : "无法读取设置，请重试。"); }
  }, []);
  useEffect(() => { alive.current = true; load(); return () => { alive.current = false; controller.current?.abort(); if (timer.current) clearTimeout(timer.current); }; }, [load]);
  useEffect(() => { const value = new URLSearchParams(location.search).get("tab"); if (isTab(value)) setTab(value); }, []);

  // Switching only hides the other cards, so a half-filled form or a running
  // test is still there on return. It adds no history entry: Back leaves 设置.
  function choose(next: Tab, focus = false) {
    if (focus) document.getElementById("settings-tab-" + next)?.focus();
    if (next === tab) return;
    setTab(next);
    const url = new URL(location.href);
    url.searchParams.set("tab", next);
    history.replaceState(history.state, "", url);
    scrollTo(0, 0);
  }
  function moveTab(event: KeyboardEvent<HTMLDivElement>) {
    const index = tabs.findIndex(item => item.id === tab);
    const target = event.key === "ArrowDown" || event.key === "ArrowRight" ? (index + 1) % tabs.length
      : event.key === "ArrowUp" || event.key === "ArrowLeft" ? (index + tabs.length - 1) % tabs.length
      : event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : -1;
    if (target < 0) return;
    event.preventDefault();
    choose(tabs[target].id, true);
  }

  function openEditor(storage: Storage | null, button: HTMLButtonElement) {
    trigger.current = button;
    setEditing(storage); setValues(storage ? { ...storage, secretKey: "" } : { ...blank }); setErrors({}); setMessage(null);
    setAdvanced(!!storage && (!!storage.region || storage.addressing !== "auto" || storage.listV1));
    requestAnimationFrame(() => {
      editor.current?.scrollIntoView({ block: "nearest" });
      document.getElementById(storage ? "s3-name" : "s3-endpoint")?.focus();
    });
  }
  function closeEditor() {
    setEditing(undefined); setErrors({}); setMessage(null);
    requestAnimationFrame(() => { if (trigger.current?.isConnected) trigger.current.focus(); else addButton.current?.focus(); });
  }
  function change(name: TextKey, value: string) {
    setValues(previous => ({ ...previous, [name]: value }));
    setErrors(previous => ({ ...previous, [name]: undefined }));
  }
  function validate() {
    const found: Partial<Record<TextKey, string>> = {};
    for (const name of ["endpoint", "bucket", "accessKey", "secretKey"] as TextKey[]) {
      if (name === "secretKey" && editing) continue;
      if (!values[name].trim()) found[name] = `请填写 ${labels[name]}。`;
    }
    setErrors(found);
    const first = Object.keys(found)[0];
    if (first) { requestAnimationFrame(() => document.getElementById("s3-" + first)?.focus()); return false; }
    return true;
  }
  async function test() {
    if (pending || !validate()) return;
    setPending("test"); setMessage({ text: "正在检查连接和图片读取权限…", kind: "wait" });
    try {
      const result = await api<{ images: number; checked: number; more: boolean }>("/api/storages/test", { method: "POST", body: JSON.stringify(values) });
      setMessage({ text: result.images ? `连接成功，${result.more ? "前 " + result.checked + " 个文件中" : ""}找到 ${result.images} 张图片。` : "连接成功，但没有找到图片。请检查 Bucket 和前缀。", kind: result.images ? "ok" : "warn" });
    } catch (err) { setMessage({ text: err instanceof Error ? err.message : "连接失败，请重试。", kind: "err" }); }
    finally { setPending(null); }
  }
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending || !validate()) return;
    setPending("save"); setMessage({ text: "正在保存…", kind: "wait" });
    try {
      await api(editing ? "/api/storages/" + editing.id : "/api/storages", { method: editing ? "PUT" : "POST", body: JSON.stringify(values) });
      closeEditor(); pollingUntil.current = Date.now() + 15 * 60 * 1000; await load();
    } catch (err) { setMessage({ text: err instanceof Error ? err.message : "保存失败，请重试。", kind: "err" }); }
    finally { setPending(null); }
  }
  async function remove(storage: Storage) {
    if (pending || !confirm(`删除「${storage.name}」？\n\n它的照片会从图库中移除，存储里的原图不会被删除。`)) return;
    setPending("delete");
    try {
      await api("/api/storages/" + storage.id, { method: "DELETE" });
      if (editing?.id === storage.id) closeEditor();
      pollingUntil.current = Date.now() + 15 * 60 * 1000; await load();
    } catch (err) { setLoadError(err instanceof Error ? err.message : "删除失败，请重试。"); }
    finally { setPending(null); }
  }

  // Only a failed scan is flagged on its button; normal states stay quiet.
  const failed: Partial<Record<Tab, boolean>> = { local: !!settings?.local.status?.err, storage: !!settings?.storages.some(storage => storage.status?.err) };
  // 本地图库 has no control inside, so its panel takes the Tab stop itself, as
  // the tabs pattern asks; the other panels start with their own controls.
  const panel = (id: Tab) => ({ role: "tabpanel", id: "settings-panel-" + id, "aria-labelledby": "settings-tab-" + id, hidden: tab !== id, tabIndex: id === "local" ? 0 : undefined });
  return <div id="settings" className="settings-shell">
    <div className="settings-layout">
      <div className="settings-tabs" role="tablist" aria-label="设置分类" onKeyDown={moveTab}>
        {tabs.map(({ id, label, icon: Icon }) => <button key={id} type="button" role="tab" id={"settings-tab-" + id} className="settings-tab" aria-selected={tab === id} aria-controls={"settings-panel-" + id}
          tabIndex={tab === id ? 0 : -1} title={failed[id] ? label + "：扫描失败" : undefined} onClick={() => choose(id)}>
          <Icon aria-hidden="true" /><span>{label}</span>{failed[id] && <><span className="settings-tab-alert" aria-hidden="true" /><span className="sr-only">，扫描失败</span></>}
        </button>)}
      </div>
      <div className="settings-content">
        {loadError && <Alert variant="destructive"><CircleAlert aria-hidden="true" /><AlertTitle>设置暂时读不出来</AlertTitle><AlertDescription>{loadError}<Button variant="outline" onClick={load}><RefreshCw data-icon="inline-start" aria-hidden="true" />重试</Button></AlertDescription></Alert>}
        {!settings && !loadError && <div aria-label="正在读取设置" role="status"><Skeleton className="h-64" /></div>}
        {settings && <>
          <div {...panel("local")}><Card className="source-card">
            <CardHeader><CardTitle><h2><FolderHeart aria-hidden="true" />本地图库</h2></CardTitle><CardAction><Badge variant="outline">可上传</Badge></CardAction></CardHeader>
            <CardContent><dl className="source-facts">
              <div><dt>照片</dt><dd id="local-count"><Count photos={settings.local.photos} broken={settings.local.broken} /></dd></div>
              <div><dt>宿主机目录</dt><dd id="local-host">{settings.local.hostDir || "未提供，见服务器 PHOTOS_DIR 配置"}</dd></div>
              <div><dt>容器内目录</dt><dd id="local-container">{settings.local.containerDir || "/photos"}</dd></div>
              <div><dt>最近扫描</dt><dd id="local-scan"><SourceState status={settings.local.status} /></dd></div>
            </dl></CardContent>
          </Card></div>
          <div {...panel("storage")}><Card className="source-card">
            <CardHeader><CardTitle><h2><Cloud aria-hidden="true" />对象存储</h2></CardTitle><CardAction>{editing === undefined && <Button ref={addButton} id="s3-add" onClick={event => openEditor(null, event.currentTarget)} disabled={!!pending}><Plus data-icon="inline-start" aria-hidden="true" />添加对象存储</Button>}</CardAction></CardHeader>
            <CardContent>
              <EventsNote events={settings.events} scanEvery={settings.scanEvery} />
              {(settings.scan.scanning || settings.scan.queued) && <p className="scan-note" role="status"><Spinner aria-hidden="true" />正在整理新照片，完成后状态会自动更新。</p>}
              <div id="s3-list" className="storage-list">
                {!settings.storages.length && <Empty><EmptyHeader><EmptyMedia variant="icon"><Cloud aria-hidden="true" /></EmptyMedia><EmptyTitle>还没有云端来源</EmptyTitle><EmptyDescription>添加一个对象存储，把云端的照片一起收进相册。</EmptyDescription></EmptyHeader></Empty>}
                {settings.storages.map(storage => <article className="storage-row" key={storage.id}>
                  <div className="storage-main"><h3>{storage.name}</h3><p className="storage-location">{storage.bucket}{storage.prefix ? "/" + storage.prefix : ""} · {new URL(storage.endpoint).host}{storage.directOriginal && " · 大图直连 " + new URL(storage.publicEndpoint || storage.endpoint).host}</p><p className="storage-count"><Count photos={storage.photos} broken={storage.broken} /></p><SourceState status={storage.status} /></div>
                  <div className="storage-actions"><Button variant="outline" aria-label={"编辑 " + storage.name} disabled={!!pending} onClick={event => openEditor(storage, event.currentTarget)}><Pencil data-icon="inline-start" aria-hidden="true" />编辑</Button><Button variant="destructive" aria-label={"删除 " + storage.name} disabled={!!pending} onClick={() => remove(storage)}><Trash2 data-icon="inline-start" aria-hidden="true" />删除</Button></div>
                </article>)}
              </div>
              {editing !== undefined && <form id="s3-form" className="storage-editor" ref={editor} onSubmit={save} noValidate aria-busy={!!pending} onKeyDown={event => { if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); if (!pending) closeEditor(); } }}>
                <h3 id="s3-form-title">{editing ? `编辑「${editing.name}」` : "添加对象存储"}</h3><p className="editor-intro">带 * 的项目为必填。凭据用于读取图片，也用于你主动发起的 S3 上传。</p>
                <FieldGroup className="storage-fields">
                  <StorageField name="name" label="名称" value={values.name} onChange={change} helper="留空使用 Bucket 名称" disabled={!!pending} />
                  <StorageField name="endpoint" label="Endpoint" value={values.endpoint} onChange={change} required error={errors.endpoint} helper="只填协议、域名和端口；默认使用 HTTPS" disabled={!!pending} />
                  <StorageField name="bucket" label="Bucket" value={values.bucket} onChange={change} required error={errors.bucket} disabled={!!pending} />
                  <StorageField name="prefix" label="前缀" value={values.prefix} onChange={change} helper="留空读取整个 Bucket" disabled={!!pending} />
                  <StorageField name="accessKey" label="Access Key" value={values.accessKey} onChange={change} required error={errors.accessKey} disabled={!!pending} />
                  <StorageField name="secretKey" label="Secret Key" value={values.secretKey} onChange={change} password required={!editing} error={errors.secretKey} helper={editing ? "已保存，留空表示不修改" : "只保存在服务器，保存后不会返回"} disabled={!!pending} />
                </FieldGroup>
                <FieldSet className="advanced-fields"><FieldLegend className="sr-only">大图直连</FieldLegend>
                  <Field orientation="horizontal" data-disabled={!!pending}><Checkbox id="s3-directOriginal" name="directOriginal" checked={values.directOriginal} disabled={!!pending} onCheckedChange={checked => setValues(previous => ({ ...previous, directOriginal: checked }))} /><FieldLabel htmlFor="s3-directOriginal">大图由浏览器直接从存储读取，不经过本服务转发</FieldLabel></Field>
                  {values.directOriginal && <FieldGroup className="storage-fields">
                    <StorageField name="publicEndpoint" label="浏览器访问地址" value={values.publicEndpoint} onChange={change} helper="浏览器能打开的存储地址，留空则与 Endpoint 相同；Endpoint 填的是服务器内网地址时必须填写" disabled={!!pending} />
                  </FieldGroup>}
                </FieldSet>
                <details className="advanced-settings" open={advanced} onToggle={event => setAdvanced(event.currentTarget.open)}>
                  <summary>高级设置</summary><FieldSet className="advanced-fields"><FieldLegend className="sr-only">对象存储高级设置</FieldLegend><FieldGroup className="storage-fields">
                    <StorageField name="region" label="Region" value={values.region} onChange={change} helper="留空自动识别，服务商要求时再填写" disabled={!!pending} />
                    <Field data-disabled={!!pending}><FieldLabel htmlFor="s3-addressing">寻址方式</FieldLabel><OptionSelect id="s3-addressing" label="寻址方式" value={values.addressing} choices={addressingChoices} disabled={!!pending} onChange={value => change("addressing", value)} /><FieldDescription>自建服务通常选路径，OSS / COS 可选虚拟主机。</FieldDescription></Field>
                  </FieldGroup><Field orientation="horizontal" data-disabled={!!pending}><Checkbox id="s3-listV1" name="listV1" checked={values.listV1} disabled={!!pending} onCheckedChange={checked => setValues(previous => ({ ...previous, listV1: checked }))} /><FieldLabel htmlFor="s3-listV1">使用旧版列举接口，仅在服务不支持 V2 时勾选</FieldLabel></Field></FieldSet>
                </details>
                {message?.kind === "err" ? <Alert variant="destructive" id="s3-msg"><CircleAlert aria-hidden="true" /><AlertDescription>{message.text}</AlertDescription></Alert> : <p id="s3-msg" className="form-message" data-kind={message?.kind} role="status" aria-live="polite">{message?.text || ""}</p>}
                <Separator /><div className="form-actions"><Button id="s3-save" type="submit" disabled={!!pending}>{pending === "save" && <Spinner data-icon="inline-start" aria-label="正在保存" />}{pending === "save" ? "保存中…" : "保存"}</Button><Button id="s3-test" type="button" variant="outline" disabled={!!pending} onClick={test}>{pending === "test" && <Spinner data-icon="inline-start" aria-label="正在连接" />}{pending === "test" ? "连接中…" : "测试连接"}</Button><Button id="s3-cancel" type="button" variant="ghost" disabled={!!pending} onClick={closeEditor}>取消</Button></div>
              </form>}
            </CardContent>
          </Card></div>
          <div {...panel("recognizer")}><RecognizerCard initial={settings.recognizer} /></div>
          <div {...panel("wallpaper")}><WallpaperCard stats={settings.wallpapers} busy={settings.scan.scanning || settings.scan.queued} /></div>
        </>}
      </div>
    </div>
  </div>;
}
