import { api, APIError, type AlbumPage, type People } from "./api";
import { defaultSort, sortAlbums } from "./albums";
import { OrganizeRejected, type OrganizeAdapter, type OrganizeResult, type Receipt } from "./album-organizer";

function resultIsConfirmed(result: OrganizeResult, id: string) {
  return result.operationId === id && Array.isArray(result.albums) && Array.isArray(result.people?.authors) && Array.isArray(result.people?.models);
}

export const organizeHTTP: OrganizeAdapter = {
  async read() {
    const [page, people] = await Promise.all([api<AlbumPage>("/api/albums"), api<People>("/api/people")]);
    return { albums: sortAlbums(page.albums, defaultSort), people };
  },
  async write(change) {
    try {
      const result = await api<OrganizeResult>("/api/organize", { method: "POST", body: JSON.stringify(change) });
      if (!resultIsConfirmed(result, change.operationId)) throw new Error("保存响应不完整，请核对结果");
      return result;
    } catch (err) {
      if (err instanceof APIError && [400, 403, 404, 422].includes(err.status)) throw new OrganizeRejected(err.message);
      throw err;
    }
  },
  async confirm(id) {
    const receipt = await api<Receipt>("/api/organize/receipts/" + encodeURIComponent(id));
    if (receipt.state === "committed" && resultIsConfirmed(receipt.result, id)) return receipt;
    if (receipt.state === "missing" || receipt.state === "expired") return receipt;
    throw new Error("保存回执不完整，请再次核对");
  },
};
