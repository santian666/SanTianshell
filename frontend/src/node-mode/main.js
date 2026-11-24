const hostWindow = window.parent !== window && window.parent?.go ? window.parent : window;
const shellActionHost = new URLSearchParams(window.location.search).has("shellAction");
if (shellActionHost) document.documentElement.classList.add("shell-action-host");
let actionModalShown = false;
let actionCloseTimer = 0;
let lastModalVisible = null;
let nodeModalObserver = null;
function syncNodeModalState(force = false) {
  const count = [...document.querySelectorAll(".modal")].filter((item) => !item.classList.contains("hidden")).length;
  const visible = count > 0;
  const changed = visible !== lastModalVisible;
  if ((force || changed) && window.parent !== window) {
    window.parent.postMessage({ type: "node-modal-state", visible, count }, "*");
  }
  lastModalVisible = visible;
  if (!shellActionHost) return;
  if (visible) {
    actionModalShown = true;
    window.clearTimeout(actionCloseTimer);
  } else if (actionModalShown && changed) {
    window.clearTimeout(actionCloseTimer);
    actionCloseTimer = window.setTimeout(() => {
      const stillHidden = ![...document.querySelectorAll(".modal")].some((item) => !item.classList.contains("hidden"));
      if (stillHidden) window.parent.postMessage({ type: "node-action-close" }, "*");
    }, 180);
  }
}
function observeNodeModals() {
  syncNodeModalState(true);
  nodeModalObserver?.disconnect();
  nodeModalObserver = new MutationObserver(() => syncNodeModalState());
  document.querySelectorAll(".modal").forEach((modal) => {
    nodeModalObserver.observe(modal, { attributes: true, attributeFilter: ["class"] });
  });
}
window.syncNodeModalState = () => syncNodeModalState(true);
function notifyNodeDataChanged(alias, runtimeState = null) {
  if (window.parent === window) return;
  window.parent.postMessage({ type: "node-data-changed", alias: alias || "", runtimeState }, "*");
}
if (document.readyState === "loading") window.addEventListener("DOMContentLoaded", observeNodeModals, { once: true });
else observeNodeModals();
const backend = () => hostWindow.go?.nodemode?.App || window.go?.main?.App || {};
const shellBackend = () => hostWindow.go?.main?.App || window.go?.main?.App || {};
const applyShellTheme = () => {
  document.documentElement.dataset.shellTheme = "dark";
  document.documentElement.style.colorScheme = "dark";
};
window.applyShellTheme = applyShellTheme;
const syncShellTheme = () => {
  applyShellTheme();
};
syncShellTheme();
if (hostWindow !== window) new MutationObserver(syncShellTheme).observe(hostWindow.document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
const api = new Proxy({}, {
  get: (_, name) => (...args) => {
    const fn = backend()[name];
    if (typeof fn !== "function") throw new Error(`后端方法未就绪: ${String(name)}`);
    return fn(...args);
  },
});
const EventsOn = (...args) => (hostWindow.runtime || window.runtime)?.EventsOn?.(...args);
const EventsOff = (...args) => (hostWindow.runtime || window.runtime)?.EventsOff?.(...args);

const state = {
  overview: null,
  overviewReady: false,
  nodes: [],
  backups: [],
  records: [],
  deleted: [],
  health: [],
  connections: [],
  rules: [],
  editingRecord: null,
  trafficAlias: "",
  trafficLimit: 10,
  trafficMode: "realtime",
  trafficData: null,
  trafficRequest: 0,
  nodeSelected: new Set(),
  nodePage: 1,
  nodePageSize: 100,
  deletedSelected: new Set(),
  nodeOutboundResults: new Map(),
  nodeTargetAlias: "",
  deletedLatencyResults: new Map(),
  deletedPage: 1,
  deletedPageSize: 100,
  middleExcelFile: null,
  middleExcelExportFile: null,
  middleExcelExportPath: "",
  middleRunId: 0,
  proxyRunId: 0,
  proxyRawItems: [],
  proxyLocation: null,
  proxyFilter: "all",
  middleSettingsHistory: { current: {}, relayUrls: [], accounts: [], entryIds: [], middleDomains: [] },
  vpsIndex: new Map(),
  nodeIndex: new Map(),
  nodeSearchIndex: new Map(),
  exportingNodeDocs: new Set(),
  detailLogContent: "",
  detailLogFileName: "log.txt",
  detailEditorContext: null,
  startupStatusRefreshDone: false,
  awaitingHostKeyAliases: new Set(),
  nodesLoadedKey: "",
  nodesLoadId: 0,
  nodesLoadKey: "",
  nodesLoadPromise: null,
  nodeRowsHTML: "",
  nodeModeRefreshedAt: 0,
  nodeModeDirty: false,
  nodeModeRevision: 0,
  nodeModeRefreshPromise: null,
  deletedLoadId: 0,
  backupTargetAlias: "",
  backupLoadId: 0,
  backupActionId: 0,
  backupExpandedPath: "",
  backupExpandedContent: "",
  backupEditingPath: "",
};
const el = (id) => document.getElementById(id);
const empty = (v, fallback = "未设置") => v === undefined || v === null || v === "" ? fallback : v;
const setText = (id, value) => { const node = el(id); if (node) node.textContent = value; };
const on = (id, event, handler) => { const node = el(id); if (node) node.addEventListener(event, handler); };
const MIDDLE_SETTINGS_KEY = "vps-management-middle-settings";
const STATUS_CACHE_KEY = "vps-management-status-cache";
const OVERVIEW_CACHE_KEY = "vps-management-overview-cache";
const debounce = (fn, wait = 120) => {
  let timer = null;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), wait);
  };
};

window.addEventListener("error", (event) => {
  reportFrontendError(event.error || event.message, "前端脚本错误");
});

window.addEventListener("unhandledrejection", (event) => {
  event.preventDefault();
  reportFrontendError(event.reason, "前端异步操作失败");
});

function reportFrontendError(error, fallback) {
  console.error(error);
  const message = frontendErrorMessage(error, fallback);
  if (!state.overview) showVpsLoadError(message);
  showToast(message);
}

function frontendErrorMessage(error, fallback = "操作失败") {
  if (typeof error === "string" && error.trim()) return error.trim();
  if (error?.message) return String(error.message);
  if (error?.error) return String(error.error);
  try {
    const text = JSON.stringify(error);
    if (text && text !== "{}") return text;
  } catch {}
  return fallback;
}

async function waitForBackend(timeoutMs = 8000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (hostWindow.go?.nodemode?.App || window.go?.main?.App) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error("后端绑定未就绪，请重启软件");
}

function escapeHtml(value) {
  return String(value ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;").replaceAll("'", "&#039;");
}

let toastConfirmAction = null;

function showToast(message, onConfirm = null) {
  const modal = el("toast");
  if (!modal) return;
  toastConfirmAction = typeof onConfirm === "function" ? onConfirm : null;
  const text = String(message ?? "操作完成");
  const kind = notificationKind(text);
  modal.dataset.kind = kind;
  setText("toastTitle", kind === "error" ? "操作未完成" : kind === "progress" ? "正在处理" : kind === "success" ? "操作完成" : "操作提示");
  setText("toastMessage", text);
  modal.classList.remove("hidden");
  if (typeof requestAnimationFrame === "function") requestAnimationFrame(() => el("toastOkBtn")?.focus());
}

function notificationKind(message) {
  if (/失败|错误|异常|无法|不存在|未找到|不能为空|为空|缺少|没有|暂无|请先|请输入|无效|取消/.test(message)) return "error";
  if (/正在|开始|处理中|检测中|生成中|发布中|加载中|准备/.test(message)) return "progress";
  if (/成功|完成|正常|已保存|已删除|已恢复|已创建|已导出|已复制|已启用|已停用|已读取|已清空|已备份/.test(message)) return "success";
  return "info";
}

function closeToast(runConfirmAction = false) {
  el("toast")?.classList.add("hidden");
  const action = toastConfirmAction;
  toastConfirmAction = null;
  if (runConfirmAction) action?.();
}

function finishAddNode(alias, count, multiIp = false) {
  showToast(`新增成功，已创建 ${count || 0} 个节点`, () => {
    if (multiIp) closeMultiIpNodeModal();
    else closeNodeModal();
    if (shellActionHost) window.parent.postMessage({ type: "node-action-complete", alias }, "*");
  });
}

function vpsByAlias(alias) {
  return state.vpsIndex.get(alias) || {};
}

function isPlainIPv4(value) {
  return /^(\d{1,3}\.){3}\d{1,3}$/.test(String(value || "").trim());
}

function extractGroupFromTag(tag = "", createdAt = "") {
  const text = String(tag || "").trim();
  if (!text) return "";
  if (isPlainIPv4(text)) {
    return createdAt ? String(createdAt).trim() : text;
  }
  // 1. Check IP suffix: prefix-IP or prefix_IP, e.g. qline.wg3-202.160.82.90 -> qline.wg3, 8G-212.114.47.46 -> 8G
  const ipMatch = text.match(/^(.*?)[-_](\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})(:\d+)?$/);
  if (ipMatch && ipMatch[1] && ipMatch[1].trim()) {
    return ipMatch[1].trim();
  }
  // 2. Check number suffix: prefix--num, prefix-num, prefix_num, e.g. TC--1 -> TC, HK-01 -> HK, US_1 -> US
  const numMatch = text.match(/^(.*?)(?:--|[-_])\d+$/);
  if (numMatch && numMatch[1] && numMatch[1].trim()) {
    return numMatch[1].trim();
  }
  // 3. Plain IP with protocol, e.g. 1.2.3.4-vless
  const protoMatch = text.match(/^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})-[a-zA-Z0-9]+$/);
  if (protoMatch) {
    return createdAt ? String(createdAt).trim() : protoMatch[1];
  }
  return text;
}

function groupFromTag(tag) {
  return extractGroupFromTag(tag, "");
}

function groupFromNode(node) {
  if (!node) return "";
  const explicit = String(node.groupName || "").trim();
  if (explicit) return explicit;
  return extractGroupFromTag(node.tag, node.createdAt);
}

function outboundFull(node) {
  const parts = [node.outboundIp, node.outboundPort].filter(Boolean);
  return parts.join(":");
}

function nodeKey(node) {
  return `${node.vpsAlias}|||${node.fileName}`;
}

function rebuildVpsIndex() {
  state.vpsIndex = new Map();
  (state.overview?.vpsList || []).forEach((vps) => {
    const alias = vps.alias || vps.id;
    if (alias) state.vpsIndex.set(alias, vps);
  });
}

function rebuildNodeIndex() {
  state.nodeIndex = new Map();
  state.nodeSearchIndex = new Map();
  state.nodes.forEach((node) => {
    const key = nodeKey(node);
    state.nodeIndex.set(key, node);
    state.nodeSearchIndex.set(key, [
      node.outboundIp,
      node.outboundPort,
      node.serverName,
      node.tag,
      node.fileName,
      node.type,
      node.uuid,
    ].map((item) => String(item || "").toLowerCase()).join(" "));
  });
}

function nodeLink(node) {
  const vps = vpsByAlias(node.vpsAlias);
  const rawHost = String(node.host || "").trim();
  const host = (rawHost && rawHost !== "::" && rawHost !== "0.0.0.0" ? rawHost : "") || vps.domain || vps.address || "";
  const port = node.listenPort || "";
  const tag = encodeURIComponent(node.tag || node.fileName || "");
  if (!host || !port) return "";
  const protocol = (node.type || "vless").toLowerCase();
  if (protocol === "anytls") {
    const params = new URLSearchParams({ security: "tls", sni: node.serverName || "bing.com", insecure: "1", allowInsecure: "1" });
    return `anytls://${node.uuid || ""}@${host}:${port}?${params.toString()}#${tag}`;
  }
  if (protocol === "trojan") {
    const params = new URLSearchParams({ security: "tls", sni: node.serverName || host, insecure: "1", allowInsecure: "1" });
    return `trojan://${node.uuid || ""}@${host}:${port}?${params.toString()}#${tag}`;
  }
  if (protocol === "vmess") {
    const payload = {
      v: "2",
      ps: node.tag || node.fileName || "",
      add: host,
      port: String(port),
      id: node.uuid || "",
      aid: "0",
      net: "tcp",
      type: "none",
      host: "",
      path: "",
      tls: "",
    };
    return `vmess://${btoa(unescape(encodeURIComponent(JSON.stringify(payload))))}`;
  }
  if (protocol === "vless" && node.reality) {
    const params = new URLSearchParams({
      encryption: "none",
      security: "reality",
      sni: node.serverName || "stock.adobe.com",
      fp: node.fingerprint || "firefox",
      pbk: node.publicKey || "",
      sid: node.shortId || "",
      type: "tcp",
    });
    if (node.flow) params.set("flow", node.flow);
    return `vless://${node.uuid || ""}@${host}:${port}?${params.toString()}#${tag}`;
  }
  if (protocol === "vless") {
    const params = new URLSearchParams({ encryption: "none", security: "tls", sni: node.serverName || host, type: "tcp", insecure: "1", allowInsecure: "1" });
    return `vless://${node.uuid || ""}@${host}:${port}?${params.toString()}#${tag}`;
  }
  if (protocol === "socks") return [host, port, node.username || "", node.password || ""].join(":");
  return `vless://${node.uuid || ""}@${host}:${port}#${tag}`;
}

function nodeProtocolLabel(node) {
  const protocol = String(node.type || "unknown").toLowerCase();
  if (protocol === "vless" && node.reality) return "VLESS Reality";
  if (protocol === "vless") return "VLESS";
  if (protocol === "vmess") return "VMess";
  if (protocol === "trojan") return "Trojan";
  if (protocol === "anytls") return "AnyTLS";
  if (protocol === "socks") return "SOCKS5";
  return protocol;
}

function loadStatusCache() {
  try {
    return JSON.parse(localStorage.getItem(STATUS_CACHE_KEY) || "{}");
  } catch {
    localStorage.removeItem(STATUS_CACHE_KEY);
    return {};
  }
}

function saveStatusCache(cache) {
  localStorage.setItem(STATUS_CACHE_KEY, JSON.stringify(cache || {}));
}

function clearStatusCacheFor(alias) {
  const cache = loadStatusCache();
  delete cache[alias];
  saveStatusCache(cache);
}

function applyStatusCache(alias, status, nodeText) {
  const cache = loadStatusCache();
  cache[alias] = {
    singboxStatus: status,
    nodeText,
    checkedAt: new Date().toISOString(),
  };
  saveStatusCache(cache);
}

function loadOverviewCache() {
  try {
    const cached = JSON.parse(localStorage.getItem(OVERVIEW_CACHE_KEY) || "null");
    if (cached && Array.isArray(cached.vpsList) && cached.stats) {
      saveOverviewCache(cached);
      return loadOverviewCacheValue(cached);
    }
  } catch {
    localStorage.removeItem(OVERVIEW_CACHE_KEY);
  }
  return null;
}

function loadOverviewCacheValue(overview) {
  return {
    ...overview,
    vpsList: overview.vpsList.map((item) => {
      const clean = { ...item };
      ["password", "key_path", "proxy_password"].forEach((key) => delete clean[key]);
      return clean;
    }),
  };
}

function saveOverviewCache(overview) {
  if (!overview || !Array.isArray(overview.vpsList)) return;
  if (!overview.vpsList.length) {
    localStorage.removeItem(OVERVIEW_CACHE_KEY);
    localStorage.removeItem(STATUS_CACHE_KEY);
    return;
  }
  localStorage.setItem(OVERVIEW_CACHE_KEY, JSON.stringify(loadOverviewCacheValue(overview)));
}

function renderCachedOverview(overview) {
  state.overview = overview;
  state.overviewReady = false;
  rebuildVpsIndex();
  setText("versionText", `版本 ${overview.appInfo?.version || ""}`);
  setText("dataPath", `数据目录：${overview.appInfo?.dataDir || ""}`);
  setText("vpsCount", overview.stats?.vpsCount || 0);
  setText("nodeCount", overview.stats?.nodeCount || 0);
  setText("backupCount", overview.stats?.backupCount || 0);
  renderVPS();
  fillAliasFilters();
}

function scheduleStartupStatusRefresh() {
  if (hostWindow !== window || shellActionHost || state.startupStatusRefreshDone) return;
  state.startupStatusRefreshDone = true;
  setText("statusText", "已加载本地数据，稍后自动刷新状态");
  setTimeout(() => {
    refreshStartupStatus().catch((err) => {
      console.error(err);
      showToast(err.message || "启动自动刷新失败");
    });
  }, 1500);
}

async function refreshStartupStatus() {
  await refreshVpsCountriesInBackground();
  let trustedAliases;
  try {
    trustedAliases = await syncHostKeyTrust();
  } catch (error) {
    console.warn("读取 SSH 指纹信任状态失败，已跳过启动状态刷新", error);
    const list = state.overview?.vpsList || [];
    state.awaitingHostKeyAliases = new Set(list.map((item) => String(item.alias || item.id || "").trim()).filter(Boolean));
    renderVPS();
    setText("statusText", "未读取到 SSH 指纹信任状态，已跳过启动自动刷新");
    return;
  }
  const list = state.overview?.vpsList || [];
  const trustedList = list.filter((item) => trustedAliases.has(String(item.alias || item.id || "").trim()));
  if (trustedList.length) {
    await refreshAllStatus({ list: trustedList, silent: true, concurrency: 1, yieldUI: true, yieldDelayMs: 500 });
  }
  const pendingCount = state.awaitingHostKeyAliases.size;
  setText("statusText", pendingCount ? `启动自动刷新完成，${pendingCount} 台待首次连接确认指纹` : "启动自动刷新完成");
}

async function syncHostKeyTrust() {
  const list = state.overview?.vpsList || [];
  const getSettings = shellBackend().GetSettings;
  if (typeof getSettings !== "function") throw new Error("主机设置接口未就绪");
  const settings = await getSettings();
  const aliases = new Map(list.map((item) => {
    const alias = String(item.alias || item.id || "").trim();
    return [alias.toLowerCase(), alias];
  }));
  const trustedAliases = new Set();
  (settings?.hosts || []).forEach((host) => {
    if (host.deleted || String(host.protocol || "ssh").toLowerCase() !== "ssh" || !String(host.hostKey || "").trim()) return;
    const alias = aliases.get(String(host.vpsAlias || "").trim().toLowerCase());
    if (alias) trustedAliases.add(alias);
  });
  state.awaitingHostKeyAliases = new Set(list
    .map((item) => String(item.alias || item.id || "").trim())
    .filter((alias) => alias && !trustedAliases.has(alias)));
  renderVPS();
  return trustedAliases;
}

async function refreshVpsCountriesInBackground() {
  try {
    const list = await api.RefreshVPSCountries();
    if (!Array.isArray(list) || !state.overview?.vpsList) return;
    const countries = new Map(list.map((item) => [item.alias || item.id, item.countryCode || "--"]));
    state.overview.vpsList.forEach((item) => {
      const alias = item.alias || item.id;
      if (countries.has(alias)) item.countryCode = countries.get(alias);
    });
    saveOverviewCache(state.overview);
    renderVPS();
  } catch (err) {
    console.error(err);
  }
}

function singboxStatusClass(status) {
  if (status === "running") return "success";
  if (status && status !== "unknown") return "warning";
  return "";
}

function nodeStatusClass(text) {
  if (!text || text === "未检测") return "";
  if (text.includes("已同步") || /^\d+ 个节点$/.test(text)) return "success";
  if (text.includes("失败") || text.includes("异常")) return "warning";
  return "";
}

function runtimeNodeText(result = {}) {
  const count = Number(result.nodeCount || 0);
  return result.synced ? `已同步 ${count} 个节点` : count > 0 ? `${count} 个节点` : "无节点";
}

function applyRuntimeOverview(alias, result) {
  if (!alias || !result) return;
  const item = state.overview?.vpsList?.find((vps) => (vps.alias || vps.id) === alias);
  if (!item) return;
  item.nodeCount = Number(result.nodeCount || 0);
  item.startPort = Number(result.startPort || 0);
  item.singboxStatus = result.status || "unknown";
}

function showDetail(title, value) {
  state.detailLogContent = "";
  state.detailLogFileName = "log.txt";
  state.detailEditorContext = null;
  el("detailContent").removeAttribute("contenteditable");
  el("detailActions")?.classList.add("hidden");
  el("copyDetailJsonBtn")?.classList.add("hidden");
  el("editDetailJsonBtn")?.classList.add("hidden");
  el("saveDetailJsonBtn")?.classList.add("hidden");
  el("exportDetailLogBtn")?.classList.remove("hidden");
  setText("detailTitle", title);
  el("detailContent").textContent = typeof value === "string" ? value : JSON.stringify(value, null, 2);
  el("detailModal").classList.remove("hidden");
}

function showLogDetail(title, content, fileName = "log.txt", exportable = true) {
  state.detailLogContent = String(content || "");
  state.detailLogFileName = fileName;
  state.detailEditorContext = null;
  el("detailContent").removeAttribute("contenteditable");
  setText("detailTitle", title);
  el("detailContent").textContent = state.detailLogContent || "没有日志内容";
  el("copyDetailJsonBtn")?.classList.add("hidden");
  el("editDetailJsonBtn")?.classList.add("hidden");
  el("saveDetailJsonBtn")?.classList.add("hidden");
  el("exportDetailLogBtn")?.classList.remove("hidden");
  el("detailActions")?.classList.toggle("hidden", !exportable);
  el("detailModal").classList.remove("hidden");
}

function setDetailProgress(percent, text = "") {
  const box = el("detailProgress");
  if (!box) return;
  const value = Math.max(0, Math.min(100, Number(percent) || 0));
  box.classList.remove("hidden");
  if (el("detailProgressBar")) el("detailProgressBar").style.width = `${value}%`;
  setText("detailProgressPercent", `${Math.round(value)}%`);
  if (text) setText("detailProgressText", text);
}

function resetDetailProgress() {
  el("detailProgress")?.classList.add("hidden");
  if (el("detailProgressBar")) el("detailProgressBar").style.width = "0%";
  setText("detailProgressPercent", "0%");
  setText("detailProgressText", "准备执行");
  setText("detailProgressResult", "正在保存并部署...");
}

function showEditableJson(title, content, context) {
  state.detailLogContent = "";
  state.detailLogFileName = "log.txt";
  state.detailEditorContext = { ...context, editing: false };
  resetDetailProgress();
  setText("detailTitle", title);
  el("detailContent").textContent = content;
  el("detailContent").removeAttribute("contenteditable");
  el("detailActions")?.classList.remove("hidden");
  el("copyDetailJsonBtn")?.classList.remove("hidden");
  el("editDetailJsonBtn")?.classList.remove("hidden");
  el("saveDetailJsonBtn")?.classList.add("hidden");
  el("exportDetailLogBtn")?.classList.add("hidden");
  el("detailModal").classList.remove("hidden");
}

function closeDetail() {
  const saveBtn = el("saveDetailJsonBtn");
  if (saveBtn?.disabled && saveBtn.textContent.includes("中...")) return;
  state.detailEditorContext = null;
  resetDetailProgress();
  el("detailContent").removeAttribute("contenteditable");
  el("detailModal").classList.add("hidden");
}

async function exportDetailLog() {
  if (!state.detailLogContent.trim()) return showToast("没有可导出的日志");
  try {
    const path = await api.SaveTextFile(state.detailLogFileName || "log.txt", state.detailLogContent);
    if (path) showToast(`日志已导出：${path}`);
  } catch (error) {
    const message = error?.message || String(error || "导出失败");
    showToast(message.includes("取消") ? "已取消导出" : `导出失败：${message}`);
  }
}

async function copyDetailJson() {
  await copyText(el("detailContent").textContent || "", "JSON 已复制");
}

function enableDetailJsonEdit() {
  if (!state.detailEditorContext) return showToast("没有可编辑的 JSON");
  state.detailEditorContext.editing = true;
  el("detailContent").setAttribute("contenteditable", "true");
  el("detailContent").focus();
  el("editDetailJsonBtn")?.classList.add("hidden");
  el("saveDetailJsonBtn")?.classList.remove("hidden");
  showToast("已进入编辑模式");
}

