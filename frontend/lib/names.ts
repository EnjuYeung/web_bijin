// Author and model names, by the server's rules (cleanPersonName and nameKey
// in organize.go): trimmed, inner whitespace joined into one space, composed
// Unicode. Two names are the same person when they differ only in English
// letter case, as SQLite NOCASE compares them; "Élise" and "élise" stay two.
export const cleanName = (name: string) => name.normalize("NFC").replace(/\p{White_Space}+/gu, " ").replace(/^ +| +$/g, "");
export const nameIdentity = (name: string) => cleanName(name).replace(/[A-Z]/g, letter => letter.toLowerCase());
// Typing to filter a list ignores the case of every letter.
export const searchKey = (text: string) => cleanName(text).toLowerCase();
