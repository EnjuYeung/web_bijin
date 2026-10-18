"use client";

import { useEffect, useState, type FormEvent } from "react";
import { ArrowRight, CircleAlert, CircleCheck } from "lucide-react";
import { api, APIError, markSignedOut } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { Alert, AlertDescription } from "@/components/ui/alert";

// The address to return to after signing in, only ever on this site. Browsers
// read "/\host" and "/<tab>/host" as another site, so the check is the
// browser's own URL parsing rather than a look at the text.
function returnAddress() {
  try {
    const url = new URL(new URLSearchParams(location.search).get("next") || "/", location.origin);
    if (url.origin === location.origin && !url.pathname.startsWith("/login")) return url.pathname + url.search + url.hash;
  } catch { /* Not an address: go to the photos. */ }
  return "/";
}

export function Login() {
  const [error, setError] = useState<"" | "wrong" | "locked" | "offline">("");
  const [busy, setBusy] = useState(false);
  // "unknown" when the server could not be asked: show the code box but do not require it.
  const [twoStep, setTwoStep] = useState<"off" | "on" | "unknown">("off");
  const [signedOut, setSignedOut] = useState(false);
  const message = {
    "": "",
    wrong: twoStep === "off" ? "用户名或密码不对，请重新输入。" : "用户名、密码或动态码不对，请重新输入。",
    locked: "输错次数太多，请稍后再试（最多等 15 分钟）。",
    offline: "暂时无法登录，请检查连接后重试。",
  }[error];
  useEffect(() => {
    api<{ twoStep: boolean }>("/api/login-options").then((o) => setTwoStep(o.twoStep ? "on" : "off"), () => setTwoStep("unknown"));
    const query = new URLSearchParams(location.search);
    const err = query.get("err");
    if (err === "1") setError("wrong");
    if (err === "locked") setError("locked");
    // The server only shows this page to a device that is signed out.
    if (query.get("out") === "1") { setSignedOut(true); markSignedOut(); }
  }, []);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    setBusy(true); setError("");
    const form = new FormData(event.currentTarget);
    try {
      await api("/api/login", { method: "POST", body: JSON.stringify({ user: form.get("user"), pass: form.get("pass"), code: form.get("code") ?? "" }) });
      const next = returnAddress();
      location.assign(next + (next.includes("#") ? "" : location.hash));
    } catch (err) { setError(err instanceof APIError && err.status === 429 ? "locked" : err instanceof Error && err.message === "unauthorized" ? "wrong" : "offline"); }
    finally { setBusy(false); }
  }
  return <main className="gate-page">
    {/* Before the page script runs, the browser posts the form itself:
        method and action keep the password out of the address bar. */}
    <form id="gate" className="gate" method="post" action="/api/login" onSubmit={submit} aria-busy={busy}>
      <h1>Juen&apos;s</h1>
      <FieldGroup>
        <Field><FieldLabel htmlFor="login-user">用户名</FieldLabel><Input id="login-user" name="user" required autoFocus autoComplete="username" spellCheck={false} disabled={busy} aria-invalid={!!error} /></Field>
        <Field><FieldLabel htmlFor="login-password">密码</FieldLabel><Input id="login-password" name="pass" type="password" required autoComplete="current-password" disabled={busy} aria-invalid={!!error} /></Field>
        {twoStep !== "off" && <Field><FieldLabel htmlFor="login-code">动态码</FieldLabel><Input id="login-code" name="code" required={twoStep === "on"} inputMode="numeric" autoComplete="one-time-code" spellCheck={false} disabled={busy} aria-invalid={!!error} /></Field>}
      </FieldGroup>
      {error && <Alert variant="destructive" id="gate-err"><CircleAlert aria-hidden="true" /><AlertDescription>{message}</AlertDescription></Alert>}
      {!error && signedOut && <Alert id="gate-out" className="gate-note" role="status"><CircleCheck aria-hidden="true" /><AlertDescription>已退出登录。</AlertDescription></Alert>}
      <Button type="submit" disabled={busy}>{busy ? <Spinner aria-label="正在登录" data-icon="inline-start" /> : <ArrowRight aria-hidden="true" data-icon="inline-start" />}{busy ? "正在进入…" : "进入相册"}</Button>
    </form>
  </main>;
}