async function saveDetailJson() {
  const ctx = state.detailEditorContext;
  if (!ctx?.alias) return showToast("没有可保存的 JSON");
  if (!ctx.editing) return showToast("请先点击编辑");
  const isAll = ctx.type === "all";
  const title = isAll ? "保存 all.json" : "保存节点 JSON";
  const target = isAll ? ctx.alias : `${ctx.alias} / ${ctx.filename}`;
  if (!await appConfirm(`确认保存 ${target} 的 JSON 并部署生效吗？\n如果配置检查失败，软件会自动回滚。`, title)) return;
  const saveBtn = el("saveDetailJsonBtn");
  const editBtn = el("editDetailJsonBtn");
  const closeBtn = el("closeDetailModalBtn");
  if (saveBtn?.disabled) return;
  try {
    if (saveBtn) {
      saveBtn.disabled = true;
      saveBtn.textContent = "保存部署中...";
    }
    if (editBtn) editBtn.disabled = true;
    if (closeBtn) closeBtn.disabled = true;
    setDetailProgress(15, "正在校验并解析 JSON 内容...");
    setText("detailProgressResult", `正在保存 ${target} 并部署生效...`);

    const content = el("detailContent").textContent || "";
    setDetailProgress(45, "正在写入本地配置并下发到 VPS (SSH)...");
    const result = isAll
      ? await api.SaveAllJSONAndDeploy(ctx.alias, content)
      : await api.SaveNodeJSONAndDeploy(ctx.alias, ctx.filename, content);
    if (!result.success) {
      setDetailProgress(100, result.message || "JSON 保存失败");
      setText("detailProgressResult", "保存失败，已自动回滚");
      showDetail("JSON 保存失败", result);
      return;
    }
    setDetailProgress(85, "正在验证远程 sing-box 服务状态...");
    await refreshStatusAfterVpsChange(ctx.alias);
    await loadNodes(true);
    setDetailProgress(100, "JSON 已保存并成功生效！");
    setText("detailProgressResult", result.message || (isAll ? "JSON 已保存并生效" : "节点 JSON 已保存并生效"));
    showToast(result.message || (isAll ? "JSON 已保存并生效" : "节点 JSON 已保存并生效"));
    await new Promise((r) => setTimeout(r, 600));
    closeDetail();
  } catch (err) {
    setDetailProgress(100, err?.message || "保存异常");
    showToast(`保存异常: ${err?.message || err}`);
  } finally {
    if (saveBtn) {
      saveBtn.disabled = false;
      saveBtn.textContent = "保存并部署";
    }
    if (editBtn) editBtn.disabled = false;
    if (closeBtn) closeBtn.disabled = false;
  }
}

let confirmResolver = null;
function appConfirm(message, title = "确认操作") {
  return new Promise((resolve) => {
    confirmResolver = resolve;
    setText("confirmTitle", title);
    setText("confirmMessage", message);
    const input = el("confirmInput");
    input.classList.add("hidden");
    input.value = "";
    el("confirmOkBtn").classList.remove("danger");
    el("confirmModal").classList.remove("hidden");
  });
}

function appPrompt(message, title = "确认操作") {
  return new Promise((resolve) => {
    confirmResolver = resolve;
    setText("confirmTitle", title);
    setText("confirmMessage", message);
    const input = el("confirmInput");
    input.classList.remove("hidden");
    input.value = "";
    el("confirmOkBtn").classList.add("danger");
    el("confirmModal").classList.remove("hidden");
    setTimeout(() => input.focus(), 30);
  });
}

function closeConfirmModal(value = false) {
  el("confirmModal").classList.add("hidden");
  if (confirmResolver) {
    const resolver = confirmResolver;
    confirmResolver = null;
    resolver(value);
  }
}

async function copyText(text, message = "已复制") {
  if (!text) return showToast("没有可复制的内容");
  if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(text);
  else {
    const input = document.createElement("textarea");
    input.value = text;
    document.body.appendChild(input);
    input.select();
    document.execCommand("copy");
    input.remove();
  }
  showToast(message);
}

function openMiddleLog(text = "准备执行...") {
  if (el("middleLogOutput")) el("middleLogOutput").textContent = text;
  el("middleLogModal")?.classList.remove("hidden");
}

function appendMiddleLog(text) {
  const node = el("middleLogOutput");
  if (!node) return;
  const lines = `${node.textContent || ""}\n${text}`.trim().split("\n");
  node.textContent = lines.slice(-500).join("\n");
  node.scrollTop = node.scrollHeight;
}

function closeMiddleLog() {
  el("middleLogModal")?.classList.add("hidden");
}

function readFileAsBase64(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const text = String(reader.result || "");
      resolve(text.includes(",") ? text.split(",").pop() : text);
    };
    reader.onerror = () => reject(reader.error || new Error("文件读取失败"));
    reader.readAsDataURL(file);
  });
}

async function convertMiddleAcceleration() {
  const input = el("middleInputText")?.value || "";
  if (!input.trim() && !state.middleExcelFile) return showToast("请输入需要转换的内容或上传 Excel");
  const req = {
    relayUrl: el("middleRelayUrl")?.value.trim() || "",
    username: el("middleRelayUsername")?.value.trim() || "",
    password: el("middleRelayPassword")?.value || "",
    entryId: el("middleEntryId")?.value.trim() || "",
    middleDomain: el("middleDomain")?.value.trim() || "",
    inputText: input,
    maxRetry: 5,
    concurrency: 10,
    reuseExisting: el("middleReuseExisting") ? Boolean(el("middleReuseExisting").checked) : true,
  };
  if (!req.relayUrl) return showToast("请输入中转网址");
  if (!req.username) return showToast("请输入账号");
  if (!req.password) return showToast("请输入密码");
  if (!req.entryId) return showToast("请输入入口ID");
  if (!req.middleDomain) return showToast("请输入中转域名");
  openMiddleLog("开始生成中转规则...\n后端会登录中转后台、批量导入规则、查询端口和状态。");
  const runId = ++state.middleRunId;
  const button = el("convertMiddleBtn");
  const oldButtonText = button?.textContent || "生成";
  if (button) {
    button.disabled = true;
    button.textContent = "生成中...";
  }
  setText("middleSummaryText", "正在生成...");
  EventsOff("middle:log");
  EventsOn("middle:log", (line) => appendMiddleLog(line));
  try {
    let result;
    if (state.middleExcelFile) {
      if (typeof backend().RunMiddleAccelerationExcelToFile === "function") {
        result = await api.RunMiddleAccelerationExcelToFile({ ...req, fileName: state.middleExcelFile.name, fileBase64: state.middleExcelFile.base64 });
        state.middleExcelExportFile = null;
        state.middleExcelExportPath = result.filePath || "";
        if (state.middleExcelExportPath) showToast(`已导出：${state.middleExcelExportPath}`);
      } else {
        result = await api.RunMiddleAccelerationExcel({ ...req, fileName: state.middleExcelFile.name, fileBase64: state.middleExcelFile.base64 });
        state.middleExcelExportFile = result.file?.base64 ? result.file : null;
        state.middleExcelExportPath = "";
        if (state.middleExcelExportFile) await saveExportFile(state.middleExcelExportFile);
      }
    } else {
      state.middleExcelExportFile = null;
      state.middleExcelExportPath = "";
      result = await api.RunMiddleAcceleration(req);
    }
    if (runId !== state.middleRunId) return;
    el("middleOutputText").value = result.output || "";
    const logs = Array.isArray(result.logs) ? result.logs.join("\n") : "";
    el("middleLogOutput").textContent = logs || result.message || "无日志";
    setText("middleSummaryText", result.message || "已完成");
    showToast(result.success ? "中转生成完成" : "中转生成失败，请查看日志");
  } catch (error) {
    if (runId !== state.middleRunId) return;
    appendMiddleLog(`执行失败：${error?.message || error}`);
    setText("middleSummaryText", "执行失败");
    showToast("中转生成失败");
  } finally {
    if (runId === state.middleRunId) {
      EventsOff("middle:log");
      if (button) {
        button.disabled = false;
        button.textContent = oldButtonText;
      }
    }
  }
}

async function loadMiddleInputFile(event) {
  const file = event.target.files?.[0];
  if (!file) return;
  state.middleExcelFile = null;
  if (/\.xlsx$/i.test(file.name)) {
    const base64 = await readFileAsBase64(file);
    state.middleExcelFile = { name: file.name, base64 };
    state.middleExcelExportFile = null;
    state.middleExcelExportPath = "";
    el("middleInputText").value = `已上传 Excel：${file.name}\n点击“生成”后会自动读取“网络链接”列，生成中转链接并导出新 Excel。`;
    event.target.value = "";
    showToast(`已读取 Excel：${file.name}`);
    return;
  }
  const text = await file.text();
  state.middleExcelExportFile = null;
  state.middleExcelExportPath = "";
  el("middleInputText").value = text;
  event.target.value = "";
  showToast(`已读取：${file.name}`);
}

function clearMiddleAcceleration() {
  state.middleRunId += 1;
  EventsOff("middle:log");
  ["middleInputText", "middleOutputText"].forEach((id) => {
    if (el(id)) el(id).value = "";
  });
  state.middleExcelFile = null;
  state.middleExcelExportFile = null;
  state.middleExcelExportPath = "";
  const button = el("convertMiddleBtn");
  if (button) {
    button.disabled = false;
    button.textContent = "生成";
  }
  setText("middleSummaryText", "未生成");
}

async function exportMiddleAcceleration() {
  if (state.middleExcelExportPath) {
    showToast(`已导出：${state.middleExcelExportPath}`);
    return;
  }
  if (state.middleExcelExportFile?.base64) {
    await saveExportFile(state.middleExcelExportFile);
    return;
  }
  if (state.middleExcelFile) return showToast("请先点击生成，生成完成后再导出 Excel");
  const content = el("middleOutputText")?.value || "";
  if (!content.trim()) return showToast("没有可导出的结果");
  if (typeof api.SaveTextFile === "function") {
    try {
      const fileName = `middle_acceleration_${new Date().toISOString().slice(0, 10)}.txt`;
      const path = await api.SaveTextFile(fileName, content);
      if (path) showToast(`已导出：${path}`);
      return;
    } catch (error) {
      const message = error?.message || String(error || "导出失败");
      showToast(message.includes("取消") ? "已取消导出" : `导出失败：${message}`);
      return;
    }
  }
  const blob = new Blob([content], { type: "text/plain;charset=utf-8" });
  const link = document.createElement("a");
  link.href = URL.createObjectURL(blob);
  link.download = `middle_acceleration_${new Date().toISOString().slice(0, 10)}.txt`;
  link.click();
  URL.revokeObjectURL(link.href);
  showToast("结果已导出");
}

function middleSettingsForm() {
  return {
    relayUrl: el("middleRelayUrl")?.value.trim() || "",
    username: el("middleRelayUsername")?.value.trim() || "",
    password: el("middleRelayPassword")?.value || "",
    entryId: el("middleEntryId")?.value.trim() || "",
    middleDomain: el("middleDomain")?.value.trim() || "",
  };
}

function normalizeMiddleSettingsHistory(history = {}) {
  return {
    current: history.current || {},
    relayUrls: Array.isArray(history.relayUrls) ? history.relayUrls : [],
    accounts: Array.isArray(history.accounts) ? history.accounts.filter((item) => item?.username) : [],
    entryIds: Array.isArray(history.entryIds) ? history.entryIds : [],
    middleDomains: Array.isArray(history.middleDomains) ? history.middleDomains : [],
  };
}

function applyMiddleSettings(settings = {}) {
  if (el("middleRelayUrl")) el("middleRelayUrl").value = settings.relayUrl || "";
  if (el("middleRelayUsername")) el("middleRelayUsername").value = settings.username || "";
  if (el("middleRelayPassword")) el("middleRelayPassword").value = settings.password || "";
  if (el("middleEntryId")) el("middleEntryId").value = settings.entryId || "";
  if (el("middleDomain")) el("middleDomain").value = settings.middleDomain || "";
  const accountCombo = document.querySelector('[data-middle-history="accounts"]');
  if (accountCombo) accountCombo.dataset.accountUsername = settings.username || "";
}

function legacyMiddleSettings() {
  try {
    return JSON.parse(localStorage.getItem(MIDDLE_SETTINGS_KEY) || "{}");
  } catch {
    localStorage.removeItem(MIDDLE_SETTINGS_KEY);
    return {};
  }
}

function middleSettingsHasValue(settings = {}) {
  return [settings.relayUrl, settings.username, settings.password, settings.entryId, settings.middleDomain].some((value) => String(value || "").trim());
}

function middleHistoryValues(kind) {
  const history = state.middleSettingsHistory;
  if (kind === "accounts") return history.accounts.map((item) => item.username);
  return Array.isArray(history[kind]) ? history[kind] : [];
}

function renderMiddleHistoryCombo(combo, showAll = false) {
  const input = combo.querySelector("input");
  const menu = combo.querySelector(".filter-combo-menu");
  const query = showAll ? "" : input.value.trim().toLowerCase();
  const values = middleHistoryValues(combo.dataset.middleHistory)
    .filter((value) => !query || String(value).toLowerCase().includes(query))
    .slice(0, 20);
  menu.innerHTML = values.length
    ? values.map((value) => `<button type="button" class="filter-combo-option" data-value="${escapeHtml(value)}"><span>${escapeHtml(value)}</span></button>`).join("")
    : `<div class="filter-combo-empty">暂无历史记录</div>`;
  combo.classList.add("open");
  combo.dataset.showAll = showAll ? "true" : "false";
  menu.classList.remove("hidden");
}

function fillMiddleAccountPassword(username) {
  const account = state.middleSettingsHistory.accounts.find((item) => item.username === username);
  if (!account) return false;
  if (el("middleRelayPassword")) el("middleRelayPassword").value = account.password || "";
  const combo = document.querySelector('[data-middle-history="accounts"]');
  if (combo) combo.dataset.accountUsername = username;
  return true;
}

function setupMiddleHistoryCombos() {
  document.querySelectorAll("[data-middle-history]").forEach((combo) => {
    const input = combo.querySelector("input");
    const clear = combo.querySelector(".filter-combo-clear");
    const toggle = combo.querySelector(".filter-combo-toggle");
    const menu = combo.querySelector(".filter-combo-menu");
    const kind = combo.dataset.middleHistory;
    input.addEventListener("focus", () => renderMiddleHistoryCombo(combo, true));
    input.addEventListener("click", () => renderMiddleHistoryCombo(combo, true));
    input.addEventListener("input", () => {
      if (kind === "accounts" && input.value !== combo.dataset.accountUsername) {
        delete combo.dataset.accountUsername;
        if (el("middleRelayPassword")) el("middleRelayPassword").value = "";
      }
      renderMiddleHistoryCombo(combo);
    });
    input.addEventListener("change", () => {
      if (kind === "accounts") fillMiddleAccountPassword(input.value.trim());
    });
    input.addEventListener("keydown", (event) => {
      if (event.key === "Escape") closeFilterCombo(combo);
      if (event.key === "ArrowDown") {
        const option = menu.querySelector(".filter-combo-option");
        if (option) {
          event.preventDefault();
          option.focus();
        }
      }
    });
    toggle.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      if (combo.classList.contains("open") && combo.dataset.showAll === "true") return closeFilterCombo(combo);
      input.focus();
      renderMiddleHistoryCombo(combo, true);
    });
    clear.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      input.value = "";
      if (kind === "accounts") {
        delete combo.dataset.accountUsername;
        if (el("middleRelayPassword")) el("middleRelayPassword").value = "";
      }
      input.focus();
      renderMiddleHistoryCombo(combo, true);
    });
    menu.addEventListener("click", (event) => {
      const option = event.target.closest(".filter-combo-option");
      if (!option) return;
      input.value = option.dataset.value || "";
      if (kind === "accounts") fillMiddleAccountPassword(input.value);
      closeFilterCombo(combo);
    });
  });
}

function refreshMiddleHistoryCombos() {
  document.querySelectorAll(".middle-history-combo.open").forEach((combo) => renderMiddleHistoryCombo(combo, combo.dataset.showAll === "true"));
}

async function saveMiddleSettings() {
  try {
    state.middleSettingsHistory = normalizeMiddleSettingsHistory(await api.SaveMiddleSettings(middleSettingsForm()));
    localStorage.removeItem(MIDDLE_SETTINGS_KEY);
    refreshMiddleHistoryCombos();
    showToast("中转设置和历史记录已保存");
  } catch (error) {
    showToast(frontendErrorMessage(error, "中转设置保存失败"));
  }
}

async function loadMiddleSettings() {
  const legacy = legacyMiddleSettings();
  try {
    let history = normalizeMiddleSettingsHistory(await api.GetMiddleSettingsHistory());
    if (!middleSettingsHasValue(history.current) && middleSettingsHasValue(legacy)) {
      history = normalizeMiddleSettingsHistory(await api.SaveMiddleSettings(legacy));
    }
    state.middleSettingsHistory = history;
    applyMiddleSettings(history.current);
    localStorage.removeItem(MIDDLE_SETTINGS_KEY);
  } catch (error) {
    console.error("加载中转设置历史失败", error);
    applyMiddleSettings(legacy);
  }
}

async function loadOverview() {
  await loadMiddleSettings();
  const cached = loadOverviewCache();
  if (cached) renderCachedOverview(cached);
  state.overview = await api.GetOverview();
  state.overviewReady = true;
  saveOverviewCache(state.overview);
  rebuildVpsIndex();
  setText("versionText", `版本 ${state.overview.appInfo.version}`);
  setText("dataPath", `数据目录：${state.overview.appInfo.dataDir}`);
  setText("vpsCount", state.overview.stats.vpsCount);
  setText("nodeCount", state.overview.stats.nodeCount);
  setText("backupCount", state.overview.stats.backupCount);
  renderVPS();
  fillAliasFilters();
  scheduleStartupStatusRefresh();
}

function showVpsLoadError(message) {
  const table = el("vpsRows");
  if (table) table.innerHTML = `<tr><td colspan="11" class="status-fail center">${escapeHtml(message || "VPS 数据读取失败")}</td></tr>`;
}

function renderVPS() {
  const statusCache = loadStatusCache();
  const vpsList = state.overview?.vpsList || [];
  const countryCounts = new Map();
  vpsList.forEach((item) => {
    const code = String(item.countryCode || "--").trim().toUpperCase() || "--";
    countryCounts.set(code, (countryCounts.get(code) || 0) + 1);
  });
  const countryFilter = el("vpsCountryFilter");
  const selectedCountry = String(countryFilter?.value || "").toUpperCase();
  if (countryFilter) {
    const countries = Array.from(countryCounts.keys())
      .filter((code) => code !== "--")
      .sort((a, b) => a.localeCompare(b));
    if (countryCounts.has("--")) countries.push("--");
    countryFilter.innerHTML = `<option value="">全部国家（${vpsList.length}）</option>${countries
      .map((code) => `<option value="${escapeHtml(code)}">${code === "--" ? "未识别" : escapeHtml(code)}（${countryCounts.get(code)}）</option>`)
      .join("")}`;
    countryFilter.value = countryCounts.has(selectedCountry) ? selectedCountry : "";
  }
  const activeCountry = countryFilter?.value || "";
  const visibleVPS = activeCountry
    ? vpsList.filter((item) => (String(item.countryCode || "--").trim().toUpperCase() || "--") === activeCountry)
    : vpsList;
  const rows = visibleVPS.map((item) => {
    const alias = item.alias || item.id;
    const cached = statusCache[alias] || {};
    const status = cached.singboxStatus || item.singboxStatus || "unknown";
    const awaitingHostKey = state.awaitingHostKeyAliases.has(String(alias || "").trim());
    const statusText = awaitingHostKey ? "待首次连接确认指纹" : statusTextMap(status);
    const statusClass = awaitingHostKey ? "warning" : singboxStatusClass(status);
    const cachedNodeText = /^正常\s+\d+\/\d+$/.test(cached.nodeText || "") ? "" : cached.nodeText;
    const nodeText = cachedNodeText || (item.nodeCount ? `${item.nodeCount} 个节点` : "无节点");
    const nodeClass = nodeStatusClass(nodeText);
    return `<tr>
      <td><button class="link-btn" data-vps-action="nodes" data-alias="${escapeHtml(alias)}">${escapeHtml(alias)}</button></td>
      <td><span class="badge country">${escapeHtml(item.countryCode || "--")}</span></td>
      <td class="code">${escapeHtml(item.address)}</td>
      <td class="code">${escapeHtml(item.port || 22)}</td>
      <td>${item.startPort ? `<span class="code">${item.startPort}</span>` : '<span class="muted">未获取</span>'}</td>
      <td>${escapeHtml(item.username)}</td>
      <td class="${item.domain ? "" : "muted"}">${escapeHtml(empty(item.domain))}</td>
      <td class="${item.middle_domain ? "" : "muted"}">${escapeHtml(empty(item.middle_domain))}</td>
      <td><span class="badge ${statusClass}${awaitingHostKey ? " host-key-pending" : ""}" id="status-${escapeHtml(alias)}"${awaitingHostKey ? ' title="待首次连接确认指纹"' : ""}>${escapeHtml(statusText)}</span></td>
      <td><span class="badge ${nodeClass}" id="node-status-${escapeHtml(alias)}">${escapeHtml(nodeText)}</span></td>
      <td><div class="actions">
        <button class="btn small primary" data-vps-action="add-node" data-alias="${escapeHtml(alias)}">新增节点</button>
        <button class="btn small outline-success" data-vps-action="add-multi-ip" data-alias="${escapeHtml(alias)}">新增多IP节点</button>
        <button class="btn small slate" data-vps-action="deploy" data-alias="${escapeHtml(alias)}">部署</button>
        <button class="btn small primary" data-vps-action="restart" data-alias="${escapeHtml(alias)}">重启</button>
        <button class="btn small danger-soft" data-vps-action="reboot" data-alias="${escapeHtml(alias)}">重启VPS</button>
        <button class="btn small warning" data-vps-action="sync" data-alias="${escapeHtml(alias)}">同步配置</button>
        <button class="btn small outline-warning" data-vps-action="json" data-alias="${escapeHtml(alias)}">编辑JSON</button>
        <button class="btn small outline-warning" data-vps-action="debug" data-alias="${escapeHtml(alias)}">调试</button>
        <button class="btn small outline" data-vps-action="log" data-alias="${escapeHtml(alias)}">日志</button>
        <button class="btn small outline" data-vps-action="modify" data-alias="${escapeHtml(alias)}">修改落地</button>
        <button class="btn small outline-success" data-vps-action="traffic" data-alias="${escapeHtml(alias)}">流量</button>
        <button class="btn small slate" data-vps-action="backups" data-alias="${escapeHtml(alias)}">备份还原</button>
        <button class="btn small danger" data-vps-action="delete" data-alias="${escapeHtml(alias)}">删除</button>
      </div></td>
    </tr>`;
  }).join("");
  el("vpsRows").innerHTML = rows || `<tr><td colspan="11" class="muted center">${activeCountry ? "当前国家暂无 VPS" : "暂无 VPS"}</td></tr>`;
  wireVPSTable();
}

function wireVPSTable() {
  el("vpsRows").onclick = (event) => {
    const button = event.target.closest("button[data-vps-action]");
    if (!button || button.disabled) return;
    const alias = button.dataset.alias || "";
    const actions = {
      nodes: () => openNodes(alias),
      "add-node": () => openAddNode(alias),
      "add-multi-ip": () => openAddMultiIPNode(alias),
      deploy: () => deploySingbox(alias, button),
      restart: () => controlSingbox(alias, "restart", button),
      reboot: () => rebootVps(alias, button),
      sync: () => syncAllJson(alias, button),
      json: () => openVpsJSON(alias),
      debug: () => openDebug(alias),
      log: () => viewSingboxLog(alias),
      modify: () => openModifyOutbound(alias),
      traffic: () => trafficRank(alias),
      backups: () => openBackups(alias),
      delete: () => deleteVPS(alias, button),
    };
    actions[button.dataset.vpsAction]?.();
  };
}

function statusTextMap(status) {
  const map = { running: "运行中", stopped: "已停止", not_installed: "未安装", ssh_failed: "SSH失败", failed: "服务失败", error: "检测失败", unknown: "未检测" };
  return map[status] || "未知";
}

function renderBackupAliasOptions(targetAlias = state.backupTargetAlias) {
  const vpsList = state.overview?.vpsList || [];
  const currentVal = el("backupAliasFilter")?.value;
  if (targetAlias) {
    const others = vpsList.filter((v) => (v.alias || v.id) && (v.alias || v.id) !== targetAlias);
    const options = [
      `<option value="${escapeHtml(targetAlias)}">本机备份 (${escapeHtml(targetAlias)})</option>`,
      `<option value="">全部机器备份</option>`,
    ].concat(others.map((v) => `<option value="${escapeHtml(v.alias || v.id)}">${escapeHtml(v.alias || v.id)} 的备份</option>`)).join("");
    el("backupAliasFilter").innerHTML = options;
    if (currentVal && Array.from(el("backupAliasFilter").options).some((o) => o.value === currentVal)) {
      el("backupAliasFilter").value = currentVal;
    } else {
      el("backupAliasFilter").value = targetAlias;
    }
  } else {
    const options = [`<option value="">全部 VPS 备份</option>`].concat(
      vpsList.map((v) => `<option value="${escapeHtml(v.alias || v.id)}">${escapeHtml(v.alias || v.id)}</option>`)
    ).join("");
    el("backupAliasFilter").innerHTML = options;
    if (currentVal && Array.from(el("backupAliasFilter").options).some((o) => o.value === currentVal)) {
      el("backupAliasFilter").value = currentVal;
    }
  }
}

