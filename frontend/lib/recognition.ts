// Recognition suggestions on the organize page. One press asks the
// recognition service about at most 100 albums, 20 per request one after the
// other, so each request ends inside the reverse proxy's 60 seconds. The
// suggestions live only in this page; accepting one goes through the album
// organizer like any other edit.

export const recognizeBatch = 20;
export const recognizeLimit = 100;

export type Field = "author" | "models";
export interface SuggestedName { name: string; new: boolean }
export interface Suggestion { author?: SuggestedName; models?: SuggestedName[] }
export interface RecognizeAnswer { recognized: number; skipped: number; suggestions: ({ album: string } & Suggestion)[] }
export interface RecognitionAdapter {
  recognize(albums: string[], signal: AbortSignal): Promise<RecognizeAnswer>;
}
export interface RecognitionSnapshot {
  suggestions: Record<string, Suggestion>;
  running: boolean; done: number; total: number;
  // Summary of the last finished run, and why batches failed.
  summary: string; failures: string[];
}

export function createRecognition(adapter: RecognitionAdapter) {
  let snapshot: RecognitionSnapshot = { suggestions: {}, running: false, done: 0, total: 0, summary: "", failures: [] };
  const listeners = new Set<() => void>();
  let controller: AbortController | null = null;
  function publish(next: Partial<RecognitionSnapshot>) {
    snapshot = { ...snapshot, ...next };
    for (const listener of listeners) listener();
  }

  async function run(albums: string[]) {
    if (snapshot.running) return;
    const targets = [...new Set(albums)].slice(0, recognizeLimit);
    if (!targets.length) return;
    const current = new AbortController();
    controller = current;
    publish({ running: true, done: 0, total: targets.length, summary: "", failures: [] });
    let recognized = 0, suggested = 0, skipped = 0, failed = 0;
    const failures: string[] = [];
    for (let start = 0; start < targets.length; start += recognizeBatch) {
      const batch = targets.slice(start, start + recognizeBatch);
      try {
        const answer = await adapter.recognize(batch, current.signal);
        if (current.signal.aborted) return;
        const suggestions = { ...snapshot.suggestions };
        // A new answer replaces what an earlier run said about these albums.
        for (const album of batch) delete suggestions[album];
        for (const { album, ...suggestion } of answer.suggestions) {
          if (!batch.includes(album)) continue;
          suggestions[album] = suggestion;
          suggested += (suggestion.author ? 1 : 0) + (suggestion.models?.length ? 1 : 0);
        }
        recognized += answer.recognized; skipped += answer.skipped;
        publish({ suggestions, done: start + batch.length });
      } catch (err) {
        if (current.signal.aborted) return;
        failed += batch.length;
        failures.push(`第 ${start / recognizeBatch + 1} 批（${batch.length} 本）：${err instanceof Error ? err.message : "识别失败"}`);
        publish({ done: start + batch.length, failures: [...failures] });
      }
    }
    controller = null;
    const parts = [`识别 ${recognized} 本，给出 ${suggested} 条建议，跳过 ${skipped} 条`];
    if (failed) parts.push(`${failed} 本失败`);
    publish({ running: false, summary: parts.join("，") + "。" });
  }

  // drop forgets one field's suggestion, after accepting or ignoring it.
  function drop(album: string, field: Field) {
    const current = snapshot.suggestions[album];
    if (!current?.[field]) return;
    const rest: Suggestion = { ...current };
    delete rest[field];
    const suggestions = { ...snapshot.suggestions };
    if (rest.author || rest.models?.length) suggestions[album] = rest;
    else delete suggestions[album];
    publish({ suggestions });
  }

  return {
    getSnapshot: () => snapshot,
    subscribe(listener: () => void) { listeners.add(listener); return () => { listeners.delete(listener); }; },
    run,
    drop,
    // stop ends a run when the page goes away; answers still on the way are ignored.
    stop() {
      controller?.abort(); controller = null;
      if (snapshot.running) publish({ running: false });
    },
  };
}
