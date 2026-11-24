import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";
import { fileURLToPath } from "node:url";

const mainPath = fileURLToPath(new URL("../src/main.js", import.meta.url));
const mainSource = readFileSync(mainPath, "utf8");
const layoutPath = fileURLToPath(new URL("../src/layout.css", import.meta.url));
const layoutSource = readFileSync(layoutPath, "utf8");

function extractFunction(name) {
  let start = mainSource.indexOf(`function ${name}(`);
  assert.notEqual(start, -1, `未找到函数 ${name}`);
  if (mainSource.slice(start - 6, start) === "async ") start -= 6;
  const bodyStart = mainSource.indexOf("{", start);
  let depth = 0;
  let quote = "";
  let escaped = false;
  for (let index = bodyStart; index < mainSource.length; index += 1) {
    const char = mainSource[index];
    if (quote) {
      if (escaped) escaped = false;
      else if (char === "\\") escaped = true;
      else if (char === quote) quote = "";
      continue;
    }
    if (char === '"' || char === "'" || char === "`") {
      quote = char;
      continue;
    }
    if (char === "{") depth += 1;
    else if (char === "}" && --depth === 0) return mainSource.slice(start, index + 1);
  }
  throw new Error(`函数 ${name} 代码不完整`);
}

function createHarness(apiValue) {
  const id = "host-1";
  const state = {
    sessions: { [id]: { connected: true, connecting: false, closing: false, latencyLoading: false } },
    latencyChecks: new Set(),
    latencyTasks: new Map(),
    latencyTaskModes: new Map(),
    latencies: {},
  };
  const context = {
    api: () => apiValue,
    latencyResultTitle: (_id, attempts) => `测试路径 · ${attempts} 次取最优`,
    renderHosts() {},
    state,
    window: { clearTimeout, setTimeout },
  };
  const exported = vm.runInNewContext(
    `(() => { ${extractFunction("performHostLatencyCheck")}
return { performHostLatencyCheck }; })()`,
    context,
  );
  return { exported, id, state };
}

test("主机延迟按钮只保留一个点击入口", () => {
  assert.equal((mainSource.match(/\$\("#hostList"\)\.onclick\s*=/g) || []).length, 1);
  assert.equal(
    (mainSource.match(/\$\("#hostList"\)\.addEventListener\("click"/g) || []).length,
    0,
    "重复的捕获阶段监听会拦截主机列表点击",
  );
});

test("RDP 从主机页面连接时不切换到工作台", () => {
  const rdpBranch = mainSource.match(
    /if \(e\.target\.closest\("\.connectHost"\)\) \{([\s\S]*?)\n  \}/,
  )?.[1] || "";
  assert.match(rdpBranch, /host\.protocol[\s\S]*=== "rdp"/);
  assert.match(rdpBranch, /connectHostById\(host\.id\)/);
  assert.match(rdpBranch, /hostDrawer"\)\.classList\.remove\("hidden"\)/);
  assert.ok(
    rdpBranch.indexOf('=== "rdp"') < rdpBranch.indexOf("selectHost(host.id)"),
    "RDP 分支必须在切换工作台前返回",
  );
});

test("手动延迟检测与 SSH 会话状态解耦", async () => {
  let rttCalls = 0;
  const harness = createHarness({
    async CheckHostRTT() {
      rttCalls += 1;
      return 37;
    },
  });

  const success = await harness.exported.performHostLatencyCheck(harness.id, true, true);

  assert.equal(success, true);
  assert.equal(rttCalls, 1);
  assert.equal(harness.state.latencies[harness.id].text, "37ms");
  assert.equal(harness.state.latencyChecks.has(harness.id), false);
});

test("工作台 SSH 延迟与主机列表 TCP 延迟状态隔离", async () => {
  assert.match(mainSource, /function sessionLatencyTargetText\([\s\S]*SSH RTT/);
  assert.match(mainSource, /api\(\)\.CheckConnectedHostLatency\(id, epoch\)/);
  const source = extractFunction("updateSessionLatency");
  assert.doesNotMatch(source, /state\.latencies/);
  assert.doesNotMatch(source, /latencyChecks/);
  assert.doesNotMatch(extractFunction("advanceConnectionEpoch"), /state\.latencies/);
  assert.doesNotMatch(extractFunction("markSessionDisconnected"), /state\.latencies/);
  assert.doesNotMatch(mainSource, /等待当前 TCP 延迟检测完成/);
});

test("监控卡片标题保持单行并让右侧状态自适应省略", () => {
  assert.match(layoutSource, /\.monitor-side \.card-title > b\s*\{[\s\S]*white-space:\s*nowrap/);
  assert.match(layoutSource, /\.monitor-side \.card-title > span\s*\{[\s\S]*text-align:\s*right[\s\S]*text-overflow:\s*ellipsis[\s\S]*white-space:\s*nowrap/);
  assert.match(mainSource, /return `SSH RTT · \$\{host\?\.address/);
});

test("手动延迟检测失败后会释放检测状态并保留真实原因", async () => {
  const harness = createHarness({
    async CheckHostRTT() {
      throw new Error("SOCKS5 目标连接超时");
    },
  });

  const success = await harness.exported.performHostLatencyCheck(harness.id, true, true);

  assert.equal(success, false);
  assert.equal(harness.state.latencies[harness.id].text, "超时");
  assert.match(harness.state.latencies[harness.id].title, /SOCKS5 目标连接超时/);
  assert.equal(harness.state.latencyChecks.has(harness.id), false);
});
