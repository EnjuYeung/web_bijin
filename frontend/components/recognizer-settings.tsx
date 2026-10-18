"use client";

import { useRef, useState, type FormEvent } from "react";
import { CircleAlert, Pencil, Sparkles, Trash2 } from "lucide-react";
import { api, type Reasoning, type RecognizerInput, type RecognizerProvider, type RecognizerView } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardAction, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Spinner } from "@/components/ui/spinner";
import { Separator } from "@/components/ui/separator";
import { OptionSelect } from "@/components/option-select";

const providers: { value: RecognizerProvider; label: string; model: string }[] = [
  { value: "claude", label: "Claude（Anthropic）", model: "例如 claude-opus-5-5" },
  { value: "openai", label: "OpenAI", model: "填写 OpenAI 控制台里的模型名" },
  { value: "gemini", label: "Gemini（Google）", model: "填写 Google AI Studio 里的模型名" },
  { value: "grok", label: "Grok（xAI）", model: "例如 grok-4.7" },
  { value: "custom", label: "其他兼容服务", model: "填写这家服务的模型名" },
];
// The select needs a non-empty value for "default", which the API sends as "".
type ReasoningChoice = "default" | "low" | "medium" | "high";
const reasoningChoices: { value: ReasoningChoice; label: string }[] = [
  { value: "default", label: "默认（由模型决定）" }, { value: "low", label: "低" }, { value: "medium", label: "中" }, { value: "high", label: "高" },
];
const reasoningLabel = (value: Reasoning) => reasoningChoices.find(choice => choice.value === (value || "default"))!.label;
const providerLabel = (value: string) => providers.find(item => item.value === value)?.label ?? value;
// The server saves a custom address trimmed and without a trailing slash.
const trimEndpoint = (value: string) => value.trim().replace(/\/+$/, "");
type Form = { provider: RecognizerProvider; endpoint: string; apiKey: string; model: string; reasoning: ReasoningChoice };
type FormKey = "endpoint" | "apiKey" | "model";

const notConfigured: RecognizerView = { configured: false, provider: "", endpoint: "", model: "", reasoning: "", hasKey: false };

