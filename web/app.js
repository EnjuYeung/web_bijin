(() => {
  const grid = document.getElementById("grid");
  const countEl = document.getElementById("count");
  const note = document.getElementById("note");
  const lb = document.getElementById("lightbox");
  const lbStage = document.getElementById("lb-stage");
  const lbImg = document.getElementById("lb-img");
  const lbCap = document.getElementById("lb-cap");
  const lbPrev = document.getElementById("lb-prev");
  const lbNext = document.getElementById("lb-next");
  const lbMeta = document.getElementById("lb-meta");
  const lbMetaToggle = document.getElementById("lb-meta-toggle");
  const themeBtn = document.getElementById("theme-btn");
  const root = document.documentElement;

  const THEME_KEY = "juens-theme";
  const THEME_AUTO_KEY = "juens-theme-auto";
  const MODES = ["auto", "night", "day"];
  const MODE_LABEL = { day: "白天", auto: "自动", night: "黑夜" };
  const GAP = 4;

  const items = [];
  const byId = new Map();
  const rendered = new Map();
  let layouts = [];
  let seed = null;
  let tz = "Asia/Shanghai";
  let next = null;
  let loading = false;
  let finished = false;
  let savedY = 0;
  let paintQueued = false;
  let wantId = "";
  let metaUserSet = false;

  function setNote(text) {
    if (!text) {
      note.hidden = true;
      note.textContent = "";
      return;
    }
    note.hidden = false;
    note.textContent = text;
  }

  function hourInTZ(name) {
    try {
      const parts = new Intl.DateTimeFormat("en-US", {
        timeZone: name,
        hour: "numeric",
        hourCycle: "h23"
      }).formatToParts(new Date());
      const hour = parts.find((p) => p.type === "hour");
      return Number(hour && hour.value);
    } catch (err) {
      return new Date().getHours();
    }
  }

  function autoTheme() {
    const hour = hourInTZ(tz);
    return hour >= 6 && hour < 18 ? "light" : "dark";
  }

  function currentMode() {
    try {
      return localStorage.getItem(THEME_KEY) || "auto";
    } catch (err) {
      return "auto";
    }
  }

  function applyTheme() {
    const mode = currentMode();
    root.dataset.mode = mode;
    let theme = "light";
    if (mode === "night") theme = "dark";
    else if (mode === "day") theme = "light";
    else {
      theme = autoTheme();
      try { localStorage.setItem(THEME_AUTO_KEY, theme); } catch (err) {}
    }
    root.dataset.theme = theme;
    if (themeBtn) {
      themeBtn.textContent = MODE_LABEL[mode] || "自动";
      themeBtn.setAttribute("aria-label", "显示模式 " + (MODE_LABEL[mode] || "自动") + "，点击切换");
    }
  }

  function setMode(mode) {
    try { localStorage.setItem(THEME_KEY, mode); } catch (err) {}
    applyTheme();
  }

  function cycleMode() {
    const i = MODES.indexOf(currentMode());
    setMode(MODES[(i + 1) % MODES.length]);
  }

  function photoIdFromHash() {
    const m = /^#p\/(\d+)$/.exec(location.hash);
    return m ? m[1] : "";
  }

  function colCount(width) {
    if (width < 560) return 2;
    if (width < 900) return 3;
    return 4;
  }

  function desktopView() {
    return window.matchMedia("(min-width: 720px)").matches;
  }

  function humanSize(n) {
    const num = Number(n) || 0;
    if (num >= 1073741824) return (num / 1073741824).toFixed(2) + " GB";
    if (num >= 1048576) return (num / 1048576).toFixed(1) + " MB";
    if (num >= 1024) return Math.round(num / 1024) + " KB";
    return num + " B";
  }

  function yearDate(p) {
    if (p.date) {
      const parts = String(p.date).split("-");
      if (parts.length === 3) {
        return Number(parts[0]) + "年" + Number(parts[1]) + "月" + Number(parts[2]) + "日";
      }
    }
    if (p.year) return String(p.year) + "年";
    return "";
  }

  function pixels(p) {
    const n = (Number(p.w) || 0) * (Number(p.h) || 0);
    if (!n) return "—";
    const mp = n / 1000000;
    const mpText = mp >= 10 ? mp.toFixed(1) : mp.toFixed(2);
    return n.toLocaleString("zh-CN") + "（" + mpText + " MP）";
  }

  function goLogin() {
    location.href = "/login?next=" + encodeURIComponent(location.pathname + location.search + location.hash);
  }

  function relayout() {
    const width = grid.clientWidth;
    if (width <= 0) {
      grid.style.height = "0px";
      return;
    }
    const cols = colCount(width);
    const cw = (width - GAP * (cols - 1)) / cols;
    const colH = new Array(cols).fill(0);
    layouts = items.map((p) => {
      const ch = p.w > 0 ? (cw * p.h) / p.w : cw;
      let c = 0;
      for (let i = 1; i < cols; i++) {
        if (colH[i] < colH[c]) c = i;
      }
      const x = c * (cw + GAP);
      const y = colH[c];
      colH[c] += ch + GAP;
      return { id: p.id, x, y, cw, ch };
    });
    const tallest = colH.length ? Math.max.apply(null, colH) : 0;
    grid.style.height = Math.max(0, tallest - GAP) + "px";
    paint();
  }

  function card(p) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "sheet";
    b.dataset.id = String(p.id);
    b.setAttribute("aria-label", p.title || p.name);
    const img = document.createElement("img");
    img.src = p.thumb;
    img.alt = "";
    img.decoding = "async";
    const meta = document.createElement("span");
    meta.className = "sheet-meta";
    const r1 = document.createElement("span");
    r1.className = "sheet-r1";
    r1.textContent = p.title || p.name || "";
    const r2 = document.createElement("span");
    r2.className = "sheet-r2";
    r2.textContent = yearDate(p);
    const r3 = document.createElement("span");
    r3.className = "sheet-r3";
    const bits = [];
    if (p.format) bits.push(p.format);
    if (p.w && p.h) bits.push(p.w + "×" + p.h);
    if (p.size) bits.push(humanSize(p.size));
    r3.textContent = bits.join("  ·  ");
    meta.append(r1, r2, r3);
    b.append(img, meta);
    b.addEventListener("click", () => openPhoto(p.id));
    return b;
  }

  function paint() {
    const gridTop = grid.getBoundingClientRect().top + window.scrollY;
    const viewTop = window.scrollY - window.innerHeight;
    const viewBot = window.scrollY + window.innerHeight * 2;
    const vis = new Set();
    for (let i = 0; i < layouts.length; i++) {
      const L = layouts[i];
      const top = gridTop + L.y;
      const bot = top + L.ch;
      if (bot < viewTop || top > viewBot) continue;
      vis.add(L.id);
      let el = rendered.get(L.id);
      if (!el) {
        const p = byId.get(L.id);
        if (!p) continue;
        el = card(p);
        rendered.set(L.id, el);
        grid.appendChild(el);
      }
      el.style.transform = "translate(" + L.x + "px," + L.y + "px)";
      el.style.width = L.cw + "px";
      el.style.height = L.ch + "px";
    }
    rendered.forEach((el, id) => {
      if (!vis.has(id)) {
        el.remove();
        rendered.delete(id);
      }
    });
    const last = layouts[layouts.length - 1];
    if (last && gridTop + last.y < window.scrollY + window.innerHeight * 3) {
      loadMore();
    }
  }

  function queuePaint() {
    if (paintQueued) return;
    paintQueued = true;
    requestAnimationFrame(() => {
      paintQueued = false;
      paint();
    });
  }

  function preferMeta() {
    return desktopView();
  }

  function setMetaOpen(open) {
    lb.classList.toggle("meta-on", open);
    lb.classList.toggle("meta-off", !open);
    lbMetaToggle.setAttribute("aria-expanded", open ? "true" : "false");
    lbMetaToggle.textContent = open ? "收起" : "信息";
    lbMetaToggle.setAttribute("aria-label", open ? "收起信息" : "显示信息");
  }

  function fillMeta(p) {
    const map = {
      name: p.title || p.name || "—",
      format: p.format || "—",
      size: p.size ? humanSize(p.size) : "—",
      res: p.w && p.h ? p.w + " × " + p.h : "—",
      px: pixels(p),
      disk: p.size ? humanSize(p.size) : "—"
    };
    lbMeta.querySelectorAll("[data-k]").forEach((el) => {
      el.textContent = map[el.dataset.k] || "—";
    });
  }

  function hideLightbox() {
    lb.hidden = true;
    lbImg.removeAttribute("src");
    lbCap.textContent = "";
    document.body.classList.remove("looking");
  }

  function showLightbox(id) {
    const p = byId.get(Number(id)) || byId.get(id);
    if (!p) return false;
    lbImg.src = p.src;
    lbImg.alt = p.title || p.name;
    lbCap.textContent = p.title || p.name;
    fillMeta(p);
    if (!metaUserSet) setMetaOpen(preferMeta());
    lb.hidden = false;
    document.body.classList.add("looking");
    syncNav(p.id);
    return true;
  }

  function indexOf(id) {
    const n = Number(id);
    for (let i = 0; i < items.length; i++) {
      if (items[i].id === n) return i;
    }
    return -1;
  }

  function syncNav(id) {
    const i = indexOf(id);
    lbPrev.hidden = i <= 0;
    lbNext.hidden = i < 0 || (i >= items.length - 1 && finished);
  }

  function openPhoto(id, replace) {
    if (lb.hidden) {
      savedY = window.scrollY;
      metaUserSet = false;
    }
    if (!showLightbox(id)) {
      wantId = String(id);
      loadMore();
      return;
    }
    const want = "#p/" + id;
    if (location.hash !== want) {
      const state = { lb: String(id), y: savedY };
      if (replace) history.replaceState(state, "", want);
      else history.pushState(state, "", want);
    }
  }

  function closePhoto() {
    hideLightbox();
    wantId = "";
    metaUserSet = false;
    if (photoIdFromHash()) {
      history.pushState({ lb: null, y: savedY }, "", location.pathname + location.search);
    }
    window.scrollTo(0, savedY);
  }

  async function step(delta) {
    const id = photoIdFromHash();
    let i = indexOf(id);
    if (i < 0) return;
    const target = i + delta;
    if (target < 0) return;
    if (target >= items.length) {
      if (finished) return;
      await loadMore();
      i = indexOf(id);
      if (i < 0 || i + delta >= items.length) return;
      openPhoto(items[i + delta].id, true);
      return;
    }
    openPhoto(items[target].id, true);
  }

  function syncFromURL() {
    const id = photoIdFromHash();
    if (id) {
      if (lb.hidden) savedY = window.scrollY;
      if (!showLightbox(id)) {
        wantId = id;
        loadMore();
      }
      return;
    }
    hideLightbox();
    const y = history.state && typeof history.state.y === "number" ? history.state.y : savedY;
    window.scrollTo(0, y);
  }

  async function loadMore() {
    if (loading || finished) return;
    loading = true;
    try {
      const q = new URLSearchParams({ limit: "40" });
      if (seed) q.set("seed", String(seed));
      if (next) q.set("after", next);
      const res = await fetch("/api/photos?" + q.toString());
      if (res.status === 401) {
        goLogin();
        return;
      }
      if (!res.ok) {
        setNote("列表读不出来，稍后刷新页面。");
        return;
      }
      const data = await res.json();
      if (data.tz) tz = data.tz;
      if (data.seed && !seed) seed = data.seed;
      applyTheme();
      countEl.textContent = String(data.total) + " 张";
      for (const p of data.photos || []) {
        if (byId.has(p.id)) continue;
        items.push(p);
        byId.set(p.id, p);
      }
      next = data.next || null;
      if (!next) finished = true;

      const scanning = data.status && data.status.scanning;
      if (data.total === 0 && scanning) {
        setNote("正在整理照片。");
      } else if (data.total === 0) {
        setNote("目录里还没有可显示的照片。把 jpg、png、webp、gif 放进挂载的文件夹，子文件夹里的也会出现。");
      } else if (scanning) {
        setNote("正在整理新照片。");
      } else {
        setNote("");
      }
      relayout();
      if (wantId && showLightbox(wantId)) {
        const want = "#p/" + wantId;
        if (location.hash !== want) {
          history.pushState({ lb: wantId, y: savedY }, "", want);
        }
        wantId = "";
      } else if (wantId && finished) {
        wantId = "";
      } else if (!wantId) {
        syncFromURL();
      }
      if (!lb.hidden) {
        const cur = photoIdFromHash();
        if (cur) syncNav(cur);
      }
    } catch (err) {
      setNote("连不上相册服务，确认容器已经启动。");
    } finally {
      loading = false;
    }
  }

  themeBtn.addEventListener("click", cycleMode);

  lbMetaToggle.addEventListener("click", (e) => {
    e.stopPropagation();
    metaUserSet = true;
    setMetaOpen(!lb.classList.contains("meta-on"));
  });

  lbPrev.addEventListener("click", (e) => {
    e.stopPropagation();
    step(-1);
  });
  lbNext.addEventListener("click", (e) => {
    e.stopPropagation();
    step(1);
  });
  lbStage.addEventListener("click", (e) => {
    if (e.target === lbStage || e.target === lbImg) closePhoto();
  });

  let touchX = 0;
  lbStage.addEventListener("touchstart", (e) => {
    if (e.changedTouches[0]) touchX = e.changedTouches[0].clientX;
  }, { passive: true });
  lbStage.addEventListener("touchend", (e) => {
    if (!e.changedTouches[0]) return;
    const dx = e.changedTouches[0].clientX - touchX;
    if (dx > 50) step(-1);
    else if (dx < -50) step(1);
  }, { passive: true });

  window.addEventListener("popstate", syncFromURL);
  window.addEventListener("keydown", (e) => {
    if (lb.hidden) return;
    if (e.key === "Escape") closePhoto();
    if (e.key === "ArrowLeft") step(-1);
    if (e.key === "ArrowRight") step(1);
  });
  window.addEventListener("scroll", queuePaint, { passive: true });
  window.addEventListener("resize", relayout);
  new ResizeObserver(relayout).observe(grid);

  applyTheme();
  fetch("/api/health").then((r) => r.json()).then((h) => {
    if (h && h.tz) tz = h.tz;
    applyTheme();
  }).catch(() => {});
  setInterval(() => {
    if (currentMode() === "auto") applyTheme();
  }, 60000);
  loadMore();
})();
