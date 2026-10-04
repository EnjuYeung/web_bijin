import { cp, mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { gzipSync } from "node:zlib";

const web = resolve("../web");
await rm(web, { recursive: true, force: true });
await mkdir(web, { recursive: true });
await cp(resolve("out"), web, { recursive: true });
async function compress(directory) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) await compress(path);
    else if (/\.(css|js)$/.test(entry.name)) await writeFile(path + ".gz", gzipSync(await readFile(path), { level: 9 }));
  }
}
await compress(resolve(web, "_next/static"));
await writeFile(resolve(web, ".gitkeep"), "");
