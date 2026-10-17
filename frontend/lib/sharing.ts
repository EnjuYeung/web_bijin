// Shared by single-photo sharing in the viewer and batch sharing by album.

// A share address as the server writes it: /i/<43-character id>.<extension>.
const sharePattern = /^\/i\/[A-Za-z0-9_-]{43}\.(jpg|png|gif|webp)$/;
export function isSharePath(value: unknown): value is string {
  return typeof value === "string" && sharePattern.test(value);
}

// Who changed sharing in this tab: the photo open in the viewer, or a batch
// task. A gallery that only shows shared photos updates itself for the first.
export type SharingSource = "photo" | "batch";
const sharingEvent = "bijin-sharing-changed";
export function sharingChanged(source: SharingSource) {
  dispatchEvent(new CustomEvent<SharingSource>(sharingEvent, { detail: source }));
}
export function onSharingChanged(listener: (source: SharingSource) => void) {
  const handle = (event: Event) => listener((event as CustomEvent<SharingSource>).detail);
  addEventListener(sharingEvent, handle);
  return () => removeEventListener(sharingEvent, handle);
}

// Safari lets a page write the clipboard only during the click, but the
// address is known after the server answers. Start the write in the click
// with text still to come, then finish or cancel it.
export function startCopy() {
  let resolve: ((blob: Blob) => void) | undefined;
  let reject: ((err: unknown) => void) | undefined;
  let writing: Promise<boolean> | undefined;
  if (navigator.clipboard?.write && typeof ClipboardItem !== "undefined") {
    const text = new Promise<Blob>((yes, no) => { resolve = yes; reject = no; });
    text.catch(() => {});
    try { writing = navigator.clipboard.write([new ClipboardItem({ "text/plain": text })]).then(() => true, () => false); }
    catch { writing = Promise.resolve(false); }
  }
  return {
    // True when the text reached the clipboard; false means copy by hand.
    async finish(text: string, wanted: () => boolean = () => true) {
      resolve?.(new Blob([text], { type: "text/plain" }));
      if (writing && await writing) return true;
      if (!wanted()) return false;
      try { await navigator.clipboard.writeText(text); return true; } catch { return false; }
    },
    cancel(reason: unknown) { reject?.(reason); },
  };
}
