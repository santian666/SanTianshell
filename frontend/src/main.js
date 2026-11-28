import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import appIcon from "./assets/app-icon.png";
import "./style.css";
import "./layout.css";

const api = () => window.go?.main?.App;
const THEME_CACHE_KEY = "santianshell.theme";
function cachedTheme() {
  try {
    const theme = localStorage.getItem(THEME_CACHE_KEY);
    return theme === "dark" || theme === "light" ? theme : "";
  } catch {
    return "";
  }
}
const state = {
  settings: null,
  selected: "",
  path: "/",
  files: [],
  editing: null,
  contextFile: null,
  contextTarget: null,
  chmodTarget: null,
  connected: false,
  sessions: {},
  rdpSessions: {},
  sshInteractions: {},
  hostEditorFromWorkspace: false,
  hostEditorRevision: 0,
  recentOnly: false,
  latencies: {},
  monitorTimer: 0,
  monitorActive: 0,
  monitorSlowActive: 0,
  monitorStatusRenderedAt: 0,
  activeMonitorStreamKey: "",
  fileTimer: 0,
  fileActive: 0,
  fileOpenRequest: 0,
  latencyChecks: new Set(),
  latencyTasks: new Map(),
  latencyTaskModes: new Map(),
  processLimit: 8,
  processes: [],
  aiProviderDraftModels: [],
  aiProviderModelSource: { endpoint: "", apiKey: "" },
  aiRunId: "",
  aiStarting: false,
  aiConfigBusy: false,
  aiFinishedRuns: new Set(),
  aiOutputBubble: null,
  aiOutputPending: "",
  aiOutputTimer: 0,
  aiProgressBubble: null,
  aiProgressMessage: "",
  aiProgressStartedAt: 0,
  aiProgressTimer: 0,
  aiPanelChanging: false,
  vpsOverview: null,
  vpsStatuses: {},
  vpsRefreshes: {},
  vpsLoading: false,
  vpsBindingWarnings: new Set(),
  vpsStartupChecked: false,
  vpsRefreshingAll: false,
  expandedHosts: new Set(),
  hostSelection: new Set(),
  batchConcurrency: Math.min(
    20,
    Math.max(1, Number(localStorage.getItem("batchConnectConcurrency")) || 10),
  ),
  syncCommands: false,
  splitHosts: [],
  portForwards: [],
  portForwardRequest: 0,
  queueRun: null,
  quickCommandBusy: false,
  connectionEpochCounter: 0,
  toastTimer: 0,
  securityBaseline: {
    configured: false,
    lockOnStartup: false,
    protectExport: false,
  },
};
const app = document.querySelector("#app");

function replaceSettings(next) {
  if (!next) return state.settings;
  const temporary = (state.settings?.hosts || []).filter((host) => host.temporary);
  const persistent = Array.isArray(next.hosts) ? next.hosts : [];
  const persistentIDs = new Set(persistent.map((host) => host.id));
  next.hosts = [
    ...persistent,
    ...temporary.filter((host) => !persistentIDs.has(host.id)),
  ];
  state.settings = next;
  return next;
}

app.innerHTML = `
<div class="app-titlebar"><div class="app-title"><img src="${appIcon}" alt=""><b>SanTianShell</b></div><div class="window-controls"><button id="windowMin" type="button" title="最小化" aria-label="最小化">—</button><button id="windowMax" type="button" title="最大化或还原" aria-label="最大化或还原">□</button><button id="windowClose" type="button" title="关闭" aria-label="关闭">×</button></div></div>
<header class="app-header">
  <div class="top-center">
    <button id="hostsBtn" class="active">Shell 模式</button>
    <button id="nodeModeBtn">节点模式</button>
  </div>
  <nav class="top-sessions"><div id="sessionTabs"></div><button class="square" id="newSession">＋</button></nav>
</header>
<section id="nodeModePage" class="node-mode-page hidden"><iframe id="nodeModeFrame" title="节点模式" src="about:blank"></iframe></section>
<section id="nodeActionHost" class="node-action-host hidden"><div class="node-action-dialog"><header><b>节点管理</b><button id="closeNodeAction" type="button">关闭</button></header><iframe id="nodeActionFrame" title="节点操作" src="about:blank" allowtransparency="true"></iframe></div></section>
<div id="nodeModalChromeMask" class="node-modal-chrome-mask hidden" aria-hidden="true"></div>
<main class="ai-collapsed">
  <aside class="monitor-side">
    <section class="host-summary"><div><i id="onlineDot"></i><b id="sideAddress">未连接</b><button id="copyHostIP" class="hidden" type="button">复制</button><button id="sideReconnect" class="hidden" type="button">重连</button></div><small id="sideUser"><span id="sideLogin" class="hidden"></span><span id="sideHostName">请选择主机开始连接</span></small></section>
    <section class="card system-card"><div class="card-title"><b>系统信息</b><span id="uptime">未连接</span></div><div class="load"><span>负载</span><small id="loadValue">—</small></div>
      <div class="meter-row"><span>CPU</span><div class="meter"><i id="cpuBar"></i></div><b id="cpuText">0%</b></div>
      <div class="meter-row"><span>内存</span><div class="meter amber"><i id="memBar"></i></div><b id="memText">0%</b></div>
      <div class="meter-row"><span>交换</span><div class="meter gray"><i id="swapBar"></i></div><b id="swapText">0%</b></div>
    </section>
    <section class="card process-card"><div class="card-title"><b>进程 TOP</b><span id="processUpdated">未连接</span></div><div class="card-scroll"><table><thead><tr><th>PID</th><th>CPU</th><th>内存</th><th>命令</th></tr></thead><tbody id="processRows"><tr><td colspan="4" class="placeholder">暂无数据</td></tr></tbody></table></div></section>
    <section class="card network-card"><div class="card-title"><b>网络</b><span id="networkState">未连接</span></div><div class="net-line up">↑ <b id="netUp">0 B/s</b></div><div class="net-line down">↓ <b id="netDown">0 B/s</b></div><div id="networkChart" class="chart"></div></section>
    <section class="card latency-card"><div class="card-title"><b>网络延迟</b><span id="latencyTarget">SSH 会话 RTT</span></div><div class="latency-values"><span id="latencyCurrent">—<small>本次</small></span><span id="packetLoss">—<small>丢包率</small></span><span id="latencyMin">—<small>Min</small></span><span id="latencyMax">—<small>Max</small></span></div><div id="latencyChart" class="mini-chart"></div></section>
    <section class="card disk-card"><div class="card-title"><b>磁盘</b><div><span id="diskUpdated">未连接</span><button id="refreshMonitor">刷新</button></div></div><div class="card-scroll"><table><thead><tr><th>路径</th><th>已用/大小</th></tr></thead><tbody id="diskRows"><tr><td colspan="2" class="placeholder">暂无数据</td></tr></tbody></table></div></section>
    <div class="side-actions"><button id="hostTab">设置</button><button id="aboutBtn">关于</button></div>
  </aside>
  <section class="center-work">
    <section class="terminal-panel">
      <div id="terminalReconnectBanner" class="terminal-reconnect-banner hidden">
        <span id="reconnectBannerText" class="reconnect-text">连接已断开，正在准备重连…</span>
        <div class="reconnect-actions">
          <button type="button" id="reconnectNowBtn" class="primary">立即重连</button>
          <button type="button" id="reconnectCancelBtn">取消重连</button>
        </div>
      </div>
      <div id="terminalEmpty" class="terminal-empty">SanTianShell 安全远程终端\n选择主机后点击“连接 SSH”。</div>
      <div id="terminalSessions" class="terminal-sessions"></div>
      <div id="rdpDeck" class="rdp-deck hidden"></div>
    </section>
    <form id="commandForm"><span>&gt;</span><input id="command" autocomplete="off" placeholder="输入命令，按 Enter 执行"><button type="button" id="reconnectBtn" title="重连当前会话（Ctrl+Shift+R）">重连</button><button type="button" id="persistentSessionBtn" aria-pressed="false">持久会话</button><button type="button" id="syncCommandBtn" aria-pressed="false">同步</button><button type="button" id="hostInfoBtn" class="blue-line">主机信息</button><button type="button" id="historyBtn" title="在当前 SSH 终端执行 history 50" disabled>历史</button><button type="button" id="clearTerminalBtn" title="清空终端屏幕与残留弹窗（Ctrl+L）" disabled>清屏</button><button type="button" id="terminalSearchBtn" title="搜索当前终端缓冲区" disabled>搜索</button><button type="button" id="splitTerminalBtn" title="开关终端分屏（Ctrl+Shift+P）" aria-pressed="false" disabled>分屏</button><button type="button" id="portForwardBtn" title="管理 SSH 隧道" disabled>SSH 隧道</button><button type="button" id="sessionLogBtn" aria-pressed="false" disabled>日志</button></form>
    <section class="file-panel">
      <nav class="file-tabs"><button id="filesTab" class="active">文件</button><button id="presetCommandsTab">命令</button></nav>
      <div class="filebar"><input id="path" value=""><button id="historyPath">历史</button><button id="hiddenBtn">隐藏项</button><button id="refreshFiles">刷新</button><button id="upBtn">上一级</button><button id="uploadBtn">上传</button></div>
      <div class="file-content"><div class="filetree"><b>目录</b><div id="dirHint">未连接</div><div id="treeItems" class="tree-note"><span class="placeholder">暂无数据</span></div></div><div class="filelist"><table><thead><tr><th>文件名 ↑</th><th>大小</th><th>类型</th><th>修改时间</th><th>权限</th><th></th></tr></thead><tbody id="fileRows"><tr><td colspan="6" class="placeholder">暂无数据</td></tr></tbody></table></div></div>
    </section>
  </section>
  <button id="aiToggle" class="ai-toggle" type="button" title="展开 AI 助手" aria-label="展开 AI 助手">◀</button>
  <aside class="ai-side">
    <div class="ai-tools"><select id="providerTop"><option value="">暂无供应商</option></select><button id="aiSettings">AI 供应商</button><button id="clearAIChat" type="button" title="清空当前对话与记忆">清空</button></div>
    <div class="model-row"><select id="modelTop"><option value="">未配置模型</option></select><span class="ai-row-label">模型</span></div>
    <div class="model-row permission-row"><select id="aiPermission" aria-label="AI 权限"><option value="suggest">建议</option><option value="review">审核</option><option value="managed">托管</option></select><span class="ai-row-label">权限</span></div>
    <div id="chat"><div class="bubble system"><b>AI 服务器助手</b><br>用自然语言说明目标（诊断、部署、排障、优化或批量运维）。支持实时终端交互、内联确认方案、Markdown 命令一键执行。</div><div class="bubble notice"><b>✦ 权限模式</b><br>“建议”：只读排查并给出可一键执行的命令方案。<br>“审核”：变更命令先在对话内确认再执行。<br>“托管”：自动规划、执行、排错并验证到完成。</div></div>
    <div id="aiQuickChips" class="ai-quick-chips" aria-label="快捷运维助手提示"><button type="button" data-prompt="全面检查当前服务器的 CPU、内存、负载、磁盘与异常进程，给出健康报告。">全面体检</button><button type="button" data-prompt="排查当前服务器监听端口、网络连接数、防火墙与连通性状态。">网络端口</button><button type="button" data-prompt="检查当前系统失败的服务（systemd）与最近关键错误日志，定位原因。">服务排障</button><button type="button" data-prompt="分析磁盘占用最高的目录与大文件，给出安全清理方案。">磁盘分析</button><button type="button" data-prompt="检查 SSH 登录记录、开放端口和基础安全加固建议。">安全检查</button></div>
    <form id="aiForm"><div class="ai-input-meta"><span id="aiTargetHost" class="ai-target-pill">未选主机</span><button id="attachTerminalCtx" type="button" class="ai-ctx-btn" aria-pressed="false" title="提问时自动附带当前终端屏幕最近 150 行输出">📎 附带终端</button></div><textarea id="question" placeholder="输入服务器需求，Enter 发送，Shift+Enter 换行"></textarea><div><small id="aiModeHint">未配置 AI</small><span class="ai-form-actions"><button id="stopAI" class="hidden" type="button">停止</button><button id="sendAI" class="primary">执行</button></span></div></form>
  </aside>
</main>
<section id="hostDrawer" class="host-page hidden"><div class="host-page-head"><div><h2>主机</h2><span id="hostCount">全部 · 0 台</span></div><div><label id="batchConcurrencyWrap" class="batch-concurrency hidden" title="批量连接同时发起的最大连接数">并发 <input id="batchConcurrency" type="number" min="1" max="20"></label><button id="batchConnect" class="primary hidden" type="button" title="同时连接已勾选的 SSH 主机">批量连接 (0)</button><button id="closeDrawer" class="primary">进入工作台</button><button id="addSmall">添加主机</button><button id="refreshAllVps" type="button">刷新状态</button><button id="importHosts">导入文件</button><button id="importHostFolder">导入文件夹</button><button id="exportHosts">导出</button><label class="deleted-toggle"><input id="showDeleted" type="checkbox">显示已删除</label></div></div><div class="host-page-tools"><input id="search" placeholder="搜索名称 / 地址 / 用户 / 分组"><button id="recentFilter" type="button" aria-pressed="false">最近</button><select id="countryFilter"><option value="">全部国家</option></select><select id="groupFilter"><option value="">全部分组</option></select><select id="sortHosts"><option value="manual">手动排序</option><option value="name">按名称</option><option value="address">按地址</option><option value="recent">最近使用</option></select><button id="addGroup">新建分组</button></div><div class="host-page-body"><div id="hostList"></div></div></section>
<section id="hostEditor" class="host-editor hidden"><form id="hostForm"><header class="host-editor-head"><div><h2 id="modalTitle">新增主机</h2><span>配置远程连接信息</span></div><button type="button" class="cancel">返回主机列表</button></header><div class="host-editor-main"><nav class="host-editor-nav"><button type="button" class="active" data-section="hostBasic">主机信息</button><button type="button" data-section="hostConnect">连接设置</button><button type="button" data-section="hostProxy">SOCKS5 代理</button></nav><div class="host-editor-content"><input type="hidden" id="id"><section id="hostBasic" class="host-form-card"><h3>主机信息</h3><div class="host-form-grid"><label><span>主机名称 <b>*</b></span><input id="name" required placeholder="例如：生产服务器"></label><label><span>主机分组</span><input id="group" placeholder="未分组"></label></div></section><section id="hostConnect" class="host-form-card hidden"><h3>连接设置</h3><div class="host-form-grid"><label><span>协议</span><select id="protocol"><option value="ssh">SSH</option><option value="rdp">RDP</option></select></label><label><span>端口</span><input id="port" type="number" min="1" max="65535" value="22"></label><label class="span-2"><span>主机 IP <b>*</b></span><input id="address" required placeholder="IP 地址或域名"></label><label><span>认证方式</span><select id="authType"><option value="password">密码</option><option value="password_mfa">密码 + MFA</option><option value="key">密钥</option><option value="key_mfa">密钥 + MFA</option><option value="agent">Windows SSH Agent（OpenSSH / Pageant）</option><option value="agent_mfa">Windows SSH Agent（OpenSSH / Pageant）+ MFA</option></select></label><label><span>用户名 <b>*</b></span><input id="username" required value="root"></label><label id="passwordField" class="span-2"><span>密码</span><input id="password" type="password" autocomplete="new-password"></label><label id="keyPathField" class="span-2 hidden"><span>私钥或证书文件</span><input id="keyPath" placeholder="例如 C:\\Users\\me\\.ssh\\id_ed25519"></label><label id="keyPassphraseField" class="span-2 hidden"><span>密钥口令</span><input id="keyPassphrase" type="password" autocomplete="new-password"></label><label><span>连接超时（秒）</span><input id="timeout" type="number" min="1" max="300" value="12"></label><label><span>Keep-Alive（秒）</span><input id="keepAlive" type="number" min="1" max="3600" value="30"></label></div><div id="authDescription" class="auth-description"></div><div id="hostKeyTrust" class="host-key-trust hidden"><span id="hostKeyText"></span><button id="resetHostKey" type="button">重新信任</button></div></section><section id="hostProxy" class="host-form-card hidden"><h3>SOCKS5 代理</h3><div class="host-form-grid"><label><span>代理主机（IP / Domain）</span><input id="proxyHost" placeholder="127.0.0.1"></label><label><span>端口</span><input id="proxyPort" type="number" min="1" max="65535" value="1080"></label><label><span>用户名（可选）</span><input id="proxyUsername"></label><label><span>密码（可选）</span><input id="proxyPassword" type="password" autocomplete="new-password"></label></div><p>代理主机留空时直接连接，不使用 SOCKS5 代理。</p></section></div></div><footer class="host-editor-actions"><span id="hostTestResult"></span><button type="button" class="cancel">取消</button><button type="button" id="testHostConfig">测试连接</button><button class="primary">保存</button></footer></form></section>
<div id="editorModal" class="modal hidden"><div class="dialog editor"><div class="editor-head"><h3 id="editorTitle">远程编辑</h3><span id="editorState">已同步</span></div><textarea id="editor"></textarea><div class="actions"><button id="downloadEdit" type="button">下载</button><button type="button" class="cancelEdit">关闭</button><button id="saveEdit" class="primary" type="button">保存并同步</button></div></div></div>
<div id="aiModal" class="modal hidden"><form id="aiConfigForm" class="dialog ai-config"><h3 id="aiConfigTitle">新增 AI 供应商</h3><input type="hidden" id="providerId"><label>供应商名称<input id="providerName" placeholder="填写实际供应商名称"></label><label>兼容 OpenAI 的 API 地址<input id="endpoint" placeholder="https://example.com/v1"></label><label>API Key<input id="apiKey" type="password"></label><div class="ai-config-model"><div class="ai-config-model-head"><span>模型</span><button id="fetchProviderModels" type="button">获取 API 模型</button></div><select id="providerModelSelect" aria-label="选择已缓存模型"><option value="">暂无缓存模型</option></select><small id="modelCacheHint">获取后将随供应商配置自动缓存</small></div><label>权限<select id="mode"><option value="suggest">建议</option><option value="review">审核</option><option value="managed">托管</option></select></label><div class="actions"><button id="deleteProvider" class="hidden" type="button">删除</button><button type="button" class="cancelAI">取消</button><button class="primary">保存</button></div></form></div>
<section id="aboutPage" class="about-page hidden"><div class="about-page-head"><div><h2>关于 SanTianShell</h2><span>Windows 桌面远程管理工具</span></div><button id="closeAbout">返回主机页面</button></div><div class="about-grid"><article><h3>软件信息</h3><dl><dt>软件版本</dt><dd id="aboutVersion">—</dd><dt>技术架构</dt><dd id="aboutFramework">—</dd><dt>运行平台</dt><dd id="aboutPlatform">—</dd><dt>Go 版本</dt><dd id="aboutGo">—</dd><dt>数据目录</dt><dd id="aboutDataDir">—</dd></dl></article><article><h3>日志与诊断</h3><p>如果遇到问题，您可以导出日志文件发送给开发者以帮助排查问题。</p><dl><dt>日志目录</dt><dd id="aboutLogDir">—</dd></dl><div class="about-actions"><button id="openLogs" class="primary">打开日志目录</button><button id="exportLogs">导出日志文件</button></div></article><article><h3>主要功能</h3><p>SSH、RDP、SFTP、远程编辑、系统监控、节点模式和 AI 辅助。</p><p><b>节点模式：</b>统一管理 VPS，支持 Sing-box 部署与状态维护、节点生成、代理检测、中转加速、流量查看及配置备份还原。</p><p>主机密码与 AI Key 使用当前 Windows 用户的 DPAPI 加密后保存在本机。</p></article></div></section>
<section id="settingsPage" class="settings-page hidden"><div class="settings-head"><div><h2>全局设置</h2><span>下载、启动、AI 与安全</span></div><button id="closeSettings" class="primary">进入工作台</button></div><form id="globalSettingsForm" class="settings-grid"><article><h3>下载与启动</h3><label>下载保存路径<div class="directory-setting"><input id="downloadDirectory" readonly><button id="selectDownloadDirectory" type="button">选择文件夹</button></div></label><label class="setting-check"><input id="autoStartSetting" type="checkbox">开机自动启动 SanTianShell</label></article><article class="ai-settings"><div class="setting-card-head"><h3>AI 配置</h3><button id="manageAI" class="primary" type="button">新增供应商</button></div><div id="settingsAIProviders" class="settings-ai-providers"></div></article><article class="security-settings"><div class="security-heading"><h3>软件密保</h3><span id="securityStatus" class="security-status">未设置</span></div><p>用于启动解锁和导出验证。密保不保存明文，请妥善保管。</p><div class="security-options"><label class="setting-check"><input id="lockStartupSetting" type="checkbox">启动时要求解锁</label><label class="setting-check"><input id="protectExportSetting" type="checkbox">导出主机时再次验证</label></div><div id="securityPasswordFields" class="security-password-grid hidden"><label id="currentSecurityField">当前密保<div class="password-input"><input id="currentSecurityPassword" type="password" autocomplete="current-password"><button class="password-toggle" data-password-target="currentSecurityPassword" type="button" aria-pressed="false">显示</button></div></label><label id="newSecurityField">新密保<div class="password-input"><input id="newSecurityPassword" type="password" autocomplete="new-password" placeholder="8～128 个字符"><button class="password-toggle" data-password-target="newSecurityPassword" type="button" aria-pressed="false">显示</button></div></label><label id="confirmSecurityField">确认新密保<div class="password-input"><input id="confirmSecurityPassword" type="password" autocomplete="new-password"><button class="password-toggle" data-password-target="confirmSecurityPassword" type="button" aria-pressed="false">显示</button></div></label></div><small id="securityHint" class="security-hint">启用任一保护选项后设置密保。</small><div id="securityMessage" class="security-inline-message" role="status" aria-live="polite"></div></article><article class="ai-preset-settings"><h3>AI 附加偏好</h3><label><textarea id="aiPresetPrompt" spellcheck="false" placeholder="例如：回复更简洁，优先使用系统软件源"></textarea></label><small>这里只填写附加偏好，内置 AI 运维框架与安全规则始终生效；留空表示不添加偏好。</small></article><article class="update-settings"><div class="setting-card-head"><h3>软件更新</h3><span id="settingsAppVersion" class="security-status">v0.1.38</span></div><p>从 GitHub Releases 检查并下载最新纯净版本进行原地更新，不会影响本地主机、密钥和配置数据。</p><div class="update-actions-row"><button id="checkAppUpdateBtn" class="primary" type="button">检查更新</button><span id="appUpdateCheckHint" class="update-check-hint">点击检查是否有可用更新</span></div></article><footer><button class="primary">保存设置</button></footer></form></section>
<div id="unlockModal" class="modal hidden"><form id="unlockForm" class="dialog unlock-dialog"><h3>SanTianShell 已锁定</h3><p>请输入软件密保后继续。</p><label for="unlockPassword">软件密保</label><div class="password-input"><input id="unlockPassword" type="password" autocomplete="current-password" required><button class="password-toggle" data-password-target="unlockPassword" type="button" aria-pressed="false">显示</button></div><div id="unlockError" class="unlock-error" role="alert" aria-live="assertive"></div><div class="actions"><button id="unlockSubmit" class="primary">解锁</button></div></form></div>
<div id="quickCommandModal" class="modal hidden" role="dialog" aria-modal="true" aria-labelledby="quickCommandTitle"><form id="quickCommandForm" class="dialog quick-command-dialog"><h3 id="quickCommandTitle">添加快捷命令</h3><input id="quickCommandId" type="hidden"><input id="quickCommandType" type="hidden" value="single"><label>名称<input id="quickCommandName" maxlength="64" required placeholder="输入快捷命令名称"></label><label id="singleCommandField">命令<textarea id="quickCommandValue" maxlength="65536" spellcheck="false" placeholder="输入要执行的命令"></textarea></label><div id="queueCommandField" class="queue-command-field hidden"><div><b>队列步骤</b><button id="addQueueStep" type="button">＋ 添加步骤</button></div><div id="queueSteps"></div><div class="queue-run-options"><label>单步超时（秒）<input id="queueTimeout" type="number" min="1" max="3600" value="300"></label><label>失败重试<select id="queueRetries"><option value="0">不重试</option><option value="1">1 次</option><option value="2">2 次</option><option value="3">3 次</option></select></label></div><small>按 Enter 新增下一步；每台主机完成当前步骤后再进入下一步，仅明确的非零退出码会按设置重试。</small></div><div class="actions"><button id="deleteQuickCommand" class="danger hidden" type="button">删除</button><button id="cancelQuickCommand" type="button">取消</button><button id="saveQuickCommand" class="primary">保存</button></div></form></div>
<div id="fileContextMenu" class="file-context-menu hidden"><button data-action="open">打开</button><button data-action="edit">编辑</button><button data-action="download">下载</button><hr><button data-action="newFile">新建文件</button><button data-action="newDir">新建文件夹</button><button data-action="upload">上传</button><hr><button data-action="rename">重命名</button><button data-action="chmod">修改权限</button><button data-action="copyPath">复制路径</button><button data-action="refresh">刷新</button><hr><button data-action="delete" class="danger">删除</button></div>
<div id="chmodModal" class="modal hidden"><form id="chmodForm" class="dialog chmod-dialog"><h3>编辑权限</h3><p id="chmodPath"></p><div class="permission-grid"><fieldset><legend>所有者</legend><label><input data-bit="400" type="checkbox">读取</label><label><input data-bit="200" type="checkbox">写入</label><label><input data-bit="100" type="checkbox">执行</label></fieldset><fieldset><legend>组</legend><label><input data-bit="40" type="checkbox">读取</label><label><input data-bit="20" type="checkbox">写入</label><label><input data-bit="10" type="checkbox">执行</label></fieldset><fieldset><legend>其他</legend><label><input data-bit="4" type="checkbox">读取</label><label><input data-bit="2" type="checkbox">写入</label><label><input data-bit="1" type="checkbox">执行</label></fieldset></div><div class="chmod-mode">权限值：<b id="chmodMode">000</b></div><div class="actions"><button id="cancelChmod" type="button">取消</button><button class="primary">确定</button></div></form></div>
<div id="uiDialogModal" class="modal hidden"><form id="uiDialogForm" class="dialog ui-dialog"><h3 id="uiDialogTitle">操作确认</h3><p id="uiDialogMessage"></p><div id="uiDialogQuestions" class="ui-dialog-questions hidden"></div><label id="uiDialogInputLabel" class="ui-dialog-input-label hidden" for="uiDialogInput">输入内容</label><input id="uiDialogInput" class="hidden" autocomplete="off"><div id="uiDialogInputError" class="ui-dialog-input-error hidden" role="alert">密码不能为空</div><fieldset id="uiDialogPasswordScope" class="ui-dialog-password-scope hidden"><legend>新密码保存方式</legend><label><input type="radio" name="uiDialogPasswordScope" value="once" checked><span><b>仅本次连接</b><small>只保存在内存中，关闭会话或退出软件后清除</small></span></label><label id="uiDialogPermanentOption"><input type="radio" name="uiDialogPasswordScope" value="permanent"><span><b>验证成功后永久保存</b><small>只有连接成功才会更新密码</small></span></label><p id="uiDialogScopeHint"></p></fieldset><div class="actions"><button id="uiDialogCancel" type="button">取消</button><button id="uiDialogOK" class="primary" type="submit">确定</button></div></form></div>
<div id="terminalSearchModal" class="modal terminal-search-modal hidden"><section class="dialog terminal-search-dialog" role="dialog" aria-modal="true" aria-labelledby="terminalSearchTitle"><header><b id="terminalSearchTitle">终端搜索</b><button id="closeTerminalSearch" type="button" title="关闭" aria-label="关闭">×</button></header><form id="terminalSearchForm"><input id="terminalSearchInput" autocomplete="off" placeholder="输入终端内容"><button id="terminalSearchPrevious" type="button">上一个</button><button class="primary" type="submit">下一个</button></form><small id="terminalSearchResult" aria-live="polite">输入关键词开始搜索</small></section></div>
<div id="splitTerminalModal" class="modal split-terminal-modal hidden"><form id="splitTerminalForm" class="dialog split-terminal-dialog"><h3>选择第二台 SSH</h3><label>已连接主机<select id="splitTerminalHost"></select></label><small>终端固定为两列等宽；点击任一终端即可切换该主机的监控和文件。</small><div class="actions"><button id="cancelSplitTerminal" type="button">取消</button><button class="primary" type="submit">开始分屏</button></div></form></div>
<div id="portForwardModal" class="modal port-forward-modal hidden"><form id="portForwardForm" class="dialog port-forward-dialog"><header><div><h3>SSH 隧道</h3><small>本地、远程和动态 SOCKS 转发</small></div><button id="closePortForward" type="button" title="关闭" aria-label="关闭">×</button></header><div class="port-forward-form-grid"><label><span>SSH 主机</span><select id="portForwardHost"></select></label><label><span>转发类型</span><select id="portForwardKind"><option value="local">本地转发</option><option value="remote">远程转发</option><option value="dynamic">动态 SOCKS</option></select></label><label><span>监听地址</span><input id="portForwardListenHost" value="127.0.0.1" spellcheck="false"></label><label><span>监听端口</span><input id="portForwardListenPort" type="number" min="0" max="65535" value="0"></label><label class="port-forward-target"><span>目标地址</span><input id="portForwardTargetHost" value="127.0.0.1" spellcheck="false"></label><label class="port-forward-target"><span>目标端口</span><input id="portForwardTargetPort" type="number" min="0" max="65535" value="0"></label></div><small id="portForwardHint">本机监听后，经 SSH 访问远端目标。</small><div class="port-forward-start"><span id="portForwardStatus" role="status" aria-live="polite"></span><button id="startPortForward" class="primary" type="submit">启动转发</button></div><section class="port-forward-running"><header><b>当前转发</b><button id="refreshPortForwards" type="button">刷新</button></header><div id="portForwardList"><div class="port-forward-empty">暂无运行中的转发</div></div></section></form></div>
<div id="appUpdateModal" class="modal hidden"><section class="dialog app-update-dialog" role="dialog" aria-modal="true" aria-labelledby="appUpdateTitle"><header><div><h3 id="appUpdateTitle">发现新版本</h3><small>SanTianShell 纯净版在线更新</small></div><button id="closeAppUpdateBtn" type="button" title="关闭" aria-label="关闭">×</button></header><div class="app-update-version-row"><span id="appUpdateCurVer">v0.1.35</span><span class="app-update-arrow">→</span><span id="appUpdateNewVer" class="app-update-new-ver">v0.1.36</span></div><pre id="appUpdateNotes" class="app-update-notes">暂无更新说明</pre><div id="appUpdateProgressBox" class="app-update-progress-box hidden"><div class="app-update-progress-status"><span id="appUpdateStatusText">准备下载更新包...</span><span id="appUpdatePercentText">0%</span></div><div class="app-update-progress-bar-bg"><div id="appUpdateProgressBar" class="app-update-progress-bar" style="width:0%"></div></div></div><div class="actions"><button id="cancelAppUpdateBtn" type="button">暂不更新</button><button id="confirmAppUpdateBtn" class="primary" type="button">立即更新</button></div></section></div>
<div id="toast"></div>`;

const $ = (s) => document.querySelector(s);
const esc = (s) =>
  String(s ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
function syncCheckedLabels(container) {
  container?.querySelectorAll("input[type=radio],input[type=checkbox]").forEach((input) => {
    input.closest("label")?.classList.toggle("selected", input.checked);
  });
}
const runtimeEventBindings = [];
let runtimeEventsBound = false;
function queueRuntimeEvent(name, handler) {
  runtimeEventBindings.push([name, handler]);
}
function bindRuntimeEvents() {
  if (runtimeEventsBound) return true;
  if (typeof window.runtime?.EventsOn !== "function") return false;
  runtimeEventBindings.forEach(([name, handler]) => window.runtime.EventsOn(name, handler));
  runtimeEventsBound = true;
  return true;
}
function toast(msg, bad = false) {
  const e = $("#toast");
  window.clearTimeout(state.toastTimer);
  e.textContent = msg;
  e.className = `show ${bad ? "bad" : ""}`;
  state.toastTimer = window.setTimeout(() => {
    e.className = "";
    state.toastTimer = 0;
  }, 2600);
}
let uiDialogActive = null;
const uiDialogQueue = [];
function openNextUIDialog() {
  if (uiDialogActive || !uiDialogQueue.length) return;
  uiDialogActive = uiDialogQueue.shift();
  const {
    title = "操作确认",
    message = "",
    input = false,
    value = "",
    password = false,
    questions = null,
  } = uiDialogActive.options;
  const multiQuestions = Array.isArray(questions) && questions.length > 0;
  const passwordRetry = !multiQuestions && uiDialogActive.options.kind === "password-retry";
  $("#uiDialogTitle").textContent = title;
  $("#uiDialogMessage").textContent = message;
  $("#uiDialogForm").classList.toggle("password-retry", passwordRetry);
  const questionsBox = $("#uiDialogQuestions");
  questionsBox.innerHTML = "";
  questionsBox.classList.toggle("hidden", !multiQuestions);
  if (multiQuestions) {
    questions.forEach((question, index) => {
      const label = document.createElement("label");
      label.className = "ui-dialog-question";
      const caption = document.createElement("span");
      caption.textContent = question?.text || `验证问题 ${index + 1}`;
      const questionInput = document.createElement("input");
      questionInput.type = question?.echo ? "text" : "password";
      questionInput.autocomplete = "off";
      questionInput.className = "ui-dialog-question-input";
      label.append(caption, questionInput);
      questionsBox.append(label);
    });
  }
  const field = $("#uiDialogInput");
  field.classList.toggle("hidden", multiQuestions || !input);
  field.type = password ? "password" : "text";
  field.autocomplete = passwordRetry ? "new-password" : password ? "current-password" : "off";
  field.value = value;
  const inputLabel = $("#uiDialogInputLabel");
  inputLabel.textContent = passwordRetry ? "新密码" : "输入内容";
  inputLabel.classList.toggle("hidden", multiQuestions || !passwordRetry);
  $("#uiDialogInputError").classList.add("hidden");
  const scope = $("#uiDialogPasswordScope");
  scope.classList.toggle("hidden", !passwordRetry);
  const onceOption = scope.querySelector('input[value="once"]');
  const permanentOption = scope.querySelector('input[value="permanent"]');
  onceOption.checked = true;
  permanentOption.checked = false;
  syncCheckedLabels(scope);
  const allowPermanent = passwordRetry && uiDialogActive.options.allowPermanent !== false;
  permanentOption.disabled = !allowPermanent;
  $("#uiDialogPermanentOption").classList.toggle("disabled", !allowPermanent);
  $("#uiDialogScopeHint").textContent = passwordRetry
    ? uiDialogActive.options.scopeHint ||
      (allowPermanent
        ? "永久保存只会在新密码验证成功后执行。"
        : "临时快速连接只能使用一次性密码，不会写入主机库。")
    : "";
  $("#uiDialogOK").textContent = passwordRetry ? "使用新密码重连" : "确定";
  $("#uiDialogModal").classList.remove("hidden");
  const active = uiDialogActive;
  if (multiQuestions) {
    requestAnimationFrame(() => {
      if (uiDialogActive !== active) return;
      questionsBox.querySelector("input")?.focus();
    });
  } else if (input)
    requestAnimationFrame(() => {
      if (uiDialogActive !== active) return;
      field.focus();
      field.select();
    });
}
function closeUIDialog(value) {
  if (!uiDialogActive) return;
  $("#uiDialogModal").classList.add("hidden");
  const active = uiDialogActive;
  uiDialogActive = null;
  $("#uiDialogInput").value = "";
  $("#uiDialogQuestions").innerHTML = "";
  $("#uiDialogQuestions").classList.add("hidden");
  active.resolve(value);
  openNextUIDialog();
}
function showUIDialog(options) {
  return new Promise((resolve) => {
    uiDialogQueue.push({ options, resolve });
    openNextUIDialog();
  });
}
function cancelUIDialogs(predicate) {
  for (let index = uiDialogQueue.length - 1; index >= 0; index -= 1) {
    if (!predicate(uiDialogQueue[index].options)) continue;
    const [item] = uiDialogQueue.splice(index, 1);
    item.resolve(null);
  }
  if (uiDialogActive && predicate(uiDialogActive.options)) closeUIDialog(null);
}
function cancelSSHInteractionsForHost(hostId) {
  const keys = new Set(
    Object.entries(state.sshInteractions)
      .filter(([, currentHostId]) => currentHostId === hostId)
      .map(([requestId]) => `ssh:${requestId}`),
  );
  if (keys.size) cancelUIDialogs((options) => keys.has(String(options.key || "")));
}
const uiConfirm = (message, title = "操作确认", key = "") =>
  showUIDialog({ title, message, key }).then(Boolean);
const uiPrompt = (message, value = "", password = false) =>
  showUIDialog({ title: "请输入", message, input: true, value, password });
$("#uiDialogCancel").onclick = () => closeUIDialog(null);
$("#uiDialogForm").onsubmit = (e) => {
  e.preventDefault();
  if (Array.isArray(uiDialogActive?.options?.questions) && uiDialogActive.options.questions.length > 0) {
    const answers = [...$("#uiDialogQuestions").querySelectorAll("input")].map((item) => item.value);
    closeUIDialog(answers);
    return;
  }
  if (uiDialogActive?.options?.kind === "password-retry") {
    const field = $("#uiDialogInput");
    if (!field.value) {
      $("#uiDialogInputError").classList.remove("hidden");
      field.focus();
      return;
    }
    const selectedScope = document.querySelector('input[name="uiDialogPasswordScope"]:checked')?.value || "once";
    closeUIDialog({ password: field.value, scope: selectedScope });
    return;
  }
  closeUIDialog(
    $("#uiDialogInput").classList.contains("hidden")
      ? true
      : $("#uiDialogInput").value,
  );
};
$("#uiDialogPasswordScope").onchange = () => syncCheckedLabels($("#uiDialogPasswordScope"));
$("#uiDialogInput").oninput = () => $("#uiDialogInputError").classList.add("hidden");
document.querySelectorAll(".password-toggle").forEach((button) => {
  button.onclick = () => {
    const input = $(`#${button.dataset.passwordTarget}`);
    if (!input) return;
    const visible = input.type === "text";
    input.type = visible ? "password" : "text";
    button.textContent = visible ? "显示" : "隐藏";
    button.setAttribute("aria-pressed", String(!visible));
    input.focus({ preventScroll: true });
    input.setSelectionRange(input.value.length, input.value.length);
  };
});
function showSideIdentity(username = "", hostName = "请选择主机开始连接") {
  $("#sideLogin").textContent = username;
  $("#sideLogin").classList.toggle("hidden", !username);
  $("#sideHostName").textContent = hostName;
}
function openChmod(file, target) {
  if (!file || !fileTargetActive(target)) return toast("SSH 会话已变化，请重新选择文件", true);
  state.chmodTarget = { ...target, file };
  const mode = String(file.mode || "").slice(-9);
  $("#chmodForm")
    .querySelectorAll("input[data-bit]")
    .forEach(
      (input, i) => (input.checked = Boolean(mode[i] && mode[i] !== "-")),
    );
  $("#chmodPath").textContent = file.path;
  updateChmodMode();
  $("#chmodModal").classList.remove("hidden");
}
function updateChmodMode() {
  const value = [
    ...$("#chmodForm").querySelectorAll("input[data-bit]:checked"),
  ].reduce((sum, input) => sum + Number(input.dataset.bit), 0);
  $("#chmodMode").textContent = String(value).padStart(3, "0");
}
const filePanel = $(".file-panel"),
  fileBar = $(".filebar"),
  fileContent = $(".file-content");
fileContent.insertAdjacentHTML(
  "afterend",
  '<div id="presetCommands" class="preset-commands hidden"><section class="preset-column"><div class="preset-head"><b>快捷命令</b><button id="addQuick" class="primary" type="button">添加快捷命令</button></div><div id="presetCommandList" class="preset-list"></div></section><section class="preset-column queue-column"><div class="preset-head"><b>队列快捷命令</b><span><button id="stopQueue" class="danger hidden" type="button">停止队列</button><button id="addQueueQuick" class="primary" type="button">添加队列快捷命令</button></span></div><div id="queueCommandList" class="preset-list"></div><div id="queueRunStatus" class="queue-run-status idle" role="status" aria-live="polite"><div><b id="queueRunName">暂无运行队列</b><span id="queueRunMeta">等待执行</span></div><div id="queueRunProgress" class="queue-run-progress" role="progressbar" aria-label="队列执行进度" aria-valuemin="0" aria-valuemax="100" aria-valuenow="0"><i></i></div></div></section></div>',
);
function showFileArea(showFiles) {
  filePanel.classList.toggle("commands-visible", !showFiles);
  $("#filesTab").classList.toggle("active", showFiles);
  $("#presetCommandsTab").classList.toggle("active", !showFiles);
  fileBar.classList.toggle("hidden", !showFiles);
  fileContent.classList.toggle("hidden", !showFiles);
  $("#presetCommands").classList.toggle("hidden", showFiles);
  if (!showFiles) renderPresetCommands();
}
function renderPresetCommands() {
  const items = state.settings?.quickCommands || [];
  const render = (target, commands, empty) => {
    $(target).innerHTML = commands.length
      ? commands
        .map(
          (item) => {
            const queue = item.type === "queue";
            const running = queue && state.queueRun?.commandId === item.id;
            const disabled = queue && state.queueRun ? " disabled" : "";
            const title = `单击${queue ? "执行队列" : "填入命令输入框"}；右键编辑或删除`;
            return `<button class="preset-command${running ? " running" : ""}" type="button" data-id="${esc(item.id)}" data-type="${queue ? "queue" : "single"}" title="${title}" aria-busy="${running}"${disabled}>${esc(item.name)}</button>`;
          },
        )
        .join("")
      : `<div class="host-empty"><b>${empty}</b></div>`;
  };
  render("#presetCommandList", items.filter(item => item.type !== "queue"), "暂无快捷命令");
  render("#queueCommandList", items.filter(item => item.type === "queue"), "暂无队列快捷命令");
  updateQueueRunUI();
}
function setQuickCommandBusy(busy, operation = "") {
  state.quickCommandBusy = busy;
  const form = $("#quickCommandForm");
  form.setAttribute("aria-busy", String(busy));
  form.querySelectorAll("input, textarea, select, button").forEach((control) => {
    control.disabled = busy;
  });
  $("#saveQuickCommand").textContent = busy && operation === "save" ? "保存中…" : "保存";
  $("#deleteQuickCommand").textContent = busy && operation === "delete" ? "删除中…" : "删除";
  if (!busy) updateQueueStepButtons();
}
function closeQuickCommandModal() {
  if (state.quickCommandBusy) return;
  $("#quickCommandModal").classList.add("hidden");
}
function openQuickCommand(item = {}, type = "single") {
  if (state.quickCommandBusy) return;
  const commandType = item.type || type;
  $("#quickCommandId").value = item.id || "";
  $("#quickCommandType").value = commandType;
  $("#quickCommandName").value = item.name || "";
  $("#quickCommandValue").value = item.command || "";
  $("#quickCommandTitle").textContent = item.id
    ? `编辑${commandType === "queue" ? "队列快捷命令" : "快捷命令"}`
    : `添加${commandType === "queue" ? "队列快捷命令" : "快捷命令"}`;
  $("#singleCommandField").classList.toggle("hidden", commandType === "queue");
  $("#queueCommandField").classList.toggle("hidden", commandType !== "queue");
  $("#deleteQuickCommand").classList.toggle("hidden", !item.id);
  if (commandType === "queue") {
    renderQueueSteps((item.command || "").split(/\r?\n/).filter(Boolean));
    $("#queueTimeout").value = String(item.timeoutSeconds || 300);
    $("#queueRetries").value = String(Math.min(3, Math.max(0, item.retries || 0)));
  }
  setQuickCommandBusy(false);
  $("#quickCommandModal").classList.remove("hidden");
  $("#quickCommandName").focus();
}
function queueStepValues() {
  return [...document.querySelectorAll("#queueSteps input")].map((input) => input.value);
}
function updateQueueStepButtons() {
  const steps = [...document.querySelectorAll("#queueSteps .queue-step")];
  steps.forEach((step, index) => {
    step.querySelector('[data-action="up"]').disabled = state.quickCommandBusy || index === 0;
    step.querySelector('[data-action="down"]').disabled = state.quickCommandBusy || index === steps.length - 1;
    step.querySelector('[data-action="delete"]').disabled = state.quickCommandBusy;
  });
}
function renderQueueSteps(commands = [""], focusIndex = -1) {
  const values = commands.length ? commands : [""];
  $("#queueSteps").innerHTML = values.map((command, index) => `<div class="queue-step" data-index="${index}"><b>${index + 1}</b><input value="${esc(command)}" maxlength="4096" aria-label="第 ${index + 1} 条队列命令" placeholder="输入第 ${index + 1} 条命令"><span class="queue-step-actions"><button type="button" data-action="up" title="上移步骤" aria-label="上移第 ${index + 1} 步">↑</button><button type="button" data-action="down" title="下移步骤" aria-label="下移第 ${index + 1} 步">↓</button><button type="button" data-action="delete" title="删除步骤" aria-label="删除第 ${index + 1} 步">×</button></span></div>`).join("");
  updateQueueStepButtons();
  if (focusIndex >= 0) {
    requestAnimationFrame(() => $("#queueSteps").querySelectorAll("input")[focusIndex]?.focus());
  }
}
const cleanTerminal = (text) =>
  String(text)
    .replace(/\x1b\[[0-?]*[ -\/]*[@-~]/g, "")
    .replace(/\r/g, "");
function sessionFor(id) {
  return (state.sessions[id] ||= {
    connected: false,
    connecting: false,
    closing: false,
    output: "",
    path: "/",
    files: [],
    monitorOutput: "",
    monitorLoading: false,
    monitorRequest: 0,
    monitorDueAt: 0,
    monitorUpdatedAt: 0,
    monitorError: "",
    processOutput: "",
    processes: [],
    processLoading: false,
    processRequest: 0,
    processDueAt: 0,
    processUpdatedAt: 0,
    processError: "",
    diskOutput: "",
    disks: [],
    diskLoading: false,
    diskRequest: 0,
    diskDueAt: 0,
    diskUpdatedAt: 0,
    diskError: "",
    monitorForced: new Set(),
    monitorNotify: new Set(),
    monitorStreamActive: false,
    monitorStreamStartedAt: 0,
    monitorStreamLastAt: 0,
    monitorManualRefresh: false,
    cpuSample: null,
    cpuPercent: null,
    cpuError: "",
    latency: null,
    latencyDueAt: 0,
    latencyError: "",
    networkSample: null,
    networkUp: 0,
    networkDown: 0,
    networkHistory: [],
    networkUpdatedAt: 0,
    networkError: "",
    latencyLoading: false,
    latencyRequest: 0,
    latencyHistory: [],
    fileView: null,
    fileLoading: false,
    fileRequest: 0,
    fileDueAt: 0,
    fileUpdatedAt: 0,
    fileError: "",
    pendingFilePath: "",
    pendingFileNotify: false,
    fileTransferActive: false,
    terminal: null,
    fitAddon: null,
    searchAddon: null,
    terminalElement: null,
    terminalHost: null,
    terminalPaneLabel: null,
    terminalDisposables: [],
    connectionEpoch: 0,
    terminalInputChain: Promise.resolve(),
    terminalInputInFlight: 0,
    terminalInputBatchTimer: 0,
    terminalInputBatchPromise: null,
    pendingInputBuffer: "",
    pendingInputEpoch: 0,
    lastConnectionStage: "",
    targetAddress: "",
    probingAlive: false,
    terminalResizeTimer: 0,
    terminalResizeChain: Promise.resolve(),
    terminalScrollFrame: 0,
    terminalRows: 0,
    terminalCols: 0,
    autoReconnect: false,
    reconnectAttempt: 0,
    reconnectTimer: 0,
    reconnectInterval: 0,
    reconnectDueAt: 0,
    tmuxPersistent: false,
    persistentTool: "",
    rdpSessionId: "",
    sessionLogEnabled: false,
    sessionLogActive: false,
    sessionLogPath: "",
    sessionLogLoading: false,
    sessionLogRequest: 0,
  });
}
function advanceConnectionEpoch(session, id = "") {
  const epoch = ++state.connectionEpochCounter;
  session.connectionEpoch = epoch;
  session.lastConnectionStage = "";
  session.pendingInputBuffer = "";
  session.pendingInputEpoch = epoch;
  session.terminalInputInFlight = 0;
  if (session.terminalInputBatchTimer) {
    window.clearTimeout(session.terminalInputBatchTimer);
    session.terminalInputBatchTimer = 0;
  }
  session.terminalInputBatchPromise = null;
  session.latencyLoading = false;
  session.latencyRequest++;
  session.latencyHistory = [];
  session.latency = null;
  session.monitorOutput = "";
  session.monitorLoading = false;
  session.monitorRequest++;
  session.monitorDueAt = 0;
  session.monitorUpdatedAt = 0;
  session.monitorError = "";
  session.processOutput = "";
  session.processes = [];
  session.processLoading = false;
  session.processRequest++;
  session.processDueAt = 0;
  session.processUpdatedAt = 0;
  session.processError = "";
  session.diskOutput = "";
  session.disks = [];
  session.diskLoading = false;
  session.diskRequest++;
  session.diskDueAt = 0;
  session.diskUpdatedAt = 0;
  session.diskError = "";
  session.monitorForced.clear();
  session.monitorNotify.clear();
  session.monitorStreamActive = false;
  session.monitorStreamStartedAt = 0;
  session.monitorStreamLastAt = 0;
  session.monitorManualRefresh = false;
  session.cpuSample = null;
  session.cpuPercent = null;
  session.cpuError = "";
  session.latencyDueAt = 0;
  session.latencyError = "";
  session.fileLoading = false;
  session.fileRequest++;
  session.fileDueAt = 0;
  session.fileUpdatedAt = 0;
  session.fileError = "";
  session.pendingFilePath = "";
  session.pendingFileNotify = false;
  session.path = "/";
  session.files = [];
  session.fileView = null;
  session.fileTransferActive = false;
  if (state.selected === id) {
    resetMonitor();
    state.path = "/";
    state.files = [];
    $("#path").value = "";
    $("#dirHint").textContent = "正在连接…";
    $("#treeItems").innerHTML = '<span class="placeholder">暂无数据</span>';
    $("#fileRows").innerHTML =
      '<tr><td colspan="6" class="placeholder">暂无数据</td></tr>';
  }
  session.networkSample = null;
  session.networkUp = 0;
  session.networkDown = 0;
  session.networkHistory = [];
  session.networkUpdatedAt = 0;
  session.networkError = "";
  if (state.contextTarget?.hostId === id) {
    state.contextFile = null;
    state.contextTarget = null;
    $("#fileContextMenu").classList.add("hidden");
  }
  if (state.chmodTarget?.hostId === id) {
    state.chmodTarget = null;
    $("#chmodModal").classList.add("hidden");
  }
  if (state.editing?.hostId === id) {
    state.editing.stale = true;
    state.editing.saving = false;
    $("#saveEdit").disabled = true;
    $("#editorState").textContent = "连接已变化，不能保存";
    $("#editorState").className = state.editing.version > 0 ? "bad dirty" : "bad";
  }
  return epoch;
}
function fileTarget(hostId = state.selected, path = state.path) {
  const session = state.sessions[hostId];
  return session?.connected
    ? { hostId, connectionEpoch: session.connectionEpoch, path }
    : null;
}
function fileTargetActive(target) {
  const session = target && state.sessions[target.hostId];
  return Boolean(
    session?.connected && session.connectionEpoch === target.connectionEpoch,
  );
}
function requireFileTarget(target) {
  if (fileTargetActive(target)) return true;
  toast("SSH 会话已变化，请重新选择文件", true);
  return false;
}
function remoteEntryName(value) {
  const name = String(value || "").trim();
  if (!name) return "";
  if (name === "." || name === ".." || /[\/\\\0]/.test(name))
    throw new Error("名称不能是 . 或 ..，也不能包含斜杠或反斜杠");
  return name;
}
function remoteChildPath(parent, name) {
  return `${parent === "/" ? "" : parent.replace(/\/$/, "")}/${name}`;
}
function buildFileView(path, files) {
  files = Array.isArray(files) ? files : [];
  const dirs = files.filter((file) => file.isDir);
  const parent =
    path === "/" ? "/" : path.split("/").slice(0, -1).join("/") || "/";
  return {
    path,
    hint: `⌄　${path}`,
    tree: `<button class="tree-dir" data-path="/"><span class="folder-icon root" aria-hidden="true"></span><span>根目录 /</span></button>${path === "/" ? "" : `<button class="tree-dir" data-path="${esc(parent)}"><span class="folder-icon parent" aria-hidden="true"></span><span>上一级 ..</span></button>`}${dirs.map((file) => `<button class="tree-dir" data-path="${esc(file.path)}"><span class="folder-icon" aria-hidden="true"></span><span>${esc(file.name)}</span></button>`).join("")}`,
    rows: files.length
      ? files
          .map(
            (file, index) =>
              `<tr data-i="${index}" class="${file.isDir ? "dir" : ""}"><td title="${esc(file.name)}"><span class="file-icon ${file.isDir ? "folder" : "document"}">${file.isDir ? "◆" : "▤"}</span><span class="file-name">${esc(file.name)}</span></td><td>${file.isDir ? "—" : formatSize(file.size)}</td><td>${file.isDir ? "文件夹" : file.name.includes(".") ? esc(file.name.split(".").pop().toUpperCase()) : "文件"}</td><td>${esc(file.modified)}</td><td>${esc(file.mode)}</td><td>${file.isDir ? "" : `<button class="edit-file" type="button">编辑</button>`}</td></tr>`,
          )
          .join("")
      : '<tr><td colspan="6" class="placeholder">暂无数据</td></tr>',
  };
}
function renderFileView(view) {
  if (!view) return;
  $("#path").value = view.path;
  $("#dirHint").textContent = view.hint;
  $("#treeItems").innerHTML = view.tree;
  $("#fileRows").innerHTML = view.rows;
}
function restoreSessionView(s) {
  state.path = s.path || "/";
  state.files = s.files || [];
  resetMonitor();
  if (s.monitorOutput || s.monitorError) renderFastMonitor(s);
  renderProcesses(s);
  renderDisks(s);
  renderNetwork(s);
  if (s.latency) renderWorkspaceLatency(s.latency);
  if (s.fileView) renderFileView(s.fileView);
  else if (s.connected) {
    $("#path").value = s.path || "/";
    $("#dirHint").textContent = "正在同步目录…";
  }
}
let sessionLayoutFrame = 0;
function syncSessionHeaderLayout() {
  window.cancelAnimationFrame(sessionLayoutFrame);
  sessionLayoutFrame = window.requestAnimationFrame(() => {
    const header = $(".app-header");
    const height = Math.max(44, Math.min(120, header.offsetHeight || 44));
    document.documentElement.style.setProperty(
      "--session-header-height",
      `${height}px`,
    );
    const tabs = $("#sessionTabs");
    const active = tabs.querySelector(".session-tab.active");
    if (active) {
      const top = active.offsetTop;
      const bottom = top + active.offsetHeight;
      if (top < tabs.scrollTop) tabs.scrollTop = top;
      else if (bottom > tabs.scrollTop + tabs.clientHeight)
        tabs.scrollTop = bottom - tabs.clientHeight;
    }
    fitActiveTerminal();
  });
}
const sessionHeaderObserver = window.ResizeObserver
  ? new window.ResizeObserver(syncSessionHeaderLayout)
  : null;
sessionHeaderObserver?.observe($(".app-header"));
window.addEventListener("resize", syncSessionHeaderLayout);
function renderSessionTabs() {
  const ids = Object.keys(state.sessions);
  $("#sessionTabs").innerHTML = ids.length
    ? ids
        .map((id, i) => {
          const h = state.settings?.hosts.find((x) => x.id === id);
          const s = state.sessions[id];
          const label = h?.name || h?.address || "会话";
          const protocol = String(h?.protocol || "ssh").toUpperCase();
          const shortcut = i < 9 ? ` · Alt+${i + 1}` : "";
          const statusClass = s?.connected
            ? "online"
            : s?.connecting || s?.reconnectTimer
              ? "connecting"
              : "";
          return `<div class="session-tab ${id === state.selected ? "active" : ""}" data-id="${esc(id)}"><button class="session-select" type="button" title="${esc(label + shortcut)}" aria-label="切换到 ${esc(label)}"><span class="session-protocol ${protocol.toLowerCase()}">${esc(protocol)}</span>${i + 1} · ${esc(label)}</button><button class="session-close" type="button" title="关闭 ${protocol} 会话 · Ctrl+Shift+W">×</button><i class="${statusClass}"></i></div>`;
        })
        .join("")
    : '<div class="session-tab active empty"><button type="button">1 · 新会话</button></div>';
  syncSessionHeaderLayout();
  updateSyncCommandButton();
  updatePersistentSessionButton();
  updateTerminalToolButtons();
  updateBatchConnectButton();
}
function updateSessionTabState(id) {
  const tab = [...$("#sessionTabs").querySelectorAll(".session-tab[data-id]")]
    .find((item) => item.dataset.id === id);
  const s = state.sessions[id];
  const dot = tab?.querySelector("i");
  if (dot) {
    dot.className = s?.connected
      ? "online"
      : s?.connecting || s?.reconnectTimer
        ? "connecting"
        : "";
  }
  if (id === state.selected) updateTerminalToolButtons();
}
function selectedBatchHosts() {
  const selectedIds = state.hostSelection;
  return (state.settings?.hosts || []).filter(
    (host) => selectedIds.has(host.id) && !host.deleted && String(host.protocol || "ssh").toLowerCase() === "ssh",
  );
}
function updateBatchConnectButton() {
  const button = $("#batchConnect");
  const count = selectedBatchHosts().length;
  button.classList.toggle("hidden", count < 2);
  $("#batchConcurrencyWrap").classList.toggle("hidden", count < 2);
  if (!button.disabled) button.textContent = `批量连接 (${count})`;
}
function connectedSSHIds() {
  return Object.entries(state.sessions)
    .filter(([id, session]) => session.connected && String(state.settings?.hosts.find((host) => host.id === id)?.protocol || "ssh").toLowerCase() === "ssh")
    .map(([id]) => id);
}
function isConnectedSSHSession(id) {
  const host = state.settings?.hosts.find((item) => item.id === id);
  return Boolean(
    id &&
    state.sessions[id]?.connected &&
    String(host?.protocol || "ssh").toLowerCase() === "ssh",
  );
}
function resetTerminalSplitState() {
  state.splitHosts = [];
  $("#terminalSessions").classList.remove("split");
  $("#terminalSessions").querySelectorAll(".terminal-session").forEach((element) => {
    element.classList.remove("split-pane", "active-pane");
  });
}
function exitTerminalSplit(notify = true) {
  if (state.splitHosts.length !== 2) return;
  resetTerminalSplitState();
  $("#splitTerminalModal").classList.add("hidden");
  showTerminal(state.selected, true);
  updateTerminalToolButtons();
  if (notify) toast("已退出终端分屏");
}
function adaptTerminalSplitForSelection(id) {
  if (state.splitHosts.length !== 2 || state.splitHosts.includes(id)) return;
  if (!isConnectedSSHSession(id)) {
    resetTerminalSplitState();
    return;
  }
  const activeIndex = state.splitHosts.indexOf(state.selected);
  state.splitHosts[activeIndex >= 0 ? activeIndex : 0] = id;
}
function openTerminalSplitDialog() {
  if (state.splitHosts.length === 2) return exitTerminalSplit();
  if (!isConnectedSSHSession(state.selected)) return toast("请先连接当前 SSH", true);
  const candidates = connectedSSHIds().filter((id) => id !== state.selected);
  if (!candidates.length) return toast("还需要一台已连接的 SSH 主机", true);
  $("#splitTerminalHost").innerHTML = candidates.map((id) =>
    `<option value="${esc(id)}">${esc(hostDisplayName(id))}</option>`,
  ).join("");
  $("#splitTerminalModal").classList.remove("hidden");
  $("#splitTerminalHost").focus();
}
function toggleTerminalSplit() {
  if (state.splitHosts.length === 2) exitTerminalSplit();
  else openTerminalSplitDialog();
}
function commandTargetIds(synchronize = state.syncCommands) {
  return synchronize ? connectedSSHIds() : state.connected && state.selected ? [state.selected] : [];
}
function terminalTheme() {
  return {
    background: "#080d18",
    foreground: "#e2e8f0",
    cursor: "#38bdf8",
    cursorAccent: "#080d18",
    selectionBackground: "rgba(56, 189, 248, 0.24)",
    black: "#0f172a",
    red: "#f43f5e",
    green: "#10b981",
    yellow: "#f59e0b",
    blue: "#3b82f6",
    magenta: "#6366f1",
    cyan: "#06b6d4",
    white: "#cbd5e1",
    brightBlack: "#64748b",
    brightRed: "#fb7185",
    brightGreen: "#34d399",
    brightYellow: "#fbbf24",
    brightBlue: "#60a5fa",
    brightMagenta: "#a855f7",
    brightCyan: "#38bdf8",
    brightWhite: "#f8fafc",
  };
}
function applyTerminalScale(session) {
  if (!session?.terminal || !session.terminalHost) return;
  const scale = Number.parseFloat(
    getComputedStyle(document.documentElement).getPropertyValue("--ui-scale"),
  ) || 1;
  session.terminalHost.style.zoom = String(1 / scale);
  session.terminal.options.fontSize = 12 * scale;
  session.terminal.element.style.padding = `${10 * scale}px`;
}
function markSessionDisconnected(id) {
  const session = state.sessions[id];
  if (!session) return;
  session.connected = false;
  session.connecting = false;
  session.sessionLogActive = false;
  if (state.splitHosts.includes(id)) exitTerminalSplit(false);
  renderSessionTabs();
  stopMonitor();
  syncActiveMonitorStream();
  if (id !== state.selected) return;
  state.connected = false;
  $("#onlineDot").className = "";
  $("#sideAddress").textContent = "未连接";
  $("#copyHostIP").classList.add("hidden");
  $("#sideReconnect").classList.remove("hidden");
  resetMonitor();
}
function showTerminalReconnectBanner(id, attempt, seconds) {
  if (id !== state.selected) return;
  const banner = $("#terminalReconnectBanner");
  const text = $("#reconnectBannerText");
  if (!banner || !text) return;
  text.textContent = `⚠️ 连接已断开。第 ${attempt}/${reconnectDelays.length} 次自动重连将在 ${seconds} 秒后开始…`;
  banner.classList.remove("hidden");
}

function hideTerminalReconnectBanner() {
  const banner = $("#terminalReconnectBanner");
  if (banner) banner.classList.add("hidden");
}

function syncTerminalReconnectBanner(id = state.selected) {
  const session = id ? state.sessions[id] : null;
  if (id && id === state.selected && session?.reconnectTimer && session?.reconnectDueAt) {
    const remaining = Math.max(1, Math.ceil((session.reconnectDueAt - Date.now()) / 1000));
    showTerminalReconnectBanner(id, session.reconnectAttempt, remaining);
    return;
  }
  hideTerminalReconnectBanner();
}

const reconnectDelays = [1000, 2000, 4000, 8000, 12000, 16000, 20000, 25000, 30000, 30000];
function cancelSessionReconnect(session) {
  if (!session) return;
  if (session.reconnectTimer) {
    window.clearTimeout(session.reconnectTimer);
    session.reconnectTimer = 0;
  }
  if (session.reconnectInterval) {
    window.clearInterval(session.reconnectInterval);
    session.reconnectInterval = 0;
  }
  session.reconnectDueAt = 0;
  syncTerminalReconnectBanner();
}

async function restorePersistentTmuxIfNeeded(id, session) {
  if (!session) return;
  if (session.tmuxPersistent) {
    const command = session.persistentTool === "screen"
      ? "screen -xRR santianshell\r"
      : "tmux new-session -A -s santianshell\r";
    await enqueueTerminalInput(id, command, false);
    return;
  }
  writeTerminalOutput(id, "[重连] 已建立新 PTY；普通 SSH 断线前的前台进程无法恢复。需要持续任务请启用“持久会话”。\r\n");
}

async function triggerImmediateReconnect(id, session, reasonText = "") {
  if (!session || !session.autoReconnect || session.connected || session.connecting || session.closing) return;
  cancelSessionReconnect(session);
  if (reasonText) writeTerminalOutput(id, `\r\n[重连] ${reasonText}\r\n`);
  const result = await connectHostById(id, false, true, { reconnecting: true });
  if (result?.status === "connected") {
    session.reconnectAttempt = 0;
    writeTerminalOutput(id, "\r\n[重连] 连接已恢复。\r\n");
    await restorePersistentTmuxIfNeeded(id, session);
    return;
  }
  scheduleSessionReconnect(id);
}

function scheduleSessionReconnect(id) {
  const session = state.sessions[id];
  if (!session?.autoReconnect || session.connected || session.connecting || session.closing || session.reconnectTimer) return;
  if (session.reconnectAttempt >= reconnectDelays.length) {
    syncTerminalReconnectBanner();
    writeTerminalOutput(id, `\r\n[重连] 已达到 ${reconnectDelays.length} 次上限，请检查网络后手动重连。\r\n`);
    if (id === state.selected) toast("自动重连失败，请检查网络后手动重连", true);
    updateSessionTabState(id);
    return;
  }
  const attempt = ++session.reconnectAttempt;
  const delay = reconnectDelays[attempt - 1];
  session.reconnectDueAt = Date.now() + delay;
  writeTerminalOutput(id, `\r\n[重连] 第 ${attempt}/${reconnectDelays.length} 次将在 ${Math.ceil(delay / 1000)} 秒后开始…\r\n`);
  showTerminalReconnectBanner(id, attempt, Math.ceil(delay / 1000));
  updateSessionTabState(id);
  window.clearInterval(session.reconnectInterval);
  session.reconnectInterval = window.setInterval(() => {
    const remaining = Math.max(0, Math.ceil((session.reconnectDueAt - Date.now()) / 1000));
    if (id === state.selected && remaining > 0) {
      showTerminalReconnectBanner(id, attempt, remaining);
    }
  }, 1000);
  session.reconnectTimer = window.setTimeout(async () => {
    session.reconnectTimer = 0;
    if (session.reconnectInterval) {
      window.clearInterval(session.reconnectInterval);
      session.reconnectInterval = 0;
    }
    session.reconnectDueAt = 0;
    syncTerminalReconnectBanner();
    if (!session.autoReconnect || session.connected || session.connecting || session.closing) return;
    const result = await connectHostById(id, false, true, { reconnecting: true });
    if (result?.status === "connected") {
      session.reconnectAttempt = 0;
      writeTerminalOutput(id, "\r\n[重连] 连接已恢复。\r\n");
      await restorePersistentTmuxIfNeeded(id, session);
      return;
    }
    scheduleSessionReconnect(id);
  }, delay);
}
const terminalInputChunkChars = 32 * 1024;
function splitTerminalInputChunks(text) {
  const value = String(text || "");
  if (value.length <= terminalInputChunkChars) return value ? [value] : [];
  const chunks = [];
  let offset = 0;
  while (offset < value.length) {
    let end = Math.min(value.length, offset + terminalInputChunkChars);
    if (end < value.length) {
      const code = value.charCodeAt(end - 1);
      if (code >= 0xd800 && code <= 0xdbff) end -= 1;
    }
    if (end <= offset) end = Math.min(value.length, offset + terminalInputChunkChars);
    chunks.push(value.slice(offset, end));
    offset = end;
  }
  return chunks;
}
function createTerminalInputBatcher(id, epoch) {
  const session = state.sessions[id];
  if (!session) return Promise.resolve({ id, sent: false, stale: true });
  if (session.pendingInputEpoch !== epoch) {
    session.pendingInputBuffer = "";
    session.pendingInputEpoch = epoch;
  }
  if (!session.terminalInputBatchPromise) {
    session.terminalInputBatchPromise = new Promise((resolve) => {
      const runFlush = () => {
        session.terminalInputBatchTimer = 0;
        const payload = session.pendingInputEpoch === epoch ? session.pendingInputBuffer : "";
        session.pendingInputBuffer = "";
        session.terminalInputBatchPromise = null;
        if (!payload) {
          resolve({ id, sent: true });
          return;
        }
        const job = session.terminalInputChain.catch(() => {}).then(async () => {
          session.terminalInputInFlight += 1;
          try {
            const chunks = splitTerminalInputChunks(payload);
            for (const chunk of chunks) {
              if (!session.connected || session.connectionEpoch !== epoch) {
                return { id, sent: false, stale: true };
              }
              try {
                await api().WriteTerminal(id, epoch, chunk);
              } catch (error) {
                const busy = /AI 正在使用当前终端|AI 正在操作当前终端/.test(String(error));
                return { id, sent: false, error, busy };
              }
            }
            return { id, sent: true };
          } finally {
            session.terminalInputInFlight = Math.max(0, session.terminalInputInFlight - 1);
          }
        });
        session.terminalInputChain = job.then(() => undefined);
        job.then(resolve);
      };
      if (session.terminalInputInFlight === 0 && session.pendingInputBuffer.length < terminalInputChunkChars) {
        queueMicrotask(runFlush);
      } else {
        session.terminalInputBatchTimer = window.setTimeout(runFlush, 12);
      }
    });
  }
  return session.terminalInputBatchPromise;
}
function enqueueTerminalInput(sourceId, input, synchronize = state.syncCommands) {
  if (!input) return Promise.resolve({ sent: 0, failed: [] });
  if (!sourceId || !state.sessions[sourceId]?.connected) {
    toast("请先连接 SSH", true);
    return Promise.resolve({ sent: 0, failed: [] });
  }
  const targetIds = synchronize ? connectedSSHIds() : [sourceId];
  if (!targetIds.length) {
    toast("没有已连接的 SSH 会话", true);
    return Promise.resolve({ sent: 0, failed: [] });
  }
  const targets = targetIds.map((id) => ({
    id,
    epoch: state.sessions[id]?.connectionEpoch,
  }));
  const isInterrupt = /^[\x03]+$/.test(input);
  const jobs = targets.map(({ id, epoch }) => {
    const session = state.sessions[id];
    if (!session) return Promise.resolve({ id, sent: false, stale: true });
    if (isInterrupt) {
      if (!session.connected || session.connectionEpoch !== epoch) {
        return Promise.resolve({ id, sent: false, stale: true });
      }
      return api().WriteTerminal(id, epoch, input).then(
        () => ({ id, sent: true }),
        (error) => ({
          id,
          sent: false,
          error,
          busy: /AI 正在使用当前终端|AI 正在操作当前终端/.test(String(error)),
        }),
      );
    }
    if (session.pendingInputEpoch !== epoch) {
      session.pendingInputBuffer = "";
      session.pendingInputEpoch = epoch;
    }
    session.pendingInputBuffer += input;
    return createTerminalInputBatcher(id, epoch);
  });
  return Promise.all(jobs).then((results) => {
    const busy = results.filter((item) => item.busy);
    const failed = results.filter((item) => item.error && !item.busy);
    for (const item of failed) {
      const session = state.sessions[item.id];
      const target = targets.find(({ id }) => id === item.id);
      if (session && session.connectionEpoch === target?.epoch) {
        advanceConnectionEpoch(session, item.id);
        markSessionDisconnected(item.id);
      }
    }
    if (failed.length) {
      toast(`终端输入发送失败：${failed.map(({ id }) => hostDisplayName(id)).join("、")}`, true);
    } else if (busy.length) {
      toast(`AI 正在操作 ${busy.map(({ id }) => hostDisplayName(id)).join("、")}，本次输入未发送`, true);
    }
    return {
      sent: results.filter((item) => item.sent).length,
      failed,
    };
  });
}
function scheduleTerminalResize(id, rows, cols) {
  const session = state.sessions[id];
  if (!session || rows < 1 || cols < 1) return;
  const epoch = session.connectionEpoch;
  window.clearTimeout(session.terminalResizeTimer);
  session.terminalResizeTimer = window.setTimeout(() => {
    if (!session.connected || session.connectionEpoch !== epoch || (session.terminalRows === rows && session.terminalCols === cols)) return;
    session.terminalResizeChain = session.terminalResizeChain.catch(() => {}).then(async () => {
      if (!session.connected || session.connectionEpoch !== epoch) return;
      await api().ResizeTerminal(id, epoch, rows, cols);
      if (session.connectionEpoch !== epoch) return;
      session.terminalRows = rows;
      session.terminalCols = cols;
    }).catch((error) => {
      if (session.connected && session.connectionEpoch === epoch) console.warn("同步终端尺寸失败", error);
    });
  }, 80);
}
function updateTerminalSearchResult(id, result) {
  if (id !== state.selected || $("#terminalSearchModal").classList.contains("hidden")) return;
  const count = Number(result?.resultCount) || 0;
  const index = Number(result?.resultIndex);
  $("#terminalSearchResult").textContent = count > 0
    ? Number.isFinite(index) && index >= 0 ? `${index + 1} / ${count}` : `${count} 个结果`
    : "未找到匹配内容";
}
function terminalSearchOptions(incremental = false) {
  return {
    caseSensitive: false,
    wholeWord: false,
    regex: false,
    incremental,
    decorations: {
      matchBackground: "#6b531e",
      matchBorder: "#e5b94f",
      matchOverviewRuler: "#e5b94f",
      activeMatchBackground: "#1677ff",
      activeMatchBorder: "#b7d6ff",
      activeMatchColorOverviewRuler: "#1677ff",
    },
  };
}
function searchCurrentTerminal(direction = "next", incremental = false) {
  const session = state.sessions[state.selected];
  const term = $("#terminalSearchInput").value;
  if (!session?.searchAddon || !term) {
    session?.searchAddon?.clearDecorations();
    $("#terminalSearchResult").textContent = "输入关键词开始搜索";
    return false;
  }
  const found = direction === "previous"
    ? session.searchAddon.findPrevious(term, terminalSearchOptions())
    : session.searchAddon.findNext(term, terminalSearchOptions(incremental));
  if (!found) $("#terminalSearchResult").textContent = "未找到匹配内容";
  return found;
}
function openTerminalSearch() {
  const host = selected();
  const session = state.sessions[state.selected];
  if (!host || String(host.protocol || "ssh").toLowerCase() !== "ssh" || !session?.terminal) {
    return toast("当前页面没有可搜索的 SSH 终端", true);
  }
  $("#terminalSearchInput").value = "";
  $("#terminalSearchResult").textContent = "输入关键词开始搜索";
  $("#terminalSearchModal").classList.remove("hidden");
  window.requestAnimationFrame(() => $("#terminalSearchInput").focus());
}
function closeTerminalSearch(refocus = true) {
  const modal = $("#terminalSearchModal");
  if (modal.classList.contains("hidden")) return;
  modal.classList.add("hidden");
  const session = state.sessions[state.selected];
  session?.searchAddon?.clearDecorations();
  if (refocus) session?.terminal?.focus();
}
function ensureTerminal(id) {
  const session = sessionFor(id);
  if (session.terminal) return session;
  const element = document.createElement("div");
  element.className = "terminal-session";
  element.dataset.id = id;
  $("#terminalSessions").append(element);
  const paneLabel = document.createElement("div");
  paneLabel.className = "terminal-pane-label";
  paneLabel.title = "点击激活此终端";
  element.append(paneLabel);
  const terminalHost = document.createElement("div");
  terminalHost.className = "terminal-fit-host";
  element.append(terminalHost);
  const terminal = new Terminal({
    convertEol: false,
    cursorBlink: true,
    cursorStyle: "bar",
    fontFamily: 'Consolas, "Microsoft YaHei Mono", monospace',
    fontSize: 12,
    lineHeight: 1.2,
    scrollback: 10000,
    scrollOnUserInput: true,
    theme: terminalTheme(),
  });
  const fitAddon = new FitAddon();
  const searchAddon = new SearchAddon();
  terminal.loadAddon(fitAddon);
  terminal.loadAddon(searchAddon);
  terminal.open(terminalHost);
  if (id !== state.selected) element.classList.add("hidden");
  terminal.attachCustomKeyEventHandler((event) => {
    if (event.type !== "keydown" || !event.ctrlKey || event.altKey || event.key.toLowerCase() !== "c") return true;
    if (!terminal.hasSelection()) return true;
    event.preventDefault();
    window.runtime?.ClipboardSetText?.(terminal.getSelection())?.catch((error) =>
      toast(`复制失败：${String(error)}`, true),
    );
    terminal.clearSelection();
    terminal.scrollToBottom();
    return false;
  });
  session.terminal = terminal;
  session.fitAddon = fitAddon;
  session.searchAddon = searchAddon;
  session.terminalElement = element;
  session.terminalHost = terminalHost;
  session.terminalPaneLabel = paneLabel;
  session.terminalDisposables = [
    terminal.onData((input) => enqueueTerminalInput(id, input)),
    terminal.onResize(({ rows, cols }) => scheduleTerminalResize(id, rows, cols)),
    searchAddon.onDidChangeResults((result) => updateTerminalSearchResult(id, result)),
  ];
  element.addEventListener("pointerdown", (event) => {
    if (event.button !== 0 || state.selected === id || !state.splitHosts.includes(id)) return;
    selectHost(id);
  });
  applyTerminalScale(session);
  if (session.output) terminal.write(session.output.replace(/\r?\n/g, "\r\n"));
  return session;
}
function fitTerminal(id, focus = false) {
  const session = state.sessions[id];
  if (!session?.terminal || session.terminalElement.classList.contains("hidden")) return;
  window.cancelAnimationFrame(session.terminalScrollFrame);
  session.terminalScrollFrame = window.requestAnimationFrame(() => {
    if (!session.terminal || session.terminalElement.classList.contains("hidden")) return;
    try {
      session.fitAddon.fit();
      scheduleTerminalResize(id, session.terminal.rows, session.terminal.cols);
    } catch (error) {
      console.warn("调整终端显示失败", error);
    }
    if (!session.terminal.hasSelection()) session.terminal.scrollToBottom();
    if (focus && id === state.selected) session.terminal.focus();
  });
}
function fitActiveTerminal() {
  if (!state.selected || !monitorPageVisible()) return;
  const ids = state.splitHosts.length === 2 ? state.splitHosts : [state.selected];
  ids.forEach((id) => fitTerminal(id));
}
function appendRDPLog(hostId, message) {
  state.rdpLogs = state.rdpLogs || {};
  state.rdpLogs[hostId] = state.rdpLogs[hostId] || [];
  const time = new Date().toLocaleTimeString("zh-CN", { hour12: false });
  state.rdpLogs[hostId].push({ time, message });
  if (state.rdpLogs[hostId].length > 100) state.rdpLogs[hostId].shift();
  if (state.selected === hostId) {
    const body = $("#rdpLogBody");
    if (body) {
      body.innerHTML = state.rdpLogs[hostId]
        .map((item) => `<div class="rdp-log-item"><span class="time">[${item.time}]</span><span>${esc(item.message)}</span></div>`)
        .join("");
      body.scrollTop = body.scrollHeight;
    }
  }
}

function renderRDPDeck(id) {
  const deck = $("#rdpDeck");
  if (!deck) return;
  const host = state.settings?.hosts.find((item) => item.id === id);
  if (!host) {
    deck.classList.add("hidden");
    return;
  }
  deck.classList.remove("hidden");
  const session = state.sessions[id];
  const rdpInfo = session?.rdpSessionId ? state.rdpSessions[session.rdpSessionId] : null;

  let statusState = "idle";
  let statusText = "未连接 / 空闲";
  if (session?.connecting) {
    statusState = "launching";
    statusText = "正在启动 mstsc…";
  } else if (session?.connected && rdpInfo?.state === "running") {
    statusState = "running";
    statusText = `mstsc 运行中 · PID ${rdpInfo.pid || "—"}`;
  } else if (rdpInfo?.state === "failed") {
    statusState = "failed";
    statusText = `连接失败: ${rdpInfo.error || "未知异常"}`;
  } else if (session?.connected) {
    statusState = "running";
    statusText = "已连接 (mstsc 运行中)";
  }

  let routeType = "直连模式 (Direct TCP)";
  let routeBadge = "直连";
  let routeIcon = "⚡";
  let routeFrom = "本地客户端";
  let routeMid = "";
  let routeTo = `${host.address}:${host.port || 3389}`;
  let routeDetail = "直连目标服务器 RDP 端口";

  if (host.rdpJumpHostId) {
    const jumpHost = state.settings?.hosts.find((h) => h.id === host.rdpJumpHostId);
    routeType = "SSH 跳板机加密隧道";
    routeBadge = "SSH 跳板";
    routeIcon = "🛡️";
    routeMid = `跳板机 [${jumpHost?.name || jumpHost?.address || "跳板主机"}]`;
    routeDetail = `通过 SSH 跳板机建立本地安全隧道并转发至 ${routeTo}`;
  } else if (host.proxyEnabled) {
    routeType = "SOCKS5 代理通道";
    routeBadge = "SOCKS5 代理";
    routeIcon = "🌐";
    routeMid = `代理 ${host.proxyHost}:${host.proxyPort}`;
    routeDetail = `通过本地 SOCKS5 代理隧道转发至 ${routeTo}`;
  } else if (host.rdpGatewayAddress) {
    routeType = "RD Gateway 远程桌面网关";
    routeBadge = "RD 网关";
    routeIcon = "🚪";
    routeMid = `网关 ${host.rdpGatewayAddress}`;
    routeDetail = `通过 Microsoft RD 网关 ${host.rdpGatewayAddress} 接入`;
  }

  const latVal = state.rdpLatencies?.[id] || state.latencies?.[id]?.latency;
  const latText = latVal != null ? `${latVal} ms` : "未测速";

  const resolution = host.rdpResolution || "1280x1024";
  const displayMode = host.rdpDisplayMode === "fullscreen" ? "全屏模式" : "窗口模式";
  const smartSizing = host.rdpSmartSizing ? "已开启 (自适应拉伸)" : "已关闭";
  const clipboard = host.rdpRedirectClipboard !== false ? "已开启" : "已禁用";
  const drives = host.rdpMapDrives ? "本地磁盘重定向 (已启用)" : "已禁用";
  const multimon = host.rdpUseMultimon ? "多显示器扩展" : "单显示器";
  const audioPlaybackModes = ["本地播放", "远端设备播放", "禁用音频"];
  const audioPlay = audioPlaybackModes[host.rdpAudioPlayback || 0] || "本地播放";
  const audioCap = host.rdpAudioCapture ? "麦克风重定向" : "已禁用";
  const printers = host.rdpRedirectPrinters ? "已开启" : "已禁用";

  state.rdpLogs = state.rdpLogs || {};
  const logs = state.rdpLogs[id] || [];
  const logsHtml = logs.length
    ? logs.map((item) => `<div class="rdp-log-item"><span class="time">[${item.time}]</span><span>${esc(item.message)}</span></div>`).join("")
    : '<div class="rdp-log-placeholder">等待启动远程桌面…</div>';

  deck.innerHTML = `
    <div class="rdp-deck-header">
      <div class="rdp-hero-identity">
        <div class="rdp-icon-wrap">🖥️</div>
        <div class="rdp-title-meta">
          <div class="rdp-title-line">
            <h2>${esc(host.name || host.address || "RDP 主机")}</h2>
            <span class="session-protocol rdp">RDP</span>
            <span class="rdp-status-badge ${statusState}">${esc(statusText)}</span>
          </div>
          <div class="rdp-address-line">
            <span>${esc(host.address)}:${host.port || 3389}</span>
            <span class="rdp-divider">·</span>
            <span>用户: ${esc(host.username || "administrator")}</span>
            <span class="rdp-divider">·</span>
            <span id="rdpDeckLatencyText">延迟: ${latText}</span>
          </div>
        </div>
      </div>
      <div class="rdp-hero-actions">
        <button type="button" id="rdpLaunchBtn" class="primary">${statusState === "running" ? "重新连接" : "启动远程桌面"}</button>
        <button type="button" id="rdpTerminateBtn" class="danger-btn ${statusState === "running" ? "" : "hidden"}">终止 RDP</button>
        <button type="button" id="rdpTestLatencyBtn">连通性检测</button>
        <button type="button" id="rdpEditHostBtn">编辑配置</button>
        <button type="button" id="rdpCopyInfoBtn">复制信息</button>
      </div>
    </div>

    <div class="rdp-deck-grid">
      <div class="rdp-card rdp-route-card">
        <div class="rdp-card-head">
          <b>连接拓扑与安全路由</b>
          <span class="rdp-chip">${esc(routeBadge)}</span>
        </div>
        <div class="rdp-route-diagram">
          <span class="rdp-route-node">💻 ${esc(routeFrom)}</span>
          <span class="rdp-route-arrow">➔</span>
          ${routeMid ? `<span class="rdp-route-node">${routeIcon} ${esc(routeMid)}</span><span class="rdp-route-arrow">➔</span>` : ""}
          <span class="rdp-route-node">🖥️ ${esc(routeTo)}</span>
        </div>
        <div class="rdp-route-specs">
          <div class="rdp-spec-item"><span>连接方式</span><b>${esc(routeType)}</b></div>
          <div class="rdp-spec-item"><span>路由描述</span><b>${esc(routeDetail)}</b></div>
          <div class="rdp-spec-item"><span>凭据配置</span><b>${host.password ? "已保存安全密码" : "需在 mstsc 中输入"}</b></div>
          <div class="rdp-spec-item"><span>网关状态</span><b>${host.rdpGatewayAddress ? esc(host.rdpGatewayAddress) : "未使用"}</b></div>
        </div>
      </div>

      <div class="rdp-card rdp-specs-card">
        <div class="rdp-card-head"><b>会话环境与重定向特性</b></div>
        <div class="rdp-specs-grid">
          <div class="rdp-spec-item"><span>桌面分辨率</span><b>${esc(resolution)}</b></div>
          <div class="rdp-spec-item"><span>显示模式</span><b>${esc(displayMode)} · ${esc(smartSizing)}</b></div>
          <div class="rdp-spec-item"><span>剪贴板同步</span><b>${esc(clipboard)}</b></div>
          <div class="rdp-spec-item"><span>本地磁盘映射</span><b>${esc(drives)}</b></div>
          <div class="rdp-spec-item"><span>多显示器</span><b>${esc(multimon)}</b></div>
          <div class="rdp-spec-item"><span>音频与麦克风</span><b>${esc(audioPlay)} · ${esc(audioCap)}</b></div>
          <div class="rdp-spec-item"><span>打印机重定向</span><b>${esc(printers)}</b></div>
          <div class="rdp-spec-item"><span>连接超时 / 保活</span><b>${host.timeout || 12}s / ${host.keepAlive || 30}s</b></div>
        </div>
      </div>

      <div class="rdp-card rdp-log-card">
        <div class="rdp-card-head">
          <b>会话运行日志</b>
          <div class="rdp-card-tools">
            <button type="button" id="rdpClearLogBtn">清空</button>
            <button type="button" id="rdpCopyLogBtn">复制日志</button>
          </div>
        </div>
        <div class="rdp-log-body" id="rdpLogBody">
          ${logsHtml}
        </div>
      </div>
    </div>
  `;

  $("#rdpLaunchBtn").onclick = async () => {
    $("#rdpLaunchBtn").disabled = true;
    appendRDPLog(id, "用户手动触发启动远程桌面…");
    renderRDPDeck(id);
    try {
      await connectHostById(id);
    } catch (e) {
      toast(String(e), true);
    } finally {
      renderRDPDeck(id);
    }
  };

  $("#rdpTerminateBtn").onclick = async () => {
    if (session?.rdpSessionId) {
      $("#rdpTerminateBtn").disabled = true;
      appendRDPLog(id, `正在终止 RDP 会话 (ID: ${session.rdpSessionId.slice(0, 8)})…`);
      try {
        await api().CloseRDP(session.rdpSessionId);
        toast("已发送终止 RDP 指令");
        appendRDPLog(id, "已终止远程桌面进程与关联隧道");
      } catch (e) {
        toast(String(e), true);
      } finally {
        session.connected = false;
        session.rdpSessionId = "";
        renderRDPDeck(id);
        updateSessionTabState(id);
      }
    }
  };

  $("#rdpTestLatencyBtn").onclick = async () => {
    const btn = $("#rdpTestLatencyBtn");
    btn.disabled = true;
    btn.textContent = "检测中…";
    appendRDPLog(id, `正在检测目标 ${host.address}:${host.port || 3389} 连通性与延迟…`);
    try {
      const res = await api().TestHostConfig(host);
      const text = typeof res === "object" ? res.Output || "可连接" : String(res);
      appendRDPLog(id, `连通性检测结果: ${text}`);
      const match = text.match(/(\d+)\s*ms/i);
      if (match) {
        state.rdpLatencies = state.rdpLatencies || {};
        state.rdpLatencies[id] = match[1];
        const latElem = $("#rdpDeckLatencyText");
        if (latElem) latElem.textContent = `延迟: ${match[1]} ms`;
      }
      toast(text);
    } catch (e) {
      appendRDPLog(id, `连通性检测失败: ${String(e)}`);
      toast(`连通性检测失败: ${String(e)}`, true);
    } finally {
      btn.disabled = false;
      btn.textContent = "连通性检测";
    }
  };

  $("#rdpEditHostBtn").onclick = () => {
    openHost(host);
  };

  $("#rdpCopyInfoBtn").onclick = () => {
    const info = `主机: ${host.name || "RDP"}\n地址: ${host.address}:${host.port || 3389}\n协议: RDP\n用户: ${host.username || "administrator"}\n方式: ${routeType}`;
    navigator.clipboard?.writeText?.(info).then(() => toast("已复制 RDP 连接信息")).catch(() => toast(info));
  };

  $("#rdpClearLogBtn").onclick = () => {
    state.rdpLogs = state.rdpLogs || {};
    state.rdpLogs[id] = [];
    $("#rdpLogBody").innerHTML = '<div class="rdp-log-placeholder">日志已清空</div>';
  };

  $("#rdpCopyLogBtn").onclick = () => {
    const logLines = (state.rdpLogs?.[id] || []).map((x) => `[${x.time}] ${x.message}`).join("\n");
    if (!logLines) return toast("暂无日志可复制");
    navigator.clipboard?.writeText?.(logLines).then(() => toast("已复制 RDP 运行日志")).catch(() => toast("复制失败", true));
  };

  const body = $("#rdpLogBody");
  if (body) body.scrollTop = body.scrollHeight;
}

function showTerminal(id, focus = false) {
  const container = $("#terminalSessions");
  container.classList.remove("split");
  container.querySelectorAll(".terminal-session").forEach((element) => {
    element.classList.add("hidden");
    element.classList.remove("split-pane", "active-pane");
  });
  const host = state.settings?.hosts.find((item) => item.id === id);
  if (!id) {
    $(".center-work")?.classList.remove("rdp-mode");
    $("#rdpDeck")?.classList.add("hidden");
    resetTerminalSplitState();
    $("#terminalEmpty").textContent = "SanTianShell 安全远程终端\n选择主机后点击“连接 SSH”。";
    $("#terminalEmpty").classList.remove("hidden");
    hideTerminalReconnectBanner();
    updateTerminalToolButtons();
    return;
  }
  if ((host?.protocol || "ssh") === "rdp") {
    resetTerminalSplitState();
    $(".center-work")?.classList.add("rdp-mode");
    $("#terminalEmpty")?.classList.add("hidden");
    hideTerminalReconnectBanner();
    renderRDPDeck(id);
    updateTerminalToolButtons();
    return;
  }
  $(".center-work")?.classList.remove("rdp-mode");
  $("#rdpDeck")?.classList.add("hidden");
  syncTerminalReconnectBanner(id);
  const splitValid = state.splitHosts.length === 2 &&
    state.splitHosts.includes(id) &&
    state.splitHosts[0] !== state.splitHosts[1] &&
    state.splitHosts.every(isConnectedSSHSession);
  if (state.splitHosts.length && !splitValid) resetTerminalSplitState();
  if (splitValid) {
    container.classList.add("split");
    $("#terminalEmpty").classList.add("hidden");
    state.splitHosts.forEach((hostID, index) => {
      const pane = ensureTerminal(hostID);
      pane.terminalPaneLabel.textContent = `${index + 1} · ${hostDisplayName(hostID)}`;
      pane.terminalElement.classList.remove("hidden");
      pane.terminalElement.classList.add("split-pane");
      pane.terminalElement.classList.toggle("active-pane", hostID === id);
      fitTerminal(hostID, focus && hostID === id);
    });
    updateTerminalToolButtons();
    return;
  }
  const session = ensureTerminal(id);
  $("#terminalEmpty").classList.add("hidden");
  session.terminalElement.classList.remove("hidden");
  fitTerminal(id, focus);
  updateTerminalToolButtons();
}
function measureInitialTerminalSize(id) {
  const session = ensureTerminal(id);
  let rows = 0;
  let cols = 0;
  try {
    if (session.terminalElement && session.fitAddon) {
      if (state.selected === id && session.terminalElement.classList.contains("hidden")) {
        $("#terminalEmpty")?.classList.add("hidden");
        session.terminalElement.classList.remove("hidden");
      }
      if (!session.terminalElement.classList.contains("hidden")) {
        session.fitAddon.fit();
      }
      rows = Number(session.terminal?.rows) || 0;
      cols = Number(session.terminal?.cols) || 0;
    }
  } catch {}
  if (rows > 0 && cols > 0) {
    session.terminalRows = rows;
    session.terminalCols = cols;
    return { rows, cols };
  }
  return { rows: 40, cols: 120 };
}
function resetTerminal(id, text) {
  const session = ensureTerminal(id);
  session.output = text;
  session.terminalRows = 0;
  session.terminalCols = 0;
  session.terminal.reset();
  session.terminal.clear();
  session.terminal.write(text.replace(/\r?\n/g, "\r\n"), () => {
    if (id === state.selected) session.terminal.scrollToBottom();
  });
}
function disposeTerminal(id) {
  if (state.splitHosts.includes(id)) resetTerminalSplitState();
  const session = state.sessions[id];
  if (!session?.terminal) return;
  if (id === state.selected) closeTerminalSearch(false);
  window.clearTimeout(session.terminalResizeTimer);
  window.cancelAnimationFrame(session.terminalScrollFrame);
  session.terminalDisposables.forEach((disposable) => disposable.dispose());
  session.terminal.dispose();
  session.terminalElement.remove();
  session.terminal = null;
  session.fitAddon = null;
  session.searchAddon = null;
  session.terminalElement = null;
  session.terminalHost = null;
  session.terminalPaneLabel = null;
  session.terminalDisposables = [];
}
function discardSession(id) {
  const session = state.sessions[id];
  if (!session) return;
  cancelSSHInteractionsForHost(id);
  session.autoReconnect = false;
  cancelSessionReconnect(session);
  advanceConnectionEpoch(session, id);
  session.connected = false;
  session.connecting = false;
  disposeTerminal(id);
  delete state.sessions[id];
  stopMonitor();
  syncActiveMonitorStream();
  if (state.selected === id) {
    state.selected = "";
    state.connected = false;
    state.path = "/";
    state.files = [];
    $("#onlineDot").className = "";
    $("#sideAddress").textContent = "未连接";
    showSideIdentity();
    $("#copyHostIP").classList.add("hidden");
    $("#sideReconnect").classList.add("hidden");
    showTerminal("");
    resetMonitor();
  }
  renderSessionTabs();
}
if (window.ResizeObserver) {
  new ResizeObserver(fitActiveTerminal).observe($("#terminalSessions"));
}
function updateSyncCommandButton() {
  const button = $("#syncCommandBtn");
  if (!button) return;
  const count = connectedSSHIds().length;
  button.classList.toggle("active", state.syncCommands);
  button.setAttribute("aria-pressed", String(state.syncCommands));
  button.title = state.syncCommands ? `同步已开启：命令将发送到 ${count} 个已连接 SSH` : "同步已关闭：命令只发送到当前 SSH";
}
function updatePersistentSessionButton() {
  const button = $("#persistentSessionBtn");
  if (!button) return;
  const session = state.sessions[state.selected];
  const host = state.settings?.hosts.find((item) => item.id === state.selected);
  const available = Boolean(session?.connected && (host?.protocol || "ssh") === "ssh");
  button.disabled = !available;
  button.classList.toggle("active", Boolean(session?.tmuxPersistent));
  button.setAttribute("aria-pressed", String(Boolean(session?.tmuxPersistent)));
  button.title = session?.tmuxPersistent
    ? `已启用 ${session.persistentTool || "持久"} 会话，断线重连后会自动附加`
    : "检测并进入服务器已有的 tmux/screen 会话";
}
function applySSHSessionLogStatus(session, status = {}) {
  session.sessionLogEnabled = Boolean(status.enabled);
  session.sessionLogActive = Boolean(status.active);
  session.sessionLogPath = String(status.path || "");
}
function updateTerminalToolButtons() {
  const host = selected();
  const session = state.sessions[state.selected];
  const ssh = Boolean(host && String(host.protocol || "ssh").toLowerCase() === "ssh");
  const connected = Boolean(ssh && session?.connected);
  $("#historyBtn").disabled = !connected;
  $("#clearTerminalBtn").disabled = !session?.terminal;
  $("#terminalSearchBtn").disabled = !ssh || !session?.terminal;
  const splitButton = $("#splitTerminalBtn");
  const splitActive = state.splitHosts.length === 2;
  splitButton.disabled = !splitActive && (!connected || connectedSSHIds().every((id) => id === state.selected));
  splitButton.classList.toggle("active", splitActive);
  splitButton.setAttribute("aria-pressed", String(splitActive));
  splitButton.textContent = splitActive ? "分屏中" : "分屏";
  splitButton.title = `${splitActive ? "退出" : "开启"}终端分屏（Ctrl+Shift+P）`;
  const forwardButton = $("#portForwardBtn");
  const sshHostCount = availableSSHHosts().length;
  forwardButton.disabled = sshHostCount === 0;
  forwardButton.title = sshHostCount
    ? `管理 SSH 隧道（${sshHostCount} 台可用主机）`
    : "请先添加 SSH 主机";
  const logButton = $("#sessionLogBtn");
  logButton.disabled = !connected || Boolean(session?.sessionLogLoading);
  logButton.classList.toggle("active", Boolean(session?.sessionLogEnabled));
  logButton.setAttribute("aria-pressed", String(Boolean(session?.sessionLogEnabled)));
  logButton.textContent = session?.sessionLogEnabled
    ? session.sessionLogActive ? "日志中" : "日志待续"
    : "日志";
  logButton.title = session?.sessionLogEnabled
    ? `${session.sessionLogActive ? "会话日志已启用" : "等待 SSH 重连后继续记录"}${session.sessionLogPath ? `：${session.sessionLogPath}` : ""}`
    : "开启当前 SSH 会话日志";
}
async function refreshSSHSessionLogStatus(id) {
  const session = state.sessions[id];
  const host = state.settings?.hosts.find((item) => item.id === id);
  if (!session) return;
  if (!host || String(host.protocol || "ssh").toLowerCase() !== "ssh") {
    applySSHSessionLogStatus(session);
    if (id === state.selected) updateTerminalToolButtons();
    return;
  }
  const request = ++session.sessionLogRequest;
  try {
    const status = await api().GetSSHSessionLogStatus(id);
    if (state.sessions[id] !== session || session.sessionLogRequest !== request) return;
    applySSHSessionLogStatus(session, status);
  } catch (error) {
    console.warn("读取 SSH 会话日志状态失败", error);
  } finally {
    if (id === state.selected) updateTerminalToolButtons();
  }
}
async function toggleSSHSessionLogging() {
  const id = state.selected;
  const session = state.sessions[id];
  const host = selected();
  if (!session?.connected || !host || String(host.protocol || "ssh").toLowerCase() !== "ssh") {
    return toast("请先连接 SSH", true);
  }
  const enabled = !session.sessionLogEnabled;
  const request = ++session.sessionLogRequest;
  session.sessionLogLoading = true;
  updateTerminalToolButtons();
  try {
    const status = await api().SetSSHSessionLogging(id, enabled);
    if (state.sessions[id] !== session || session.sessionLogRequest !== request) return;
    applySSHSessionLogStatus(session, status);
    const path = status.path ? `：${status.path}` : "";
    toast(status.enabled ? `会话日志已启用${path}` : `会话日志已关闭${path}`);
  } catch (error) {
    toast(`切换会话日志失败：${String(error)}`, true);
    refreshSSHSessionLogStatus(id);
  } finally {
    session.sessionLogLoading = false;
    if (id === state.selected) updateTerminalToolButtons();
  }
}
function hostDisplayName(id) {
  const host = state.settings?.hosts.find((item) => item.id === id);
  return host?.name || host?.address || id;
}
function availableSSHHosts() {
  return (state.settings?.hosts || []).filter(
    (host) =>
      !host.deleted &&
      String(host.protocol || "ssh").toLowerCase() === "ssh",
  );
}
function portForwardKindText(kind) {
  return {
    local: "本地",
    remote: "远程",
    dynamic: "动态 SOCKS",
  }[String(kind || "").toLowerCase()] || kind || "转发";
}
function setPortForwardStatus(message = "", bad = false) {
  const status = $("#portForwardStatus");
  status.textContent = message;
  status.classList.toggle("bad", bad && Boolean(message));
}
function renderPortForwards() {
  const items = Array.isArray(state.portForwards) ? state.portForwards : [];
  $("#portForwardList").innerHTML = items.length
    ? items.map((item) => {
        const target = item.targetAddress ? ` → ${item.targetAddress}` : "";
        const error = item.lastError
          ? `<small class="bad" title="${esc(item.lastError)}">${esc(item.lastError)}</small>`
          : `<small>${esc(item.state || "运行中")}</small>`;
        return `<article class="port-forward-item"><div><b>${esc(portForwardKindText(item.kind))} · ${esc(hostDisplayName(item.hostId))}</b><span title="${esc((item.listenAddress || "") + target)}">${esc(item.listenAddress || "等待监听")}${esc(target)}</span>${error}</div><button class="stopPortForward" type="button" data-forward-id="${esc(item.id)}">停止</button></article>`;
      }).join("")
    : '<div class="port-forward-empty">暂无运行中的转发</div>';
}
async function refreshPortForwards(showError = true) {
  const button = $("#refreshPortForwards");
  const request = ++state.portForwardRequest;
  button.disabled = true;
  button.textContent = "刷新中";
  try {
    const items = (await api().ListPortForwards()) || [];
    if (request !== state.portForwardRequest) return false;
    state.portForwards = items;
    renderPortForwards();
    return true;
  } catch (error) {
    if (request !== state.portForwardRequest) return false;
    setPortForwardStatus(`读取失败：${String(error)}`, true);
    if (showError) toast(`读取端口转发失败：${String(error)}`, true);
    return false;
  } finally {
    if (request === state.portForwardRequest) {
      button.disabled = false;
      button.textContent = "刷新";
    }
  }
}
function updatePortForwardFields() {
  const kind = $("#portForwardKind").value;
  const dynamic = kind === "dynamic";
  document.querySelectorAll(".port-forward-target").forEach((label) =>
    label.classList.toggle("hidden", dynamic),
  );
  $("#portForwardTargetHost").disabled = dynamic;
  $("#portForwardTargetPort").disabled = dynamic;
  $("#portForwardHint").textContent = {
    local: "本机监听后，经 SSH 访问远端目标。",
    remote: "服务器监听后，经 SSH 访问本机目标。",
    dynamic: "在监听地址创建 SOCKS5 代理，经所选 SSH 主机访问网络。",
  }[kind];
}
function isLoopbackListenHost(value) {
  const host = String(value || "").trim().toLowerCase().replace(/^\[|\]$/g, "");
  return host === "localhost" || host === "::1" || /^127(?:\.\d{1,3}){3}$/.test(host);
}
async function openPortForwardDialog() {
  const hosts = availableSSHHosts();
  if (!hosts.length) return toast("请先添加可用的 SSH 主机", true);
  $("#portForwardHost").innerHTML = hosts.map((host) =>
    `<option value="${esc(host.id)}">${esc(host.name || host.address)} · ${esc(host.address)}</option>`,
  ).join("");
  $("#portForwardHost").value = hosts.some((host) => host.id === state.selected)
    ? state.selected
    : hosts[0].id;
  $("#portForwardKind").value = "local";
  $("#portForwardListenHost").value = "127.0.0.1";
  $("#portForwardListenPort").value = "0";
  $("#portForwardTargetHost").value = "127.0.0.1";
  $("#portForwardTargetPort").value = "0";
  updatePortForwardFields();
  setPortForwardStatus("正在读取转发列表…");
  $("#portForwardModal").classList.remove("hidden");
  $("#portForwardHost").focus();
  if (await refreshPortForwards(false)) setPortForwardStatus();
}
async function startPortForward() {
  const kind = $("#portForwardKind").value;
  const listenHost = $("#portForwardListenHost").value.trim() || "127.0.0.1";
  const listenPort = Number($("#portForwardListenPort").value);
  const targetHost = $("#portForwardTargetHost").value.trim();
  const targetPort = Number($("#portForwardTargetPort").value);
  if (!Number.isInteger(listenPort) || listenPort < 0 || listenPort > 65535) {
    return setPortForwardStatus("监听端口必须在 0 到 65535 之间", true);
  }
  if (
    kind !== "dynamic" &&
    (!targetHost || !Number.isInteger(targetPort) || targetPort < 1 || targetPort > 65535)
  ) {
    return setPortForwardStatus("本地/远程转发需要有效目标地址和端口", true);
  }
  let allowExternal = false;
  if (!isLoopbackListenHost(listenHost)) {
    allowExternal = await uiConfirm(
      `监听地址 ${listenHost}:${listenPort} 可能被其他设备访问。仅在确认防火墙和访问控制安全时继续。`,
      "确认外部监听",
    );
    if (!allowExternal) return;
  }
  const button = $("#startPortForward");
  button.disabled = true;
  button.textContent = "启动中…";
  setPortForwardStatus("正在建立 SSH 转发…");
  try {
    const item = await api().StartPortForward({
      hostId: $("#portForwardHost").value,
      kind,
      listenHost,
      listenPort,
      targetHost: kind === "dynamic" ? "" : targetHost,
      targetPort: kind === "dynamic" ? 0 : targetPort,
      allowExternal,
      connectTimeoutSeconds: 0,
      handshakeTimeoutSeconds: 0,
    });
    setPortForwardStatus(`已启动：${item.listenAddress || "等待监听"}`);
    await refreshPortForwards(false);
    toast("端口转发已启动");
  } catch (error) {
    setPortForwardStatus(`启动失败：${String(error)}`, true);
    toast(`端口转发启动失败：${String(error)}`, true);
  } finally {
    button.disabled = false;
    button.textContent = "启动转发";
  }
}
async function stopPortForward(button) {
  const id = button.dataset.forwardId;
  if (!id) return;
  const item = state.portForwards.find((forward) => forward.id === id);
  if (!(await uiConfirm(
    `确认停止${item ? ` ${portForwardKindText(item.kind)} · ${item.listenAddress}` : "该"}端口转发？`,
    "停止端口转发",
  ))) return;
  button.disabled = true;
  button.textContent = "停止中";
  try {
    await api().StopPortForward(id);
    setPortForwardStatus("转发已停止");
    await refreshPortForwards(false);
  } catch (error) {
    button.disabled = false;
    button.textContent = "停止";
    setPortForwardStatus(`停止失败：${String(error)}`, true);
    toast(`停止端口转发失败：${String(error)}`, true);
  }
}
async function runQueueStepForTargets(command, targets, step, run) {
  const runTarget = async ({ id, epoch }) => {
    const session = state.sessions[id];
    if (!session?.connected || session.connectionEpoch !== epoch) {
      throw new Error("SSH 会话已变化，队列已停止");
    }
    let lastError = null;
    for (let attempt = 0; attempt <= run.retries; attempt += 1) {
      if (run.cancelled) throw new Error("队列已停止");
      const result = await api().RunQueueStep(
        id,
        epoch,
        command,
        run.id,
        run.timeoutSeconds,
      );
      if (!session.connected || session.connectionEpoch !== epoch) {
        throw new Error("SSH 会话已变化，队列已停止");
      }
      const exitCode = Number(result?.exitCode);
      if (!Number.isFinite(exitCode)) throw new Error("队列步骤返回了无效退出码");
      if (exitCode === 0) return result;
      lastError = new Error(`退出码 ${exitCode}\n${result.output || "无输出"}`);
      if (attempt < run.retries && !run.cancelled) {
        writeTerminalOutput(id, `\r\n[队列] 第 ${step} 步失败，正在重试 ${attempt + 1}/${run.retries}…\r\n`);
      }
    }
    throw lastError || new Error("队列步骤执行失败");
  };
  const settled = new Array(targets.length);
  let nextIndex = 0;
  const concurrency = Math.min(
    targets.length,
    Math.max(1, Number(state.batchConcurrency) || 1),
  );
  const worker = async () => {
    while (nextIndex < targets.length) {
      const index = nextIndex++;
      try {
        settled[index] = { status: "fulfilled", value: await runTarget(targets[index]) };
      } catch (reason) {
        settled[index] = { status: "rejected", reason };
      } finally {
        run.completedUnits = Math.min(run.totalUnits, run.completedUnits + 1);
        updateQueueRunUI();
      }
    }
  };
  await Promise.all(Array.from({ length: concurrency }, worker));
  const failed = [];
  settled.forEach((item, index) => {
    if (item.status === "rejected") {
      failed.push(`${hostDisplayName(targets[index].id)}：${String(item.reason)}`);
    }
  });
  if (failed.length) throw new Error(`第 ${step} 步执行失败：\n${failed.join("\n")}`);
}

function updateQueueRunUI() {
  const button = $("#stopQueue");
  if (!button) return;
  const run = state.queueRun;
  button.classList.toggle("hidden", !run);
  button.disabled = Boolean(run?.cancelled);
  button.textContent = run?.cancelled ? "停止中…" : "停止队列";
  $("#addQueueQuick").disabled = Boolean(run);
  document.querySelectorAll("#queueCommandList .preset-command").forEach((commandButton) => {
    const running = Boolean(run && commandButton.dataset.id === run.commandId);
    commandButton.disabled = Boolean(run);
    commandButton.classList.toggle("running", running);
    commandButton.setAttribute("aria-busy", String(running));
  });
  const status = $("#queueRunStatus");
  const name = $("#queueRunName");
  const meta = $("#queueRunMeta");
  const progress = $("#queueRunProgress");
  const percent = run?.totalUnits
    ? Math.min(100, Math.round((run.completedUnits / run.totalUnits) * 100))
    : 0;
  status.classList.toggle("idle", !run);
  status.classList.toggle("running", Boolean(run && !run.cancelled));
  status.classList.toggle("stopping", Boolean(run?.cancelled));
  name.textContent = run?.commandName || "暂无运行队列";
  meta.textContent = !run
    ? "等待执行"
    : `${run.cancelled ? "停止中" : run.currentStep ? `步骤 ${run.currentStep}/${run.totalSteps}` : "准备中"} · ${run.targetCount} 台主机`;
  progress.setAttribute("aria-valuenow", String(percent));
  progress.querySelector("i").style.width = `${percent}%`;
}
const terminalOutputLimit = 250000;
const terminalOutputRetained = 180000;
function appendSessionOutput(session, text) {
  session.output += text;
  if (session.output.length <= terminalOutputLimit) return false;
  session.output = session.output.slice(-terminalOutputRetained);
  return true;
}
function scrollTerminalToEnd(id = state.selected) {
  const session = state.sessions[id];
  if (!session?.terminal) return;
  window.cancelAnimationFrame(session.terminalScrollFrame);
  session.terminalScrollFrame = window.requestAnimationFrame(() => {
    if (session.terminal && !session.terminal.hasSelection()) session.terminal.scrollToBottom();
  });
}
function writeTerminalOutput(id, output, rawPTY = false) {
  const session = state.sessions[id];
  if (!session) return;
  const raw = String(output || "");
  const terminal = ensureTerminal(id).terminal;
  appendSessionOutput(session, cleanTerminal(raw));
  const chunk = rawPTY ? raw : raw.replace(/\r?\n/g, "\r\n");
  terminal.write(chunk, () => scrollTerminalToEnd(id));
}
queueRuntimeEvent("terminal-output", (data) => {
  if (!data?.id || !state.sessions[data.id]) return;
  if (Number(data.epoch) !== state.sessions[data.id].connectionEpoch) return;
  writeTerminalOutput(data.id, data.output, !data.convertEol);
});
queueRuntimeEvent("ssh-connection-retry", (data) => {
  if (!data?.id || !state.sessions[data.id]) return;
  if (Number(data.epoch) !== state.sessions[data.id].connectionEpoch) return;
  const delay = Math.max(0, Number(data.delayMs) || 0) / 1000;
  writeTerminalOutput(
    data.id,
    `[连接] 握手暂时被拒绝，${delay.toFixed(delay < 1 ? 1 : 0)} 秒后重试（${data.attempt}/${data.maximum}）…\r\n`,
  );
});
const sshStageMessages = {
  DNS: "正在解析服务器域名 (DNS)…",
  TCP: "正在建立 TCP 传输通道…",
  SOCKS5: "正在通过 SOCKS5 代理建立连接通道…",
  "SSH 跳板": "正在通过 SSH 跳板主机建立隧道…",
  "SSH 握手": "正在执行 SSH 协议版本与加密算法协商…",
  主机指纹: "正在校验主机公钥指纹…",
  认证: "正在验证登录凭据（密码/密钥/Agent）…",
  PTY: "正在分配交互式伪终端 (PTY) 会话…",
};
queueRuntimeEvent("ssh-connection-stage", (data) => {
  if (!data?.id || !state.sessions[data.id]) return;
  const session = state.sessions[data.id];
  if (!session.connecting || Number(data.epoch) !== session.connectionEpoch) return;
  const stage = String(data.stage || "").trim();
  if (!stage || session.lastConnectionStage === stage) return;
  session.lastConnectionStage = stage;
  const desc = stage === "TCP" && session.targetAddress
    ? `正在建立 TCP 传输通道 (${session.targetAddress})…`
    : sshStageMessages[stage] || `正在处理 [${stage}] 阶段…`;
  writeTerminalOutput(data.id, `[连接] ${desc}\r\n`);
  updateSessionTabState(data.id);
});
queueRuntimeEvent("ssh-diagnostic-stage", (data) => {
  if (!data?.hostId && !data?.address) return;
  if (!$("#hostEditor").classList.contains("hidden")) {
    const sameID = data?.hostId && data.hostId === $("#id").value;
    const sameAddress = data?.address && data.address === $("#address").value.trim();
    if (sameID || sameAddress) $("#hostTestResult").textContent = `正在检测：${data.stage || "处理中"}…`;
  }
});
queueRuntimeEvent("ssh-interaction", async (request) => {
  if (!request?.id) return;
  state.sshInteractions[request.id] = request.hostId || "";
  let accepted = false;
  const answers = [];
  const host = request.hostName || request.address || "SSH 主机";
  try {
    if (request.kind === "hostkey") {
      accepted = await uiConfirm(
        `${host}\n地址：${request.address || "—"}\nSHA256 指纹：${request.fingerprint || "—"}\n\n请核对服务器指纹。接受后仅在认证成功时保存；如果指纹不符，请取消连接。`,
        "确认 SSH 主机指纹",
        `ssh:${request.id}`,
      );
    } else if (request.kind === "key-passphrase") {
      const answer = await showUIDialog({
        title: "输入私钥口令",
        message: `${host}\n地址：${request.address || "—"}\n\n该私钥已加密，请输入本次连接使用的口令。口令不会保存。`,
        input: true,
        password: true,
        key: `ssh:${request.id}`,
      });
      accepted = answer !== null;
      if (accepted) answers.push(String(answer));
    } else if (request.kind === "password-retry") {
      const configuredHost = (state.settings?.hosts || []).find((item) => item.id === request.hostId);
      const credential = configuredHost?.credentialId
        ? (state.settings?.credentials || []).find((item) => item.id === configuredHost.credentialId)
        : null;
      let affectedHosts = Array.isArray(request.affectedHosts)
        ? request.affectedHosts.length
        : Math.max(0, Number(request.affectedHosts) || 0);
      if (!affectedHosts && credential?.type === "password") {
        affectedHosts = (state.settings?.hosts || []).filter(
          (item) => !item.deleted && item.credentialId === credential.id,
        ).length;
      }
      const credentialName = request.credentialName || credential?.name || "";
      const allowPermanent = request.allowPermanent !== false && !configuredHost?.temporary;
      let scopeHint = "永久保存只会在新密码验证成功后更新当前主机。";
      if (!allowPermanent) {
        scopeHint = "这是临时快速连接，只能使用一次性密码，不会写入主机库。";
      } else if (credentialName) {
        scopeHint = `永久保存将更新凭证库“${credentialName}”${affectedHosts ? `，并影响 ${affectedHosts} 台使用该凭证的主机` : ""}。`;
      }
      const originalError = String(request.error || "未返回原始错误");
      const answer = await showUIDialog({
        kind: "password-retry",
        title: "SSH 密码认证失败",
        message: `${host}\n地址：${request.address || "—"}\n\n服务器拒绝密码认证，可能是用户名/密码错误或服务器禁用密码登录。\n\n原始错误：${originalError}`,
        input: true,
        password: true,
        allowPermanent,
        scopeHint,
        key: `ssh:${request.id}`,
      });
      accepted = answer !== null;
      if (accepted) answers.push(String(answer.password), answer.scope === "permanent" ? "permanent" : "once");
    } else if (request.kind === "keyboard-interactive") {
      const questions = Array.isArray(request.questions) ? request.questions : [];
      if (questions.length === 0) {
        accepted = true;
      } else if (questions.length === 1) {
        const question = questions[0];
        const answer = await showUIDialog({
          title: "SSH 二次验证",
          message: `${host}${request.instruction ? `\n${request.instruction}` : ""}\n\n${question.text || "验证问题 1"}`,
          input: true,
          password: !question.echo,
          key: `ssh:${request.id}`,
        });
        accepted = answer !== null;
        if (accepted) answers.push(String(answer));
      } else {
        const values = await showUIDialog({
          title: "SSH 二次验证",
          message: `${host}${request.instruction ? `\n${request.instruction}` : ""}`,
          questions,
          key: `ssh:${request.id}`,
        });
        accepted = Array.isArray(values);
        if (accepted) values.forEach((value) => answers.push(String(value ?? "")));
      }
    }
    await api().ReplySSHInteraction(request.id, accepted, answers);
  } catch (error) {
    if (!/请求已失效/.test(String(error))) toast(`SSH 验证交互失败：${String(error)}`, true);
  } finally {
    delete state.sshInteractions[request.id];
  }
});
queueRuntimeEvent("terminal-closed", (data) => {
  if (!data?.id || !state.sessions[data.id]) return;
  const s = state.sessions[data.id];
  if (Number(data.epoch) !== s.connectionEpoch) return;
  advanceConnectionEpoch(s, data.id);
  writeTerminalOutput(data.id, `\r\n${data.message || "SSH 连接已断开"}\r\n`);
  markSessionDisconnected(data.id);
  if (data.id === state.selected) toast(data.message || "SSH 连接已断开", true);
  if (data.retryable) {
    scheduleSessionReconnect(data.id);
  }
});
queueRuntimeEvent("ssh-monitor-stream", (data) => {
  const id = data?.id;
  const session = id ? state.sessions[id] : null;
  if (!session?.connected || Number(data?.epoch) !== session.connectionEpoch) return;
  applyMonitorStreamFrame(id, session, data?.output);
});
queueRuntimeEvent("ssh-monitor-latency", (data) => {
  const id = data?.id;
  const session = id ? state.sessions[id] : null;
  if (!session?.connected || Number(data?.epoch) !== session.connectionEpoch) return;
  recordSessionLatencySample(id, session, data?.latency, data?.error || "");
});
queueRuntimeEvent("ssh-monitor-stream-stopped", (data) => {
  const id = data?.id;
  const session = id ? state.sessions[id] : null;
  if (!session || Number(data?.epoch) !== session.connectionEpoch) return;
  session.monitorStreamActive = false;
  session.monitorStreamStartedAt = 0;
  if (state.activeMonitorStreamKey === `${id}:${session.connectionEpoch}`) {
    state.activeMonitorStreamKey = "";
  }
  runMonitorScheduler();
});
queueRuntimeEvent("rdp-status", (data) => {
  if (!data?.sessionId) return;
  const previousRDP = state.rdpSessions[data.sessionId];
  state.rdpSessions[data.sessionId] = {
    hostId: data.hostId || "",
    state: data.state || "",
    pid: Number(data.pid) || 0,
    error: data.error || "",
  };
  const host = state.settings?.hosts.find((item) => item.id === data.hostId);
  const session = data.hostId ? state.sessions[data.hostId] : null;
  const name = host?.name || host?.address || "RDP";
  const instance = data.pid ? `PID ${data.pid}` : String(data.sessionId).slice(0, 8);
  if (data.state === "launching") {
    appendRDPLog(data.hostId, "正在启动远程桌面客户端 (mstsc.exe)…");
    toast(`${name}：正在启动远程桌面`);
  } else if (data.state === "failed") {
    appendRDPLog(data.hostId, `远程桌面连接中断: ${data.error || "未知错误"}`);
    const wasRunning = previousRDP?.state === "running" ||
      (session?.rdpSessionId === data.sessionId && session.connected);
    if (session && (!session.rdpSessionId || session.rdpSessionId === data.sessionId)) {
      session.connected = false;
      session.rdpSessionId = "";
      updateSessionTabState(data.hostId);
    }
    if (wasRunning && data.hostId === state.selected) {
      state.connected = false;
      $("#onlineDot").className = "";
      $("#sideAddress").textContent = "未连接";
      $("#copyHostIP").classList.add("hidden");
      $("#sideReconnect").classList.remove("hidden");
    }
    if (wasRunning) {
      toast(`${name} 连接中断：${data.error || "未知错误"}`, true);
    }
    delete state.rdpSessions[data.sessionId];
    if (wasRunning && host?.temporary) {
      removeTemporarySession(data.hostId).catch((error) =>
        console.warn("清理临时 RDP 会话失败", error),
      );
    }
  } else if (data.state === "running") {
    appendRDPLog(data.hostId, `远程桌面客户端已启动并连接（${instance}）`);
    if (session) {
      session.connected = true;
      session.rdpSessionId = data.sessionId;
      updateSessionTabState(data.hostId);
    }
    toast(`${name}：远程桌面已启动，正在连接（${instance}）`);
  } else if (data.state === "exited") {
    appendRDPLog(data.hostId, `远程桌面客户端已退出（${instance}）${data.error ? `，原因: ${data.error}` : ""}`);
    if (session?.rdpSessionId === data.sessionId) {
      session.connected = false;
      session.rdpSessionId = "";
      if (data.hostId === state.selected) {
        state.connected = false;
        $("#onlineDot").className = "";
        $("#sideAddress").textContent = "未连接";
      }
      updateSessionTabState(data.hostId);
    }
    if (data.error) toast(`${name} 已退出（${instance}）：${data.error}`, true);
    else toast(`${name} 远程桌面已退出（${instance}）`);
    delete state.rdpSessions[data.sessionId];
    if (host?.temporary) {
      removeTemporarySession(data.hostId).catch((error) =>
        console.warn("清理临时 RDP 会话失败", error),
      );
    }
  }
  if (data.hostId === state.selected) {
    renderRDPDeck(data.hostId);
  }
});
const AI_OUTPUT_LIMIT = 60000;
const AI_CHAT_MAX_BUBBLES = 200;
const AI_CHAT_MAX_CHARS = 2000000;
function limitedAIOutput(text) {
  const value = String(text || "");
  return value.length > AI_OUTPUT_LIMIT
    ? `…前面的输出已省略…\n${value.slice(-AI_OUTPUT_LIMIT)}`
    : value;
}
function trimAIChat() {
  const chat = $("#chat");
  let totalChars = chat.textContent.length;
  while (
    chat.children.length > AI_CHAT_MAX_BUBBLES ||
    totalChars > AI_CHAT_MAX_CHARS
  ) {
    const victim = chat.children[2];
    if (!victim || victim === state.aiOutputBubble) break;
    totalChars -= victim.textContent.length;
    victim.remove();
  }
}
function appendAIBubble(type, text, metaBadge = "") {
  const bubble = document.createElement("div");
  bubble.className = `bubble ${type}`;
  const value = String(text || "");
  const trimmed = value.length > 100000 ? `${value.slice(0, 100000)}\n…内容过长，已省略…` : value;
  if (type === "answer") {
    bubble.innerHTML = `<div class="ai-answer-tools"><button type="button" class="ai-answer-copy">复制全文</button></div><div class="ai-markdown">${renderAIMarkdown(trimmed)}</div>`;
    bubble.dataset.rawText = trimmed;
  } else if (metaBadge) {
    bubble.innerHTML = `<div class="ai-user-text">${esc(trimmed).replace(/\n/g, "<br>")}</div><span class="ai-user-badge">${esc(metaBadge)}</span>`;
  } else {
    bubble.textContent = trimmed;
  }
  $("#chat").appendChild(bubble);
  trimAIChat();
  $("#chat").scrollTop = $("#chat").scrollHeight;
  return bubble;
}
function formatAIInlineMarkdown(raw) {
  let html = esc(raw);
  html = html.replace(/`([^`\n]+)`/g, '<code class="ai-inline-code">$1</code>');
  html = html.replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>");
  return html;
}
function renderAIMarkdown(text) {
  const source = String(text || "").replace(/\r\n/g, "\n");
  const blocks = [];
  const fenceRegex = /```([a-zA-Z0-9_-]*)\n([\s\S]*?)```/g;
  let lastIndex = 0;
  let match;
  const renderProse = (prose) => {
    const lines = prose.split("\n");
    const out = [];
    let inList = "";
    const closeList = () => {
      if (inList) {
        out.push(`</${inList}>`);
        inList = "";
      }
    };
    for (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed) {
        closeList();
        continue;
      }
      const h = trimmed.match(/^(#{1,3})\s+(.+)$/);
      if (h) {
        closeList();
        const level = Math.min(4, h[1].length + 1);
        out.push(`<h${level} class="ai-md-h">${formatAIInlineMarkdown(h[2])}</h${level}>`);
        continue;
      }
      const ul = trimmed.match(/^[-*•]\s+(.+)$/);
      if (ul) {
        if (inList !== "ul") {
          closeList();
          inList = "ul";
          out.push('<ul class="ai-md-list">');
        }
        out.push(`<li>${formatAIInlineMarkdown(ul[1])}</li>`);
        continue;
      }
      const ol = trimmed.match(/^\d+[.)]\s+(.+)$/);
      if (ol) {
        if (inList !== "ol") {
          closeList();
          inList = "ol";
          out.push('<ol class="ai-md-list">');
        }
        out.push(`<li>${formatAIInlineMarkdown(ol[1])}</li>`);
        continue;
      }
      closeList();
      if (trimmed.startsWith("> ")) {
        out.push(`<blockquote class="ai-md-quote">${formatAIInlineMarkdown(trimmed.slice(2))}</blockquote>`);
      } else {
        out.push(`<p class="ai-md-p">${formatAIInlineMarkdown(trimmed)}</p>`);
      }
    }
    closeList();
    return out.join("");
  };
  while ((match = fenceRegex.exec(source)) !== null) {
    if (match.index > lastIndex) {
      blocks.push(renderProse(source.slice(lastIndex, match.index)));
    }
    const lang = esc(match[1] || "shell");
    const code = match[2].replace(/\n$/, "");
    blocks.push(
      `<div class="ai-code-block"><div class="ai-code-head"><span>${lang}</span><span class="ai-code-actions"><button type="button" class="ai-code-copy">复制</button><button type="button" class="ai-code-fill">填入</button><button type="button" class="ai-code-run">终端执行</button></span></div><pre><code>${esc(code)}</code></pre></div>`,
    );
    lastIndex = fenceRegex.lastIndex;
  }
  if (lastIndex < source.length) {
    blocks.push(renderProse(source.slice(lastIndex)));
  }
  return blocks.join("") || `<p class="ai-md-p">${formatAIInlineMarkdown(source)}</p>`;
}
function readActiveTerminalTail(maxLines = 150) {
  const session = state.selected ? state.sessions[state.selected] : null;
  const buffer = session?.terminal?.buffer?.active;
  if (!buffer) return "";
  const lines = [];
  const start = Math.max(0, buffer.length - maxLines - 64);
  for (let i = start; i < buffer.length; i += 1) {
    const line = buffer.getLine(i)?.translateToString(true);
    if (line !== undefined) lines.push(line);
  }
  while (lines.length && !lines[lines.length - 1].trim()) lines.pop();
  const tail = lines.slice(-maxLines).join("\n").trim();
  return tail.length > 12000 ? tail.slice(-12000) : tail;
}
function resolveInlineAICards(runId) {
  $("#chat")
    .querySelectorAll(`.ai-inline-card[data-run-id="${CSS.escape(String(runId))}"]`)
    .forEach((card) => {
      card.classList.add("resolved");
      card.querySelectorAll("button, input").forEach((el) => {
        el.disabled = true;
      });
    });
}
function syncAITargetPill() {
  const pill = $("#aiTargetHost");
  if (!pill) return;
  const host = state.selected ? selected() : null;
  const session = state.selected ? state.sessions[state.selected] : null;
  if (!host) {
    pill.textContent = "未选主机（可跨主机）";
    pill.classList.remove("connected");
    return;
  }
  const label = host.name || host.address || host.id;
  pill.textContent = session?.connected ? `● ${label}` : `○ ${label}`;
  pill.classList.toggle("connected", Boolean(session?.connected));
}
function renderAIProgress() {
  if (!state.aiProgressBubble) return;
  const seconds = Math.max(0, Math.floor((Date.now() - state.aiProgressStartedAt) / 1000));
  state.aiProgressBubble.textContent = seconds
    ? `${state.aiProgressMessage}\n已等待 ${seconds} 秒，任务仍在处理中…`
    : state.aiProgressMessage;
  $("#chat").scrollTop = $("#chat").scrollHeight;
}
function startAIProgress(message) {
  if (state.aiProgressTimer) window.clearInterval(state.aiProgressTimer);
  state.aiProgressMessage = String(message || "AI 正在处理…");
  state.aiProgressStartedAt = Date.now();
  if (!state.aiProgressBubble)
    state.aiProgressBubble = appendAIBubble("progress", state.aiProgressMessage);
  renderAIProgress();
  state.aiProgressTimer = window.setInterval(renderAIProgress, 1000);
}
function updateAIProgress(message) {
  const value = String(message || "AI 正在处理…");
  if (!state.aiProgressBubble) {
    startAIProgress(value);
    return;
  }
  state.aiProgressMessage = value;
  renderAIProgress();
}
function stopAIProgress(remove = true) {
  if (state.aiProgressTimer) window.clearInterval(state.aiProgressTimer);
  state.aiProgressTimer = 0;
  if (remove) state.aiProgressBubble?.remove();
  state.aiProgressBubble = null;
  state.aiProgressMessage = "";
  state.aiProgressStartedAt = 0;
}
function flushAIOutput() {
  if (state.aiOutputTimer) {
    window.clearTimeout(state.aiOutputTimer);
    state.aiOutputTimer = 0;
  }
  const pending = state.aiOutputPending;
  state.aiOutputPending = "";
  if (!state.aiOutputBubble || !pending) return;
  const output = limitedAIOutput(`${state.aiOutputBubble.aiOutput || ""}${pending}`);
  state.aiOutputBubble.aiOutput = output;
  state.aiOutputBubble.textContent = output || "等待命令输出…";
  state.aiOutputBubble.scrollTop = state.aiOutputBubble.scrollHeight;
  $("#chat").scrollTop = $("#chat").scrollHeight;
}
function queueAIOutput(text) {
  const pending = `${state.aiOutputPending}${text}`;
  state.aiOutputPending =
    pending.length > AI_OUTPUT_LIMIT * 2
      ? pending.slice(-AI_OUTPUT_LIMIT * 2)
      : pending;
  if (!state.aiOutputTimer)
    state.aiOutputTimer = window.setTimeout(flushAIOutput, 100);
}
function aiTaskBusy() {
  return state.aiStarting || Boolean(state.aiRunId);
}
function updateAIControls() {
  const taskBusy = aiTaskBusy();
  const locked = taskBusy || state.aiConfigBusy;
  $("#sendAI").disabled = locked;
  $("#stopAI").classList.toggle("hidden", !state.aiRunId);
  const clearBtn = $("#clearAIChat");
  if (clearBtn) clearBtn.disabled = taskBusy;
  syncAITargetPill();
  $("#aiQuickChips")
    ?.querySelectorAll("button")
    .forEach((btn) => {
      btn.disabled = locked || !currentProvider();
    });
  $("#providerTop").disabled = locked;
  $("#modelTop").disabled = locked;
  $("#aiPermission").disabled = locked || !currentProvider();
  $("#aiSettings").disabled = locked;
  $("#fetchProviderModels").disabled = locked;
  $("#aiConfigForm")
    .querySelectorAll("input, select, button:not(.cancelAI)")
    .forEach((control) => {
      control.disabled = locked;
    });
  const providers = state.settings?.aiProviders || [];
  $("#manageAI").disabled = locked || providers.length >= 8;
  const currentProviderId = state.settings?.currentProviderId || "";
  $("#settingsAIProviders")
    .querySelectorAll("button")
    .forEach((button) => {
      button.disabled =
        locked ||
        (button.dataset.providerAction === "use" &&
          button.dataset.id === currentProviderId);
    });
  $("#aiConfigForm").querySelector("button.primary").disabled = locked;
  $("#deleteProvider").disabled = locked;
  if (!state.aiRunId) {
    $("#stopAI").disabled = false;
    $("#stopAI").textContent = "停止";
  }
}
async function runAIConfigTask(message, action) {
  if (aiTaskBusy() || state.aiConfigBusy)
    throw new Error("AI 正在执行其他操作，请稍候");
  state.aiConfigBusy = true;
  updateAIControls();
  $("#aiModeHint").textContent = message;
  try {
    return await action();
  } finally {
    state.aiConfigBusy = false;
    fillAI();
    updateAIControls();
  }
}
function rememberAIFinishedRun(runId) {
  state.aiFinishedRuns.add(runId);
  while (state.aiFinishedRuns.size > 32)
    state.aiFinishedRuns.delete(state.aiFinishedRuns.values().next().value);
}
function setAIAgentRunning(running, runId = "") {
  state.aiStarting = false;
  state.aiRunId = running ? runId || state.aiRunId : "";
  if (!running) {
    stopAIProgress();
    flushAIOutput();
    state.aiOutputBubble = null;
    fillAI();
  }
  updateAIControls();
}
queueRuntimeEvent("ai-agent-event", async (data) => {
  if (!data?.runId) return;
  if (state.aiFinishedRuns.has(data.runId)) return;
  if (state.aiRunId && data.runId !== state.aiRunId) return;
  if (!state.aiRunId) {
    if (!state.aiStarting) return;
    setAIAgentRunning(true, data.runId);
  }
  switch (data.type) {
    case "started":
    case "thinking":
    case "status":
      $("#aiModeHint").textContent = data.message || "AI 正在执行…";
      updateAIProgress(data.message || "AI 正在执行…");
      break;
    case "approval": {
      stopAIProgress();
      const card = document.createElement("div");
      card.className = `bubble ai-inline-card ai-approval-card ${data.risk === "高危操作" ? "high-risk" : ""}`;
      card.dataset.runId = data.runId;
      const hostLabel = esc(data.hostName || data.hostId || "目标主机");
      const riskLabel = esc(data.risk || "需要确认");
      const purpose = data.message ? `<div class="ai-inline-meta">${esc(data.message)}</div>` : "";
      card.innerHTML = `<div class="ai-inline-head"><b>${data.risk === "高危操作" ? "⚠ 高危操作确认" : "⚡ 命令执行确认"}</b><span class="ai-inline-tag">${hostLabel} · ${riskLabel}</span></div>${purpose}<pre class="ai-inline-cmd">$ ${esc(data.command || "")}</pre><div class="ai-inline-actions"><button type="button" class="primary ai-approve-yes">确认执行</button><button type="button" class="ai-approve-no">拒绝</button></div>`;
      $("#chat").appendChild(card);
      trimAIChat();
      $("#chat").scrollTop = $("#chat").scrollHeight;
      $("#aiModeHint").textContent = "请在右侧对话卡片中确认是否执行命令…";
      const submitApproval = async (approved) => {
        if (card.classList.contains("resolved")) return;
        card.classList.add("resolved");
        card.querySelectorAll("button").forEach((b) => { b.disabled = true; });
        const note = document.createElement("div");
        note.className = "ai-inline-result";
        note.textContent = approved ? "✓ 已确认执行" : "✕ 已拒绝该命令";
        card.appendChild(note);
        if (state.aiFinishedRuns.has(data.runId) || state.aiRunId !== data.runId) return;
        try {
          await api().ApproveAIAgent(data.runId, data.actionId, approved);
          $("#aiModeHint").textContent = approved ? "已批准执行，AI 正在继续…" : "已拒绝，AI 正在调整方案…";
        } catch (error) {
          if (state.aiRunId === data.runId) toast(String(error), true);
        }
      };
      card.querySelector(".ai-approve-yes").onclick = () => submitApproval(true);
      card.querySelector(".ai-approve-no").onclick = () => submitApproval(false);
      break;
    }
    case "question": {
      stopAIProgress();
      const question = String(data.message || "AI 需要您补充信息").trim();
      const reason = String(data.reason || "").trim();
      const options = Array.isArray(data.options)
        ? data.options.map((option) => String(option || "").trim()).filter(Boolean)
        : [];
      const card = document.createElement("div");
      card.className = "bubble ai-inline-card ai-question-card";
      card.dataset.runId = data.runId;
      const optionsHtml = options.length
        ? `<div class="ai-option-list">${options.map((opt, idx) => `<button type="button" class="ai-option-btn" data-opt-index="${idx}"><span>${idx + 1}.</span> ${esc(opt)}</button>`).join("")}</div>`
        : "";
      card.innerHTML = `<div class="ai-inline-head"><b>💬 AI 需要您确认</b></div><div class="ai-inline-question">${esc(question)}</div>${reason ? `<div class="ai-inline-meta">${esc(reason)}</div>` : ""}${optionsHtml}<div class="ai-inline-input-row"><input type="text" class="ai-inline-answer" placeholder="${options.length ? "或输入自定义回答…" : "请输入回答…"}"><button type="button" class="primary ai-answer-send">发送</button><button type="button" class="ai-answer-stop">停止</button></div>`;
      $("#chat").appendChild(card);
      trimAIChat();
      $("#chat").scrollTop = $("#chat").scrollHeight;
      $("#aiModeHint").textContent = "请在右侧对话卡片中回复 AI 问题…";
      const inputEl = card.querySelector(".ai-inline-answer");
      requestAnimationFrame(() => inputEl?.focus());
      const submitAnswer = async (rawAnswer, displayLabel = "") => {
        const answer = String(rawAnswer || "").trim();
        if (!answer) return toast("回答不能为空", true);
        if (card.classList.contains("resolved")) return;
        card.classList.add("resolved");
        card.querySelectorAll("button, input").forEach((el) => { el.disabled = true; });
        const chosen = document.createElement("div");
        chosen.className = "ai-inline-result";
        chosen.textContent = `✓ 已回答：${displayLabel || answer}`;
        card.appendChild(chosen);
        if (state.aiFinishedRuns.has(data.runId) || state.aiRunId !== data.runId) return;
        try {
          await api().AnswerAIAgent(data.runId, data.actionId, answer);
          $("#aiModeHint").textContent = "已提交回答，AI 正在继续执行…";
        } catch (error) {
          appendAIBubble("answer bad", `提交回答失败：${String(error)}`);
          if (state.aiRunId === data.runId) {
            toast(String(error), true);
            try { await api().StopAIAgent(data.runId); } catch {}
          }
        }
      };
      card.querySelectorAll(".ai-option-btn").forEach((btn) => {
        btn.onclick = () => {
          const idx = Number(btn.dataset.optIndex);
          if (idx >= 0 && idx < options.length) {
            submitAnswer(options[idx], `${idx + 1}. ${options[idx]}`);
          }
        };
      });
      card.querySelector(".ai-answer-send").onclick = () => {
        const val = inputEl.value.trim();
        const idx = /^\d+$/.test(val) ? Number(val) - 1 : -1;
        if (idx >= 0 && idx < options.length) {
          submitAnswer(options[idx], `${idx + 1}. ${options[idx]}`);
        } else {
          submitAnswer(val);
        }
      };
      inputEl.onkeydown = (ev) => {
        if (ev.key === "Enter" && !ev.isComposing && ev.keyCode !== 229) {
          ev.preventDefault();
          card.querySelector(".ai-answer-send").click();
        }
      };
      card.querySelector(".ai-answer-stop").onclick = async () => {
        if (card.classList.contains("resolved")) return;
        card.classList.add("resolved");
        card.querySelectorAll("button, input").forEach((el) => { el.disabled = true; });
        appendAIBubble("notice", "已取消回答，正在停止当前 AI 任务。");
        try {
          await api().StopAIAgent(data.runId);
        } catch (error) {
          if (state.aiRunId === data.runId) toast(String(error), true);
        }
      };
      break;
    }
    case "plan-confirmation": {
      stopAIProgress();
      const plan = String(data.plan || "未提供具体步骤").trim();
      const impact = String(data.message || "未说明额外影响").trim();
      const rollback = String(data.rollback || "未提供回滚步骤").trim();
      const card = document.createElement("div");
      card.className = "bubble ai-inline-card ai-plan-card";
      card.dataset.runId = data.runId;
      card.innerHTML = `<div class="ai-inline-head"><b>📋 AI 执行方案确认</b></div><div class="ai-plan-section"><span>执行方案</span><pre>${esc(plan)}</pre></div><div class="ai-plan-section"><span>影响评估</span><pre>${esc(impact)}</pre></div><div class="ai-plan-section"><span>回滚策略</span><pre>${esc(rollback)}</pre></div><div class="ai-inline-actions"><button type="button" class="primary ai-plan-yes">确认执行方案</button><button type="button" class="ai-plan-no">取消方案</button></div>`;
      $("#chat").appendChild(card);
      trimAIChat();
      $("#chat").scrollTop = $("#chat").scrollHeight;
      $("#aiModeHint").textContent = "请在右侧对话卡片中审核执行方案…";
      const submitPlan = async (approved) => {
        if (card.classList.contains("resolved")) return;
        card.classList.add("resolved");
        card.querySelectorAll("button").forEach((b) => { b.disabled = true; });
        const note = document.createElement("div");
        note.className = "ai-inline-result";
        note.textContent = approved ? "✓ 已确认执行方案" : "✕ 已取消该方案";
        card.appendChild(note);
        if (state.aiFinishedRuns.has(data.runId) || state.aiRunId !== data.runId) return;
        try {
          await api().ApproveAIAgent(data.runId, data.actionId, approved);
          $("#aiModeHint").textContent = approved ? "方案已批准，AI 正在执行…" : "已取消方案";
        } catch (error) {
          if (state.aiRunId === data.runId) toast(String(error), true);
        }
      };
      card.querySelector(".ai-plan-yes").onclick = () => submitPlan(true);
      card.querySelector(".ai-plan-no").onclick = () => submitPlan(false);
      break;
    }
    case "command-start": {
      stopAIProgress();
      flushAIOutput();
      const card = document.createElement("div");
      card.className = "bubble ai-step-card running";
      const hostLabel = esc(data.hostName || data.hostId || "");
      const purpose = esc(data.message || "执行命令");
      const cmd = String(data.command || "");
      card.innerHTML = `<div class="ai-step-head"><span class="ai-step-status">执行中…</span><b title="${purpose}">${hostLabel} · ${purpose}</b><span class="ai-step-actions"><button type="button" class="ai-step-copy">复制</button><button type="button" class="ai-step-toggle">收起</button></span></div><div class="ai-step-cmd">$ ${esc(cmd)}</div><pre class="ai-step-output">等待命令输出…</pre>`;
      card.dataset.command = cmd;
      const pre = card.querySelector(".ai-step-output");
      pre.aiOutput = "";
      pre.aiStepCard = card;
      state.aiOutputBubble = pre;
      $("#chat").appendChild(card);
      trimAIChat();
      $("#chat").scrollTop = $("#chat").scrollHeight;
      $("#aiModeHint").textContent = `正在操作 ${data.hostName || data.hostId}`;
      break;
    }
    case "command-output": {
      if (!state.aiOutputBubble) {
        const fallback = appendAIBubble("result", "等待命令输出…");
        fallback.aiOutput = "";
        state.aiOutputBubble = fallback;
      }
      const chunk = cleanTerminal(data.output || "");
      queueAIOutput(chunk);
      break;
    }
    case "command-end": {
      flushAIOutput();
      const exitCode = Number(data.exitCode ?? 0);
      const output = cleanTerminal(data.output || "");
      const succeeded = exitCode === 0 && !data.message;
      const shown = limitedAIOutput(output) || (succeeded ? "执行完成（无额外输出）" : "无输出");
      const error = data.message ? `\n错误：${data.message}` : "";
      const text = succeeded ? shown : `退出码 ${exitCode}${error}\n${shown}`;
      const card = state.aiOutputBubble?.aiStepCard;
      if (card) {
        card.classList.remove("running");
        card.classList.add(succeeded ? "done" : "bad");
        const duration = Number(data.duration) > 0 ? ` · ${data.duration}ms` : "";
        card.querySelector(".ai-step-status").textContent = succeeded ? `✓ 完成${duration}` : `✕ 失败 (${exitCode})`;
        const pre = card.querySelector(".ai-step-output");
        pre.textContent = text;
        pre.aiOutput = shown;
        const lineCount = text.split("\n").length;
        if (succeeded && (lineCount > 6 || text.length > 260)) {
          card.classList.add("collapsed");
          card.querySelector(".ai-step-toggle").textContent = "展开";
        }
      } else if (state.aiOutputBubble) {
        state.aiOutputBubble.className = `bubble ${succeeded ? "result" : "result bad"}`;
        state.aiOutputBubble.textContent = text;
        state.aiOutputBubble.aiOutput = shown;
      } else {
        appendAIBubble(succeeded ? "result" : "result bad", text);
      }
      state.aiOutputBubble = null;
      trimAIChat();
      $("#chat").scrollTop = $("#chat").scrollHeight;
      break;
    }
    case "completed":
      rememberAIFinishedRun(data.runId);
      resolveInlineAICards(data.runId);
      cancelUIDialogs((options) =>
        String(options.key || "").startsWith(`ai:${data.runId}:`),
      );
      appendAIBubble("answer", data.message || "任务已完成并验证");
      setAIAgentRunning(false);
      break;
    case "cancelled":
      rememberAIFinishedRun(data.runId);
      resolveInlineAICards(data.runId);
      cancelUIDialogs((options) =>
        String(options.key || "").startsWith(`ai:${data.runId}:`),
      );
      appendAIBubble("notice", data.message || "任务已停止");
      setAIAgentRunning(false);
      break;
    case "failed":
      rememberAIFinishedRun(data.runId);
      resolveInlineAICards(data.runId);
      cancelUIDialogs((options) =>
        String(options.key || "").startsWith(`ai:${data.runId}:`),
      );
      appendAIBubble("answer bad", `任务未完成：${data.message || "未知错误"}`);
      setAIAgentRunning(false);
      break;
  }
});
function selected() {
  return state.settings?.hosts.find((h) => h.id === state.selected);
}
function latencyTargetText(host) {
  const protocol = String(host?.protocol || "ssh").toLowerCase();
  const port = host?.port || (protocol === "rdp" ? 3389 : 22);
  if (protocol !== "rdp" && host?.sshJumpHostId) {
    return `SSH 跳板 ${hostDisplayName(host.sshJumpHostId)} → ${host?.address || "—"}:${port}`;
  }
  if (protocol === "rdp" && host?.rdpJumpHostId) {
    return `SSH 跳板 ${hostDisplayName(host.rdpJumpHostId)} → ${host?.address || "—"}:${port}`;
  }
  return host?.proxyEnabled
    ? `SOCKS5 ${host.proxyHost || "127.0.0.1"}:${host.proxyPort || 20808} → ${host.address}:${port}`
    : `直连 → ${host?.address || "—"}:${port}`;
}
function latencyResultTitle(id, attempts) {
  const host = (state.settings?.hosts || []).find((item) => item.id === id);
  const proxyHost = String(host?.proxyHost || "127.0.0.1").toLowerCase();
  const proxyPort = Number(host?.proxyPort || 20808);
  if (host?.proxyEnabled && proxyPort === 20808 && ["127.0.0.1", "localhost", "::1"].includes(proxyHost)) {
    return `本地代理当前节点快速延迟 · ${attempts} 次取最优`;
  }
  return `${latencyTargetText(host)} · ${attempts} 次取最优`;
}
function sessionLatencyTargetText(host) {
  const protocol = String(host?.protocol || "ssh").toUpperCase();
  const port = host?.port || (protocol === "RDP" ? 3389 : 22);
  return `${protocol} RTT · ${host?.address || "—"}:${port}`;
}
function setAIPanel(open) {
  const main = $("main");
  const isOpen = !main.classList.contains("ai-collapsed");
  if (open === isOpen || state.aiPanelChanging) return;
  state.aiPanelChanging = true;
  const toggle = $("#aiToggle");
  toggle.disabled = true;
  const scale =
    Number.parseFloat(
      getComputedStyle(document.documentElement).getPropertyValue("--ui-scale"),
    ) || 1;
  const availableWidth =
    window.screen?.availWidth || window.screen?.width || window.innerWidth;
  const maxWidth = Math.max(780, Math.floor(availableWidth * 0.98));
  const width = Math.min(
    maxWidth,
    Math.max(780, Math.round(window.innerWidth + (open ? 330 : -330) * scale)),
  );
  main.classList.toggle("ai-collapsed", !open);
  toggle.textContent = open ? "▶" : "◀";
  toggle.title = open ? "隐藏 AI 助手" : "展开 AI 助手";
  toggle.setAttribute("aria-label", toggle.title);
  try {
    window.runtime?.WindowSetSize?.(width, window.innerHeight);
  } catch (error) {
    toast(`调整 AI 面板失败：${String(error)}`, true);
  }
  window.setTimeout(() => {
    try {
      adaptContentScale();
    } finally {
      state.aiPanelChanging = false;
      toggle.disabled = false;
    }
  }, 180);
}

function adaptWindowToScreen() {
  const availableWidth =
    window.screen?.availWidth || window.screen?.width || 1200;
  const availableHeight =
    window.screen?.availHeight || window.screen?.height || 800;
  const scale = Math.max(
    0.6,
    Math.min(1, (availableWidth * 0.96) / 1690, (availableHeight * 0.94) / 980),
  );
  const width = Math.max(780, Math.floor(1360 * scale));
  const height = Math.max(600, Math.floor(980 * scale));
  window.runtime?.WindowSetMinSize?.(
    Math.min(780, width),
    Math.min(600, height),
  );
  window.runtime?.WindowSetSize?.(width, height);
  window.runtime?.WindowCenter?.();
}

function adaptContentScale() {
  const width = Math.max(1, window.innerWidth);
  const height = Math.max(1, window.innerHeight);
  const nodeMode = app.classList.contains("node-mode-active");
  const baseWidth = !nodeMode && !$("main")?.classList.contains("ai-collapsed") ? 1690 : 1360;
  const scale = Math.max(0.6, Math.min(1, width / baseWidth, height / 980));
  const root = document.documentElement;
  root.style.setProperty("--ui-scale", scale.toFixed(4));
  root.style.setProperty("--ui-width", `${width / scale}px`);
  root.style.setProperty("--ui-height", `${height / scale}px`);
  root.classList.toggle("ui-scaled", scale < 0.999);
  for (const session of Object.values(state.sessions)) applyTerminalScale(session);
  fitActiveTerminal();
}

let resizeFrame = 0;
window.addEventListener("resize", () => {
  cancelAnimationFrame(resizeFrame);
  resizeFrame = requestAnimationFrame(adaptContentScale);
});

async function init() {
  const deadline = Date.now() + 10000;
  while ((!api() || typeof window.runtime?.EventsOn !== "function") && Date.now() < deadline)
    await new Promise((resolve) => setTimeout(resolve, 50));
  if (!api()) throw new Error("后端绑定加载超时，请重启软件");
  if (!bindRuntimeEvents()) throw new Error("运行时事件加载超时，请重启软件");
  adaptWindowToScreen();
  adaptContentScale();
  window.setTimeout(adaptContentScale, 100);
  const publicSettings = await api().GetPublicSettings();
  applyTheme(cachedTheme() || publicSettings.theme, false);
  if (publicSettings.lockOnStartup && publicSettings.securityConfigured) {
    $("#unlockError").textContent = "";
    $("#unlockPassword").value = "";
    $("#unlockPassword").disabled = false;
    $("#unlockSubmit").disabled = false;
    $("#unlockSubmit").textContent = "解锁";
    resetPasswordVisibility("unlockPassword");
    $("#unlockModal").classList.remove("hidden");
    $("#unlockPassword").focus();
    return;
  }
  await loadApp();
}
async function loadApp() {
  replaceSettings(await api().GetSettings());
  fillAI();
  renderSessionTabs();
  renderHosts();
  detectHostCountries();
  checkAllHostLatencies();
  loadShellVPS();
  showHostListPage(false);
}
function resolveVPSHostBinding(vps, hosts = []) {
  const alias = String(vps?.alias || vps?.id || "").trim();
  const hostId = String(vps?.hostId || "").trim();
  const sshHosts = hosts.filter(
    (host) => !host.deleted && String(host.protocol || "ssh").toLowerCase() === "ssh",
  );
  if (hostId) {
    const host = hosts.find((item) => String(item.id || "").trim() === hostId);
    if (!host) return { host: null, error: `${alias} 绑定的 Shell 主机不存在（ID：${hostId}）` };
    if (host.deleted) return { host: null, error: `${alias} 绑定的 Shell 主机已删除` };
    if (String(host.protocol || "ssh").toLowerCase() !== "ssh") {
      return { host: null, error: `${alias} 绑定的不是 SSH 主机` };
    }
    return { host, error: "" };
  }

  const aliasKey = alias.toLowerCase();
  const aliasMatches = sshHosts.filter(
    (host) => String(host.vpsAlias || "").trim().toLowerCase() === aliasKey,
  );
  if (aliasMatches.length === 1) return { host: aliasMatches[0], error: "" };
  if (aliasMatches.length > 1) {
    return { host: null, error: `${alias} 匹配到多个 Shell 主机，请先修正重复的节点别名` };
  }

  const address = String(vps?.address || "").trim().toLowerCase();
  const port = Number(vps?.port || 22) || 22;
  const addressMatches = sshHosts.filter(
    (host) => !String(host.vpsAlias || "").trim()
      && String(host.address || "").trim().toLowerCase() === address
      && (Number(host.port || 22) || 22) === port,
  );
  if (addressMatches.length === 1) return { host: addressMatches[0], error: "" };
  if (addressMatches.length > 1) {
    return { host: null, error: `${alias} 的地址和端口匹配到多个 Shell 主机，无法安全绑定 SOCKS5 配置` };
  }
  return { host: null, error: "" };
}
async function loadShellVPS() {
  if (state.vpsLoading) return;
  state.vpsLoading = true;
  try {
    state.vpsOverview = await window.go.nodemode.App.GetOverview();
    const items = state.vpsOverview?.vpsList || [];
    const addedHostIds = [];
    const bindingErrors = [];
    let overviewChanged = false;
    for (const vps of items) {
      const alias = vps.alias || vps.id;
      if (!alias) continue;
      const binding = resolveVPSHostBinding(vps, state.settings.hosts || []);
      if (binding.error) {
        bindingErrors.push(binding.error);
        continue;
      }
      let host = binding.host;
      if (host && host.vpsAlias !== alias) {
        replaceSettings(await api().SaveHost({ ...host, vpsAlias: alias }));
        host = (state.settings.hosts || []).find((item) => item.id === host.id) || host;
      } else if (!host && vps.address) {
        const previousIds = new Set((state.settings.hosts || []).map((item) => item.id));
        replaceSettings(await api().SaveHost({
          name: alias,
          group: "VPS",
          protocol: "ssh",
          address: vps.address,
          port: Number(vps.port || 22),
          username: vps.username || "root",
          password: vps.password || "",
          keyPath: vps.key_path || "",
          authType: vps.key_path ? "key" : "password",
          vpsAlias: alias,
          vpsDomain: vps.domain || "",
          vpsMiddleDomain: vps.middle_domain || "",
          proxyEnabled: Boolean(vps.proxy_host && Number(vps.proxy_port || 0)),
          proxyHost: vps.proxy_host || "127.0.0.1",
          proxyPort: Number(vps.proxy_port || 20808),
          proxyUsername: vps.proxy_username || "",
          proxyPassword: vps.proxy_password || "",
        }));
        host = (state.settings.hosts || []).find(
          (item) => !previousIds.has(item.id) && item.vpsAlias === alias,
        );
        if (host) addedHostIds.push(host.id);
      }
      if (host && String(vps.hostId || "").trim() !== host.id) {
        await window.go.nodemode.App.SaveVPS(hostVPSConfig(host, alias, vps));
        overviewChanged = true;
      }
    }
    if (overviewChanged) state.vpsOverview = await window.go.nodemode.App.GetOverview();
    applyCachedShellVPSStatuses();
    renderHosts();
    if (addedHostIds.length) probeHosts(addedHostIds);
    const newWarnings = bindingErrors.filter((message) => !state.vpsBindingWarnings.has(message));
    bindingErrors.forEach((message) => {
      state.vpsBindingWarnings.add(message);
      console.warn("VPS 主机绑定失败", message);
    });
    if (newWarnings.length) {
      toast(`有 ${newWarnings.length} 个 VPS 无法绑定对应 Shell 主机：${newWarnings[0]}`, true);
    }
    if (!state.vpsStartupChecked) {
      state.vpsStartupChecked = true;
      const starredIds = (state.settings.hosts || [])
        .filter((host) => !host.deleted && host.starred)
        .map((host) => host.id);
      if (starredIds.length) refreshAllVPSStatuses(false, starredIds);
    }
  } catch (x) {
    console.warn("VPS 数据加载失败", x);
  } finally {
    state.vpsLoading = false;
  }
}
const vpsStatusText = (status) => ({ running: "运行中", stopped: "已停止", not_installed: "未安装", ssh_failed: "SSH 失败", failed: "服务失败", error: "检测失败", untrusted: "待首次连接确认指纹", unknown: "未检测" })[status || "unknown"] || "未检测";
const VPS_STATUS_CACHE_KEY = "vps-management-status-cache";
function loadShellVPSStatusCache() {
  try {
    return JSON.parse(localStorage.getItem(VPS_STATUS_CACHE_KEY) || "{}");
  } catch {
    localStorage.removeItem(VPS_STATUS_CACHE_KEY);
    return {};
  }
}
function saveShellVPSStatusCache(alias, result, nodeText = vpsNodeText(result)) {
  alias = String(alias || "").trim();
  if (!alias || !result) return;
  const cache = loadShellVPSStatusCache();
  cache[alias] = {
    singboxStatus: result.status || "unknown",
    nodeText,
    checkedAt: new Date().toISOString(),
  };
  localStorage.setItem(VPS_STATUS_CACHE_KEY, JSON.stringify(cache));
}
function clearShellVPSStatusCache(alias) {
  alias = String(alias || "").trim();
  if (!alias) return;
  const cache = loadShellVPSStatusCache();
  delete cache[alias];
  localStorage.setItem(VPS_STATUS_CACHE_KEY, JSON.stringify(cache));
}
function applyCachedShellVPSStatuses() {
  const cache = loadShellVPSStatusCache();
  for (const vps of state.vpsOverview?.vpsList || []) {
    const alias = String(vps.alias || vps.id || "").trim();
    const cached = cache[alias];
    if (!cached) continue;
    vps.singboxStatus = cached.singboxStatus || vps.singboxStatus || "unknown";
    const host = (state.settings.hosts || []).find((item) => item.vpsAlias === alias && !item.deleted);
    if (!host) continue;
    const awaitingTrust = !String(host.hostKey || "").trim();
    state.vpsStatuses[host.id] = {
      singboxStatus: awaitingTrust ? "untrusted" : cached.singboxStatus || "unknown",
      nodeText: awaitingTrust ? "待首次连接确认指纹" : cached.nodeText || (vps.nodeCount ? `${vps.nodeCount} 个节点` : "无节点"),
      awaitingTrust,
      loading: false,
      checkedAt: Date.parse(cached.checkedAt || "") || 0,
    };
  }
}
const vpsNodeText = (result = {}) => result.synced
  ? `已同步 ${Number(result.nodeCount || 0)} 个节点`
  : Number(result.nodeCount || 0) > 0 ? `${Number(result.nodeCount)} 个节点` : "无节点";
function markHostAwaitingTrust(host, alias = host?.vpsAlias || "") {
  const result = {
    alias,
    status: "untrusted",
    nodeCount: 0,
    startPort: 0,
    synced: false,
    message: "待首次连接确认指纹",
  };
  if (host?.id) {
    state.vpsStatuses[host.id] = {
      singboxStatus: result.status,
      nodeText: result.message,
      awaitingTrust: true,
      loading: false,
      checkedAt: Date.now(),
    };
  }
  return result;
}
function applyVPSRuntime(alias, result) {
  if (!alias || !result) return;
  const vps = (state.vpsOverview?.vpsList || []).find((item) => (item.alias || item.id) === alias);
  if (!vps) return;
  vps.nodeCount = Number(result.nodeCount || 0);
  vps.startPort = Number(result.startPort || 0);
  vps.singboxStatus = result.status || "unknown";
}
function applyShellVPSStatus(alias, result) {
  applyVPSRuntime(alias, result);
  const host = (state.settings.hosts || []).find((item) => item.vpsAlias === alias);
  if (!host || !result) return;
  const awaitingTrust = !String(host.hostKey || "").trim();
  state.vpsStatuses[host.id] = {
    singboxStatus: awaitingTrust ? "untrusted" : result.status || "unknown",
    nodeText: awaitingTrust ? "待首次连接确认指纹" : vpsNodeText(result),
    awaitingTrust,
    loading: false,
    checkedAt: Date.now(),
  };
  saveShellVPSStatusCache(alias, result, vpsNodeText(result));
}
function updateHostVPSRow(host) {
  const entry = [...$("#hostList").querySelectorAll(".host-entry")]
    .find((item) => item.dataset.id === host.id);
  if (!entry) return;
  const vps = (state.vpsOverview?.vpsList || []).find((item) => (item.alias || item.id) === host.vpsAlias);
  const refreshedStatus = state.vpsStatuses[host.id] || {};
  const loading = Boolean(refreshedStatus.loading);
  const currentHost = (state.settings.hosts || []).find((item) => item.id === host.id) || host;
  const awaitingTrust = Boolean(refreshedStatus.awaitingTrust || (!currentHost.hostKey && currentHost.vpsAlias && !loading && !refreshedStatus.singboxStatus));
  const singboxStatus = awaitingTrust ? "untrusted" : refreshedStatus.singboxStatus || vps?.singboxStatus || "unknown";
  const singboxText = loading ? "检测中" : vpsStatusText(singboxStatus);
  const nodeText = loading
    ? "检测中"
    : awaitingTrust ? "待首次连接确认指纹" : refreshedStatus.nodeText || (vps?.nodeCount ? `${vps.nodeCount} 个节点` : "无节点");
  const startPort = entry.querySelector(".vps-start-port");
  const statusButton = entry.querySelector(".vps-status");
  const nodeButton = entry.querySelector(".vps-node-status");
  if (startPort) startPort.textContent = vps?.startPort || "未配置";
  if (statusButton) {
    statusButton.textContent = singboxText;
    statusButton.title = `Sing-box：${singboxText}，点击刷新`;
    statusButton.disabled = loading;
    statusButton.classList.toggle("running", singboxStatus === "running");
    statusButton.classList.toggle("pending-trust", awaitingTrust);
  }
  if (nodeButton) {
    nodeButton.textContent = nodeText;
    nodeButton.title = `节点状态：${nodeText}，点击刷新`;
    nodeButton.disabled = loading;
    nodeButton.classList.toggle("pending-trust", awaitingTrust);
  }
}
async function reloadShellVPSOverview(alias = "", result = null, shouldRender = true) {
  state.vpsOverview = await window.go.nodemode.App.GetOverview();
  applyCachedShellVPSStatuses();
  applyShellVPSStatus(alias, result);
  const host = (state.settings.hosts || []).find((item) => item.vpsAlias === alias);
  if (host && !result) {
    const vps = (state.vpsOverview?.vpsList || []).find((item) => (item.alias || item.id) === alias);
    const awaitingTrust = Boolean(state.vpsStatuses[host.id]?.awaitingTrust || !host.hostKey);
    state.vpsStatuses[host.id] = {
      ...state.vpsStatuses[host.id],
      nodeText: awaitingTrust ? "待首次连接确认指纹" : Number(vps?.nodeCount || 0) > 0 ? `${Number(vps.nodeCount)} 个节点` : "无节点",
      awaitingTrust,
      loading: false,
    };
  }
  if (shouldRender) renderHosts();
}
async function refreshVPSStatus(host, notify = false, shouldRender = true, options = {}) {
  if (state.vpsRefreshes[host.id]) return state.vpsRefreshes[host.id];
  const request = refreshVPSStatusOnce(host, notify, shouldRender, options);
  state.vpsRefreshes[host.id] = request;
  try {
    return await request;
  } finally {
    if (state.vpsRefreshes[host.id] === request) delete state.vpsRefreshes[host.id];
  }
}
async function refreshVPSStatusOnce(host, notify = false, shouldRender = true, options = {}) {
  const allowHostKeyPrompt = options.allowHostKeyPrompt ?? notify;
  let alias = host.vpsAlias || "";
  if (!host.hostKey && !allowHostKeyPrompt) {
    const result = markHostAwaitingTrust(host, alias);
    if (shouldRender) updateHostVPSRow(host);
    return result;
  }
  state.vpsStatuses[host.id] = { ...state.vpsStatuses[host.id], loading: true };
  if (shouldRender) updateHostVPSRow(host);
  try {
    let result = null;
    const linkedVPS = (state.vpsOverview?.vpsList || []).some((item) => (item.alias || item.id) === alias);
    if (!alias || !linkedVPS) {
      const ensured = await ensureHostVPS(host, true, allowHostKeyPrompt);
      alias = ensured.alias;
      result = ensured.runtimeState;
    }
    if (!result) {
      const refresh = allowHostKeyPrompt
        ? window.go.nodemode.App.RefreshVPSStateInteractive
        : window.go.nodemode.App.RefreshVPSState;
      result = await refresh(alias, false);
    }
    if (!host.hostKey && allowHostKeyPrompt) {
      try {
        replaceSettings(await api().GetSettings());
      } catch {}
    }
    applyShellVPSStatus(alias, result);
    if (shouldRender) updateHostVPSRow(host);
    const nodeText = vpsNodeText(result);
    if (notify) {
      const detail = result.message ? `，${result.message}` : "";
      const failed = result.status === "ssh_failed" || result.status === "error" || /失败/.test(result.message || "");
      toast(`${host.name || host.address}：${vpsStatusText(result.status)}，${nodeText}${detail}`, failed);
    }
    return result;
  } catch (error) {
    const result = { alias, status: "ssh_failed", nodeCount: 0, startPort: 20000, synced: false, message: String(error) };
    const currentHost = (state.settings.hosts || []).find((item) => item.id === host.id) || host;
    if (currentHost.hostKey) {
      state.vpsStatuses[host.id] = { singboxStatus: result.status, nodeText: "刷新失败", awaitingTrust: false, loading: false, checkedAt: Date.now() };
    } else {
      markHostAwaitingTrust(currentHost, alias);
    }
    if (shouldRender) updateHostVPSRow(host);
    if (notify) toast(`${host.name || host.address}：${String(error)}`, true);
    return result;
  }
}
async function refreshAllVPSStatuses(interactive = false, hostIds = null, manageButton = true, showResult = interactive) {
  if (state.vpsRefreshingAll) return;
  const selectedIds = Array.isArray(hostIds) ? new Set(hostIds) : null;
  const candidates = (state.settings.hosts || []).filter((host) =>
    !host.deleted
    && (host.protocol || "ssh") === "ssh"
    && host.vpsAlias
    && (!selectedIds || selectedIds.has(host.id))
  );
  if (!interactive) candidates.filter((host) => !host.hostKey).forEach((host) => markHostAwaitingTrust(host));
  const items = interactive ? candidates : candidates.filter((host) => host.hostKey);
  if (!items.length) {
    renderHosts();
    if (showResult) toast("已选主机没有可刷新的节点管理状态", true);
    return { checked: 0, candidates: candidates.length, failures: [] };
  }
  state.vpsRefreshingAll = true;
  const button = $("#refreshAllVps");
  if (manageButton) {
    button.disabled = true;
    button.textContent = "刷新中";
  }
  let index = 0;
  const failures = [];
  const runtimeStates = [];
  const worker = async () => {
    while (index < items.length) {
      const host = items[index++];
      let result = await refreshVPSStatus(host, false, false, { syncRemote: false, allowHostKeyPrompt: interactive, reloadOverview: false });
      if (!interactive && vpsRefreshFailed(result)) {
        state.vpsStatuses[host.id] = { ...state.vpsStatuses[host.id], loading: true, nodeText: "重试中" };
        updateHostVPSRow(host);
        await new Promise((resolve) => window.setTimeout(resolve, 1200));
        result = await refreshVPSStatus(host, false, false, { syncRemote: false, allowHostKeyPrompt: false, reloadOverview: false });
      }
      runtimeStates.push({ alias: host.vpsAlias || result?.alias || "", result });
      if (vpsRefreshFailed(result)) {
        failures.push(`${host.name || host.address}：${result?.message || vpsStatusText(result?.status)}`);
      }
    }
  };
  try {
    await Promise.all([worker(), worker()]);
    state.vpsOverview = await window.go.nodemode.App.GetOverview();
    runtimeStates.forEach((item) => applyVPSRuntime(item.alias, item.result));
    renderHosts();
    if (showResult) toast(failures.length ? `状态刷新完成，${failures.length} 台失败：${failures.slice(0, 2).join("；")}` : "已选主机状态已刷新", failures.length > 0);
    return { checked: items.length, candidates: candidates.length, failures };
  } finally {
    state.vpsRefreshingAll = false;
    if (manageButton) {
      button.disabled = false;
      button.textContent = "刷新状态";
    }
  }
}

async function refreshSelectedHostStatuses() {
  const ids = [...state.hostSelection].filter((id) =>
    (state.settings.hosts || []).some((host) => host.id === id && !host.deleted && !host.temporary)
  );
  if (!ids.length) {
    toast("请先勾选需要刷新的主机", true);
    return;
  }
  if (state.vpsRefreshingAll) {
    toast("主机状态正在刷新，请稍候", true);
    return;
  }
  const button = $("#refreshAllVps");
  button.disabled = true;
  button.textContent = `刷新中 0/${ids.length}`;
  try {
    const [, vpsResult] = await Promise.all([
      checkHostLatencies(ids),
      refreshAllVPSStatuses(true, ids, false, false),
    ]);
    const failed = vpsResult?.failures || [];
    const vpsText = vpsResult?.checked
      ? `，Sing-box/节点状态 ${vpsResult.checked - failed.length}/${vpsResult.checked} 台`
      : "，无节点管理状态";
    toast(
      failed.length
        ? `已刷新 ${ids.length} 台主机延迟${vpsText}；${failed.length} 台状态失败：${failed.slice(0, 2).join("；")}`
        : `已刷新 ${ids.length} 台主机延迟${vpsText}`,
      failed.length > 0,
    );
  } finally {
    button.disabled = false;
    button.textContent = "刷新状态";
  }
}
function vpsRefreshFailed(result) {
  return !result || result.status === "ssh_failed" || result.status === "error" || /失败/.test(result.message || "");
}
function vpsActionButtons() {
  return `<button class="refreshVpsStatus" type="button">刷新状态</button><button class="primary" data-vps-action="openAddNode">新增节点</button><button class="success" data-vps-action="openAddMultiIPNode">新增多IP节点</button><button data-vps-action="deploySingbox">部署</button><button class="primary" data-vps-action="restartSingbox">重启</button><button class="danger-soft" data-vps-action="rebootVps">重启VPS</button><button class="warning" data-vps-action="syncAllJson">同步配置</button><button data-vps-action="openVpsJSON">编辑JSON</button><button data-vps-action="openDebug">调试</button><button data-vps-action="viewSingboxLog">日志</button><button data-vps-action="openModifyOutbound">修改落地</button><button class="success" data-vps-action="trafficRank">流量</button><button data-vps-action="openBackups">备份还原</button>`;
}
function hostVPSConfig(host, alias, existing = {}, originalAlias = "") {
  const credential = (state.settings.credentials || []).find((item) => item.id === host.credentialId);
  const password = credential?.type === "password" ? credential.password : host.password || "";
  return {
    ...existing,
    hostId: host.id,
    id: alias,
    alias,
    // 修改 Shell 主机名称时，向节点模式传递旧别名，让后端迁移节点配置而不是新建一组空节点。
    original_alias: originalAlias || existing.alias || existing.id || alias,
    address: host.address,
    port: Number(host.port || 22),
    username: host.username || "root",
    password,
    key_path: host.keyPath || "",
    domain: host.vpsDomain || "",
    middle_domain: host.vpsMiddleDomain || "",
    countryCode: /^[A-Za-z]{2}$/.test(host.country || "") ? host.country.toUpperCase() : "",
    proxy_host: host.proxyEnabled ? host.proxyHost || "127.0.0.1" : "",
    proxy_port: host.proxyEnabled ? Number(host.proxyPort || 20808) : 0,
    proxy_username: host.proxyEnabled ? host.proxyUsername || "" : "",
    proxy_password: host.proxyEnabled ? host.proxyPassword || "" : "",
  };
}
async function syncHostVPS(host, alias, previousAlias = "") {
  const existing = (state.vpsOverview?.vpsList || []).find((item) => {
    const itemAlias = String(item.alias || item.id || "");
    return itemAlias.toLowerCase() === String(alias || "").toLowerCase() ||
      (previousAlias && itemAlias.toLowerCase() === String(previousAlias).toLowerCase());
  }) || {};
  await window.go.nodemode.App.SaveVPS(hostVPSConfig(host, alias, existing, previousAlias));
  state.vpsOverview = await window.go.nodemode.App.GetOverview();
}
async function ensureHostVPS(host, syncRemoteOnBind = true, allowHostKeyPrompt = false) {
  const linkedVPS = (state.vpsOverview?.vpsList || []).find((item) => (item.alias || item.id) === host.vpsAlias);
  const binding = !host.vpsAlias || !linkedVPS;
  let alias = host.vpsAlias || "";
  if (!binding) return { alias, runtimeState: null, binding: false };
  if (!alias) {
    const items = state.vpsOverview?.vpsList || [];
    const usedAliases = new Set(items.map((item) => String(item.alias || item.id || "").toLowerCase()));
    const baseAlias = String(host.name || host.address || "VPS").trim();
    alias = baseAlias;
    for (let suffix = 2; usedAliases.has(alias.toLowerCase()); suffix++) alias = `${baseAlias}-${suffix}`;
  }
  await syncHostVPS(host, alias);
  replaceSettings(await api().SaveHost({ ...host, vpsAlias: alias }));
  let runtimeState = null;
  if (syncRemoteOnBind) {
    try {
      const refresh = allowHostKeyPrompt
        ? window.go.nodemode.App.RefreshVPSStateInteractive
        : window.go.nodemode.App.RefreshVPSState;
      runtimeState = await refresh(alias, false);
    } catch (error) {
      runtimeState = { alias, status: "ssh_failed", nodeCount: 0, startPort: 20000, synced: false, message: String(error) };
    }
  }
  state.vpsOverview = await window.go.nodemode.App.GetOverview();
  if (runtimeState) applyVPSRuntime(alias, runtimeState);
  renderHosts();
  if (runtimeState && (runtimeState.status === "ssh_failed" || /失败/.test(runtimeState.message || ""))) {
    toast(`${alias}：${runtimeState.message || "服务器状态读取失败"}`, true);
  }
  return { alias, runtimeState, binding: true };
}
async function detectHostCountries() {
  let changed = false;
  for (const host of (state.settings.hosts || []).filter((item) => !item.deleted && !item.country)) {
    try {
      replaceSettings(await api().DetectHostCountry(host.id));
      changed = true;
    } catch {}
  }
  if (changed) renderHosts();
}
async function probeHosts(ids) {
  const uniqueIds = [...new Set(ids.filter(Boolean))];
  let changed = false;
  for (const id of uniqueIds) {
    const host = (state.settings.hosts || []).find((item) => item.id === id);
    if (!host || host.deleted || host.country) continue;
    try {
      replaceSettings(await api().DetectHostCountry(id));
      changed = true;
    } catch {}
  }
  if (changed) renderHosts();
  await checkHostLatencies(uniqueIds);
}
let themeTransitionTimer = 0;
function applyTheme(_theme, _animate = true) {
  const root = document.documentElement;
  window.clearTimeout(themeTransitionTimer);
  root.classList.remove("theme-changing");
  root.dataset.theme = "dark";
  root.style.colorScheme = "dark";
  try {
    localStorage.setItem(THEME_CACHE_KEY, "dark");
  } catch {}
  syncNodeFrameSurface();
  for (const session of Object.values(state.sessions)) {
    if (session.terminal) session.terminal.options.theme = terminalTheme();
  }
}

function setSecurityMessage(message = "", bad = false) {
  const element = $("#securityMessage");
  element.textContent = message;
  element.classList.toggle("bad", bad && Boolean(message));
}

function resetPasswordVisibility(...ids) {
  for (const id of ids) {
    const input = $(`#${id}`);
    const button = document.querySelector(`[data-password-target="${id}"]`);
    if (!input || !button) continue;
    input.type = "password";
    button.textContent = "显示";
    button.setAttribute("aria-pressed", "false");
  }
}

function updateSecurityUI() {
  const configured = state.securityBaseline.configured;
  const enabled =
    $("#lockStartupSetting").checked || $("#protectExportSetting").checked;
  const fields = $("#securityPasswordFields");
  $("#securityStatus").textContent = configured ? "已设置" : "未设置";
  $("#securityStatus").classList.toggle("configured", configured);

  fields.classList.toggle("hidden", !configured && !enabled);
  $("#currentSecurityField").classList.toggle("hidden", !configured);
  $("#newSecurityField").classList.toggle("hidden", configured && !enabled);
  $("#confirmSecurityField").classList.toggle("hidden", configured && !enabled);
  fields.classList.toggle("no-current", !configured);
  fields.classList.toggle("current-only", configured && !enabled);

  if (!configured) {
    $("#securityHint").textContent = enabled
      ? "请设置 8～128 个字符的密保，并再次输入确认。"
      : "启用任一保护选项后设置密保。";
  } else if (!enabled) {
    $("#securityHint").textContent =
      "保存后将停用并删除密保，需要输入当前密保确认。";
  } else {
    $("#securityHint").textContent =
      "留空不会修改现有密保；修改保护选项或密保时需输入当前密保。";
  }
  setSecurityMessage();
}

function validateNewSecurityPassword(password, confirmation, required) {
  if (!password && !confirmation && !required) return "";
  if (!password) return "请输入新密保";
  const length = Array.from(password).length;
  if (length < 8 || length > 128) return "新密保必须为 8～128 个字符";
  if (password !== confirmation) return "两次输入的新密保不一致";
  return "";
}

async function openSettings() {
  $("#hostEditor").classList.add("hidden");
  $("#hostDrawer").classList.add("hidden");
  $("#aboutPage").classList.add("hidden");
  $("#settingsPage").classList.remove("hidden");
  const [settings, appInfo] = await Promise.all([
    api().GetPublicSettings(),
    api().GetAppInfo().catch(() => null),
  ]);
  if (appInfo?.version && $("#settingsAppVersion")) {
    $("#settingsAppVersion").textContent = `v${String(appInfo.version).replace(/^v/i, "")}`;
  }
  $("#downloadDirectory").value = settings.downloadDirectory || "";
  $("#autoStartSetting").checked = settings.autoStart;
  $("#lockStartupSetting").checked = settings.lockOnStartup;
  $("#protectExportSetting").checked = settings.protectExport;
  $("#aiPresetPrompt").value = settings.aiPresetPrompt || "";
  state.securityBaseline = {
    configured: Boolean(settings.securityConfigured),
    lockOnStartup: Boolean(settings.lockOnStartup),
    protectExport: Boolean(settings.protectExport),
  };
  $("#currentSecurityPassword").value = "";
  $("#newSecurityPassword").value = "";
  $("#confirmSecurityPassword").value = "";
  resetPasswordVisibility(
    "currentSecurityPassword",
    "newSecurityPassword",
    "confirmSecurityPassword",
  );
  updateSecurityUI();
  renderSettingsAI();
}
function renderHosts() {
  const previousScroll = $("#hostList")?.querySelector(".host-list-scroll");
  const previousScrollTop = previousScroll?.scrollTop || 0;
  const previousScrollLeft = previousScroll?.scrollLeft || 0;
  const existingIds = new Set((state.settings?.hosts || []).map((host) => host.id));
  for (const id of state.hostSelection) if (!existingIds.has(id)) state.hostSelection.delete(id);
  for (const id of state.expandedHosts) if (!existingIds.has(id)) state.expandedHosts.delete(id);
  for (const host of state.settings?.hosts || []) {
    if (host.temporary) state.hostSelection.delete(host.id);
  }
  const q = $("#search").value.toLowerCase();
  const group = $("#groupFilter")?.value || "";
  const all = state.settings.hosts || [];
  const vpsByAlias = new Map((state.vpsOverview?.vpsList || []).map((item) => [item.alias || item.id, item]));
  const hostCountry = (host) => String(host.country || vpsByAlias.get(host.vpsAlias)?.countryCode || "").trim().toUpperCase();
  const countries = [...new Set(all.map(hostCountry).filter((country) => country && country !== "--"))].sort((a, b) => a.localeCompare(b));
  const countryFilter = $("#countryFilter");
  if (countryFilter) {
    const oldCountry = countryFilter.value;
    countryFilter.innerHTML = '<option value="">全部国家</option>' + countries.map((country) => `<option value="${esc(country)}">${esc(country)}</option>`).join("");
    countryFilter.value = countries.includes(oldCountry) ? oldCountry : "";
  }
  const country = countryFilter?.value || "";
  const groups = [
    ...new Set([
      ...(state.settings.groups || []),
      ...all.map((h) => h.group).filter(Boolean),
    ]),
  ];
  if ($("#groupFilter")) {
    const old = $("#groupFilter").value;
    $("#groupFilter").innerHTML =
      '<option value="">全部分组</option>' +
      groups
        .map((item) => `<option value="${esc(item)}">${esc(item)}</option>`)
        .join("");
    $("#groupFilter").value = old;
  }
  $("#recentFilter").classList.toggle("active", state.recentOnly);
  $("#recentFilter").setAttribute("aria-pressed", String(state.recentOnly));
  let hosts = all.filter(
    (h) =>
      `${h.name}${h.address}${h.username}${h.group}`
        .toLowerCase()
        .includes(q) &&
      (!group || h.group === group) &&
      (!country || hostCountry(h) === country) &&
      ($("#showDeleted")?.checked ? h.deleted : !h.deleted) &&
      (!state.recentOnly || h.lastUsed),
  );
  const sort = $("#sortHosts")?.value || "manual";
  if (sort === "name") hosts.sort((a, b) => a.name.localeCompare(b.name));
  if (sort === "address")
    hosts.sort((a, b) => a.address.localeCompare(b.address));
  if (sort === "recent")
    hosts.sort((a, b) =>
      String(b.lastUsed || "").localeCompare(String(a.lastUsed || "")),
    );
  const hostScope = $("#showDeleted")?.checked
    ? "已删除"
    : state.recentOnly ? "最近" : "全部";
  $("#hostCount").textContent = `${hostScope} · ${hosts.length} 台`;
  const rows = hosts
    .map((h) => {
      const vps = vpsByAlias.get(h.vpsAlias);
      const refreshedStatus = state.vpsStatuses[h.id] || {};
      const awaitingTrust = Boolean(refreshedStatus.awaitingTrust || ((h.protocol || "ssh") === "ssh" && h.vpsAlias && !h.hostKey && !refreshedStatus.loading && !refreshedStatus.singboxStatus));
      const singboxStatus = awaitingTrust ? "untrusted" : refreshedStatus.singboxStatus || vps?.singboxStatus || "unknown";
      const singboxText = refreshedStatus.loading ? "检测中" : vpsStatusText(singboxStatus);
      const nodeText = refreshedStatus.loading ? "检测中" : awaitingTrust ? "待首次连接确认指纹" : refreshedStatus.nodeText || (vps?.nodeCount ? `${vps.nodeCount} 个节点` : "无节点");
      const expanded = state.expandedHosts.has(h.id);
      const actions = h.deleted
        ? `<button class="restoreHost" data-id="${esc(h.id)}">恢复</button><button class="permanentDeleteHost" data-id="${esc(h.id)}">永久删除</button>`
        : h.temporary
          ? `<button class="connectHost primary" data-id="${esc(h.id)}">连接</button><button class="saveTemporaryHost" data-id="${esc(h.id)}" title="保存到主机库">保存</button><button class="closeTemporaryHost" data-id="${esc(h.id)}" title="关闭并移除临时主机">关闭</button>`
          : `<button class="starHost ${h.starred ? "active" : ""}" data-id="${esc(h.id)}" type="button" aria-pressed="${h.starred ? "true" : "false"}" title="${h.starred ? "取消星标" : "设为星标主机"}">${h.starred ? "★" : "☆"}</button><button class="connectHost primary" data-id="${esc(h.id)}">连接</button><button class="editHost" data-id="${esc(h.id)}">编辑</button><button class="deleteHost" data-id="${esc(h.id)}" title="删除主机">删除</button>`;
      const detail = !expanded ? "" : (h.protocol || "ssh") === "ssh"
        ? `<div class="host-vps-detail" data-alias="${esc(h.vpsAlias || "")}"><div class="host-vps-detail-title"><span>起始端口</span><span>入站域名</span><span>中转域名</span><span>Sing-box</span><span>节点状态</span><span>操作</span></div><div class="host-vps-detail-row"><span class="vps-start-port">${vps?.startPort || "未配置"}</span><span title="${esc(vps?.domain || h.vpsDomain || "")}">${esc(vps?.domain || h.vpsDomain || "未设置")}</span><span title="${esc(vps?.middle_domain || h.vpsMiddleDomain || "")}">${esc(vps?.middle_domain || h.vpsMiddleDomain || "未设置")}</span><button class="vps-status refreshVpsStatus ${singboxStatus === "running" ? "running" : ""} ${awaitingTrust ? "pending-trust" : ""}" type="button" title="Sing-box：${esc(singboxText)}，点击刷新" ${refreshedStatus.loading ? "disabled" : ""}>${esc(singboxText)}</button><button class="vps-node-status refreshVpsStatus ${awaitingTrust ? "pending-trust" : ""}" type="button" title="节点状态：${esc(nodeText)}，点击刷新" ${refreshedStatus.loading ? "disabled" : ""}>${esc(nodeText)}</button><span class="host-vps-actions">${vpsActionButtons()}</span></div></div>`
        : '<div class="host-vps-detail host-vps-empty">RDP 主机不支持 Sing-box 节点管理</div>';
      return `<article class="host-entry ${expanded ? "expanded" : ""}" data-id="${esc(h.id)}"><div class="host host-row ${h.id === state.selected ? "selected" : ""} ${h.deleted ? "deleted" : ""} ${h.temporary ? "temporary" : ""}" data-id="${esc(h.id)}"><span class="host-select"><input class="host-check" data-id="${esc(h.id)}" type="checkbox" aria-label="选择 ${esc(h.name || h.address || "主机")}" ${state.hostSelection.has(h.id) ? "checked" : ""} ${h.temporary ? 'disabled title="临时主机需先保存到主机库"' : ""}></span><span class="host-name" title="${esc(h.name || h.address || "未命名主机")}"><b>${esc(h.name || h.address || "未命名主机")}</b></span><span class="host-address" title="${esc(h.address || "—")}">${esc(h.address || "—")}</span><span><button class="countryBtn ${h.countryManual ? "manual" : ""}" data-id="${esc(h.id)}" title="点击修改国家缩写">${esc(h.country || "—")}</button></span><span><button class="latencyBtn ${state.latencies[h.id]?.className || ""}" data-id="${esc(h.id)}" title="${esc(state.latencies[h.id]?.title || "点击检测延迟")}">${esc(state.latencies[h.id]?.text || "检测")}</button></span><span><em class="protocol">${esc((h.protocol || "ssh").toUpperCase())}</em></span><span title="${esc(h.group || "")}">${esc(h.group || "—")}</span><span title="${esc(h.remark || "")}">${esc(h.remark || "—")}</span><span class="host-row-actions">${actions}</span></div>${expanded && !h.deleted ? detail : ""}</article>`;
    })
    .join("");
  const visibleIds = hosts.filter((host) => !host.temporary).map((host) => host.id);
  const selectedVisible = visibleIds.filter((id) => state.hostSelection.has(id)).length;
  const batchDeleteAction = selectedVisible
    ? $("#showDeleted")?.checked
      ? `<button id="batchPermanentDeleteHosts" type="button">批量永久删除 ${selectedVisible}</button>`
      : `<button id="batchDeleteHosts" type="button">批量删除 ${selectedVisible}</button>`
    : "";
  $("#hostList").className = "host-view-list";
  $("#hostList").innerHTML = `<div class="host-list-scroll">${hosts.length
    ? `<div class="host-table-head"><span class="host-select"><input id="hostSelectAll" type="checkbox" aria-label="全选当前列表" title="全选当前列表"></span><span>主机名</span><span>机器 IP</span><span>国家</span><span>延迟</span><span>协议</span><span>分组</span><span>备注</span><span class="host-actions-head"><span>操作</span>${batchDeleteAction}</span></div>${rows}`
    : '<div class="host-empty"><b>暂无主机</b><span>调整筛选条件，或点击“添加主机”。</span></div>'}</div>`;
  const nextScroll = $("#hostList").querySelector(".host-list-scroll");
  nextScroll.scrollTop = previousScrollTop;
  nextScroll.scrollLeft = previousScrollLeft;
  const selectAll = $("#hostSelectAll");
  if (selectAll) {
    selectAll.disabled = visibleIds.length === 0;
    selectAll.checked = visibleIds.length > 0 && selectedVisible === visibleIds.length;
    selectAll.indeterminate = selectedVisible > 0 && selectedVisible < visibleIds.length;
  }
  $("#exportHosts").textContent = state.hostSelection.size ? `导出 (${state.hostSelection.size})` : "导出";
  updateBatchConnectButton();
  updateTerminalToolButtons();
}
function selectHost(id) {
  if (state.selected !== id) closeTerminalSearch(false);
  if (state.selected && state.sessions[state.selected]) {
    state.sessions[state.selected].terminal?.clearSelection();
  }
  adaptTerminalSplitForSelection(id);
  state.selected = id;
  const s = sessionFor(id);
  state.path = s.path || "/";
  state.files = s.files || [];
  const h = selected();
  if (!h) return;
  const ssh = String(h.protocol || "ssh").toLowerCase() === "ssh";
  state.connected = Boolean(s.connected && ssh);
  const hostName = h.name || h.address || "新会话";
  const username =
    h.username || ((h.protocol || "ssh") === "rdp" ? "administrator" : "root");
  $("#sideAddress").textContent = s.connected ? h.address : (!ssh && h.address ? h.address : "未连接");
  $("#latencyTarget").textContent = sessionLatencyTargetText(h);
  showSideIdentity(username, hostName);
  $("#copyHostIP").classList.toggle("hidden", !h.address);
  $("#sideReconnect").classList.remove("hidden");
  $("#onlineDot").className = s.connected ? "online" : "";
  $("#hostDrawer").classList.add("hidden");
  renderHosts();
  renderSessionTabs();
  syncTerminalReconnectBanner(id);
  restoreSessionView(s);
  if (ssh && !s.output) {
    resetTerminal(id, "SanTianShell 安全远程终端\r\n点击“重连”或主机列表中的“连接”。\r\n");
  }
  showTerminal(id, true);
  refreshSSHSessionLogStatus(id);
  if (s.connected && ssh) {
    startMonitor();
    rescheduleSessionMonitor(s, id);
    syncActiveMonitorStream();
    if (!s.fileUpdatedAt || Date.now() - s.fileUpdatedAt >= FILE_INTERVAL)
      s.fileDueAt = 0;
    runMonitorScheduler();
    runFileScheduler();
  } else {
    syncActiveMonitorStream();
  }
}
function resetMonitor() {
  $("#uptime").textContent = "未连接";
  $("#loadValue").textContent = "—";
  setMeter("cpu", 0);
  setMeter("mem", 0);
  setMeter("swap", 0);
  $("#netUp").textContent = "0 B/s";
  $("#netDown").textContent = "0 B/s";
  $("#networkState").textContent = "未连接";
  $("#networkChart").innerHTML = "";
  $("#latencyCurrent").firstChild.textContent = "—";
  $("#packetLoss").firstChild.textContent = "—";
  $("#latencyMin").firstChild.textContent = "—";
  $("#latencyMax").firstChild.textContent = "—";
  $("#latencyChart").innerHTML = "";
  state.processes = [];
  $("#processUpdated").textContent = "未连接";
  renderProcesses();
  $("#diskUpdated").textContent = "未连接";
  $("#diskRows").innerHTML =
    '<tr><td colspan="2" class="placeholder">暂无数据</td></tr>';
  $("#dirHint").textContent = "未连接";
  $("#treeItems").innerHTML = '<span class="placeholder">暂无数据</span>';
  $("#fileRows").innerHTML =
    '<tr><td colspan="6" class="placeholder">暂无数据</td></tr>';
  $("#path").value = "";
}
function setMeter(name, value) {
  const valid = value !== null && value !== "" && Number.isFinite(Number(value));
  const n = valid ? Math.max(0, Math.min(100, Number(value))) : 0;
  const width = `${n}%`;
  const text = valid ? `${Math.round(n)}%` : "—";
  if ($(`#${name}Bar`).style.width !== width) $(`#${name}Bar`).style.width = width;
  if ($(`#${name}Text`).textContent !== text) $(`#${name}Text`).textContent = text;
}
function monitorSection(output, name) {
  return (String(output || "").split(`__${name}__`)[1] || "")
    .split(/__[A-Z]+__/)[0]
    .trim();
}
function monitorFreshness(updatedAt, expectedInterval, error, liveText = "实时") {
  if (!updatedAt) return error ? "更新失败" : "等待采样…";
  const time = new Date(updatedAt).toLocaleTimeString("zh-CN", { hour12: false });
  if (error) return `更新失败 · ${time}`;
  return `${Date.now() - updatedAt > expectedInterval * 2.5 ? "数据延迟" : liveText} · ${time}`;
}
function renderProcesses(session) {
  if (session) state.processes = session.processes || [];
  const rows = state.processes.slice(0, state.processLimit);
  $("#processRows").innerHTML = rows.length
    ? rows
        .map(
          (p) =>
            `<tr><td>${esc(p.pid)}</td><td>${esc(p.cpu)}%</td><td>${esc(p.memory)}%</td><td title="${esc(p.command)}">${esc(p.command)}</td></tr>`,
        )
        .join("")
    : `<tr><td colspan="4" class="placeholder">${session?.processError ? "进程更新失败" : (session?.connected ? "正在读取进程列表…" : "暂无数据")}</td></tr>`;
  if (session) {
    $("#processUpdated").textContent = (session.processLoading && !session.processUpdatedAt) || (session.connected && !session.processUpdatedAt && !session.processError)
      ? "正在采样…"
      : monitorFreshness(
          session.processUpdatedAt,
          monitorDelay("process", state.selected),
          session.processError,
        );
  }
}
function renderDisks(session) {
  const disks = session?.disks || [];
  $("#diskRows").innerHTML = disks.length
    ? disks
        .map(
          (disk) =>
            `<tr><td title="${esc(disk.path)}">${esc(disk.path)}</td><td title="${esc(`${disk.used}/${disk.size} · ${disk.percent}`)}">${esc(disk.used)}/${esc(disk.size)} · ${esc(disk.percent)}</td></tr>`,
        )
        .join("")
    : `<tr><td colspan="2" class="placeholder">${session?.diskError ? "磁盘更新失败" : (session?.connected ? "正在读取磁盘信息…" : "暂无数据")}</td></tr>`;
  if (session) {
    $("#diskUpdated").textContent = (session.diskLoading && !session.diskUpdatedAt) || (session.connected && !session.diskUpdatedAt && !session.diskError)
      ? "正在采样…"
      : monitorFreshness(
          session.diskUpdatedAt,
          monitorDelay("disk", state.selected),
          session.diskError,
          "已更新",
        );
  }
}
function renderFastMonitor(session) {
  const uptime = monitorSection(session?.monitorOutput, "UPTIME"),
    mem = monitorSection(session?.monitorOutput, "MEM");
  $("#uptime").textContent =
    uptime.match(/up\s+(.+?),\s+\d+ user/)?.[1] || (uptime ? "运行中" : "等待采样");
  $("#loadValue").textContent =
    uptime.match(/load average:\s*(.+)/)?.[1] || "—";
  setMeter("cpu", session?.cpuPercent);
  const memLine =
    mem
      .split("\n")
      .find((line) => /^Mem:/.test(line.trim()))
      ?.trim()
      .split(/\s+/) || [];
  const swapLine =
    mem
      .split("\n")
      .find((line) => /^Swap:/.test(line.trim()))
      ?.trim()
      .split(/\s+/) || [];
  const memTotal = Number(memLine[1]),
    memAvailable = Number(memLine[6]),
    memUsed = Number(memLine[2]);
  setMeter(
    "mem",
    memTotal > 0
      ? ((memTotal - (Number.isFinite(memAvailable) ? memAvailable : memTotal - memUsed)) / memTotal) * 100
      : null,
  );
  const swapTotal = Number(swapLine[1]);
  setMeter(
    "swap",
    swapLine.length ? (swapTotal > 0 ? (Number(swapLine[2]) / swapTotal) * 100 : 0) : null,
  );
}
function renderNetwork(session) {
  const hasSample = Boolean(session?.networkUpdatedAt);
  $("#netUp").textContent = hasSample ? formatRate(session.networkUp || 0) : (session?.networkSample ? "0 B/s" : "—");
  $("#netDown").textContent = hasSample ? formatRate(session.networkDown || 0) : (session?.networkSample ? "0 B/s" : "—");
  $("#networkState").textContent = session?.networkSample && !hasSample
    ? "正在计算速率…"
    : monitorFreshness(
        session?.networkUpdatedAt,
        monitorDelay("fast", state.selected),
        session?.networkError || session?.monitorError,
      );
  const history = session?.networkHistory || [],
    sorted = history.slice().sort((a, b) => a - b),
    peak = Math.max(1, sorted[Math.max(0, Math.ceil(sorted.length * 0.9) - 1)] || 1);
  $("#networkChart").innerHTML = history
    .map((value) => `<i style="height:${Math.max(2, Math.min(100, Math.round((value / peak) * 100)))}%"></i>`)
    .join("");
  renderCurrentMonitorFreshness();
}
function renderCurrentMonitorFreshness() {
  const session = state.sessions[state.selected];
  if (!session?.connected || !monitorPageVisible()) return;
  const manualRefreshing = Boolean(session.monitorManualRefresh);
  const networkText = (session.monitorLoading && !session.networkUpdatedAt) || (!session.networkUpdatedAt && !session.networkError && !session.monitorError && !session.networkSample)
      ? "正在采样…"
      : manualRefreshing
      ? "更新中…"
      : (session.networkSample && !session.networkUpdatedAt ? "正在计算速率…" : monitorFreshness(
          session.networkUpdatedAt,
          monitorDelay("fast", state.selected),
          session.networkError || session.monitorError,
        )),
    processText = (session.processLoading && !session.processUpdatedAt) || (!session.processUpdatedAt && !session.processError)
      ? "正在采样…"
      : manualRefreshing
      ? "更新中…"
      : monitorFreshness(
          session.processUpdatedAt,
          monitorDelay("process", state.selected),
          session.processError,
        ),
    diskText = (session.diskLoading && !session.diskUpdatedAt) || (!session.diskUpdatedAt && !session.diskError)
      ? "正在采样…"
      : manualRefreshing
      ? "更新中…"
      : monitorFreshness(
          session.diskUpdatedAt,
          monitorDelay("disk", state.selected),
          session.diskError,
          "已更新",
        ),
    fastText = monitorFreshness(
      session.monitorUpdatedAt,
      monitorDelay("fast", state.selected),
      session.monitorError,
    );
  $("#networkState").textContent = networkText;
  $("#processUpdated").textContent = processText;
  $("#diskUpdated").textContent = diskText;
  $("#diskUpdated").title = diskText;
  $("#uptime").title = session.cpuError ? `${fastText} · ${session.cpuError}` : fastText;
  if (session.monitorError) {
    const time = session.monitorUpdatedAt
      ? new Date(session.monitorUpdatedAt).toLocaleTimeString("zh-CN", { hour12: false })
      : "";
    $("#uptime").textContent = time ? `更新失败 · ${time}` : "更新失败";
  }
}
function updateSessionCPU(session, output) {
  const values = monitorSection(output, "CPU").split(/\s+/).map(Number);
  if (values.length < 2 || !values.slice(0, 2).every(Number.isFinite)) {
    session.cpuError = "未获取到 CPU 计数";
    session.cpuPercent = null;
    return false;
  }
  const current = { total: values[0], idle: values[1] },
    previous = session.cpuSample;
  session.cpuSample = current;
  session.cpuError = "";
  if (!previous) return false;
  const total = current.total - previous.total,
    idle = current.idle - previous.idle;
  if (total <= 0 || idle < 0 || idle > total) {
    session.cpuPercent = null;
    session.cpuError = "CPU 计数已重置，正在重新采样";
    return false;
  }
  session.cpuPercent = Math.max(0, Math.min(100, ((total - idle) * 100) / total));
  return true;
}
function parseNetworkSamples(output) {
  const network = monitorSection(output, "NETWORK");
  return network
    .split("\n")
    .filter(Boolean)
    .map((line) => {
      const parts = line.trim().split(/\s+/);
      if (parts.length >= 4 && !Number.isFinite(Number(parts[0]))) {
        return {
          iface: parts[0],
          rx: Number(parts[1]),
          tx: Number(parts[2]),
          at: Number(parts[3]) * 1000,
        };
      }
      return {
        iface: "",
        rx: Number(parts[0]),
        tx: Number(parts[1]),
        at: Number.isFinite(Number(parts[2])) ? Number(parts[2]) * 1000 : Date.now(),
      };
    })
    .filter((sample) => [sample.rx, sample.tx, sample.at].every(Number.isFinite));
}
function updateSessionNetwork(session, output) {
  const samples = parseNetworkSamples(output);
  if (!samples.length) {
    session.networkError = "未获取到网络计数";
    return false;
  }
  const current = samples[samples.length - 1],
    elapsed = current.at - (session.networkSample?.at || current.at);
  let down = 0,
    up = 0,
    sampled = false;
  if (
    session.networkSample &&
    session.networkSample.iface === current.iface &&
    elapsed >= 100 &&
    elapsed <= 60000 &&
    current.rx >= session.networkSample.rx &&
    current.tx >= session.networkSample.tx
  ) {
    down = ((current.rx - session.networkSample.rx) * 1000) / elapsed;
    up = ((current.tx - session.networkSample.tx) * 1000) / elapsed;
    sampled = true;
  }
  session.networkSample = current;
  session.networkError = "";
  if (!sampled) {
    session.networkUpdatedAt = 0;
    session.networkHistory = [];
    return false;
  }
  session.networkDown = Math.max(0, down);
  session.networkUp = Math.max(0, up);
  session.networkHistory.push(session.networkDown + session.networkUp);
  session.networkHistory = session.networkHistory.slice(-24);
  session.networkUpdatedAt = Date.now();
  return true;
}
function parseProcesses(output) {
  return monitorSection(output, "PROCESS")
    .split("\n")
    .slice(1)
    .filter(Boolean)
    .map((line) => {
      const parts = line.trim().split(/\s+/);
      return {
        pid: parts[0],
        cpu: (parts[1] || "0").replace(/%$/, ""),
        memory: (parts[2] || "0").replace(/%$/, ""),
        command: parts.slice(3).join(" "),
      };
    })
    .filter((process) => /^\d+$/.test(process.pid));
}
function parseDisks(output) {
  return monitorSection(output, "DISK")
    .split("\n")
    .slice(1)
    .map((line) => line.trim().match(/^\S+\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s+(.+)$/))
    .filter(Boolean)
    .map((parts) => ({
      size: parts[1],
      used: parts[2],
      percent: parts[4],
      path: parts[5].replace(/\\040/g, " "),
    }));
}
function validateFastMonitorOutput(output) {
  const cpu = monitorSection(output, "CPU").split(/\s+/).map(Number),
    memLine = monitorSection(output, "MEM")
      .split("\n")
      .find((line) => /^Mem:\s+\d+/.test(line.trim()));
  if (cpu.length < 2 || !cpu.slice(0, 2).every(Number.isFinite))
    throw new Error("CPU 采样数据不完整");
  if (!memLine || !(Number(memLine.trim().split(/\s+/)[1]) > 0))
    throw new Error("内存采样数据不完整");
  if (!parseNetworkSamples(output).length)
    throw new Error("网络采样数据不完整");
}
async function connectHostById(id, renderHostList = true, renderTabs = true, options = {}) {
  const host = state.settings?.hosts.find((item) => item.id === id);
  if (!host) return { status: "failed", error: "主机不存在" };
  const session = sessionFor(id);
  if (session.connected) return { status: "connected", skipped: true };
  if (session.connecting || session.closing) return { status: "busy" };
  const epoch = advanceConnectionEpoch(session, id);
  session.connecting = true;
  if (renderHostList) renderHosts();
  if (renderTabs) renderSessionTabs();
  try {
    if ((host.protocol || "ssh") === "rdp") {
      appendRDPLog(id, `发起 RDP 连接：目标 ${host.address}:${host.port || 3389}`);
      if (state.selected === id) renderRDPDeck(id);
      const rdpSessionId = await api().OpenRDP(id);
      if (session.connectionEpoch !== epoch || state.sessions[id] !== session) {
        await api().CloseRDP(rdpSessionId).catch(() => {});
        return { status: "cancelled" };
      }
      session.rdpSessionId = rdpSessionId;
      session.connected = true;
      session.autoReconnect = false;
      updateSessionTabState(id);
      appendRDPLog(id, `远程桌面会话已就绪 (会话ID: ${rdpSessionId.slice(0, 8)})`);
      if (state.selected === id) {
        state.connected = false;
        $("#onlineDot").className = "online";
        $("#sideAddress").textContent = host.address;
        $("#copyHostIP").classList.remove("hidden");
        renderRDPDeck(id);
      }
      return { status: "rdp", sessionId: rdpSessionId };
    }
    session.targetAddress = `${host.address}:${host.port || 22}`;
    if (options.reconnecting) {
      writeTerminalOutput(id, `\r\n[重连] 正在连接 ${session.targetAddress}…\r\n`);
    } else {
      resetTerminal(id, `[连接] 正在连接 ${session.targetAddress}…\r\n`);
    }
    if (state.selected === id) {
      showTerminal(id);
      $("#uptime").textContent = "连接中…";
      $("#processUpdated").textContent = "连接中…";
      $("#networkState").textContent = "连接中…";
      $("#diskUpdated").textContent = "连接中…";
    }
    const initSize = measureInitialTerminalSize(id);
    const terminalAPI = api();
    let startResult = null;
    if (typeof terminalAPI?.StartTerminalInteractiveSized === "function") {
      startResult = await terminalAPI.StartTerminalInteractiveSized(id, epoch, !options.reconnecting, initSize.rows, initSize.cols);
      if (startResult && startResult.connected === false) {
        const failure = String(startResult.error || "SSH 连接失败");
        const warning = String(startResult.warning || "").trim();
        throw new Error(warning ? `${failure}\n${warning}` : failure);
      }
    } else if (typeof terminalAPI?.StartTerminalInteractive === "function") {
      startResult = await terminalAPI.StartTerminalInteractive(id, epoch, !options.reconnecting);
      if (startResult && startResult.connected === false) {
        const failure = String(startResult.error || "SSH 连接失败");
        const warning = String(startResult.warning || "").trim();
        throw new Error(warning ? `${failure}\n${warning}` : failure);
      }
    } else {
      await terminalAPI.StartTerminal(id, epoch);
    }
    if (session.connectionEpoch !== epoch || state.sessions[id] !== session) return { status: "cancelled" };
    const passwordSaved = Boolean(startResult?.passwordSaved);
    if (!host.hostKey || passwordSaved) {
      try {
        replaceSettings(await api().GetSettings());
      } catch {}
    }
    const resultMessage = String(startResult?.message || startResult?.warning || "").trim();
    if (resultMessage) toast(resultMessage, Boolean(startResult?.warning));
    else if (passwordSaved) toast("新密码已验证并永久保存");
    session.connected = true;
    session.autoReconnect = true;
    session.reconnectAttempt = 0;
    cancelSessionReconnect(session);
    hideTerminalReconnectBanner();
    writeTerminalOutput(id, `[连接] 会话已建立，连接成功。\r\n`);
    session.monitorDueAt = 0;
    session.processDueAt = 0;
    session.latencyDueAt = 0;
    session.diskDueAt = 0;
    session.fileDueAt = 0;
    updateSessionTabState(id);
    refreshSSHSessionLogStatus(id);
    if (state.selected === id) {
      state.connected = true;
      $("#onlineDot").className = "online";
      $("#sideAddress").textContent = host.address;
      $("#latencyTarget").textContent = sessionLatencyTargetText(host);
      $("#copyHostIP").classList.remove("hidden");
      $("#sideReconnect").classList.remove("hidden");
      $("#processUpdated").textContent = "正在采样…";
      $("#networkState").textContent = "正在采样…";
      $("#diskUpdated").textContent = "正在采样…";
      $("#uptime").textContent = "正在采样…";
      renderProcesses(session);
      renderDisks(session);
      fitTerminal(id, true);
      updateSessionFiles(id, session.path || "/", true, true);
    }
    startMonitor();
    syncActiveMonitorStream();
    runMonitorScheduler();
    return { status: "connected" };
  } catch (e) {
    if (session.connectionEpoch !== epoch || state.sessions[id] !== session) return { status: "cancelled" };
    markSessionDisconnected(id);
    const failure = String(e?.message || e).replace(/^Error:\s*/i, "").trim() || "未知错误";
    if ((host.protocol || "ssh") === "rdp") {
      appendRDPLog(id, `RDP 连接失败: ${failure}`);
      if (state.selected === id) renderRDPDeck(id);
    } else {
      writeTerminalOutput(id, `[连接] 连接失败：${failure}\r\n`);
      if (/timeout|connection refused|网络|timed out/i.test(failure)) {
        writeTerminalOutput(id, "[诊断建议] 1. 请检查服务器防火墙与端口 22 放行状态\r\n[诊断建议] 2. 若本地开启了全局代理或 TUN 模式，代理节点可能阻断了 22 端口，建议在主机设置中配置 SOCKS5 代理\r\n");
      }
    }
    return { status: "failed", error: failure };
  } finally {
    if (session.connectionEpoch === epoch) session.connecting = false;
    if (renderHostList) renderHosts();
    if (renderTabs) renderSessionTabs();
  }
}
async function connect() {
  if (!state.selected) return toast("请先选择主机", true);
  const result = await connectHostById(state.selected);
  if (result?.status === "failed") toast(result.error || "连接失败", true);
  return result;
}
async function batchConnectSelected() {
  const hosts = selectedBatchHosts();
  if (hosts.length < 2) return toast("请至少勾选两台 SSH 主机", true);
  const button = $("#batchConnect");
  button.disabled = true;
  hosts.forEach((host) => sessionFor(host.id));
  const activeHost = hosts.find((host) => host.id === state.selected) || hosts[0];
  $("#hostDrawer").classList.add("hidden");
  if (state.selected !== activeHost.id) selectHost(activeHost.id);
  else renderSessionTabs();
  const alreadyConnected = hosts.filter((host) => state.sessions[host.id].connected).length;
  const busy = hosts.filter((host) => {
    const session = state.sessions[host.id];
    return !session.connected && (session.connecting || session.closing);
  }).length;
  const pending = hosts.filter((host) => {
    const session = state.sessions[host.id];
    return !session.connected && !session.connecting && !session.closing;
  });
  let completed = 0;
  let results = [];
  const connectOne = async (host) => {
    const result = await connectHostById(host.id, false, false);
    completed += 1;
    button.textContent = `连接中 ${completed}/${pending.length}`;
    return result;
  };
  try {
    button.textContent = pending.length ? `连接中 0/${pending.length}` : "检查连接状态";
    const interactive = pending.filter((host) => !host.hostKey || String(host.authType || "").endsWith("_mfa"));
    const unattended = pending.filter((host) => !interactive.includes(host));
    const concurrency = Math.min(state.batchConcurrency, unattended.length);
    results = [];
    let nextIndex = 0;
    await Promise.all(
      Array.from({ length: concurrency }, async () => {
        while (nextIndex < unattended.length) {
          const index = nextIndex++;
          results.push(await connectOne(unattended[index]));
        }
      }),
    );
    for (const host of interactive) results.push(await connectOne(host));
    renderSessionTabs();
    const connected = results.filter((result) => result.status === "connected").length;
    const failed = results.filter((result) => result.status === "failed").length;
    const cancelled = results.length - connected - failed;
    const summary = [`新连接 ${connected} 台`];
    if (alreadyConnected) summary.push(`已连接 ${alreadyConnected} 台`);
    if (busy + cancelled) summary.push(`处理中 ${busy + cancelled} 台`);
    if (failed) summary.push(`失败 ${failed} 台`);
    toast(`批量连接完成：${summary.join("，")}`, failed > 0);
  } finally {
    button.disabled = false;
    updateBatchConnectButton();
  }
}
async function closeSession() {
  const id = state.selected;
  const session = state.sessions[id];
  const host = state.settings?.hosts.find((item) => item.id === id);
  const rdpSessionId = session?.rdpSessionId || "";
  if (session?.closing) return toast("会话正在关闭，请稍候", true);
  const previous = session
    ? {
        autoReconnect: session.autoReconnect,
        connected: session.connected,
        connecting: session.connecting,
      }
    : null;
  if (session) {
    session.autoReconnect = false;
    cancelSessionReconnect(session);
    cancelSSHInteractionsForHost(id);
    session.closing = true;
  }
  let closed = false;
  try {
    if (rdpSessionId) {
      try {
        await api().CloseRDP(rdpSessionId);
      } catch (error) {
        if (!/不存在|已经退出/.test(String(error))) throw error;
      }
    } else if (id && String(host?.protocol || "ssh").toLowerCase() === "ssh") {
      await api().CloseTerminal(id);
    }
    closed = true;
    if (session && state.sessions[id] === session) {
      advanceConnectionEpoch(session, id);
      session.connected = false;
      session.connecting = false;
      disposeTerminal(id);
      delete state.sessions[id];
    }
    if (state.selected === id) {
      state.connected = false;
      stopMonitor();
      syncActiveMonitorStream();
    } else {
      stopMonitor();
      syncActiveMonitorStream();
      if (host?.temporary) await releaseTemporaryHost(id);
      renderSessionTabs();
      return toast(id ? "远程会话已关闭" : "会话已关闭");
    }
    state.selected = "";
    state.connected = false;
    state.path = "/";
    state.files = [];
    if (host?.temporary) await releaseTemporaryHost(id);
    const next = Object.keys(state.sessions)[0];
    if (next) return selectHost(next);
    $("#onlineDot").className = "";
    $("#sideAddress").textContent = "未连接";
    showSideIdentity();
    $("#copyHostIP").classList.add("hidden");
    $("#sideReconnect").classList.add("hidden");
    showTerminal("");
    resetMonitor();
    renderHosts();
    renderSessionTabs();
    toast(id ? "远程会话已关闭" : "会话已关闭");
  } finally {
    if (!closed && session && state.sessions[id] === session) {
      session.closing = false;
      session.autoReconnect = previous.autoReconnect;
      session.connected = previous.connected;
      session.connecting = previous.connecting;
      renderSessionTabs();
      if (session.autoReconnect && !session.connected && !session.connecting) {
        scheduleSessionReconnect(id);
      }
    }
  }
}

async function releaseTemporaryHost(id) {
  const host = state.settings?.hosts.find((item) => item.id === id);
  if (!host?.temporary) return;
  try {
    await api().ReleaseTemporaryHost(id);
  } finally {
    state.hostSelection.delete(id);
    state.settings.hosts = state.settings.hosts.filter((item) => item.id !== id);
  }
}

async function removeTemporarySession(id) {
  const host = state.settings?.hosts.find((item) => item.id === id);
  if (!host?.temporary) return;
  if (state.sessions[id]) discardSession(id);
  await releaseTemporaryHost(id);
  const next = Object.keys(state.sessions)[0];
  if (!state.selected && next) selectHost(next);
  renderHosts();
  renderSessionTabs();
}
async function runCommand(command, synchronize = state.syncCommands) {
  if (!state.selected) return toast("请先选择主机", true);
  if (!state.connected) return toast("请先连接 SSH", true);
  const result = await enqueueTerminalInput(state.selected, `${command}\r`, synchronize);
  if (synchronize && result.sent > 1) {
    toast(`命令已同步发送到 ${result.sent} 台主机`);
  }
}
const FILE_INTERVAL = 30000;
const FILE_CONCURRENCY = 4;
const MONITOR_CONCURRENCY_MIN = 8;
const MONITOR_CONCURRENCY_MAX = 48;
const MONITOR_SLOW_CONCURRENCY_MIN = 6;
const MONITOR_SLOW_CONCURRENCY_MAX = 32;
const MONITOR_PER_HOST_MAX = 4;
const MONITOR_MIN_REST = 100;
const LATENCY_HISTORY_LIMIT = 24;
const MONITOR_INTERVALS = {
  fast: 800,
  process: 1000,
  disk: 3000,
  latency: 1000,
};
const MONITOR_BACKGROUND_INTERVALS = {
  fast: 2500,
  process: 4000,
  disk: 15000,
  latency: 5000,
};
const MONITOR_TICK = 100;
const MONITOR_TASKS = {
  fast: {
    loading: "monitorLoading",
    request: "monitorRequest",
    due: "monitorDueAt",
    updated: "monitorUpdatedAt",
    error: "monitorError",
    method: "GetMonitorFast",
    timeout: 6000,
  },
  process: {
    loading: "processLoading",
    request: "processRequest",
    due: "processDueAt",
    updated: "processUpdatedAt",
    error: "processError",
    method: "GetMonitorProcesses",
    timeout: 7000,
  },
  disk: {
    loading: "diskLoading",
    request: "diskRequest",
    due: "diskDueAt",
    updated: "diskUpdatedAt",
    error: "diskError",
    method: "GetMonitorDisks",
    timeout: 8000,
  },
  latency: {
    loading: "latencyLoading",
    request: "latencyRequest",
    due: "latencyDueAt",
    error: "latencyError",
    timeout: 6500,
  },
};
const MONITOR_KIND_ORDER = ["fast", "process", "disk", "latency"];
function monitorJitter(id, kind, max) {
  let hash = 0;
  for (const char of `${id}:${kind}`) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return max > 0 ? hash % max : 0;
}
function monitorConcurrencyLimit() {
  return Math.min(
    MONITOR_CONCURRENCY_MAX,
    Math.max(MONITOR_CONCURRENCY_MIN, Math.ceil(connectedSSHIds().length * 0.9)),
  );
}
function monitorSlowConcurrencyLimit() {
  return Math.min(
    MONITOR_SLOW_CONCURRENCY_MAX,
    Math.max(MONITOR_SLOW_CONCURRENCY_MIN, Math.ceil(connectedSSHIds().length * 0.55)),
  );
}
function monitorDelay(kind, id, failed = false) {
  const selectedHost = id === state.selected;
  let delay = selectedHost
    ? MONITOR_INTERVALS[kind]
    : (MONITOR_BACKGROUND_INTERVALS[kind] || MONITOR_INTERVALS[kind]);
  if (failed) delay = Math.max(5000, delay * 2);
  return delay + (
    selectedHost
      ? 0
      : monitorJitter(id, kind, Math.min(400, Math.round(delay / 5)))
  );
}
function desiredMonitorStreamTarget() {
  if (!monitorPageVisible() || !state.selected) return null;
  const host = selected();
  const session = state.sessions[state.selected];
  if (
    !host ||
    !session?.connected ||
    session.connecting ||
    session.closing ||
    String(host.protocol || "ssh").toLowerCase() !== "ssh"
  ) {
    return null;
  }
  return {
    id: state.selected,
    epoch: session.connectionEpoch,
    key: `${state.selected}:${session.connectionEpoch}`,
    session,
  };
}
function syncActiveMonitorStream(forceRestart = false) {
  const target = desiredMonitorStreamTarget();
  if (!target) {
    if (state.activeMonitorStreamKey) {
      state.activeMonitorStreamKey = "";
      api()?.StopMonitorStream?.("")?.catch(() => {});
    }
    return;
  }
  if (!forceRestart && state.activeMonitorStreamKey === target.key) return;
  if (typeof api()?.StartMonitorStream !== "function") return;
  state.activeMonitorStreamKey = target.key;
  target.session.monitorStreamStartedAt = Date.now();
  target.session.monitorStreamActive = false;
  Promise.resolve(api().StartMonitorStream(target.id, target.epoch)).catch(() => {
    const current = state.sessions[target.id];
    if (current === target.session && current.connectionEpoch === target.epoch) {
      current.monitorStreamStartedAt = 0;
      current.monitorStreamActive = false;
    }
    if (state.activeMonitorStreamKey === target.key) {
      state.activeMonitorStreamKey = "";
    }
    runMonitorScheduler();
  });
}
function isSessionStreamHandling(session, now = Date.now()) {
  if (!session?.connected) return false;
  if (session.monitorStreamActive && now - session.monitorStreamLastAt < 2500) return true;
  if (session.monitorStreamStartedAt && now - session.monitorStreamStartedAt < 2200) return true;
  return false;
}
function applyMonitorStreamFrame(id, session, output) {
  const raw = String(output || "");
  if (!raw) return;
  const now = Date.now();
  const visibleSelected = state.selected === id && monitorPageVisible();
  let updated = false;

  if (raw.includes("__CPU__")) {
    try {
      validateFastMonitorOutput(raw);
      session.monitorOutput = raw;
      session.monitorUpdatedAt = now;
      session.monitorError = "";
      updateSessionCPU(session, raw);
      updateSessionNetwork(session, raw);
      updated = true;
      if (visibleSelected) {
        renderFastMonitor(session);
        renderNetwork(session);
      }
    } catch (error) {
      session.monitorError = String(error);
      invalidateFailedMonitorData(session, "fast");
      if (visibleSelected) {
        renderFastMonitor(session);
        renderNetwork(session);
      }
    }
  }

  if (raw.includes("__PROCESS__")) {
    const processes = parseProcesses(raw);
    if (processes.length) {
      session.processOutput = raw;
      session.processes = processes;
      session.processUpdatedAt = now;
      session.processError = "";
      updated = true;
    } else if (!session.processUpdatedAt) {
      session.processError = "未获取到进程数据";
    }
    if (visibleSelected) renderProcesses(session);
  }

  if (raw.includes("__DISK__")) {
    const disks = parseDisks(raw);
    if (disks.length) {
      session.diskOutput = raw;
      session.disks = disks;
      session.diskUpdatedAt = now;
      session.diskError = "";
      updated = true;
    } else if (!session.diskUpdatedAt) {
      session.diskError = "未获取到磁盘数据";
    }
    if (visibleSelected) renderDisks(session);
  }

  if (updated) {
    session.monitorManualRefresh = false;
    session.monitorStreamActive = true;
    session.monitorStreamLastAt = now;
    if (visibleSelected) renderCurrentMonitorFreshness();
  }
}
function startMonitor() {
  if (!state.monitorTimer)
    state.monitorTimer = window.setInterval(runMonitorScheduler, MONITOR_TICK);
  if (!state.fileTimer)
    state.fileTimer = window.setInterval(runFileScheduler, 1000);
  runMonitorScheduler();
  runFileScheduler();
}
function stopMonitor() {
  if (connectedSSHIds().length) return;
  if (state.monitorTimer) window.clearInterval(state.monitorTimer);
  if (state.fileTimer) window.clearInterval(state.fileTimer);
  state.monitorTimer = 0;
  state.fileTimer = 0;
}
function monitorPageVisible() {
  return !document.hidden &&
    !app.classList.contains("node-mode-active") &&
    $("#hostDrawer").classList.contains("hidden") &&
    $("#hostEditor").classList.contains("hidden") &&
    $("#settingsPage").classList.contains("hidden") &&
    $("#aboutPage").classList.contains("hidden");
}
function rescheduleSessionMonitor(session, id) {
  for (const kind of MONITOR_KIND_ORDER) {
    const config = MONITOR_TASKS[kind],
      updatedAt = kind === "latency" ? session.latency?.updatedAt : session[config.updated],
      nextDue = updatedAt
        ? updatedAt + monitorDelay(kind, id, Boolean(session[config.error]))
        : Number(session[config.due]),
      currentDue = Number(session[config.due]);
    if (!Number.isFinite(currentDue)) {
      session[config.due] = 0;
    } else if (!session[config.loading] && !session[config.error] && currentDue > nextDue) {
      session[config.due] = nextDue;
    }
  }
}
function refreshVisibleMonitor() {
  if (document.hidden) {
    syncActiveMonitorStream();
    return;
  }
  const ids = connectedSSHIds();
  if (!ids.length) {
    syncActiveMonitorStream();
    return stopMonitor();
  }
  ids.forEach((id) => rescheduleSessionMonitor(state.sessions[id], id));
  if (state.connected && state.selected && monitorPageVisible()) {
    showTerminal(state.selected);
    restoreSessionView(state.sessions[state.selected]);
  }
  syncActiveMonitorStream();
  startMonitor();
}
document.addEventListener("visibilitychange", refreshVisibleMonitor);
window.addEventListener("focus", refreshVisibleMonitor);
function renderWorkspaceLatency(stats) {
  $("#latencyCurrent").firstChild.textContent = Number.isFinite(stats.current) ? `${stats.current}ms` : "—";
  $("#packetLoss").firstChild.textContent = `${stats.loss}%`;
  $("#latencyMin").firstChild.textContent = Number.isFinite(stats.min) ? `${stats.min}ms` : "—";
  $("#latencyMax").firstChild.textContent = Number.isFinite(stats.max) ? `${stats.max}ms` : "—";
  const host = selected(),
    session = state.sessions[state.selected],
    target = sessionLatencyTargetText(host),
    time = stats.updatedAt
      ? new Date(stats.updatedAt).toLocaleTimeString("zh-CN", { hour12: false })
      : "";
  $("#latencyTarget").textContent = stats.updatedAt
      ? session?.latencyError
      ? `探测超时 · ${time}`
      : `${target} · ${time}`
    : target;
  const history = stats.history || [],
    peak = Math.max(1, ...history.filter(Number.isFinite));
  $("#latencyChart").innerHTML = history
    .map((value) => Number.isFinite(value)
      ? `<i title="${value}ms" style="height:${Math.max(12, Math.round((value / peak) * 100))}%"></i>`
      : '<i class="failed" title="探测失败"></i>')
    .join("");
}
function recordSessionLatencySample(id, session, latency, errorText = "") {
  if (!session) return null;
  const numeric = Number(latency);
  const valid = !errorText && Number.isFinite(numeric) && numeric >= 0;
  session.latencyHistory.push(valid ? numeric : null);
  session.latencyHistory = session.latencyHistory.slice(-LATENCY_HISTORY_LIMIT);
  const values = session.latencyHistory.filter(Number.isFinite);
  const stats = {
    current: valid ? numeric : null,
    loss: Math.round(((session.latencyHistory.length - values.length) * 100) / session.latencyHistory.length),
    min: values.length ? Math.min(...values) : null,
    max: values.length ? Math.max(...values) : null,
    updatedAt: Date.now(),
    history: session.latencyHistory.slice(),
  };
  session.latency = stats;
  session.latencyError = valid ? "" : String(errorText || "延迟探测超时");
  if (state.selected === id && monitorPageVisible()) renderWorkspaceLatency(stats);
  return stats;
}
async function updateSessionLatency(id, force = false) {
  const session = state.sessions[id];
  if (!session?.connected || session.connecting || session.closing || session.latencyLoading) return false;
  if (!force && session.latencyDueAt > Date.now()) return false;
  const startedAt = Date.now(),
    nextDueAt = startedAt + monitorDelay("latency", id),
    epoch = session.connectionEpoch,
    request = ++session.latencyRequest;
  session.latencyLoading = true;
  session.latencyDueAt = nextDueAt;
  let timeout = 0;
  try {
    const latency = await Promise.race([
      api().CheckConnectedHostLatency(id, epoch),
      new Promise((_, reject) => {
        timeout = window.setTimeout(() => reject(new Error("延迟探测超时")), 10000);
      }),
    ]);
    if (state.sessions[id] !== session || !session.connected || session.connectionEpoch !== epoch || request !== session.latencyRequest) return;
    recordSessionLatencySample(id, session, latency, "");
    return true;
  } catch (error) {
    if (state.sessions[id] !== session || !session.connected || session.connectionEpoch !== epoch || request !== session.latencyRequest) return;
    recordSessionLatencySample(id, session, null, String(error));
    return false;
  } finally {
    if (timeout) window.clearTimeout(timeout);
    if (state.sessions[id] === session && request === session.latencyRequest) {
      session.latencyLoading = false;
      const finishedAt = Date.now();
      session.latencyDueAt = session.latencyError
        ? finishedAt + monitorDelay("latency", id, true)
        : Math.max(nextDueAt, finishedAt + MONITOR_MIN_REST);
    }
    const current = state.sessions[id];
    if (current?.connected && (current !== session || current.connectionEpoch !== epoch)) {
      current.latencyDueAt = 0;
      runMonitorScheduler();
    }
  }
}
function invalidateFailedMonitorData(session, kind) {
  if (kind === "fast") {
    session.monitorOutput = "";
    session.monitorUpdatedAt = 0;
    session.cpuSample = null;
    session.cpuPercent = null;
    session.cpuError = "";
    session.networkSample = null;
    session.networkUp = 0;
    session.networkDown = 0;
    session.networkHistory = [];
    session.networkUpdatedAt = 0;
    session.networkError = "";
  } else if (kind === "disk") {
    session.diskOutput = "";
    session.disks = [];
    session.diskUpdatedAt = 0;
  }
}
async function updateSessionMonitor(id, kind, notify = false, force = false) {
  const config = MONITOR_TASKS[kind];
  if (!config || kind === "latency") return false;
  const session = state.sessions[id];
  if (!session?.connected) return false;
  if (session[config.loading]) {
    if (force) {
      session.monitorForced.add(kind);
      if (notify) session.monitorNotify.add(kind);
    }
    return false;
  }
  const now = Date.now();
  if (!force && session[config.due] > now) return false;
  if (session.monitorNotify.has(kind)) {
    notify = true;
    session.monitorNotify.delete(kind);
  }
  const nextDueAt = now + monitorDelay(kind, id),
    epoch = session.connectionEpoch,
    request = ++session[config.request];
  session[config.loading] = true;
  session[config.due] = nextDueAt;
  try {
    const method = api()?.[config.method];
    if (typeof method !== "function") throw new Error(`监控接口不可用：${config.method}`);
    const r = await method(id, epoch);
    if (
      state.sessions[id] !== session ||
      !session.connected ||
      session.connectionEpoch !== epoch ||
      request !== session[config.request]
    )
      return false;
    const output = String(r?.output || ""),
      exitCode = Number(r?.exitCode);
    if (Number.isFinite(exitCode) && exitCode !== 0)
      throw new Error(`${kind === "process" ? "进程" : kind === "disk" ? "磁盘" : "系统"}采集失败（退出码 ${exitCode}）`);
    let parsed = null;
    if (kind === "fast") validateFastMonitorOutput(output);
    else if (kind === "process") {
      parsed = parseProcesses(output);
      if (!parsed.length) throw new Error("未获取到进程数据");
    } else if (kind === "disk") {
      parsed = parseDisks(output);
      if (!parsed.length) throw new Error("未获取到磁盘数据");
    }
    const updatedAt = Date.now();
    session[config.updated] = updatedAt;
    session[config.error] = "";
    session.monitorManualRefresh = false;
    if (kind === "fast") {
      session.monitorOutput = output;
      updateSessionCPU(session, output);
      updateSessionNetwork(session, output);
      if (!session.networkUpdatedAt) {
        // 首个网络基准样本刚记录，150ms 后立即执行第 2 次采样以即时输出速率与波形
        session[config.due] = updatedAt + 150;
      } else {
        session[config.due] = Math.max(nextDueAt, updatedAt + MONITOR_MIN_REST);
      }
    } else {
      session[config.due] = Math.max(nextDueAt, updatedAt + MONITOR_MIN_REST);
    }
    if (kind === "process") {
      session.processOutput = output;
      session.processes = parsed;
    } else if (kind === "disk") {
      session.diskOutput = output;
      session.disks = parsed;
    }
    if (state.selected === id && monitorPageVisible()) {
      if (kind === "fast") {
        renderFastMonitor(session);
        renderNetwork(session);
      } else if (kind === "process") {
        renderProcesses(session);
      } else if (kind === "disk") {
        renderDisks(session);
      }
    }
    return true;
  } catch (e) {
    if (
      state.sessions[id] === session &&
      session.connected &&
      session.connectionEpoch === epoch &&
      request === session[config.request]
    ) {
      session[config.error] = String(e);
      session.monitorManualRefresh = false;
      session[config.due] = Date.now() + monitorDelay(kind, id, true);
      if (kind === "process") session.processes = [];
      invalidateFailedMonitorData(session, kind);
    }
    if (state.selected === id && request === session[config.request]) {
      if (kind === "fast") {
        renderFastMonitor(session);
        renderNetwork(session);
      }
      else if (kind === "process") renderProcesses(session);
      else if (kind === "disk") renderDisks(session);
    }
    if (notify && state.selected === id && request === session[config.request]) {
      toast(String(e), true);
    }
    return false;
  } finally {
    if (state.sessions[id] === session && request === session[config.request]) {
      session[config.loading] = false;
      if (session.monitorForced.has(kind) && session.connected && session.connectionEpoch === epoch) {
        session.monitorForced.delete(kind);
        session[config.due] = 0;
      }
    }
  }
}
function sessionMonitorActiveCount(session) {
  return ["fast", "process", "disk"].reduce(
    (count, kind) => count + (session[MONITOR_TASKS[kind].loading] ? 1 : 0),
    0,
  );
}
function monitorKindCanStart(session, kind) {
  const config = MONITOR_TASKS[kind];
  if (!session?.connected || !config || session[config.loading]) return false;
  if (kind === "latency") return true;
  return sessionMonitorActiveCount(session) < MONITOR_PER_HOST_MAX;
}
function nextSessionMonitorJobs(id, now) {
  const session = state.sessions[id];
  if (!session?.connected) return [];
  if (isSessionStreamHandling(session, now) && !session.monitorForced.size && !session.monitorManualRefresh) {
    return [];
  }
  return MONITOR_KIND_ORDER
    .map((kind, order) => ({ id, kind, order, dueAt: session[MONITOR_TASKS[kind].due] || 0 }))
    .filter((job) =>
      monitorKindCanStart(session, job.kind) &&
      job.dueAt <= now,
    )
    .sort((a, b) => a.dueAt - b.dueAt || a.order - b.order);
}
function nextSessionMonitorJob(id, now, fastOnly) {
  const session = state.sessions[id];
  if (!session?.connected) return null;
  if (isSessionStreamHandling(session, now) && !session.monitorForced.size && !session.monitorManualRefresh) {
    return null;
  }
  const kinds = fastOnly ? ["fast"] : MONITOR_KIND_ORDER.slice(1);
  return kinds
    .map((kind, order) => ({ id, kind, order, dueAt: session[MONITOR_TASKS[kind].due] || 0 }))
    .filter((job) =>
      monitorKindCanStart(session, job.kind) &&
      job.dueAt <= now,
    )
    .sort((a, b) => a.dueAt - b.dueAt || a.order - b.order)[0] || null;
}
function renderMonitorTaskFailure(id, kind) {
  if (state.selected !== id || !monitorPageVisible()) return;
  const session = state.sessions[id];
  if (kind === "fast") {
    renderFastMonitor(session);
    renderNetwork(session);
  }
  else if (kind === "process") renderProcesses(session);
  else if (kind === "disk") renderDisks(session);
  else if (kind === "latency" && session?.latency) renderWorkspaceLatency(session.latency);
}
function dispatchMonitorJob({ id, kind }) {
  const config = MONITOR_TASKS[kind],
    session = state.sessions[id];
  if (!monitorKindCanStart(session, kind)) return;
  state.monitorActive++;
  if (kind !== "fast") state.monitorSlowActive++;
  const epoch = session.connectionEpoch,
    task = kind === "latency" ? updateSessionLatency(id) : updateSessionMonitor(id, kind),
    request = session[config.request];
  let released = false,
    timeout = 0;
  const release = (timedOut = false) => {
    if (released) return;
    released = true;
    if (timeout) window.clearTimeout(timeout);
    if (
      timedOut &&
      state.sessions[id] === session &&
      session.connectionEpoch === epoch &&
      session[config.request] === request &&
      session[config.loading]
    ) {
      session[config.request]++;
      session[config.loading] = false;
      session[config.error] = "监控请求超时";
      if (kind === "process") session.processes = [];
      invalidateFailedMonitorData(session, kind);
      const retryForced = session.monitorForced.delete(kind);
      session[config.due] = retryForced
        ? 0
        : Date.now() + monitorDelay(kind, id, true);
      renderMonitorTaskFailure(id, kind);
    }
    state.monitorActive = Math.max(0, state.monitorActive - 1);
    if (kind !== "fast") state.monitorSlowActive = Math.max(0, state.monitorSlowActive - 1);
    runMonitorScheduler();
  };
  timeout = window.setTimeout(() => release(true), config.timeout);
  Promise.resolve(task).catch(() => false).finally(() => release(false));
}
function runMonitorScheduler() {
  const now = Date.now();
  syncActiveMonitorStream();
  if (now - state.monitorStatusRenderedAt >= 1000) {
    state.monitorStatusRenderedAt = now;
    renderCurrentMonitorFreshness();
  }
  const ids = connectedSSHIds();
  if (!ids.length) return stopMonitor();
  const capacity = monitorConcurrencyLimit() - state.monitorActive;
  if (capacity <= 0) return;
  const sortJobs = (jobs) => jobs.sort((a, b) => {
      const aOverdue = (now - a.dueAt) / MONITOR_INTERVALS[a.kind],
        bOverdue = (now - b.dueAt) / MONITOR_INTERVALS[b.kind];
      if (aOverdue !== bOverdue) return bOverdue - aOverdue;
      if (a.id === state.selected) return -1;
      if (b.id === state.selected) return 1;
      return a.order - b.order;
    });
  const jobs = [];
  const scheduledKeys = new Set();
  const scheduledHosts = new Set();
  if (state.selected && ids.includes(state.selected)) {
    for (const job of nextSessionMonitorJobs(state.selected, now)) {
      if (jobs.length >= capacity) break;
      jobs.push(job);
      scheduledKeys.add(`${job.id}:${job.kind}`);
      scheduledHosts.add(job.id);
    }
  }
  const otherIds = ids.filter((id) => id !== state.selected);
  const fastCandidates = sortJobs(otherIds.map((id) => nextSessionMonitorJob(id, now, true)).filter(Boolean));
  let slowCapacity = Math.max(0, monitorSlowConcurrencyLimit() - state.monitorSlowActive);
  const slowCandidates = sortJobs(otherIds.map((id) => nextSessionMonitorJob(id, now, false)).filter(Boolean));
  let slowIndex = 0;
  const remaining = Math.max(0, capacity - jobs.length);
  const reservedSlow = Math.min(
    Math.max(2, Math.ceil(remaining * 0.4)),
    slowCapacity,
    slowCandidates.length,
    remaining,
  );
  for (; slowIndex < reservedSlow; slowIndex++) {
    const job = slowCandidates[slowIndex];
    const key = `${job.id}:${job.kind}`;
    if (scheduledKeys.has(key)) continue;
    jobs.push(job);
    scheduledKeys.add(key);
    scheduledHosts.add(job.id);
    slowCapacity--;
  }
  for (const job of fastCandidates) {
    if (jobs.length >= capacity) break;
    const key = `${job.id}:${job.kind}`;
    if (scheduledKeys.has(key) || scheduledHosts.has(job.id)) continue;
    jobs.push(job);
    scheduledKeys.add(key);
    scheduledHosts.add(job.id);
  }
  for (; slowIndex < slowCandidates.length; slowIndex++) {
    const job = slowCandidates[slowIndex];
    if (jobs.length >= capacity || slowCapacity <= 0) break;
    const key = `${job.id}:${job.kind}`;
    if (scheduledKeys.has(key) || scheduledHosts.has(job.id)) continue;
    jobs.push(job);
    scheduledKeys.add(key);
    scheduledHosts.add(job.id);
    slowCapacity--;
  }
  jobs.forEach(dispatchMonitorJob);
}
function loadMonitor() {
  if (!state.selected || !state.connected) return;
  const id = state.selected;
  const session = state.sessions[id];
  if (!session) return;
  session.monitorManualRefresh = true;
  for (const kind of MONITOR_KIND_ORDER) {
    const config = MONITOR_TASKS[kind];
    session[config.due] = 0;
    if (session[config.loading]) session.monitorForced.add(kind);
  }
  $("#networkState").textContent = "更新中…";
  $("#processUpdated").textContent = "更新中…";
  $("#diskUpdated").textContent = "更新中…";
  if (monitorPageVisible() && typeof api()?.StartMonitorStream === "function") {
    syncActiveMonitorStream(true);
  } else {
    updateSessionLatency(id, true);
    for (const kind of ["fast", "process", "disk"]) {
      updateSessionMonitor(id, kind, kind === "disk", true);
    }
  }
  runMonitorScheduler();
}
const formatSize = (n) =>
  n < 1024
    ? `${n} B`
    : n < 1048576
      ? `${(n / 1024).toFixed(1)} KB`
      : `${(n / 1048576).toFixed(1)} MB`;
const formatRate = (n) =>
  !Number.isFinite(n) || n < 0
    ? "—"
    : n < 1024
      ? `${Math.round(n)} B/s`
      : n < 1048576
        ? `${(n / 1024).toFixed(1)} KB/s`
        : `${(n / 1048576).toFixed(1)} MB/s`;
async function updateSessionFiles(id, path, notify = false, force = false) {
  const session = state.sessions[id];
  if (!session?.connected) return false;
  if (!path) {
    if (notify && state.selected === id) toast("服务器目录尚未同步", true);
    return false;
  }
  if (session.fileLoading) {
    if (force) {
      session.pendingFilePath = path;
      session.pendingFileNotify ||= notify;
    }
    return false;
  }
  const now = Date.now();
  if (!force && session.fileDueAt > now) return false;
  const epoch = session.connectionEpoch,
    request = ++session.fileRequest;
  session.fileLoading = true;
  session.fileDueAt = Number.POSITIVE_INFINITY;
  try {
    const result = await api().ListFiles(id, epoch, path);
    if (
      state.sessions[id] !== session ||
      !session.connected ||
      session.connectionEpoch !== epoch ||
      request !== session.fileRequest
    )
      return false;
    if (session.pendingFilePath && session.pendingFilePath !== path) return false;
    const files = Array.isArray(result) ? result : [];
    session.files = files;
    session.path = path;
    session.fileView = buildFileView(path, files);
    session.fileUpdatedAt = Date.now();
    session.fileDueAt = session.fileUpdatedAt + FILE_INTERVAL;
    session.fileError = "";
    const shouldRender =
      state.selected === id &&
      monitorPageVisible() &&
      (notify || document.activeElement !== $("#path"));
    if (shouldRender) {
      state.files = files;
      state.path = path;
      renderFileView(session.fileView);
    }
    return true;
  } catch (e) {
    if (
      state.sessions[id] === session &&
      session.connected &&
      session.connectionEpoch === epoch &&
      request === session.fileRequest
    ) {
      session.fileError = String(e);
      session.fileDueAt = Date.now() + (session.fileError.includes("超过") ? 60000 : FILE_INTERVAL);
      if (state.selected === id && monitorPageVisible()) {
        $("#dirHint").textContent = "SFTP 暂不可用，SSH 终端仍可正常使用";
        $("#treeItems").innerHTML = '<span class="placeholder">点击“刷新”重试文件通道</span>';
        $("#fileRows").innerHTML = `<tr><td colspan="6" class="placeholder">${esc(session.fileError.replace(/^Error:\s*/i, ""))}</td></tr>`;
      }
    }
    if (notify && state.selected === id && request === session.fileRequest) {
      toast(String(e), true);
    }
    return false;
  } finally {
    if (state.sessions[id] === session && request === session.fileRequest) {
      session.fileLoading = false;
      if (session.pendingFilePath && session.connected && session.connectionEpoch === epoch) {
        const pendingPath = session.pendingFilePath,
          pendingNotify = session.pendingFileNotify;
        session.pendingFilePath = "";
        session.pendingFileNotify = false;
        window.setTimeout(
          () => updateSessionFiles(id, pendingPath, pendingNotify, true),
          0,
        );
      }
    }
  }
}
function runFileScheduler() {
  const capacity = FILE_CONCURRENCY - state.fileActive;
  if (capacity <= 0) return;
  const now = Date.now();
  const ids = connectedSSHIds()
    .filter((item) => !state.sessions[item].fileLoading && (state.sessions[item].fileDueAt || 0) <= now)
    .sort((a, b) => {
      if (a === state.selected) return -1;
      if (b === state.selected) return 1;
      return (state.sessions[a].fileDueAt || 0) - (state.sessions[b].fileDueAt || 0);
    })
    .slice(0, capacity);
  for (const id of ids) {
    state.fileActive++;
    const slotSession = state.sessions[id],
      slotEpoch = slotSession.connectionEpoch,
      task = updateSessionFiles(id, slotSession.path || "/"),
      slotRequest = slotSession.fileRequest;
    let released = false,
      timeout = 0;
    const release = (timedOut = false) => {
      if (released) return;
      released = true;
      if (timeout) window.clearTimeout(timeout);
      if (
        timedOut &&
        state.sessions[id] === slotSession &&
        slotSession.connectionEpoch === slotEpoch &&
        slotSession.fileRequest === slotRequest &&
        slotSession.fileLoading
      ) {
        const notify = slotSession.pendingFileNotify;
        slotSession.fileRequest++;
        slotSession.fileLoading = false;
        slotSession.pendingFilePath = "";
        slotSession.pendingFileNotify = false;
        slotSession.fileError = "读取远程目录超时";
        slotSession.fileDueAt = Date.now() + 60000;
        if (notify && state.selected === id) toast("读取远程目录超时，请重试", true);
      }
      state.fileActive = Math.max(0, state.fileActive - 1);
      runFileScheduler();
    };
    timeout = window.setTimeout(() => release(true), 12000);
    task.catch(() => false).finally(() => release(false));
  }
}
async function loadFiles(path = $("#path").value) {
  if (!state.selected) return;
  if (!state.connected) return toast("请先连接 SSH", true);
  path = String(path || "").trim();
  if (!path.startsWith("/") || path.includes("\0"))
    return toast("请输入以 / 开头的服务器绝对路径", true);
  await updateSessionFiles(state.selected, path, true, true);
}
async function uploadToTarget(target, button = null) {
  if (!requireFileTarget(target)) return false;
  const session = state.sessions[target.hostId];
  if (session.fileTransferActive) {
    toast("当前主机已有文件传输，请等待完成", true);
    return false;
  }
  const oldText = button?.textContent;
  session.fileTransferActive = true;
  if (button) {
    button.disabled = true;
    button.textContent = "上传中…";
  }
  try {
    const uploaded = await api().UploadFile(
      target.hostId,
      target.connectionEpoch,
      target.path,
    );
    if (!uploaded) return false;
    if (!fileTargetActive(target)) {
      toast("文件已上传，但 SSH 会话已变化", true);
      return true;
    }
    toast("上传完成");
    await updateSessionFiles(target.hostId, target.path, false, true);
    return true;
  } catch (error) {
    toast(String(error), true);
    return false;
  } finally {
    if (state.sessions[target.hostId] === session && session.connectionEpoch === target.connectionEpoch)
      session.fileTransferActive = false;
    if (button) {
      button.disabled = false;
      button.textContent = oldText;
    }
  }
}
async function downloadFromTarget(target, file, button = null) {
  if (!file || file.isDir || !requireFileTarget(target)) return false;
  const session = state.sessions[target.hostId];
  if (session.fileTransferActive) {
    toast("当前主机已有文件传输，请等待完成", true);
    return false;
  }
  const oldText = button?.textContent;
  session.fileTransferActive = true;
  if (button) {
    button.disabled = true;
    button.textContent = "下载中…";
  }
  try {
    const downloaded = await api().DownloadFile(
      target.hostId,
      target.connectionEpoch,
      file.path,
      file.name,
    );
    if (!downloaded) return false;
    toast("下载完成");
    return true;
  } catch (error) {
    toast(String(error), true);
    return false;
  } finally {
    if (state.sessions[target.hostId] === session && session.connectionEpoch === target.connectionEpoch)
      session.fileTransferActive = false;
    if (button) {
      button.disabled = false;
      button.textContent = oldText;
    }
  }
}
async function openFile(f, target = fileTarget()) {
  if (!f || f.isDir || !requireFileTarget(target)) return;
  const hostId = target.hostId;
  const request = ++state.fileOpenRequest;
  if (f.size > 5 * 1024 * 1024)
    return toast("文件超过 5 MB，请下载后编辑", true);
  try {
    const content = await api().ReadRemoteFile(
      hostId,
      target.connectionEpoch,
      f.path,
    );
    if (
      request !== state.fileOpenRequest ||
      !fileTargetActive(target) ||
      state.selected !== hostId ||
      state.path !== target.path
    )
      return;
    $("#editor").value = content;
    state.editing = {
      ...f,
      hostId,
      connectionEpoch: target.connectionEpoch,
      directoryPath: target.path,
      version: 0,
      saving: false,
      stale: false,
    };
    $("#editorTitle").textContent = `远程编辑 · ${f.path}`;
    $("#editorState").textContent = "已同步";
    $("#editorState").className = "";
    $("#saveEdit").disabled = false;
    $("#editorModal").classList.remove("hidden");
    $("#editor").focus();
  } catch (e) {
    if (
      request === state.fileOpenRequest &&
      fileTargetActive(target) &&
      state.selected === hostId &&
      state.path === target.path
    )
      toast(String(e), true);
  }
}
function setupCredentialUI() {
  $("#refreshAllVps").insertAdjacentHTML(
    "afterend",
    '<button id="credentialBtn" type="button">凭证库</button>',
  );
  document.body.insertAdjacentHTML(
    "beforeend",
    '<div id="credentialLibrary" class="modal hidden"><div class="dialog credential-library"><div class="credential-head"><div><h3>凭证库</h3><small id="credentialCount" class="muted"></small></div><button id="closeCredentials" type="button">关闭</button></div><div class="credential-toolbar"><input id="credentialSearch" type="search" placeholder="搜索凭证名称、备注或密钥指纹…"><select id="credentialTypeFilter"><option value="all">全部类型</option><option value="password">仅密码</option><option value="key">仅私钥</option></select></div><div id="credentialList" class="credential-list"></div><div class="credential-add"><button id="addPasswordCredential" class="primary" type="button">新增密码</button><button id="addKeyCredential" type="button">粘贴私钥</button><button id="importKeyCredential" type="button">从文件导入私钥</button></div></div></div><div id="credentialEditor" class="modal hidden"><form id="credentialForm" class="dialog credential-editor"><h3 id="credentialTitle">新增密码凭证</h3><input id="credentialEditId" type="hidden"><input id="credentialType" type="hidden" value="password"><label>名称<input id="credentialName" required placeholder="例如：生产环境密码"></label><label id="credentialPasswordField">密码<div class="password-input"><input id="credentialPassword" type="password" autocomplete="new-password"><button class="password-toggle" data-password-target="credentialPassword" type="button" aria-pressed="false">显示</button></div></label><div id="credentialKeyField" class="credential-key-field hidden"><div class="credential-key-head"><span id="credentialKeyFile"></span><button id="reimportCredentialKey" type="button">从文件导入</button></div><textarea id="credentialKeyContent" spellcheck="false" placeholder="粘贴 OpenSSH / PEM 格式私钥内容（-----BEGIN ... PRIVATE KEY-----）"></textarea><small id="credentialKeyMeta"></small></div><label id="credentialPassphraseField" class="hidden">私钥口令（未加密可留空）<div class="password-input"><input id="credentialPassphrase" type="password" autocomplete="new-password"><button class="password-toggle" data-password-target="credentialPassphrase" type="button" aria-pressed="false">显示</button></div></label><label>备注<input id="credentialRemark" placeholder="标注用途、来源等"></label><div id="credentialUsageInfo" class="credential-usage hidden"></div><div class="actions"><button id="deleteCredential" class="hidden" type="button">删除</button><button id="cancelCredential" type="button">取消</button><button class="primary">保存</button></div></form></div>',
  );
  $("#credentialEditor").querySelectorAll(".password-toggle").forEach((button) => {
    button.onclick = () => {
      const target = $(`#${button.dataset.passwordTarget}`);
      if (!target) return;
      const show = target.type === "password";
      target.type = show ? "text" : "password";
      button.textContent = show ? "隐藏" : "显示";
      button.setAttribute("aria-pressed", String(show));
    };
  });
}
setupCredentialUI();
function setupQuickConnectUI() {
  $("#addSmall").insertAdjacentHTML("beforebegin", '<button id="quickConnectBtn" type="button">快速连接</button>');
  document.body.insertAdjacentHTML(
    "beforeend",
    '<div id="quickConnectModal" class="modal hidden"><form id="quickConnectForm" class="dialog quick-connect-dialog" role="dialog" aria-modal="true" aria-labelledby="quickConnectTitle"><h3 id="quickConnectTitle">快速连接</h3><label class="quick-connect-main">连接信息<input id="quickConnectInput" maxlength="2048" autocomplete="off" spellcheck="false" placeholder="例如：1.2.3.4 root 密码 ----主机名"></label><small class="quick-connect-rule">规则：支持“IP 用户名 密码 ----主机名”、IP:端口、用户名@IP:端口、SSH URL 和“主机:端口:用户名:密码”。省略端口默认 22，省略用户名默认 root；含空格内容请使用英文引号。</small><div id="quickCandidateStatus" class="quick-candidate-status" role="status" aria-live="polite">输入后将自动识别连接方式</div><div id="quickConnectCandidates" class="quick-connect-candidates" role="radiogroup" aria-label="连接信息解析结果"></div><div class="quick-connect-options"><label id="quickSaveOption" class="quick-connect-save"><input id="quickSaveHost" type="checkbox"><span>连接成功后保存到主机列表</span></label><small id="quickSaveHint" class="quick-save-hint">不勾选时仅创建临时会话，关闭标签后自动移除。</small></div><div class="actions"><button id="cancelQuickConnect" type="button">取消</button><button id="quickConnectSubmit" class="primary" disabled>确认并连接</button></div></form></div>',
  );
}
setupQuickConnectUI();
let quickConnectCandidates = [];
let quickConnectParseTimer = 0;
let quickConnectParseVersion = 0;
let quickConnectBusy = false;
function selectedQuickConnectCandidate() {
  const id = document.querySelector('input[name="quickConnectCandidate"]:checked')?.value || "";
  return quickConnectCandidates.find((candidate) => candidate.id === id) || null;
}
function syncQuickConnectSaveOption() {
  const candidate = selectedQuickConnectCandidate();
  const save = $("#quickSaveHost");
  const supportsSave = candidate?.scheme === "ssh";
  save.disabled = quickConnectBusy || !supportsSave;
  if (!supportsSave) save.checked = false;
  $("#quickSaveOption").classList.toggle("disabled", quickConnectBusy || !supportsSave);
  $("#quickSaveHint").textContent = !candidate
    ? "确认解析结果后可选择是否保存。"
    : !supportsSave
      ? "RDP 只能临时启动；自动保存目前仅支持已认证成功的 SSH。"
      : save.checked
        ? "只有 SSH 认证和 PTY 创建成功后才会保存；失败不会写入主机库。"
        : "不勾选时仅创建临时会话，关闭标签后自动移除。";
}
function renderQuickConnectCandidates(candidates, error = "") {
  quickConnectCandidates = Array.isArray(candidates) ? candidates : [];
  const status = $("#quickCandidateStatus");
  status.classList.toggle("bad", Boolean(error));
  status.textContent = error || (quickConnectCandidates.length
    ? `识别到 ${quickConnectCandidates.length} 种可能，请确认一项`
    : "输入后将自动识别连接方式");
  $("#quickConnectCandidates").innerHTML = quickConnectCandidates
    .map((candidate, index) => {
      const passwordHint = String(candidate.passwordHint || "").trim();
      const passwordText = passwordHint && passwordHint !== "未提供"
        ? `已填写（${passwordHint}）`
        : "未提供";
      const candidateName = candidate.name && candidate.name !== candidate.host
        ? candidate.name
        : `连接方案 ${index + 1}`;
      const port = Number(candidate.port) || 22;
      return `<label class="quick-connect-candidate"><input type="radio" name="quickConnectCandidate" value="${esc(candidate.id)}" ${index === 0 ? "checked" : ""}><span><span class="quick-candidate-title"><b>${esc(candidateName)}</b>${candidate.recommended ? '<em>推荐</em>' : ""}<i>${esc((candidate.scheme || "ssh").toUpperCase())}</i></span><span class="quick-candidate-fields"><span title="${esc(candidate.host)}"><i>主机</i><code>${esc(candidate.host)}</code></span><span><i>端口</i><code>${port}</code></span><span title="${esc(candidate.user)}"><i>用户名</i><code>${esc(candidate.user)}</code></span><span title="${esc(passwordText)}"><i>密码</i><code>${esc(passwordText)}</code></span></span><small>${esc(candidate.reason || "请核对连接信息")}</small></span></label>`;
    })
    .join("");
  syncCheckedLabels($("#quickConnectCandidates"));
  $("#quickConnectSubmit").disabled = quickConnectBusy || quickConnectCandidates.length === 0;
  syncQuickConnectSaveOption();
}
function setQuickConnectBusy(busy) {
  quickConnectBusy = busy;
  $("#quickConnectForm").setAttribute("aria-busy", String(busy));
  $("#quickConnectInput").disabled = busy;
  $("#cancelQuickConnect").disabled = busy;
  document.querySelectorAll('input[name="quickConnectCandidate"]').forEach((input) => {
    input.disabled = busy;
  });
  syncQuickConnectSaveOption();
  $("#quickConnectSubmit").disabled = busy || !selectedQuickConnectCandidate();
}
async function parseQuickConnectInput(input, version) {
  if (!input) {
    renderQuickConnectCandidates([]);
    return;
  }
  $("#quickCandidateStatus").classList.remove("bad");
  $("#quickCandidateStatus").textContent = "正在识别…";
  $("#quickConnectSubmit").disabled = true;
  try {
    const candidates = await api().ParseQuickConnectCandidates(input);
    if (version !== quickConnectParseVersion || $("#quickConnectInput").value.trim() !== input) return;
    renderQuickConnectCandidates(candidates || []);
  } catch (error) {
    if (version !== quickConnectParseVersion || $("#quickConnectInput").value.trim() !== input) return;
    renderQuickConnectCandidates([], String(error?.message || error).replace(/^Error:\s*/i, ""));
  }
}
function openQuickConnect() {
  if (quickConnectBusy) return toast("快速连接正在进行中，请稍候", true);
  window.clearTimeout(quickConnectParseTimer);
  quickConnectParseVersion++;
  $("#quickConnectInput").value = "";
  $("#quickSaveHost").checked = false;
  renderQuickConnectCandidates([]);
  $("#quickConnectModal").classList.remove("hidden");
  $("#quickConnectInput").focus();
}
$("#quickConnectBtn").onclick = openQuickConnect;
$("#cancelQuickConnect").onclick = () => {
  if (quickConnectBusy) return;
  window.clearTimeout(quickConnectParseTimer);
  quickConnectParseVersion++;
  $("#quickConnectInput").value = "";
  quickConnectCandidates = [];
  $("#quickConnectModal").classList.add("hidden");
};
$("#quickConnectInput").oninput = () => {
  window.clearTimeout(quickConnectParseTimer);
  const version = ++quickConnectParseVersion;
  const input = $("#quickConnectInput").value.trim();
  renderQuickConnectCandidates([]);
  if (!input) return;
  $("#quickCandidateStatus").textContent = "等待识别…";
  quickConnectParseTimer = window.setTimeout(() => parseQuickConnectInput(input, version), 180);
};
$("#quickConnectModal").onkeydown = (event) => {
  if (event.key !== "Escape" || quickConnectBusy) return;
  event.preventDefault();
  $("#cancelQuickConnect").click();
};
$("#quickConnectCandidates").onchange = () => {
  syncCheckedLabels($("#quickConnectCandidates"));
  syncQuickConnectSaveOption();
};
$("#quickSaveHost").onchange = syncQuickConnectSaveOption;
$("#quickConnectForm").onsubmit = async (event) => {
  event.preventDefault();
  if (quickConnectBusy) return;
  const input = $("#quickConnectInput").value.trim();
  const candidate = selectedQuickConnectCandidate();
  if (!input || !candidate) return toast("请先输入连接信息并确认解析结果", true);
  const saveAfterConnect = $("#quickSaveHost").checked && candidate.scheme === "ssh";
  const button = event.submitter || $("#quickConnectSubmit");
  setQuickConnectBusy(true);
  button.textContent = "连接中…";
  try {
    const host = await api().CreateQuickHostCandidate(input, candidate.id);
    state.settings.hosts.push(host);
    quickConnectParseVersion++;
    $("#quickConnectInput").value = "";
    quickConnectCandidates = [];
    $("#quickConnectModal").classList.add("hidden");
    selectHost(host.id);
    const result = await connectHostById(host.id);
    if (result?.status !== "connected" && result?.status !== "rdp") {
      const failure = result?.status === "failed" ? result.error || "临时连接失败" : "快速连接已取消";
      toast(`${failure}${saveAfterConnect ? "；未保存主机" : ""}`, true);
    } else {
      let saved = false;
      if (saveAfterConnect) {
        try {
          replaceSettings(await api().SaveConnectedTemporaryHost(host.id));
          saved = true;
          renderHosts();
          renderSessionTabs();
          detectHostCountries();
        } catch (saveError) {
          toast(`SSH 已连接，但保存主机失败：${String(saveError?.message || saveError)}`, true);
        }
      }
      $("#hostDrawer").classList.add("hidden");
      refreshVisibleMonitor();
      if (saved) toast("SSH 连接成功，主机已保存");
    }
  } catch (error) {
    toast(String(error), true);
  } finally {
    button.textContent = "确认并连接";
    setQuickConnectBusy(false);
  }
};
$('#credentialForm input[type="hidden"]').id = "credentialEditId";
function mergeHostFormLayout() {
  const basic = $("#hostBasic"),
    connect = $("#hostConnect"),
    grid = basic.querySelector(".host-form-grid");
  connect
    .querySelectorAll(".host-form-grid>label")
    .forEach((label) => grid.appendChild(label));
  for (const field of ["name", "group", "protocol", "port", "address", "username", "authType"])
    $(`#${field}`).closest("label")?.classList.add(`host-field-${field}`);
  $("#group").setAttribute("list", "hostGroupOptions");
  $("#group").placeholder = "选择已有分组或输入新分组";
  $("#group").insertAdjacentHTML("afterend", '<datalist id="hostGroupOptions"></datalist>');
  $("#timeout").closest("label").classList.add("hidden");
  $("#keepAlive").closest("label").classList.add("hidden");
  grid.insertAdjacentHTML(
    "beforeend",
    '<label class="node-domain-field"><span>入站域名（可选）</span><input id="vpsDomain" placeholder="例如：node.example.com"></label><label class="node-domain-field"><span>中转域名（可选）</span><input id="vpsMiddleDomain" placeholder="例如：relay.example.com"></label><label class="remark-field"><span>备注</span><textarea id="remark" rows="2" placeholder="填写主机用途、环境或其他说明（可选）"></textarea></label>',
  );
  basic.appendChild($("#authDescription"));
  basic.appendChild($("#hostKeyTrust"));
  basic.querySelector("h3").remove();
  connect.remove();
  $("#hostProxy h3").innerHTML =
    '<label class="proxy-toggle"><input id="proxyEnabled" type="checkbox">启用 SOCKS5 代理</label>';
  $("#hostProxy").insertAdjacentHTML(
    "afterbegin",
    '<div id="sshJumpField" class="ssh-jump-field"><label class="ssh-jump-host"><span>SSH 跳板主机（Jump Host）</span><select id="sshJumpHostId"><option value="">不使用 SSH 跳板</option></select></label><small class="ssh-jump-note">SSH 跳板与当前主机 SOCKS5 代理互斥；最多支持 4 级级联跳板（最外层跳板可独立配置代理）。</small></div>',
  );
  $("#proxyHost").value = "127.0.0.1";
  $("#proxyPort").value = 20808;
  $("#protocol").closest("label").insertAdjacentHTML("afterend", '<div id="rdpOptionsField" class="rdp-options-field hidden"><div class="rdp-options-head"><span>RDP 显示与本地资源</span><small>密码通过 Windows 凭据临时传递，会话退出后清理</small></div><div class="rdp-options-grid"><label><span>显示模式</span><select id="rdpDisplayMode"><option value="window">窗口</option><option value="fullscreen">全屏</option></select></label><label><span>分辨率</span><select id="rdpResolution"><option value="1280x1024">1280 × 1024</option><option value="1366x768">1366 × 768</option><option value="1600x900">1600 × 900</option><option value="1920x1080">1920 × 1080</option><option value="2560x1440">2560 × 1440</option></select></label><label class="rdp-check"><input id="rdpSmartSizing" type="checkbox">智能缩放</label><label class="rdp-check"><input id="rdpUseMultimon" type="checkbox">多显示器</label><label class="rdp-check"><input id="rdpRedirectClipboard" type="checkbox" checked>剪贴板</label><label class="rdp-check"><input id="rdpRedirectPrinters" type="checkbox">打印机</label><label><span>音频播放</span><select id="rdpAudioPlayback"><option value="0">在本机播放</option><option value="1">在远端播放</option><option value="2">禁用</option></select></label><label class="rdp-check"><input id="rdpAudioCapture" type="checkbox">录制本机音频</label><label class="rdp-check"><input id="rdpMapDrives" type="checkbox" checked>映射本地磁盘</label><label><span>指定磁盘（可选）</span><input id="rdpDriveIds" placeholder="例如 C,D；留空映射全部"></label></div><details class="rdp-gateway"><summary>RD Gateway</summary><div><label><span>使用方式</span><select id="rdpGatewayUsage"><option value="0">不使用</option><option value="1">始终使用</option><option value="2">直连失败后使用</option><option value="3">系统默认</option><option value="4">检测本地地址</option></select></label><label><span>网关地址</span><input id="rdpGatewayAddress" placeholder="gateway.example.com:443"></label><label><span>网关用户名</span><input id="rdpGatewayUsername" placeholder="DOMAIN\\user"></label></div><small>网关密码由 mstsc 安全提示临时输入，不写入主机配置。</small></details></div>');
  $("#rdpOptionsField .rdp-options-grid").insertAdjacentHTML(
    "afterbegin",
    '<label class="rdp-jump-host"><span>经 SSH 主机建立隧道</span><select id="rdpJumpHostId"><option value="">不使用 SSH 跳板</option></select></label>',
  );
  $("#rdpOptionsField .rdp-options-head").insertAdjacentHTML(
    "afterend",
    '<small class="rdp-route-note">SSH 跳板与目标 SOCKS5、RD Gateway 互斥，一次只能使用一种连接路径。</small>',
  );
  $("#rdpOptionsField").insertAdjacentHTML(
    "beforeend",
    '<small id="rdpRouteState" class="rdp-route-state"></small>',
  );
}
mergeHostFormLayout();
$("#passwordField").insertAdjacentHTML(
  "beforebegin",
  '<label id="credentialSelectField"><span>登录凭证</span><div class="credential-select-row"><select id="credentialId"><option value="">独立填写</option></select><button id="openCredentialLibraryFromHost" type="button" title="管理凭证库">管理</button><button id="saveHostAsCredentialBtn" type="button" title="将当前密码或私钥保存为凭证库条目">存为凭证</button></div></label>',
);
function hostFormValue() {
  const h = {};
  for (const k of [
    "id",
    "name",
    "group",
    "protocol",
    "address",
    "username",
    "password",
    "authType",
    "keyPath",
    "keyPassphrase",
    "credentialId",
    "proxyHost",
    "proxyUsername",
    "proxyPassword",
    "rdpResolution",
    "rdpDisplayMode",
    "rdpJumpHostId",
    "sshJumpHostId",
    "rdpGatewayAddress",
    "rdpGatewayUsername",
    "vpsDomain",
    "vpsMiddleDomain",
    "remark",
  ])
    h[k] = $(`#${k}`).value.trim();
  for (const k of ["port", "timeout", "keepAlive", "proxyPort"])
    h[k] = Number($(`#${k}`).value);
  h.proxyEnabled = $("#proxyEnabled").checked;
  h.rdpMapDrives = $("#rdpMapDrives").checked;
  h.rdpSmartSizing = $("#rdpSmartSizing").checked;
  h.rdpUseMultimon = $("#rdpUseMultimon").checked;
  h.rdpRedirectClipboard = $("#rdpRedirectClipboard").checked;
  h.rdpRedirectPrinters = $("#rdpRedirectPrinters").checked;
  h.rdpAudioCapture = $("#rdpAudioCapture").checked;
  h.rdpAudioPlayback = Number($("#rdpAudioPlayback").value);
  h.rdpGatewayUsage = Number($("#rdpGatewayUsage").value);
  h.rdpDriveIds = $("#rdpDriveIds").value
    .split(/[，,;；\s]+/)
    .map((value) => value.trim())
    .filter(Boolean);
  if ((h.protocol || "ssh").toLowerCase() === "rdp") {
    h.sshJumpHostId = "";
    if (h.rdpJumpHostId) {
      h.proxyEnabled = false;
      h.rdpGatewayUsage = 0;
      h.rdpGatewayAddress = "";
      h.rdpGatewayUsername = "";
    } else if (h.proxyEnabled) {
      h.rdpGatewayUsage = 0;
      h.rdpGatewayAddress = "";
      h.rdpGatewayUsername = "";
    }
  } else {
    h.rdpJumpHostId = "";
    if (h.sshJumpHostId) {
      h.proxyEnabled = false;
    }
  }
  return h;
}
function updateProxyFields() {
  const disabled = !$("#proxyEnabled").checked || $("#proxyEnabled").disabled;
  $("#hostProxy .host-form-grid").classList.toggle("proxy-disabled", disabled);
  document
    .querySelectorAll("#hostProxy .host-form-grid input")
    .forEach((input) => (input.disabled = disabled));
}
function fillCredentialSelect(selectedId = $("#credentialId").value) {
  const items = state.settings.credentials || [];
  $("#credentialId").innerHTML =
    '<option value="">独立填写</option>' +
    items.map((c) => `<option value="${esc(c.id)}">${c.type === "key" ? "密钥" : "密码"} · ${esc(c.name)}</option>`).join("");
  $("#credentialId").value = items.some((item) => item.id === selectedId) ? selectedId : "";
  updateCredentialUsage();
}
function updateCredentialUsage() {
  const credential = (state.settings.credentials || []).find((item) => item.id === $("#credentialId").value);
  if (credential) {
    const mfa = $("#authType").value.endsWith("_mfa");
    $("#authType").value = credential.type === "key" ? (mfa ? "key_mfa" : "key") : (mfa ? "password_mfa" : "password");
  }
  const selected = Boolean(credential);
  const key = $("#authType").value.startsWith("key");
  const agent = $("#authType").value.startsWith("agent");
  $("#passwordField").classList.toggle("hidden", key || agent || selected);
  $("#keyPathField").classList.toggle("hidden", !key || selected);
  $("#keyPassphraseField").classList.toggle("hidden", !key || selected);
  const saveAsCred = $("#saveHostAsCredentialBtn");
  if (saveAsCred) {
    const canSave = !selected && !agent;
    saveAsCred.classList.toggle("hidden", !canSave);
  }
  const dynamicMFA = $("#authType").value.endsWith("_mfa") ? "；动态验证码将在连接时询问" : "";
  $("#authDescription").textContent = (credential ? `使用凭证库：${credential.name}` : (agent ? "使用 Windows SSH Agent（OpenSSH / Pageant，最多尝试 6 把密钥）" : key ? "使用独立私钥登录" : "使用独立密码登录")) + dynamicMFA;
}
function updateAuthFields() {
  const type = $("#authType").value;
  const credential = (state.settings.credentials || []).find((item) => item.id === $("#credentialId").value);
  if (
    credential &&
    (type.startsWith("agent") ||
      (credential.type === "key" && !type.startsWith("key")) ||
      (credential.type === "password" && type.startsWith("key")))
  ) {
    $("#credentialId").value = "";
  }
  $("#authDescription").textContent = {
    password: "普通 SSH 密码登录",
    password_mfa: "先使用密码，连接时再动态询问二次验证",
    key: "使用私钥或证书登录",
    key_mfa: "先使用私钥或证书，连接时再动态询问二次验证",
    agent: "使用 Windows SSH Agent（OpenSSH / Pageant），不启用 Agent 转发",
    agent_mfa: "使用 Windows SSH Agent（OpenSSH / Pageant），连接时再动态询问二次验证",
  }[type];
  updateCredentialUsage();
}
function sshJumpWouldCycle(candidateId, editedHostId, hosts) {
  if (!editedHostId || !candidateId) return false;
  const byId = new Map(hosts.map((item) => [item.id, item]));
  const visited = new Set([editedHostId]);
  let cursor = candidateId;
  let hops = 0;
  while (cursor && hops < 16) {
    if (visited.has(cursor)) return true;
    visited.add(cursor);
    cursor = String(byId.get(cursor)?.sshJumpHostId || "").trim();
    hops += 1;
  }
  return false;
}
function fillSSHJumpHostSelect(selectedId = "", editedHostId = "") {
  const allHosts = availableSSHHosts();
  const hosts = allHosts.filter(
    (host) =>
      !host.temporary &&
      host.id !== editedHostId &&
      !sshJumpWouldCycle(host.id, editedHostId, allHosts),
  );
  $("#sshJumpHostId").innerHTML =
    '<option value="">不使用 SSH 跳板</option>' +
    hosts.map((host) =>
      `<option value="${esc(host.id)}">${esc(host.name || host.address)} · ${esc(host.address)}</option>`,
    ).join("");
  $("#sshJumpHostId").value = hosts.some((host) => host.id === selectedId)
    ? selectedId
    : "";
}
function fillRDPJumpHostSelect(selectedId = "", editedHostId = "") {
  const hosts = availableSSHHosts().filter(
    (host) => !host.temporary && host.id !== editedHostId,
  );
  $("#rdpJumpHostId").innerHTML =
    '<option value="">不使用 SSH 跳板</option>' +
    hosts.map((host) =>
      `<option value="${esc(host.id)}">${esc(host.name || host.address)} · ${esc(host.address)}</option>`,
    ).join("");
  $("#rdpJumpHostId").value = hosts.some((host) => host.id === selectedId)
    ? selectedId
    : "";
}
function updateProtocol(reset = true) {
  const rdp = $("#protocol").value === "rdp";
  if (reset) {
    $("#port").value = rdp ? 3389 : 22;
    $("#username").value = rdp ? "administrator" : "root";
    if (!rdp) $("#rdpJumpHostId").value = "";
    else $("#sshJumpHostId").value = "";
  }
  $("#authType").disabled = rdp;
  $("#hostBasic").classList.toggle("rdp-mode", rdp);
  $("#rdpOptionsField").classList.toggle("hidden", !rdp);
  $("#sshJumpField").classList.toggle("hidden", rdp);
  document.querySelectorAll(".node-domain-field").forEach((field) => field.classList.toggle("hidden", rdp));
  $("#authType").closest("label").classList.toggle("hidden", rdp);
  $("#authDescription").classList.toggle("hidden", rdp);
  $("#hostKeyTrust").classList.toggle("hidden", rdp || !$("#hostKeyText").textContent);
  $("#testHostConfig").textContent = rdp ? "测试端口" : "测试连接";
  updateRDPOptionFields();
  updateSSHJumpFields();
  updateProxyFields();
  updateAuthFields();
}
function updateSSHJumpFields() {
  const rdp = $("#protocol").value === "rdp";
  const jumpSelect = $("#sshJumpHostId");
  if (!jumpSelect) return;
  const jump = !rdp && Boolean(jumpSelect.value);
  const proxy = !rdp && $("#proxyEnabled").checked;
  jumpSelect.disabled = rdp || (!jump && proxy);
  jumpSelect.closest("label")?.classList.toggle("route-disabled", jumpSelect.disabled);
  if (!rdp) {
    $("#proxyEnabled").disabled = jump;
  }
}
function updateRDPOptionFields() {
  const rdp = $("#protocol").value === "rdp";
  const jumpSelect = $("#rdpJumpHostId");
  const jump = rdp && Boolean(jumpSelect.value);
  const proxy = rdp && $("#proxyEnabled").checked;
  const gateway = rdp && Number($("#rdpGatewayUsage").value) !== 0;
  const drives = $("#rdpMapDrives").checked;
  $("#rdpDriveIds").disabled = !drives;
  jumpSelect.disabled = !rdp || (!jump && (proxy || gateway));
  jumpSelect.closest("label").classList.toggle("route-disabled", jumpSelect.disabled);
  if (rdp) {
    $("#proxyEnabled").disabled = jump;
  }
  $("#rdpGatewayUsage").disabled = jump;
  $("#rdpGatewayAddress").disabled = jump || !gateway;
  $("#rdpGatewayUsername").disabled = jump || !gateway;
  const route = jump
    ? `当前连接路径：经 ${hostDisplayName(jumpSelect.value)} 建立 SSH 隧道`
    : proxy
      ? "当前连接路径：目标主机 SOCKS5"
      : gateway
        ? "当前连接路径：RD Gateway"
        : "当前连接路径：直连";
  $("#rdpRouteState").textContent = route;
}
$("#rdpMapDrives").onchange = updateRDPOptionFields;
$("#sshJumpHostId").onchange = () => {
  if ($("#sshJumpHostId").value) {
    $("#proxyEnabled").checked = false;
  }
  updateSSHJumpFields();
  updateProxyFields();
};
$("#rdpJumpHostId").onchange = () => {
  if ($("#rdpJumpHostId").value) {
    $("#proxyEnabled").checked = false;
    $("#rdpGatewayUsage").value = "0";
    updateProxyFields();
  }
  updateRDPOptionFields();
};
$("#rdpGatewayUsage").onchange = () => {
  if (Number($("#rdpGatewayUsage").value) !== 0) {
    $("#rdpJumpHostId").value = "";
    $("#proxyEnabled").checked = false;
    updateProxyFields();
  }
  updateRDPOptionFields();
};
function showHostSection() {
  document
    .querySelectorAll(".host-form-card")
    .forEach((section) => section.classList.remove("hidden"));
}
function setHostEditorBusy(busy) {
  $("#hostForm").setAttribute("aria-busy", busy ? "true" : "false");
  $("#testHostConfig").disabled = busy;
  $("#hostForm button.primary").disabled = busy;
}
function hostEditorOperationCurrent(revision) {
  return state.hostEditorRevision === revision && !$("#hostEditor").classList.contains("hidden");
}
function fillHostGroupOptions() {
  const groups = [...new Set([
    ...(state.settings?.groups || []),
    ...(state.settings?.hosts || []).map((host) => String(host.group || "").trim()),
  ].map((group) => String(group || "").trim()).filter(Boolean))]
    .sort((left, right) => left.localeCompare(right, "zh-CN"));
  $("#hostGroupOptions").innerHTML = groups
    .map((group) => `<option value="${esc(group)}"></option>`)
    .join("");
}
function openHost(h = {}, fromWorkspace = false) {
  state.hostEditorRevision += 1;
  state.hostEditorFromWorkspace = fromWorkspace;
  fillHostGroupOptions();
  const linkedVPS = (state.vpsOverview?.vpsList || []).find((item) => (item.alias || item.id) === h.vpsAlias);
  h = { ...h, vpsDomain: h.vpsDomain || linkedVPS?.domain || "", vpsMiddleDomain: h.vpsMiddleDomain || linkedVPS?.middle_domain || "" };
  for (const k of [
    "id",
    "name",
    "group",
    "address",
    "username",
    "password",
    "keyPath",
    "keyPassphrase",
    "proxyUsername",
    "proxyPassword",
    "rdpGatewayAddress",
    "rdpGatewayUsername",
    "vpsDomain",
    "vpsMiddleDomain",
    "remark",
  ])
    $(`#${k}`).value = h[k] || "";
  $("#proxyHost").value = h.proxyHost || "127.0.0.1";
  $("#protocol").value = h.protocol || "ssh";
  $("#port").value = h.port || ($("#protocol").value === "rdp" ? 3389 : 22);
  $("#timeout").value = h.timeout || 12;
  $("#keepAlive").value = h.keepAlive || 30;
  $("#proxyPort").value = h.proxyPort || 20808;
  $("#proxyEnabled").checked = Boolean(h.proxyEnabled);
  $("#rdpResolution").value = h.rdpResolution || "1280x1024";
  $("#rdpDisplayMode").value = h.rdpDisplayMode || "window";
  $("#rdpMapDrives").checked = h.rdpResolution ? Boolean(h.rdpMapDrives) : true;
  $("#rdpSmartSizing").checked = Boolean(h.rdpSmartSizing);
  $("#rdpUseMultimon").checked = Boolean(h.rdpUseMultimon);
  $("#rdpRedirectClipboard").checked = h.rdpRedirectClipboard !== false;
  $("#rdpRedirectPrinters").checked = Boolean(h.rdpRedirectPrinters);
  $("#rdpAudioCapture").checked = Boolean(h.rdpAudioCapture);
  $("#rdpAudioPlayback").value = String(h.rdpAudioPlayback ?? 0);
  $("#rdpGatewayUsage").value = String(h.rdpGatewayUsage ?? 0);
  $("#rdpDriveIds").value = Array.isArray(h.rdpDriveIds) ? h.rdpDriveIds.join(", ") : "";
  fillRDPJumpHostSelect(h.rdpJumpHostId || "", h.id || "");
  fillSSHJumpHostSelect(h.sshJumpHostId || "", h.id || "");
  if ($("#protocol").value === "rdp") {
    if ($("#rdpJumpHostId").value) {
      $("#proxyEnabled").checked = false;
      $("#rdpGatewayUsage").value = "0";
    } else if ($("#proxyEnabled").checked) {
      $("#rdpGatewayUsage").value = "0";
    }
  } else if ($("#sshJumpHostId").value) {
    $("#proxyEnabled").checked = false;
  }
  updateRDPOptionFields();
  updateSSHJumpFields();
  $("#authType").value = h.authType || "password";
  $("#hostKeyText").textContent = h.hostKey ? `已固定 SSH 主机密钥：${h.hostKey}` : "";
  $("#hostKeyTrust").classList.toggle("hidden", !h.hostKey || (h.protocol || "ssh") === "rdp");
  updateProxyFields();
  updateProtocol(false);
  fillCredentialSelect(h.credentialId || "");
  showHostSection();
  $("#hostTestResult").textContent = "";
  $("#modalTitle").textContent = h.temporary
    ? "保存临时主机"
    : h.id ? "编辑主机" : "新增主机";
  $("#hostForm button.primary").textContent = h.temporary
    ? "保存到主机库"
    : "保存";
  setHostEditorBusy(false);
  $("#hostEditor").classList.remove("hidden");
}
function currentProvider() {
  return (state.settings?.aiProviders || []).find(
    (item) => item.id === state.settings?.currentProviderId,
  );
}
const AI_PERMISSION_NAMES = {
  suggest: "建议",
  review: "审核",
  managed: "托管",
};
function normalizeAIPermission(permission) {
  return Object.hasOwn(AI_PERMISSION_NAMES, permission) ? permission : "review";
}
async function confirmManagedPermission(permission, previousPermission = "") {
  if (
    permission !== "managed" ||
    normalizeAIPermission(previousPermission) === "managed"
  )
    return true;
  return uiConfirm(
    "托管权限下，AI 执行服务器命令时不再逐条询问，将持续操作和验证直到任务结束。确认启用托管权限？",
    "启用 AI 托管权限",
  );
}
function renderSettingsAI() {
  const providers = state.settings?.aiProviders || [];
  const add = $("#manageAI");
  add.disabled = providers.length >= 8;
  add.textContent = providers.length >= 8 ? "已达上限" : "新增供应商";
  add.title = providers.length >= 8 ? "最多可添加 8 个 AI 供应商" : "";
  $("#settingsAIProviders").innerHTML = providers.length
    ? providers
        .slice(0, 8)
        .map((item) => {
          const current = item.id === state.settings.currentProviderId;
          return `<div class="settings-provider ${current ? "current" : ""}"><span class="settings-provider-name" title="${esc(item.name)}">${esc(item.name)}</span><span class="settings-provider-actions"><button class="settings-provider-use" type="button" data-provider-action="use" data-id="${esc(item.id)}" aria-pressed="${current}" ${current ? "disabled" : ""}>${current ? "已使用" : "使用"}</button><button class="settings-provider-edit" type="button" data-provider-action="edit" data-id="${esc(item.id)}">编辑</button></span></div>`;
        })
        .join("")
    : '<span class="settings-ai-empty">暂无供应商</span>';
}
function fillAI() {
  const providers = state.settings.aiProviders || [];
  const c = currentProvider();
  $("#providerTop").innerHTML = providers.length
    ? providers
        .map(
          (item) =>
            `<option value="${esc(item.id)}">${esc(item.name)}</option>`,
        )
        .join("")
    : '<option value="">暂无供应商</option>';
  $("#providerTop").value = c?.id || "";
  const models = c ? normalizeAIModels([...(c.models || []), c.model]) : [];
  $("#modelTop").innerHTML = models.length ? models.map(model => `<option value="${esc(model)}" title="${esc(model)}">${esc(model)}</option>`).join("") : '<option value="">暂无模型</option>';
  $("#modelTop").value = c?.model || "";
  $("#modelTop").title = $("#modelTop").value || "暂无模型";
  $("#aiPermission").value = normalizeAIPermission(c?.mode);
  $("#aiModeHint").textContent = c
    ? `${c.name} · ${AI_PERMISSION_NAMES[normalizeAIPermission(c.mode)]}`
    : "未配置 AI";
  renderSettingsAI();
}
function normalizeAIModels(models) {
  if (!Array.isArray(models)) return [];
  return [...new Set(models.map((model) => String(model || "").trim()).filter(Boolean))];
}
function renderAIProviderModelOptions(selected = $("#providerModelSelect").value) {
  const models = state.aiProviderDraftModels;
  $("#providerModelSelect").innerHTML = [
    `<option value="">${models.length ? "请选择模型" : "暂无可选模型，请先获取"}</option>`,
    ...models.map(
      (model) => `<option value="${esc(model)}">${esc(model)}</option>`,
    ),
  ].join("");
  $("#providerModelSelect").value = models.includes(selected) ? selected : "";
  $("#modelCacheHint").textContent = models.length
    ? `已缓存 ${models.length} 个模型，请从列表选择`
    : "点击“获取 API 模型”后选择模型，保存时自动缓存";
}
function aiProviderFormConfig() {
  return {
    id: $("#providerId").value,
    name: $("#providerName").value.trim(),
    endpoint: $("#endpoint").value.trim(),
    apiKey: $("#apiKey").value.trim(),
    model: $("#providerModelSelect").value,
    models: normalizeAIModels(state.aiProviderDraftModels),
    mode: $("#mode").value,
  };
}
function invalidateAIProviderModelCache() {
  const source = state.aiProviderModelSource;
  if (
    $("#endpoint").value.trim() === source.endpoint &&
    $("#apiKey").value.trim() === source.apiKey
  )
    return;
  state.aiProviderDraftModels = [];
  renderAIProviderModelOptions();
  $("#modelCacheHint").textContent = "API 连接信息已更改，请重新获取模型";
}
function openAIProvider(c = {}) {
  $("#aiConfigTitle").textContent = c.id ? "编辑 AI 供应商" : "新增 AI 供应商";
  $("#providerId").value = c.id || "";
  $("#providerName").value = c.name || "";
  $("#endpoint").value = c.endpoint || "";
  $("#apiKey").value = c.apiKey || "";
  state.aiProviderModelSource = {
    endpoint: $("#endpoint").value.trim(),
    apiKey: $("#apiKey").value.trim(),
  };
  state.aiProviderDraftModels = normalizeAIModels(
    c.models?.length ? c.models : [c.model],
  );
  renderAIProviderModelOptions(c.model || "");
  $("#mode").value = normalizeAIPermission(c.mode);
  $("#deleteProvider").classList.toggle("hidden", !c.id);
  $("#aiModal").classList.remove("hidden");
}

function showHostListPage(refreshVps = true) {
  switchAppMode("shell");
  $("#hostEditor").classList.add("hidden");
  $("#settingsPage").classList.add("hidden");
  $("#aboutPage").classList.add("hidden");
  renderHosts();
  if (refreshVps) loadShellVPS();
  $("#hostDrawer").classList.remove("hidden");
}
$("#hostsBtn").onclick = () => showHostListPage();
$("#nodeModeBtn").onclick = () => switchAppMode("node");
let nodeModeReadyPromise = null;
const nodeFrameCacheToken = Date.now().toString(36);
let nodeModeModalVisible = false;
let nodeActionModalVisible = false;
let requestedAppMode = "shell";
let nodeActionReadyPromise = null;
let nodeActionRequestId = 0;
let nodeActionBusyRequestId = 0;
function setNodeActionBusy(requestId, busy) {
  if (busy) nodeActionBusyRequestId = requestId;
  else if (nodeActionBusyRequestId === requestId) nodeActionBusyRequestId = 0;
  const button = $("#closeNodeAction");
  const active = nodeActionBusyRequestId !== 0;
  button.disabled = active;
  button.textContent = active ? "执行中…" : "关闭";
}
function nodeActionHasVisibleModal() {
  try {
    return Boolean($("#nodeActionFrame").contentDocument?.querySelector(".modal:not(.hidden)"));
  } catch {
    return nodeActionModalVisible;
  }
}
function syncNodeFrameSurface() {
  const theme = "dark";
  const background = "#060a12";
  $("#nodeModePage").style.backgroundColor = background;
  $("#nodeModeFrame").style.backgroundColor = background;
  $("#nodeActionFrame").style.backgroundColor = "transparent";
  for (const frame of [$("#nodeModeFrame"), $("#nodeActionFrame")]) {
    try {
      frame.contentDocument?.documentElement?.setAttribute("data-shell-theme", theme);
      frame.contentWindow?.applyShellTheme?.(theme);
    } catch {}
  }
}
syncNodeFrameSurface();
new MutationObserver(syncNodeFrameSurface).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
function updateNodeModalChromeMask() {
  const actionVisible = nodeActionModalVisible && !$("#nodeActionHost").classList.contains("hidden");
  const modeVisible = nodeModeModalVisible && !$("#nodeModePage").classList.contains("hidden");
  const mask = $("#nodeModalChromeMask");
  mask.classList.toggle("hidden", !actionVisible && !modeVisible);
  mask.classList.toggle("node-action", actionVisible);
}
function ensureNodeModeReady() {
  const frame = $("#nodeModeFrame");
  if (typeof frame.contentWindow?.refreshNodeMode === "function") {
    return Promise.resolve(frame.contentWindow);
  }
  if (nodeModeReadyPromise) return nodeModeReadyPromise;
  nodeModeReadyPromise = new Promise((resolve, reject) => {
    let settled = false;
    const cleanup = () => {
      window.clearTimeout(timeout);
      frame.removeEventListener("load", onLoad);
    };
    const fail = (error) => {
      if (settled) return;
      settled = true;
      cleanup();
      reject(error instanceof Error ? error : new Error(String(error || "节点模式加载失败")));
    };
    const onLoad = () => {
      Promise.resolve(frame.contentWindow?.nodeModeReady)
        .then(() => {
          if (typeof frame.contentWindow?.refreshNodeMode !== "function") throw new Error("节点模式初始化未完成");
          if (settled) return;
          settled = true;
          cleanup();
          resolve(frame.contentWindow);
        })
        .catch(fail);
    };
    const timeout = window.setTimeout(() => {
      fail(new Error("节点模式加载超时"));
      frame.src = "about:blank";
    }, 10000);
    nodeModeModalVisible = false;
    updateNodeModalChromeMask();
    frame.addEventListener("load", onLoad);
    frame.src = `./node-mode/index.html?v=${nodeFrameCacheToken}`;
  }).catch((error) => {
    nodeModeReadyPromise = null;
    throw error;
  });
  return nodeModeReadyPromise;
}
function isAppLocked() {
  return !$("#unlockModal").classList.contains("hidden");
}
function switchAppMode(mode, refreshNode = true) {
  const nodeMode = mode === "node";
  if (nodeMode && isAppLocked()) return;
  requestedAppMode = nodeMode ? "node" : "shell";
  const alreadyLoaded = typeof $("#nodeModeFrame").contentWindow?.refreshNodeMode === "function";
  if (nodeMode && !alreadyLoaded) {
    const button = $("#nodeModeBtn");
    button.disabled = true;
    button.textContent = "加载中…";
    ensureNodeModeReady()
      .then(() => {
        if (requestedAppMode === "node") switchAppMode("node", false);
      })
      .catch((error) => toast(String(error), true))
      .finally(() => {
        button.disabled = false;
        button.textContent = "节点模式";
      });
    return;
  }
  const previousBaseWidth = app.classList.contains("node-mode-active") || $("main").classList.contains("ai-collapsed") ? 1360 : 1690;
  const modeChanged = app.classList.contains("node-mode-active") !== nodeMode;
  app.classList.toggle("node-mode-active", nodeMode);
  $("#nodeModePage").classList.toggle("hidden", !nodeMode);
  $("#hostsBtn").classList.toggle("active", !nodeMode);
  $("#nodeModeBtn").classList.toggle("active", nodeMode);
  updateNodeModalChromeMask();
  if (nodeMode) {
    $("#hostDrawer").classList.add("hidden");
    $("#hostEditor").classList.add("hidden");
    $("#settingsPage").classList.add("hidden");
    $("#aboutPage").classList.add("hidden");
    ensureNodeModeReady()
      .then((nodeWindow) => {
        nodeWindow.syncNodeModalState?.();
        if (refreshNode && alreadyLoaded && modeChanged) return nodeWindow.refreshNodeMode?.();
      })
      .catch((error) => toast(String(error), true));
  } else {
    refreshVisibleMonitor();
  }
  if (!modeChanged) return;
  const availableWidth = window.screen?.availWidth || window.screen?.width || 1690;
  const availableHeight = window.screen?.availHeight || window.screen?.height || 980;
  const scale = Math.max(0.6, Math.min(1, (availableWidth * 0.96) / 1690, (availableHeight * 0.94) / 980));
  const baseWidth = !nodeMode && !$("main").classList.contains("ai-collapsed") ? 1690 : 1360;
  if (baseWidth !== previousBaseWidth) {
    window.runtime?.WindowSetSize?.(Math.max(780, Math.floor(baseWidth * scale)), Math.max(600, Math.floor(980 * scale)));
    window.setTimeout(adaptContentScale, 50);
  } else {
    window.requestAnimationFrame(adaptContentScale);
  }
}
function ensureNodeActionReady() {
  const frame = $("#nodeActionFrame");
  if (frame.dataset.ready && typeof frame.contentWindow?.refreshNodeMode === "function") {
    return Promise.resolve(frame.contentWindow);
  }
  if (nodeActionReadyPromise) return nodeActionReadyPromise;
  nodeActionReadyPromise = new Promise((resolve, reject) => {
    let settled = false;
    const cleanup = () => {
      window.clearTimeout(timeout);
      frame.removeEventListener("load", onLoad);
    };
    const fail = (error) => {
      if (settled) return;
      settled = true;
      cleanup();
      delete frame.dataset.ready;
      reject(error instanceof Error ? error : new Error(String(error || "节点功能加载失败")));
    };
    const onLoad = () => {
      Promise.resolve(frame.contentWindow?.nodeModeReady)
        .then(() => {
          if (typeof frame.contentWindow?.refreshNodeMode !== "function") throw new Error("节点功能初始化未完成");
          if (settled) return;
          settled = true;
          cleanup();
          frame.dataset.ready = "1";
          resolve(frame.contentWindow);
        })
        .catch(fail);
    };
    const timeout = window.setTimeout(() => {
      fail(new Error("节点功能加载超时"));
      frame.src = "about:blank";
    }, 10000);
    frame.addEventListener("load", onLoad);
    frame.src = `./node-mode/index.html?shellAction=1&v=${nodeFrameCacheToken}`;
  }).finally(() => {
    nodeActionReadyPromise = null;
  });
  return nodeActionReadyPromise;
}
async function openNodeAction(action, alias) {
  if (isAppLocked()) return;
  const host = $("#nodeActionHost");
  const frame = $("#nodeActionFrame");
  const requestId = ++nodeActionRequestId;
  showHostListPage(false);
  window.clearTimeout(nodeActionUnloadTimer);
  nodeActionModalVisible = false;
  host.classList.add("hidden");
  updateNodeModalChromeMask();
  const alreadyLoaded = Boolean(frame.dataset.ready);
  try {
    const nodeWindow = await ensureNodeActionReady();
    if (requestId !== nodeActionRequestId) return;
    if (alreadyLoaded) await nodeWindow.refreshNodeMode?.(false, false);
    if (requestId !== nodeActionRequestId) return;
    host.classList.remove("hidden");
    updateNodeModalChromeMask();
    const handler = action === "restartSingbox" ? nodeWindow.controlSingbox : nodeWindow[action];
    if (typeof handler !== "function") throw new Error(`节点功能不可用：${action}`);
    setNodeActionBusy(requestId, true);
    if (action === "restartSingbox") await handler.call(nodeWindow, alias, "restart");
    else await handler.call(nodeWindow, alias);
    if (requestId !== nodeActionRequestId) return;
    setNodeActionBusy(requestId, false);
    nodeActionModalVisible = nodeActionHasVisibleModal();
    updateNodeModalChromeMask();
    if (!nodeActionModalVisible) hideNodeAction();
  } catch (error) {
    if (requestId === nodeActionRequestId) {
      hideNodeAction(true);
    }
    throw error;
  } finally {
    setNodeActionBusy(requestId, false);
  }
}
let nodeActionUnloadTimer = 0;
function hideNodeAction(force = false) {
  if (nodeActionBusyRequestId && !force) return;
  nodeActionRequestId += 1;
  $("#nodeActionHost").classList.add("hidden");
  nodeActionModalVisible = false;
  updateNodeModalChromeMask();
  window.clearTimeout(nodeActionUnloadTimer);
  nodeActionUnloadTimer = window.setTimeout(() => {
    const frame = $("#nodeActionFrame");
    if (!$("#nodeActionHost").classList.contains("hidden")) return;
    frame.src = "about:blank";
    delete frame.dataset.ready;
    nodeActionReadyPromise = null;
  }, 30000);
}
$("#closeNodeAction").onclick = hideNodeAction;
window.addEventListener("message", (event) => {
  const fromNodeMode = event.source === $("#nodeModeFrame").contentWindow;
  const fromNodeAction = event.source === $("#nodeActionFrame").contentWindow;
  if (!fromNodeMode && !fromNodeAction) return;
  if (!event.data || typeof event.data !== "object") return;
  if (event.data?.type === "node-data-changed") {
    if (fromNodeAction) {
      $("#nodeModeFrame").contentWindow?.markNodeModeDirty?.();
    }
    const alias = event.data.alias || "";
    const runtimeState = event.data.runtimeState || null;
    reloadShellVPSOverview(alias, runtimeState)
      .then(async () => {
        if (!alias || runtimeState) return;
        const host = (state.settings.hosts || []).find((item) => !item.deleted && item.vpsAlias === alias);
        if (host) await refreshVPSStatus(host, false, true, { syncRemote: false, allowHostKeyPrompt: false, reloadOverview: false });
      })
      .catch((error) => toast(`节点数据刷新失败：${String(error)}`, true));
  } else if (event.data?.type === "node-modal-state") {
    if (fromNodeMode) {
      nodeModeModalVisible = Boolean(event.data.visible);
    } else if (fromNodeAction) {
      nodeActionModalVisible = Boolean(event.data.visible);
    }
    updateNodeModalChromeMask();
  } else if (fromNodeAction && event.data?.type === "node-action-close") {
    hideNodeAction();
  } else if (fromNodeAction && event.data?.type === "node-action-complete") {
    hideNodeAction(true);
    showHostListPage(false);
  } else if (fromNodeAction && event.data?.type === "node-view-list") {
    hideNodeAction();
    switchAppMode("node", false);
    ensureNodeModeReady()
      .then(async (nodeWindow) => {
        await nodeWindow.refreshNodeMode?.(true, false);
        await nodeWindow.showNodesForAlias?.(event.data.alias || "");
      })
      .catch((error) => toast(String(error), true));
  }
});
$("#closeDrawer").onclick = () => {
  $("#hostDrawer").classList.add("hidden");
  refreshVisibleMonitor();
};
$("#addSmall").onclick = () => openHost();
$("#refreshAllVps").onclick = refreshSelectedHostStatuses;
document.querySelectorAll("#hostEditor .cancel").forEach(
  (button) =>
    (button.onclick = () => {
      const returnWorkspace = state.hostEditorFromWorkspace;
      state.hostEditorRevision += 1;
      $("#hostEditor").classList.add("hidden");
      renderHosts();
      if (!returnWorkspace)
        $("#hostDrawer").classList.remove("hidden");
      state.hostEditorFromWorkspace = false;
      if (returnWorkspace) refreshVisibleMonitor();
    }),
);
let hostSearchTimer = 0;
$("#search").oninput = () => {
  window.clearTimeout(hostSearchTimer);
  hostSearchTimer = window.setTimeout(renderHosts, 120);
};
$("#groupFilter").onchange = renderHosts;
$("#countryFilter").onchange = renderHosts;
syncCheckedLabels($("#showDeleted").closest("label"));
$("#showDeleted").onchange = () => {
  syncCheckedLabels($("#showDeleted").closest("label"));
  renderHosts();
};
$("#sortHosts").onchange = renderHosts;
$("#recentFilter").onclick = () => {
  state.recentOnly = !state.recentOnly;
  $("#recentFilter").classList.toggle("active", state.recentOnly);
  renderHosts();
};
$("#addGroup").onclick = async () => {
  const name = await uiPrompt("输入新分组名称");
  if (!name) return;
  try {
    replaceSettings(await api().SaveGroup(name));
    renderHosts();
    toast("分组已添加");
  } catch (e) {
    toast(String(e), true);
  }
};
async function importHosts(method, button) {
  const originalText = button.textContent;
  button.disabled = true;
  button.textContent = "导入中";
  try {
    const previousIds = new Set((state.settings.hosts || []).map((host) => host.id));
    const result = await api()[method]();
    replaceSettings(result.settings);
    renderHosts();
    const importedHosts = (state.settings.hosts || []).filter((host) => !previousIds.has(host.id));
    const importedIds = importedHosts.map((host) => host.id);
    if (importedIds.length) probeHosts(importedIds);
    const sshHosts = importedHosts.filter((host) => !host.deleted && (host.protocol || "ssh") === "ssh");
    let checked = 0;
    for (let index = 0; index < sshHosts.length; index++) {
      button.textContent = `检测 ${index + 1}/${sshHosts.length}`;
      try {
        const initialized = await ensureHostVPS(sshHosts[index], Boolean(sshHosts[index].hostKey));
        if (initialized.runtimeState && initialized.runtimeState.status !== "ssh_failed") checked += 1;
      } catch (error) {
        console.warn("导入主机状态检测失败", error);
      }
    }
    const source = result.finalShell ? `，其中 FinalShell ${result.finalShell} 台` : "";
    const ignored = result.ignoredFiles ? `，忽略非主机 JSON ${result.ignoredFiles} 个` : "";
    const remote = sshHosts.length ? `，状态检测 ${checked}/${sshHosts.length} 台` : "";
    toast(`导入完成：新增 ${result.imported} 台，跳过 ${result.skipped} 台${source}${ignored}${remote}`);
  } catch (e) {
    toast(String(e), true);
  } finally {
    button.disabled = false;
    button.textContent = originalText;
  }
}
$("#importHosts").onclick = () => importHosts("ImportHosts", $("#importHosts"));
$("#importHostFolder").onclick = () => importHosts("ImportHostsFromFolder", $("#importHostFolder"));
$("#exportHosts").onclick = async () => {
  const ids = [...state.hostSelection];
  if (!ids.length) return toast("请勾选要导出的主机，或使用表头全选", true);
  if (!(await uiConfirm(`将导出选中的 ${ids.length} 台主机，文件包含连接凭据，请妥善保管。确认导出？`))) return;
  try {
    const security = await api().GetPublicSettings();
    let exported = false;
    if (security.protectExport) {
      const password = await uiPrompt("请输入软件密保", "", true);
      if (!password) return;
      exported = await api().ExportHostsWithPassword(password, ids);
    } else exported = await api().ExportHosts(ids);
    if (!exported) return;
    toast(`导出完成：已生成 ${ids.length} 个独立 JSON 文件`);
  } catch (e) {
    if (e) {
      const message = String(e?.message || e).replace(/^Error:\s*/i, "");
      toast(message || "导出失败，请重试", true);
    }
  }
};
$("#hostList").onchange = (e) => {
  if (e.target.id === "hostSelectAll") {
    document.querySelectorAll(".host-check:not(:disabled)").forEach((checkbox) => {
      if (e.target.checked) state.hostSelection.add(checkbox.dataset.id);
      else state.hostSelection.delete(checkbox.dataset.id);
    });
    renderHosts();
    return;
  }
  if (!e.target.classList.contains("host-check")) return;
  if (e.target.checked) state.hostSelection.add(e.target.dataset.id);
  else state.hostSelection.delete(e.target.dataset.id);
  renderHosts();
};
$("#hostList").onclick = async (e) => {
  const batchButton = e.target.closest("#batchDeleteHosts, #batchPermanentDeleteHosts");
  if (batchButton) {
    const permanent = batchButton.id === "batchPermanentDeleteHosts";
    const ids = [...$("#hostList").querySelectorAll(".host-check:checked:not(:disabled)")]
      .map((checkbox) => checkbox.dataset.id)
      .filter(Boolean);
    const hosts = ids
      .map((id) => state.settings.hosts.find((host) => host.id === id))
      .filter(Boolean);
    if (!hosts.length) {
      renderHosts();
      return;
    }
    const message = permanent
      ? `永久删除选中的 ${hosts.length} 台主机？\n\n该操作无法恢复，只清除软件本地记录和节点聚合展示，不会删除远端服务器配置。`
      : `删除选中的 ${hosts.length} 台主机？\n\n主机将移入“已删除”，并清除本地节点聚合展示。`;
    if (!(await uiConfirm(message, permanent ? "批量永久删除" : "批量删除主机"))) return;
    const originalText = batchButton.textContent;
    batchButton.disabled = true;
    batchButton.textContent = permanent ? "永久删除中…" : "删除中…";
    try {
      replaceSettings(await api()[permanent ? "PermanentDeleteHosts" : "DeleteHosts"](ids));
      for (const host of hosts) {
        discardSession(host.id);
        state.hostSelection.delete(host.id);
        delete state.vpsStatuses[host.id];
        clearShellVPSStatusCache(host.vpsAlias);
      }
      ids.forEach((id) => state.expandedHosts.delete(id));
      state.vpsOverview = await window.go.nodemode.App.GetOverview();
      $("#nodeModeFrame").contentWindow?.markNodeModeDirty?.();
      renderHosts();
      toast(permanent ? `已永久删除 ${hosts.length} 台主机` : `已删除 ${hosts.length} 台主机`);
    } catch (error) {
      renderHosts();
      toast(String(error), true);
    }
    return;
  }
  const entry = e.target.closest(".host-entry");
  if (!entry) return;
  const host = state.settings.hosts.find((h) => h.id === entry.dataset.id);
  if (!host) return;
  if (e.target.closest(".host-check")) return;
  const latencyButton = e.target.closest(".latencyBtn");
  if (latencyButton) {
    e.preventDefault();
    e.stopPropagation();
    latencyButton.disabled = true;
    latencyButton.textContent = "检测中";
    latencyButton.classList.remove("measured", "offline");
    try {
      const success = await checkHostLatency(host.id, true, true);
      if (!success) {
        const result = state.latencies[host.id];
        toast(result?.title || "延迟检测失败，请检查主机地址、端口或代理", true);
      }
    } catch (error) {
      state.latencies[host.id] = {
        text: "失败",
        className: "offline",
        title: String(error?.message || error).replace(/^Error:\s*/i, "") || "延迟检测失败",
      };
      renderHosts();
      toast(state.latencies[host.id].title, true);
    }
    return;
  }
  if (e.target.closest(".countryBtn")) {
    const country = await uiPrompt("输入两位国家缩写，例如 UK、US、CN；留空可恢复自动识别。", host.country || "");
    if (country === null) return;
    try {
      replaceSettings(await api().SetHostCountry(host.id, country));
      renderHosts();
      if (!country.trim()) detectHostCountries();
      else toast("国家已修改");
    } catch (x) { toast(String(x), true) }
    return;
  }
  if (e.target.closest(".starHost")) {
    try {
      replaceSettings(await api().SetHostStarred(host.id, !host.starred));
      renderHosts();
      toast(host.starred ? "已取消星标" : "已设为星标主机");
    } catch (x) { toast(String(x), true) }
    return;
  }
  if (e.target.closest(".restoreHost")) {
    try {
      replaceSettings(await api().RestoreHost(host.id));
      renderHosts();
      toast("主机已恢复");
    } catch (x) { toast(String(x), true) }
    return;
  }
  if (e.target.closest(".permanentDeleteHost")) {
    if (!(await uiConfirm(`永久删除主机“${host.name || host.address}”？\n\n该操作无法恢复，只清除软件本地记录，不会删除远端服务器配置。`, "永久删除主机"))) return;
    try {
      replaceSettings(await api().PermanentDeleteHost(host.id));
      discardSession(host.id);
      state.hostSelection.delete(host.id);
      delete state.vpsStatuses[host.id];
      clearShellVPSStatusCache(host.vpsAlias);
      state.expandedHosts.delete(host.id);
      state.vpsOverview = await window.go.nodemode.App.GetOverview();
      $("#nodeModeFrame").contentWindow?.markNodeModeDirty?.();
      renderHosts();
      toast("主机已永久删除");
    } catch (x) { toast(String(x), true) }
    return;
  }
  if (host.deleted) return;
  if (e.target.closest(".saveTemporaryHost")) {
    openHost(host);
    return;
  }
  const closeTemporaryButton = e.target.closest(".closeTemporaryHost");
  if (closeTemporaryButton) {
    const returnToHostList = !$("#hostDrawer").classList.contains("hidden");
    closeTemporaryButton.disabled = true;
    closeTemporaryButton.textContent = "关闭中";
    try {
      if (state.sessions[host.id]) {
        if (state.selected !== host.id) selectHost(host.id);
        await closeSession();
      } else {
        await releaseTemporaryHost(host.id);
        renderHosts();
        renderSessionTabs();
        toast("临时主机已关闭");
      }
      if (returnToHostList) $("#hostDrawer").classList.remove("hidden");
    } catch (x) {
      if (returnToHostList) $("#hostDrawer").classList.remove("hidden");
      closeTemporaryButton.disabled = false;
      closeTemporaryButton.textContent = "关闭";
      toast(String(x), true);
    }
    return;
  }
  if (e.target.closest(".deleteHost")) {
    if (!(await uiConfirm(`确认删除主机“${host.name || host.address}”？`, "删除主机"))) return;
    try {
      replaceSettings(await api().DeleteHost(host.id));
      discardSession(host.id);
      delete state.vpsStatuses[host.id];
      clearShellVPSStatusCache(host.vpsAlias);
      state.expandedHosts.delete(host.id);
      state.vpsOverview = await window.go.nodemode.App.GetOverview();
      $("#nodeModeFrame").contentWindow?.markNodeModeDirty?.();
      renderHosts();
      toast("主机已移入已删除，本地节点展示已清除");
    } catch (x) { toast(String(x), true) }
    return;
  }
  if (e.target.closest(".connectHost")) {
    selectHost(host.id);
    if ((host.protocol || "ssh").toLowerCase() === "rdp") {
      const result = await connectHostById(host.id);
      if (result?.status === "failed") toast(result.error || "RDP 连接失败", true);
      return;
    }
    return sessionFor(host.id).connected ? undefined : connect();
  }
  if (e.target.closest(".editHost")) {
    openHost(host);
    return;
  }
  if (e.target.closest(".refreshVpsStatus")) {
    await refreshVPSStatus(host, true);
    return;
  }
  const vpsActionButton = e.target.closest("[data-vps-action]");
  if (vpsActionButton) {
    if (!host.hostKey) {
      toast("请先连接主机、测试连接或手动刷新，确认 SSH 指纹后再操作", true);
      return;
    }
    const action = vpsActionButton.dataset.vpsAction;
    vpsActionButton.disabled = true;
    try {
      const { alias } = await ensureHostVPS(host, action !== "syncAllJson");
      await openNodeAction(action, alias);
    } catch (x) {
      toast(`无法启用节点管理：${String(x)}`, true);
    } finally {
      vpsActionButton.disabled = false;
    }
    return;
  }
  if (state.expandedHosts.has(host.id)) state.expandedHosts.delete(host.id);
  else state.expandedHosts.add(host.id);
  renderHosts();
};
let bindCredentialToHostOnSave = false;
function activeHostsForCredential(credentialId) {
  if (!credentialId) return [];
  return (state.settings?.hosts || []).filter((h) => !h.deleted && h.credentialId === credentialId);
}
function renderCredentials() {
  const items = state.settings.credentials || [];
  const keyword = ($("#credentialSearch")?.value || "").trim().toLowerCase();
  const typeFilter = $("#credentialTypeFilter")?.value || "all";
  const filtered = items.filter((c) => {
    if (typeFilter !== "all" && c.type !== typeFilter) return false;
    if (!keyword) return true;
    const haystack = [c.name, c.remark, c.keyType, c.keyFingerprint, c.keyFileName].join(" ").toLowerCase();
    return haystack.includes(keyword);
  });
  const countEl = $("#credentialCount");
  if (countEl) {
    countEl.textContent = items.length
      ? (filtered.length === items.length ? `共 ${items.length} 项` : `匹配 ${filtered.length} / ${items.length} 项`)
      : "暂无凭证";
  }
  $("#credentialList").innerHTML = filtered.length
    ? filtered
        .map((c) => {
          const usedHosts = activeHostsForCredential(c.id);
          const usedNames = usedHosts.map((h) => h.name || h.address).join("、");
          const meta = [
            c.type === "key" ? (c.keyType || "SSH 密钥") : "登录密码",
            c.type === "key" && c.keyFingerprint ? c.keyFingerprint : "",
            c.remark || "",
          ]
            .filter(Boolean)
            .join(" · ");
          const copyBtn = c.type === "password"
            ? `<button class="credential-copy" data-id="${esc(c.id)}" type="button" title="复制密码">复制密码</button>`
            : (c.keyFingerprint ? `<button class="credential-copy" data-id="${esc(c.id)}" type="button" title="复制密钥指纹">复制指纹</button>` : "");
          const usageBadge = usedHosts.length
            ? `<em class="credential-used-badge" title="正在使用：${esc(usedNames)}">已绑定 ${usedHosts.length} 台</em>`
            : '<em class="credential-unused-badge">未绑定</em>';
          return `<div class="credential-item" data-id="${esc(c.id)}"><button class="credential-item-main" data-id="${esc(c.id)}" type="button"><span class="credential-item-title"><i class="credential-type-badge ${c.type === "key" ? "key" : "password"}">${c.type === "key" ? "私钥" : "密码"}</i><b>${esc(c.name)}</b>${usageBadge}</span><small>${esc(meta)}</small></button><span class="credential-item-actions">${copyBtn}<button class="credential-edit" data-id="${esc(c.id)}" type="button">编辑</button></span></div>`;
        })
        .join("")
    : `<div class="host-empty">${items.length ? "没有匹配的凭证" : "暂无凭证，可点击下方按钮新增密码或导入私钥"}</div>`;
}
function updateCredentialFields() {
  const key = $("#credentialType").value === "key";
  $("#credentialPasswordField").classList.toggle("hidden", key);
  $("#credentialKeyField").classList.toggle("hidden", !key);
  $("#credentialPassphraseField").classList.toggle("hidden", !key);
}
function credentialEditId() {
  return $("#credentialEditId");
}
function openCredential(c = {}, bindToHost = false) {
  bindCredentialToHostOnSave = Boolean(bindToHost);
  credentialEditId().value = c.id || "";
  $("#credentialName").value = c.name || "";
  $("#credentialType").value = c.type || "password";
  $("#credentialPassword").value = c.password || "";
  $("#credentialKeyContent").value = c.keyContent || "";
  $("#credentialPassphrase").value = c.keyPassphrase || "";
  $("#credentialRemark").value = c.remark || "";
  $("#credentialKeyFile").textContent = c.keyFileName || (c.id ? "私钥内容（可重新导入或直接修改）" : "粘贴或导入私钥文件");
  $("#credentialKeyMeta").textContent = [c.keyType, c.keyFingerprint].filter(Boolean).join(" · ") || "支持 OpenSSH、RSA、ECDSA、Ed25519 私钥";
  $("#credentialTitle").textContent = c.id ? "编辑凭证" : (c.type === "key" ? "新增 SSH 私钥凭证" : "新增密码凭证");
  $("#deleteCredential").classList.toggle("hidden", !c.id);
  resetPasswordVisibility("credentialPassword", "credentialPassphrase");
  const usedHosts = c.id ? activeHostsForCredential(c.id) : [];
  const usageBox = $("#credentialUsageInfo");
  if (usedHosts.length) {
    const names = usedHosts.slice(0, 5).map((h) => h.name || h.address).join("、");
    usageBox.textContent = `当前有 ${usedHosts.length} 台主机正在使用该凭证：${names}${usedHosts.length > 5 ? " 等" : ""}`;
    usageBox.classList.remove("hidden");
  } else {
    usageBox.textContent = "";
    usageBox.classList.add("hidden");
  }
  updateCredentialFields();
  $("#credentialEditor").classList.remove("hidden");
  requestAnimationFrame(() => $("#credentialName").focus());
}
async function importKeyIntoCredentialEditor(existing = null) {
  const imported = await api().SelectPrivateKeyFile();
  if (!imported?.content) return;
  let passphrase = "";
  if (imported.encrypted) {
    passphrase = await uiPrompt("该私钥已加密，请输入私钥口令", "", true);
    if (!passphrase) return;
  }
  if (existing) {
    $("#credentialKeyFile").textContent = imported.fileName;
    $("#credentialKeyContent").value = imported.content;
    $("#credentialPassphrase").value = passphrase;
    if (!$("#credentialName").value.trim()) $("#credentialName").value = imported.fileName;
    $("#credentialKeyMeta").textContent = imported.encrypted ? "已更换加密私钥，保存时验证口令" : "已导入新私钥，保存后生效";
  } else {
    openCredential({
      type: "key",
      name: imported.fileName,
      keyFileName: imported.fileName,
      keyContent: imported.content,
      keyPassphrase: passphrase,
    });
    $("#credentialKeyMeta").textContent = imported.encrypted ? "加密私钥，保存时验证口令" : "私钥已识别，保存后可直接使用";
  }
}
$("#credentialBtn").onclick = () => {
  renderCredentials();
  $("#credentialLibrary").classList.remove("hidden");
};
$("#openCredentialLibraryFromHost").onclick = () => {
  renderCredentials();
  $("#credentialLibrary").classList.remove("hidden");
};
$("#saveHostAsCredentialBtn").onclick = () => {
  const auth = $("#authType").value;
  const hostName = $("#name").value.trim() || $("#address").value.trim() || "主机凭证";
  if (auth.startsWith("key")) {
    openCredential({ type: "key", name: `${hostName} 密钥` }, true);
  } else {
    openCredential({ type: "password", name: `${hostName} 密码`, password: $("#password").value }, true);
  }
};
$("#closeCredentials").onclick = () =>
  $("#credentialLibrary").classList.add("hidden");
$("#credentialSearch").oninput = renderCredentials;
$("#credentialTypeFilter").onchange = renderCredentials;
$("#addPasswordCredential").onclick = () => openCredential({ type: "password" });
$("#addKeyCredential").onclick = () => openCredential({ type: "key" });
$("#importKeyCredential").onclick = async () => {
  try {
    await importKeyIntoCredentialEditor(null);
  } catch (x) { if (x) toast(String(x), true) }
};
$("#reimportCredentialKey").onclick = async () => {
  try {
    await importKeyIntoCredentialEditor(true);
  } catch (x) { if (x) toast(String(x), true) }
};
$("#cancelCredential").onclick = () => {
  bindCredentialToHostOnSave = false;
  $("#credentialEditor").classList.add("hidden");
};
$("#credentialList").onclick = async (e) => {
  const copyBtn = e.target.closest(".credential-copy");
  if (copyBtn) {
    const c = (state.settings.credentials || []).find((x) => x.id === copyBtn.dataset.id);
    if (!c) return;
    const text = c.type === "password" ? c.password : c.keyFingerprint;
    if (!text) return toast("无可复制内容", true);
    await navigator.clipboard.writeText(text);
    toast(c.type === "password" ? "已复制密码到剪贴板" : "已复制密钥指纹");
    return;
  }
  const trigger = e.target.closest(".credential-item-main, .credential-edit");
  if (trigger) {
    const found = (state.settings.credentials || []).find((c) => c.id === trigger.dataset.id);
    if (found) openCredential(found);
  }
};
$("#credentialForm").onsubmit = async (e) => {
  e.preventDefault();
  const type = $("#credentialType").value;
  const beforeIDs = new Set((state.settings.credentials || []).map((item) => item.id));
  const c = {
    id: credentialEditId().value,
    name: $("#credentialName").value.trim(),
    type,
    password: type === "password" ? $("#credentialPassword").value : "",
    keyContent: type === "key" ? $("#credentialKeyContent").value : "",
    keyPassphrase: type === "key" ? $("#credentialPassphrase").value : "",
    keyFileName: type === "key" ? $("#credentialKeyFile").textContent : "",
    remark: $("#credentialRemark").value.trim(),
  };
  try {
    replaceSettings(await api().SaveCredential(c));
    $("#credentialEditor").classList.add("hidden");
    renderCredentials();
    let targetCredentialID = $("#credentialId").value;
    if (bindCredentialToHostOnSave) {
      const created = (state.settings.credentials || []).find((item) => !beforeIDs.has(item.id));
      if (created) targetCredentialID = created.id;
      bindCredentialToHostOnSave = false;
    }
    fillCredentialSelect(targetCredentialID);
    toast("凭证已保存");
  } catch (x) {
    toast(String(x), true);
  }
};
$("#deleteCredential").onclick = async () => {
  const id = credentialEditId().value;
  if (!(await uiConfirm("确认删除该凭证？"))) return;
  try {
    replaceSettings(await api().DeleteCredential(id));
    $("#credentialEditor").classList.add("hidden");
    renderCredentials();
    fillCredentialSelect();
    toast("凭证已删除");
  } catch (x) {
    const msg = String(x);
    if (/正在被/.test(msg)) {
      if (!(await uiConfirm(`${msg}。\n\n是否一键解除主机关联并强制删除该凭证？`, "解绑并删除凭证"))) return;
      try {
        replaceSettings(await api().UnbindAndDeleteCredential(id));
        $("#credentialEditor").classList.add("hidden");
        renderCredentials();
        fillCredentialSelect();
        renderHosts();
        toast("已解绑主机并删除凭证");
      } catch (unbindErr) {
        toast(String(unbindErr), true);
      }
      return;
    }
    toast(msg, true);
  }
};
$("#protocol").onchange = () => updateProtocol(true);
$("#proxyEnabled").onchange = () => {
  if ($("#protocol").value === "rdp" && $("#proxyEnabled").checked) {
    $("#rdpJumpHostId").value = "";
    $("#rdpGatewayUsage").value = "0";
  } else if ($("#protocol").value !== "rdp" && $("#proxyEnabled").checked) {
    $("#sshJumpHostId").value = "";
  }
  updateSSHJumpFields();
  updateRDPOptionFields();
  updateProxyFields();
};
$("#authType").onchange = updateAuthFields;
$("#credentialId").onchange = updateCredentialUsage;
$("#resetHostKey").onclick = async () => {
  const id = $("#id").value;
  if (!id || !(await uiConfirm("仅当服务器重装或确认主机密钥确实变化时使用。重置后，下次连接将重新固定服务器指纹。", "重新信任 SSH 主机密钥"))) return;
  try {
    replaceSettings(await api().ResetHostKey(id));
    $("#hostKeyText").textContent = "";
    $("#hostKeyTrust").classList.add("hidden");
    toast("主机密钥已重置，下次连接将重新信任");
  } catch (error) {
    toast(String(error), true);
  }
};
document
  .querySelectorAll(".host-editor-nav button")
  .forEach(
    (button) =>
      (button.onclick = () => showHostSection(button.dataset.section)),
  );
$("#testHostConfig").onclick = async () => {
  const button = $("#testHostConfig");
  const revision = state.hostEditorRevision;
  const host = hostFormValue();
  setHostEditorBusy(true);
  button.textContent = "正在连接…";
  $("#hostTestResult").textContent = "";
  try {
    const result = await api().TestHostConfig(host);
    if (host.id) {
      try {
        replaceSettings(await api().GetSettings());
        if (!hostEditorOperationCurrent(revision)) return;
        const trusted = (state.settings.hosts || []).find((item) => item.id === host.id);
        $("#hostKeyText").textContent = trusted?.hostKey ? `已固定 SSH 主机密钥：${trusted.hostKey}` : "";
        $("#hostKeyTrust").classList.toggle("hidden", !trusted?.hostKey);
      } catch {}
    }
    if (!hostEditorOperationCurrent(revision)) return;
    $("#hostTestResult").textContent = result.output.trim();
    toast("测试连接成功");
  } catch (e) {
    if (!hostEditorOperationCurrent(revision)) return;
    $("#hostTestResult").textContent = String(e);
    toast(String(e), true);
  } finally {
    if (hostEditorOperationCurrent(revision)) {
      setHostEditorBusy(false);
      button.textContent = $("#protocol").value === "rdp" ? "测试端口" : "测试连接";
    }
  }
};
$("#hostForm").onsubmit = async (e) => {
  e.preventDefault();
  const h = hostFormValue();
  const revision = state.hostEditorRevision;
  const button = $("#hostForm button.primary");
  const originalText = button.textContent;
  setHostEditorBusy(true);
  try {
    let vpsResult = null;
    const returnWorkspace = state.hostEditorFromWorkspace;
    const previousHost = h.id ? (state.settings.hosts || []).find((host) => host.id === h.id) : null;
    const previousVPSAlias = String(previousHost?.vpsAlias || "").trim();
    // 节点模式以 VPS 别名作为节点归属键。已绑定 SSH 主机改名时同步改名该键，
    // 否则主机列表虽显示新名称，节点聚合仍会停留在旧服务器名下。
    if ((h.protocol || "ssh") === "ssh" && previousVPSAlias &&
      String(previousHost?.name || "").trim() !== String(h.name || "").trim()) {
      h.vpsAlias = String(h.name || "").trim();
    }
    const previousIds = new Set((state.settings.hosts || []).map((host) => host.id));
    replaceSettings(await api().SaveHost(h));
    const savedHost = (state.settings.hosts || []).find((item) => item.id === h.id) ||
      (state.settings.hosts || []).find((item) => !previousIds.has(item.id));
    if (savedHost && (savedHost.protocol || "ssh") === "ssh") {
      if (savedHost.vpsAlias) {
        await syncHostVPS(savedHost, savedHost.vpsAlias, previousVPSAlias);
        if (hostEditorOperationCurrent(revision)) button.textContent = savedHost.hostKey ? "刷新状态…" : "保存节点配置…";
        vpsResult = await refreshVPSStatus(savedHost, false, false, { syncRemote: false, reloadOverview: false });
      } else {
        if (hostEditorOperationCurrent(revision)) button.textContent = "保存节点配置…";
        const initialized = await ensureHostVPS(savedHost, Boolean(savedHost.hostKey));
        const boundHost = (state.settings.hosts || []).find((item) => item.id === savedHost.id) || savedHost;
        vpsResult = initialized.runtimeState || (!boundHost.hostKey ? markHostAwaitingTrust(boundHost, initialized.alias) : null);
      }
    }
    if (!hostEditorOperationCurrent(revision)) {
      renderHosts();
      if (savedHost) probeHosts([savedHost.id]);
      return;
    }
    const current = selected();
    if (current) {
      const username =
        current.username ||
        ((current.protocol || "ssh") === "rdp" ? "administrator" : "root");
      showSideIdentity(
        username,
        current.name || current.address || "未命名主机",
      );
      if (state.connected) {
        $("#sideAddress").textContent = current.address;
        $("#latencyTarget").textContent = sessionLatencyTargetText(current);
      }
    }
    renderSessionTabs();
    $("#hostEditor").classList.add("hidden");
    renderHosts();
    if (savedHost) probeHosts([savedHost.id]);
    if (!returnWorkspace) $("#hostDrawer").classList.remove("hidden");
    state.hostEditorFromWorkspace = false;
    if (returnWorkspace) refreshVisibleMonitor();
    const awaitingTrust = vpsResult?.status === "untrusted";
    const vpsFailed = vpsResult?.status === "ssh_failed" || vpsResult?.status === "error" || /失败/.test(vpsResult?.message || "");
    toast(awaitingTrust ? "主机已保存，首次连接或手动刷新时确认指纹" : vpsFailed ? `主机已保存，但连接验证失败：${vpsResult.message || vpsStatusText(vpsResult.status)}` : vpsResult ? "主机已保存并刷新状态" : "主机已保存", vpsFailed);
  } catch (x) {
    if (hostEditorOperationCurrent(revision)) toast(String(x), true);
  } finally {
    if (hostEditorOperationCurrent(revision)) {
      setHostEditorBusy(false);
      button.textContent = originalText;
    }
  }
};
$("#filesTab").onclick = () => showFileArea(true);
$("#presetCommandsTab").onclick = () => showFileArea(false);
$("#addQuick").onclick = () => openQuickCommand();
$("#addQueueQuick").onclick = () => openQuickCommand({}, "queue");
$("#presetCommands").onclick = async (e) => {
  const button = e.target.closest(".preset-command[data-id]");
  if (!button) return;
  const item = (state.settings.quickCommands || []).find(
    (x) => x.id === button.dataset.id,
  );
  if (!item) return;
  if (item.type === "queue") {
    if (!state.connected) return toast("请先连接 SSH", true);
    if (state.queueRun) return toast("已有队列正在执行", true);
    const commands = item.command.split(/\r?\n/).map(command => command.trim()).filter(Boolean);
    if (!commands.length) return toast("队列快捷命令没有可执行步骤", true);
    const targets = commandTargetIds().map((id) => ({
      id,
      epoch: state.sessions[id].connectionEpoch,
    }));
    if (!targets.length) return toast("没有已连接的 SSH 会话", true);
    if (
      targets.length > 1 &&
      !(await uiConfirm(
        `队列快捷命令“${item.name}”包含 ${commands.length} 个步骤，将在 ${targets.length} 台主机执行。确认继续？`,
        "执行队列快捷命令",
      ))
    ) return;
    if (state.queueRun) return toast("已有队列正在执行", true);
    const run = {
      id: window.crypto?.randomUUID?.() || `queue-${Date.now()}-${Math.random().toString(36).slice(2)}`,
      commandId: item.id,
      commandName: item.name,
      cancelled: false,
      currentStep: 0,
      totalSteps: commands.length,
      targetCount: targets.length,
      completedUnits: 0,
      totalUnits: commands.length * targets.length,
      timeoutSeconds: Math.min(3600, Math.max(1, Number(item.timeoutSeconds) || 300)),
      retries: Math.min(3, Math.max(0, Number(item.retries) || 0)),
    };
    state.queueRun = run;
    updateQueueRunUI();
    try {
      await api().BeginQueue(run.id);
      for (let index = 0; index < commands.length; index++) {
        if (run.cancelled) throw new Error("队列已停止");
        run.currentStep = index + 1;
        updateQueueRunUI();
        toast(`队列快捷命令 ${item.name}：执行 ${index + 1}/${commands.length}${targets.length > 1 ? ` · ${targets.length} 台` : ""}`);
        await runQueueStepForTargets(commands[index], targets, index + 1, run);
      }
      toast(`队列快捷命令执行完成：${item.name}${targets.length > 1 ? ` · ${targets.length} 台` : ""}`);
    } catch (x) {
      toast(run.cancelled ? "队列已停止" : String(x), true);
    } finally {
      await api().FinishQueue(run.id).catch(() => {});
      if (state.queueRun === run) state.queueRun = null;
      updateQueueRunUI();
    }
    return;
  }
  $("#command").value = item.command;
  $("#command").focus();
  toast(`已填入：${item.name}`);
};
$("#presetCommands").oncontextmenu = (e) => {
  const button = e.target.closest(".preset-command[data-id]");
  if (!button || button.disabled) return;
  const item = (state.settings.quickCommands || []).find(
    (command) => command.id === button.dataset.id,
  );
  if (!item) return;
  e.preventDefault();
  openQuickCommand(item);
};
$("#cancelQuickCommand").onclick = closeQuickCommandModal;
$("#quickCommandModal").onkeydown = (event) => {
  if (event.key !== "Escape" || state.quickCommandBusy) return;
  event.preventDefault();
  closeQuickCommandModal();
};
$("#deleteQuickCommand").onclick = async () => {
  if (state.quickCommandBusy) return;
  const id = $("#quickCommandId").value;
  const item = (state.settings.quickCommands || []).find((command) => command.id === id);
  if (!item) return;
  const label = item.type === "queue" ? "队列快捷命令" : "快捷命令";
  if (!(await uiConfirm(`确认删除${label}“${item.name}”？`, `删除${label}`))) return;
  setQuickCommandBusy(true, "delete");
  try {
    replaceSettings(await api().DeleteQuickCommand(id));
    $("#quickCommandModal").classList.add("hidden");
    renderPresetCommands();
    toast(`${label}已删除`);
  } catch (error) {
    toast(String(error), true);
  } finally {
    setQuickCommandBusy(false);
  }
};
$("#stopQueue").onclick = async () => {
  const run = state.queueRun;
  if (!run || run.cancelled) return;
  run.cancelled = true;
  updateQueueRunUI();
  try {
    await api().StopQueue(run.id);
  } catch (error) {
    toast(`停止队列失败：${String(error)}`, true);
  }
};
$("#addQueueStep").onclick = () => {
  const values = queueStepValues();
  values.push("");
  renderQueueSteps(values, values.length - 1);
};
$("#queueSteps").onclick = (event) => {
  const button = event.target.closest("button[data-action]");
  const step = button?.closest(".queue-step");
  if (!button || !step) return;
  const values = queueStepValues();
  const index = Number(step.dataset.index);
  let focusIndex = index;
  switch (button.dataset.action) {
    case "up":
      if (index <= 0) return;
      [values[index - 1], values[index]] = [values[index], values[index - 1]];
      focusIndex = index - 1;
      break;
    case "down":
      if (index >= values.length - 1) return;
      [values[index + 1], values[index]] = [values[index], values[index + 1]];
      focusIndex = index + 1;
      break;
    case "delete":
      values.splice(index, 1);
      if (!values.length) values.push("");
      focusIndex = Math.min(index, values.length - 1);
      break;
    default:
      return;
  }
  renderQueueSteps(values, focusIndex);
};
$("#queueSteps").onkeydown = (event) => {
  if (event.key !== "Enter" || event.isComposing || !event.target.matches("input")) return;
  event.preventDefault();
  const values = queueStepValues();
  const index = Number(event.target.closest(".queue-step").dataset.index);
  values.splice(index + 1, 0, "");
  renderQueueSteps(values, index + 1);
};
$("#quickCommandForm").onsubmit = async (e) => {
  e.preventDefault();
  if (state.quickCommandBusy) return;
  setQuickCommandBusy(true, "save");
  try {
    const editing = Boolean($("#quickCommandId").value);
    const queue = $("#quickCommandType").value === "queue";
    const label = queue ? "队列快捷命令" : "快捷命令";
    const command = queue ? queueStepValues().map(value => value.trim()).filter(Boolean).join("\n") : $("#quickCommandValue").value.trim();
    if (!command) throw new Error("请至少填写一条命令");
    replaceSettings(await api().SaveQuickCommand({
      id: $("#quickCommandId").value,
      name: $("#quickCommandName").value.trim(),
      command,
      type: $("#quickCommandType").value,
      timeoutSeconds: queue ? Number($("#queueTimeout").value) || 300 : 0,
      retries: queue ? Number($("#queueRetries").value) || 0 : 0,
    }));
    $("#quickCommandModal").classList.add("hidden");
    renderPresetCommands();
    toast(editing ? `${label}已更新` : `${label}已保存`);
  } catch (x) {
    toast(String(x), true);
  } finally {
    setQuickCommandBusy(false);
  }
};
$("#windowMin").onclick = () => window.runtime?.WindowMinimise?.();
$("#windowMax").onclick = () => window.runtime?.WindowToggleMaximise?.();
$("#windowClose").onclick = () => window.runtime?.Quit?.();
$("#hostTab").onclick = () =>
  openSettings().catch((e) => toast(String(e), true));
$("#sessionTabs").onclick = (e) => {
  if (e.target.closest(".session-tab.empty")) {
    $("#hostDrawer").classList.remove("hidden");
    return;
  }
  const tab = e.target.closest(".session-tab[data-id]");
  if (!tab) return;
  if (e.target.closest(".session-close")) {
    if (tab.dataset.id !== state.selected) selectHost(tab.dataset.id);
    return closeSession().catch((x) => toast(String(x), true));
  }
  selectHost(tab.dataset.id);
};
$("#batchConnect").onclick = batchConnectSelected;
$("#batchConcurrency").value = String(state.batchConcurrency);
$("#batchConcurrency").onchange = (event) => {
  const value = Math.min(20, Math.max(1, Number(event.target.value) || 10));
  state.batchConcurrency = value;
  event.target.value = String(value);
  localStorage.setItem("batchConnectConcurrency", String(value));
};
$("#newSession").onclick = () => showHostListPage(false);
$("#commandForm").onsubmit = (e) => {
  e.preventDefault();
  const c = $("#command").value.trim();
  if (c) {
    runCommand(c);
    $("#command").value = "";
  } else if (state.selected) {
    enqueueTerminalInput(state.selected, "\r", false);
  }
  state.sessions[state.selected]?.terminal?.focus();
};
$("#command").onkeydown = (event) => {
  if (event.key !== "Enter" || event.shiftKey || event.isComposing || event.keyCode === 229) return;
  event.preventDefault();
  event.stopPropagation();
  $("#commandForm").requestSubmit();
};
function clearActiveTerminal() {
  const id = state.selected;
  const session = state.sessions[id];
  if (!session?.terminal) return;
  session.terminal.clear();
  if (session.connected) {
    enqueueTerminalInput(id, "\x0c", false);
  }
  session.terminal.focus();
}
$("#clearTerminalBtn").onclick = clearActiveTerminal;
$("#historyBtn").onclick = async () => {
  const id = state.selected;
  const session = state.sessions[id];
  const host = selected();
  if (!session?.connected || !host || String(host.protocol || "ssh").toLowerCase() !== "ssh") {
    return toast("请先连接 SSH", true);
  }
  const result = await enqueueTerminalInput(id, "history 50\r", false);
  if (result.sent) session.terminal?.focus();
};
$("#terminalSearchBtn").onclick = openTerminalSearch;
$("#closeTerminalSearch").onclick = () => closeTerminalSearch();
$("#terminalSearchForm").onsubmit = (event) => {
  event.preventDefault();
  searchCurrentTerminal("next");
};
$("#terminalSearchPrevious").onclick = () => searchCurrentTerminal("previous");
$("#terminalSearchInput").oninput = () => searchCurrentTerminal("next", true);
$("#terminalSearchInput").onkeydown = (event) => {
  if (event.key === "Enter" && event.shiftKey) {
    event.preventDefault();
    searchCurrentTerminal("previous");
  }
};
$("#terminalSearchModal").onclick = (event) => {
  if (event.target === $("#terminalSearchModal")) closeTerminalSearch();
};
$("#splitTerminalBtn").onclick = toggleTerminalSplit;
$("#cancelSplitTerminal").onclick = () =>
  $("#splitTerminalModal").classList.add("hidden");
$("#splitTerminalForm").onsubmit = (event) => {
  event.preventDefault();
  const second = $("#splitTerminalHost").value;
  if (
    !isConnectedSSHSession(state.selected) ||
    !isConnectedSSHSession(second) ||
    second === state.selected
  ) {
    return toast("分屏主机已断开，请重新选择", true);
  }
  state.splitHosts = [state.selected, second];
  $("#splitTerminalModal").classList.add("hidden");
  showTerminal(state.selected, true);
  updateTerminalToolButtons();
  toast(`已分屏：${hostDisplayName(state.selected)} / ${hostDisplayName(second)}`);
};
$("#splitTerminalModal").onclick = (event) => {
  if (event.target === $("#splitTerminalModal")) {
    $("#splitTerminalModal").classList.add("hidden");
  }
};
$("#portForwardBtn").onclick = openPortForwardDialog;
$("#closePortForward").onclick = () =>
  $("#portForwardModal").classList.add("hidden");
$("#portForwardKind").onchange = updatePortForwardFields;
$("#refreshPortForwards").onclick = () => refreshPortForwards();
$("#portForwardForm").onsubmit = (event) => {
  event.preventDefault();
  startPortForward();
};
$("#portForwardList").onclick = (event) => {
  const button = event.target.closest(".stopPortForward");
  if (button) stopPortForward(button);
};
$("#portForwardModal").onclick = (event) => {
  if (event.target === $("#portForwardModal")) {
    $("#portForwardModal").classList.add("hidden");
  }
};
function applicationShortcutBlocked(event) {
  if (document.querySelector(".modal:not(.hidden)")) return true;
  const target = event.target;
  if (!(target instanceof Element)) return false;
  if (target.closest(".xterm")) return false;
  return Boolean(
    target.closest('input, textarea, select, [contenteditable="true"]'),
  );
}
window.addEventListener("keydown", (event) => {
  if (event.repeat || applicationShortcutBlocked(event)) return;
  let handled = false;
  if (
    event.altKey &&
    !event.ctrlKey &&
    !event.shiftKey &&
    !event.metaKey &&
    /^[1-9]$/.test(event.key)
  ) {
    const id = Object.keys(state.sessions)[Number(event.key) - 1];
    if (id) {
      selectHost(id);
      handled = true;
    }
  } else if (
    event.ctrlKey &&
    event.shiftKey &&
    !event.altKey &&
    !event.metaKey
  ) {
    switch (event.key.toLowerCase()) {
      case "w":
        if (state.selected) {
          closeSession().catch((error) => toast(String(error), true));
          handled = true;
        }
        break;
      case "r":
        if (state.selected) {
          reconnectCurrent().catch((error) => toast(String(error), true));
          handled = true;
        }
        break;
      case "p":
        toggleTerminalSplit();
        handled = true;
        break;
    }
  }
  if (!handled) return;
  event.preventDefault();
  event.stopPropagation();
}, true);
window.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !$("#terminalSearchModal").classList.contains("hidden")) {
    event.preventDefault();
    closeTerminalSearch();
  } else if (event.ctrlKey && !event.shiftKey && !event.altKey && !event.metaKey && event.key.toLowerCase() === "l") {
    const session = state.sessions[state.selected];
    if (session?.terminal && monitorPageVisible()) {
      event.preventDefault();
      clearActiveTerminal();
    }
  }
});
$("#sessionLogBtn").onclick = toggleSSHSessionLogging;
$("#aboutBtn").onclick = async () => {
  $("#hostEditor").classList.add("hidden");
  $("#hostDrawer").classList.add("hidden");
  $("#settingsPage").classList.add("hidden");
  $("#aboutPage").classList.remove("hidden");
  try {
    const info = await api().GetAppInfo();
    $("#aboutVersion").textContent = info.version;
    $("#aboutFramework").textContent = info.framework;
    $("#aboutPlatform").textContent = `${info.platform} / ${info.arch}`;
    $("#aboutGo").textContent = info.goVersion;
    $("#aboutDataDir").textContent = info.dataDir;
    $("#aboutLogDir").textContent = info.logDir;
  } catch (e) {
    toast(String(e), true);
  }
};
$("#closeAbout").onclick = () => {
  $("#aboutPage").classList.add("hidden");
  renderHosts();
  $("#hostDrawer").classList.remove("hidden");
};
$("#openLogs").onclick = () =>
  api()
    .OpenLogDirectory()
    .catch((e) => toast(String(e), true));
$("#exportLogs").onclick = () =>
  api()
    .ExportLogs()
    .then(() => toast("日志已导出"))
    .catch((e) => {
      if (e) toast(String(e), true);
    });
$("#closeSettings").onclick = () => {
  $("#settingsPage").classList.add("hidden");
  refreshVisibleMonitor();
};
$("#manageAI").onclick = () => openAIProvider();
$("#settingsAIProviders").onclick = async (e) => {
  const button = e.target.closest("button[data-provider-action]");
  if (!button) return;
  const provider = (state.settings.aiProviders || []).find(
    (item) => item.id === button.dataset.id,
  );
  if (!provider) return;
  if (button.dataset.providerAction === "edit") {
    openAIProvider(provider);
    return;
  }
  if (
    button.dataset.providerAction !== "use" ||
    provider.id === state.settings.currentProviderId
  )
    return;
  try {
    await runAIConfigTask("正在切换 AI 供应商…", async () => {
      await api().SelectAIProvider(provider.id);
      state.settings.currentProviderId = provider.id;
    });
    toast(`已使用 ${provider.name}`);
  } catch (error) {
    toast(String(error), true);
  }
};
$("#selectDownloadDirectory").onclick = async () => {
  try {
    const path = await api().SelectDownloadDirectory();
    if (path) $("#downloadDirectory").value = path;
  } catch (e) {
    if (e) toast(String(e), true);
  }
};
$("#lockStartupSetting").onchange = updateSecurityUI;
$("#protectExportSetting").onchange = updateSecurityUI;
[
  "currentSecurityPassword",
  "newSecurityPassword",
  "confirmSecurityPassword",
].forEach((id) => {
  $(`#${id}`).oninput = () => setSecurityMessage();
});
$("#globalSettingsForm").onsubmit = async (e) => {
  e.preventDefault();
  const configured = state.securityBaseline.configured;
  const lockOnStartup = $("#lockStartupSetting").checked;
  const protectExport = $("#protectExportSetting").checked;
  const protectionEnabled = lockOnStartup || protectExport;
  const switchesChanged =
    lockOnStartup !== state.securityBaseline.lockOnStartup ||
    protectExport !== state.securityBaseline.protectExport;
  const removeSecurity = configured && !protectionEnabled;
  const currentPassword = $("#currentSecurityPassword").value;
  const enteredNewPassword = $("#newSecurityPassword").value;
  const enteredConfirmation = $("#confirmSecurityPassword").value;
  const ignoreNewPassword = removeSecurity || (!configured && !protectionEnabled);
  const newPassword = ignoreNewPassword ? "" : enteredNewPassword;
  const confirmPassword = ignoreNewPassword ? "" : enteredConfirmation;
  const changingPassword = Boolean(newPassword || confirmPassword);

  setSecurityMessage();
  if (!configured && protectionEnabled) {
    const error = validateNewSecurityPassword(newPassword, confirmPassword, true);
    if (error) {
      setSecurityMessage(error, true);
      $("#newSecurityPassword").focus();
      return;
    }
  } else if (configured && changingPassword) {
    const error = validateNewSecurityPassword(newPassword, confirmPassword, false);
    if (error) {
      setSecurityMessage(error, true);
      $("#newSecurityPassword").focus();
      return;
    }
  }
  if (configured && (switchesChanged || changingPassword) && !currentPassword) {
    setSecurityMessage("请输入当前密保以确认修改", true);
    $("#currentSecurityPassword").focus();
    return;
  }
  if (
    removeSecurity &&
    !(await uiConfirm(
      "停用后，启动和导出将不再验证密保，现有密保也会被删除。确定继续吗？",
      "停用软件密保",
    ))
  )
    return;

  const button =
    e.submitter || $("#globalSettingsForm footer .primary");
  const buttonText = button.textContent;
  button.disabled = true;
  button.textContent = "保存中…";
  try {
    replaceSettings(await api().SaveGlobalSettings({
      theme: "dark",
      downloadDirectory: $("#downloadDirectory").value,
      autoStart: $("#autoStartSetting").checked,
      lockOnStartup,
      protectExport,
      aiPresetPrompt: $("#aiPresetPrompt").value,
      currentPassword,
      newPassword,
      confirmPassword,
      removeSecurity,
    }));
    applyTheme(state.settings.theme);
    state.securityBaseline = {
      configured: removeSecurity
        ? false
        : configured || Boolean(newPassword),
      lockOnStartup,
      protectExport,
    };
    $("#currentSecurityPassword").value = "";
    $("#newSecurityPassword").value = "";
    $("#confirmSecurityPassword").value = "";
    resetPasswordVisibility(
      "currentSecurityPassword",
      "newSecurityPassword",
      "confirmSecurityPassword",
    );
    updateSecurityUI();
    toast("全局设置已保存");
  } catch (x) {
    const message = String(x?.message || x).replace(/^Error:\s*/i, "");
    if (/密保|密码|尝试|稍后/.test(message)) {
      setSecurityMessage(message || "密保设置保存失败", true);
    } else {
      toast(message || "全局设置保存失败", true);
    }
  } finally {
    button.disabled = false;
    button.textContent = buttonText;
  }
};

let unlockRetryTimer = 0;
function startUnlockRetryDelay(seconds, message) {
  window.clearInterval(unlockRetryTimer);
  const input = $("#unlockPassword");
  const button = $("#unlockSubmit");
  const until = Date.now() + seconds * 1000;
  input.disabled = true;
  button.disabled = true;
  const update = () => {
    const remaining = Math.max(0, Math.ceil((until - Date.now()) / 1000));
    if (remaining > 0) {
      $("#unlockError").textContent = `${message}（${remaining} 秒）`;
      button.textContent = `${remaining} 秒后重试`;
      return;
    }
    window.clearInterval(unlockRetryTimer);
    unlockRetryTimer = 0;
    input.disabled = false;
    button.disabled = false;
    button.textContent = "解锁";
    input.focus();
    input.select();
  };
  update();
  unlockRetryTimer = window.setInterval(update, 1000);
}

$("#unlockPassword").oninput = () => {
  if (!$("#unlockPassword").disabled) $("#unlockError").textContent = "";
};
$("#unlockForm").onsubmit = async (e) => {
  e.preventDefault();
  const input = $("#unlockPassword");
  const button = $("#unlockSubmit");
  if (button.disabled) return;
  $("#unlockError").textContent = "";
  input.disabled = true;
  button.disabled = true;
  button.textContent = "验证中…";
  let retryDelayed = false;
  try {
    const result = await api().VerifySecurityPassword(input.value);
    const success = typeof result === "boolean" ? result : Boolean(result?.success);
    if (!success) {
      const retryAfter = Math.max(0, Number(result?.retryAfterSeconds) || 0);
      let message = result?.message || "密保不正确，请重试";
      const remainingAttempts = Number(result?.remainingAttempts);
      if (
        !retryAfter &&
        !message.includes("还可尝试") &&
        Number.isFinite(remainingAttempts) &&
        remainingAttempts >= 0
      ) {
        message += `（还可尝试 ${remainingAttempts} 次）`;
      }
      if (retryAfter) {
        retryDelayed = true;
        startUnlockRetryDelay(retryAfter, message);
      } else {
        $("#unlockError").textContent = message;
        input.disabled = false;
        input.focus();
        input.select();
      }
      return;
    }
    await loadApp();
    window.clearInterval(unlockRetryTimer);
    unlockRetryTimer = 0;
    $("#unlockModal").classList.add("hidden");
    input.value = "";
    resetPasswordVisibility("unlockPassword");
    toast("软件已解锁");
  } catch (error) {
    $("#unlockError").textContent = String(error?.message || error)
      .replace(/^Error:\s*/i, "") || "验证失败，请重试";
    input.disabled = false;
    input.focus();
    input.select();
  } finally {
    if (!retryDelayed) {
      input.disabled = false;
      button.disabled = false;
      button.textContent = "解锁";
    }
  }
};
async function reconnectCurrent() {
  if (!state.selected) return toast("请先选择主机", true);
  const id = state.selected;
  const session = sessionFor(id);
  if (session.connecting || session.closing) return;
  session.autoReconnect = false;
  cancelSessionReconnect(session);
  if ((selected().protocol || "ssh") === "ssh") {
    session.closing = true;
    advanceConnectionEpoch(session, id);
    session.connected = false;
    state.connected = false;
    stopMonitor();
    try {
      await api().CloseTerminal(id);
    } finally {
      session.closing = false;
    }
    if (state.sessions[id] !== session || state.selected !== id) return;
  } else if (session.rdpSessionId) {
    const rdpSessionId = session.rdpSessionId;
    session.closing = true;
    session.connected = false;
    session.rdpSessionId = "";
    try {
      await api().CloseRDP(rdpSessionId);
    } catch (error) {
      if (!/不存在|已经退出/.test(String(error))) throw error;
    } finally {
      session.closing = false;
    }
    if (state.sessions[id] !== session || state.selected !== id) return;
  }
  await connect();
}
$("#reconnectBtn").onclick = reconnectCurrent;
$("#sideReconnect").onclick = reconnectCurrent;
$("#reconnectNowBtn").onclick = async () => {
  const id = state.selected;
  const session = state.sessions[id];
  if (!session) return;
  await triggerImmediateReconnect(id, session, "用户触发立即重连…");
};
$("#reconnectCancelBtn").onclick = () => {
  const id = state.selected;
  const session = state.sessions[id];
  if (!session) return;
  session.autoReconnect = false;
  cancelSessionReconnect(session);
  hideTerminalReconnectBanner();
  writeTerminalOutput(id, "\r\n[重连] 已取消自动重连，您可以点击工具栏“重连”手动恢复。\r\n");
  toast("已取消自动重连");
  updateSessionTabState(id);
};
$("#persistentSessionBtn").onclick = async () => {
  const session = state.sessions[state.selected];
  if (!session?.connected) return toast("请先连接 SSH", true);
  if (session.tmuxPersistent) {
    session.tmuxPersistent = false;
    session.persistentTool = "";
    updatePersistentSessionButton();
    return toast("已关闭断线后的自动附加；当前 tmux/screen 会话不会被终止");
  }
  const button = $("#persistentSessionBtn");
  button.disabled = true;
  button.textContent = "准备中…";
  toast("正在检测 tmux/screen；服务器缺失时将自动安装");
  try {
    const tool = await api().DetectPersistentSessionTool(state.selected, session.connectionEpoch);
    session.tmuxPersistent = true;
    session.persistentTool = tool;
    updatePersistentSessionButton();
    await enqueueTerminalInput(
      state.selected,
      tool === "screen" ? "screen -xRR santianshell\r" : "tmux new-session -A -s santianshell\r",
      false,
    );
    toast(`已进入 ${tool} 持久会话`);
  } catch (error) {
    session.tmuxPersistent = false;
    session.persistentTool = "";
    toast(String(error), true);
  } finally {
    button.textContent = "持久会话";
    updatePersistentSessionButton();
  }
};
$("#syncCommandBtn").onclick = () => {
  state.syncCommands = !state.syncCommands;
  updateSyncCommandButton();
  const count = connectedSSHIds().length;
  toast(state.syncCommands ? `同步已开启，当前包含 ${count} 个 SSH 会话` : "同步已关闭，仅发送到当前 SSH");
};
$("#hostInfoBtn").onclick = () => {
  const host = selected();
  if (!host) return toast("请先选择主机", true);
  openHost(host, true);
};
$("#aiToggle").onclick = () =>
  setAIPanel($("main").classList.contains("ai-collapsed"));
$("#refreshFiles").onclick = () => loadFiles();
$("#path").onkeydown = (e) => {
  if (e.key === "Enter") loadFiles();
};
$("#upBtn").onclick = () =>
  loadFiles(
    state.path === "/"
      ? "/"
      : state.path.split("/").slice(0, -1).join("/") || "/",
  );
$("#uploadBtn").onclick = (event) => {
  const target = fileTarget();
  if (!target) return toast("请先连接 SSH", true);
  uploadToTarget(target, event.currentTarget);
};
$("#fileRows").onclick = (e) => {
  const row = e.target.closest("tr[data-i]");
  document
    .querySelectorAll("#fileRows tr.selected")
    .forEach((x) => x.classList.remove("selected"));
  if (!row) return;
  row.classList.add("selected");
  if (e.target.closest(".edit-file"))
    openFile(state.files[Number(row.dataset.i)], fileTarget());
};
$("#fileRows").ondblclick = (e) => {
  const row = e.target.closest("tr[data-i]");
  if (!row || e.target.closest("button")) return;
  const f = state.files[Number(row.dataset.i)];
  f.isDir ? loadFiles(f.path) : openFile(f, fileTarget());
};
$(".filelist").oncontextmenu = (e) => {
  const row = e.target.closest("tr[data-i]");
  e.preventDefault();
  document
    .querySelectorAll("#fileRows tr.selected")
    .forEach((x) => x.classList.remove("selected"));
  const target = fileTarget();
  if (!target) {
    $("#fileContextMenu").classList.add("hidden");
    return toast("请先连接 SSH", true);
  }
  state.contextTarget = target;
  state.contextFile = row ? state.files[Number(row.dataset.i)] : null;
  if (row) row.classList.add("selected");
  const menu = $("#fileContextMenu");
  for (const action of [
    "open",
    "edit",
    "download",
    "rename",
    "chmod",
    "copyPath",
    "delete",
  ])
    menu
      .querySelector(`[data-action="${action}"]`)
      .classList.toggle(
        "hidden",
        !state.contextFile ||
          ((action === "edit" || action === "download") &&
            state.contextFile.isDir),
      );
  menu.style.left = `${Math.min(e.clientX, innerWidth - 180)}px`;
  menu.style.top = `${Math.min(e.clientY, innerHeight - 360)}px`;
  menu.classList.remove("hidden");
};
document.addEventListener("click", (e) => {
  if (!e.target.closest("#fileContextMenu"))
    $("#fileContextMenu").classList.add("hidden");
});
$("#fileContextMenu").onclick = async (e) => {
  const action = e.target.closest("button")?.dataset.action;
  if (!action) return;
  const f = state.contextFile,
    target = state.contextTarget;
  $("#fileContextMenu").classList.add("hidden");
  try {
    if (!requireFileTarget(target)) return;
    if (action === "open")
      return f?.isDir
        ? updateSessionFiles(target.hostId, f.path, true, true)
        : openFile(f, target);
    if (action === "edit") return openFile(f, target);
    if (action === "download") return downloadFromTarget(target, f);
    if (action === "upload") return uploadToTarget(target);
    if (action === "refresh")
      return updateSessionFiles(target.hostId, target.path, true, true);
    if (action === "copyPath") {
      await window.runtime?.ClipboardSetText?.(f.path);
      return toast("路径已复制");
    }
    if (action === "newFile") {
      const name = remoteEntryName(await uiPrompt("输入新文件名称"));
      if (!name || !requireFileTarget(target)) return;
      await api().CreateRemoteFile(
        target.hostId,
        target.connectionEpoch,
        remoteChildPath(target.path, name),
      );
      return updateSessionFiles(target.hostId, target.path, false, true);
    }
    if (action === "newDir") {
      const name = remoteEntryName(await uiPrompt("输入新文件夹名称"));
      if (!name || !requireFileTarget(target)) return;
      await api().CreateRemoteDirectory(
        target.hostId,
        target.connectionEpoch,
        remoteChildPath(target.path, name),
      );
      return updateSessionFiles(target.hostId, target.path, false, true);
    }
    if (action === "rename") {
      const name = remoteEntryName(await uiPrompt("输入新名称", f.name));
      if (!name || name === f.name || !requireFileTarget(target)) return;
      const parent = f.path.split("/").slice(0, -1).join("/") || "/";
      await api().RenameRemotePath(
        target.hostId,
        target.connectionEpoch,
        f.path,
        remoteChildPath(parent, name),
      );
      return updateSessionFiles(target.hostId, target.path, false, true);
    }
    if (action === "chmod") return openChmod(f, target);
    if (action === "delete") {
      if (!(await uiConfirm(`确认永久删除“${f.name}”${f.isDir ? "及其全部内容" : ""}？`)))
        return;
      if (!requireFileTarget(target)) return;
      await api().DeleteRemotePath(
        target.hostId,
        target.connectionEpoch,
        f.path,
      );
      toast("已删除");
      return updateSessionFiles(target.hostId, target.path, false, true);
    }
  } catch (x) {
    toast(String(x), true);
  }
};
$("#chmodForm").onchange = updateChmodMode;
$("#cancelChmod").onclick = () => {
  state.chmodTarget = null;
  $("#chmodModal").classList.add("hidden");
};
$("#chmodForm").onsubmit = async (e) => {
  e.preventDefault();
  const target = state.chmodTarget,
    file = target?.file;
  if (!file || !requireFileTarget(target)) return;
  const button = e.submitter || $("#chmodForm .primary");
  button.disabled = true;
  try {
    await api().ChmodRemotePath(
      target.hostId,
      target.connectionEpoch,
      file.path,
      $("#chmodMode").textContent,
    );
    if (!fileTargetActive(target)) return;
    state.chmodTarget = null;
    $("#chmodModal").classList.add("hidden");
    toast("权限已更新");
    await updateSessionFiles(target.hostId, target.path, false, true);
  } catch (x) {
    toast(String(x), true);
  } finally {
    button.disabled = false;
  }
};
$("#treeItems").onclick = (e) => {
  const item = e.target.closest(".tree-dir");
  if (item) loadFiles(item.dataset.path);
};
async function saveRemoteEdit() {
  if (!state.editing) return;
  const file = state.editing,
    button = $("#saveEdit"),
    target = {
      hostId: file.hostId,
      connectionEpoch: file.connectionEpoch,
      path: file.directoryPath,
    };
  if (file.saving) return;
  if (file.stale || !fileTargetActive(target))
    return toast("原主机连接已切换，无法保存", true);
  const content = $("#editor").value,
    version = file.version;
  file.saving = true;
  button.disabled = true;
  $("#editorState").textContent = "同步中…";
  try {
    await api().SaveRemoteFile(
      file.hostId,
      file.connectionEpoch,
      file.path,
      content,
    );
    if (state.editing !== file) return;
    if (!fileTargetActive(target)) {
      file.stale = true;
      $("#editorState").textContent = "已保存，但连接已变化";
      $("#editorState").className = "bad";
      return;
    }
    if (file.version === version && $("#editor").value === content) {
      $("#editorState").textContent = "已同步";
      $("#editorState").className = "";
      toast("已覆盖保存到服务器原文件");
    } else {
      $("#editorState").textContent = "上一版本已保存，仍有修改未同步";
      $("#editorState").className = "dirty";
      toast("上一版本已保存，请再次保存最新修改");
    }
    const session = state.sessions[file.hostId];
    await updateSessionFiles(
      file.hostId,
      session?.path || file.directoryPath,
      false,
      true,
    );
  } catch (e) {
    if (state.editing === file) {
      $("#editorState").textContent = "同步失败，内容尚未保存";
      $("#editorState").className = "bad dirty";
    }
    toast(String(e), true);
  } finally {
    if (state.editing === file) {
      file.saving = false;
      button.disabled = file.stale;
    }
  }
}
$(".cancelEdit").onclick = async () => {
  if (state.editing?.saving)
    return toast("文件正在同步，请等待完成", true);
  if (
    $("#editorState").classList.contains("dirty") &&
    !(await uiConfirm("内容尚未保存，确认关闭？"))
  )
    return;
  $("#editorModal").classList.add("hidden");
  state.editing = null;
  $("#editor").value = "";
  $("#saveEdit").disabled = false;
};
$("#editor").oninput = () => {
  if (!state.editing) return;
  state.editing.version++;
  $("#editorState").textContent = state.editing.stale
    ? "连接已变化，修改不能保存"
    : "未同步";
  $("#editorState").className = state.editing.stale ? "bad dirty" : "dirty";
};
$("#editor").onkeydown = (e) => {
  if (e.ctrlKey && e.key.toLowerCase() === "s") {
    e.preventDefault();
    saveRemoteEdit();
  }
};
$("#saveEdit").onclick = saveRemoteEdit;
$("#downloadEdit").onclick = async () => {
  const file = state.editing;
  if (!file) return;
  await downloadFromTarget(
    {
      hostId: file.hostId,
      connectionEpoch: file.connectionEpoch,
      path: file.directoryPath,
    },
    file,
    $("#downloadEdit"),
  );
};
$("#refreshMonitor").onclick = loadMonitor;
$("#aiSettings").onclick = () => openAIProvider();
$("#providerTop").onchange = async (e) => {
  const providerId = e.currentTarget.value;
  if (!providerId) return;
  try {
    await runAIConfigTask("正在切换 AI 供应商…", async () => {
      await api().SelectAIProvider(providerId);
      state.settings.currentProviderId = providerId;
    });
  } catch (x) {
    toast(String(x), true);
  }
};
$("#modelTop").onchange = async (e) => {
  const c = currentProvider();
  const model = e.currentTarget.value;
  e.currentTarget.title = model || "暂无模型";
  if (!c || !model) return;
  try {
    await runAIConfigTask("正在切换 AI 模型…", async () => {
      replaceSettings(await api().SelectAIModel(c.id, model));
    });
    toast("AI 模型已切换");
  } catch (x) {
    toast(String(x), true);
  }
};
$("#aiPermission").onchange = async (e) => {
  const c = currentProvider();
  if (!c) return;
  const select = e.currentTarget;
  const previous = normalizeAIPermission(c.mode);
  const permission = normalizeAIPermission(select.value);
  if (!(await confirmManagedPermission(permission, previous))) {
    select.value = previous;
    return;
  }
  try {
    await runAIConfigTask("正在切换 AI 权限…", async () => {
      replaceSettings(await api().SelectAIPermission(c.id, permission));
    });
    toast(`AI 权限已切换为${AI_PERMISSION_NAMES[permission]}`);
  } catch (x) {
    select.value = previous;
    toast(String(x), true);
  }
};
$("#endpoint").addEventListener("input", invalidateAIProviderModelCache);
$("#apiKey").addEventListener("input", invalidateAIProviderModelCache);
$("#fetchProviderModels").onclick = async () => {
  const c = aiProviderFormConfig();
  if (!c.endpoint) return toast("请先填写 API 地址", true);
  if (!c.apiKey) return toast("请先填写 API Key", true);
  const button = $("#fetchProviderModels");
  button.textContent = "获取中…";
  try {
    const models = await runAIConfigTask("正在获取供应商模型…", async () =>
      normalizeAIModels(
        await api().FetchAIModelsByConfig(c.endpoint, c.apiKey),
      ),
    );
    if (!models.length) throw new Error("供应商没有返回可用模型");
    const current = aiProviderFormConfig();
    if (current.endpoint !== c.endpoint || current.apiKey !== c.apiKey) {
      toast("API 连接信息已更改，已忽略旧模型结果，请重新获取", true);
      return;
    }
    state.aiProviderDraftModels = models;
    state.aiProviderModelSource = { endpoint: c.endpoint, apiKey: c.apiKey };
    const selected = models.includes(c.model) ? c.model : models[0];
    renderAIProviderModelOptions(selected);
    $("#modelCacheHint").textContent = `已获取 ${models.length} 个模型，保存供应商后自动缓存`;
    toast(`已获取 ${models.length} 个模型`);
  } catch (x) {
    toast(`获取模型失败：${String(x)}`, true);
  } finally {
    button.textContent = "获取 API 模型";
    updateAIControls();
  }
};
$(".cancelAI").onclick = () => $("#aiModal").classList.add("hidden");
$("#aiConfigForm").onsubmit = async (e) => {
  e.preventDefault();
  const c = aiProviderFormConfig();
  if (!c.model) return toast("请先获取并选择模型", true);
  const previous = (state.settings.aiProviders || []).find(
    (provider) => provider.id === c.id,
  );
  if (!(await confirmManagedPermission(c.mode, previous?.mode))) return;
  try {
    await runAIConfigTask("正在保存 AI 供应商…", async () => {
      replaceSettings(await api().SaveAI(c));
    });
    $("#aiModal").classList.add("hidden");
    toast("AI 供应商已保存");
  } catch (x) {
    toast(String(x), true);
  }
};
$("#deleteProvider").onclick = async () => {
  const id = $("#providerId").value;
  if (!id) return;
  try {
    const deleted = await runAIConfigTask("等待确认删除 AI 供应商…", async () => {
      if (!(await uiConfirm("确认删除当前 AI 供应商？"))) return false;
      replaceSettings(await api().DeleteAIProvider(id));
      return true;
    });
    if (!deleted) return;
    $("#aiModal").classList.add("hidden");
    toast("AI 供应商已删除");
  } catch (x) {
    toast(String(x), true);
  }
};
$("#chat").onclick = async (e) => {
  const stepToggle = e.target.closest(".ai-step-toggle");
  if (stepToggle) {
    const card = stepToggle.closest(".ai-step-card");
    if (!card) return;
    const collapsed = card.classList.toggle("collapsed");
    stepToggle.textContent = collapsed ? "展开" : "收起";
    return;
  }
  const stepCopy = e.target.closest(".ai-step-copy");
  if (stepCopy) {
    const cmd = stepCopy.closest(".ai-step-card")?.dataset.command || "";
    if (cmd) {
      await navigator.clipboard.writeText(cmd);
      toast("已复制命令");
    }
    return;
  }
  const answerCopy = e.target.closest(".ai-answer-copy");
  if (answerCopy) {
    const raw = answerCopy.closest(".bubble.answer")?.dataset.rawText || "";
    if (raw) {
      await navigator.clipboard.writeText(raw);
      toast("已复制 AI 回答");
    }
    return;
  }
  const codeBlock = e.target.closest(".ai-code-block");
  if (codeBlock) {
    const code = codeBlock.querySelector("code")?.textContent || "";
    if (!code) return;
    if (e.target.closest(".ai-code-copy")) {
      await navigator.clipboard.writeText(code);
      toast("已复制命令");
    } else if (e.target.closest(".ai-code-fill")) {
      $("#command").value = code.replace(/\r?\n/g, " && ");
      $("#command").focus();
      toast("已填入命令栏");
    } else if (e.target.closest(".ai-code-run")) {
      if (!state.connected) return toast("请先连接 SSH", true);
      await runCommand(code.replace(/\r?\n/g, " && "));
    }
  }
};
$("#attachTerminalCtx").onclick = () => {
  const btn = $("#attachTerminalCtx");
  const next = btn.getAttribute("aria-pressed") !== "true";
  btn.setAttribute("aria-pressed", String(next));
  btn.classList.toggle("active", next);
};
$("#aiQuickChips").onclick = (e) => {
  const btn = e.target.closest("button[data-prompt]");
  if (!btn || btn.disabled) return;
  $("#question").value = btn.dataset.prompt || "";
  $("#aiForm").requestSubmit();
};
$("#clearAIChat").onclick = async () => {
  if (aiTaskBusy()) return toast("请先停止当前 AI 任务", true);
  $("#chat").innerHTML = '<div class="bubble system"><b>AI 服务器助手</b><br>已开启新对话。直接说明目标，AI 将自动排查、执行并反馈结果。</div>';
  await api()?.ClearAIAgentChat?.(state.selected || "")?.catch(() => {});
  toast("已清空对话与上下文");
};
$("#question").onkeydown = (event) => {
  if (event.key !== "Enter" || event.shiftKey || event.isComposing || event.keyCode === 229) return;
  event.preventDefault();
  $("#aiForm").requestSubmit();
};
$("#aiForm").onsubmit = async (e) => {
  e.preventDefault();
  if (aiTaskBusy()) return toast("已有 AI 任务正在执行", true);
  if (state.aiConfigBusy) return toast("AI 配置处理中，请稍候", true);
  const q = $("#question").value.trim();
  if (!q) return;
  const attachTerminal = $("#attachTerminalCtx")?.getAttribute("aria-pressed") === "true";
  const tail = attachTerminal ? readActiveTerminalTail(150) : "";
  const fullQuestion = tail
    ? `${q}\n\n[附带当前终端最近输出上下文]\n\`\`\`\n${tail}\n\`\`\``
    : q;
  appendAIBubble("user", q, tail ? "📎 已附带终端屏幕上下文" : "");
  $("#question").value = "";
  state.aiStarting = true;
  updateAIControls();
  $("#aiModeHint").textContent = "正在启动服务器执行代理…";
  startAIProgress("正在分析需求并准备执行…");
  try {
    await new Promise((resolve) => window.requestAnimationFrame(resolve));
    const runId = await api().StartAIAgent(state.selected || "", fullQuestion);
    if (state.aiFinishedRuns.has(runId)) return;
    setAIAgentRunning(true, runId);
  } catch (x) {
    appendAIBubble("answer bad", `无法启动任务：${String(x)}`);
    setAIAgentRunning(false);
    toast(String(x), true);
  }
};
$("#stopAI").onclick = async () => {
  if (!state.aiRunId) return;
  const button = $("#stopAI");
  button.disabled = true;
  button.textContent = "停止中…";
  try {
    await api().StopAIAgent(state.aiRunId);
    $("#aiModeHint").textContent = "正在停止当前命令…";
    updateAIProgress("正在停止当前任务…");
  } catch (error) {
    toast(String(error), true);
    button.disabled = false;
    button.textContent = "停止";
  }
};
async function checkHostLatency(id, shouldRender = true, accurate = false) {
  const existing = state.latencyTasks.get(id);
  if (existing) {
    const existingMode = state.latencyTaskModes.get(id);
    state.latencies[id] = { text: "检测中", className: "" };
    if (shouldRender) renderHosts();
    const result = await existing;
    if (!accurate || existingMode === "manual") return result;
    if (state.latencyTasks.get(id) === existing) state.latencyTasks.delete(id);
    state.latencyTaskModes.delete(id);
  }
  const task = performHostLatencyCheck(id, shouldRender, accurate);
  state.latencyTasks.set(id, task);
  state.latencyTaskModes.set(id, accurate ? "manual" : "automatic");
  try {
    return await task;
  } finally {
    if (state.latencyTasks.get(id) === task) {
      state.latencyTasks.delete(id);
      state.latencyTaskModes.delete(id);
    }
  }
}
async function performHostLatencyCheck(id, shouldRender = true, accurate = false) {
  state.latencyChecks.add(id);
  state.latencies[id] = { text: "检测中", className: "" };
  if (shouldRender) renderHosts();
  let timeout = 0;
  try {
    const latency = await Promise.race([
      accurate ? api().CheckHostRTT(id) : api().CheckHostLatency(id),
      new Promise((_, reject) => {
        timeout = window.setTimeout(() => reject(new Error("延迟检测超时")), accurate ? 15000 : 10000);
      }),
    ]);
    state.latencies[id] = {
      text: `${latency}ms`,
      className: "measured",
      title: latencyResultTitle(id, accurate ? 3 : 2),
    };
    return true;
  } catch (error) {
    const message = String(error?.message || error).replace(/^Error:\s*/i, "");
    state.latencies[id] = {
      text: /超时|timeout/i.test(message) ? "超时" : "离线",
      className: "offline",
      title: message || "延迟检测失败",
    };
    return false;
  } finally {
    if (timeout) window.clearTimeout(timeout);
    state.latencyChecks.delete(id);
    if (shouldRender) renderHosts();
  }
}
async function checkHostLatencies(ids) {
  const uniqueIds = [...new Set(ids.filter(Boolean))];
  if (!uniqueIds.length) return;
  uniqueIds.forEach((id) => {
    if (!state.latencyChecks.has(id)) state.latencies[id] = { text: "等待", className: "" };
  });
  renderHosts();
  let index = 0;
  const worker = async () => {
    while (index < uniqueIds.length) await checkHostLatency(uniqueIds[index++], false);
  };
  await Promise.all(Array.from({ length: Math.min(4, uniqueIds.length) }, worker));
  renderHosts();
}
async function checkAllHostLatencies() {
  await checkHostLatencies((state.settings.hosts || []).filter((h) => !h.deleted).map((h) => h.id));
}
$("#copyHostIP").onclick = async () => {
  const host = selected();
  if (!host?.address) return;
  await window.runtime?.ClipboardSetText?.(host.address);
  toast("主机 IP 已复制");
};
let lastResumeProbeAt = 0;
function handleNetworkOrWakeResume(reason) {
  if (!state.unlocked) return;
  const now = Date.now();
  if (reason === "visibility" && now - lastResumeProbeAt < 3000) return;
  lastResumeProbeAt = now;
  Object.entries(state.sessions).forEach(([id, session]) => {
    const host = state.settings?.hosts.find((item) => item.id === id);
    if (!host || (host.protocol || "ssh").toLowerCase() !== "ssh") return;
    if (session.reconnectTimer && session.autoReconnect && !session.connected && !session.connecting && !session.closing) {
      triggerImmediateReconnect(id, session, reason === "online" ? "检测到网络已恢复，提前尝试重连…" : "检测到系统恢复活跃，提前尝试重连…");
      return;
    }
    if (session.connected && !session.connecting && !session.closing && !session.probingAlive) {
      const epoch = session.connectionEpoch;
      session.probingAlive = true;
      api().ProbeTerminalAlive?.(id, epoch)
        ?.then((alive) => {
          if (alive && state.sessions[id] === session && session.connected && session.connectionEpoch === epoch && id === state.selected) {
            session.latencyDueAt = 0;
            updateSessionLatency(id, true);
          }
        })
        ?.catch(() => {})
        ?.finally(() => {
          session.probingAlive = false;
        });
    }
  });
}
window.addEventListener("online", () => handleNetworkOrWakeResume("online"));
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") handleNetworkOrWakeResume("visibility");
});
let lastWakeClockAt = Date.now();
window.setInterval(() => {
  const now = Date.now();
  if (now - lastWakeClockAt > 15000) {
    lastWakeClockAt = now;
    handleNetworkOrWakeResume("wake");
    return;
  }
  lastWakeClockAt = now;
}, 5000);
function showAppUpdateDialog(info) {
  const modal = $("#appUpdateModal");
  if (!modal) return;
  const curVer = String(info?.currentVersion || "").replace(/^v/i, "");
  const latestVer = String(info?.latestVersion || "").replace(/^v/i, "");
  $("#appUpdateCurVer").textContent = curVer ? `v${curVer}` : "-";
  $("#appUpdateNewVer").textContent = latestVer ? `v${latestVer}` : "-";
  $("#appUpdateNotes").textContent = info?.releaseNotes?.trim() || "暂无更新说明";
  const progressBox = $("#appUpdateProgressBox");
  const progressBar = $("#appUpdateProgressBar");
  const statusText = $("#appUpdateStatusText");
  const percentText = $("#appUpdatePercentText");
  const cancelBtn = $("#cancelAppUpdateBtn");
  const confirmBtn = $("#confirmAppUpdateBtn");
  const closeBtn = $("#closeAppUpdateBtn");

  progressBox.classList.add("hidden");
  progressBar.style.width = "0%";
  percentText.textContent = "0%";
  statusText.textContent = "准备下载更新包...";
  cancelBtn.disabled = false;
  cancelBtn.textContent = "暂不更新";
  cancelBtn.classList.remove("hidden");
  confirmBtn.disabled = false;
  confirmBtn.textContent = "立即更新";
  closeBtn.disabled = false;
  modal.classList.remove("hidden");

  const closeModal = () => {
    if (!confirmBtn.disabled) modal.classList.add("hidden");
  };
  cancelBtn.onclick = closeModal;
  closeBtn.onclick = closeModal;
  confirmBtn.onclick = async () => {
    confirmBtn.disabled = true;
    confirmBtn.textContent = "正在更新...";
    cancelBtn.disabled = true;
    closeBtn.disabled = true;
    progressBox.classList.remove("hidden");
    statusText.textContent = "正在连接并下载更新包...";
    try {
      await api().ApplyAppUpdate();
      statusText.textContent = "更新包已就绪，即将自动重启覆盖旧版...";
      percentText.textContent = "100%";
      progressBar.style.width = "100%";
    } catch (err) {
      statusText.textContent = `更新失败：${err?.message || err}`;
      cancelBtn.disabled = false;
      cancelBtn.textContent = "关闭";
      cancelBtn.classList.remove("hidden");
      confirmBtn.disabled = false;
      confirmBtn.textContent = "重试";
      closeBtn.disabled = false;
      toast(`更新失败：${err?.message || err}`, true);
    }
  };
}
queueRuntimeEvent("app-update-progress", (data) => {
  const progressBox = $("#appUpdateProgressBox");
  const progressBar = $("#appUpdateProgressBar");
  const statusText = $("#appUpdateStatusText");
  const percentText = $("#appUpdatePercentText");
  if (!progressBox) return;
  progressBox.classList.remove("hidden");
  const percent = Math.min(100, Math.max(0, Math.round(data?.percent || 0)));
  progressBar.style.width = `${percent}%`;
  percentText.textContent = `${percent}%`;
  if (data?.phase === "downloading") {
    const spd = data.speedString ? ` (${data.speedString})` : "";
    statusText.textContent = `正在下载更新包 ${percent}%${spd}`;
  } else if (data?.phase === "extracting") {
    statusText.textContent = data.message || "下载完成，正在解压校验更新包...";
  } else if (data?.phase === "applying") {
    statusText.textContent = data.message || "准备完成，即将自动重启覆盖旧版本...";
  } else if (data?.message) {
    statusText.textContent = data.message;
  }
});
$("#checkAppUpdateBtn").onclick = async () => {
  const btn = $("#checkAppUpdateBtn");
  const hint = $("#appUpdateCheckHint");
  btn.disabled = true;
  btn.textContent = "检查中...";
  hint.textContent = "正在连接 GitHub Releases 检查最新版本...";
  try {
    const info = await api().CheckAppUpdate();
    if (info?.available) {
      hint.textContent = `发现新版本 ${info.latestVersion}`;
      showAppUpdateDialog(info);
    } else {
      const cur = String(info?.currentVersion || "").replace(/^v/i, "");
      hint.textContent = `当前已是最新版本 (v${cur})`;
      toast(`当前已是最新版本 (v${cur})`);
    }
  } catch (err) {
    const msg = err?.message || String(err);
    hint.textContent = `检查失败：${msg}`;
    toast(`检查更新失败：${msg}`, true);
  } finally {
    btn.disabled = false;
    btn.textContent = "检查更新";
  }
};
init().catch((e) => toast(String(e), true));
