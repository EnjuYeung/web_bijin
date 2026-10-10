"use client";

import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { Check, ChevronDown, ChevronUp, CircleAlert, Eraser, Pencil, RefreshCw, Trash2, UserRoundPen, Users, X } from "lucide-react";
import { type Album, type Person } from "@/lib/api";
import { createAlbumOrganizer, type Change, type Message, type Role, type RowState } from "@/lib/album-organizer";
import { organizeHTTP } from "@/lib/organize-http";
import { albumNames, cleanName, emptyFilter, fillLabels, filtering, matchesFilter, nameCollator, nameKey, type AlbumFilter, type FillState } from "@/lib/albums";
import { OptionSelect } from "@/components/option-select";
import { PersonPicker } from "@/components/person-picker";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";

const fillChoices = (Object.keys(fillLabels) as FillState[]).map(value => ({ value, label: fillLabels[value] }));

function RowStatus({ state }: { state?: RowState }) {
  return <p className="organize-status" data-kind={state?.kind} role="status">
    {state?.kind === "saving" ? <><Spinner aria-hidden="true" />保存中</> : state?.kind === "saved" ? <><Check aria-hidden="true" />已保存</> : state?.kind === "uncertain" ? "待确认" : state?.kind === "waiting" ? "待保存（等待核对）" : state?.kind === "error" ? state.text : null}
  </p>;
}