function fillAliasFilters() {
  const vpsList = state.overview.vpsList || [];
  const previous = {
    node: el("nodeAliasFilter").value,
    debug: el("debugAlias").value,
    iperf: el("debugIperfCurrent")?.value || "",
    rule: el("ruleAlias").value,
  };
  const options = [`<option value="">全部 VPS</option>`].concat(vpsList.map((v) => `<option value="${escapeHtml(v.alias || v.id)}">${escapeHtml(v.alias || v.id)}</option>`)).join("");
  const vpsOptions = options.replace('<option value="">全部 VPS</option>', "");
  el("nodeAliasFilter").innerHTML = options;
  if (previous.node && Array.from(el("nodeAliasFilter").options).some((item) => item.value === previous.node)) {
    el("nodeAliasFilter").value = previous.node;
  }
  renderBackupAliasOptions(state.backupTargetAlias);
  refreshOpenFilterCombos();
  el("debugAlias").innerHTML = vpsOptions;
  if (previous.debug && Array.from(el("debugAlias").options).some((item) => item.value === previous.debug)) el("debugAlias").value = previous.debug;
  if (el("debugIperfCurrent")) {
    el("debugIperfCurrent").innerHTML = `<option value="__local_windows__">本地 Windows</option>${vpsOptions}`;
    if (previous.iperf && Array.from(el("debugIperfCurrent").options).some((item) => item.value === previous.iperf)) el("debugIperfCurrent").value = previous.iperf;
  }
  updateDebugIperfTargetOptions(el("debugIperfCurrent")?.value || "");
  el("ruleAlias").innerHTML = vpsOptions;
  if (previous.rule && Array.from(el("ruleAlias").options).some((item) => item.value === previous.rule)) el("ruleAlias").value = previous.rule;
  if (el("singboxLogAlias")) {
    const prevLog = el("singboxLogAlias").value;
    el("singboxLogAlias").innerHTML = vpsOptions;
    if (prevLog && Array.from(el("singboxLogAlias").options).some((item) => item.value === prevLog)) {
      el("singboxLogAlias").value = prevLog;
    }
  }
  if (state.backupTargetAlias && !vpsList.some((item) => (item.alias || item.id) === state.backupTargetAlias)) {
    state.backupTargetAlias = "";
    setText("backupModalTitle", "备份还原");
  }
  renderProxyTargets();
}

function updateDebugIperfTargetOptions(currentAlias = "") {
  const target = el("debugIperfTarget");
  if (!target || !state.overview?.vpsList) return;
  const oldValue = target.value;
  target.innerHTML = state.overview.vpsList
    .map((v) => v.alias || v.id)
    .filter((alias) => alias && alias !== currentAlias)
    .map((alias) => `<option value="${escapeHtml(alias)}">${escapeHtml(alias)}</option>`)
    .join("");
  if (oldValue && oldValue !== currentAlias && Array.from(target.options).some((item) => item.value === oldValue)) {
    target.value = oldValue;
  }
}

function filterComboItems(kind) {
  const vpsList = state.overview?.vpsList || [];
  if (kind === "alias") {
    return vpsList.map((v) => ({ value: v.alias || v.id || "", meta: v.address || "" })).filter((item) => item.value);
  }
  const seen = new Set();
  return vpsList
    .map((v) => ({ value: String(v.address || "").trim(), meta: v.alias || v.id || "" }))
    .filter((item) => {
      if (!item.value || seen.has(item.value)) return false;
      seen.add(item.value);
      return true;
    });
}

function renderFilterCombo(combo, showAll = false) {
  const input = combo.querySelector("input");
  const menu = combo.querySelector(".filter-combo-menu");
  const query = showAll ? "" : input.value.trim().toLowerCase();
  const items = filterComboItems(combo.dataset.filterCombo)
    .filter((item) => !query || item.value.toLowerCase().includes(query) || item.meta.toLowerCase().includes(query))
    .slice(0, 30);
  menu.innerHTML = items.length
    ? items.map((item) => `<button type="button" class="filter-combo-option" data-value="${escapeHtml(item.value)}"><span>${escapeHtml(item.value)}</span><span class="filter-combo-option-meta">${escapeHtml(item.meta)}</span></button>`).join("")
    : `<div class="filter-combo-empty">暂无匹配</div>`;
  combo.classList.add("open");
  combo.dataset.showAll = showAll ? "true" : "false";
  menu.classList.remove("hidden");
}

function closeFilterCombo(combo) {
  combo.classList.remove("open");
  delete combo.dataset.showAll;
  combo.querySelector(".filter-combo-menu")?.classList.add("hidden");
}

function refreshOpenFilterCombos() {
  document.querySelectorAll(".filter-combo.open").forEach((combo) => {
    renderFilterCombo(combo, combo.dataset.showAll === "true");
  });
}

function setupFilterCombos() {
  document.querySelectorAll("[data-filter-combo]").forEach((combo) => {
    const input = combo.querySelector("input");
    const clear = combo.querySelector(".filter-combo-clear");
    const toggle = combo.querySelector(".filter-combo-toggle");
    const menu = combo.querySelector(".filter-combo-menu");
    input.addEventListener("focus", () => renderFilterCombo(combo));
    input.addEventListener("click", () => renderFilterCombo(combo));
    input.addEventListener("input", () => renderFilterCombo(combo));
    input.addEventListener("keydown", (event) => {
      if (event.key === "Escape") closeFilterCombo(combo);
      if (event.key === "ArrowDown") {
        const option = menu.querySelector(".filter-combo-option");
        if (option) {
          event.preventDefault();
          option.focus();
        }
      }
    });
    toggle.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      if (combo.classList.contains("open") && combo.dataset.showAll === "true") return closeFilterCombo(combo);
      input.focus();
      renderFilterCombo(combo, true);
    });
    clear.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      input.value = "";
      input.focus();
      renderFilterCombo(combo, true);
      state.nodePage = 1;
      renderNodes();
    });
    menu.addEventListener("click", (event) => {
      const option = event.target.closest(".filter-combo-option");
      if (!option) return;
      input.value = option.dataset.value || "";
      closeFilterCombo(combo);
      state.nodePage = 1;
      renderNodes();
    });
    menu.addEventListener("keydown", (event) => {
      if (!event.target.classList.contains("filter-combo-option")) return;
      const options = Array.from(menu.querySelectorAll(".filter-combo-option"));
      const index = options.indexOf(event.target);
      if (event.key === "Escape") {
        closeFilterCombo(combo);
        input.focus();
      } else if (event.key === "ArrowDown" && index < options.length - 1) {
        event.preventDefault();
        options[index + 1].focus();
      } else if (event.key === "ArrowUp") {
        event.preventDefault();
        if (index > 0) options[index - 1].focus();
        else input.focus();
      }
    });
  });
  document.addEventListener("click", (event) => {
    document.querySelectorAll(".filter-combo.open").forEach((combo) => {
      if (!combo.contains(event.target)) closeFilterCombo(combo);
    });
  });
}

function renderProxyTargets() {
  const box = el("proxyTargetOptions");
  if (!box || !state.overview) return;
  const selected = box.querySelector('input[name="proxyTarget"]:checked')?.value || "local";
  const targets = [{ value: "local", label: "本地" }]
    .concat(state.overview.vpsList.map((v) => ({ value: v.alias || v.id, label: v.alias || v.id })));
  const current = targets.some((target) => target.value === selected) ? selected : "local";
  box.innerHTML = targets.map((target) => `<label class="target-option"><input type="radio" name="proxyTarget" value="${escapeHtml(target.value)}" ${target.value === current ? "checked" : ""}> ${escapeHtml(target.label)}</label>`).join("");
  syncProxyTargetSelection();
}

function syncProxyTargetSelection() {
  const box = el("proxyTargetOptions");
  box?.querySelectorAll(".target-option").forEach((label) => {
    label.classList.toggle("selected", Boolean(label.querySelector("input")?.checked));
  });
}

async function loadNodes(force = false) {
  const selectValue = el("nodeAliasFilter") ? el("nodeAliasFilter").value : "";
  const key = selectValue || "__all__";
  if (!force && state.nodesLoadedKey === key) {
    renderNodes();
    return;
  }
  if (state.nodesLoadPromise && state.nodesLoadKey === key) return state.nodesLoadPromise;
  const loadId = ++state.nodesLoadId;
  if (state.nodesLoadedKey !== key) state.nodePage = 1;
  if (state.nodesLoadedKey !== key) {
    el("nodeRows").innerHTML = `<tr><td colspan="11" class="muted center">正在读取节点...</td></tr>`;
    state.nodeRowsHTML = "";
  }
  const promise = (async () => {
    let nodes;
    try {
      nodes = await api.GetNodes(selectValue);
    } catch (error) {
      if (loadId !== state.nodesLoadId) return;
      state.nodesLoadedKey = "";
      state.nodeRowsHTML = "";
      el("nodeRows").innerHTML = `<tr><td colspan="11" class="status-fail center">${escapeHtml(frontendErrorMessage(error, "节点读取失败"))}</td></tr>`;
      return;
    }
    if (loadId !== state.nodesLoadId) return;
    state.nodes = Array.isArray(nodes) ? nodes : [];
    state.nodesLoadedKey = key;
    const currentKeys = new Set(state.nodes.map(nodeKey));
    for (const cachedKey of state.nodeOutboundResults.keys()) {
      if (!currentKeys.has(cachedKey)) state.nodeOutboundResults.delete(cachedKey);
    }
    for (const selectedKey of state.nodeSelected) {
      if (!currentKeys.has(selectedKey)) state.nodeSelected.delete(selectedKey);
    }
    rebuildNodeIndex();
    renderNodes();
  })();
  state.nodesLoadKey = key;
  state.nodesLoadPromise = promise;
  try {
    await promise;
  } finally {
    if (state.nodesLoadPromise === promise) {
      state.nodesLoadPromise = null;
      state.nodesLoadKey = "";
    }
  }
}

function renderNodes() {
  const aliasText = el("nodeAliasText")?.value.trim().toLowerCase() || "";
  const vpsIpText = el("nodeVpsIpSearch")?.value.trim().toLowerCase() || "";
  const groupText = el("nodeGroupSearch")?.value.trim().toLowerCase() || "";
  const outboundText = el("nodeSearch")?.value.trim().toLowerCase() || "";
  const portText = el("nodePortSearch")?.value.trim() || "";
  const allNodes = filteredNodes(aliasText, vpsIpText, groupText, outboundText, portText);
  const pages = Math.max(1, Math.ceil(allNodes.length / state.nodePageSize));
  if (state.nodePage > pages) state.nodePage = pages;
  const start = (state.nodePage - 1) * state.nodePageSize;
  const visibleNodes = allNodes.slice(start, start + state.nodePageSize);
  const rows = visibleNodes.map((node) => {
    const vps = vpsByAlias(node.vpsAlias);
    const key = nodeKey(node);
    const checked = state.nodeSelected.has(key) ? "checked" : "";
    const docBusy = state.exportingNodeDocs.has(key);
    const outboundResult = state.nodeOutboundResults.get(key);
    const outboundStatus = outboundResult ? (outboundResult.success ? outboundResult.outbound_ip : outboundResult.message) : "未查询";
    const fullOutbound = outboundFull(node);
    return `<tr>
      <td><input type="checkbox" class="node-select" data-key="${escapeHtml(key)}" ${checked}></td>
      <td title="${escapeHtml(node.vpsAlias)}">${escapeHtml(node.vpsAlias)}</td>
      <td class="code">${escapeHtml(vps.address || "")}</td>
      <td title="${escapeHtml(node.tag || node.fileName)}">${escapeHtml(node.tag || node.fileName)}</td>
      <td><span class="badge">${escapeHtml(nodeProtocolLabel(node))}</span></td>
      <td class="code">${escapeHtml(node.listenPort || "")}</td>
      <td>${escapeHtml(vps.domain || vps.address || "-")}</td>
      <td><button class="link-btn outbound-query" data-key="${escapeHtml(key)}">${escapeHtml(outboundStatus)}</button></td>
      <td class="copy-cell" data-copy="${escapeHtml(fullOutbound)}" title="${escapeHtml(fullOutbound)}">${escapeHtml(node.outboundIp || "无")}</td>
      <td><div class="actions node-actions">
        <button class="btn small secondary" data-action="detail" data-key="${escapeHtml(key)}">详情</button>
        <button class="btn small success" data-action="copy" data-key="${escapeHtml(key)}">复制</button>
        <button class="btn small danger" data-action="delete" data-key="${escapeHtml(key)}">删除</button>
        <button class="btn small warning" data-action="delete-group" data-key="${escapeHtml(key)}">删除同组</button>
        <button class="btn small slate" data-action="copy-group" data-key="${escapeHtml(key)}">复制同组</button>
        <button class="btn small primary" data-action="download-group" data-key="${escapeHtml(key)}">下载</button>
        <button class="btn small success" data-action="doc" data-key="${escapeHtml(key)}" ${docBusy ? "disabled" : ""}>${docBusy ? "导出中" : "文档"}</button>
      </div></td>
      <td><button class="btn small secondary" data-action="qr" data-key="${escapeHtml(key)}">二维码</button></td>
    </tr>`;
  }).join("");
  const tbody = el("nodeRows");
  const tableCard = tbody?.closest(".nodes-table-card");
  const active = tbody?.contains(document.activeElement) ? document.activeElement : null;
  const focusKey = active?.dataset?.key || "";
  const focusKind = active?.classList?.contains("node-select")
    ? "select"
    : active?.classList?.contains("outbound-query")
      ? "outbound"
      : active?.dataset?.action || "";
  const scrollTop = tableCard?.scrollTop || 0;
  const scrollLeft = tableCard?.scrollLeft || 0;
  const nextHTML = rows || `<tr><td colspan="11" class="muted center">没有找到任何节点配置</td></tr>`;
  if (tbody && state.nodeRowsHTML !== nextHTML) {
    tbody.innerHTML = nextHTML;
    state.nodeRowsHTML = nextHTML;
    wireNodeTable();
    if (focusKey) {
      const target = Array.from(tbody.querySelectorAll("[data-key]")).find((item) => {
        if (item.dataset.key !== focusKey) return false;
        if (focusKind === "select") return item.classList.contains("node-select");
        if (focusKind === "outbound") return item.classList.contains("outbound-query");
        return item.dataset.action === focusKind;
      });
      try {
        target?.focus({ preventScroll: true });
      } catch {
        target?.focus();
      }
    }
    if (tableCard) {
      tableCard.scrollTop = scrollTop;
      tableCard.scrollLeft = scrollLeft;
    }
  }
  setText("nodeSummaryText", `共 ${allNodes.length} 条，当前页 ${visibleNodes.length} 条`);
  setText("nodePageText", `第 ${state.nodePage} / ${pages} 页`);
  el("nodePrevPageBtn").disabled = state.nodePage <= 1;
  el("nodeNextPageBtn").disabled = state.nodePage >= pages;
  updateNodeSelectionState();
}

function filteredNodes(aliasText = "", vpsIpText = "", groupText = "", outboundText = "", portText = "") {
  return state.nodes.filter((node) => {
    const vps = vpsByAlias(node.vpsAlias);
    const group = groupFromNode(node).toLowerCase();
    const vpsIp = String(vps.address || "").toLowerCase();
    if (aliasText && !String(node.vpsAlias || "").toLowerCase().includes(aliasText)) return false;
    if (vpsIpText && !vpsIp.includes(vpsIpText)) return false;
    if (groupText && !group.includes(groupText)) return false;
    if (outboundText && !(state.nodeSearchIndex.get(nodeKey(node)) || "").includes(outboundText)) return false;
    if (portText && String(node.listenPort || "") !== portText) return false;
    return true;
  });
}

function currentFilteredNodes() {
  const aliasText = el("nodeAliasText")?.value.trim().toLowerCase() || "";
  const vpsIpText = el("nodeVpsIpSearch")?.value.trim().toLowerCase() || "";
  const groupText = el("nodeGroupSearch")?.value.trim().toLowerCase() || "";
  const outboundText = el("nodeSearch")?.value.trim().toLowerCase() || "";
  const portText = el("nodePortSearch")?.value.trim() || "";
  return filteredNodes(aliasText, vpsIpText, groupText, outboundText, portText);
}

let lastSelectedInputKey = null;

function wireNodeTable() {
  const tbody = el("nodeRows");
  if (!tbody) return;
  tbody.onchange = (event) => {
    const input = event.target.closest(".node-select");
    if (!input) return;
    if (input.checked) state.nodeSelected.add(input.dataset.key);
    else state.nodeSelected.delete(input.dataset.key);
    lastSelectedInputKey = input.dataset.key;
    updateNodeSelectionState();
  };
  tbody.onclick = (event) => {
    const input = event.target.closest(".node-select");
    if (input) {
      if (event.shiftKey && lastSelectedInputKey && lastSelectedInputKey !== input.dataset.key) {
        const allInputs = Array.from(tbody.querySelectorAll(".node-select"));
        const lastIdx = allInputs.findIndex((item) => item.dataset.key === lastSelectedInputKey);
        const currIdx = allInputs.findIndex((item) => item.dataset.key === input.dataset.key);
        if (lastIdx !== -1 && currIdx !== -1) {
          const [start, end] = [Math.min(lastIdx, currIdx), Math.max(lastIdx, currIdx)];
          const targetChecked = input.checked;
          for (let i = start; i <= end; i++) {
            const targetInput = allInputs[i];
            targetInput.checked = targetChecked;
            if (targetChecked) state.nodeSelected.add(targetInput.dataset.key);
            else state.nodeSelected.delete(targetInput.dataset.key);
          }
          updateNodeSelectionState();
        }
      }
      lastSelectedInputKey = input.dataset.key;
      return;
    }
    const copyCell = event.target.closest(".copy-cell");
    if (copyCell) {
      copyText(copyCell.dataset.copy, "落地信息已复制");
      return;
    }
    const outboundButton = event.target.closest(".outbound-query");
    if (outboundButton) {
      queryOutboundByKey(outboundButton.dataset.key);
      return;
    }
    const button = event.target.closest("button[data-action]");
    if (!button || button.disabled) return;
    const node = findNodeByKey(button.dataset.key);
    if (!node) return showToast("节点不存在");
    const group = groupFromNode(node);
    switch (button.dataset.action) {
      case "detail":
        showNodeDetail(button.dataset.key);
        break;
      case "copy":
        copyNodeLink(button.dataset.key);
        break;
      case "delete":
        deleteNode(node.vpsAlias, node.fileName);
        break;
      case "delete-group":
        deleteNodeGroup(node.vpsAlias, group);
        break;
      case "copy-group":
        copyGroupLinks(node.vpsAlias, group);
        break;
      case "download-group":
        downloadGroup(node.vpsAlias, group);
        break;
      case "doc":
        downloadNodeDoc(button.dataset.key);
        break;
      case "qr":
        showNodeQR(button.dataset.key);
        break;
    }
  };
}

function updateNodeSelectionState() {
  setText("nodeSelectedCount", state.nodeSelected.size);
  ["batchDeleteNodesBtn", "batchForceDeleteNodesBtn", "batchRulesBtn", "batchCopyLinksBtn", "batchExportDocBtn"].forEach((id) => {
    const button = el(id);
    if (button) button.disabled = state.nodeSelected.size === 0;
  });
  const visibleKeys = Array.from(document.querySelectorAll(".node-select")).map((input) => input.dataset.key);
  const selectAll = el("selectAllNodesInput");
  if (selectAll) selectAll.checked = visibleKeys.length > 0 && visibleKeys.every((key) => state.nodeSelected.has(key));
}

function findNodeByKey(key) {
  return state.nodeIndex.get(key);
}

function beginBackupAction() {
  return {
    actionId: ++state.backupActionId,
    loadId: state.backupLoadId,
    sourceAlias: el("backupAliasFilter").value,
    targetAlias: state.backupTargetAlias,
  };
}

function backupActionCurrent(action, loadId = action.loadId) {
  return action.actionId === state.backupActionId &&
    loadId === state.backupLoadId &&
    el("backupAliasFilter").value === action.sourceAlias &&
    state.backupTargetAlias === action.targetAlias &&
    !el("backupModal").classList.contains("hidden");
}

function clearBackupPreview() {
  state.backupExpandedPath = "";
  state.backupExpandedContent = "";
  state.backupEditingPath = "";
}

async function loadBackups() {
  const requestId = ++state.backupLoadId;
  const sourceAlias = el("backupAliasFilter").value;
  let backups;
  try {
    backups = await api.GetBackups(sourceAlias);
  } catch (error) {
    if (requestId === state.backupLoadId && !el("backupModal").classList.contains("hidden")) {
      el("backupRows").innerHTML = `<tr><td colspan="5" class="status-fail">${escapeHtml(frontendErrorMessage(error, "备份读取失败"))}</td></tr>`;
    }
    return;
  }
  if (requestId !== state.backupLoadId || el("backupModal").classList.contains("hidden")) return;
  state.backups = Array.isArray(backups) ? backups : [];
  const rows = state.backups.map((file) => {
    const expanded = state.backupExpandedPath === file.path;
    const target = state.backupTargetAlias || el("backupAliasFilter").value || "";
    const source = file.sourceAlias || backupSourceAlias(file.name);
    const isCrossServer = Boolean(target && source && source.toLowerCase() !== target.toLowerCase());
    const deployLabel = isCrossServer ? `还原到本机 (${escapeHtml(target)})` : "还原到本机";
    const deployClass = isCrossServer ? "warning" : "primary";
    const deployTitle = isCrossServer
      ? `将【${escapeHtml(source)}】的历史备份还原并覆盖部署到当前本机【${escapeHtml(target)}】`
      : `将当前本机的备份还原并部署`;
    const preview = expanded ? `<tr class="backup-expand-row"><td colspan="5">
      <div class="backup-expand-panel">
        <div class="backup-expand-head">
          <span>${escapeHtml(file.name)}</span>
          <div class="actions">
            <button class="btn small secondary" data-backup-action="copy" data-path="${escapeHtml(file.path)}">复制</button>
            ${state.backupEditingPath === file.path
              ? `<button class="btn small primary" data-backup-action="save" data-path="${escapeHtml(file.path)}">保存部署</button>`
              : `<button class="btn small warning" data-backup-action="edit" data-path="${escapeHtml(file.path)}">编辑</button>`}
            <button class="btn small secondary" data-backup-action="read" data-path="${escapeHtml(file.path)}">收起</button>
          </div>
        </div>
        <textarea class="backup-expand-content" data-backup-path="${escapeHtml(file.path)}" ${state.backupEditingPath === file.path ? "" : "readonly"}>${escapeHtml(state.backupExpandedContent)}</textarea>
      </div>
    </td></tr>` : "";
    return `<tr class="${expanded ? "selected-row" : ""}">
    <td class="code" title="${escapeHtml(source || "通用")}"><strong>${escapeHtml(source || "通用")}</strong></td>
    <td class="code" title="${escapeHtml(file.name)}">${escapeHtml(file.name)}</td><td>${formatSize(file.size)}</td><td>${escapeHtml(file.modified)}</td>
    <td><div class="actions">
      <button class="btn small secondary" data-backup-action="read" data-path="${escapeHtml(file.path)}">${expanded ? "收起" : "查看"}</button>
      <button class="btn small ${deployClass}" data-backup-action="restore" data-path="${escapeHtml(file.path)}" title="${deployTitle}">${deployLabel}</button>
      <button class="btn small danger" data-backup-action="delete" data-path="${escapeHtml(file.path)}">删除</button>
    </div></td>
  </tr>${preview}`;
  }).join("");
  el("backupRows").innerHTML = rows || `<tr><td colspan="5" class="muted">暂无备份</td></tr>`;
  wireBackupTable();
}

function wireBackupTable() {
  el("backupRows").onclick = (event) => {
    const button = event.target.closest("button[data-backup-action]");
    if (!button || button.disabled) return;
    const path = button.dataset.path || "";
    const actions = {
      read: () => readBackup(path),
      copy: () => copyBackupContent(path),
      edit: () => enableBackupJsonEdit(path),
      save: () => saveBackupJson(path),
      restore: () => restoreBackup(path),
      delete: () => deleteBackup(path),
    };
    actions[button.dataset.backupAction]?.();
  };
}

function backupSourceAlias(name) {
  const text = String(name || "");
  const matched = text.match(/^(?:~)?(.*)_\d{8}_\d{6}_all\.json$/i);
  if (matched) {
    try { return decodeURIComponent(matched[1]); } catch { return matched[1]; }
  }
  const parts = text.split("_");
  return parts.length > 1 ? parts[0] : "";
}

