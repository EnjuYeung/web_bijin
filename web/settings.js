(() => {
  const query = new URLSearchParams(location.search);
  if (query.has("album") || query.get("view") !== "settings") return;

  const localHost = document.getElementById("local-host");
  const localContainer = document.getElementById("local-container");
  const localCount = document.getElementById("local-count");
  const localScan = document.getElementById("local-scan");
  const localEvery = document.getElementById("local-every");
  const scanLine = document.getElementById("s3-scan");
  const list = document.getElementById("s3-list");
  const addBtn = document.getElementById("s3-add");
  const form = document.getElementById("s3-form");
  const formTitle = document.getElementById("s3-form-title");
  const secretHint = document.getElementById("s3-secret-hint");
  const msg = document.getElementById("s3-msg");
  const saveBtn = document.getElementById("s3-save");
  const testBtn = document.getElementById("s3-test");
  const cancelBtn = document.getElementById("s3-cancel");
  const note = document.getElementById("note");

  const SECRET_NEW = "只保存在服务器上，之后不会再显示";
  const SECRET_KEEP = "已保存；留空表示不修改";

  let storages = [];
  let editing = null;
  let busy = false;
  let pollTimer = 0;
  let pollUntil = 0;

  function goLogin() {
    location.href = "/login?next=" + encodeURIComponent(location.pathname + location.search);
  }

  function setNote(text) {
    note.hidden = !text;
    note.textContent = text || "";
  }

  function setMsg(text, kind) {
    msg.textContent = text || "";
    if (kind) msg.dataset.kind = kind;
    else delete msg.dataset.kind;
  }

  function validTime(value) {
    const d = new Date(value);
    return !isNaN(d) && d.getFullYear() > 2000 ? d : null;
  }

  function formatTime(value) {
    const d = validTime(value);
    if (!d) return "";
    return d.toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });
  }

  function formatEvery(seconds) {
    const n = Number(seconds) || 0;
    if (n <= 0) return "";
    if (n % 3600 === 0) return "每 " + n / 3600 + " 小时自动扫描一次。";
    if (n % 60 === 0) return "每 " + n / 60 + " 分钟自动扫描一次。";
    return "每 " + n + " 秒自动扫描一次。";
  }

  function countText(photos, broken) {
    let text = (photos || 0) + " 张照片";
    if (broken) text += "，" + broken + " 个文件无法显示（损坏或格式不支持）";
    return text;
  }

  function scanText(status) {
    if (!status) return { text: "等待扫描", kind: "wait" };
    const when = formatTime(status.at);
    if (status.err) return { text: (when ? when + " " : "") + "扫描失败：" + status.err + "。已有照片会保留。", kind: "err" };
    return { text: (when ? when + " " : "") + "扫描正常", kind: "ok" };
  }

  function hostOf(endpoint) {
    try {
      return new URL(endpoint).host;
    } catch (err) {
      return endpoint;
    }
  }

  function storageItem(s) {
    const item = document.createElement("article");
    item.className = "s3-item";

    const main = document.createElement("div");
    main.className = "s3-item-main";
    const name = document.createElement("h3");
    name.textContent = s.name;
    const where = document.createElement("p");
    where.className = "s3-where";
    where.textContent = s.bucket + (s.prefix ? "/" + s.prefix : "") + " · " + hostOf(s.endpoint);
    const count = document.createElement("p");
    count.className = "s3-count";
    count.textContent = countText(s.photos, s.broken);
    const state = document.createElement("p");
    const scan = scanText(s.status);
    state.className = "s3-state";
    state.dataset.kind = scan.kind;
    state.textContent = scan.text;
    main.append(name, where, count, state);

    const actions = document.createElement("div");
    actions.className = "s3-actions";
    const edit = document.createElement("button");
    edit.type = "button";
    edit.className = "btn";
    edit.textContent = "编辑";
    edit.setAttribute("aria-label", "编辑 " + s.name);
    edit.addEventListener("click", () => openForm(s));
    const del = document.createElement("button");
    del.type = "button";
    del.className = "btn btn-danger";
    del.textContent = "删除";
    del.setAttribute("aria-label", "删除 " + s.name);
    del.addEventListener("click", () => removeStorage(s));
    actions.append(edit, del);

    item.append(main, actions);
    return item;
  }

  function render(data) {
    const local = data.local || {};
    localHost.textContent = local.hostDir || "未提供（见 .env 中的 PHOTOS_DIR）";
    localContainer.textContent = (local.containerDir || "/photos") + "（只读）";
    localCount.textContent = countText(local.photos, local.broken);
    const ls = scanText(local.status);
    localScan.textContent = ls.text;
    localScan.dataset.kind = ls.kind;
    localEvery.textContent = formatEvery(data.scanEvery);

    storages = data.storages || [];
    list.replaceChildren();
    if (!storages.length) {
      const empty = document.createElement("p");
      empty.className = "s3-empty";
      empty.textContent = "还没有添加对象存储。";
      list.appendChild(empty);
    }
    for (const s of storages) list.appendChild(storageItem(s));

    const scan = data.scan || {};
    scanLine.textContent = scan.scanning || scan.queued ? "正在扫描，新照片会陆续出现；完成后这里的状态会自动更新。" : "";
  }

  async function api(method, url, body) {
    const opts = { method, headers: { Accept: "application/json" } };
    if (body !== undefined) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    const res = await fetch(url, opts);
    if (res.status === 401) {
      goLogin();
      throw new Error("unauthorized");
    }
    let data = {};
    try { data = await res.json(); } catch (err) {}
    if (!res.ok) {
      const e = new Error(data.error || "请求失败（" + res.status + "）");
      e.shown = true;
      throw e;
    }
    return data;
  }

  async function load() {
    try {
      const data = await api("GET", "/api/settings");
      render(data);
      setNote("");
      const scan = data.scan || {};
      schedulePoll(scan.scanning || scan.queued);
    } catch (err) {
      if (err.message !== "unauthorized") setNote("设置读不出来，确认容器已经启动后刷新页面。");
    }
  }

  // Refresh only while a scan is running, so a new storage's photos and
  // status appear without a permanent background poll.
  function schedulePoll(active) {
    clearTimeout(pollTimer);
    if (!active || Date.now() > pollUntil) return;
    pollTimer = setTimeout(load, 2000);
  }

  function startPolling() {
    pollUntil = Date.now() + 15 * 60 * 1000;
    schedulePoll(true);
  }

  function field(name) {
    return form.elements.namedItem(name);
  }

  function openForm(s) {
    editing = s || null;
    form.reset();
    formTitle.textContent = s ? "编辑「" + s.name + "」" : "添加对象存储";
    field("name").value = s ? s.name : "";
    field("endpoint").value = s ? s.endpoint : "";
    field("bucket").value = s ? s.bucket : "";
    field("prefix").value = s ? s.prefix : "";
    field("accessKey").value = s ? s.accessKey : "";
    field("secretKey").value = "";
    field("secretKey").placeholder = s ? "留空表示不修改" : "";
    secretHint.textContent = s ? SECRET_KEEP : SECRET_NEW;
    field("region").value = s ? s.region : "";
    field("addressing").value = s ? s.addressing : "auto";
    field("listV1").checked = !!(s && s.listV1);
    form.querySelector(".set-adv").open = !!(s && (s.region || s.addressing !== "auto" || s.listV1));
    setMsg("");
    form.hidden = false;
    addBtn.hidden = true;
    form.scrollIntoView({ block: "nearest" });
    field(s ? "name" : "endpoint").focus();
  }

  function closeForm() {
    editing = null;
    form.hidden = true;
    addBtn.hidden = false;
    setMsg("");
    addBtn.focus();
  }

  function formData() {
    return {
      id: editing ? editing.id : 0,
      name: field("name").value,
      endpoint: field("endpoint").value,
      bucket: field("bucket").value,
      prefix: field("prefix").value,
      accessKey: field("accessKey").value,
      secretKey: field("secretKey").value,
      region: field("region").value,
      addressing: field("addressing").value,
      listV1: field("listV1").checked
    };
  }

  function missing(data) {
    if (!data.endpoint.trim()) return "endpoint";
    if (!data.bucket.trim()) return "bucket";
    if (!data.accessKey.trim()) return "accessKey";
    if (!editing && !data.secretKey.trim()) return "secretKey";
    return "";
  }

  const LABEL = { endpoint: "Endpoint", bucket: "Bucket", accessKey: "Access Key", secretKey: "Secret Key" };

  function setBusy(on) {
    busy = on;
    for (const b of [saveBtn, testBtn, cancelBtn]) b.disabled = on;
    form.setAttribute("aria-busy", on ? "true" : "false");
  }

  async function testConnection() {
    if (busy) return;
    const data = formData();
    const miss = missing(data);
    if (miss) {
      setMsg("请先填写 " + LABEL[miss] + "。", "err");
      field(miss).focus();
      return;
    }
    setBusy(true);
    setMsg("正在连接……", "wait");
    try {
      const res = await api("POST", "/api/storages/test", data);
      if (!res.images) {
        setMsg("连接成功，但" + (res.more ? "前 " + res.checked + " 个文件中" : "") + "没有找到图片。请检查 Bucket 和前缀。", "warn");
      } else {
        setMsg("连接成功，" + (res.more ? "前 " + res.checked + " 个文件中" : "") + "找到 " + res.images + " 张图片。", "ok");
      }
    } catch (err) {
      if (err.message !== "unauthorized") setMsg(err.shown ? err.message : "连不上相册服务。", "err");
    } finally {
      setBusy(false);
    }
  }

  async function save(e) {
    e.preventDefault();
    if (busy) return;
    const data = formData();
    const miss = missing(data);
    if (miss) {
      setMsg("请填写 " + LABEL[miss] + "。", "err");
      field(miss).focus();
      return;
    }
    setBusy(true);
    setMsg("正在保存……", "wait");
    try {
      if (editing) await api("PUT", "/api/storages/" + editing.id, data);
      else await api("POST", "/api/storages", data);
      closeForm();
      await load();
      startPolling();
    } catch (err) {
      if (err.message !== "unauthorized") setMsg(err.shown ? err.message : "连不上相册服务。", "err");
    } finally {
      setBusy(false);
    }
  }

  async function removeStorage(s) {
    if (!confirm("删除「" + s.name + "」？\n\n它的照片会从图库中移除，存储里的原图不会被删除。")) return;
    try {
      await api("DELETE", "/api/storages/" + s.id);
      if (editing && editing.id === s.id) closeForm();
      await load();
      startPolling();
    } catch (err) {
      if (err.message !== "unauthorized") setNote(err.shown ? err.message : "连不上相册服务。");
    }
  }

  pollUntil = Date.now() + 15 * 60 * 1000;
  addBtn.addEventListener("click", () => openForm(null));
  cancelBtn.addEventListener("click", closeForm);
  testBtn.addEventListener("click", testConnection);
  form.addEventListener("submit", save);
  form.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !busy) closeForm();
  });

  load();
})();
