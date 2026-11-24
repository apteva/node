// Apteva Node device UI. One WebSocket to the local daemon; every message
// is a full snapshot, and each screen region re-renders from it. The text
// input lives outside the re-rendered regions so typing is never disturbed.

import { WidgetHost, type WidgetView, type AppResult, type HostServices } from "./surfaces.ts";
import { SetupUI, type SetupView } from "./setup.ts";
import { Keyboard } from "./osk.ts";
import { holdButton } from "./hold.ts";
import { agentGlyph } from "./agentmark.ts";
import { AgentsPage, type AgentInfo } from "./agents.ts";

type LinkState = "unpaired" | "standalone" | "connecting" | "connected" | "disconnected" | "rejected";

interface Snapshot {
  node: { name: string; version: string; backend: string; sim: boolean };
  link: { state: LinkState; server?: string; since: string; error?: string; retry_at?: string };
  queued: number;
  agent: { project_id?: string; id?: string; name?: string; icon?: string; state: string; detail?: string; since: string };
  agents: AgentInfo[] | null;
  projects: { id: string; name: string }[] | null;
  conversation: { id: string; from: "you" | "agent" | "system"; text: string; at: string }[] | null;
  approvals: { id: string; capability: string; tool: string; summary: string; mode: "screen" | "hold"; created_at: string; expires_at: string }[] | null;
  capabilities: Cap[] | null;
  actions: { at: string; by: string; tool: string; ok: boolean; summary: string }[] | null;
  widgets: WidgetView[] | null;
  system: { hostname: string; model: string; cpu_temp_c?: number; uptime_s: number; load_avg_1m?: number };
  setup: SetupView;
  sent_photo?: { capability: string; to: string; at: string; width: number; height: number };
}

interface Cap {
  id: string;
  kind: "relay" | "led" | "button" | "i2c_sensor" | "camera";
  description?: string;
  pin?: number;
  confirm: string;
  safe_state?: string;
  on?: boolean;
  pressed?: boolean;
  reading?: Record<string, number>;
  read_at?: string;
  error?: string;
  photo_at?: string;
  camera_model?: string;
  thresholds?: { field: string; above?: number; below?: number }[];
}

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

let snap: Snapshot | null = null;
let ws: WebSocket | null = null;
let currentScreen = "presence";
let dismissedSheet = new Set<string>();
let lastConversationKey = "";
let heldPad: string | null = null;
let lastChipsHTML = "";
let lastApprovalsHTML = "";

// ---------- socket ----------

function connect() {
  ws = new WebSocket(`ws://${location.host}/ws`);
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.type === "snapshot") {
      snap = msg.data as Snapshot;
      render();
    }
  };
  ws.onclose = () => {
    ws = null;
    renderDaemonDown();
    setTimeout(connect, 1500);
  };
}

function send(cmd: Record<string, unknown>) {
  if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify(cmd));
}

// ---------- helpers ----------

function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}