function formatSize(size) {
  if (size > 1048576) return `${(size / 1048576).toFixed(2)} MB`;
  if (size > 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${size} B`;
}

async function readBackup(path) {
  if (state.backupExpandedPath === path) {
    state.backupActionId += 1;
    clearBackupPreview();
    await loadBackups();
    return false;
  }
  const action = beginBackupAction();
  let content;
  try {
    content = await api.ReadBackup(path);
  } catch (error) {
    if (backupActionCurrent(action)) throw error;
    return false;
  }
  if (!backupActionCurrent(action)) return false;
  state.backupExpandedPath = path;
  state.backupEditingPath = "";
  try { state.backupExpandedContent = JSON.stringify(JSON.parse(content), null, 2); }
  catch { state.backupExpandedContent = content; }
  const renderLoadId = state.backupLoadId + 1;
  await loadBackups();
  return backupActionCurrent(action, renderLoadId);
}

async function copyBackupContent(path) {
  const input = backupEditor(path);
  let content = input ? input.value : "";
  if (!content && state.backupExpandedPath === path) content = state.backupExpandedContent;
  if (!content) {
    content = await api.ReadBackup(path);
    try { content = JSON.stringify(JSON.parse(content), null, 2); } catch {}
  }
  await copyText(content, "备份内容已复制");
}

async function enableBackupJsonEdit(path) {
  if (state.backupExpandedPath !== path) {
    if (!await readBackup(path)) return;
  }
  const action = beginBackupAction();
  if (!backupActionCurrent(action)) return;
  state.backupEditingPath = path;
  const renderLoadId = state.backupLoadId + 1;
  await loadBackups();
  if (!backupActionCurrent(action, renderLoadId)) return;
  backupEditor(path)?.focus();
  showToast("已进入编辑模式");
}

async function saveBackupJson(path) {
  const alias = state.backupTargetAlias || el("backupAliasFilter").value;
  if (!alias) return showToast("请先选择目标 VPS");
  if (state.backupEditingPath !== path) return showToast("请先点击编辑");
  const input = backupEditor(path);
  const content = input ? input.value : state.backupExpandedContent;
  if (!content.trim()) return showToast("JSON 内容不能为空");
  if (!await appConfirm(`确认将当前编辑的 JSON 部署到 ${alias} 并重启 sing-box 吗？\n原备份文件不会被修改；失败会自动回滚。`, "保存部署 JSON")) return;
  setBackupBusy(true);
  try {
    showToast("正在保存并部署 JSON...");
    const result = await api.ImportAllJSONAndDeploy(alias, content);
    if (!result.success) {
      showDetail("JSON 保存部署失败", result);
      showToast("保存部署失败，已尝试回滚");
      return;
    }
    await finishBackupDeployment(alias, "保存部署完成");
  } finally {
    setBackupBusy(false);
  }
}

function backupEditor(path) {
  return Array.from(document.querySelectorAll(".backup-expand-content")).find((item) => item.dataset.backupPath === path);
}

async function deleteBackup(path) {
  if (!await appConfirm("确认删除该备份吗？", "删除备份")) return;
  await api.DeleteBackup(path);
  showToast("备份已删除");
  await loadBackups();
  await refreshOverviewAfterChange();
  notifyNodeDataChanged(state.backupTargetAlias || "");
}

function setBackupProgress(percent, text = "") {
  const box = el("backupProgress");
  if (!box) return;
  const value = Math.max(0, Math.min(100, Number(percent) || 0));
  box.classList.remove("hidden");
  box.scrollIntoView({ behavior: "smooth", block: "nearest" });
  if (el("backupProgressBar")) el("backupProgressBar").style.width = `${value}%`;
  setText("backupProgressPercent", `${Math.round(value)}%`);
  if (text) setText("backupProgressText", text);
}

function resetBackupProgress() {
  el("backupProgress")?.classList.add("hidden");
  if (el("backupProgressBar")) el("backupProgressBar").style.width = "0%";
  setText("backupProgressPercent", "0%");
  setText("backupProgressText", "准备执行");
  setText("backupProgressResult", "准备执行");
}

async function restoreBackup(path) {
  const alias = state.backupTargetAlias || el("backupAliasFilter").value;
  if (!alias) return showToast("请先选择目标 VPS");
  const file = state.backups.find((item) => item.path === path);
  const fileName = file?.name || String(path || "").split(/[\\/]/).pop() || "";
  const source = file?.sourceAlias || backupSourceAlias(fileName);
  const isMigration = Boolean(source && source.toLowerCase() !== alias.toLowerCase());
  const title = isMigration ? "跨机器还原到本机" : "还原到本机";
  const message = isMigration
    ? `【确认跨机器还原到当前本机】\n\n您正在将主机【${source}】的历史备份文件还原到当前本机【${alias}】！\n\n此操作会用【${source}】备份中的全部节点配置替换【${alias}】的本地节点，并下发部署到【${alias}】重启 sing-box 生效。\n\n确认将【${source}】的备份还原并覆盖部署到【${alias}】吗？`
    : `确认用该备份覆盖【${alias}】的本地节点配置，并部署到该服务器吗？\n部署成功后会自动重启 sing-box。`;
  if (!await appConfirm(message, title)) return;
  setBackupBusy(true);
  try {
    setBackupProgress(15, `正在读取并解析【${source || "历史"}】备份配置...`);
    setText("backupProgressResult", `正在将备份还原部署到【${alias}】...`);
    showToast(isMigration ? `正在将【${source}】备份还原到【${alias}】...` : "正在还原并部署配置...");
    let publish;
    setBackupProgress(45, `正在下发配置文件到【${alias}】(SSH)...`);
    if (isMigration) {
      if (typeof api.MigrateBackup === "function") {
        publish = await api.MigrateBackup(alias, path);
      } else {
        const content = await api.ReadBackup(path);
        publish = await api.ImportAllJSONAndDeploy(alias, content);
      }
    } else {
      if (typeof api.RestoreBackup === "function") {
        publish = await api.RestoreBackup(alias, path);
      } else {
        const content = await api.ReadBackup(path);
        publish = await api.ImportAllJSONAndDeploy(alias, content);
      }
    }
    if (!publish.success) {
      setBackupProgress(100, `${title}失败`);
      setText("backupProgressResult", `${title}失败: ${publish.message || "执行未成功"}`);
      showDetail(`${title}失败`, publish);
      showToast(`${title}失败: ${publish.message || "执行未成功"}`);
      return;
    }
    setBackupProgress(80, "正在重启服务并同步状态...");
    await finishBackupDeployment(alias, isMigration ? `已成功将【${source}】备份还原部署到【${alias}】` : "还原部署完成");
    setBackupProgress(100, "还原部署完成！");
    setText("backupProgressResult", "还原部署已完成，状态已同步");
    await new Promise((r) => setTimeout(r, 600));
  } finally {
    setBackupBusy(false);
  }
}

function setBackupBusy(busy) {
  document.querySelectorAll("#backupModal button, #backupModal select, #backupModal textarea").forEach((item) => {
    item.disabled = busy;
  });
}

async function finishBackupDeployment(alias, message) {
  let finalMessage = message;
  try {
    const backup = await api.BackupNow(alias);
    finalMessage += `，已备份：${backup.name}`;
  } catch (error) {
    finalMessage += `，但自动备份失败：${error?.message || "未知错误"}`;
  }
  closeBackupModal();
  await refreshStatusAfterVpsChange(alias);
  await showNodesForAlias("");
  showToast(finalMessage);
}

async function backupNow() {
  const alias = state.backupTargetAlias || el("backupAliasFilter").value;
  if (!alias) return showToast("请先选择 VPS");
  const btn = el("backupNowBtn");
  if (btn?.disabled) return;
  try {
    if (btn) {
      btn.disabled = true;
      btn.textContent = "备份中...";
    }
    setBackupProgress(30, `正在为【${alias}】生成配置备份...`);
    setText("backupProgressResult", `正在备份【${alias}】...`);
    showToast(`正在为【${alias}】生成备份...`);
    const file = await api.BackupNow(alias);
    setBackupProgress(100, `已备份：${file.name}`);
    setText("backupProgressResult", `已备份【${alias}】：${file.name}`);
    showToast(`已备份【${alias}】：${file.name}`);
    await loadBackups();
    await refreshOverviewAfterChange();
    notifyNodeDataChanged(alias);
  } catch (err) {
    setBackupProgress(100, `备份失败: ${err?.message || err}`);
    showToast(`备份失败: ${err?.message || err}`);
  } finally {
    if (btn) {
      btn.disabled = false;
      btn.textContent = "立即备份";
    }
  }
}

async function restoreJsonFromNodes() {
  const alias = state.backupTargetAlias || el("backupAliasFilter").value;
  if (!alias) return showToast("请先选择 VPS");
  const btn = el("restoreJsonBtn");
  if (btn?.disabled) return;
  setBackupBusy(true);
  try {
    if (btn) btn.textContent = "生成中...";
    setBackupProgress(30, "正在根据本地节点生成还原 JSON...");
    setText("backupProgressResult", "正在生成还原 JSON...");
    showToast("正在根据本地节点生成还原 JSON...");
    const backup = await api.BackupAllJSONFromNodes(alias);
    setBackupProgress(100, `还原 JSON 已生成：${backup.name}`);
    setText("backupProgressResult", `还原 JSON 已保存：${backup.name}`);
    await loadBackups();
    await refreshOverviewAfterChange();
    notifyNodeDataChanged(alias);
    showToast("可迁移 all.json 已保存到备份列表：" + backup.name);
  } catch (error) {
    setBackupProgress(100, "保存还原 JSON 失败");
    showToast("保存还原 JSON 失败：" + frontendErrorMessage(error, "未知错误"));
  } finally {
    if (btn) btn.textContent = "保存还原 JSON";
    setBackupBusy(false);
  }
}

function setView(name, options = {}) {
  document.querySelectorAll(".nav-item").forEach((btn) => btn.classList.toggle("active", btn.dataset.view === name));
  ["vps", "nodes", "proxy", "middle", "backups", "records", "connections", "rules", "deleted", "debug"].forEach((view) => el(`${view}View`).classList.toggle("hidden", view !== name));
  const titles = {
    vps: ["VPS 连接列表", "管理服务器、Sing-box 状态和节点配置。"],
    nodes: ["所有节点的聚合列表", ""],
    proxy: ["代理检测", "SOCKS5、HTTP 验证真实代理请求；VMess、Trojan 仅检测端点连通。"],
    middle: ["中转加速", "批量生成中转后的节点或代理地址。"],
    backups: ["备份记录", "查看、还原、删除本地备份文件。"],
    records: ["节点记录", "管理节点采购和续费记录。"],
    connections: ["连接记录", "导入、搜索、导出落地连接信息。"],
    rules: ["规则组", "管理并应用节点路由规则组。"],
    deleted: ["删除记录", "自动删除会保留节点快照，可在这里恢复并重新发布到对应 VPS。"],
    debug: ["VPS 调试", "执行 Ping、MTR、端口检测、流量查看。"],
  };
  el("pageTitle").textContent = titles[name][0];
  el("pageDesc").textContent = titles[name][1];
  el("addVpsBtn").classList.toggle("hidden", name !== "vps");
  el("vpsCountryFilterWrap").classList.toggle("hidden", name !== "vps");
  if (name === "nodes" && !options.skipNodeLoad) loadNodes();
  if (name === "records") loadRecords();
  if (name === "connections") loadConnections();
  if (name === "rules") loadRules();
  if (name === "deleted") loadDeleted();
}

async function refreshNodeMode(force = false, refreshActiveNodes = true) {
  const fresh = Date.now() - state.nodeModeRefreshedAt < 15000;
  if (!force && !state.nodeModeDirty && fresh) return;
  if (state.nodeModeRefreshPromise) return state.nodeModeRefreshPromise;
  const revision = state.nodeModeRevision;
  state.nodeModeRefreshPromise = (async () => {
    await refreshOverviewAfterChange();
    try {
      await syncHostKeyTrust();
    } catch (error) {
      console.warn("同步 SSH 指纹信任状态失败", error);
    }
    if (refreshActiveNodes && document.querySelector(".nav-item.active")?.dataset.view === "nodes") {
      await loadNodes(true);
    }
    state.nodeModeRefreshedAt = Date.now();
    if (state.nodeModeRevision === revision) state.nodeModeDirty = false;
  })().finally(() => {
    state.nodeModeRefreshPromise = null;
  });
  return state.nodeModeRefreshPromise;
}

function markNodeModeDirty() {
  state.nodeModeDirty = true;
  state.nodeModeRevision += 1;
}

function openModal(vps) {
  el("modalTitle").textContent = vps ? "编辑 VPS" : "添加 VPS";
  el("vpsModal").dataset.originalAlias = vps ? (vps.alias || vps.id || "") : "";
  el("vpsModal").dataset.hostId = vps?.hostId || "";
  el("vpsAlias").value = vps?.alias || vps?.id || "";
  el("vpsAddress").value = vps?.address || "";
  el("vpsPort").value = vps?.port || 22;
  el("vpsUsername").value = vps?.username || "root";
  el("vpsCountryCode").value = vps?.countryCode && vps.countryCode !== "--" ? vps.countryCode : "";
  el("vpsPassword").value = vps?.password || "";
  el("vpsAuthMethod").value = vps?.key_path ? "key" : "password";
  el("vpsDomain").value = vps?.domain || "";
  el("vpsMiddleDomain").value = vps?.middle_domain || "";
  el("vpsKeyPath").value = vps?.key_path || "";
  refreshVpsAuthFields();
  el("vpsModal").classList.remove("hidden");
}

function closeModal() { el("vpsModal").classList.add("hidden"); }

function refreshVpsAuthFields() {
  const method = el("vpsAuthMethod")?.value || "password";
  el("vpsPasswordField")?.classList.toggle("hidden", method !== "password");
  el("vpsKeyPathField")?.classList.toggle("hidden", method !== "key");
}

async function saveVPS() {
  const saveBtn = el("saveVpsBtn");
  const cancelBtn = el("cancelVpsModalBtn");
  const closeBtn = el("closeVpsModalBtn");
  if (saveBtn?.disabled) return;
  const originalAlias = el("vpsModal").dataset.originalAlias || "";
  const alias = el("vpsAlias").value.trim();
  const authMethod = el("vpsAuthMethod").value;
  try {
    if (saveBtn) {
      saveBtn.disabled = true;
      saveBtn.textContent = "保存中...";
    }
    if (cancelBtn) cancelBtn.disabled = true;
    if (closeBtn) closeBtn.disabled = true;
    showToast("正在保存 VPS 配置...");
    await api.SaveVPS({
      id: originalAlias || alias,
      hostId: el("vpsModal").dataset.hostId || "",
      original_alias: originalAlias,
      alias,
      address: el("vpsAddress").value.trim(),
      port: Number(el("vpsPort").value || 22),
      username: el("vpsUsername").value.trim(),
      password: authMethod === "password" ? el("vpsPassword").value : "",
      key_path: authMethod === "key" ? el("vpsKeyPath").value.trim() : "",
      domain: el("vpsDomain").value.trim(),
      middle_domain: el("vpsMiddleDomain").value.trim(),
      countryCode: el("vpsCountryCode").value.trim().toUpperCase(),
    });
    closeModal();
    showToast("VPS 已保存");
    await loadOverview();
    if (originalAlias && originalAlias !== alias) clearStatusCacheFor(originalAlias);
    await refreshStatusAfterVpsChange(alias, { syncRemote: !originalAlias });
  } finally {
    if (saveBtn) {
      saveBtn.disabled = false;
      saveBtn.textContent = "保存";
    }
    if (cancelBtn) cancelBtn.disabled = false;
    if (closeBtn) closeBtn.disabled = false;
  }
}

async function deleteVPS(alias, button = null) {
  if (!await appConfirm(`确认删除 ${alias} 吗？`, "删除 VPS")) return;
  if (button) {
    button.disabled = true;
    button.textContent = "删除中...";
  }
  showToast(`正在删除 ${alias}...`);
  try {
    await api.DeleteVPS(alias);
    showToast("VPS 已删除");
    await loadOverview();
    clearStatusCacheFor(alias);
  } finally {
    if (button) {
      button.disabled = false;
      button.textContent = "删除";
    }
  }
}

function showResult(result) {
  showDetail("执行结果", result);
}

async function testTCP() {
  el("tcpResult").textContent = "检测中...";
  el("tcpResult").textContent = JSON.stringify(await api.TestTCP(el("tcpHost").value, Number(el("tcpPort").value)), null, 2);
}

async function testProxyNodes() {
  const input = el("proxyNodes")?.value || "";
  if (!input.trim()) return showToast("请先输入需要检测的节点");
  const concurrency = Math.min(50, Math.max(1, Number(el("proxyConcurrency")?.value || 5)));
  if (el("proxyConcurrency")) el("proxyConcurrency").value = concurrency;
  const runId = ++state.proxyRunId;
  const button = el("proxyBatchBtn");
  const cancelButton = el("cancelProxyBtn");
  const oldButtonText = button?.textContent || "开始检测";
  if (button) {
    button.disabled = true;
    button.textContent = "检测中...";
    button.dataset.runId = String(runId);
  }
  if (cancelButton) cancelButton.classList.remove("hidden");

  const preset = el("proxyTargetPreset")?.value || "https://cp.cloudflare.com/generate_204";
  const customUrl = el("proxyCustomUrl")?.value?.trim() || "";
  const testUrl = preset === "custom" ? (customUrl || "https://cp.cloudflare.com/generate_204") : preset;

  const progressBox = el("proxyProgressBox");
  const progressBar = el("proxyProgressBar");
  const progressText = el("proxyProgressText");
  if (progressBox) progressBox.classList.remove("hidden");
  if (progressBar) progressBar.style.width = "0%";
  if (progressText) progressText.textContent = "进度：0%";

  setText("proxySummaryText", "检测中...");
  setText("proxyRunLocation", "检测位置：等待后端确认");
  el("proxyResultRows").innerHTML = `<tr><td colspan="7" class="muted center">检测中...</td></tr>`;
  const target = document.querySelector('input[name="proxyTarget"]:checked')?.value || "local";

  state.proxyRawItems = [];
  state.proxyLocation = null;

  EventsOff("proxy:item");
  EventsOff("proxy:progress");

  EventsOn("proxy:item", (item) => {
    if (runId !== state.proxyRunId) return;
    state.proxyRawItems.push(item);
    renderProxyResults(state.proxyRawItems, state.proxyLocation);
  });

  EventsOn("proxy:progress", (data) => {
    if (runId !== state.proxyRunId) return;
    const current = Number(data?.index || 0);
    const total = Number(data?.total || 1);
    const percent = Math.min(100, Math.max(0, Math.round((current / total) * 100)));
    if (progressBar) progressBar.style.width = `${percent}%`;
    if (progressText) progressText.textContent = `进度：${current}/${total} (${percent}%)`;
  });

  try {
    const response = await api.TestProxyNodes({ nodes: input, concurrency, target, testUrl });
    if (runId !== state.proxyRunId) return;
    const result = normalizeProxyDetectResponse(response);
    state.proxyRawItems = result.items;
    state.proxyLocation = result.location;
    if (progressBar) progressBar.style.width = "100%";
    if (progressText) progressText.textContent = `进度：${result.items.length}/${result.items.length} (100%)`;
    renderProxyResults(result.items, result.location);
    el("tcpResult").textContent = `检测完成：${result.items.length} 个节点`;
  } catch (error) {
    if (runId !== state.proxyRunId) return;
    const message = frontendErrorMessage(error, "代理检测失败");
    setText("proxySummaryText", "检测失败");
    setText("proxyRunLocation", "检测位置：后端未确认");
    el("proxyResultRows").innerHTML = `<tr><td colspan="7" class="status-fail center">${escapeHtml(message)}</td></tr>`;
    showToast(message);
  } finally {
    EventsOff("proxy:item");
    EventsOff("proxy:progress");
    if (cancelButton) cancelButton.classList.add("hidden");
    if (button?.dataset.runId === String(runId)) {
      button.disabled = false;
      button.textContent = oldButtonText;
      delete button.dataset.runId;
    }
  }
}

async function cancelProxyDetect() {
  try {
    if (typeof backend().CancelProxyDetect === "function") {
      await api.CancelProxyDetect();
    }
    showToast("已请求停止检测");
  } catch (e) {
    console.warn("停止代理检测请求失败:", e);
  }
}

function setProxyFilter(filter) {
  state.proxyFilter = filter;
  document.querySelectorAll("[data-proxy-filter]").forEach((btn) => {
    btn.classList.toggle("active", btn.dataset.proxyFilter === filter);
  });
  renderProxyResults(state.proxyRawItems, state.proxyLocation);
}

function getFilteredProxyItems() {
  const items = Array.isArray(state.proxyRawItems) ? state.proxyRawItems : [];
  if (state.proxyFilter === "ok") {
    return items.filter((item) => item.success);
  }
  if (state.proxyFilter === "fail") {
    return items.filter((item) => !item.success);
  }
  return items;
}

function copyProxyOkNodes() {
  const items = Array.isArray(state.proxyRawItems) ? state.proxyRawItems : [];
  const okItems = items.filter((item) => item.success);
  if (!okItems.length) return showToast("当前没有检测正常的节点");
  const text = okItems.map((item) => item.line || proxyEndpoint(item)).filter(Boolean).join("\n");
  copyText(text, `已复制 ${okItems.length} 个正常节点`);
}

async function exportProxyTxt() {
  const items = getFilteredProxyItems();
  if (!items.length) return showToast("当前没有可导出的结果");
  const content = items.map((item) => {
    const ep = proxyEndpoint(item);
    const tcp = Number.isFinite(item.tcpLatency) && item.tcpLatency >= 0 ? `${item.tcpLatency}ms` : "-";
    const url = Number.isFinite(item.urlLatency) && item.urlLatency >= 0 ? `${item.urlLatency}ms` : "-";
    const status = item.success ? "正常" : "失败";
    const exitIp = item.detectExitIp ? ` 出口IP:${item.detectExitIp}` : "";
    return `${ep}\t协议:${item.protocol || "-"}\tTCP:${tcp}\tURL:${url}\t状态:${status}\t国家:${item.country || "-"}\t运营商:${item.isp || "-"}${exitIp}\t说明:${item.message || "-"}`;
  }).join("\n");
  const timeStr = new Date().toISOString().slice(0, 10);
  try {
    const path = await api.SaveTextFile(`proxy-detect-${timeStr}.txt`, content);
    showToast(`已导出 TXT：${path}`);
  } catch (e) {
    if (!String(e).includes("取消")) showToast(`导出失败：${e?.message || e}`);
  }
}

async function exportProxyExcel() {
  const items = getFilteredProxyItems();
  if (!items.length) return showToast("当前没有可导出的结果");
  const header = ["节点", "协议", "TCP延迟(ms)", "URL延迟(ms)", "检测状态", "国家代码", "运营商", "真实出口IP", "说明信息"];
  const rows = items.map((item) => {
    const tcp = Number.isFinite(item.tcpLatency) && item.tcpLatency >= 0 ? item.tcpLatency : "";
    const url = Number.isFinite(item.urlLatency) && item.urlLatency >= 0 ? item.urlLatency : "";
    const status = item.success ? "正常" : "失败";
    return [
      proxyEndpoint(item),
      item.protocol || "",
      tcp,
      url,
      status,
      item.country || "",
      item.isp || "",
      item.detectExitIp || "",
      item.message || "",
    ];
  });
  const csvContent = "\uFEFF" + [header.join(","), ...rows.map(r => r.map(c => `"${String(c).replace(/"/g, '""')}"`).join(","))].join("\r\n");
  const timeStr = new Date().toISOString().slice(0, 10);
  try {
    const path = await api.SaveTextFile(`proxy-detect-${timeStr}.csv`, csvContent);
    showToast(`已导出 Excel/CSV 报表：${path}`);
  } catch (e) {
    if (!String(e).includes("取消")) showToast(`导出失败：${e?.message || e}`);
  }
}

function normalizeProxyDetectResponse(response) {
  const payload = response && typeof response === "object" && !Array.isArray(response) ? response : {};
  const items = Array.isArray(response)
    ? response
    : (Array.isArray(payload.results) ? payload.results : (Array.isArray(payload.items) ? payload.items : []));
  const first = items[0] && typeof items[0] === "object" ? items[0] : {};
  const responseLocation = payload.location ?? payload.detect_location ?? payload.detectLocation
    ?? first.detect_location ?? first.detectLocation;
  const target = payload.target ?? payload.detect_target ?? payload.detectTarget
    ?? responseLocation?.target ?? responseLocation?.alias
    ?? first.detect_target ?? first.detectTarget;
  const exitIP = payload.exit_ip ?? payload.exitIP ?? payload.detect_exit_ip ?? payload.detectExitIp ?? payload.detectExitIP
    ?? responseLocation?.exit_ip ?? responseLocation?.exitIP
    ?? first.exit_ip ?? first.exitIP ?? first.detect_exit_ip ?? first.detectExitIp ?? first.detectExitIP;
  return { items, location: { target, location: responseLocation, exitIP } };
}

function proxyLocationText(meta) {
  if (!meta || (!meta.target && !meta.location && !meta.exitIP)) return "检测位置：后端未返回";
  const target = String(meta.target || "").trim();
  const location = typeof meta.location === "object" && meta.location
    ? String(meta.location.label || meta.location.alias || meta.location.name || meta.location.host || "").trim()
    : String(meta.location || "").trim();
  const exitIP = String(meta.exitIP || "").trim();
  const parts = [];
  const normalizedTarget = target.toLowerCase() === "local" ? "本地" : target;
  if (location && target && location.toLowerCase().includes(target.toLowerCase())) {
    parts.push(location);
  } else {
    if (normalizedTarget) parts.push(normalizedTarget);
    if (location && location !== target && !(normalizedTarget === "本地" && /^(本机|本地)$/.test(location))) parts.push(location);
  }
  if (exitIP) parts.push(`出口 IP ${exitIP}`);
  return parts.length ? `检测位置：${parts.join(" · ")}` : "检测位置：后端未返回";
}

function proxyEndpoint(item) {
  const host = String(item?.host || "").trim();
  const port = Number(item?.port);
  if (!host || !Number.isInteger(port) || port < 1 || port > 65535) return "未解析";
  return `${host.includes(":") && !host.startsWith("[") ? `[${host}]` : host}:${port}`;
}