export default function OrganizePanel() {
  const [organizer] = useState(() => createAlbumOrganizer(organizeHTTP));
  const state = useSyncExternalStore(organizer.subscribe, organizer.getSnapshot, organizer.getSnapshot);
  const { albums, people, loadError, rows } = state;
  const [filter, setFilter] = useState("");
  const [fill, setFill] = useState<FillState>("all");
  const [peopleFilter, setPeopleFilter] = useState<AlbumFilter>(emptyFilter);
  // Albums that matched when a filter was set stay listed while being filled in.
  const [matching, setMatching] = useState<Set<string> | null>(null);
  const [peopleOpen, setPeopleOpen] = useState(false);
  const [selected, setSelected] = useState<string[]>([]);
  const [batchAuthor, setBatchAuthor] = useState<string[]>([]);
  const [batchModels, setBatchModels] = useState<string[]>([]);
  const [batchError, setBatchError] = useState<Message | null>(null);
  const batchMessage = batchError ?? state.batchMessage;
  function setBatchMessage(message: Message | null) { setBatchError(message); organizer.clearMessage("batch"); }
  const [editing, setEditing] = useState<{ role: Role; source: string; name: string } | null>(null);
  const [listError, setListError] = useState<Message | null>(null);
  const listMessage = listError ?? state.listMessage;
  function setListMessage(message: Message | null) { setListError(message); organizer.clearMessage("list"); }
  useEffect(() => {
    void organizer.load();
    const beforeLeave = (event: BeforeUnloadEvent) => {
      if (!organizer.getSnapshot().hasPending) return;
      event.preventDefault(); event.returnValue = "";
    };
    addEventListener("beforeunload", beforeLeave);
    return () => { removeEventListener("beforeunload", beforeLeave); organizer.stop(); };
  }, [organizer]);

  function saveRow(album: Album, change: Change) {
    organizer.submit({ kind: "assign", albums: [album.id], ...change });
  }

  function applyBatch() {
    const change: Change = {};
    if (batchAuthor.length) change.author = batchAuthor[0];
    if (batchModels.length) change.models = batchModels;
    if (!selected.length || (!change.author && !change.models)) { setBatchMessage({ text: "先选择作者或模特。", kind: "err" }); return; }
    setBatchError(null);
    organizer.submit({ kind: "assign", albums: selected, batch: true, ...change });
    setSelected([]); setBatchAuthor([]); setBatchModels([]);
  }

  function rename(person: Person, list: Person[], role: Role) {
    if (!editing) return;
    const name = cleanName(editing.name);
    if (!name) { setListMessage({ text: "名字不能为空。", kind: "err" }); return; }
    if (name === person.name) { setEditing(null); return; }
    const other = list.find(item => item.id !== person.id && nameKey(item.name) === nameKey(name));
    if (other && !confirm(`「${other.name}」已经在名单里。\n\n合并后，所有相册里的「${person.name}」都会改成「${name}」。继续？`)) return;
    setListError(null); setEditing(null);
    organizer.submit({ kind: "rename", role, personId: person.id, name });
  }
  function remove(person: Person, role: Role) {
    if (!confirm(`删除「${person.name}」？\n\n会从 ${person.albums} 本相册中去掉这个名字，照片不受影响。`)) return;
    setListError(null);
    organizer.submit({ kind: "remove", role, personId: person.id });
  }
  function cleanStale(count: number) {
    if (!confirm(`清理 ${count} 本已不存在的相册的作者和模特记录？\n\n名单里的名字会保留。`)) return;
    setListError(null);
    organizer.submit({ kind: "cleanStale" });
  }

  const visible = useMemo(() => {
    const wanted = nameKey(filter);
    return (albums ?? []).filter(album => (!wanted || nameKey(album.name).includes(wanted) || nameKey(album.id).includes(wanted)) && (!matching || matching.has(album.id)));
  }, [albums, filter, matching]);
  const names = useMemo(() => albumNames(albums ?? []), [albums]);
  const visibleIds = visible.map(album => album.id);
  const allSelected = visibleIds.length > 0 && visibleIds.every(id => selected.includes(id));
  const someSelected = !allSelected && visibleIds.some(id => selected.includes(id));
  function toggle(id: string, on: boolean) {
    setBatchMessage(null);
    setSelected(previous => on ? [...previous.filter(item => item !== id), id] : previous.filter(item => item !== id));
  }
  function toggleAll() {
    setBatchMessage(null);
    setSelected(previous => allSelected ? previous.filter(id => !visibleIds.includes(id)) : [...new Set([...previous, ...visibleIds])]);
  }
  function applyFilter(nextFill: FillState, nextFilter: AlbumFilter) {
    setFill(nextFill); setPeopleFilter(nextFilter); setBatchMessage(null);
    setMatching(filtering(nextFilter, nextFill) && albums ? new Set(albums.filter(album => matchesFilter(album, nextFilter, nextFill)).map(album => album.id)) : null);
  }
  function clearFilters() { setFilter(""); applyFilter("all", emptyFilter); }

  function nameList(title: string, role: "author" | "model", list: Person[]) {
    return <section className="people-list" aria-labelledby={"people-" + role}>
      <h3 id={"people-" + role}>{title}<span>{list.length}</span></h3>
      {!list.length ? <p className="people-none">还没有{title}，在上面给相册填写后会出现在这里。</p> : <ul>
        {[...list].sort((a, b) => nameCollator.compare(a.name, b.name)).map(person => <li key={person.id}>
          {editing?.role === role && editing.source === person.name ? <form className="people-rename" onSubmit={event => { event.preventDefault(); rename(person, list, role); }}>
            <Input aria-label={`「${person.name}」的新名字`} value={editing.name} maxLength={40} autoFocus
              onChange={event => setEditing({ role, source: person.name, name: event.target.value })}
              onKeyDown={event => { if (event.key === "Escape") { event.preventDefault(); setEditing(null); } }} />
            <Button type="submit" size="icon" aria-label="保存新名字" title="保存"><Check aria-hidden="true" /></Button>
            <Button type="button" variant="ghost" size="icon" aria-label="取消改名" title="取消" onClick={() => setEditing(null)}><X aria-hidden="true" /></Button>
          </form> : <>
            <span className="people-name">{person.name}</span>
            <small>{person.albums} 本</small>
            <Button variant="ghost" size="icon" aria-label={`改名「${person.name}」`} title="改名" onClick={() => { setEditing({ role, source: person.name, name: person.name }); setListMessage(null); }}><Pencil aria-hidden="true" /></Button>
            <Button variant="ghost" size="icon" aria-label={`删除「${person.name}」`} title="删除" onClick={() => remove(person, role)}><Trash2 aria-hidden="true" /></Button>
          </>}
        </li>)}
      </ul>}
    </section>;
  }

  if (!albums && !loadError) return <div className="organize-shell" role="status" aria-label="正在读取相册"><Skeleton className="h-64" /><Skeleton className="h-48" /></div>;
  return <div id="organize" className="organize-shell">
    {loadError && <Alert variant="destructive"><CircleAlert aria-hidden="true" /><AlertTitle>相册暂时读不出来</AlertTitle><AlertDescription>{loadError}<Button variant="outline" onClick={() => void organizer.load()}><RefreshCw data-icon="inline-start" aria-hidden="true" />重试</Button></AlertDescription></Alert>}
    {state.uncertain && <Alert id="organize-confirmation"><CircleAlert aria-hidden="true" /><AlertTitle>保存结果待确认</AlertTitle><AlertDescription>
      <p>{state.uncertain.text}</p><p>可以继续填写；后续修改会等这次结果查清后再保存。</p>
      <div className="form-actions"><Button id="organize-verify" variant="outline" disabled={state.uncertain.checking} onClick={() => void organizer.verify()}><RefreshCw data-icon="inline-start" aria-hidden="true" />{state.uncertain.checking ? "正在核对…" : "再次核对"}</Button>
      {state.uncertain.canRetry && <Button id="organize-retry" onClick={() => organizer.retry()}>重试这次保存</Button>}</div>
    </AlertDescription></Alert>}
    {state.rejection && <Alert variant="destructive" id="organize-rejection"><CircleAlert aria-hidden="true" /><AlertTitle>有一项修改未保存</AlertTitle><AlertDescription>{state.rejection}。该项已恢复，后续填写仍保留。</AlertDescription></Alert>}
    {albums && people && <>
      <Card className="source-card">
        <CardHeader><CardTitle><h2><UserRoundPen aria-hidden="true" />整理相册</h2></CardTitle><CardDescription>给相册填写作者和模特：从下拉里选已有的名字，或输入新名字后按回车。修改会立即保存。</CardDescription></CardHeader>
        <CardContent className="organize-content">
          {!albums.length ? <Empty><EmptyHeader><EmptyMedia variant="icon"><UserRoundPen aria-hidden="true" /></EmptyMedia><EmptyTitle>还没有相册</EmptyTitle><EmptyDescription>照片进入相册后，就可以在这里填写作者和模特。</EmptyDescription></EmptyHeader></Empty> : <>
            <div className="organize-filters">
              <Input id="organize-filter" className="organize-filter" type="search" aria-label="按相册名筛选" placeholder="按相册名筛选" value={filter} onChange={event => setFilter(event.target.value)} />
              <OptionSelect id="organize-fill" label="按填写情况筛选" value={fill} choices={fillChoices} onChange={value => applyFilter(value, peopleFilter)} />
              <PersonPicker label="按作者筛选" placeholder="全部作者" multiple create={false} people={names.authors} value={peopleFilter.authors} onChange={authors => applyFilter(fill, { ...peopleFilter, authors })} />
              <PersonPicker label="按模特筛选" placeholder="全部模特" multiple create={false} people={names.models} value={peopleFilter.models} onChange={models => applyFilter(fill, { ...peopleFilter, models })} />
            </div>
            <div className="organize-tools">
              <Field orientation="horizontal" className="organize-toggle"><Checkbox id="organize-select-all" checked={allSelected} indeterminate={someSelected} disabled={!visible.length} onCheckedChange={toggleAll} /><FieldLabel htmlFor="organize-select-all">全选当前列表</FieldLabel></Field>
              <p id="organize-count" className="organize-count" aria-live="polite">{visible.length === albums.length ? `共 ${albums.length} 本` : `显示 ${visible.length} / ${albums.length} 本`}</p>
              {(filter || matching) && <Button id="organize-clear" variant="ghost" onClick={clearFilters}><X data-icon="inline-start" aria-hidden="true" />清除筛选</Button>}
            </div>
            {selected.length > 0 && <div id="organize-batch" className="organize-batch" role="group" aria-label="批量设置所选相册">
              <p><strong>已选 {selected.length} 本相册</strong>：选好作者和 / 或模特后应用，留空的一项保持不变。</p>
              <div className="organize-batch-fields">
                <PersonPicker id="batch-author" label="批量设置作者" placeholder="作者" people={people.authors} value={batchAuthor} onChange={setBatchAuthor} />
                <PersonPicker id="batch-models" label="批量设置模特" placeholder="模特（可多位）" multiple people={people.models} value={batchModels} onChange={setBatchModels} />
              </div>
              <div className="form-actions">
                <Button id="organize-apply" onClick={() => void applyBatch()}>应用到所选相册</Button>
                <Button variant="ghost" onClick={() => { setSelected([]); setBatchMessage(null); }}><X data-icon="inline-start" aria-hidden="true" />取消选择</Button>
              </div>
            </div>}
            {batchMessage && <p id="organize-batch-message" className="form-message" data-kind={batchMessage.kind} role="status">{batchMessage.text}</p>}
            <div className="organize-head" aria-hidden="true"><span>相册</span><span>作者</span><span>模特</span></div>
            <ul id="organize-list" className="organize-list" aria-label="相册的作者与模特">
              {visible.map(album => <li key={album.id} className="organize-row">
                <span className="organize-check"><Checkbox checked={selected.includes(album.id)} onCheckedChange={checked => toggle(album.id, checked)} aria-label={`选择「${album.name}」`} /></span>
                <img className="organize-cover" src={album.cover.thumb} alt="" width={album.cover.w} height={album.cover.h} loading="lazy" decoding="async" />
                <div className="organize-album"><strong title={album.id === "." ? album.name : album.id}>{album.name}</strong><small>{album.count.toLocaleString("zh-CN")} 张 · {album.addedDate}{album.id.includes("/") ? " · " + album.id : ""}</small><RowStatus state={rows[album.id]} /></div>
                <div className="organize-author"><PersonPicker label={`「${album.name}」的作者`} placeholder="作者" people={people.authors} value={album.author ? [album.author.name] : []} onChange={names => saveRow(album, { author: names[0] ?? "" })} /></div>
                <div className="organize-models"><PersonPicker label={`「${album.name}」的模特`} placeholder="模特" multiple people={people.models} value={album.models.map(model => model.name)} onChange={names => saveRow(album, { models: names })} /></div>
              </li>)}
            </ul>
            {!visible.length && <p className="note">没有符合条件的相册。</p>}
          </>}
        </CardContent>
      </Card>
      <Card className="source-card" id="people-card">
        <CardHeader>
          <CardTitle><h2><Users aria-hidden="true" />作者与模特名单</h2></CardTitle>
          <CardDescription>{peopleOpen ? "改名会同步到所有相册，改成已有的名字会合并；删除只去掉名字，照片不受影响。" : `作者 ${people.authors.length} 位，模特 ${people.models.length} 位${people.stale ? `；${people.stale} 本相册的记录已失效` : ""}。展开后可以改名、合并和删除。`}</CardDescription>
          <CardAction><Button id="people-toggle" variant="outline" aria-expanded={peopleOpen} aria-controls="people-content" onClick={() => setPeopleOpen(open => !open)}>{peopleOpen ? <ChevronUp data-icon="inline-start" aria-hidden="true" /> : <ChevronDown data-icon="inline-start" aria-hidden="true" />}{peopleOpen ? "收起名单" : "展开名单"}</Button></CardAction>
        </CardHeader>
        {peopleOpen && <CardContent id="people-content" className="organize-content">
          {people.stale > 0 && <Alert id="organize-stale"><CircleAlert aria-hidden="true" /><AlertTitle>有 {people.stale} 本相册的文件夹已经不存在</AlertTitle><AlertDescription>文件夹可能被改名或删除了，它们的作者和模特记录还在，改回原名会自动恢复。确定不再需要时可以清理。<Button variant="outline" onClick={() => void cleanStale(people.stale)}><Eraser data-icon="inline-start" aria-hidden="true" />清理失效记录</Button></AlertDescription></Alert>}
          <div className="people-columns">{nameList("作者", "author", people.authors)}{nameList("模特", "model", people.models)}</div>
          {listMessage && <p id="people-message" className="form-message" data-kind={listMessage.kind} role="status">{listMessage.text}</p>}
        </CardContent>}
      </Card>
    </>}
  </div>;
}