// The settings page reads the card's state with everything else, so leaving the
// page never cuts off a request of its own.
export function RecognizerCard({ initial }: { initial: RecognizerView }) {
  const [view, setView] = useState<RecognizerView>(initial);
  const [editing, setEditing] = useState(false);
  const [form, setForm] = useState<Form>({ provider: "claude", endpoint: "", apiKey: "", model: "", reasoning: "default" });
  const [errors, setErrors] = useState<Partial<Record<FormKey, string>>>({});
  const [message, setMessage] = useState<{ text: string; kind: "ok" | "err" | "wait" } | null>(null);
  const [pending, setPending] = useState<"save" | "test" | "delete" | null>(null);
  const editor = useRef<HTMLFormElement>(null);

  // A saved key is reused only for the same service, and for 其他兼容服务 only
  // at the same address: the server refuses to send it anywhere else.
  const sameEndpoint = form.provider !== "custom" || trimEndpoint(form.endpoint) === trimEndpoint(view.endpoint);
  const keyKept = view.configured && view.hasKey && view.provider === form.provider && sameEndpoint;
  function open() {
    setForm(view.configured && view.provider ? { provider: view.provider, endpoint: view.endpoint, apiKey: "", model: view.model, reasoning: view.reasoning || "default" } : { provider: "claude", endpoint: "", apiKey: "", model: "", reasoning: "default" });
    setErrors({}); setMessage(null); setEditing(true);
    requestAnimationFrame(() => { editor.current?.scrollIntoView({ block: "nearest" }); document.getElementById("recognizer-provider")?.focus(); });
  }
  function close() { setEditing(false); setErrors({}); setMessage(null); }
  function change(key: FormKey, value: string) {
    setForm(previous => ({ ...previous, [key]: value }));
    setErrors(previous => ({ ...previous, [key]: undefined }));
  }
  function input(): RecognizerInput {
    return { provider: form.provider, endpoint: form.provider === "custom" ? form.endpoint : "", apiKey: form.apiKey, model: form.model, reasoning: form.reasoning === "default" ? "" : form.reasoning };
  }
  function validate() {
    const found: Partial<Record<FormKey, string>> = {};
    if (form.provider === "custom" && !form.endpoint.trim()) found.endpoint = "请填写接口地址。";
    if (!keyKept && !form.apiKey.trim()) found.apiKey = !view.configured ? "请填写 API Key。"
      : view.provider !== form.provider ? "换了识别服务，请填写这家服务的 API Key。"
      : !sameEndpoint ? "换了接口地址，请重新填写 API Key。" : "请填写 API Key。";
    if (!form.model.trim()) found.model = "请填写模型名。";
    setErrors(found);
    const first = Object.keys(found)[0];
    if (first) { requestAnimationFrame(() => document.getElementById("recognizer-" + first)?.focus()); return false; }
    return true;
  }
  async function test() {
    if (pending || !validate()) return;
    setPending("test"); setMessage({ text: "正在向识别服务发送一次测试请求…", kind: "wait" });
    try {
      const result = await api<{ ms: number }>("/api/recognizer/test", { method: "POST", body: JSON.stringify(input()) });
      setMessage({ text: `测试成功，识别服务 ${(result.ms / 1000).toFixed(1)} 秒内按要求的格式回答了。`, kind: "ok" });
    } catch (err) { setMessage({ text: err instanceof Error ? err.message : "测试失败，请重试。", kind: "err" }); }
    finally { setPending(null); }
  }
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending || !validate()) return;
    setPending("save"); setMessage({ text: "正在保存…", kind: "wait" });
    try {
      const saved = await api<RecognizerView>("/api/recognizer", { method: "PUT", body: JSON.stringify(input()) });
      setView(saved); close();
    } catch (err) { setMessage({ text: err instanceof Error ? err.message : "保存失败，请重试。", kind: "err" }); }
    finally { setPending(null); }
  }
  async function remove() {
    if (pending || !confirm("删除识别服务设置？\n\n保存的 API Key 会一起删除；整理页将不能再识别，已填写的作者和模特不受影响。")) return;
    setPending("delete");
    try {
      await api("/api/recognizer", { method: "DELETE" });
      setView(notConfigured); close();
    } catch (err) { setMessage({ text: err instanceof Error ? err.message : "删除失败，请重试。", kind: "err" }); }
    finally { setPending(null); }
  }

  const current = providers.find(item => item.value === form.provider)!;
  return <Card className="source-card" id="recognizer-card">
    <CardHeader>
      <CardTitle><h2><Sparkles aria-hidden="true" />识别服务</h2></CardTitle>
      <CardAction><Badge variant="outline" id="recognizer-state">{view.configured ? "已配置" : "未配置"}</Badge></CardAction>
    </CardHeader>
    <CardContent>
      {!editing && message?.kind === "err" && <Alert variant="destructive" id="recognizer-delete-msg"><CircleAlert aria-hidden="true" /><AlertDescription>{message.text}</AlertDescription></Alert>}
      {!editing && (view.configured ? <div className="storage-row">
        <dl className="source-facts storage-main" id="recognizer-facts">
          <div><dt>服务</dt><dd id="recognizer-provider-value">{providerLabel(view.provider)}{view.provider === "custom" && <span className="wall-split">{view.endpoint}</span>}</dd></div>
          <div><dt>模型</dt><dd id="recognizer-model-value">{view.model}</dd></div>
          <div><dt>推理强度</dt><dd id="recognizer-reasoning-value">{reasoningLabel(view.reasoning)}</dd></div>
          <div><dt>API Key</dt><dd>{view.hasKey ? "已保存" : "未保存"}</dd></div>
        </dl>
        <div className="storage-actions"><Button id="recognizer-edit" variant="outline" disabled={!!pending} onClick={open}><Pencil data-icon="inline-start" aria-hidden="true" />编辑</Button><Button id="recognizer-delete" variant="destructive" disabled={!!pending} onClick={() => void remove()}><Trash2 data-icon="inline-start" aria-hidden="true" />删除</Button></div>
      </div> : <Empty><EmptyHeader><EmptyMedia variant="icon"><Sparkles aria-hidden="true" /></EmptyMedia><EmptyTitle>还没有配置识别服务</EmptyTitle><EmptyDescription>配置后，整理页可以一键为没填的相册给出作者和模特建议。</EmptyDescription></EmptyHeader><Button id="recognizer-add" onClick={open}>配置识别服务</Button></Empty>)}
      {editing && <form id="recognizer-form" className="storage-editor" ref={editor} onSubmit={save} noValidate aria-busy={!!pending} onKeyDown={event => { if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); if (!pending) close(); } }}>
        <h3>{view.configured ? "编辑识别服务" : "配置识别服务"}</h3><p className="editor-intro">带 * 的项目为必填。费用由你在服务商的账户支付。</p>
        <FieldGroup className="storage-fields">
          <Field data-disabled={!!pending}><FieldLabel htmlFor="recognizer-provider">服务</FieldLabel><OptionSelect id="recognizer-provider" label="服务" value={form.provider} choices={providers} disabled={!!pending} onChange={value => { setForm(previous => ({ ...previous, provider: value })); setErrors({}); }} /><FieldDescription>{form.provider === "claude" ? "使用 Claude 自己的接口。" : form.provider === "custom" ? "例如 DeepSeek、OpenRouter 等兼容 OpenAI 格式的服务。" : "使用这家服务兼容 OpenAI 格式的接口。"}</FieldDescription></Field>
          {form.provider === "custom" && <TextField name="endpoint" label="接口地址" value={form.endpoint} onChange={change} required error={errors.endpoint} helper="填到 /v1 为止，例如 https://api.deepseek.com/v1" disabled={!!pending} />}
          <TextField name="apiKey" label="API Key" value={form.apiKey} onChange={change} password required={!keyKept} error={errors.apiKey} helper={keyKept ? "已保存，留空表示不修改" : "只保存在服务器，保存后不会返回"} disabled={!!pending} />
          <TextField name="model" label="模型名" value={form.model} onChange={change} required error={errors.model} helper={current.model} disabled={!!pending} />
          <Field data-disabled={!!pending}><FieldLabel htmlFor="recognizer-reasoning">推理强度</FieldLabel><OptionSelect id="recognizer-reasoning" label="推理强度" value={form.reasoning} choices={reasoningChoices} disabled={!!pending} onChange={value => setForm(previous => ({ ...previous, reasoning: value }))} /><FieldDescription>越高越仔细，也越慢、越贵。模型不支持时测试会报错，改回默认即可。</FieldDescription></Field>
        </FieldGroup>
        {message?.kind === "err" ? <Alert variant="destructive" id="recognizer-msg"><CircleAlert aria-hidden="true" /><AlertDescription>{message.text}</AlertDescription></Alert> : <p id="recognizer-msg" className="form-message" data-kind={message?.kind} role="status" aria-live="polite">{message?.text || ""}</p>}
        <Separator /><div className="form-actions"><Button id="recognizer-save" type="submit" disabled={!!pending}>{pending === "save" && <Spinner data-icon="inline-start" aria-label="正在保存" />}{pending === "save" ? "保存中…" : "保存"}</Button><Button id="recognizer-test" type="button" variant="outline" disabled={!!pending} onClick={() => void test()}>{pending === "test" && <Spinner data-icon="inline-start" aria-label="正在测试" />}{pending === "test" ? "测试中…" : "测试"}</Button><Button id="recognizer-cancel" type="button" variant="ghost" disabled={!!pending} onClick={close}>取消</Button></div>
      </form>}
    </CardContent>
  </Card>;
}

function TextField({ name, label, value, onChange, helper, error, required, password, disabled }: {
  name: FormKey; label: string; value: string; onChange: (name: FormKey, value: string) => void;
  helper?: string; error?: string; required?: boolean; password?: boolean; disabled: boolean;
}) {
  const id = "recognizer-" + name;
  return <Field data-invalid={!!error} data-disabled={disabled}>
    <FieldLabel htmlFor={id}>{label}{required && <span className="required-mark" aria-label="必填">*</span>}</FieldLabel>
    <Input id={id} name={name} type={password ? "password" : "text"} value={value} onChange={event => onChange(name, event.target.value)}
      autoComplete={password ? "new-password" : "off"} autoCapitalize="off" spellCheck={false} disabled={disabled} inputMode={name === "endpoint" ? "url" : undefined}
      aria-required={required} aria-invalid={!!error} aria-describedby={[helper ? id + "-hint" : "", error ? id + "-error" : ""].filter(Boolean).join(" ") || undefined}
      placeholder={name === "endpoint" ? "https://api.example.com/v1" : undefined} />
    {helper && <FieldDescription id={id + "-hint"}>{helper}</FieldDescription>}
    {error && <FieldError id={id + "-error"}>{error}</FieldError>}
  </Field>;
}