function renderProxyResults(results, location = null) {
  state.proxyRawItems = Array.isArray(results) ? results : [];
  if (location) state.proxyLocation = location;
  const allCount = state.proxyRawItems.length;
  const okCount = state.proxyRawItems.filter((item) => item.success).length;
  const failCount = allCount - okCount;
  setText("proxyFilterAll", `全部 (${allCount})`);
  setText("proxyFilterOk", `仅正常 (${okCount})`);
  setText("proxyFilterFail", `仅失败 (${failCount})`);

  if (!state.proxyRawItems.length) {
    setText("proxyRunLocation", state.proxyLocation ? proxyLocationText(state.proxyLocation) : "检测位置：未开始");
    el("proxyResultRows").innerHTML = `<tr><td colspan="7" class="muted center">暂无结果</td></tr>`;
    setText("proxySummaryText", "未开始");
    return;
  }

  setText("proxyRunLocation", proxyLocationText(state.proxyLocation));
  const filtered = getFilteredProxyItems();
  if (!filtered.length) {
    el("proxyResultRows").innerHTML = `<tr><td colspan="7" class="muted center">当前筛选条件下无结果</td></tr>`;
    setText("proxySummaryText", `完成：正常 ${okCount} 个，失败 ${failCount} 个`);
    return;
  }

  const rows = filtered.map((item) => {
    const ok = Boolean(item.success);
    const tcpLatency = Number.isFinite(item.tcpLatency) ? item.tcpLatency : item.latency;
    const urlLatency = Number.isFinite(item.urlLatency) ? item.urlLatency : -1;
    const verified = ok && urlLatency >= 0;
    const endpointOnly = !ok && Number.isFinite(tcpLatency) && tcpLatency >= 0 && String(item.message || "").includes("端口可达");
    const endpoint = proxyEndpoint(item);
    const exitIpTag = item.detectExitIp ? `<span class="badge" title="出口IP: ${escapeHtml(item.detectExitIp)}">${escapeHtml(item.detectExitIp)}</span> ` : "";
    return `<tr>
      <td class="node-cell" title="${escapeHtml(endpoint)}">${escapeHtml(endpoint)}</td>
      <td>${Number.isFinite(tcpLatency) && tcpLatency >= 0 ? `${escapeHtml(tcpLatency)} ms` : "-"}</td>
      <td>${urlLatency >= 0 ? `${escapeHtml(urlLatency)} ms` : "-"}</td>
      <td class="${ok ? "status-ok" : "status-fail"}">${ok ? (verified ? "代理正常" : "端点可达") : (endpointOnly ? "端点可达" : "失败")}</td>
      <td>${escapeHtml(item.country || "-")}</td>
      <td class="muted" title="${escapeHtml(item.isp || "-")}">${escapeHtml(item.isp || "-")}</td>
      <td class="muted" title="${escapeHtml(item.message || "-")}">${exitIpTag}${escapeHtml(item.message || "-")}</td>
    </tr>`;
  }).join("");
  el("proxyResultRows").innerHTML = rows;
  setText("proxySummaryText", `完成：正常 ${okCount} 个，失败 ${failCount} 个`);
}

async function deploySingbox(alias, button = null) {
  if (!await appConfirm(`确认部署 ${alias} 吗？\n会在远程服务器执行 sing-box 安装命令，并使用安装脚本当前默认的最新内核。`, "部署 sing-box")) return;
  if (button) {
    button.disabled = true;
    button.textContent = "部署中...";
  }
  showLogDetail("部署中", `正在部署 ${alias}，请等待...\n（此过程需要下载并安装 sing-box，耗时约 10~30 秒，请勿重复操作）`, "log.txt", false);
  try {
    const result = await api.DeploySingbox(alias);
    if (result.success) {
      closeDetail();
      showToast("部署成功");
      await refreshStatusAfterVpsChange(alias);
      return;
    }
    const log = [
      `VPS: ${alias}`,
      `结果: 部署失败`,
      `退出码: ${result.exitCode ?? ""}`,
      `消息: ${result.message || ""}`,
      "",
      "STDOUT:",
      result.stdout || "",
      "",
      "STDERR:",
      result.stderr || "",
    ].join("\n");
    showLogDetail("部署失败", log, `deploy-${alias}-${new Date().toISOString().replace(/[:.]/g, "-")}.txt`);
  } finally {
    if (button) {
      button.disabled = false;
      button.textContent = "部署";
    }
  }
}

async function controlSingbox(alias, action, button = null) {
  const actionLabel = action === "restart" ? "重启" : action;
  if (!await appConfirm(`确认对 ${alias} 执行 ${actionLabel} 吗？`, "sing-box 操作")) return;
  if (button) {
    button.disabled = true;
    button.textContent = `${actionLabel}中...`;
  }
  showToast(`正在向【${alias}】发送${actionLabel}指令 (SSH)...`);
  try {
    const result = await api.ControlSingbox(alias, action);
    showResult(result);
    if (result.success) await refreshStatusAfterVpsChange(alias);
  } finally {
    if (button) {
      button.disabled = false;
      button.textContent = actionLabel;
    }
  }
}

async function rebootVps(alias, button = null) {
  if (!await appConfirm(`确认重启 VPS ${alias} 吗？\n服务器会断开连接。`, "重启 VPS")) return;
  if (button) {
    button.disabled = true;
    button.textContent = "重启中...";
  }
  showToast(`正在向【${alias}】发送重启服务器指令...`);
  try {
    const result = await api.RebootVPS(alias);
    showResult(result);
    if (result.success) await refreshStatusAfterVpsChange(alias, { delayMs: 3000 });
  } finally {
    if (button) {
      button.disabled = false;
      button.textContent = "重启VPS";
    }
  }
}

async function syncAllJson(alias, button = null) {
  if (!await appConfirm(`确认从 ${alias} 拉取 SanTianShell 部署目录中的 all.json，并覆盖本地节点配置吗？\n其他 sing-box 配置不会导入。`, "同步配置")) return;
  if (button) {
    button.disabled = true;
    button.textContent = "同步中...";
  }
  showToast(`正在从【${alias}】同步 all.json (SSH)...`);
  try {
    const result = await api.SyncAllJSON(alias);
    showResult(result);
    if (result.success) await refreshStatusAfterVpsChange(alias);
  } finally {
    if (button) {
      button.disabled = false;
      button.textContent = "同步配置";
    }
  }
}

async function trafficRank(alias) {
  state.trafficAlias = alias;
  state.trafficLimit = 10;
  state.trafficMode = "realtime";
  state.trafficData = null;
  setText("trafficTitle", `${alias} 流量排行`);
  document.querySelectorAll(".segment").forEach((btn) => btn.classList.toggle("active", btn.dataset.limit === "10"));
  setTrafficMode("realtime", false);
  el("trafficModal").classList.remove("hidden");
  await refreshTrafficRank();
}

function closeTrafficModal() {
  state.trafficRequest += 1;
  el("trafficModal").classList.add("hidden");
}

function formatTrafficBytes(value) {
  let size = Number(value || 0);
  const units = ["B", "KB", "MB", "GB", "TB"];
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index += 1;
  }
  const fixed = size >= 10 || index === 0 ? 0 : 1;
  return `${size.toFixed(fixed)} ${units[index]}`;
}

function setTrafficMode(mode, render = true) {
  state.trafficMode = mode === "total" ? "total" : "realtime";
  document.querySelectorAll("#trafficModal .traffic-tabs .debug-tab").forEach((btn, index) => {
    btn.classList.toggle("active", state.trafficMode === (index === 1 ? "total" : "realtime"));
  });
  if (render) renderTrafficRows();
}

function renderTrafficRows() {
  const data = state.trafficData || {};
  const isTotal = state.trafficMode === "total";
  const rows = (isTotal ? data.total : data.realtime) || [];
  const limited = rows.slice(0, state.trafficLimit);
  const headers = isTotal
    ? ["排名", "端口", "节点名", "节点组", "协议", "下行总量", "上行总量", "合计", "连接数"]
    : ["排名", "端口", "节点名", "节点组", "协议", "实时下行", "实时上行", "合计", "连接数"];
  const thead = el("trafficHead");
  if (thead) thead.innerHTML = `<tr>${headers.map((item) => `<th>${item}</th>`).join("")}</tr>`;
  if (isTotal && data.total_available === false) {
    el("trafficRows").innerHTML = `<tr><td colspan="9" class="muted center">${escapeHtml(data.total_message || "端口总流量不可用")}</td></tr>`;
    return;
  }
  const body = limited.map((item, index) => {
    const rx = isTotal ? formatTrafficBytes(item.rx_total_bytes) : `${formatTrafficBytes(item.rx_bytes_per_second)}/s`;
    const tx = isTotal ? formatTrafficBytes(item.tx_total_bytes) : `${formatTrafficBytes(item.tx_bytes_per_second)}/s`;
    const total = isTotal ? formatTrafficBytes(item.total_bytes) : `${formatTrafficBytes(item.bytes_per_second)}/s`;
    return `<tr><td>${index + 1}</td><td class="code">${escapeHtml(item.port || "")}</td><td>${escapeHtml(item.node_name || "")}</td><td>${escapeHtml(item.group_name || "")}</td><td>${escapeHtml(item.protocol || "")}</td><td class="code">${rx}</td><td class="code">${tx}</td><td class="code">${total}</td><td class="code">${escapeHtml(item.connection_count || 0)}</td></tr>`;
  }).join("");
  el("trafficRows").innerHTML = body || `<tr><td colspan="9" class="muted center">暂无可显示数据</td></tr>`;
}

async function refreshTrafficRank() {
  const alias = state.trafficAlias;
  if (!alias) return;
  const request = ++state.trafficRequest;
  const button = el("refreshTrafficBtn");
  if (button) button.disabled = true;
  state.trafficData = null;
  setText("trafficStatus", "采集中...");
  el("trafficRows").innerHTML = `<tr><td colspan="9" class="muted center">采集中...</td></tr>`;
  try {
    const result = await api.GetTrafficRank(alias);
    if (request !== state.trafficRequest || alias !== state.trafficAlias) return;
    if (!result.success) {
      setText("trafficStatus", result.message || "获取失败");
      el("trafficRows").innerHTML = `<tr><td colspan="9" class="status-fail center">${escapeHtml(result.message || "获取失败")}</td></tr>`;
      return;
    }
    try {
      state.trafficData = JSON.parse(result.stdout || "{}");
    } catch {
      setText("trafficStatus", "流量数据解析失败");
      el("trafficRows").innerHTML = `<tr><td colspan="9" class="status-fail center">流量数据解析失败</td></tr>`;
      return;
    }
    renderTrafficRows();
    setText("trafficStatus", `采样完成，显示 Top${state.trafficLimit}`);
  } catch (error) {
    if (request !== state.trafficRequest || alias !== state.trafficAlias) return;
    const message = error?.message || String(error || "获取失败");
    setText("trafficStatus", message);
    el("trafficRows").innerHTML = `<tr><td colspan="9" class="status-fail center">${escapeHtml(message)}</td></tr>`;
  } finally {
    if (button && request === state.trafficRequest) button.disabled = false;
  }
}

function openNodes(alias) {
  el("nodeAliasText").value = alias || "";
  el("nodeAliasFilter").value = "";
  setView("nodes");
}
function openBackups(alias) {
  state.backupActionId += 1;
  state.backupTargetAlias = alias || "";
  clearBackupPreview();
  resetBackupProgress();
  renderBackupAliasOptions(alias);
  el("backupAliasFilter").value = alias || "";
  el("backupAliasFilter").disabled = false;
  if (alias) {
    setText("backupModalTitle", `备份还原 / 目标本机：${alias}`);
  } else {
    setText("backupModalTitle", "备份管理");
  }
  el("backupModal").classList.remove("hidden");
  loadBackups();
}
function closeBackupModal() {
  state.backupLoadId += 1;
  state.backupActionId += 1;
  state.backupTargetAlias = "";
  el("backupAliasFilter").disabled = false;
  clearBackupPreview();
  resetBackupProgress();
  renderBackupAliasOptions("");
  el("backupModal").classList.add("hidden");
}
async function runLimited(items, limit, worker) {
  const queue = items.slice();
  const workers = Array.from({ length: Math.min(limit, queue.length) }, async () => {
    while (queue.length) {
      const item = queue.shift();
      await worker(item);
    }
  });
  await Promise.all(workers);
}

async function refreshAllStatus(options = {}) {
  const reloadBtn = el("reloadBtn");
  if (reloadBtn?.disabled && reloadBtn.textContent === "刷新中...") return;
  if (reloadBtn && !options.silent) {
    reloadBtn.disabled = true;
    reloadBtn.textContent = "刷新中...";
  }
  try {
    const activeView = document.querySelector(".nav-item.active")?.dataset.view || "vps";
    if (activeView === "nodes") {
      await loadNodes(true);
      if (!options.silent) showToast("节点列表已刷新");
      return;
    }
    if (activeView === "deleted") {
      await loadDeleted();
      if (!options.silent) showToast("删除记录已刷新");
      return;
    }
    const list = Array.isArray(options.list) ? options.list : (state.overview?.vpsList || []);
    if (!list.length) {
      if (!options.silent) showToast("暂无 VPS");
      return;
    }
    if (!options.silent) showToast("正在刷新状态...");
    const cache = loadStatusCache();
    const runtimeStates = new Map();
    const syncRemote = false;
    let failedCount = 0;
    for (const item of list) {
      const alias = item.alias || item.id;
      setText(`status-${alias}`, "检查中...");
      const nodeStatus = el(`node-status-${alias}`);
      if (nodeStatus) nodeStatus.textContent = item.nodeCount ? "检测中" : "无节点";
    }
    await runLimited(list, options.concurrency || 2, async (item) => {
      if (options.yieldUI) await new Promise((resolve) => setTimeout(resolve, options.yieldDelayMs || 60));
      const alias = item.alias || item.id;
      let result;
      try {
        result = options.silent
          ? await api.RefreshVPSState(alias, syncRemote)
          : await api.RefreshVPSStateInteractive(alias, syncRemote);
        runtimeStates.set(alias, result);
        setText(`status-${alias}`, statusTextMap(result.status));
        const badge = el(`status-${alias}`);
        if (badge) badge.className = `badge ${singboxStatusClass(result.status)}`;
        if (result.status === "ssh_failed" || result.status === "error" || /失败/.test(result.message || "")) failedCount += 1;
      } catch (error) {
        result = { alias, status: "ssh_failed", nodeCount: Number(item.nodeCount || 0), startPort: Number(item.startPort || 0), synced: false, message: String(error) };
        runtimeStates.set(alias, result);
        failedCount += 1;
        setText(`status-${alias}`, "SSH失败");
        const badge = el(`status-${alias}`);
        if (badge) badge.className = "badge warning";
      }
      const nodeStatus = el(`node-status-${alias}`);
      const nodeText = runtimeNodeText(result);
      if (nodeStatus) {
        nodeStatus.textContent = nodeText;
        nodeStatus.className = `badge ${nodeStatusClass(nodeText)}`;
      }
      cache[alias] = {
        singboxStatus: result.status || "unknown",
        nodeText,
        checkedAt: new Date().toISOString(),
      };
    });
    saveStatusCache(cache);
    if (!options.silent) {
      try {
        await syncHostKeyTrust();
      } catch (error) {
        console.warn("刷新后同步 SSH 指纹信任状态失败", error);
      }
    }
    await refreshOverviewAfterChange();
    runtimeStates.forEach((result, alias) => applyRuntimeOverview(alias, result));
    saveOverviewCache(state.overview);
    renderVPS();
    notifyNodeDataChanged();
    if (!options.silent) showToast(failedCount ? `状态刷新完成，${failedCount} 台连接或检测失败` : "状态刷新完成");
  } finally {
    if (reloadBtn) {
      reloadBtn.disabled = false;
      reloadBtn.textContent = "刷新状态";
    }
  }
}

async function refreshStatusAfterVpsChange(alias, options = {}) {
  alias = String(alias || "").trim();
  if (!alias) return;
  if (options.delayMs) await new Promise((resolve) => setTimeout(resolve, options.delayMs));
  const item = state.overview?.vpsList?.find((v) => (v.alias || v.id) === alias);
  setText(`status-${alias}`, "检查中...");
  const nodeStatus = el(`node-status-${alias}`);
  if (nodeStatus) nodeStatus.textContent = item?.nodeCount ? "检测中" : "无节点";
  let result;
  try {
    result = await api.RefreshVPSState(alias, Boolean(options.syncRemote));
    setText(`status-${alias}`, statusTextMap(result.status));
    const badge = el(`status-${alias}`);
    if (badge) badge.className = `badge ${singboxStatusClass(result.status)}`;
  } catch (error) {
    result = { alias, status: "ssh_failed", nodeCount: Number(item?.nodeCount || 0), startPort: Number(item?.startPort || 0), synced: false, message: String(error) };
    setText(`status-${alias}`, "SSH失败");
    const badge = el(`status-${alias}`);
    if (badge) badge.className = "badge warning";
  }
  const nodeText = runtimeNodeText(result);
  if (nodeStatus) {
    nodeStatus.textContent = nodeText;
    nodeStatus.className = `badge ${nodeStatusClass(nodeText)}`;
  }
  applyStatusCache(alias, result.status || "unknown", nodeText);
  try {
    await refreshOverviewAfterChange(alias, result);
  } finally {
    notifyNodeDataChanged(alias, result);
  }
  return result;
}

function defaultNodeGroupName() {
  return "";
}

function nodeOutboundPlaceholder() {
  if (el("nodeOutboundProtocol")?.value === "anytls") return "例如:\nanytls.example.com:443:示例密码\n192.0.2.1:443:示例密码:sni.example.com";
  return "例如:\nproxy.example.com:1080\nauth.example.com:8080:示例用户:示例密码\n192.0.2.1:10002:示例用户:示例密码";
}

function refreshInboundProtocolUI() {
  const value = el("nodeInboundProtocol")?.value || "vless-reality";
  const isReality = value === "vless-reality";
  const realityInput = el("nodeRealityEnabled");
  const sniLabel = el("nodeRealitySni")?.closest("label");
  if (realityInput) {
    realityInput.checked = isReality;
    realityInput.disabled = true;
    realityInput.closest("label")?.classList.add("hidden");
  }
  if (sniLabel) sniLabel.classList.toggle("hidden", !isReality);
}

function refreshMultiIpProtocolUI() {
  const value = el("multiIpInboundProtocol")?.value || "vless-reality";
  const sniLabel = el("multiIpRealitySni")?.closest("label");
  if (sniLabel) sniLabel.classList.toggle("hidden", value !== "vless-reality");
}

function countOutboundLines(text) {
  return String(text || "").split(/\r?\n/).map((line) => line.trim()).filter(Boolean).length;
}

function looksLikeOutboundLine(line) {
  const text = String(line || "").trim();
  if (!text) return false;
  if (text.includes(":")) {
    const parts = text.split(":");
    return parts.length >= 2 && parts[0].trim() && parts[1].trim();
  }
  const parts = text.split(/\s+/);
  return parts.length >= 2 && /^\d+$/.test(parts[1]);
}

function collectBatchNodeGroups(text) {
  const groups = [];
  let currentGroup = "";
  let lines = [];
  const append = () => {
    if (!currentGroup) return;
    groups.push({ groupName: currentGroup, outboundLines: lines.join("\n") });
  };
  String(text || "").split(/\r?\n/).forEach((raw) => {
    const line = raw.trim();
    if (!line) return;
    if (looksLikeOutboundLine(line)) {
      if (!currentGroup) {
        groups.push({ groupName: "", outboundLines: line });
        return;
      }
      lines.push(line);
      return;
    }
    append();
    currentGroup = line;
    lines = [];
  });
  append();
  return groups;
}

function collectAddNodeGroups() {
  if (el("nodeAddMode")?.value === "batch") return collectBatchNodeGroups(el("nodeBatchGroupsText")?.value || "");
  return Array.from(document.querySelectorAll(".node-group-card")).map((card) => ({
    groupName: card.querySelector(".node-group-name-input")?.value.trim() || "",
    outboundLines: card.querySelector(".node-group-outbound-input")?.value || "",
  }));
}

function requestedNodeCount() {
  const groups = collectAddNodeGroups();
  const needsOutbound = !["direct", "block", "dns"].includes(el("nodeOutboundProtocol")?.value || "socks");
  let total = 0;
  groups.forEach((group) => {
    total += needsOutbound ? Math.max(1, countOutboundLines(group.outboundLines)) : 1;
  });
  return Math.max(1, total || 1);
}

async function refreshNodeStartPort(force = false) {
  const alias = state.nodeTargetAlias || el("nodeVpsAlias")?.value || "";
  const input = el("nodeStartPort");
  if (!input || !alias) return;
  if (!force && input.value && input.dataset.autoValue !== input.value) return;
  try {
    const defaults = await api.GetNodeDefaults(alias, requestedNodeCount());
    input.value = defaults.default_start_port || 20000;
    input.dataset.autoValue = String(input.value);
    input.placeholder = `例如: ${input.value}`;
  } catch {
    input.value = input.value || "20000";
    input.placeholder = "例如: 20000";
  }
}

function refreshAddNodeMode() {
  const isBatch = el("nodeAddMode")?.value === "batch";
  el("nodeGroupsContainer")?.classList.toggle("hidden", isBatch);
  el("addNodeGroupBtn")?.classList.toggle("hidden", isBatch);
  el("nodeBatchSection")?.classList.toggle("hidden", !isBatch);
  refreshNodeStartPort();
}

function refreshNodeGroupCards() {
  const cards = Array.from(document.querySelectorAll(".node-group-card"));
  cards.forEach((card, index) => {
    const badge = card.querySelector(".node-group-badge");
    const remove = card.querySelector(".remove-node-group-btn");
    if (badge) badge.textContent = `第 ${index + 1} 组`;
    if (remove) remove.classList.toggle("hidden", cards.length <= 1);
  });
}

function addNodeGroupCard(groupName = "") {
  const box = el("nodeGroupsContainer");
  if (!box) return;
  const card = document.createElement("div");
  card.className = "node-group-card";
  card.innerHTML = `
    <div class="node-group-card-head">
      <span class="badge node-group-badge">第 1 组</span>
      <button type="button" class="btn small danger-soft remove-node-group-btn">删除本组</button>
    </div>
    <label>节点组名<input class="node-group-name-input" placeholder="可空，例如 8G" value="${escapeHtml(groupName)}" /></label>
    <label>落地信息<textarea class="node-group-outbound-input" rows="4" placeholder="${escapeHtml(nodeOutboundPlaceholder())}"></textarea></label>
  `;
  box.appendChild(card);
  card.querySelector(".remove-node-group-btn").addEventListener("click", () => {
    card.remove();
    refreshNodeGroupCards();
    refreshNodeStartPort();
  });
  card.querySelector(".node-group-outbound-input").addEventListener("input", () => refreshNodeStartPort());
  refreshNodeGroupCards();
}

function resetNodeGroups(alias = "") {
  const box = el("nodeGroupsContainer");
  if (!box) return;
  box.innerHTML = "";
  addNodeGroupCard(defaultNodeGroupName());
}

function setNodeSaveProgress(percent, text = "") {
  const box = el("nodeSaveProgress");
  if (!box) return;
  const value = Math.max(0, Math.min(100, Number(percent) || 0));
  box.classList.remove("hidden");
  box.scrollIntoView({ behavior: "smooth", block: "nearest" });
  if (el("nodeSaveProgressBar")) el("nodeSaveProgressBar").style.width = `${value}%`;
  setText("nodeSaveProgressPercent", `${Math.round(value)}%`);
  if (text) setText("nodeSaveProgressText", text);
}

function resetNodeSaveProgress() {
  const box = el("nodeSaveProgress");
  if (box) box.classList.add("hidden");
  if (el("nodeSaveProgressBar")) el("nodeSaveProgressBar").style.width = "0%";
  setText("nodeSaveProgressPercent", "0%");
  setText("nodeSaveProgressText", "准备保存");
  setText("nodeSaveResult", "请等待生成");
  el("viewNodeSaveResultBtn")?.classList.add("hidden");
}

async function refreshOverviewAfterChange(alias = "", runtimeState = null) {
  state.overview = await api.GetOverview();
  applyRuntimeOverview(alias, runtimeState);
  state.overviewReady = true;
  saveOverviewCache(state.overview);
  rebuildVpsIndex();
  setText("versionText", `版本 ${state.overview.appInfo.version}`);
  setText("dataPath", `数据目录：${state.overview.appInfo.dataDir}`);
  setText("vpsCount", state.overview.stats.vpsCount);
  setText("nodeCount", state.overview.stats.nodeCount);
  setText("backupCount", state.overview.stats.backupCount);
  renderVPS();
  fillAliasFilters();
}

async function refreshNodesAfterChange() {
  await refreshOverviewAfterChange();
  state.nodePage = 1;
  await loadNodes(true);
}

async function showNodesForAlias(alias) {
  if (el("nodeAliasText")) el("nodeAliasText").value = alias || "";
  if (el("nodeAliasFilter")) el("nodeAliasFilter").value = "";
  if (el("nodeVpsIpSearch")) el("nodeVpsIpSearch").value = "";
  if (el("nodeGroupSearch")) el("nodeGroupSearch").value = "";
  if (el("nodeSearch")) el("nodeSearch").value = "";
  if (el("nodePortSearch")) el("nodePortSearch").value = "";
  state.nodePage = 1;
  setView("nodes", { skipNodeLoad: true });
  await loadNodes(true);
}

function refreshAddNodeVpsInfo() {
  const alias = state.nodeTargetAlias || "";
  const vps = vpsByAlias(alias);
  if (el("nodeVpsAlias")) el("nodeVpsAlias").value = alias;
  if (el("nodeVpsAliasDisplay")) el("nodeVpsAliasDisplay").value = alias;
  if (el("nodeVpsAddress")) el("nodeVpsAddress").value = vps.domain || vps.address || "";
  setText("nodeModalTitle", alias ? `新增节点 - VPS: ${alias}` : "新增节点");
}