function hhmm(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function ago(iso?: string): string {
  if (!iso) return "";
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  return `${Math.round(s / 3600)}h ago`;
}

function duration(sec: number): string {
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m`;
}

const icon = {
  lock: `<svg viewBox="0 0 24 24" width="13" height="13" aria-hidden="true"><rect x="5" y="11" width="14" height="9" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3" fill="none" stroke="currentColor" stroke-width="2"/></svg>`,
};

const linkLabel: Record<LinkState, string> = {
  unpaired: "Not paired",
  standalone: "Standalone",
  connecting: "Connecting",
  connected: "Online",
  disconnected: "Offline",
  rejected: "Rejected",
};

const stateLabel: Record<string, string> = {
  idle: "Here with you",
  thinking: "Thinking",
  acting: "Working",
  speaking: "Speaking",
};

const kindLabel: Record<Cap["kind"], string> = {
  relay: "Relay", led: "LED", button: "Button", i2c_sensor: "Sensor", camera: "Camera",
};

const readingLabel: Record<string, [string, string]> = {
  temperature_c: ["°C", "Temp"],
  humidity_pct: ["%", "Humidity"],
  pressure_hpa: ["hPa", "Pressure"],
};

// ---------- render ----------

let lastMode = "";

function render() {
  if (!snap) return;
  renderMode();
  setupUI.render();
  renderTop();
  renderPresence();
  renderTalk();
  renderApprovals();
  renderSheet();
  renderSentPhoto();
  renderWidgets();
  agentsPage.render();
  if (currentScreen === "hardware") renderHardware();
}

// Standalone has no agent: no presence, talk or approvals, just the
// hardware under a big clock.
function renderMode() {
  const mode = snap!.setup.mode;
  if (mode !== lastMode) {
    lastMode = mode;
    document.body.dataset.mode = mode;
    if (mode === "standalone") show("hardware");
    else if (mode === "connected") show("presence");
  }
  const notice = snap!.setup.notice;
  $("notice").hidden = !notice;
  $("notice-text").textContent = notice ?? "";
}

// ---------- app widgets ----------

function titleCase(s: string): string {
  return s ? s[0].toUpperCase() + s.slice(1) : s;
}

let toastTimer: ReturnType<typeof setTimeout> | undefined;

// Sheets opened for an app say which app is asking, and sit under the
// device's own approval sheet: an app can never draw an approval.
function appSheet(opts: { app: string; title: string; message?: string; okLabel: string; danger?: boolean; fields?: { name: string; type: string; label?: string; placeholder?: string; required?: boolean; default?: unknown; options?: { label: string; value: unknown }[] }[] }): Promise<Record<string, unknown> | null> {
  return new Promise((resolve) => {
    const sheet = $("app-sheet");
    const form = $<HTMLFormElement>("app-sheet-form");
    $("app-sheet-kicker").textContent = `${titleCase(opts.app)} app`;
    $("app-sheet-title").textContent = opts.title;
    $("app-sheet-message").textContent = opts.message ?? "";
    const ok = $<HTMLButtonElement>("app-sheet-ok");
    ok.textContent = opts.okLabel;
    ok.classList.toggle("danger-solid", !!opts.danger);
    $("app-sheet-fields").innerHTML = (opts.fields ?? [])
      .map((f) => {
        const label = esc(f.label ?? f.name);
        const req = f.required ? "required" : "";
        const def = f.default == null ? "" : String(f.default);
        switch (f.type) {
          case "boolean":
            return `<label class="field check"><input type="checkbox" name="${esc(f.name)}" ${f.default === true ? "checked" : ""}><span>${label}</span></label>`;
          case "choice":
            return `<label class="field"><span>${label}</span><select name="${esc(f.name)}" ${req}>${(f.options ?? [])
              .map((o) => `<option value="${esc(JSON.stringify(o.value))}" ${o.value === f.default ? "selected" : ""}>${esc(o.label)}</option>`)
              .join("")}</select></label>`;
          case "multiline":
            return `<label class="field"><span>${label}</span><textarea name="${esc(f.name)}" rows="3" placeholder="${esc(f.placeholder ?? "")}" ${req}>${esc(def)}</textarea></label>`;
          default: {
            const type = f.type === "integer" || f.type === "number" ? "number" : f.type === "url" ? "url" : "text";
            const step = f.type === "number" ? `step="any"` : "";
            return `<label class="field"><span>${label}</span><input type="${type}" ${step} name="${esc(f.name)}" value="${esc(def)}" placeholder="${esc(f.placeholder ?? "")}" ${req}></label>`;
          }
        }
      })
      .join("");
    sheet.hidden = false;
    (form.querySelector("input,select,textarea") as HTMLElement | null)?.focus();

    const done = (v: Record<string, unknown> | null) => {
      sheet.hidden = true;
      form.onsubmit = null;
      $("app-sheet-cancel").onclick = null;
      resolve(v);
    };
    $("app-sheet-cancel").onclick = () => done(null);
    form.onsubmit = (ev) => {
      ev.preventDefault();
      const out: Record<string, unknown> = {};
      for (const f of opts.fields ?? []) {
        const el = form.elements.namedItem(f.name) as HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement | null;
        if (!el) continue;
        if (f.type === "boolean") out[f.name] = (el as HTMLInputElement).checked;
        else if (f.type === "choice") out[f.name] = JSON.parse(el.value);
        else if (f.type === "integer") out[f.name] = el.value === "" ? null : parseInt(el.value, 10);
        else if (f.type === "number") out[f.name] = el.value === "" ? null : Number(el.value);
        else out[f.name] = el.value;
      }
      done(out);
    };
  });
}

const hostServices: HostServices = {
  async request(app, req): Promise<AppResult> {
    const res = await fetch("/api/app-request", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ app, ...req }),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || `request failed (${res.status})`);
    return data as AppResult;
  },
  toast(message, toneName = "ok") {
    const t = $("toast");
    t.textContent = message;
    t.dataset.tone = toneName;
    t.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => (t.hidden = true), 2600);
  },
  async confirm(o) {
    return (await appSheet({ app: o.app, title: o.title, message: o.message, okLabel: o.confirmLabel ?? "OK", danger: o.destructive })) !== null;
  },
  form(o) {
    return appSheet({ app: o.app, title: o.title, okLabel: "Send", fields: o.fields });
  },
};

