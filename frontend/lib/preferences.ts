"use client";

import { useCallback, useEffect, useState } from "react";

export type Mode = "auto" | "night" | "day";
export const modeLabels: Record<Mode, string> = { auto: "自动", night: "黑夜", day: "白天" };

function read(key: string, fallback: string) {
  try { return localStorage.getItem(key) || fallback; } catch { return fallback; }
}
function write(key: string, value: string) {
  try { localStorage.setItem(key, value); } catch { /* Preferences remain available for this visit. */ }
}
function paintMode(mode: Mode, timezone: string) {
  let hour = new Date().getHours();
  try { hour = Number(new Intl.DateTimeFormat("en-US", { timeZone: timezone, hour: "numeric", hourCycle: "h23" }).format(new Date())); } catch { /* Use browser time as a fallback. */ }
  const dark = mode === "night" || (mode === "auto" && (hour < 6 || hour >= 18));
  const root = document.documentElement;
  root.dataset.mode = mode;
  root.dataset.theme = dark ? "dark" : "light";
  root.classList.toggle("dark", dark);
  if (mode === "auto") write("juens-theme-auto", dark ? "dark" : "light");
}

export function usePreferences(timezone: string) {
  const [mode, setMode] = useState<Mode>("auto");
  const [expanded, setExpanded] = useState(true);
  useEffect(() => {
    const saved = read("juens-theme", "auto");
    const valid = saved === "day" || saved === "night" ? saved : "auto";
    setMode(valid);
    const open = read("juens-sidebar", matchMedia("(max-width:719px)").matches ? "collapsed" : "expanded") === "expanded";
    setExpanded(open);
    document.documentElement.dataset.sidebar = open ? "expanded" : "collapsed";
  }, []);
  useEffect(() => {
    paintMode(mode, timezone);
    if (mode !== "auto") return;
    const timer = setInterval(() => paintMode(mode, timezone), 60000);
    return () => clearInterval(timer);
  }, [mode, timezone]);
  const toggleSidebar = useCallback((open?: boolean) => {
    setExpanded(previous => {
      const next = open ?? !previous;
      document.documentElement.dataset.sidebar = next ? "expanded" : "collapsed";
      write("juens-sidebar", next ? "expanded" : "collapsed");
      return next;
    });
  }, []);
  const cycleMode = useCallback(() => {
    const modes: Mode[] = ["auto", "night", "day"];
    setMode(previous => {
      const next = modes[(modes.indexOf(previous) + 1) % modes.length];
      write("juens-theme", next);
      return next;
    });
  }, []);
  return { mode, expanded, toggleSidebar, cycleMode };
}
