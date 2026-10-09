// A stand-in for `pier api`: the app's protocol over stdio, with sessions,
// repos and chats in memory and agents that answer — text streaming in,
// tools running, approvals, questions and plans — so the app can be
// developed, tested (Playwright) and looked at without a cloud. Its chats
// come from real recorded agent streams (chats.json, written by
// `go test ./pkg/pier/chat -run MockChats -update-mock`).
//
// Run the app on it: PIER_API_BIN=$PWD/mock/pier pnpm exec vite --port 1420
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import readline from "node:readline";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const recorded = JSON.parse(readFileSync(join(here, "chats.json"), "utf8"));
const fast = process.env.PIER_MOCK_FAST === "1"; // tests: no artificial delays

const now = Date.now();
const iso = (msAgo = 0) => new Date(Date.now() - msAgo).toISOString();
const H = 3600e3;
const D = 24 * H;
const wait = (ms) => new Promise((r) => setTimeout(r, fast ? 0 : ms));

// --- wire --------------------------------------------------------------------

const send = (m) => process.stdout.write(`${JSON.stringify({ jsonrpc: "2.0", ...m })}\n`);
let subSeq = 0;
/** Open watches: sub id → {topic, key, seq}. */
const subs = new Map();
function openSub(topic, key) {
  const id = `${topic}-${++subSeq}`;
  subs.set(id, { topic, key, seq: 0 });
  return id;
}
function emit(topic, key, data) {
  for (const [id, s] of subs) {
    if (s.topic === topic && s.key === key) {
      s.seq++;
      send({ method: "sub.event", params: { sub: id, seq: s.seq, topic, data } });
    }
  }
}

// --- data --------------------------------------------------------------------

const res = (cpu, mem) => ({ cpu, cpus: 2, memUsed: mem * 2 ** 30, memTotal: 4 * 2 ** 30, diskUsed: 14 * 2 ** 30, diskTotal: 40 * 2 ** 30, load1: cpu / 50 });
const sess = (o) => ({
  origin: "github.com/acme/shop",
  base: "main",
  attached: false,
  strained: false,
  setup: "",
  machineType: "t4g.medium",
  costNote: "~$0.04/h",
  hourlyUsd: 0.04,
  pooled: false,
  hasKey: true,
  activity: "",
  label: "running",
  ...o,
});

const sessions = [
  sess({ id: "i-01", name: "checkout-flow", repo: "shop", branch: "checkout-flow", state: "working", activity: "agent working, you're detached", createdAt: iso(3 * H), resources: res(62, 2.6), ports: [{ port: 3000, process: "node" }] }),
  sess({ id: "i-02", name: "fix-login", repo: "shop", branch: "fix-login", state: "idle", activity: "quiet — parks itself when the idle timer runs out", createdAt: iso(26 * H), strained: true, machineType: "t4g.large", costNote: "~$0.07/h", hourlyUsd: 0.07, resources: res(8, 3.5), parkAt: new Date(now + 17 * 60e3).toISOString(),
    pr: { number: 41, title: "Fix the login redirect loop", url: "https://github.com/acme/shop/pull/41", state: "open", headOid: "abc", baseRef: "main", additions: 12, deletions: 3, checks: "failing", checksPassed: 3, checksTotal: 4, review: "changes_requested", mergeable: "blocked" } }),
  sess({ id: "i-03", name: "search-v2", repo: "shop", branch: "search-v2", state: "parked", label: "parked", costNote: "~$3-4/mo", hourlyUsd: 0, createdAt: iso(4 * D),
    pr: { number: 38, title: "Search v2", url: "u", state: "merged", headOid: "d", baseRef: "main", additions: 300, deletions: 120, checks: "passing", checksPassed: 5, checksTotal: 5, review: "approved", mergeable: "unknown" } }),
  sess({ id: "i-04", name: "new-onboarding", repo: "shop", branch: "new-onboarding", state: "creating", label: "starting", createdAt: iso(60e3) }),
  sess({ id: "i-05", name: "pool-a1", repo: "shop", branch: "pier-pool", state: "parked", pooled: true, costNote: "~$3-4/mo", hourlyUsd: 0, createdAt: iso(2 * D) }),
  sess({ id: "i-06", name: "docs-refresh", repo: "website", origin: "github.com/acme/website", branch: "docs-refresh", state: "running", setup: "running", createdAt: iso(5 * 60e3) }),
  sess({ id: "i-07", name: "api-rate-limits", repo: "api", origin: "github.com/acme/api", branch: "api-rate-limits", state: "failed", label: "create failed", failReason: "vCPU quota exceeded — request a raise", costNote: "", hourlyUsd: 0, createdAt: iso(2 * H), logPath: "/tmp/x" }),
];
const checkouts = [
  { path: "/Users/dev/code/shop", name: "shop", origin: "github.com/acme/shop", missing: false },
  { path: "/Users/dev/code/website", name: "website", origin: "github.com/acme/website", missing: false },
];
const repos = [
  { name: "api", origin: "github.com/acme/api", hasPier: false, imageKind: "", baking: false, poolTarget: 0, poolReady: 0, poolFilling: 0, poolStale: 0, poolAges: [], sessions: 1, monthlyUsd: 0, hourlyUsd: 0, idleMonthlyUsd: 0, reminders: [] },
  { name: "shop", origin: "github.com/acme/shop", checkout: checkouts[0], hasPier: true, image: "ami-0abc", imageKind: "prebuilt", bakedAt: iso(34 * D), baking: false, poolTarget: 1, poolReady: 1, poolFilling: 0, poolStale: 0, poolAges: ["2d"], sessions: 4, monthlyUsd: 5.8, hourlyUsd: 0.11, idleMonthlyUsd: 9.6,
    reminders: [{ message: "shop's session image is 34 days old. Rebake so new sessions start with current dependencies.", action: "pier bake" }] },
  { name: "website", origin: "github.com/acme/website", checkout: checkouts[1], hasPier: true, imageKind: "toolchain", image: "ami-0def", bakedAt: iso(3 * D), baking: false, poolTarget: 0, poolReady: 0, poolFilling: 0, poolStale: 0, poolAges: [], sessions: 1, monthlyUsd: 2, hourlyUsd: 0.04, idleMonthlyUsd: 2, reminders: [] },
];

