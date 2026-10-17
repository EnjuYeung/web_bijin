import type { Metadata } from "next";
import { Login } from "@/components/login";

export const metadata: Metadata = { title: "Juen's" };

export default function LoginPage() {
  return <Login />;
}