const widgetHost = new WidgetHost($("widgets"), hostServices);
const agentsPage = new AgentsPage({
  projects: () => snap?.projects ?? [],
  activeProjectID: () => snap?.agent.project_id,
  selectProject(id) { send({ type: "select_project", id }); },
  agents: () => snap?.agents ?? [],
  activeID: () => snap?.agent.id,
  online: () => snap?.link.state === "connected",
  select(id) {
    if (id !== snap?.agent.id) send({ type: "select_agent", id });
    show("talk");
  },
});
const setupUI = new SetupUI({ snap: () => snap as never, toast: (m, t) => hostServices.toast(m, t) });
new Keyboard($("osk"));

function renderWidgets() {
  const list = snap!.widgets ?? [];
  document.querySelector(".screen[data-screen=presence]")!.classList.toggle("with-widgets", list.length > 0);
  widgetHost.update(list);
}

function renderDaemonDown() {
  const pill = $("link-pill");
  pill.dataset.state = "disconnected";
  pill.querySelector("span")!.textContent = "Daemon stopped";
}

function renderTop() {
  const s = snap!;
  $("node-name").textContent = s.node.name + (s.node.sim ? " · sim" : "");
  const pill = $("link-pill");
  pill.dataset.state = s.link.state;
  let label = linkLabel[s.link.state] ?? s.link.state;
  if (s.queued > 0 && s.link.state !== "connected") label += ` · ${s.queued} queued`;
  pill.querySelector("span")!.textContent = label;
  pill.title = s.link.error ?? s.link.server ?? "";
}

function renderPresence() {
  const s = snap!;
  const online = s.link.state === "connected";
  const mark = $("orb");
  mark.dataset.state = online ? s.agent.state || "idle" : "offline";
  const icon = s.agent.icon || "robot";
  if (mark.dataset.icon !== icon) {
    mark.dataset.icon = icon;
    mark.innerHTML = agentGlyph(icon);
  }

  $("agent-name").textContent = s.agent.name || (online ? "Your agent" : "No agent");

  let state: string;
  if (online) {
    state = s.agent.detail || stateLabel[s.agent.state] || s.agent.state;
  } else if (s.link.state === "unpaired") {
    state = "This device isn't paired with an Apteva instance yet";
  } else if (s.link.state === "rejected") {
    state = s.link.error || "The server refused this device";
  } else if (s.link.state === "connecting") {
    state = "Reaching your Apteva instance…";
  } else {
    const retry = s.link.retry_at ? Math.max(0, Math.round((new Date(s.link.retry_at).getTime() - Date.now()) / 1000)) : 0;
    state = retry > 0 ? `Link lost — retrying in ${retry}s` : "Link lost — retrying";
  }
  $("agent-state").textContent = state;

  const said = [...(s.conversation ?? [])].reverse().find((l) => l.from === "agent");
  $("last-said").textContent = said ? said.text : "";
}

function renderTalk() {
  const lines = snap!.conversation ?? [];
  const key = lines.map((l) => l.id).join(",");
  if (key !== lastConversationKey) {
    lastConversationKey = key;
    const box = $("transcript");
    box.innerHTML = lines.length
      ? lines
          .map((l) => `<div class="bubble ${l.from}">${esc(l.text)}${l.from !== "system" ? `<time>${hhmm(l.at)}</time>` : ""}</div>`)
          .join("")
      : `<div class="empty">Talk to your agent here. It can use this device's hardware when you allow it.</div>`;
    box.scrollTop = box.scrollHeight;
  }
  const ph = `Say something to ${snap!.agent.name || "your agent"}`;
  const input = $<HTMLInputElement>("composer-input");
  if (input.placeholder !== ph) input.placeholder = ph;
  renderChips();
}