// Chats: the recorded conversations, with the prompts that started them.
const user = (id, text, msAgo) => ({ id, kind: "user", time: iso(msAgo), text });
const stamp = (items, from) => items.map((it, i) => ({ ...it, time: iso(from - i * 4000) }));
const chats = new Map(); // chat id → {info, items, sessionId, streaming, turn}
function addChat(sessionId, info, items) {
  chats.set(info.id, { sessionId, info: { queue: [], created: iso(2 * H), ...info }, items, streaming: [] });
}
addChat("i-01", { id: "c1", agent: "claude", title: "Create hello.txt and add add()", model: "opus", effort: "high", mode: "default", state: "ready", since: iso(30 * 60e3), last: "done" },
  [user("p1", "Create a file hello.txt containing the single line 'hi'. Then run `ls` and `cat hello.txt`, and edit math.py to add an add(a, b) function.", 32 * 60e3), ...stamp(recorded["claude-tools"], 31 * 60e3)]);
addChat("i-01", { id: "c2", agent: "claude", title: "Plan add() with a docstring", model: "sonnet", mode: "plan", state: "ready", since: iso(20 * 60e3), created: iso(H) },
  [user("p2", "Plan adding an add(a, b) function to math.py, then present it for approval. Once approved, implement it.", 22 * 60e3), ...stamp(recorded["claude-plan"], 21 * 60e3)]);
addChat("i-01", { id: "c3", agent: "codex", title: "Ask, plan, then add mul()", model: "gpt-6-luna", mode: "plan", state: "ready", since: iso(10 * 60e3), created: iso(50 * 60e3) },
  [user("p3", "I want to add a function to math.py. Before planning, ask me whether it should be add or mul, then write a 3-line plan.", 12 * 60e3), ...stamp(recorded["codex-question"], 11 * 60e3)]);
addChat("i-02", { id: "c4", agent: "codex", title: "Create hello.txt", state: "ready", since: iso(5 * 60e3), last: "done" },
  [user("p4", "Create a file hello.txt containing 'hi', then run `ls` and `cat hello.txt`, and add an add(a, b) function to math.py.", 7 * 60e3), ...stamp(recorded["codex-tools"], 6 * 60e3)]);
addChat("i-03", { id: "c5", agent: "claude", title: "Answer two questions", state: "ready", since: iso(D) },
  [user("p5", "Ask me which color I prefer and which fruits I like, then tell me my answers.", D + 60e3), ...stamp(recorded["claude-question"], D)]);

const chatsOf = (sessionId) => [...chats.values()].filter((c) => c.sessionId === sessionId);
const summary = (c) => {
  const { model, effort, mode, created, pending, queue, error, ...s } = c.info;
  return { ...s, ask: pending?.summary, queued: queue.length };
};
const sessionView = (s) => ({ ...s, chats: chatsOf(s.id).map(summary), attention: attention(s) });
function attention(s) {
  if (s.state === "failed") return "create_failed";
  if (chatsOf(s.id).some((c) => c.info.state === "waiting")) return "needs_input";
  if (s.pr?.state === "merged") return "merged_cleanup";
  if (s.pr?.checks === "failing") return "ci_failing";
  return undefined;
}
const overview = () => ({
  cloud: "AWS eu-central-1",
  sessions: sessions.map(sessionView),
  repos,
  checkouts,
  totals: { running: 4, parked: 1, poolReady: 1, hourlyUsd: 0.15, idleMonthlyUsd: 15.4 },
  quota: { used: 10, limit: 32, detail: "10/32 vCPUs in use" },
  updatedAt: iso(),
  enriched: true,
});
const live = (sessionId) => ["running", "working", "idle"].includes(sessions.find((s) => s.id === sessionId)?.state);

/** An error of a kind the app tells apart (parked, conflict, ...). */
function rpcError(kind, message) {
  return Object.assign(new Error(message), { kind });
}