async function openAddNode(alias = "") {
  const targetAlias = String(alias || "").trim();
  if (!targetAlias) return showToast("未指定目标 VPS");
  state.nodeTargetAlias = targetAlias;
  if (!state.vpsIndex.has(targetAlias)) {
    try {
      await loadOverview();
    } catch (error) {
      state.nodeTargetAlias = "";
      return showToast(frontendErrorMessage(error, "目标 VPS 加载失败"));
    }
  }
  if (!state.vpsIndex.has(targetAlias)) {
    state.nodeTargetAlias = "";
    return showToast(`未找到目标 VPS：${targetAlias}`);
  }
  resetNodeSaveProgress();
  refreshAddNodeVpsInfo();
  resetNodeGroups(targetAlias);
  if (el("nodeAddMode")) el("nodeAddMode").value = "normal";
  if (el("nodeStartPort")) {
    el("nodeStartPort").value = "20000";
    el("nodeStartPort").dataset.autoValue = "20000";
  }
  refreshAddNodeMode();
  refreshInboundProtocolUI();
  el("nodeModal").classList.remove("hidden");
  refreshNodeStartPort(true);
}
function closeNodeModal() {
  el("nodeModal").classList.add("hidden");
  state.nodeTargetAlias = "";
}

function setMultiIpProgress(percent, text = "") {
  const box = el("multiIpProgress");
  if (!box) return;
  const value = Math.max(0, Math.min(100, Number(percent) || 0));
  box.classList.remove("hidden");
  box.scrollIntoView({ behavior: "smooth", block: "nearest" });
  if (el("multiIpProgressBar")) el("multiIpProgressBar").style.width = `${value}%`;
  setText("multiIpProgressPercent", `${Math.round(value)}%`);
  if (text) setText("multiIpProgressText", text);
}

function resetMultiIpProgress() {
  el("multiIpProgress")?.classList.add("hidden");
  if (el("multiIpProgressBar")) el("multiIpProgressBar").style.width = "0%";
  setText("multiIpProgressPercent", "0%");
  setText("multiIpProgressText", "准备执行");
  setText("multiIpResult", "请等待生成");
  el("viewMultiIpResultBtn")?.classList.add("hidden");
}

async function viewGeneratedNodes(alias) {
  if (shellActionHost) {
    window.parent.postMessage({ type: "node-view-list", alias }, "*");
    return;
  }
  closeNodeModal();
  closeMultiIpNodeModal();
  await showNodesForAlias(alias);
}

async function openAddMultiIPNode(alias = "") {
  el("multiIpAlias").value = alias;
  el("multiIpGroupName").value = "";
  el("multiIpStartPort").value = "20000";
  el("multiIpInboundProtocol").value = "vless-reality";
  refreshMultiIpProtocolUI();
  el("multiIpList").value = "";
  setText("multiIpSummary", "正在检测服务器绑定 IP，并过滤已创建协议的 IP...");
  resetMultiIpProgress();
  el("multiIpNodeModal").classList.remove("hidden");
  try {
    const result = await api.DetectVPSIPs(alias);
    el("multiIpList").value = (result.ips || []).join("\n");
    if (result.default_start_port && el("multiIpStartPort")) el("multiIpStartPort").value = result.default_start_port;
    const total = result.total_count ?? result.count ?? 0;
    const created = result.created_ip_count ?? 0;
    setText("multiIpSummary", `检测到 ${total} 个绑定 IP，已创建 ${created} 个，待创建 ${result.count || 0} 个；端口从 ${result.default_start_port || el("multiIpStartPort").value || 20000} 开始。`);
  } catch (error) {
    setText("multiIpSummary", `检测失败：${error?.message || error}`);
    showToast("多IP检测失败");
  }
}

function closeMultiIpNodeModal() { el("multiIpNodeModal").classList.add("hidden"); }

async function saveMultiIPNode() {
  const alias = el("multiIpAlias").value;
  if (!alias) return showToast("VPS 不能为空");
  const ips = (el("multiIpList").value || "")
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean);
  if (!ips.length) return showToast("没有未创建协议的 IP");
  const button = el("saveMultiIpNodeBtn");
  const cancelButton = el("cancelMultiIpNodeModalBtn");
  if (button?.disabled) return;
  try {
    if (button) {
      button.disabled = true;
      button.textContent = "生成中...";
    }
    if (cancelButton) cancelButton.disabled = true;
    setMultiIpProgress(10, "读取当前 IP 列表");
    const result = await api.AddMultiIPNodes({
      vpsAlias: alias,
      groupName: el("multiIpGroupName").value.trim(),
      inboundProtocol: el("multiIpInboundProtocol").value,
      startPort: Number(el("multiIpStartPort").value || 20000),
      realitySni: el("multiIpRealitySni").value.trim(),
      ips,
    });
    setMultiIpProgress(45, "本地多IP节点已生成");
    if (!result.success) {
      setMultiIpProgress(100, result.message || "生成失败");
      showToast(result.message || "生成失败");
      return;
    }
    setMultiIpProgress(65, "正在发布到 VPS");
    let publish;
    try {
      publish = await api.UploadAllJSON(alias);
    } catch (err) {
      publish = { success: false, message: err?.message || String(err) };
    }
    if (!publish.success) {
      if (result.items?.length && api.RollbackLocalNodes) {
        try {
          await api.RollbackLocalNodes(alias, result.items);
        } catch (_) {}
      }
      setMultiIpProgress(100, "发布失败");
      showDetail("发布失败", publish);
      showToast("发布到 VPS 失败，未保存至本地");
      return;
    }
    setMultiIpProgress(88, "检查远端状态并同步");
    await refreshStatusAfterVpsChange(alias);
    setMultiIpProgress(95, "刷新节点列表");
    await refreshNodesAfterChange();
    setMultiIpProgress(100, "完成");
    setText("multiIpResult", `新增多IP节点结果：已创建 ${result.count || 0} 个节点`);
    el("viewMultiIpResultBtn").dataset.alias = alias;
    el("viewMultiIpResultBtn").classList.remove("hidden");
    finishAddNode(alias, result.count, true);
  } finally {
    if (button) {
      button.disabled = false;
      button.textContent = "生成并发布";
    }
    if (cancelButton) cancelButton.disabled = false;
  }
}

async function saveNode() {
  const button = el("saveNodeBtn");
  const cancelButton = el("cancelNodeModalBtn");
  if (button?.disabled) return;
  const groups = collectAddNodeGroups();
  if (!groups.length) return showToast("请先添加节点组");
  const alias = state.nodeTargetAlias;
  if (!alias) return showToast("目标 VPS 已失效，请关闭后重新进入");
  let startPort = Number(el("nodeStartPort").value || 0);
  if (!startPort) startPort = 20000;
  const seen = new Set();
  const batchGroups = [];
  for (const group of groups) {
    const groupName = group.groupName;
    const outboundLines = group.outboundLines;
    const outboundProtocol = el("nodeOutboundProtocol").value;
    const needsOutbound = !["direct", "block", "dns"].includes(outboundProtocol);
    if (needsOutbound && !outboundLines.trim()) return showToast(`第 ${batchGroups.length + 1} 组缺少落地信息`);
    const seenKey = `${groupName.toLowerCase()}|||${outboundLines.trim().toLowerCase()}`;
    if (seen.has(seenKey)) return showToast(`本次提交存在重复节点组：${groupName || "空"}`);
    seen.add(seenKey);
    const count = needsOutbound ? Math.max(1, countOutboundLines(outboundLines)) : 1;
    batchGroups.push({ groupName, outboundLines, count });
  }
  const inboundValue = el("nodeInboundProtocol").value;
  const outboundProtocol = el("nodeOutboundProtocol").value;
  try {
    if (button) {
      button.disabled = true;
      button.textContent = "保存中...";
    }
    if (cancelButton) cancelButton.disabled = true;
    setNodeSaveProgress(8, "检查输入参数");
    const result = await api.BatchAddNodes({
      vpsAlias: alias,
      inboundProtocol: inboundValue === "vless-reality" ? "vless" : inboundValue,
      outboundProtocol,
      startPort,
      tlsEnabled: true,
      realityEnabled: inboundValue === "vless-reality",
      realitySni: el("nodeRealitySni").value.trim(),
      groups: batchGroups,
    });
    setNodeSaveProgress(42, "本地节点已生成");
    if (!result.success) {
      setNodeSaveProgress(100, result.message || "节点添加失败");
      showToast(result.message || "节点添加失败");
      return;
    }
    setNodeSaveProgress(62, "正在发布到 VPS");
    let publish;
    try {
      publish = await api.UploadAllJSON(alias);
    } catch (err) {
      publish = { success: false, message: err?.message || String(err) };
    }
    if (!publish.success) {
      if (result.items?.length && api.RollbackLocalNodes) {
        try {
          await api.RollbackLocalNodes(alias, result.items);
        } catch (_) {}
      }
      setNodeSaveProgress(100, "发布失败");
      showDetail("发布失败", publish);
      showToast("发布到 VPS 失败，未保存至本地");
      return;
    }
    setNodeSaveProgress(86, "检查远端状态并同步");
    await refreshStatusAfterVpsChange(alias);
    setNodeSaveProgress(95, "刷新节点列表");
    await refreshNodesAfterChange();
    setNodeSaveProgress(100, "完成");
    setText("nodeSaveResult", `新增节点结果：已创建 ${result.count || 0} 个节点`);
    el("viewNodeSaveResultBtn").dataset.alias = alias;
    el("viewNodeSaveResultBtn").classList.remove("hidden");
    finishAddNode(alias, result.count);
  } finally {
    if (button) {
      button.disabled = false;
      button.textContent = "保存节点";
    }
    if (cancelButton) cancelButton.disabled = false;
  }
}

async function deleteNode(alias, filename) {
  if (!await appConfirm(`确认删除节点 ${filename} 吗？`, "删除节点")) return;
  const result = await api.DeleteNode(alias, filename);
  if (!result.success) {
    showToast(result.message || "节点删除失败");
    return;
  }
  const publish = await api.UploadAllJSONAndDeleteRemoteNodes(alias, result.items || [filename]);
  if (!publish.success) {
    if (await appConfirm(`发布到 VPS 失败：${publish.message}\n\n该机器可能已重装、失效或无法连接。\n是否直接强制仅从本地移除此节点？`, "强制移除本地节点")) {
      showToast("已强制从本地移除节点");
      await refreshNodesAfterChange();
      setView("nodes");
      await refreshStatusAfterVpsChange(alias);
      return;
    }
    await rollbackDeletedNodes(alias, result.items || [filename]);
    await refreshNodesAfterChange();
    showDetail("发布失败", publish);
    showToast("发布到 VPS 失败，已恢复本地节点");
    return;
  }
  showToast(result.message);
  await refreshNodesAfterChange();
  setView("nodes");
  await refreshStatusAfterVpsChange(alias);
}

async function rollbackDeletedNodes(alias, filenames) {
  const items = Array.from(new Set((filenames || []).filter(Boolean)));
  if (!items.length) return { success: true, count: 0, items: [] };
  try {
    const result = await api.RollbackDeletedNodes(alias, items);
    if (!result.success) showDetail("本地恢复失败", result);
    return result;
  } catch (err) {
    const message = String(err?.message || err || "本地恢复失败");
    showDetail("本地恢复失败", message);
    return { success: false, message };
  }
}

async function queryOutboundByKey(key) {
  const node = findNodeByKey(key);
  if (!node) return showToast("节点不存在");
  state.nodeOutboundResults.set(key, { success: false, message: "查询中..." });
  renderNodes();
  const result = await api.QueryOutboundIP(node.vpsAlias, node.fileName);
  state.nodeOutboundResults.set(key, result);
  renderNodes();
}

async function queryVisibleOutbounds() {
  const visible = Array.from(document.querySelectorAll(".node-select"))
    .map((input) => findNodeByKey(input.dataset.key))
    .filter(Boolean);
  if (!visible.length) return showToast("当前页没有节点");
  const button = el("queryAllOutboundBtn");
  if (button) button.disabled = true;
  let done = 0;
  try {
    for (const node of visible) {
      const key = nodeKey(node);
      state.nodeOutboundResults.set(key, { success: false, message: "查询中..." });
      const result = await api.QueryOutboundIP(node.vpsAlias, node.fileName);
      state.nodeOutboundResults.set(key, result);
      done += 1;
      if (button) button.textContent = `查询中 ${done}/${visible.length}`;
      renderNodes();
    }
    showToast(`出站查询完成：${done} 个`);
  } finally {
    if (button) {
      button.disabled = false;
      button.textContent = "查询当页出站";
    }
  }
}

async function showNodeDetail(key) {
  const node = findNodeByKey(key);
  if (!node) return;
  try {
    const content = await api.ReadNodeJSON(node.vpsAlias, node.fileName);
    showEditableJson(`节点 JSON：${node.tag || node.fileName}`, content, { alias: node.vpsAlias, filename: node.fileName });
  } catch (error) {
    showDetail("节点详情", node);
    showToast(error?.message || "读取节点 JSON 失败");
  }
}

async function openVpsJSON(alias) {
  if (!alias) return showToast("VPS 为空");
  try {
    const content = await api.ReadAllJSON(alias);
    showEditableJson(`编辑 all.json：${alias}`, content, { type: "all", alias });
  } catch (error) {
    showToast(error?.message || "读取 all.json 失败");
  }
}

function copyNodeLink(key) {
  const node = findNodeByKey(key);
  if (node) copyText(nodeLink(node), "节点链接已复制");
}

function copyGroupLinks(alias, group) {
  const matching = state.nodes.filter((node) => node.vpsAlias === alias && groupFromNode(node) === group);
  const links = matching
    .map(nodeLink)
    .filter(Boolean)
    .join("\n");
  copyText(links, `同组链接已复制（共 ${matching.length} 条）`);
}

async function deleteNodeGroup(alias, group) {
  const matching = state.nodes.filter((node) => node.vpsAlias === alias && groupFromNode(node) === group);
  const countDesc = matching.length > 0 ? `（共 ${matching.length} 个节点）` : "";
  if (!await appConfirm(`确认删除 ${alias} 的节点组 “${group}”${countDesc} 吗？\n删除后将同步清理远程节点配置。`, "删除节点组")) return;
  const result = await api.DeleteNodeGroup(alias, group);
  if (!result.success) {
    showToast(result.message || "节点组删除失败");
    return;
  }
  const publish = await api.UploadAllJSONAndDeleteRemoteNodes(alias, result.items || []);
  if (!publish.success) {
    if (await appConfirm(`发布到 VPS 失败：${publish.message}\n\n该机器可能已重装、失效或无法连接。\n是否直接强制仅从本地移除此节点组？`, "强制移除本地节点组")) {
      showToast(`已强制从本地移除节点组 “${group}”`);
      await refreshNodesAfterChange();
      setView("nodes");
      await refreshStatusAfterVpsChange(alias);
      return;
    }
    await rollbackDeletedNodes(alias, result.items || []);
    await refreshNodesAfterChange();
    showDetail("发布失败", publish);
    showToast("发布到 VPS 失败，已恢复本地节点组");
    return;
  }
  const deletedCount = result.count || result.items?.length || matching.length;
  showToast(result.message || `节点组 “${group}” 已删除，共清理 ${deletedCount} 个节点`);
  await refreshNodesAfterChange();
  setView("nodes");
  await refreshStatusAfterVpsChange(alias);
}

function downloadGroup(alias, group) {
  const matching = state.nodes.filter((node) => node.vpsAlias === alias && groupFromNode(node) === group);
  const content = matching
    .map(nodeLink)
    .filter(Boolean)
    .join("\n");
  if (!content) return showToast("该组没有可下载的节点链接");
  const blob = new Blob([content], { type: "text/plain;charset=utf-8" });
  const link = document.createElement("a");
  link.href = URL.createObjectURL(blob);
  link.download = `${alias}_${group}_${matching.length}个节点.txt`;
  link.click();
  URL.revokeObjectURL(link.href);
  showToast(`已下载同组链接（共 ${matching.length} 条）`);
}

function downloadBase64File(file) {
  const bytes = Uint8Array.from(atob(file.base64 || ""), (char) => char.charCodeAt(0));
  const blob = new Blob([bytes], { type: file.mimeType || "application/octet-stream" });
  const link = document.createElement("a");
  link.href = URL.createObjectURL(blob);
  link.download = file.fileName || "export.xlsx";
  link.click();
  URL.revokeObjectURL(link.href);
}

async function saveExportFile(file) {
  if (!file?.base64) return "";
  if (typeof api.SaveExportFile === "function") {
    try {
      const path = await api.SaveExportFile(file);
      if (path) showToast(`已导出：${path}`);
      return path || "";
    } catch (error) {
      const message = error?.message || String(error || "导出失败");
      showToast(message.includes("取消") ? "已取消导出" : `导出失败：${message}`);
      return "";
    }
  }
  downloadBase64File(file);
  showToast(`已导出：${file.fileName || "export.xlsx"}`);
  return file.fileName || "";
}

async function chooseExportMode(node) {
  return new Promise((resolve) => {
    const modal = el("exportModeModal");
    modal?.classList.remove("hidden");
    const finish = (value) => {
      if (modal) modal.classList.add("hidden");
      resolve(value);
    };
    if (el("exportSingleNodeBtn")) el("exportSingleNodeBtn").onclick = () => finish("single");
    if (el("exportBatchNodeBtn")) el("exportBatchNodeBtn").onclick = () => finish("batch");
    if (modal) modal.onclick = (event) => {
      if (event.target === modal) finish("");
    };
  });
}

async function downloadNodeDoc(key) {
  const node = findNodeByKey(key);
  if (!node) return showToast("节点不存在");
  if (state.exportingNodeDocs.has(key)) return;
  const mode = await chooseExportMode(node);
  if (!mode) return;
  state.exportingNodeDocs.add(key);
  renderNodes();
  try {
    if (typeof backend().ExportNodeExcelToFile === "function") {
      const path = await api.ExportNodeExcelToFile(node.vpsAlias, node.fileName, mode);
      if (path) showToast(`已导出：${path}`);
      return;
    }
    const file = await api.ExportNodeExcel(node.vpsAlias, node.fileName, mode);
    await saveExportFile(file);
  } catch (error) {
    const message = error?.message || String(error || "导出失败");
    showToast(message.includes("取消") ? "已取消导出" : `导出失败：${message}`);
  } finally {
    state.exportingNodeDocs.delete(key);
    renderNodes();
  }
}

async function showNodeQR(key) {
  const node = findNodeByKey(key);
  if (!node) return showToast("节点不存在");
  try {
    const qr = await api.GetNodeQRCode(node.vpsAlias, node.fileName);
    el("qrImage").src = qr.image || "";
    el("qrLink").value = qr.link || "";
    setText("qrNodeName", `${node.vpsAlias} · ${node.tag || node.fileName}`);
    el("qrModal").classList.remove("hidden");
  } catch (error) {
    showToast(frontendErrorMessage(error, "二维码生成失败"));
  }
}

function closeQrModal() {
  el("qrModal").classList.add("hidden");
  el("qrImage").removeAttribute("src");
  el("qrLink").value = "";
}

async function copyQrLink() {
  await copyText(el("qrLink").value, "节点链接已复制");
}

async function batchDeleteSelectedNodes() {
  const groups = new Map();
  state.nodeSelected.forEach((key) => {
    const node = findNodeByKey(key);
    if (!node) return;
    if (!groups.has(node.vpsAlias)) groups.set(node.vpsAlias, []);
    groups.get(node.vpsAlias).push(node.fileName);
  });
  if (!groups.size) return;
  const selectedCount = state.nodeSelected.size;
  if (!await appConfirm(`确认批量删除 ${selectedCount} 个节点吗？`, "批量删除节点")) return;

  const button = el("batchDeleteNodesBtn");
  if (button) {
    button.disabled = true;
    button.textContent = "删除中...";
  }
  try {
    const entries = Array.from(groups.entries());
    const localResults = await Promise.all(entries.map(async ([alias, files]) => {
      const result = await api.BatchDeleteNodes(alias, files);
      if (!result.success) throw new Error(result.message || `${alias} 删除失败`);
      return { alias, files, result };
    }));

    let deletedCount = 0;
    localResults.forEach(({ alias, result }) => {
      deletedCount += result.count || 0;
    });
    showToast(`正在发布删除结果到 VPS，共 ${deletedCount} 个节点...`);

    const publishResults = await Promise.all(localResults.map(async ({ alias, result }) => ({ alias, publish: await api.UploadAllJSONAndDeleteRemoteNodes(alias, result.items || []) })));
    const succeeded = publishResults.filter((item) => item.publish.success);
    const failed = publishResults.filter((item) => !item.publish.success);
    if (failed.length) {
      const failedAliases = failed.map((item) => item.alias).join("、");
      const confirmForce = await appConfirm(
        `部分 VPS（${failedAliases}）发布失败。\n服务器可能已重装、失效或无法连接。\n\n是否直接强制保留本地删除结果（不回滚）？`,
        "强制移除本地节点"
      );
      if (!confirmForce) {
        await Promise.all(failed.map((item) => {
          const local = localResults.find((result) => result.alias === item.alias);
          return rollbackDeletedNodes(item.alias, local?.result?.items || []);
        }));
        await refreshNodesAfterChange();
        showDetail("发布失败", failed.map((item) => ({ alias: item.alias, result: item.publish })));
        showToast(`${failed.length} 台 VPS 发布失败，已恢复对应本地节点`);
        return;
      }
      showToast(`已强制保留本地删除结果，共清理 ${deletedCount} 个节点（成功发布 ${succeeded.length}/${publishResults.length} 台 VPS）`);
    } else {
      showToast(`批量删除完成，已发布 ${publishResults.length} 台 VPS`);
    }
    state.nodeSelected.clear();
    await refreshNodesAfterChange();
    for (const item of succeeded) {
      await refreshStatusAfterVpsChange(item.alias);
    }
  } catch (err) {
    showToast(String(err?.message || err || "批量删除失败"));
    await refreshNodesAfterChange();
  } finally {
    if (button) {
      button.textContent = "批量删除";
      updateNodeSelectionState();
    }
  }
}

async function batchForceDeleteSelectedNodes() {
  const groups = new Map();
  state.nodeSelected.forEach((key) => {
    const node = findNodeByKey(key);
    if (!node) return;
    if (!groups.has(node.vpsAlias)) groups.set(node.vpsAlias, []);
    groups.get(node.vpsAlias).push(node.fileName);
  });
  if (!groups.size) return showToast("请先勾选需要强制移除的节点");

  const selectedCount = state.nodeSelected.size;
  const aliasList = Array.from(groups.keys()).join("、");
  const confirmed = await appConfirm(
    `确认强制移除选中的 ${selectedCount} 个本地节点吗？（涉及 VPS：${aliasList}）\n\n` +
    `⚡ 注意：此操作直接清理本地数据，【完全不连接服务器发布】，专用于机器已重装、失效、销毁或无法连接的节点。\n\n` +
    `确定直接移除？`,
    "强制移除本地节点"
  );
  if (!confirmed) return;

  const button = el("batchForceDeleteNodesBtn");
  const normalBtn = el("batchDeleteNodesBtn");
  if (button) {
    button.disabled = true;
    button.textContent = "移除中...";
  }
  if (normalBtn) normalBtn.disabled = true;

  try {
    const entries = Array.from(groups.entries());
    let deletedCount = 0;
    for (const [alias, files] of entries) {
      const result = await api.ForceDeleteNodes(alias, files);
      if (result.success) {
        deletedCount += result.count || files.length;
      }
    }

    state.nodeSelected.clear();
    showToast(`强制移除完成：已直接清理本地 ${deletedCount} 个节点（未发布远程）`);
    await refreshNodesAfterChange();
    setView("nodes");
  } catch (err) {
    showToast(String(err?.message || err || "强制移除失败"));
    await refreshNodesAfterChange();
  } finally {
    if (button) {
      button.disabled = state.nodeSelected.size === 0;
      button.textContent = "强制移除";
    }
    updateNodeSelectionState();
  }
}
function batchCopySelectedLinks() {
  const links = Array.from(state.nodeSelected).map(findNodeByKey).filter(Boolean).map(nodeLink).filter(Boolean).join("\n");
  copyText(links, "选中节点链接已复制");
}

async function batchExportSelectedDocs() {
  const selectedNodes = Array.from(state.nodeSelected)
    .map(findNodeByKey)
    .filter(Boolean);
  if (!selectedNodes.length) {
    showToast("请先勾选需要导出的节点");
    return;
  }

  const exportBtn = el("batchExportDocBtn");
  if (exportBtn?.disabled && exportBtn?.dataset.busy === "1") return;

  const originalText = exportBtn ? exportBtn.textContent : "批量导出文档";
  if (exportBtn) {
    exportBtn.disabled = true;
    exportBtn.dataset.busy = "1";
    exportBtn.textContent = `导出中(${selectedNodes.length})...`;
  }

  try {
    const req = {
      nodes: selectedNodes.map((n) => ({
        vpsAlias: n.vpsAlias,
        fileName: n.fileName,
      })),
    };

    if (typeof backend().ExportSelectedNodesExcelToFile === "function") {
      const path = await api.ExportSelectedNodesExcelToFile(req);
      if (path) {
        showToast(`已成功导出 ${selectedNodes.length} 个勾选节点文档：${path}`);
        return;
      }
    }
    const file = await api.ExportSelectedNodesExcel(req);
    await saveExportFile(file);
    showToast(`已成功导出 ${selectedNodes.length} 个勾选节点文档`);
  } catch (error) {
    const message = error?.message || String(error || "导出失败");
    showToast(message.includes("取消") ? "已取消导出" : `批量导出失败：${message}`);
  } finally {
    if (exportBtn) {
      exportBtn.disabled = state.nodeSelected.size === 0;
      exportBtn.dataset.busy = "";
      exportBtn.textContent = originalText;
    }
    updateNodeSelectionState();
  }
}

