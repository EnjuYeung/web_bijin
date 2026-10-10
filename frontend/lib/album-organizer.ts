import type { Album, AlbumPeople, People, Person, PersonRef } from "./api";

export type Role = "author" | "model";
export type Change = { author?: string; models?: string[] };
export type Message = { text: string; kind: "ok" | "err" | "wait" };
export type RowState = { kind: "saving" | "saved" | "waiting" | "uncertain" } | { kind: "error"; text: string };
export type OrganizeIntent =
  | ({ kind: "assign"; albums: string[]; batch?: boolean } & Change)
  | { kind: "rename"; role: Role; personId: number; name: string }
  | { kind: "remove"; role: Role; personId: number }
  | { kind: "cleanStale" };
export type WireChange = {
  operationId: string; kind: OrganizeIntent["kind"]; albums?: string[];
  author?: string; models?: string[]; personId?: number; name?: string;
};
export interface OrganizeResult {
  operationId: string; albums: (AlbumPeople & { id: string })[]; people: People;
  person?: PersonRef; merged?: boolean; removed?: number;
}
export type Receipt = { state: "committed"; result: OrganizeResult } | { state: "missing" | "expired" };
export interface OrganizeAdapter {
  read(): Promise<{ albums: Album[]; people: People }>;
  write(change: WireChange): Promise<OrganizeResult>;
  confirm(operationId: string): Promise<Receipt>;
}
export class OrganizeRejected extends Error {}
export interface OrganizeSnapshot {
  albums: Album[] | null; people: People | null; loadError: string;
  rows: Record<string, RowState>; hasPending: boolean;
  uncertain: { text: string; canRetry: boolean; checking: boolean } | null;
  batchMessage: Message | null; listMessage: Message | null;
  rejection: string;
}

// Match Go and SQLite NOCASE exactly for identity; display/filter collation is
// deliberately separate. IDs survive renames; newly typed names use local IDs
// until a confirmed answer binds them to persisted people.
const clean = (name: string) => name.normalize("NFC").replace(/\p{White_Space}+/gu, " ").replace(/^ +| +$/g, "");
const key = (name: string) => clean(name).replace(/[A-Z]/g, letter => letter.toLowerCase());
const group = (role: Role) => role === "author" ? "authors" : "models";
const aliasKey = (role: Role, name: string) => role + ":" + key(name);
const invalidName = (name: string) => !name || [...name].length > 40 || /\p{Cc}/u.test(name);
type Assignment = { kind: "assign"; albums: string[]; author?: PersonRef | null; models?: PersonRef[]; batch?: boolean };
type Operation = {
  intent: Assignment | Exclude<OrganizeIntent, { kind: "assign" }>;
  affected: string[]; wire?: WireChange;
  channel?: "batch" | "list"; label?: string;
};
type Projection = { albums: Album[]; people: People; aliases: Map<string, number | null> };