// Each session's checkout against its base, as the session pushes it.
const checkoutFiles = new Map();
function filesOf(id) {
  if (!checkoutFiles.has(id)) {
    checkoutFiles.set(id, [
      { path: "src/checkout/PayButton.tsx", status: "modified", added: 4, removed: 1, uncommitted: true, stamp: "1" },
      { path: "src/api/charges.ts", status: "modified", added: 6, removed: 2, stamp: "1" },
      { path: "src/api/charges.test.ts", status: "added", added: 31, removed: 0, stamp: "1" },
    ]);
  }
  return checkoutFiles.get(id);
}
function changes(id) {
  const files = filesOf(id);
  const on = live(id);
  return { branch: "checkout-flow", head: "abc1234", upstream: "origin/checkout-flow", ahead: 2, behind: 0, baseRef: "origin/main", baseOid: "b4se",
    uncommitted: files.filter((f) => f.uncommitted).length, live: on, asOf: on ? undefined : iso(20 * 60e3), files: files.map((f) => ({ ...f, stamp: on ? f.stamp : undefined })) };
}
/** An agent edited a file: the session pushes its new changes. */
function edited(id, path, added) {
  const files = filesOf(id);
  const f = files.find((x) => x.path === path);
  if (f) Object.assign(f, { added: f.added + added, uncommitted: true, stamp: String(Number(f.stamp) + 1) });
  else files.push({ path, status: "untracked", added, removed: 0, uncommitted: true, stamp: "1" });
  emit("git", id, changes(id));
}
// What pier asks the agent: its own wording, the user's, a repo's.
const promptKinds = [
  { key: "fixCI", title: "Fix CI", desc: "Starts the message that sends the failing checks' logs to the agent.", def: "CI is failing on PR #{pr}. Find out why, fix it, and push." },
  { key: "addressComments", title: "Address review comments", desc: "Starts the message that sends the pull request's open review feedback to the agent.", def: "Address the review feedback on PR #{pr}, then push." },
  { key: "createPR", title: "Create a pull request", desc: "What Create PR asks the agent.", def: "Open a pull request for this branch." },
  { key: "onboard", title: "Set a repo up for pier", desc: "The first message of the session that onboards a repository.", def: "Set this repository up for pier, using the pier-onboard skill." },
  { key: "review", title: "Review comments", desc: "Heads your diff and file comments when they go to the agent.", def: "Review comments on your changes — please address each one:" },
];
const globalPrompts = {};
const repoPrompts = new Map();
const repoFiles = ["src/checkout/PayButton.tsx", "src/checkout/Summary.tsx", "src/api/charges.ts", "src/api/charges.test.ts", "package.json", "README.md"];
const fileText = (path) => `// ${path}\nexport function PayButton() {\n  return null;\n}\n`;

function changed(c) {
  emit("chats", c.sessionId, { chats: chatsOf(c.sessionId).map((x) => x.info), live: live(c.sessionId) });
  emit("chat", c.info.id, { op: "chat", chat: c.info });
  emit("overview", "", overview());
}
function put(c, item) {
  const at = c.items.findIndex((i) => i.id === item.id);
  c.streaming = c.streaming.filter((s) => s.id !== item.id);
  if (at >= 0) {
    c.items[at] = item;
    emit("chat", c.info.id, { op: "update", items: [item] });
  } else {
    c.items.push(item);
    emit("chat", c.info.id, { op: "append", items: [item] });
  }
}
function setState(c, state, extra = {}) {
  c.info = { ...c.info, state, since: iso(), ...extra };
  changed(c);
}

// --- agents ------------------------------------------------------------------

let n = 0;
const nid = (p) => `${p}-${++n}`;

async function stream(c, kind, text) {
  const id = nid("msg");
  for (const chunk of text.match(/.{1,6}/gs) ?? []) {
    if (c.turn?.stopped) return;
    const s = c.streaming.find((x) => x.id === id);
    if (s) s.text += chunk;
    else c.streaming.push({ id, kind, time: iso(), text: chunk });
    emit("chat", c.info.id, { op: "delta", deltas: [{ id, kind, text: chunk }] });
    await wait(35);
  }
  put(c, { id, kind, time: iso(), text });
}

/** Waits on the user: resolves with their decision. */
function askUser(c, req) {
  return new Promise((resolve) => {
    c.decide = (d) => {
      c.decide = null;
      c.info = { ...c.info, pending: undefined };
      setState(c, "working");
      resolve(d);
    };
    c.info = { ...c.info, pending: { at: iso(), ...req } };
    setState(c, "waiting", { waiting: req.kind === "tool" ? "permission" : req.kind });
  });
}

const tool = (name, view, summary, extra = {}) => ({ id: nid("toolu"), name, view, status: "running", summary, ...extra });

