import { api } from "./api";
import type { RecognitionAdapter, RecognizeAnswer } from "./recognition";

export const recognitionHTTP: RecognitionAdapter = {
  async recognize(albums, signal) {
    const answer = await api<RecognizeAnswer>("/api/recognize", { method: "POST", body: JSON.stringify({ albums }), signal });
    if (!Array.isArray(answer.suggestions) || typeof answer.recognized !== "number") throw new Error("识别响应不完整，请重试");
    return answer;
  },
};
