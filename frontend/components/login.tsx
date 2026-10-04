"use client";

import { useEffect, useState, type FormEvent } from "react";
import { ArrowRight, CircleAlert } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { Alert, AlertDescription } from "@/components/ui/alert";

export function Login() {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => { if (new URLSearchParams(location.search).get("err") === "1") setError("用户名或密码不对，请重新输入。"); }, []);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    setBusy(true); setError("");
    const form = new FormData(event.currentTarget);
    try {
      await api("/api/login", { method: "POST", body: JSON.stringify({ user: form.get("user"), pass: form.get("pass") }) });
      const next = new URLSearchParams(location.search).get("next") || "/";
      const safe = next.startsWith("/") && !next.startsWith("//") && !next.startsWith("/login") ? next : "/";
      location.assign(safe + (safe.includes("#") ? "" : location.hash));
    } catch (err) { setError(err instanceof Error && err.message === "unauthorized" ? "用户名或密码不对，请重新输入。" : "暂时无法登录，请检查连接后重试。"); }
    finally { setBusy(false); }
  }
  return <main className="gate-page">
    <form id="gate" className="gate" onSubmit={submit} aria-busy={busy}>
      <h1>Juen&apos;s</h1><p className="gate-intro">把家里的回忆，留在这里。</p>
      <FieldGroup>
        <Field><FieldLabel htmlFor="login-user">用户名</FieldLabel><Input id="login-user" name="user" required autoFocus autoComplete="username" spellCheck={false} disabled={busy} aria-invalid={!!error} /></Field>
        <Field><FieldLabel htmlFor="login-password">密码</FieldLabel><Input id="login-password" name="pass" type="password" required autoComplete="current-password" disabled={busy} aria-invalid={!!error} /></Field>
      </FieldGroup>
      {error && <Alert variant="destructive" id="gate-err"><CircleAlert aria-hidden="true" /><AlertDescription>{error}</AlertDescription></Alert>}
      <Button type="submit" disabled={busy}>{busy ? <Spinner aria-label="正在登录" data-icon="inline-start" /> : <ArrowRight aria-hidden="true" data-icon="inline-start" />}{busy ? "正在进入…" : "进入相册"}</Button>
    </form>
  </main>;
}