async function turn(c, text) {
  c.turn = { stopped: false };
  const t = c.turn;
  const name = c.info.agent === "codex" ? "Codex" : "Claude";
  setState(c, "working", { error: undefined });
  await wait(400);
  try {
    if (/plan/i.test(text) || c.info.mode === "plan") {
      await stream(c, "thinking", "Let me read the code before proposing anything.");
      const read = tool("Read", "read", "/home/agent/work/shop/src/checkout/PayButton.tsx");
      put(c, { id: read.id, kind: "tool", time: iso(), tool: read });
      await wait(500);
      put(c, { id: read.id, kind: "tool", time: iso(), tool: { ...read, status: "ok", result: "1\texport function PayButton…" } });
      const plan = "## Fix double charges\n\n1. **Disable Pay while pending** — `PayButton` tracks the request.\n2. **Idempotency key** — `createCharge` sends `Idempotency-Key: <order id>`.\n3. **Test** — a double click charges once.";
      const d = await askUser(c, { id: nid("req"), kind: "plan", tool: c.info.agent === "codex" ? "Plan" : "ExitPlanMode", summary: "Fix double charges", plan });
      if (d.behavior === "deny") {
        put(c, { id: nid("s"), kind: "status", time: iso(), text: d.message ? `plan sent back: ${d.message}` : "plan declined" });
        return finish(c, t, `I'll rework the plan${d.message ? ` — ${d.message}` : ""}.`);
      }
      c.info = { ...c.info, mode: d.mode || "default" };
      await stream(c, "assistant", "Plan approved — implementing it now.");
    } else if (/\?|ask|question/i.test(text)) {
      const questions = [
        { id: c.info.agent === "codex" ? "approach" : "Which approach should I take?", question: "Which approach should I take?", header: "Approach", kind: "choice", other: true,
          options: [{ label: "Disable the button", description: "Simplest; covers double clicks." }, { label: "Idempotency key", description: "Also covers retries and reloads." }] },
        { id: c.info.agent === "codex" ? "tests" : "Which tests should I add?", question: "Which tests should I add?", header: "Tests", kind: "choice", multiSelect: true,
          options: [{ label: "Unit" }, { label: "Integration" }, { label: "End to end" }] },
      ];
      const d = await askUser(c, { id: nid("req"), kind: "question", tool: c.info.agent === "codex" ? "Question" : "AskUserQuestion", summary: questions[0].question, title: `${name} has questions`, questions });
      if (d.behavior === "deny") return finish(c, t, "No answers — I'll go with an idempotency key and unit tests.");
      const said = Object.entries(d.answers ?? {}).map(([q, a]) => `**${q}** ${a}`).join("\n");
      await stream(c, "assistant", `Got it:\n\n${said}`);
    }
    if (t.stopped) return;
    await stream(c, "assistant", "I'll look at how the **Pay** button submits.");
    const grep = tool("Grep", "search", "createCharge in src");
    put(c, { id: grep.id, kind: "tool", time: iso(), tool: grep });
    await wait(500);
    put(c, { id: grep.id, kind: "tool", time: iso(), tool: { ...grep, status: "ok", result: "src/api/charges.ts:14" } });
    if (/run|test|rm|delete|install/i.test(text) && c.info.mode !== "bypassPermissions") {
      const cmd = /rm|delete/i.test(text) ? "rm -rf node_modules/.cache" : "pnpm test checkout";
      const bash = tool("Bash", "bash", cmd, { bash: { command: cmd } });
      put(c, { id: bash.id, kind: "tool", time: iso(), tool: bash });
      const d = await askUser(c, { id: nid("req"), kind: "tool", tool: c.info.agent === "codex" ? "Shell" : "Bash", summary: cmd, allowSession: true, call: bash });
      if (d.behavior === "deny") {
        put(c, { id: bash.id, kind: "tool", time: iso(), tool: { ...bash, status: "denied" } });
        return finish(c, t, d.message ? `Understood — ${d.message}` : "OK, I won't run it.");
      }
      await wait(700);
      put(c, { id: bash.id, kind: "tool", time: iso(), tool: { ...bash, status: "ok", bash: { command: cmd, output: " ✓ renders the total\n ✓ charges once\n\n Test Files  1 passed (1)", exitCode: 0 } } });
    }
    const edit = tool("Edit", "edit", "/home/agent/work/shop/src/checkout/PayButton.tsx", {
      edit: { path: "/home/agent/work/shop/src/checkout/PayButton.tsx", hunks: [{ oldStart: 12, oldLines: 1, newStart: 12, newLines: 3, lines: ["-  return <button onClick={onPay}>Pay</button>;", "+  const [pending, setPending] = useState(false);", "+  return <button disabled={pending} onClick={pay}>Pay</button>;"] }] },
    });
    put(c, { id: edit.id, kind: "tool", time: iso(), tool: edit });
    await wait(400);
    put(c, { id: edit.id, kind: "tool", time: iso(), tool: { ...edit, status: "ok" } });
    edited(c.sessionId, "src/checkout/PayButton.tsx", 2);
    await finish(c, t, "Done. `PayButton` now ignores clicks while a charge is in flight:\n\n```tsx\nconst [pending, setPending] = useState(false);\nreturn <button disabled={pending} onClick={pay}>Pay</button>;\n```");
  } catch (e) {
    put(c, { id: nid("e"), kind: "error", time: iso(), text: String(e) });
    setState(c, "ready");
  }
}

async function finish(c, t, text) {
  if (t.stopped) return;
  await stream(c, "assistant", text);
  c.turn = null;
  c.streaming = [];
  setState(c, "ready", { last: text.split("\n")[0], waiting: undefined });
  // The queue goes in once the turn is over.
  const next = c.info.queue[0];
  if (next) {
    c.info = { ...c.info, queue: c.info.queue.slice(1) };
    put(c, user(next.id, next.text, 0));
    void turn(c, next.text);
  }
}

function prompt(c, p, mode) {
  if (c.items.some((i) => i.id === p.id) || c.info.queue.some((q) => q.id === p.id)) return;
  const busy = c.info.state === "working" || c.info.state === "waiting";
  if (busy && mode !== "steer") {
    c.info = { ...c.info, queue: [...c.info.queue, { id: p.id, text: p.text, attachments: p.attachments, at: iso() }] };
    changed(c);
    return;
  }
  put(c, { ...user(p.id, p.text, 0), attachments: p.attachments });
  if (!busy) void turn(c, p.text);
}

// --- methods -----------------------------------------------------------------