function renderChips() {
  const caps = snap!.capabilities ?? [];
  const chips: string[] = [];
  if (caps.some((c) => c.kind === "camera")) chips.push("Take a photo");
  if (caps.some((c) => c.kind === "i2c_sensor")) chips.push("What's the temperature?");
  const out = caps.find((c) => c.kind === "relay" || c.kind === "led");
  if (out) chips.push(`Turn ${out.description ?? out.id} ${out.on ? "off" : "on"}`);
  chips.push("What can you do here?");
  const html = chips.map((c) => `<button class="chip" data-say="${esc(c)}">${esc(c)}</button>`).join("");
  if (html !== lastChipsHTML) {
    lastChipsHTML = html;
    $("chips").innerHTML = html;
  }
}

function approvalActions(a: NonNullable<Snapshot["approvals"]>[number]): string {
  if (a.mode === "hold") {
    return `<button class="btn danger" data-deny="${a.id}">Deny</button>
      ${holdButton("Hold to allow", `data-hold-allow="${a.id}"`)}`;
  }
  return `<button class="btn danger" data-deny="${a.id}">Deny</button>
    <button class="btn primary" data-allow="${a.id}">Allow</button>`;
}

function remaining(a: { created_at: string; expires_at: string }): number {
  const total = new Date(a.expires_at).getTime() - new Date(a.created_at).getTime();
  const left = new Date(a.expires_at).getTime() - Date.now();
  return Math.max(0, Math.min(1, left / total));
}

function renderApprovals() {
  const list = snap!.approvals ?? [];
  const badge = $("approvals-badge");
  badge.hidden = list.length === 0;
  badge.textContent = String(list.length);
  const html = list.length
    ? list
        .map(
          (a) => `<div class="card">
          <div class="approval-title">${esc(a.summary)}</div>
          <div class="approval-meta">${esc(a.capability)} · ${a.mode === "hold" ? "hold to allow" : "tap to decide"} · asked at ${hhmm(a.created_at)}</div>
          <div class="countdown"><i data-countdown="${a.id}"></i></div>
          <div class="approval-actions">${approvalActions(a)}</div>
        </div>`,
        )
        .join("")
    : `<div class="empty">Nothing waiting. When your agent wants to use protected hardware, you'll decide here.</div>`;
  if (html !== lastApprovalsHTML) {
    lastApprovalsHTML = html;
    $("approvals").innerHTML = html;
  }
}

function renderSheet() {
  const list = (snap!.approvals ?? []).filter((a) => !dismissedSheet.has(a.id));
  const sheet = $("sheet");
  const a = list[0];
  if (!a) {
    sheet.hidden = true;
    delete sheet.dataset.id;
    return;
  }
  sheet.hidden = false;
  if (sheet.dataset.id === a.id) return;
  sheet.dataset.id = a.id;
  $("sheet-title").textContent = a.summary;
  const cap = (snap!.capabilities ?? []).find((c) => c.id === a.capability);
  $("sheet-meta").textContent = `${snap!.agent.name || "Your agent"} wants to use ${cap?.description ?? a.capability}.` + (list.length > 1 ? ` ${list.length - 1} more waiting.` : "");
  $("sheet-actions").innerHTML = approvalActions(a);
}

// Show each photo that leaves the device for a few seconds; tap to close.
const SENT_PHOTO_MS = 8000;
let sentShownKey = "";
let sentDismissedKey = "";
let sentTimer: number | undefined;

function renderSentPhoto() {
  const p = snap!.sent_photo;
  const el = $("sent-photo");
  const key = p ? `${p.capability}@${p.at}` : "";
  const age = p ? Date.now() - new Date(p.at).getTime() : Infinity;
  if (!p || key === sentDismissedKey || age > SENT_PHOTO_MS) {
    el.hidden = true;
    return;
  }
  if (key !== sentShownKey) {
    sentShownKey = key;
    $<HTMLImageElement>("sent-photo-img").src = `/photo/${encodeURIComponent(p.capability)}?t=${encodeURIComponent(p.at)}`;
    const cap = (snap!.capabilities ?? []).find((c) => c.id === p.capability);
    $("sent-photo-meta").textContent = `From ${cap?.description ?? p.capability} to ${p.to} · ${p.width}×${p.height}`;
    el.onclick = () => {
      sentDismissedKey = key;
      el.hidden = true;
    };
    clearTimeout(sentTimer);
    sentTimer = window.setTimeout(() => { if (snap) renderSentPhoto(); }, SENT_PHOTO_MS - age + 50);
  }
  el.hidden = false;
}