function setModifyProgress(percent, text = "") {
  const box = el("modifyProgress");
  if (!box) return;
  const value = Math.max(0, Math.min(100, Number(percent) || 0));
  box.classList.remove("hidden");
  box.scrollIntoView({ behavior: "smooth", block: "nearest" });
  if (el("modifyProgressBar")) el("modifyProgressBar").style.width = `${value}%`;
  setText("modifyProgressPercent", `${Math.round(value)}%`);
  if (text) setText("modifyProgressText", text);
}

function resetModifyProgress() {
  el("modifyProgress")?.classList.add("hidden");
  if (el("modifyProgressBar")) el("modifyProgressBar").style.width = "0%";
  setText("modifyProgressPercent", "0%");
  setText("modifyProgressText", "准备执行");
  setText("modifyResult", "准备修改");
}

function parseModifyOutboundLinesSummary(text) {
  const rawLines = (text || "").split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  if (!rawLines.length) {
    return { total: 0, valid: 0, invalid: 0, uniqueIps: 0 };
  }
  let valid = 0;
  let invalid = 0;
  const ipSet = new Set();
  for (const line of rawLines) {
    if (/^[a-zA-Z0-9+.-]+:\/\//.test(line)) {
      valid++;
      continue;
    }
    const parts = line.split(/[:|,\t]/).map((s) => s.trim()).filter(Boolean);
    if (parts.length >= 2) {
      valid++;
      ipSet.add(parts[0].replace(/^\[|\]$/g, "").toLowerCase());
    } else {
      invalid++;
    }
  }
  return { total: rawLines.length, valid, invalid, uniqueIps: ipSet.size };
}

function updateModifyLinesCount() {
  const text = el("modifyOutboundLines")?.value || "";
  const info = parseModifyOutboundLinesSummary(text);
  if (info.total === 0) {
    setText("modifyLinesCount", "已识别 0 条有效落地配置");
    setText("modifyLinesWarning", "");
    return;
  }
  let countText = `已输入 ${info.total} 行（${info.valid} 条有效落地配置`;
  if (info.uniqueIps > 0) {
    countText += `，${info.uniqueIps} 个独立 IP`;
  }
  countText += "）";
  setText("modifyLinesCount", countText);

  let warningText = "";
  if (info.invalid > 0) {
    warningText = `注意：有 ${info.invalid} 行格式不符合规范`;
  } else if (info.valid > info.uniqueIps) {
    warningText = `包含 ${info.valid - info.uniqueIps} 条相同 IP`;
  }
  setText("modifyLinesWarning", warningText);
}

function openModifyOutbound(alias) {
  el("modifyAlias").value = alias;
  el("modifyOutboundLines").value = "";
  resetModifyProgress();
  updateModifyLinesCount();
  const saveBtn = el("saveModifyBtn");
  if (saveBtn) {
    saveBtn.disabled = false;
    saveBtn.textContent = "保存修改";
  }
  if (el("cancelModifyModalBtn")) el("cancelModifyModalBtn").disabled = false;
  if (el("closeModifyModalBtn")) el("closeModifyModalBtn").disabled = false;
  el("modifyModal").classList.remove("hidden");
}

function closeModifyModal() {
  const saveBtn = el("saveModifyBtn");
  if (saveBtn?.disabled && saveBtn.textContent === "修改中...") return;
  el("modifyModal").classList.add("hidden");
}

async function saveModifyOutbound() {
  const alias = el("modifyAlias").value;
  const rawText = el("modifyOutboundLines")?.value || "";
  const lines = rawText.trim();
  if (!lines) {
    showToast("请输入落地代理配置");
    return;
  }
  const summary = parseModifyOutboundLinesSummary(lines);
  if (summary.valid === 0) {
    showToast("未检测到有效落地代理配置，请检查格式");
    return;
  }

  const saveBtn = el("saveModifyBtn");
  const cancelBtn = el("cancelModifyModalBtn");
  const closeBtn = el("closeModifyModalBtn");
  if (saveBtn?.disabled) return;

  try {
    if (saveBtn) {
      saveBtn.disabled = true;
      saveBtn.textContent = "修改中...";
    }
    if (cancelBtn) cancelBtn.disabled = true;
    if (closeBtn) closeBtn.disabled = true;

    setModifyProgress(15, `核对配置：共识别 ${summary.valid} 条有效落地`);
    setText("modifyResult", `正在处理 ${summary.valid} 条落地配置...`);

    setModifyProgress(40, "正在按 IP 匹配并更新本地节点配置...");
    const result = await api.ModifyNodeOutbounds(alias, [], lines, "");
    if (!result.success) {
      setModifyProgress(100, result.message || "修改落地失败");
      setText("modifyResult", result.message || "修改落地失败");
      showDetail("修改落地失败", result);
      showToast(result.message || "修改落地失败");
      return;
    }

    setModifyProgress(70, "正在发布配置文件到 VPS (SSH)...");
    let publish;
    try {
      publish = await api.UploadAllJSON(alias);
    } catch (err) {
      publish = { success: false, message: err?.message || String(err) };
    }
    if (!publish.success) {
      setModifyProgress(100, "发布到 VPS 失败");
      setText("modifyResult", "本地落地已修改，但发布到 VPS 失败");
      showDetail("发布失败", publish);
      showToast("本地落地已修改，但发布到 VPS 失败");
      return;
    }

    setModifyProgress(88, "检查远端服务状态并同步...");
    await refreshStatusAfterVpsChange(alias);

    setModifyProgress(95, "刷新节点列表...");
    await refreshNodesAfterChange();

    setModifyProgress(100, "修改完成！");
    setText("modifyResult", result.message || "落地修改成功并已发布");
    showToast(result.message || "落地修改成功并已发布");

    await new Promise((resolve) => setTimeout(resolve, 800));
    closeModifyModal();
    el("modifyOutboundLines").value = "";
    resetModifyProgress();
  } catch (error) {
    setModifyProgress(100, error?.message || "操作异常");
    setText("modifyResult", `修改异常: ${error?.message || error}`);
    showToast(`修改异常: ${error?.message || error}`);
  } finally {
    if (saveBtn) {
      saveBtn.disabled = false;
      saveBtn.textContent = "保存修改";
    }
    if (cancelBtn) cancelBtn.disabled = false;
    if (closeBtn) closeBtn.disabled = false;
  }
}

function openDebug(alias) {
  el("debugAlias").value = alias;
  setText("debugModalTitle", `${alias || "VPS"} 调试`);
  if (el("debugIperfCurrent")) {
    el("debugIperfCurrent").value = alias || "__local_windows__";
    updateDebugIperfTargetOptions(el("debugIperfCurrent").value);
  }
  if (el("debugIperfTarget")) {
    const target = (state.overview?.vpsList || []).find((v) => (v.alias || v.id) !== alias);
    el("debugIperfTarget").value = target ? (target.alias || target.id) : "";
  }
  setDebugTab("basic");
  el("debugModal").classList.remove("hidden");
}
function closeDebugModal() { el("debugModal").classList.add("hidden"); }
function setDebugTab(tab) {
  const isIperf = tab === "iperf3";
  document.querySelectorAll("#debugModal .debug-tab").forEach((btn, index) => {
    btn.classList.toggle("active", isIperf ? index === 1 : index === 0);
  });
  el("debugBasicPanel")?.classList.toggle("hidden", isIperf);
  el("debugIperfPanel")?.classList.toggle("hidden", !isIperf);
}
async function runDebug(action) {
  const target = action === "iperf3" ? el("debugIperfTarget").value : el("debugTarget").value.trim();
  const alias = action === "iperf3" ? el("debugIperfCurrent").value : el("debugAlias").value;
  setText("debugSummary", `${action} 执行中...`);
  setText("debugAnalysis", "正在等待返回结果。");
  el("debugResult").textContent = "执行中...";
  try {
    const result = await api.DebugVPS(alias, action, target, Number(el("debugPort").value || 0));
    el("debugResult").textContent = result.output || result.stdout || result.stderr || JSON.stringify(result, null, 2);
    setText("debugSummary", result.message || result.summary || "调试完成");
    setText("debugAnalysis", result.analysis || result.summary || result.message || "请查看左侧原始结果。");
  } catch (err) {
    const message = String(err?.message || err || "调试执行失败");
    el("debugResult").textContent = message;
    setText("debugSummary", message);
    setText("debugAnalysis", "执行失败，请检查 VPS 连接、目标地址和工具安装状态。");
  }
}

async function viewSingboxLog(alias) {
  if (el("singboxLogAlias")) {
    fillAliasFilters();
    if (alias) el("singboxLogAlias").value = alias;
    else alias = el("singboxLogAlias").value;
  }
  setText("singboxLogModalTitle", `${alias || "VPS"} sing-box 运行日志`);
  el("singboxLogModal")?.classList.remove("hidden");
  await refreshSingboxLog();
}

function closeSingboxLogModal() {
  el("singboxLogModal")?.classList.add("hidden");
}

async function refreshSingboxLog() {
  const alias = el("singboxLogAlias")?.value || "";
  const lines = Number(el("singboxLogLines")?.value || 200);
  const pre = el("singboxLogContent");
  const status = el("singboxLogStatus");
  if (!alias) {
    if (pre) pre.textContent = "请先选择 VPS";
    return;
  }
  setText("singboxLogModalTitle", `${alias} sing-box 运行日志`);
  if (status) status.textContent = "正在拉取日志...";
  if (pre) pre.textContent = "正在连接 VPS 并读取 sing-box 运行日志，请稍候...";
  try {
    const result = await api.GetSingboxLog(alias, lines);
    const content = result.output || result.stdout || result.stderr || result.message || "暂无日志输出";
    if (pre) {
      pre.textContent = content;
      pre.scrollTop = pre.scrollHeight;
    }
    const timeStr = new Date().toLocaleTimeString();
    if (status) status.textContent = `已获取 (更新于 ${timeStr})`;
  } catch (err) {
    const msg = String(err?.message || err || "获取日志失败");
    if (pre) pre.textContent = `获取日志失败：${msg}`;
    if (status) status.textContent = "获取失败";
  }
}

async function copySingboxLog() {
  const pre = el("singboxLogContent");
  const text = pre?.textContent || "";
  if (!text) {
    showToast("没有可复制的日志");
    return;
  }
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
    } else {
      const textarea = document.createElement("textarea");
      textarea.value = text;
      document.body.appendChild(textarea);
      textarea.select();
      document.execCommand("copy");
      document.body.removeChild(textarea);
    }
    showToast("日志已复制到剪贴板");
  } catch (err) {
    showToast(`复制失败: ${String(err?.message || err)}`);
  }
}

function clearSingboxLogView() {
  const pre = el("singboxLogContent");
  if (pre) pre.textContent = "";
  const status = el("singboxLogStatus");
  if (status) status.textContent = "已清空显示";
}

async function loadRecords() {
  state.records = await api.GetNodeRecords();
  const rows = state.records.map((r) => `<tr><td>${escapeHtml(r.country)}</td><td>${escapeHtml(r.quantity)}</td><td>${escapeHtml(r.created_date)}</td><td>${escapeHtml(r.renewal_date)}</td><td>${escapeHtml(r.remark)}</td><td><button class="btn small secondary" data-record-action="edit" data-id="${escapeHtml(r.id)}">编辑</button><button class="btn small danger" data-record-action="delete" data-id="${escapeHtml(r.id)}">删除</button></td></tr>`).join("");
  el("recordRows").innerHTML = rows || `<tr><td colspan="6" class="muted">暂无记录</td></tr>`;
  el("recordRows").onclick = (event) => {
    const button = event.target.closest("button[data-record-action]");
    if (!button) return;
    const id = Number(button.dataset.id);
    if (button.dataset.recordAction === "edit") editRecord(id);
    if (button.dataset.recordAction === "delete") deleteRecord(id);
  };
}
function openRecordModal(record) {
  state.editingRecord = record || null;
  el("recordCountry").value = record?.country || "";
  el("recordQuantity").value = record?.quantity || 1;
  el("recordCreatedDate").value = record?.created_date || "";
  el("recordRenewalDate").value = record?.renewal_date || "";
  el("recordRemark").value = record?.remark || "";
  el("recordModal").classList.remove("hidden");
}
function closeRecordModal() { el("recordModal").classList.add("hidden"); }
function editRecord(id) { openRecordModal(state.records.find((r) => r.id === id)); }
async function saveRecord() {
  const saveBtn = el("saveRecordBtn");
  const cancelBtn = el("cancelRecordModalBtn");
  const closeBtn = el("closeRecordModalBtn");
  if (saveBtn?.disabled) return;
  try {
    if (saveBtn) {
      saveBtn.disabled = true;
      saveBtn.textContent = "保存中...";
    }
    if (cancelBtn) cancelBtn.disabled = true;
    if (closeBtn) closeBtn.disabled = true;
    await api.SaveNodeRecord({ id: state.editingRecord?.id || 0, country: el("recordCountry").value.trim(), quantity: Number(el("recordQuantity").value || 1), created_date: el("recordCreatedDate").value, renewal_date: el("recordRenewalDate").value, remark: el("recordRemark").value.trim() });
    closeRecordModal();
    showToast("记录已保存");
    await loadRecords();
  } finally {
    if (saveBtn) {
      saveBtn.disabled = false;
      saveBtn.textContent = "保存";
    }
    if (cancelBtn) cancelBtn.disabled = false;
    if (closeBtn) closeBtn.disabled = false;
  }
}
async function deleteRecord(id) {
  if (!await appConfirm("确认删除这条记录吗？", "删除记录")) return;
  await api.DeleteNodeRecord(id);
  await loadRecords();
}

async function loadConnections() {
  state.connections = await api.SearchNodeConnections(el("connectionSearch").value.trim(), 300);
  const rows = state.connections.map((item) => `<tr><td>${escapeHtml(item.ip)}</td><td>${escapeHtml(item.port)}</td><td>${escapeHtml(item.username)}</td><td>${escapeHtml(item.password)}</td><td>${escapeHtml(item.renewal_date)}</td><td>${escapeHtml(item.remark)}</td><td>${escapeHtml(item.country)}</td><td><button class="btn small danger" data-connection-action="delete" data-id="${escapeHtml(item.id)}">删除</button></td></tr>`).join("");
  el("connectionRows").innerHTML = rows || `<tr><td colspan="8" class="muted">暂无连接记录</td></tr>`;
  el("connectionRows").onclick = (event) => {
    const button = event.target.closest("button[data-connection-action='delete']");
    if (button) deleteConnection(Number(button.dataset.id));
  };
}
async function importConnections() {
  const result = await api.ImportNodeConnections(el("connectionImportText").value);
  showToast(result.message);
  await loadConnections();
}
async function deleteConnection(id) {
  if (!await appConfirm("确认删除这条连接记录吗？", "删除连接记录")) return;
  await api.DeleteNodeConnection(id);
  await loadConnections();
}
async function clearConnections() {
  const text = await appPrompt("输入“确认清空”以清空连接记录", "清空连接记录");
  if (!text) return;
  const count = await api.ClearNodeConnections(text);
  showToast(`已清空 ${count} 条`);
  await loadConnections();
}
async function exportConnections() {
  const csv = await api.ExportNodeConnections(el("connectionSearch").value.trim());
  showDetail("导出结果", csv);
}

async function loadRules() {
  state.rules = await api.GetRuleGroups();
  const rows = state.rules.map((item) => `<tr><td>${escapeHtml(item.name)}</td><td>${escapeHtml(item.description)}</td><td>${item.rules.length}</td><td><button class="btn small secondary" data-rule-action="edit" data-name="${escapeHtml(item.name)}">编辑</button><button class="btn small danger" data-rule-action="delete" data-name="${escapeHtml(item.name)}">删除</button></td></tr>`).join("");
  el("ruleRows").innerHTML = rows || `<tr><td colspan="4" class="muted">暂无规则组</td></tr>`;
  el("ruleRows").onclick = (event) => {
    const button = event.target.closest("button[data-rule-action]");
    if (!button) return;
    if (button.dataset.ruleAction === "edit") editRule(button.dataset.name || "");
    if (button.dataset.ruleAction === "delete") deleteRule(button.dataset.name || "");
  };
}
function editRule(name) {
  const item = state.rules.find((rule) => rule.name === name);
  if (!item) return;
  el("ruleName").value = item.name;
  el("ruleDescription").value = item.description || "";
  el("ruleLines").value = item.rules.join("\n");
}
async function saveRule() {
  await api.SaveRuleGroup({ name: el("ruleName").value.trim(), description: el("ruleDescription").value.trim(), rules: el("ruleLines").value.split("\n").map((v) => v.trim()).filter(Boolean) });
  showToast("规则组已保存");
  await loadRules();
}
async function deleteRule(name) {
  if (!await appConfirm(`确认删除规则组 ${name} 吗？`, "删除规则组")) return;
  await api.DeleteRuleGroup(name);
  await loadRules();
}
async function applyRule() {
  const files = el("ruleFiles").value.split("\n").map((v) => v.trim()).filter(Boolean);
  const result = await api.ApplyRuleGroup(el("ruleAlias").value, files, el("ruleName").value.trim(), "add");
  showToast(result.message);
}

function setBatchRuleProgress(percent, text = "") {
  const box = el("batchRuleProgress");
  if (!box) return;
  const value = Math.max(0, Math.min(100, Number(percent) || 0));
  box.classList.remove("hidden");
  box.scrollIntoView({ behavior: "smooth", block: "nearest" });
  if (el("batchRuleProgressBar")) el("batchRuleProgressBar").style.width = `${value}%`;
  setText("batchRuleProgressPercent", `${Math.round(value)}%`);
  if (text) setText("batchRuleProgressText", text);
}

function resetBatchRuleProgress() {
  el("batchRuleProgress")?.classList.add("hidden");
  if (el("batchRuleProgressBar")) el("batchRuleProgressBar").style.width = "0%";
  setText("batchRuleProgressPercent", "0%");
  setText("batchRuleProgressText", "准备执行");
  setText("batchRuleResult", "准备执行");
}

async function openBatchRules() {
  if (!state.nodeSelected.size) return;
  await loadRules();
  const select = el("batchRuleName");
  select.innerHTML = state.rules.map((rule) => `<option value="${escapeHtml(rule.name)}">${escapeHtml(rule.name)}</option>`).join("");
  if (!state.rules.length) {
    showToast("请先创建规则组");
    setView("rules");
    return;
  }
  resetBatchRuleProgress();
  setText("batchRuleSelectedText", `已选 ${state.nodeSelected.size} 个节点`);
  refreshBatchRulePreview();
  el("batchRuleModal").classList.remove("hidden");
}

function closeBatchRules() {
  const applyBtn = el("applyBatchRuleBtn");
  if (applyBtn?.disabled && applyBtn.textContent.includes("中...")) return;
  resetBatchRuleProgress();
  el("batchRuleModal").classList.add("hidden");
}

function refreshBatchRulePreview() {
  const name = el("batchRuleName")?.value || "";
  const rule = state.rules.find((item) => item.name === name);
  setText("batchRulePreview", rule ? rule.rules.join("\n") : "请选择规则组");
}

async function applyBatchRules() {
  const ruleName = el("batchRuleName").value;
  if (!ruleName) return showToast("请选择规则组");
  const action = el("batchRuleAction").value || "add";
  const groups = new Map();
  state.nodeSelected.forEach((key) => {
    const node = findNodeByKey(key);
    if (!node) return;
    if (!groups.has(node.vpsAlias)) groups.set(node.vpsAlias, []);
    groups.get(node.vpsAlias).push(node.fileName);
  });
  if (!groups.size) return showToast("没有可处理的节点");
  const actionText = action === "remove" ? "移除" : "应用";
  if (!await appConfirm(`确认对 ${state.nodeSelected.size} 个节点${actionText}规则组“${ruleName}”吗？`, "批量规则")) return;

  const applyBtn = el("applyBatchRuleBtn");
  const cancelBtn = el("cancelBatchRuleModalBtn");
  const closeBtn = el("closeBatchRuleModalBtn");
  if (applyBtn?.disabled) return;

  try {
    if (applyBtn) {
      applyBtn.disabled = true;
      applyBtn.textContent = "执行中...";
    }
    if (cancelBtn) cancelBtn.disabled = true;
    if (closeBtn) closeBtn.disabled = true;

    setBatchRuleProgress(10, `正在解析选中的 ${state.nodeSelected.size} 个节点...`);
    setText("batchRuleResult", `正在批量${actionText}规则组...`);

    let count = 0;
    const entries = Array.from(groups.entries());
    for (let i = 0; i < entries.length; i++) {
      const [alias, files] = entries[i];
      const progressPercent = Math.round(15 + (i / entries.length) * 70);
      setBatchRuleProgress(progressPercent, `正在${actionText}并发布到【${alias}】(${i + 1}/${entries.length})...`);
      const result = await api.ApplyRuleGroup(alias, files, ruleName, action);
      count += result.count || 0;
      const publish = await api.UploadAllJSON(alias);
      if (!publish.success) {
        setBatchRuleProgress(100, `发布失败: ${alias}`);
        setText("batchRuleResult", `规则已写入本地，但 ${alias} 发布到 VPS 失败`);
        showDetail("发布失败", publish);
        showToast(`规则已写入本地，但 ${alias} 发布到 VPS 失败`);
        return;
      }
    }

    setBatchRuleProgress(92, "正在刷新节点状态...");
    await loadNodes(true);
    for (const alias of groups.keys()) {
      await refreshStatusAfterVpsChange(alias);
    }

    setBatchRuleProgress(100, `完成：已对 ${count} 个节点${actionText}规则`);
    setText("batchRuleResult", `规则已成功${actionText}并发布到全部节点！`);
    showToast(`批量规则完成，处理 ${count} 个节点`);
    await new Promise((r) => setTimeout(r, 600));
    closeBatchRules();
  } catch (err) {
    setBatchRuleProgress(100, err?.message || "执行失败");
    setText("batchRuleResult", `执行异常: ${err?.message || err}`);
    showToast(`批量规则失败: ${err?.message || err}`);
  } finally {
    if (applyBtn) {
      applyBtn.disabled = false;
      applyBtn.textContent = "执行";
    }
    if (cancelBtn) cancelBtn.disabled = false;
    if (closeBtn) closeBtn.disabled = false;
  }
}

async function loadDeleted() {
  const loadId = ++state.deletedLoadId;
  el("deletedRows").innerHTML = `<tr><td colspan="11" class="muted center">正在读取删除记录...</td></tr>`;
  let records;
  try {
    records = await api.GetDeletedRecords();
  } catch (error) {
    if (loadId !== state.deletedLoadId) return;
    setText("deletedSummaryText", "读取失败");
    el("deletedRows").innerHTML = `<tr><td colspan="11" class="status-fail center">${escapeHtml(frontendErrorMessage(error, "删除记录读取失败"))}</td></tr>`;
    return;
  }
  if (loadId !== state.deletedLoadId) return;
  state.deleted = Array.isArray(records) ? records : [];
  const currentIds = new Set(state.deleted.map((item) => item.id));
  for (const id of state.deletedLatencyResults.keys()) {
    if (!currentIds.has(id)) state.deletedLatencyResults.delete(id);
  }
  for (const id of state.deletedSelected) {
    if (!currentIds.has(id)) state.deletedSelected.delete(id);
  }
  renderDeleted();
  const extras = await Promise.allSettled([loadHealth(), loadAutoDeleteSettings()]);
  if (extras.some((item) => item.status === "rejected")) showToast("删除记录已读取，部分健康设置加载失败");
}

function renderDeleted() {
  const alias = el("deletedFilterAlias")?.value.trim().toLowerCase() || "";
  const ip = el("deletedFilterIp")?.value.trim().toLowerCase() || "";
  const group = el("deletedFilterGroup")?.value.trim().toLowerCase() || "";
  const filtered = state.deleted.filter((item) => {
    if (alias && !String(item.vps_alias || "").toLowerCase().includes(alias)) return false;
    if (ip && !String(item.vps_ip || "").toLowerCase().includes(ip)) return false;
    if (group && !String(item.node_group || "").toLowerCase().includes(group)) return false;
    return true;
  });
  const pages = Math.max(1, Math.ceil(filtered.length / state.deletedPageSize));
  if (state.deletedPage > pages) state.deletedPage = pages;
  const start = (state.deletedPage - 1) * state.deletedPageSize;
  const pageItems = filtered.slice(start, start + state.deletedPageSize);
  const rows = pageItems.map((item) => {
    const latency = state.deletedLatencyResults.get(item.id);
    const outboundText = deletedOutboundText(item);
    return `<tr>
      <td><input type="checkbox" class="deleted-select" data-id="${item.id}" ${state.deletedSelected.has(item.id) ? "checked" : ""}></td>
      <td class="deleted-time">${escapeHtml(item.deleted_at || "-")}</td>
      <td>${escapeHtml(item.vps_alias || "-")}</td>
      <td class="code">${escapeHtml(item.vps_ip || "-")}</td>
      <td>${escapeHtml(item.node_group || "-")}</td>
      <td>${escapeHtml(item.node_tag || item.filename || "-")}</td>
      <td>${escapeHtml(deletedProtocol(item))}</td>
      <td class="copy-cell outbound-full" data-copy="${escapeHtml(outboundText)}" title="${escapeHtml(outboundText)}">${escapeHtml(outboundText || "-")}</td>
      <td class="${latency?.success ? "status-ok" : latency ? "status-fail" : "muted"}">${escapeHtml(latencyText(latency))}</td>
      <td><button class="btn small secondary" data-deleted-action="detail" data-id="${escapeHtml(item.id)}">详情</button></td>
      <td><button class="btn small success" data-deleted-action="restore" data-id="${escapeHtml(item.id)}">恢复</button></td>
    </tr>`;
  }).join("");
  el("deletedRows").innerHTML = rows || `<tr><td colspan="11" class="muted center">暂无删除记录</td></tr>`;
  setText("deletedSummaryText", `共 ${filtered.length} 条，当前页 ${pageItems.length} 条`);
  setText("deletedPageText", `第 ${state.deletedPage} / ${pages} 页`);
  el("deletedPrevPageBtn").disabled = state.deletedPage <= 1;
  el("deletedNextPageBtn").disabled = state.deletedPage >= pages;
  wireDeletedTable();
  updateDeletedSelectionState();
}