const settings = {
  groups: [
    { key: "cloud", title: "Cloud" },
    { key: "sessions", title: "New sessions" },
    { key: "idle", title: "Idle & cost" },
    { key: "agents", title: "Agents" },
    { key: "speed", title: "Speed" },
    { key: "app", title: "Appearance" },
  ],
  settings: [
    { key: "driver", group: "cloud", label: "provider", hint: "where sessions run", detail: "AWS EC2 or Google Compute Engine.", default: "aws-ec2", kind: "choice", options: [{ value: "aws-ec2", label: "AWS", desc: "Amazon EC2" }, { value: "gcp-gce", label: "GCP", desc: "Google Compute Engine" }], noCustom: true, value: "aws-ec2", display: "AWS" },
    { key: "aws.region", group: "cloud", label: "region", hint: "where new VMs launch", detail: "New session VMs launch here.", default: "eu-central-1", kind: "choice", options: [{ value: "us-east-1", desc: "N. Virginia" }, { value: "eu-central-1", desc: "Frankfurt" }], noCustom: false, value: "eu-central-1", display: "eu-central-1" },
    { key: "aws.instance_type", group: "sessions", label: "machine", hint: "VM for new sessions", detail: "Resize a session any time.", default: "t4g.medium", kind: "machine", noCustom: false, value: "t4g.medium", display: "t4g.medium" },
    { key: "idle_timeout", group: "idle", label: "park after", hint: "quiet time before a session parks", detail: "", default: "30m", kind: "text", noCustom: false, value: "30m", display: "30m" },
    { key: "agents.default", group: "agents", label: "agent", hint: "who new chats talk to", detail: "", default: "claude", kind: "choice", options: [{ value: "claude", label: "Claude" }, { value: "codex", label: "Codex" }], noCustom: true, value: "claude", display: "Claude" },
    { key: "agents.claude_model", group: "agents", label: "Claude model", hint: "model new Claude chats run", detail: "", default: "", kind: "choice", options: [{ value: "opus", label: "Opus" }, { value: "sonnet", label: "Sonnet" }], noCustom: false, value: "opus", display: "Opus" },
    { key: "agents.claude_mode", group: "agents", label: "Claude permissions", hint: "what it does without asking", detail: "", default: "default", kind: "choice", options: [{ value: "default", label: "ask" }, { value: "acceptEdits", label: "accept edits" }, { value: "plan", label: "plan" }, { value: "bypassPermissions", label: "bypass" }], noCustom: true, value: "default", display: "ask" },
    { key: "speed.auto_refill", group: "speed", label: "refill pools", hint: "rebuild a pool that runs low on its own", detail: "", default: "off", kind: "choice", options: [{ value: "true", label: "on" }, { value: "false", label: "off" }], noCustom: true, value: "false", display: "off" },
    { key: "theme", group: "app", label: "accent color", hint: "", detail: "", default: "teal", kind: "choice", options: ["teal", "navy", "violet", "emerald", "orange", "crimson", "pink", "amber", "graphite"].map((v) => ({ value: v, label: v })), noCustom: true, value: "teal", display: "teal" },
  ],
};
const models = {
  claude: [
    { id: "default", name: "Default", description: "Opus 5.5 · best for everyday, complex tasks", efforts: ["low", "medium", "high", "xhigh", "max"], default: true },
    { id: "opus", name: "Opus 5.5", description: "For complex work", efforts: ["low", "medium", "high", "xhigh", "max"] },
    { id: "sonnet", name: "Sonnet 5.5", description: "Most efficient for simpler tasks", efforts: ["low", "medium", "high", "xhigh", "max"] },
    { id: "haiku", name: "Haiku 4.5", description: "Fastest for quick answers" },
  ],
  codex: [
    { id: "gpt-6.1-sol", name: "GPT-6.1 Sol", description: "Frontier reasoning", efforts: ["low", "medium", "high", "xhigh"], defaultEffort: "high", default: true },
    { id: "gpt-6-luna", name: "GPT-6 Luna", description: "Fast and efficient", efforts: ["low", "medium", "high"], defaultEffort: "medium" },
  ],
};
const modes = {
  claude: [
    { id: "default", name: "Ask", description: "Asks before edits and commands it isn't sure are safe." },
    { id: "acceptEdits", name: "Accept edits", description: "Edits files without asking; asks before commands." },
    { id: "plan", name: "Plan", description: "Plans first and asks you to approve the plan before it changes anything." },
    { id: "bypassPermissions", name: "Bypass", description: "Runs everything without asking." },
  ],
  codex: [
    { id: "readOnly", name: "Read only", description: "Reads and answers; asks before changing anything." },
    { id: "default", name: "Auto", description: "Works in the repo and on the network; asks before anything else." },
    { id: "plan", name: "Plan", description: "Plans first and asks you to approve the plan before it changes anything." },
    { id: "bypassPermissions", name: "Full access", description: "Runs everything without asking." },
  ],
};
const windows = new Map([
  ["i-01", [
    { id: "@2", index: 1, name: "setup", command: "bash", active: false, role: "setup" },
    { id: "@3", index: 2, name: "server", command: "node", active: false, role: "" },
  ]],
]);
const env = (id) => ({
  windows: windows.get(id) ?? [],
  at: iso(),
  resources: res(40 + Math.random() * 30, 2.6),
  ports: [{ port: 3000, process: "node", pid: 4411 }, { port: 5432, process: "docker-proxy", pid: 990 }],
  containers: [{ id: "c1", name: "shop-db", image: "postgres:16", state: "running", status: "Up 3 hours", ports: "0.0.0.0:5432->5432/tcp" }],
  processes: [{ pid: 4411, command: "node /home/agent/work/shop/node_modules/.bin/vite --host", cpu: 31.2, rss: 412 * 2 ** 20 }],
});
const terms = new Map();
let tid = 0;

