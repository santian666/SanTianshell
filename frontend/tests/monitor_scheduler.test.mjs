import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";
import { fileURLToPath } from "node:url";

const mainPath = fileURLToPath(new URL("../src/main.js", import.meta.url));
const mainSource = readFileSync(mainPath, "utf8");

function extractFunction(name) {
  const start = mainSource.indexOf(`function ${name}(`);
  assert.notEqual(start, -1, `未找到函数 ${name}`);
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

const schedulerStart = mainSource.indexOf("const MONITOR_CONCURRENCY_MIN");
const schedulerEnd = mainSource.indexOf("function renderMonitorTaskFailure", schedulerStart);
assert.notEqual(schedulerStart, -1, "未找到监控调度常量");
assert.notEqual(schedulerEnd, -1, "未找到监控调度边界");
const schedulerPrelude = mainSource.slice(schedulerStart, schedulerEnd);
const schedulerSource = `${extractFunction("monitorSection")}
${extractFunction("parseProcesses")}
${schedulerPrelude}
${extractFunction("runMonitorScheduler")}`;

function createSession() {
  return {
    connected: true,
    connectionEpoch: 1,
    monitorLoading: false,
    monitorRequest: 0,
    monitorDueAt: 0,
    monitorUpdatedAt: 0,
    monitorError: "",
    processLoading: false,
    processRequest: 0,
    processDueAt: 0,
    processUpdatedAt: 0,
    processError: "",
    processes: [],
    diskLoading: false,
    diskRequest: 0,
    diskDueAt: 0,
    diskUpdatedAt: 0,
    diskError: "",
    latencyLoading: false,
    latencyRequest: 0,
    latencyDueAt: 0,
    latencyError: "",
    latency: null,
    monitorForced: new Set(),
    monitorNotify: new Set(),
  };
}

function createHarness(hostCount = 12) {
  let now = 10_000;
  const ids = Array.from({ length: hostCount }, (_, index) => `host-${index}`);
  const sessions = Object.fromEntries(ids.map((id) => [id, createSession()]));
  const dispatched = [];
  const state = {
    selected: ids[0],
    connected: true,
    sessions,
    monitorActive: 0,
    monitorSlowActive: 0,
    monitorStatusRenderedAt: now,
    latencyChecks: new Set(),
    latencies: {},
  };
  let monitorAPI = {};
  const classList = { contains: () => true };
  const context = {
    api: () => monitorAPI,
    app: { classList: { contains: () => false } },
    connectedSSHIds: () => ids.filter((id) => sessions[id]?.connected),
    Date: { now: () => now },
    dispatchMonitorJob: (job) => dispatched.push(job),
    document: { hidden: false, addEventListener() {} },
    renderCurrentMonitorFreshness() {},
    renderProcesses() {},
    renderNetwork() {},
    renderDisks() {},
    renderFastMonitor() {},
    renderWorkspaceLatency() {},
    restoreSessionView() {},
    showTerminal() {},
    state,
    toast() {},
    window: {
      addEventListener() {},
      clearTimeout,
      setInterval: () => 1,
      setTimeout,
    },
    $: () => ({ classList }),
  };
  const exported = vm.runInNewContext(
    `(() => { ${schedulerSource}; return { parseProcesses, rescheduleSessionMonitor, runMonitorScheduler, updateSessionMonitor }; })()`,
    context,
  );
  return {
    dispatched,
    exported,
    ids,
    setAPI(value) {
      monitorAPI = value;
    },
    setNow(value) {
      now = value;
    },
    state,
  };
}

test("监控槽饱和时仍调度当前主机的进程任务", () => {
  const harness = createHarness(12);
  harness.exported.runMonitorScheduler();

  assert.ok(harness.dispatched.some((job) => job.kind === "fast"), "饱和调度仍应包含系统快照任务");
  assert.ok(harness.dispatched.some((job) => job.kind !== "fast"), "慢任务不应被系统快照永久挤占");
  assert.ok(
    harness.dispatched.some((job) => job.id === harness.state.selected && job.kind === "process"),
    "当前主机的进程任务应优先获得调度机会",
  );
  assert.equal(
    new Set(harness.dispatched.map((job) => job.id)).size,
    harness.dispatched.length,
    "同一轮不应向同一主机并发派发两个监控任务",
  );
});

test("返回工作台时会缩短旧的后台到期时间", () => {
  const harness = createHarness(1);
  const session = harness.state.sessions[harness.state.selected];
  harness.setNow(20_000);
  session.processUpdatedAt = 18_500;
  session.processDueAt = 60_000;

  harness.exported.rescheduleSessionMonitor(session, harness.state.selected);

  assert.ok(session.processDueAt <= 20_000, `进程任务仍被旧到期时间延迟：${session.processDueAt}`);
});

test("进程刷新失败后清空旧列表，避免旧数据伪装成冻结", async () => {
  const harness = createHarness(1);
  const id = harness.ids[0];
  const session = harness.state.sessions[id];
  harness.state.selected = "";
  session.processes = [{ pid: "99", cpu: "88", memory: "1", command: "stale" }];
  harness.setAPI({
    async GetMonitorProcesses() {
      throw new Error("模拟采集失败");
    },
  });

  const updated = await harness.exported.updateSessionMonitor(id, "process");

  assert.equal(updated, false);
  assert.equal(session.processes.length, 0, "失败后仍展示上一次进程数据");
  assert.match(session.processError, /模拟采集失败/);
  assert.equal(session.processLoading, false);
});

test("标准化进程输出保留 PID、CPU、内存和命令", () => {
  const harness = createHarness(1);
  const rows = harness.exported.parseProcesses(
    "__PROCESS__\nPID CPU MEM COMMAND\n123 37.5 4.2 worker\n456 1% 0.5% helper --once\n",
  );

  assert.equal(rows.length, 2);
  assert.equal(rows[0].pid, "123");
  assert.equal(rows[0].cpu, "37.5");
  assert.equal(rows[0].memory, "4.2");
  assert.equal(rows[1].command, "helper --once");
});
