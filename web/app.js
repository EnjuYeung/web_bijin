(() => {
  const grid = document.getElementById("grid");
  const countEl = document.getElementById("count");
  const note = document.getElementById("note");
  const sent = document.getElementById("sent");
  const lb = document.getElementById("lightbox");
  const lbImg = document.getElementById("lb-img");
  const lbCap = document.getElementById("lb-cap");

  let next = null;
  let loading = false;
  let finished = false;
  let savedY = 0;

  function setNote(text) {
    if (!text) {
      note.hidden = true;
      note.textContent = "";
      return;
    }
    note.hidden = false;
    note.textContent = text;
  }

  function photoIdFromHash() {
    const m = /^#p\/(\d+)$/.exec(location.hash);
    return m ? m[1] : "";
  }

  function card(p) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "sheet";
    b.dataset.id = String(p.id);
    b.dataset.src = p.src;
    b.dataset.name = p.name;
    b.setAttribute("aria-label", p.name);
    const img = document.createElement("img");
    img.src = p.thumb;
    img.width = p.w;
    img.height = p.h;
    img.alt = "";
    img.decoding = "async";
    img.loading = "lazy";
    b.appendChild(img);
    b.addEventListener("click", () => openPhoto(p.id));
    return b;
  }

  function hideLightbox() {
    lb.hidden = true;
    lbImg.removeAttribute("src");
    lbCap.textContent = "";
    document.body.classList.remove("looking");
  }

  function showLightbox(id) {
    const btn = grid.querySelector('[data-id="' + id + '"]');
    if (!btn) return false;
    lbImg.src = btn.dataset.src;
    lbImg.alt = btn.dataset.name;
    lbCap.textContent = btn.dataset.name;
    lb.hidden = false;
    document.body.classList.add("looking");
    return true;
  }

  function openPhoto(id) {
    savedY = window.scrollY;
    if (!showLightbox(String(id))) return;
    const want = "#p/" + id;
    if (location.hash !== want) {
      history.pushState({ lb: String(id), y: savedY }, "", want);
    }
  }

  function closePhoto() {
    hideLightbox();
    if (photoIdFromHash()) {
      history.pushState({ lb: null, y: savedY }, "", location.pathname + location.search);
    }
    window.scrollTo(0, savedY);
  }

  function syncFromURL() {
    const id = photoIdFromHash();
    if (id) {
      if (lb.hidden) savedY = window.scrollY;
      if (!showLightbox(id)) hideLightbox();
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
      if (next) q.set("after", next);
      const res = await fetch("/api/photos?" + q.toString());
      if (!res.ok) {
        setNote("列表读不出来，稍后刷新页面。");
        return;
      }
      const data = await res.json();
      countEl.textContent = String(data.total) + " 张";
      for (const p of data.photos || []) {
        grid.appendChild(card(p));
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
      syncFromURL();
    } catch (err) {
      setNote("连不上相册服务，确认容器已经启动。");
    } finally {
      loading = false;
    }
  }

  lb.addEventListener("click", closePhoto);
  window.addEventListener("popstate", syncFromURL);
  window.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !lb.hidden) closePhoto();
  });

  const io = new IntersectionObserver((entries) => {
    if (entries.some((e) => e.isIntersecting)) loadMore();
  }, { rootMargin: "800px 0px" });
  io.observe(sent);

  loadMore();
})();