const handlers = {
  initialize: () => ({ protocol: 1, version: "dev", configured: true, cloud: "AWS eu-central-1" }),
  "overview.get": () => overview(),
  "overview.watch": () => ({ sub: openSub("overview", ""), seq: 0, snapshot: overview() }),
  "settings.get": () => settings,
  "settings.set": (p) => {
    const s = settings.settings.find((x) => x.key === p.key);
    if (s) s.value = s.display = p.value;
    return settings;
  },
  "settings.machines": () => machines,
  "sessions.machines": () => machines,
  "repos.inspect": () => ({ ...checkouts[0], branch: "main", head: "a1b2c3d", dirty: 4, base: "main" }),
  "logs.setup": () => ({ text: "==> pnpm install\nProgress: resolved 1204, reused 1180\n==> docker compose up -d\npier setup: done\n", truncated: false }),
  "jobs.list": () => [],
  "sessions.create": (p) => ({ id: nid("job"), kind: "create", repo: "shop", session: p.branch, state: "running", pid: 1, startedAt: iso(), logPath: "/tmp/x" }),
  "sessions.resume": (p) => {
    const s = sessions.find((x) => x.id === p.id);
    if (s) {
      s.state = "idle";
      s.label = "running";
      emit("overview", "", overview());
    }
    return null;
  },
  "env.watch": (p) => ({ sub: openSub("env", p.id), seq: 0, snapshot: env(p.id) }),
  "chats.watch": (p) => ({ sub: openSub("chats", p.id), seq: 0, snapshot: { chats: chatsOf(p.id).map((c) => c.info), live: live(p.id) } }),
  "chat.watch": (p) => {
    const c = chats.get(p.chatId);
    const snapshot = c
      ? { items: c.items.slice(-300), more: c.items.length > 300, chat: c.info, streaming: c.streaming, live: live(c.sessionId), asOf: live(c.sessionId) ? undefined : c.info.since }
      : { items: [], more: false, live: live(p.sessionId) };
    return { sub: openSub("chat", p.chatId), seq: 0, snapshot };
  },
  "chat.history": (p) => {
    const c = chats.get(p.chatId);
    const end = c ? c.items.findIndex((i) => i.id === p.before) : -1;
    if (!c || end < 0) return { items: [], more: false };
    const from = Math.max(0, end - (p.limit || 200));
    return { items: c.items.slice(from, end), more: from > 0 };
  },
  "chat.create": (p) => {
    if (!live(p.sessionId)) return { chatId: p.chatId, job: wakeJob(p.sessionId) };
    if (!chats.has(p.chatId)) {
      addChat(p.sessionId, { id: p.chatId, agent: p.agent, title: (p.prompt?.text ?? "New chat").split("\n")[0].slice(0, 48), model: p.model, effort: p.effort, mode: p.mode || "default", state: "ready", since: iso(), created: iso() }, []);
      changed(chats.get(p.chatId));
    }
    if (p.prompt?.text) prompt(chats.get(p.chatId), p.prompt, "queue");
    return { chatId: p.chatId };
  },
  "chat.send": (p) => {
    const c = chats.get(p.chatId);
    if (!c) throw new Error(`no chat ${p.chatId}`);
    if (!live(c.sessionId)) return { job: wakeJob(c.sessionId) };
    prompt(c, p.prompt, p.mode);
    return {};
  },
  "chat.decide": (p) => {
    const c = chats.get(p.chatId);
    if (!c?.decide || c.info.pending?.id !== p.requestId) throw new Error("that request is no longer waiting");
    c.decide(p);
    return null;
  },
  "chat.interrupt": (p) => {
    const c = chats.get(p.chatId);
    if (c?.turn) {
      c.turn.stopped = true;
      c.turn = null;
      c.decide = null;
      c.streaming = [];
      c.info = { ...c.info, pending: undefined };
      put(c, { id: nid("s"), kind: "status", time: iso(), text: "interrupted" });
      setState(c, "ready");
    }
    return null;
  },
  "chat.configure": (p) => {
    const c = chats.get(p.chatId);
    for (const k of ["model", "effort", "mode", "title"]) if (p[k]) c.info = { ...c.info, [k]: p[k] };
    changed(c);
    return null;
  },
  "chat.dequeue": (p) => {
    const c = chats.get(p.chatId);
    c.info = { ...c.info, queue: c.info.queue.filter((q) => q.id !== p.promptId) };
    changed(c);
    return null;
  },
  "chat.close": (p) => {
    const c = chats.get(p.chatId);
    chats.delete(p.chatId);
    if (c) emit("chats", c.sessionId, { chats: chatsOf(c.sessionId).map((x) => x.info), live: true });
    return null;
  },
  "chat.takeover": () => ({ windowId: "@9" }),
  "chats.window": () => ({ windowId: "@8" }),
  "chat.compose": () => ({ text: "The `test (ubuntu)` check fails:\n\n```\nFAIL src/auth/mw.test.ts\n  expected 1 redirect, got 2\n```\n\nFix it and push." }),
  "agents.models": (p) => ({ agent: p.agent, models: models[p.agent] ?? [], modes: modes[p.agent] ?? [] }),
  "files.upload": (p) => (live(p.sessionId) ? { name: p.name, path: `/home/agent/.pier/uploads/${p.name}`, mime: p.mime } : { name: p.name, path: `staged:/tmp/${p.name}`, mime: p.mime }),
  "files.list": (p) => ({ paths: [...repoFiles, ...filesOf(p.sessionId).filter((f) => !repoFiles.includes(f.path)).map((f) => f.path)], truncated: false, asOf: live(p.sessionId) ? undefined : iso(20 * 60e3) }),
  "git.changes": (p) => changes(p.id),
  "git.watch": (p) => ({ sub: openSub("git", p.id), seq: 0, snapshot: changes(p.id) }),
  "git.diff": (p) => {
    const f = filesOf(p.sessionId).find((x) => x.path === p.path);
    const extra = Array.from({ length: Math.max(0, (f?.added ?? 4) - 4) }, (_, i) => `+  // edit ${i + 1}\n`).join("");
    const before = p.context ? Array.from({ length: 7 }, (_, i) => ` // line ${i + 1}\n`).join("") : "";
    const start = p.context ? 1 : 8;
    return { path: p.path, live: live(p.sessionId), asOf: live(p.sessionId) ? undefined : iso(20 * 60e3),
      patch: `diff --git a/${p.path} b/${p.path}\n--- a/${p.path}\n+++ b/${p.path}\n@@ -${start},${p.context ? 10 : 3} +${start},${(p.context ? 13 : 6) + extra.split("\n").length - 1} @@\n${before} export function PayButton({ onPay }: Props) {\n-  return <button onClick={onPay}>Pay</button>;\n+  const [pending, setPending] = useState(false);\n+  return <button disabled={pending} onClick={pay}>Pay</button>;\n${extra} }\n` };
  },
  "files.read": (p) => {
    if (!live(p.sessionId) && !filesOf(p.sessionId).some((f) => f.path === p.path)) throw rpcError("parked", "Parked — wake it to read it");
    const text = p.at === "base" ? "export function PayButton() {}\n" : fileText(p.path);
    return { path: p.path, size: text.length, sha: live(p.sessionId) ? "abc" : "", text, asOf: live(p.sessionId) ? undefined : iso(20 * 60e3) };
  },
  "search.watch": (p) => {
    const sub = openSub("search", `${p.sessionId}:${subSeq}`);
    const key = subs.get(sub).key;
    const all = [
      { path: "src/api/charges.ts", line: 14, col: 9, text: "  await createCharge(order, { idempotencyKey })" },
      { path: "src/api/charges.test.ts", line: 3, col: 10, text: "import { createCharge } from './charges'" },
      { path: "src/checkout/PayButton.tsx", line: 21, col: 5, text: "    createCharge(order)" },
    ];
    const q = p.caseSensitive ? p.query : p.query.toLowerCase();
    const found = all.filter((m) => (p.caseSensitive ? m.text : m.text.toLowerCase()).includes(q));
    setTimeout(() => emit("search", key, { matches: found.slice(0, 2), truncated: false, done: false }), fast ? 5 : 120);
    setTimeout(() => emit("search", key, { matches: found.slice(2), truncated: false, done: true }), fast ? 10 : 400);
    return { sub, seq: 0, snapshot: { matches: [], truncated: false, done: false } };
  },
  "pr.get": () => ({ id: "PR_41", number: 41, title: "Fix the login redirect loop", url: "https://github.com/acme/shop/pull/41", state: "open", headOid: "abc", baseRef: "main", additions: 12, deletions: 3, checks: "failing", checksPassed: 3, checksTotal: 4, review: "changes_requested", mergeable: "blocked", body: "",
    runs: [{ name: "test (ubuntu)", state: "failing", url: "u", jobId: 1, runId: 9 }, { name: "lint", state: "passing", url: "u", jobId: 2, runId: 9 }],
    threads: [{ path: "src/auth/mw.ts", line: 12, resolved: false, outdated: false, comments: [{ author: "maria", body: "Read the cookie name from config instead." }] }],
    reviews: [{ author: "maria", state: "changes_requested", body: "Close — one thing about the cookie name." }] }),
  "pr.log": () => ({ text: "Run pnpm test\n FAIL src/auth/mw.test.ts\nError: Process completed with exit code 1." }),
  "cli.status": () => ({ link: "/usr/local/bin/pier", installed: false }),
  "prompts.get": (p) => ({
    prompts: promptKinds.map((k) => {
      const own = (p.repo ? repoPrompts.get(p.repo)?.[k.key] : globalPrompts[k.key]) ?? "";
      const inherited = p.repo ? globalPrompts[k.key] || k.def : k.def;
      return { key: k.key, title: k.title, description: k.desc, own, inherited, effective: own || inherited };
    }),
  }),
  "prompts.set": (p) => {
    const set = p.repo ? (repoPrompts.get(p.repo) ?? repoPrompts.set(p.repo, {}).get(p.repo)) : globalPrompts;
    set[p.key] = p.text.trim();
    return null;
  },
  "setup.detect": () => ({
    configured: true,
    draft: { cloud: "aws-ec2", awsProfile: "dev", awsRegion: "eu-central-1", machine: "t4g.medium", diskGib: 30, idleTimeout: "30m", unattendedCap: "8h", speed: "fast", manifest: [] },
    awsTools: [{ name: "aws", hint: "brew install awscli", found: true }, { name: "session-manager-plugin", hint: "brew install --cask session-manager-plugin", found: true }],
    gcpTools: [{ name: "gcloud", hint: "brew install --cask google-cloud-sdk", found: false }],
    awsProfiles: ["default", "dev", "prod"],
    agents: [".claude", ".codex"],
    needsClaudeToken: true,
    hasClaudeToken: true,
  }),
  "setup.check": (p) => ({ identity: `arn:aws:iam::111122223333:user/dev (${p.profile})`, region: "eu-central-1" }),
  "setup.defaults": (d) => ({
    rows: [
      { key: "machine", label: "Machine", value: d.machine, note: "2 vCPU · 4 GiB · ~$0.04/h while running" },
      { key: "disk", label: "Disk", value: `${d.diskGib} GiB`, note: "~$2.40/mo per session, parked or not" },
      { key: "idle", label: "Parks after", value: d.idleTimeout, note: "idle with nobody attached" },
      { key: "cap", label: "Unattended cap", value: d.unattendedCap, note: "a busy session nobody watches parks after this" },
    ],
    machines: [
      { type: "t4g.medium", cpu: "2", mem: "4", cost: "~$0.04/h" },
      { type: "t4g.large", cpu: "2", mem: "8", cost: "~$0.07/h" },
      { type: "t4g.xlarge", cpu: "4", mem: "16", cost: "~$0.13/h" },
    ],
  }),
  "setup.apply": async () => {
    await wait(600);
    return { created: [], existed: ["security group pier", "instance profile pier"], checks: [{ name: "aws login", status: "ok", detail: "dev" }], skills: [".claude"], adopted: {} };
  },
  "cli.status": () => ({ link: "/usr/local/bin/pier", installed: true }),
  "proxy.watch": () => ({ sub: openSub("proxy", ""), seq: 0, snapshot: { running: true, caTrusted: false, sessions: [{ id: "i-01", host: "checkout-flow.pier", ports: [3000], localhost: [3000] }] } }),
  "tmux.windows": (p) => windows.get(p.sessionId) ?? [],
  "tmux.newWindow": (p) => {
    const list = windows.get(p.sessionId) ?? [];
    const w = { id: `@${10 + list.length}`, index: list.length + 1, name: "zsh", command: "zsh", active: false, role: "" };
    windows.set(p.sessionId, [...list, w]);
    emit("env", p.sessionId, env(p.sessionId));
    return w;
  },
  "tmux.killWindow": (p) => {
    windows.set(p.sessionId, (windows.get(p.sessionId) ?? []).filter((w) => w.id !== p.window));
    emit("env", p.sessionId, env(p.sessionId));
    return null;
  },
  "term.close": (p) => {
    terms.get(p.termId)?.kill();
    terms.delete(p.termId);
    return null;
  },
};
const machines = [
  { type: "t4g.medium", cpu: "2", mem: "4", cost: "$0.04/h" },
  { type: "t4g.large", cpu: "2", mem: "8", cost: "$0.07/h" },
];