function capTile(c: Cap, sim: boolean): string {
  const lock = c.confirm !== "none" ? `<span class="lock" title="Agent needs your approval">${icon.lock}${c.confirm === "button" ? "press" : "ask"}</span>` : "";
  const head = `<div class="tile-head"><span class="tile-id">${esc(c.id)}</span><span class="tile-kind">${lock || kindLabel[c.kind]}</span></div>`;
  const desc = c.description ? `<div class="tile-desc">${esc(c.description)}</div>` : "";
  const footBits: string[] = [];
  if (c.pin !== undefined) footBits.push(`GPIO ${c.pin}`);
  if (c.safe_state) footBits.push(`safe ${c.safe_state}`);
  const foot = (body = "") => `<div class="tile-foot"><span>${footBits.join(" · ")}</span>${body}</div>`;

  switch (c.kind) {
    case "relay":
    case "led":
      return `<div class="tile">${head}${desc}
        <div class="tile-foot"><span>${footBits.join(" · ")}</span>
        <button class="switch" role="switch" aria-checked="${!!c.on}" aria-label="Toggle ${esc(c.id)}" data-toggle="${c.id}"></button></div></div>`;
    case "button":
      return `<div class="tile">${head}${desc}
        ${sim ? `<button class="pad${heldPad === c.id ? " down" : ""}" data-pad="${c.id}">Hold to press</button>` : ""}
        ${foot(`<span class="pressed-dot${c.pressed ? " on" : ""}"></span>`)}</div>`;
    case "i2c_sensor": {
      const r = c.reading ?? {};
      const cells = Object.keys(readingLabel)
        .filter((k) => k in r)
        .map((k) => `<div class="reading"><b>${r[k].toFixed(k === "pressure_hpa" ? 0 : 1)}<small>${readingLabel[k][0]}</small></b><span>${readingLabel[k][1]}</span></div>`)
        .join("");
      return `<div class="tile wide">${head}${desc}
        ${c.error ? `<div class="err">${esc(c.error)}</div>` : `<div class="readings">${cells}</div>`}
        ${foot(`<span>${c.read_at ? ago(c.read_at) : ""}</span>`)}</div>`;
    }
    case "camera":
      return `<div class="tile wide">${head}${desc}
        ${c.error ? `<div class="err">${esc(c.error)}</div>` : ""}
        ${c.photo_at ? `<img class="photo" alt="Last photo from ${esc(c.id)}" src="/photo/${c.id}?t=${encodeURIComponent(c.photo_at)}">` : `<div class="photo-empty">No photo yet</div>`}
        ${foot(`<span>${[c.camera_model ? `<span class="cam-model">${esc(c.camera_model)}</span>` : "", c.photo_at ? ago(c.photo_at) : ""].filter(Boolean).join(" · ")}</span>`)}</div>`;
  }
}

let lastTilesHTML = "";

function renderHardware() {
  const s = snap!;
  const sys = s.system;
  const sysTile = `<div class="tile wide">
    <div class="tile-head"><span class="tile-id">${esc(sys.model || "This device")}</span><span class="tile-kind">System</span></div>
    <div class="sys">
      ${sys.cpu_temp_c !== undefined ? `<div class="reading"><b>${sys.cpu_temp_c.toFixed(0)}<small>°C</small></b><span>CPU</span></div>` : ""}
      ${sys.load_avg_1m !== undefined ? `<div class="reading"><b>${sys.load_avg_1m.toFixed(2)}</b><span>Load</span></div>` : ""}
      <div class="reading"><b>${duration(sys.uptime_s)}</b><span>Uptime</span></div>
    </div>
    <div class="tile-foot"><span>${esc(s.node.backend)} · v${esc(s.node.version)}</span></div>
  </div>`;
  const html = (s.capabilities ?? []).map((c) => capTile(c, s.node.sim)).join("") + sysTile;
  if (html !== lastTilesHTML) {
    lastTilesHTML = html;
    $("tiles").innerHTML = html;
  }

  const actions = [...(s.actions ?? [])].reverse();
  $("actions").innerHTML = actions.length
    ? actions
        .map(
          (a) => `<li><time>${hhmm(a.at)}</time><span class="${a.ok ? "" : "fail"}">${esc(a.summary || a.tool)}</span><span class="by ${a.by}">${a.by}</span></li>`,
        )
        .join("")
    : `<li><span></span><span class="tile-desc">No actions yet.</span><span></span></li>`;
}

