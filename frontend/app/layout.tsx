import type { Metadata, Viewport } from "next";
import type { ReactNode } from "react";
import "./globals.css";

export const metadata: Metadata = {
  title: "Juen's",
  description: "家里的照片，和云端的回忆。",
  robots: { index: false, follow: false },
  icons: { icon: "/favicon.svg" },
};
export const viewport: Viewport = { width: "device-width", initialScale: 1, viewportFit: "cover", colorScheme: "dark light" };

const preferenceScript = `try{const h=document.documentElement,m=localStorage.getItem('juens-theme')||'auto';h.dataset.mode=m;h.dataset.theme=m==='night'?'dark':m==='day'?'light':localStorage.getItem('juens-theme-auto')||(new Date().getHours()>=6&&new Date().getHours()<18?'light':'dark');h.classList.toggle('dark',h.dataset.theme==='dark');h.dataset.sidebar=localStorage.getItem('juens-sidebar')||(matchMedia('(max-width:719px)').matches?'collapsed':'expanded')}catch(e){}`;

export default function RootLayout({ children }: { children: ReactNode }) {
  return <html lang="zh-CN" data-theme="light" data-mode="auto" suppressHydrationWarning>
    <head><script dangerouslySetInnerHTML={{ __html: preferenceScript }} /></head>
    <body>{children}<noscript>打开相册需要浏览器启用 JavaScript。</noscript></body>
  </html>;
}