function wakeJob(sessionId) {
  const s = sessions.find((x) => x.id === sessionId);
  setTimeout(() => {
    if (s) {
      s.state = "idle";
      s.label = "running";
      emit("overview", "", overview());
    }
  }, fast ? 0 : 1500);
  return { id: nid("job"), kind: "deliver", repo: s?.repo ?? "", session: s?.name ?? "", state: "running", pid: 1, startedAt: iso(), logPath: "/tmp/x" };
}

function openTerm(m) {
  const id = `t${++tid}`;
  const child = spawn("python3", ["-c", 'import pty; pty.spawn(["/bin/zsh", "-f"])'], {
    env: { ...process.env, TERM: "xterm-256color", PS1: "agent@shop %~ %# " },
  });
  terms.set(id, child);
  let seq = 0;
  child.stdout.on("data", (b) => send({ method: "term.output", params: { termId: id, seq: ++seq, data: b.toString("base64") } }));
  child.on("exit", (code) => send({ method: "term.exit", params: { termId: id, code: code ?? 0 } }));
  send({ id: m.id, result: { termId: id } });
}

const rl = readline.createInterface({ input: process.stdin });
rl.on("line", (line) => {
  const m = JSON.parse(line);
  if (m.id === undefined) {
    if (m.method === "term.input") terms.get(m.params.termId)?.stdin.write(Buffer.from(m.params.data, "base64"));
    if (m.method === "sub.cancel") subs.delete(m.params.sub);
    return;
  }
  if (m.method === "term.open" || m.method === "term.openLocal") return openTerm(m);
  const h = handlers[m.method];
  if (!h) return send({ id: m.id, error: { code: -32000, message: `mock: ${m.method} isn't here`, data: { kind: "failed" } } });
  try {
    send({ id: m.id, result: h(m.params ?? {}) ?? null });
  } catch (e) {
    send({ id: m.id, error: { code: -32000, message: e.message, data: { kind: e.kind ?? "failed", message: e.message } } });
  }
});
rl.on("close", () => process.exit(0));