// ---------- interaction ----------

function show(name: string) {
  // Tapping Agents again from an agent's page goes back to the grid.
  if (name === "agents") agentsPage.show(null);
  currentScreen = name;
  document.querySelectorAll<HTMLElement>(".screen").forEach((el) => (el.hidden = el.dataset.screen !== name));
  document.querySelectorAll<HTMLButtonElement>(".tabs button").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
  if (name === "hardware") {
    lastTilesHTML = "";
    renderHardware();
  }
  if (name === "talk") {
    const box = $("transcript");
    box.scrollTop = box.scrollHeight;
  }
}

function say(text: string) {
  text = text.trim();
  if (!text) return;
  send({ type: "message", text });
  if (currentScreen !== "talk") show("talk");
}

document.addEventListener("click", (ev) => {
  const t = (ev.target as HTMLElement).closest<HTMLElement>("button");
  if (!t) return;
  if (t.dataset.tab) return show(t.dataset.tab);
  if (t.dataset.open) return setupUI.openSettings(t.dataset.open === "connect" ? "connect" : undefined);
  if (t.dataset.dismissNotice !== undefined) return void fetch("/api/setup/notice/dismiss", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
  if (t.dataset.say) return say(t.dataset.say);
  if (t.dataset.allow) return send({ type: "approve", id: t.dataset.allow, allow: true });
  if (t.dataset.deny) {
    dismissedSheet.add(t.dataset.deny);
    return send({ type: "approve", id: t.dataset.deny, allow: false });
  }
  if (t.dataset.toggle) return send({ type: "toggle", capability: t.dataset.toggle });
});

// Hold-to-allow on approvals.
document.addEventListener("held", (ev) => {
  const id = (ev.target as HTMLElement).dataset.holdAllow;
  if (id) send({ type: "approve", id, allow: true, held: true });
});

// Sim buttons: hold to press, like the real thing.
document.addEventListener("pointerdown", (ev) => {
  const pad = (ev.target as HTMLElement).closest<HTMLElement>("[data-pad]");
  if (!pad) return;
  heldPad = pad.dataset.pad!;
  pad.classList.add("down");
  pad.setPointerCapture(ev.pointerId);
  send({ type: "press", capability: heldPad, down: true });
});
const release = () => {
  if (!heldPad) return;
  send({ type: "press", capability: heldPad, down: false });
  document.querySelector(`[data-pad="${heldPad}"]`)?.classList.remove("down");
  heldPad = null;
};
document.addEventListener("pointerup", release);
document.addEventListener("pointercancel", release);

// Tapping the dimmed backdrop hides the sheet; the request stays in Approvals.
$("sheet").addEventListener("click", (ev) => {
  if (ev.target === ev.currentTarget && $("sheet").dataset.id) {
    dismissedSheet.add($("sheet").dataset.id!);
    renderSheet();
  }
});

$("composer").addEventListener("submit", (ev) => {
  ev.preventDefault();
  const input = $<HTMLInputElement>("composer-input");
  say(input.value);
  input.value = "";
});

// Clock, countdowns and "ago" labels tick between snapshots.
function tick() {
  const now = new Date();
  $("clock").textContent = now.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  $("big-clock").textContent = $("clock").textContent;
  $("big-date").textContent = now.toLocaleDateString([], { weekday: "long", day: "numeric", month: "long" });
  setupUI.tick();
  if (snap) {
    for (const a of snap.approvals ?? []) {
      const r = remaining(a);
      document.querySelectorAll<HTMLElement>(`[data-countdown="${a.id}"]`).forEach((el) => (el.style.transform = `scaleX(${r})`));
      if ($("sheet").dataset.id === a.id) $("sheet-countdown").style.transform = `scaleX(${r})`;
    }
    if (snap.link.state === "disconnected") renderPresence();
  }
}
// Relative times inside widgets ("5 minutes ago") age slowly; refresh them gently.
setInterval(() => widgetHost.tick(), 30_000);
setInterval(tick, 250);
tick();

connect();