export function createAlbumOrganizer(adapter: OrganizeAdapter, newID = () => `${Date.now()}-${crypto.randomUUID()}`) {
  let confirmed: { albums: Album[]; people: People } | null = null;
  let projection: Projection | null = null;
  const pending: Operation[] = [];
  const aliases = new Map<string, number | null>();
  const touched = new Set<string>();
  const errors = new Map<string, string>();
  const listeners = new Set<() => void>();
  const latest: { batch?: Operation; list?: Operation } = {};
  let temporaryID = -1;
  let revision = 0;
  let running = false;
  let active = true;
  let snapshot: OrganizeSnapshot = { albums: null, people: null, loadError: "", rows: {}, hasPending: false, uncertain: null, batchMessage: null, listMessage: null, rejection: "" };

  function project(): Projection | null {
    if (!confirmed) return null;
    const state: Projection = {
      albums: confirmed.albums.map(album => ({ ...album, author: album.author && { ...album.author }, models: album.models.map(person => ({ ...person })) })),
      people: { authors: confirmed.people.authors.map(person => ({ ...person })), models: confirmed.people.models.map(person => ({ ...person })), stale: confirmed.people.stale },
      aliases: new Map(aliases),
    };
    const retired = new Set<number>();
    const resolve = (role: Role, ref: PersonRef) => {
      const list = state.people[group(role)];
      const existing = list.find(person => person.id === ref.id);
      if (existing) return existing;
      if (ref.id >= 0 || retired.has(ref.id)) return null;
      const created = { ...ref, albums: 0 };
      list.push(created);
      return created;
    };
    for (const { intent } of pending) {
      if (intent.kind === "assign") {
        const author = intent.author === undefined ? undefined : intent.author === null ? null : resolve("author", intent.author);
        const models = intent.models?.map(ref => resolve("model", ref)).filter((ref): ref is Person => ref !== null).filter((person, index, list) => list.findIndex(item => item.id === person.id) === index);
        const targets = new Set(intent.albums);
        state.albums = state.albums.map(album => targets.has(album.id) ? {
          ...album, author: author === undefined ? album.author : author,
          models: models === undefined ? album.models : models,
        } : album);
      } else if (intent.kind === "cleanStale") {
        state.people.stale = 0;
      } else {
        const list = state.people[group(intent.role)];
        const source = list.find(person => person.id === intent.personId);
        if (!source) continue;
        const target = intent.kind === "rename" ? list.find(person => person.id !== source.id && key(person.name) === key(intent.name)) : null;
        const replacement = intent.kind === "rename" ? { ...(target ?? source), name: clean(intent.name) } : null;
        state.aliases.set(aliasKey(intent.role, source.name), replacement?.id ?? null);
        for (const [name, id] of state.aliases) if (id === source.id) state.aliases.set(name, replacement?.id ?? null);
        if (replacement) {
          state.people[group(intent.role)] = list.filter(person => person.id !== source.id && person.id !== replacement.id).concat(replacement);
        } else {
          state.people[group(intent.role)] = list.filter(person => person.id !== source.id);
        }
        if (!replacement || replacement.id !== source.id) retired.add(source.id);
        state.albums = state.albums.map(album => {
          if (intent.role === "author") return { ...album, author: album.author?.id === source.id || album.author?.id === replacement?.id ? replacement : album.author };
          // The existing target keeps its position when merging, as Go's
          // UPDATE OR IGNORE does. Without a target, the source keeps its slot.
          const hasTarget = replacement && replacement.id !== source.id && album.models.some(person => person.id === replacement.id);
          return { ...album, models: album.models.flatMap(person => person.id === source.id ? replacement && !hasTarget ? [replacement] : [] : person.id === replacement?.id ? [replacement] : [person]) };
        });
      }
    }
    for (const role of ["author", "model"] as const) {
      const counts = new Map<number, number>();
      for (const album of state.albums) for (const ref of role === "author" ? album.author ? [album.author] : [] : album.models) counts.set(ref.id, (counts.get(ref.id) ?? 0) + 1);
      state.people[group(role)] = state.people[group(role)].map(person => ({ ...person, albums: counts.get(person.id) ?? 0 }));
    }
    return state;
  }

  function publish() {
    projection = project();
    const rows: Record<string, RowState> = {};
    for (const id of touched) rows[id] = errors.has(id) ? { kind: "error", text: errors.get(id)! } : { kind: "saved" };
    for (const op of pending) for (const id of op.affected) rows[id] = { kind: snapshot.uncertain ? "waiting" : "saving" };
    if (snapshot.uncertain) for (const id of pending[0]?.affected ?? []) rows[id] = { kind: "uncertain" };
    snapshot = { ...snapshot, albums: projection?.albums ?? null, people: projection?.people ?? null, rows, hasPending: pending.length > 0 };
    for (const listener of listeners) listener();
  }

  function message(op: Operation, value: Message) {
    if (op.channel && latest[op.channel] === op) snapshot = { ...snapshot, [op.channel + "Message"]: value };
  }

  function remap(from: number, to: number) {
    for (const op of pending) {
      const intent = op.intent;
      if (intent.kind === "assign") {
        if (intent.author?.id === from) intent.author = { ...intent.author, id: to };
        if (intent.models) intent.models = intent.models.map(ref => ref.id === from ? { ...ref, id: to } : ref);
      } else if ((intent.kind === "rename" || intent.kind === "remove") && intent.personId === from) intent.personId = to;
    }
    for (const [name, id] of aliases) if (id === from) aliases.set(name, to);
  }

  function accept(op: Operation, result: OrganizeResult) {
    if (!confirmed || pending[0] !== op) return;
    const intent = op.intent;
    if (intent.kind === "assign") {
      for (const [role, refs] of [["author", intent.author ? [intent.author] : []], ["model", intent.models ?? []]] as const) {
        for (const ref of refs) if (ref.id < 0) {
          const saved = result.people[group(role)].find(person => key(person.name) === key(ref.name));
          if (saved) remap(ref.id, saved.id);
        }
      }
    } else if (intent.kind === "rename" || intent.kind === "remove") {
      const before = confirmed.people[group(intent.role)].find(person => person.id === intent.personId);
      const target = intent.kind === "rename" ? result.person?.id ?? intent.personId : null;
      if (before) aliases.set(aliasKey(intent.role, before.name), target);
      for (const [name, id] of aliases) if (id === intent.personId) aliases.set(name, target);
      if (target !== null && target !== intent.personId) remap(intent.personId, target);
    }
    const saved = new Map(result.albums.map(album => [album.id, album]));
    confirmed = { albums: confirmed.albums.map(album => ({ ...album, ...(saved.get(album.id) ?? {}) })), people: result.people };
    pending.shift();
    for (const id of op.affected) errors.delete(id);
    revision++;
    snapshot = { ...snapshot, uncertain: null };
    message(op, { kind: "ok", text: intent.kind === "assign" ? `已设置 ${intent.albums.length} 本相册。` : intent.kind === "rename" ? `已${result.merged ? "合并" : "改名"}为「${intent.name}」。` : intent.kind === "remove" ? `已删除「${op.label}」。` : `已清理 ${result.removed ?? 0} 本相册的失效记录。` });
    publish();
  }

  function reject(op: Operation, error: Error) {
    pending.shift();
    revision++;
    // A later selection may include the same invalid new name plus a valid
    // addition. Withdraw the refused value, retaining that later addition.
    const intent = op.intent;
    const refused = new Set<number>();
    if (intent.kind === "assign") for (const ref of [...(intent.author ? [intent.author] : []), ...(intent.models ?? [])]) if (invalidName(ref.name)) refused.add(ref.id);
    for (const later of pending) if (later.intent.kind === "assign") {
      if (later.intent.author && refused.has(later.intent.author.id)) delete later.intent.author;
      if (later.intent.models) later.intent.models = later.intent.models.filter(ref => !refused.has(ref.id));
    }
    for (const id of op.affected) errors.set(id, error.message);
    snapshot = { ...snapshot, uncertain: null, rejection: error.message };
    message(op, { kind: "err", text: error.message });
    publish();
  }

  function prepare(op: Operation): WireChange {
    const intent = op.intent;
    const wire: WireChange = { operationId: newID(), kind: intent.kind };
    if (intent.kind === "assign") {
      wire.albums = [...intent.albums];
      const resolve = (role: Role, ref: PersonRef) => confirmed!.people[group(role)].find(person => person.id === ref.id)?.name ?? (ref.id < 0 ? ref.name : null);
      if (intent.author !== undefined) wire.author = intent.author ? resolve("author", intent.author) ?? "" : "";
      if (intent.models !== undefined) wire.models = [...new Set(intent.models.map(ref => resolve("model", ref)).filter((name): name is string => name !== null))];
      if (wire.author === undefined && wire.models === undefined) throw new OrganizeRejected("没有要修改的作者或模特");
    } else if (intent.kind === "rename" || intent.kind === "remove") {
      if (!confirmed!.people[group(intent.role)].some(person => person.id === intent.personId)) throw new OrganizeRejected("找不到这个名字，请核对名单");
      wire.personId = intent.personId;
      if (intent.kind === "rename") wire.name = intent.name;
    }
    return wire;
  }

  async function verifyHead() {
    const op = pending[0];
    if (!op?.wire || !snapshot.uncertain || snapshot.uncertain.checking) return;
    snapshot = { ...snapshot, uncertain: { ...snapshot.uncertain, checking: true } }; publish();
    try {
      const receipt = await adapter.confirm(op.wire.operationId);
      if (!active || pending[0] !== op) return;
      if (receipt.state === "committed") accept(op, receipt.result);
      else {
        snapshot = { ...snapshot, uncertain: { checking: false, canRetry: receipt.state === "missing", text: receipt.state === "missing" ? "尚未找到保存回执。可以按原编号重试，后面的修改会继续保留。" : "保存回执已过期，无法确认这次修改。请核对相册；后面的修改仍留在页面上。" } };
        publish();
      }
    } catch (err) {
      if (!active) return;
      snapshot = { ...snapshot, uncertain: { checking: false, canRetry: false, text: `暂时无法核对保存结果。${err instanceof Error ? err.message : "请稍后再次核对。"}` } };
      publish();
    }
  }

  async function pump() {
    if (running || !active || snapshot.uncertain || !confirmed) return;
    running = true;
    try {
      while (active && pending.length && !snapshot.uncertain) {
        const op = pending[0];
        try {
          // Freeze the wire data once. Continued edits change the projection,
          // never the payload or ID of an uncertain operation's retry.
          op.wire ??= prepare(op);
          const result = await adapter.write(op.wire);
          if (!active) return;
          accept(op, result);
        } catch (err) {
          if (!active) return;
          if (err instanceof OrganizeRejected) reject(op, err);
          else {
            snapshot = { ...snapshot, uncertain: { checking: false, canRetry: false, text: `待确认：${err instanceof Error ? err.message : "保存结果尚未确定"}` } };
            message(op, { kind: "wait", text: "保存结果待确认，最新选择仍保留。" });
            publish();
            await verifyHead();
          }
        }
      }
    } finally {
      running = false;
      if (active && pending.length && !snapshot.uncertain) queueMicrotask(() => void pump());
    }
  }

  function submit(input: OrganizeIntent) {
    if (!projection) return;
    const state = projection;
    let intent: Operation["intent"];
    let affected: string[];
    let label: string | undefined;
    if (input.kind === "assign") {
      const local = new Map<string, PersonRef>();
      const ref = (role: Role, raw: string): PersonRef | null => {
        const name = clean(raw);
        const existing = state.people[group(role)].find(person => key(person.name) === key(name));
        if (existing) return { id: existing.id, name: existing.name };
        const aliased = state.aliases.get(aliasKey(role, name));
        if (aliased !== undefined) {
          const current = state.people[group(role)].find(person => person.id === aliased);
          return current ? { id: current.id, name: current.name } : null;
        }
        const identity = aliasKey(role, name);
        if (!local.has(identity)) local.set(identity, { id: temporaryID--, name });
        return local.get(identity)!;
      };
      intent = { kind: "assign", albums: [...new Set(input.albums)], batch: input.batch };
      if (input.author !== undefined) intent.author = input.author ? ref("author", input.author) : null;
      if (input.models !== undefined) intent.models = input.models.map(name => ref("model", name)).filter((person): person is PersonRef => person !== null).filter((person, index, list) => list.findIndex(item => item.id === person.id) === index);
      affected = intent.albums;
    } else {
      intent = { ...input };
      if (input.kind === "cleanStale") affected = [];
      else {
        label = state.people[group(input.role)].find(person => person.id === input.personId)?.name;
        affected = state.albums.filter(album => input.role === "author" ? album.author?.id === input.personId : album.models.some(person => person.id === input.personId)).map(album => album.id);
        if (intent.kind === "rename") intent.name = clean(intent.name);
      }
    }
    const channel = input.kind === "assign" ? input.batch ? "batch" : undefined : "list";
    const op: Operation = { intent, affected, channel, label };
    pending.push(op); revision++;
    snapshot = { ...snapshot, rejection: "" };
    for (const id of affected) { touched.add(id); errors.delete(id); }
    if (channel) latest[channel] = op;
    message(op, { kind: "wait", text: snapshot.uncertain ? "等待核对，最新修改已留在页面上。" : "正在保存…" });
    publish();
    void pump();
  }

  return {
    getSnapshot: () => snapshot,
    subscribe: (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; },
    async load() {
      active = true;
      if (pending.length) return;
      const at = ++revision;
      try {
        const state = await adapter.read();
        if (!active || revision !== at) return;
        confirmed = state; snapshot = { ...snapshot, loadError: "" }; publish();
      } catch (err) {
        if (active && revision === at) { snapshot = { ...snapshot, loadError: err instanceof Error ? err.message : "无法读取相册，请重试。" }; publish(); }
      }
    },
    submit,
    async verify() { await verifyHead(); void pump(); },
    retry() {
      if (!snapshot.uncertain?.canRetry || snapshot.uncertain.checking) return;
      snapshot = { ...snapshot, uncertain: null }; publish(); void pump();
    },
    clearMessage(channel: "batch" | "list") {
      delete latest[channel]; snapshot = { ...snapshot, [channel + "Message"]: null }; publish();
    },
    stop() { active = false; revision++; },
  };
}