function deletedProtocol(item) {
  const inbounds = item.config_json?.inbounds;
  return inbounds?.[0]?.type || "-";
}

function deletedOutboundText(item) {
  const outbound = item.config_json?.outbounds?.[0] || {};
  const ip = outbound.server || "";
  const port = outbound.server_port || "";
  const user = outbound.username || "";
  const pass = outbound.password || "";
  return [ip, port, user, pass].filter(Boolean).join(":");
}

function latencyText(result) {
  if (!result) return "-";
  if (result.testing) return "测试中...";
  if (result.success && Number.isFinite(result.latency)) return `${result.latency} ms`;
  return result.message || "失败";
}

function wireDeletedTable() {
  document.querySelectorAll(".deleted-select").forEach((input) => {
    input.addEventListener("change", () => {
      const id = Number(input.dataset.id);
      if (input.checked) state.deletedSelected.add(id);
      else state.deletedSelected.delete(id);
      updateDeletedSelectionState();
    });
  });
  document.querySelectorAll("#deletedView .copy-cell").forEach((cell) => {
    cell.addEventListener("click", () => copyText(cell.dataset.copy, "落地信息已复制"));
  });
  el("deletedRows").onclick = (event) => {
    const button = event.target.closest("button[data-deleted-action]");
    if (!button) return;
    const id = Number(button.dataset.id);
    if (button.dataset.deletedAction === "detail") showDeletedDetail(id);
    if (button.dataset.deletedAction === "restore") restoreDeleted(id);
  };
}

function updateDeletedSelectionState() {
  setText("deletedSelectedCount", state.deletedSelected.size);
  ["copyDeletedOutboundBtn", "testDeletedOutboundBtn", "batchRestoreDeletedBtn", "batchDeleteDeletedBtn"].forEach((id) => {
    const button = el(id);
    if (button) button.disabled = state.deletedSelected.size === 0;
  });
  const ids = Array.from(document.querySelectorAll(".deleted-select")).map((input) => Number(input.dataset.id));
  const selectAll = el("selectAllDeletedInput");
  if (selectAll) selectAll.checked = ids.length > 0 && ids.every((id) => state.deletedSelected.has(id));
}

function showDeletedDetail(id) {
  const item = state.deleted.find((record) => record.id === id);
  if (item) showDetail("删除详情", item);
}

function copySelectedDeletedOutbounds() {
  const text = state.deleted
    .filter((item) => state.deletedSelected.has(item.id))
    .map(deletedOutboundText)
    .filter(Boolean)
    .join("\n");
  copyText(text, "出站信息已复制");
}

async function testSelectedDeletedOutbounds() {
  const selected = state.deleted.filter((item) => state.deletedSelected.has(item.id));
  if (!selected.length) return;
  selected.forEach((item) => state.deletedLatencyResults.set(item.id, { testing: true }));
  renderDeleted();
  for (const item of selected) {
    const text = deletedOutboundText(item);
    if (!text) {
      state.deletedLatencyResults.set(item.id, { success: false, message: "无落地" });
      renderDeleted();
      continue;
    }
    try {
      const response = await api.TestProxyNodes({ target: "local", nodes: text, concurrency: 1 });
      const { items } = normalizeProxyDetectResponse(response);
      state.deletedLatencyResults.set(item.id, items[0] || { success: false, message: "未返回结果" });
    } catch (error) {
      state.deletedLatencyResults.set(item.id, { success: false, message: frontendErrorMessage(error, "检测失败") });
    }
    renderDeleted();
  }
}
function applyRestoredNodes(result, ids) {
  const idSet = new Set(ids || []);
  const restoredCount = Number(result.count || idSet.size || 0);
  state.deleted = state.deleted.filter((item) => !idSet.has(item.id));
  idSet.forEach((id) => {
    state.deletedSelected.delete(id);
    state.deletedLatencyResults.delete(id);
  });
  if (state.overview?.stats) {
    state.overview.stats.nodeCount = Number(state.overview.stats.nodeCount || 0) + restoredCount;
    setText("nodeCount", state.overview.stats.nodeCount);
  }
  const aliases = new Set();
  (result.items || []).forEach((item) => {
    const parts = String(item || "").split("::");
    if (parts[0]) aliases.add(parts[0]);
  });
  if (state.overview?.vpsList) {
    aliases.forEach((alias) => {
      const count = (result.items || []).filter((item) => String(item || "").startsWith(`${alias}::`)).length;
      const vps = state.overview.vpsList.find((item) => (item.alias || item.id) === alias);
      if (vps) vps.nodeCount = Number(vps.nodeCount || 0) + count;
    });
    renderVPS();
  }
  renderDeleted();
  return aliases;
}

async function publishRestoredNodes(result) {
  const aliases = new Set();
  (result.items || []).forEach((item) => {
    const parts = String(item || "").split("::");
    if (parts[0]) aliases.add(parts[0]);
  });
  return Promise.all(Array.from(aliases).map(async (alias) => ({ alias, publish: await api.UploadAllJSON(alias) })));
}

async function restoreDeleted(id) {
  if (!await appConfirm("确认恢复这个节点吗？", "恢复节点")) return;
  const result = await api.RestoreDeletedRecord(id);
  if (!result.success) return showToast(result.message || "恢复失败");
  applyRestoredNodes(result, [id]);
  showToast("本地已恢复，正在发布到 VPS...");
  const publishResults = await publishRestoredNodes(result);
  const failed = publishResults.filter((item) => !item.publish.success);
  if (failed.length) {
    showDetail("恢复发布失败", failed.map((item) => ({ alias: item.alias, result: item.publish })));
    showToast("节点已恢复到本地，但发布到 VPS 失败");
  } else {
    showToast(result.message || "节点已恢复并发布到 VPS");
    for (const item of publishResults) {
      await refreshStatusAfterVpsChange(item.alias);
    }
  }
  await refreshNodesAfterChange();
}
async function batchRestoreDeleted() {
  const ids = Array.from(state.deletedSelected);
  if (!ids.length) return;
  if (!await appConfirm(`确认恢复 ${ids.length} 条记录吗？`, "批量恢复")) return;
  const result = await api.RestoreDeletedRecords(ids);
  if (!result.success) return showToast(result.message || "批量恢复失败");
  applyRestoredNodes(result, ids);
  showToast(`本地已恢复 ${result.count || ids.length} 个节点，正在发布到 VPS...`);
  const publishResults = await publishRestoredNodes(result);
  const failed = publishResults.filter((item) => !item.publish.success);
  if (failed.length) {
    showDetail("恢复发布失败", failed.map((item) => ({ alias: item.alias, result: item.publish })));
    showToast(`已恢复到本地，但 ${failed.length} 台 VPS 发布失败`);
  } else {
    showToast(result.message || "恢复完成并发布到 VPS");
    for (const item of publishResults) {
      await refreshStatusAfterVpsChange(item.alias);
    }
  }
  state.deletedSelected.clear();
  await refreshNodesAfterChange();
}

async function batchDeleteDeleted() {
  const ids = Array.from(state.deletedSelected);
  if (!ids.length) return;
  if (!await appConfirm(`确认永久删除 ${ids.length} 条删除记录吗？`, "批量永久删除")) return;
  const text = await appPrompt("为避免误删，请输入：确认删除", "批量永久删除");
  if (!text) return;
  const result = await api.DeleteDeletedRecords(ids, text);
  showToast(result.message);
  state.deletedSelected.clear();
  await loadDeleted();
}
async function loadHealth() {
  state.health = await api.GetHealthStatus();
  const rows = state.health.map((item) => `<tr><td>${escapeHtml(item.vps_alias)}</td><td>${escapeHtml(item.vps_ip)}</td><td>${escapeHtml(item.node_group)}</td><td>${escapeHtml(item.node_tag)}</td><td>${escapeHtml(item.inbound_port)}</td><td>${escapeHtml(item.fail_count)}</td><td>${escapeHtml(item.last_checked_at)}</td><td>${escapeHtml(item.last_message)}</td></tr>`).join("");
  el("healthRows").innerHTML = rows || `<tr><td colspan="8" class="muted center">暂无连续失败记录</td></tr>`;
}
async function loadAutoDeleteSettings() {
  const settings = await api.GetAutoDeleteSettings();
  el("autoEnabled").value = settings.enabled ? "1" : "0";
  el("autoFailureDays").value = settings.failure_days || 3;
  el("autoCheckTime").value = settings.check_time || "03:00";
  el("autoConcurrency").value = settings.concurrency || 10;
  el("autoTimezone").value = settings.timezone || "Asia/Shanghai";
}
async function saveAutoDeleteSettings() {
  const settings = await api.SaveAutoDeleteSettings({ enabled: el("autoEnabled").value === "1", failure_days: Number(el("autoFailureDays").value || 3), check_time: el("autoCheckTime").value, timezone: el("autoTimezone").value || "Asia/Shanghai", concurrency: Number(el("autoConcurrency").value || 10) });
  el("autoEnabled").value = settings.enabled ? "1" : "0";
  el("autoFailureDays").value = settings.failure_days || 3;
  el("autoCheckTime").value = settings.check_time || "03:00";
  el("autoConcurrency").value = settings.concurrency || 10;
  el("autoTimezone").value = settings.timezone || "Asia/Shanghai";
  showToast("自动删除设置已保存");
}
async function runAutoDelete() {
  const alias = el("runNowVpsInput")?.value.trim() || "";
  const scope = alias ? `VPS：${alias}` : "全部 VPS";
  if (!await appConfirm(`立即执行会检测 ${scope}，失败节点会转入删除记录并从本地节点列表移除。确认继续吗？`, "立即执行自动删除")) return;
  const button = el("runAutoDeleteBtn");
  const oldText = button.textContent;
  button.disabled = true;
  button.textContent = "执行中";
  try {
    const result = await api.RunAutoDeleteCheck(alias);
    showToast(result.message);
    await loadOverview();
    await loadDeleted();
  } finally {
    button.disabled = false;
    button.textContent = oldText;
  }
}
async function clearHealth() {
  const text = await appPrompt("输入“确认清空”以清空健康记录", "清空健康记录");
  if (!text) return;
  const count = await api.ClearHealthStatus(text);
  showToast(`已清空 ${count} 条`);
  await loadHealth();
}

document.querySelectorAll(".nav-item").forEach((btn) => btn.addEventListener("click", () => setView(btn.dataset.view)));
setupFilterCombos();
setupMiddleHistoryCombos();
on("reloadBtn", "click", refreshAllStatus);
on("addVpsBtn", "click", () => openModal());
on("vpsCountryFilter", "change", renderVPS);
on("closeModalBtn", "click", closeModal);
on("cancelModalBtn", "click", closeModal);
on("saveVpsBtn", "click", saveVPS);
on("vpsAuthMethod", "change", refreshVpsAuthFields);
on("nodeAliasFilter", "change", loadNodes);
const renderNodesDebounced = debounce(renderNodes, 120);
["nodeAliasText", "nodeVpsIpSearch", "nodeGroupSearch", "nodeSearch", "nodePortSearch"].forEach((id) => on(id, "input", () => {
  state.nodePage = 1;
  renderNodesDebounced();
}));
on("nodeSearchBtn", "click", () => { state.nodePage = 1; renderNodes(); });
on("nodeClearBtn", "click", () => {
  ["nodeAliasText", "nodeVpsIpSearch", "nodeGroupSearch", "nodeSearch", "nodePortSearch"].forEach((id) => { if (el(id)) el(id).value = ""; });
  state.nodePage = 1;
  loadNodes();
});
on("queryAllOutboundBtn", "click", queryVisibleOutbounds);
on("selectAllNodesInput", "change", () => {
  const isChecked = el("selectAllNodesInput").checked;
  const filtered = currentFilteredNodes();
  if (isChecked) {
    filtered.forEach((node) => state.nodeSelected.add(nodeKey(node)));
  } else {
    filtered.forEach((node) => state.nodeSelected.delete(nodeKey(node)));
  }
  document.querySelectorAll(".node-select").forEach((input) => {
    input.checked = isChecked;
  });
  updateNodeSelectionState();
});
on("batchDeleteNodesBtn", "click", batchDeleteSelectedNodes);
on("batchForceDeleteNodesBtn", "click", batchForceDeleteSelectedNodes);
on("batchCopyLinksBtn", "click", batchCopySelectedLinks);
on("batchExportDocBtn", "click", batchExportSelectedDocs);
on("batchRulesBtn", "click", openBatchRules);
on("clearNodeSelectionBtn", "click", () => { state.nodeSelected.clear(); renderNodes(); });
on("nodePrevPageBtn", "click", () => { if (state.nodePage > 1) { state.nodePage -= 1; renderNodes(); } });
on("nodeNextPageBtn", "click", () => { state.nodePage += 1; renderNodes(); });
on("nodePageSize", "change", () => { state.nodePageSize = Number(el("nodePageSize").value || 100); state.nodePage = 1; renderNodes(); });
on("backupAliasFilter", "change", () => {
  state.backupActionId += 1;
  clearBackupPreview();
  loadBackups();
});
on("backupNowBtn", "click", backupNow);
on("restoreJsonBtn", "click", restoreJsonFromNodes);
on("refreshBackupsBtn", "click", loadBackups);
on("closeBackupModalBtn", "click", closeBackupModal);
on("closeBackupModalBtn2", "click", closeBackupModal);
on("tcpTestBtn", "click", testTCP);
on("proxyBatchBtn", "click", testProxyNodes);
on("cancelProxyBtn", "click", cancelProxyDetect);
on("proxyTargetOptions", "change", syncProxyTargetSelection);
on("proxyTargetPreset", "change", (e) => {
  const isCustom = e.target.value === "custom";
  const customInput = el("proxyCustomUrl");
  if (customInput) {
    customInput.classList.toggle("hidden", !isCustom);
    if (isCustom) customInput.focus();
  }
});
on("proxyFilterAll", "click", () => setProxyFilter("all"));
on("proxyFilterOk", "click", () => setProxyFilter("ok"));
on("proxyFilterFail", "click", () => setProxyFilter("fail"));
on("copyProxyOkBtn", "click", copyProxyOkNodes);
on("exportProxyTxtBtn", "click", exportProxyTxt);
on("exportProxyExcelBtn", "click", exportProxyExcel);
on("clearProxyBtn", "click", () => {
  state.proxyRunId += 1;
  el("proxyNodes").value = "";
  el("tcpResult").textContent = "暂无结果";
  const progressBox = el("proxyProgressBox");
  if (progressBox) progressBox.classList.add("hidden");
  renderProxyResults([]);
});
on("convertMiddleBtn", "click", convertMiddleAcceleration);
on("clearMiddleBtn", "click", clearMiddleAcceleration);
on("copyMiddleBtn", "click", () => copyText(el("middleOutputText")?.value || "", "中转结果已复制"));
on("exportMiddleBtn", "click", exportMiddleAcceleration);
on("middleInputFile", "change", loadMiddleInputFile);
on("middleInputText", "input", () => {
  state.middleExcelFile = null;
  state.middleExcelExportFile = null;
});
on("saveMiddleSettingsBtn", "click", saveMiddleSettings);
on("closeMiddleLogModalBtn", "click", closeMiddleLog);
on("closeMiddleLogModalBtn2", "click", closeMiddleLog);
on("addNodeGroupBtn", "click", () => { addNodeGroupCard(defaultNodeGroupName()); refreshNodeStartPort(); });
on("nodeAddMode", "change", refreshAddNodeMode);
on("nodeBatchGroupsText", "input", () => refreshNodeStartPort());
on("nodeStartPort", "input", () => { if (el("nodeStartPort")) el("nodeStartPort").dataset.autoValue = ""; });
on("nodeOutboundProtocol", "change", () => {
  document.querySelectorAll(".node-group-outbound-input").forEach((input) => { input.placeholder = nodeOutboundPlaceholder(); });
  refreshNodeStartPort();
});
on("nodeInboundProtocol", "change", () => {
  refreshInboundProtocolUI();
});
on("nodeRealityEnabled", "change", refreshInboundProtocolUI);
on("closeNodeModalBtn", "click", closeNodeModal);
on("cancelNodeModalBtn", "click", closeNodeModal);
on("saveNodeBtn", "click", saveNode);
on("viewNodeSaveResultBtn", "click", () => viewGeneratedNodes(el("viewNodeSaveResultBtn")?.dataset.alias || ""));
on("closeMultiIpNodeModalBtn", "click", closeMultiIpNodeModal);
on("cancelMultiIpNodeModalBtn", "click", closeMultiIpNodeModal);
on("saveMultiIpNodeBtn", "click", saveMultiIPNode);
on("viewMultiIpResultBtn", "click", () => viewGeneratedNodes(el("viewMultiIpResultBtn")?.dataset.alias || ""));
on("multiIpInboundProtocol", "change", refreshMultiIpProtocolUI);
on("closeModifyModalBtn", "click", closeModifyModal);
on("cancelModifyModalBtn", "click", closeModifyModal);
on("saveModifyBtn", "click", saveModifyOutbound);
on("modifyOutboundLines", "input", updateModifyLinesCount);
on("closeBatchRuleModalBtn", "click", closeBatchRules);
on("cancelBatchRuleModalBtn", "click", closeBatchRules);
on("batchRuleName", "change", refreshBatchRulePreview);
on("applyBatchRuleBtn", "click", applyBatchRules);
on("debugPingBtn", "click", () => runDebug("ping"));
on("debugMtrBtn", "click", () => runDebug("mtr"));
on("debugPortBtn", "click", () => runDebug("port"));
on("debugIperfBtn", "click", () => runDebug("iperf3"));
on("debugIperfCurrent", "change", () => updateDebugIperfTargetOptions(el("debugIperfCurrent")?.value || ""));
document.querySelectorAll("#debugModal .debug-tab").forEach((btn, index) => {
  btn.addEventListener("click", () => setDebugTab(index === 1 ? "iperf3" : "basic"));
});
on("closeDebugModalBtn", "click", closeDebugModal);
on("closeDebugModalBtn2", "click", closeDebugModal);
on("debugClearBtn", "click", () => {
  el("debugResult").textContent = "等待操作...";
  setText("debugSummary", "选择功能后会在这里显示中文说明。");
  setText("debugAnalysis", "MTR 开始后，这里显示简短结论：路径、最终延迟、丢包和异常点。");
});
on("addRecordBtn", "click", () => openRecordModal());
on("closeRecordModalBtn", "click", closeRecordModal);
on("cancelRecordModalBtn", "click", closeRecordModal);
on("saveRecordBtn", "click", saveRecord);
on("searchConnectionBtn", "click", loadConnections);
on("importConnectionBtn", "click", importConnections);
on("exportConnectionBtn", "click", exportConnections);
on("clearConnectionBtn", "click", clearConnections);
on("saveRuleBtn", "click", saveRule);
on("applyRuleBtn", "click", applyRule);
on("reloadDeletedBtn", "click", () => { state.deletedPage = 1; renderDeleted(); });
on("resetDeletedBtn", "click", () => {
  ["deletedFilterAlias", "deletedFilterIp", "deletedFilterGroup"].forEach((id) => { el(id).value = ""; });
  state.deletedPage = 1;
  renderDeleted();
});
on("refreshDeletedBtn", "click", loadDeleted);
on("saveAutoDeleteBtn", "click", saveAutoDeleteSettings);
on("scheduleToggleBtn", "click", async () => {
  el("autoEnabled").value = el("autoEnabled").value === "1" ? "0" : "1";
  await saveAutoDeleteSettings();
});
on("healthRecordsBtn", "click", async () => { await loadHealth(); el("healthModal").classList.remove("hidden"); });
on("runAutoDeleteBtn", "click", runAutoDelete);
on("clearHealthBtn", "click", clearHealth);
on("closeHealthModalBtn", "click", () => el("healthModal").classList.add("hidden"));
on("closeDetailModalBtn", "click", closeDetail);
on("closeQrModalBtn", "click", closeQrModal);
on("closeQrModalBtn2", "click", closeQrModal);
on("copyQrLinkBtn", "click", copyQrLink);
on("closeToastBtn", "click", () => closeToast(false));
on("toastOkBtn", "click", () => closeToast(true));
on("exportDetailLogBtn", "click", exportDetailLog);
on("copyDetailJsonBtn", "click", copyDetailJson);
on("editDetailJsonBtn", "click", enableDetailJsonEdit);
on("saveDetailJsonBtn", "click", saveDetailJson);
on("confirmCloseBtn", "click", () => closeConfirmModal(false));
on("confirmCancelBtn", "click", () => closeConfirmModal(false));
on("confirmOkBtn", "click", () => {
  const input = el("confirmInput");
  closeConfirmModal(input.classList.contains("hidden") ? true : input.value);
});
on("selectAllDeletedInput", "change", () => {
  document.querySelectorAll(".deleted-select").forEach((input) => {
    const id = Number(input.dataset.id);
    input.checked = el("selectAllDeletedInput").checked;
    if (input.checked) state.deletedSelected.add(id);
    else state.deletedSelected.delete(id);
  });
  updateDeletedSelectionState();
});
on("copyDeletedOutboundBtn", "click", copySelectedDeletedOutbounds);
on("testDeletedOutboundBtn", "click", testSelectedDeletedOutbounds);
on("batchRestoreDeletedBtn", "click", batchRestoreDeleted);
on("batchDeleteDeletedBtn", "click", batchDeleteDeleted);
on("deletedPrevPageBtn", "click", () => { if (state.deletedPage > 1) { state.deletedPage -= 1; renderDeleted(); } });
on("deletedNextPageBtn", "click", () => { state.deletedPage += 1; renderDeleted(); });
on("deletedPageSize", "change", () => { state.deletedPageSize = Number(el("deletedPageSize").value || 100); state.deletedPage = 1; renderDeleted(); });
on("closeTrafficModalBtn", "click", closeTrafficModal);
on("closeTrafficModalBtn2", "click", closeTrafficModal);
on("refreshTrafficBtn", "click", refreshTrafficRank);
el("toast")?.addEventListener("click", (event) => {
  if (event.target === el("toast")) closeToast();
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !el("toast")?.classList.contains("hidden")) closeToast();
});
document.querySelectorAll("#trafficModal .traffic-tabs .debug-tab").forEach((btn, index) => {
  btn.addEventListener("click", () => setTrafficMode(index === 1 ? "total" : "realtime"));
});
document.querySelectorAll(".segment").forEach((btn) => {
  btn.addEventListener("click", async () => {
    state.trafficLimit = Number(btn.dataset.limit || 10);
    document.querySelectorAll(".segment").forEach((item) => item.classList.toggle("active", item === btn));
    if (state.trafficData) renderTrafficRows();
    else await refreshTrafficRank();
  });
});

on("closeSingboxLogModalBtn", "click", closeSingboxLogModal);
on("closeSingboxLogModalBtn2", "click", closeSingboxLogModal);
on("refreshSingboxLogBtn", "click", refreshSingboxLog);
on("copySingboxLogBtn", "click", copySingboxLog);
on("clearSingboxLogBtn", "click", clearSingboxLogView);
on("singboxLogAlias", "change", refreshSingboxLog);
on("singboxLogLines", "change", refreshSingboxLog);

Object.assign(window, {
  refreshNodeMode, markNodeModeDirty, showNodesForAlias,
  openBackups, deploySingbox, controlSingbox, rebootVps, syncAllJson,
  openVpsJSON, trafficRank, openAddNode, openAddMultiIPNode,
  openModifyOutbound, openDebug, viewSingboxLog,
});

async function startApp() {
  const revision = state.nodeModeRevision;
  const embeddedNodeMode = hostWindow !== window && !shellActionHost;
  if (embeddedNodeMode) setView("nodes", { skipNodeLoad: true });
  await waitForBackend();
  await loadOverview();
  if (embeddedNodeMode) await loadNodes();
  state.nodeModeRefreshedAt = Date.now();
  if (state.nodeModeRevision === revision) state.nodeModeDirty = false;
}

window.nodeModeReady = startApp().catch((err) => {
  console.error(err);
  const message = frontendErrorMessage(err, "加载失败");
  if (!state.overview) showVpsLoadError(message);
  showToast(message);
});
