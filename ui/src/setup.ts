// First-boot setup and Settings. Both are built from the same steps
// (network, server, pair, hardware, PIN); setup strings them together,
// Settings opens one or two at a time. Every step can be skipped: a device
// that never meets an Apteva instance is a working standalone device.

import qrcode from "qrcode-generator";
import { holdButton } from "./hold.ts";

export interface NetStatus { supported: boolean; online: boolean; ethernet: boolean; wifi_ssid?: string; wifi_signal?: number; clock_synced: boolean }
export interface Pairing { status: "idle" | "waiting" | "approved" | "denied" | "expired" | "error"; code?: string; verify_url?: string; expires_at?: string; error?: string; agent?: string }
export interface SetupView {
  mode: "setup" | "standalone" | "connected";
  server_url?: string;
  agent?: string;
  pin_set: boolean;
  locked: boolean;
  network: NetStatus;
  pairing: Pairing;
  notice?: string;
  provisioned_by?: string;
}

// The slice of the snapshot setup needs.
interface Snap {
  node: { name: string; version: string; backend: string; sim: boolean };
  link: { state: string; server?: string; error?: string };
  setup: SetupView;
  capabilities: { id: string; kind: string; description?: string; pin?: number; on?: boolean; pressed?: boolean; reading?: Record<string, number>; error?: string; photo_at?: string; camera_model?: string }[] | null;
}

interface Host {
  snap(): Snap | null;
  toast(msg: string, tone?: "ok" | "bad"): void;
}

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}

const ic = {
  check: `<svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/></svg>`,
  lock: `<svg viewBox="0 0 24 24" width="15" height="15" aria-hidden="true"><rect x="5" y="11" width="14" height="9" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3" fill="none" stroke="currentColor" stroke-width="2"/></svg>`,
  back: `<svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path d="M15 5l-7 7 7 7" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/></svg>`,
  close: `<svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"/></svg>`,
  del: `<svg viewBox="0 0 24 24" width="24" height="24" aria-hidden="true"><path d="M9 6h10a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H9l-6-6 6-6z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"/><path d="M12 10l4 4M16 10l-4 4" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>`,
};

function bars(signal = 0): string {
  const lit = signal >= 75 ? 4 : signal >= 50 ? 3 : signal >= 25 ? 2 : 1;
  return `<svg class="bars" viewBox="0 0 20 16" width="20" height="16" aria-label="signal ${signal}%">${[0, 1, 2, 3]
    .map((i) => `<rect x="${i * 5}" y="${12 - i * 4}" width="3.4" height="${4 + i * 4}" rx="1" opacity="${i < lit ? 1 : 0.25}" fill="currentColor"/>`)
    .join("")}</svg>`;
}

// ---------- API ----------

const unlock = { token: "", exp: 0 };
const unlocked = () => unlock.token !== "" && Date.now() < unlock.exp;

class LockedError extends Error {}

async function api<T = Record<string, unknown>>(path: string, body: Record<string, unknown> = {}): Promise<T> {
  const res = await fetch("/api/" + path, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Unlock": unlock.token },
    body: JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (res.status === 401) {
    unlock.token = "";
    throw new LockedError("Settings are locked");
  }
  if (!res.ok) throw new Error(data.error || `failed (${res.status})`);
  return data as T;
}

// ---------- PIN pad ----------

function pinPad(root: HTMLElement, o: { title: string; sub?: string; onEnter(pin: string): Promise<string | void> }) {
  let pin = "";
  root.innerHTML = `<div class="pinpad">
    <div class="pin-title">${esc(o.title)}</div>
    <div class="pin-sub">${esc(o.sub ?? "")}</div>
    <div class="pin-dots"></div>
    <div class="pin-err" role="alert"></div>
    <div class="pin-keys">
      ${[1, 2, 3, 4, 5, 6, 7, 8, 9].map((d) => `<button type="button" data-d="${d}">${d}</button>`).join("")}
      <button type="button" data-d="ok" class="ok" aria-label="OK">${ic.check}</button>
      <button type="button" data-d="0">0</button>
      <button type="button" data-d="del" aria-label="Delete">${ic.del}</button>
    </div>
  </div>`;
  const dots = root.querySelector<HTMLElement>(".pin-dots")!;
  const err = root.querySelector<HTMLElement>(".pin-err")!;
  const draw = () => {
    dots.innerHTML = Array.from({ length: Math.max(4, pin.length) }, (_, i) => `<i class="${i < pin.length ? "on" : ""}"></i>`).join("");
    root.querySelector<HTMLButtonElement>("[data-d=ok]")!.disabled = pin.length < 4;
  };
  draw();
  root.querySelector(".pin-keys")!.addEventListener("click", async (ev) => {
    const b = (ev.target as HTMLElement).closest<HTMLButtonElement>("[data-d]");
    if (!b) return;
    const d = b.dataset.d!;
    if (d === "del") pin = pin.slice(0, -1);
    else if (d === "ok") {
      if (pin.length < 4) return;
      const entered = pin;
      pin = "";
      draw();
      const e = await o.onEnter(entered);
      // onEnter may have redrawn the pad (e.g. "enter it again"): look up fresh.
      if (e) {
        const er = root.querySelector<HTMLElement>(".pin-err"), ds = root.querySelector<HTMLElement>(".pin-dots");
        if (er) er.textContent = e;
        ds?.classList.remove("shake");
        void ds?.offsetWidth;
        ds?.classList.add("shake");
      }
      return;
    } else if (pin.length < 8) pin += d;
    err.textContent = "";
    draw();
  });
}

// ---------- steps ----------

type StepName = "welcome" | "network" | "server" | "pair" | "hardware" | "pin" | "done";

interface Step {
  title(f: Flow): string;
  body(f: Flow): string;
  foot(f: Flow): string;
  mount?(f: Flow): void;
  update?(f: Flow): void;
  act?(f: Flow, act: string, el: HTMLElement): void | Promise<void>;
  submit?(f: Flow, form: HTMLFormElement): void | Promise<void>;
  leave?(f: Flow): void;
}

class Flow {
  i = 0;
  path: "apteva" | "standalone" = "apteva";
  pressedSeen = new Set<string>();
  private cache = new Map<string, string>();

  constructor(
    public root: HTMLElement,
    public steps: StepName[],
    public host: Host,
    public opts: { settings: boolean; onExit(done: boolean): void },
  ) {
    root.onclick = (ev) => {
      const el = (ev.target as HTMLElement).closest<HTMLElement>("[data-act]");
      if (!el || !root.contains(el) || (el as HTMLButtonElement).disabled) return;
      const a = el.dataset.act!;
      if (a === "next") return this.next();
      if (a === "back") return this.back();
      if (a === "close") return this.exit(false);
      if (a === "standalone") return this.standalone();
      this.step.act?.(this, a, el);
    };
    root.onsubmit = (ev) => {
      ev.preventDefault();
      this.step.submit?.(this, ev.target as HTMLFormElement);
    };
    this.show();
  }

  get name(): StepName { return this.steps[this.i]; }
  get step(): Step { return stepDefs[this.name]; }
  get snap(): Snap { return this.host.snap()!; }
  q<T extends HTMLElement = HTMLElement>(sel: string): T | null { return this.root.querySelector<T>(sel); }

  // set writes html into a region only when it changed, so inputs and
  // images inside other regions are left alone.
  set(sel: string, html: string) {
    const el = this.q(sel);
    if (!el || this.cache.get(sel) === html) return;
    this.cache.set(sel, html);
    el.innerHTML = html;
  }

  show() {
    this.cache.clear();
    const s = this.step;
    const total = this.steps.filter((n) => n !== "welcome" && n !== "done").length;
    const pos = this.steps.slice(0, this.i + 1).filter((n) => n !== "welcome" && n !== "done").length;
    const dots = !this.opts.settings && this.name !== "welcome" && this.name !== "done"
      ? `<div class="step-dots">${Array.from({ length: total }, (_, k) => `<i class="${k < pos ? "on" : ""}"></i>`).join("")}</div>`
      : "";
    const nav = this.opts.settings
      ? `<button class="icon-btn" data-act="${this.i === 0 ? "close" : "back"}" aria-label="Back">${ic.back}</button>`
      : this.i > 0 && this.name !== "done" ? `<button class="icon-btn" data-act="back" aria-label="Back">${ic.back}</button>` : `<span class="icon-btn-space"></span>`;
    this.root.innerHTML = `<div class="step" data-step="${this.name}">
      <div class="step-top">${nav}${dots}<span class="icon-btn-space"></span></div>
      <div class="step-scroll"><h1 class="step-title">${s.title(this)}</h1>${s.body(this)}</div>
      <div class="step-foot">${s.foot(this)}</div>
    </div>`;
    s.mount?.(this);
    s.update?.(this);
  }

  update() { this.step.update?.(this); }

  private move(to: number) {
    this.step.leave?.(this);
    if (to < 0) return this.exit(false);
    if (to >= this.steps.length) return this.exit(true);
    this.i = to;
    this.show();
  }

  // Server and pairing make no sense once the person chose standalone.
  private skippable(n: StepName) { return this.path === "standalone" && (n === "server" || n === "pair"); }

  next() {
    let j = this.i + 1;
    while (j < this.steps.length && this.skippable(this.steps[j])) j++;
    this.move(j);
  }

  back() {
    let j = this.i - 1;
    while (j >= 0 && this.skippable(this.steps[j])) j--;
    if (this.opts.settings && j < 0) return this.exit(false);
    this.move(Math.max(0, j));
  }

  standalone() {
    this.path = "standalone";
    if (this.opts.settings) return this.exit(false);
    this.next();
  }

  exit(done: boolean) {
    this.step.leave?.(this);
    this.opts.onExit(done);
  }

  async run(btn: HTMLButtonElement | null, busy: string, fn: () => Promise<void>) {
    const label = btn?.innerHTML;
    if (btn) (btn.disabled = true), (btn.textContent = busy);
    try {
      await fn();
    } catch (e) {
      if (e instanceof LockedError) this.host.toast("Settings locked again — enter your PIN", "bad");
      throw e;
    } finally {
      if (btn && btn.isConnected) (btn.disabled = false), (btn.innerHTML = label!);
    }
  }
}

const primary = (label: string, act = "next", extra = "") => `<button class="btn primary" data-act="${act}" ${extra}>${label}</button>`;
const secondary = (label: string, act: string, extra = "") => `<button class="btn" data-act="${act}" ${extra}>${label}</button>`;

function netLine(n: NetStatus): string {
  if (n.ethernet) return `<span class="ok-dot"></span>Connected by cable`;
  if (n.wifi_ssid) return `<span class="ok-dot"></span>Connected to <b>${esc(n.wifi_ssid)}</b>`;
  if (n.online) return `<span class="ok-dot"></span>Online`;
  return `<span class="off-dot"></span>Not connected`;
}

const stepDefs: Record<StepName, Step> = {
  welcome: {
    title: (f) => `Let's set up ${esc(f.snap.node.name)}`,
    body: () => `<p class="lead">Connect it to your Apteva instance so an agent can see, hear and act through it — or use it on its own with just its hardware.</p>
      <p class="hint">You can change this any time in Settings.</p>`,
    foot: () => secondary("Use on its own", "solo") + primary("Get started"),
    act(f, a) {
      if (a === "solo") {
        f.path = "standalone";
        f.next();
      }
    },
  },

  network: {
    title: () => "Network",
    body: (f) => `<div class="status-line" id="net-status"></div>
      ${f.snap.setup.network.supported
        ? `<div class="list-head"><span>Wi-Fi networks</span><button class="link-btn" data-act="rescan">Rescan</button></div>
           <div class="net-list" id="net-list"><div class="hint">Looking for networks…</div></div>
           <form class="join" id="net-join" hidden autocomplete="off">
             <div class="join-ssid" id="join-ssid"></div>
             <input class="input" name="password" type="password" placeholder="Password" data-osk enterkeyhint="go" autocomplete="off">
             <div class="form-err" id="join-err"></div>
             <div class="row"><button type="button" class="btn" data-act="cancel-join">Cancel</button><button class="btn primary" type="submit">Join</button></div>
           </form>`
        : `<p class="hint">This device's network is managed by the system, not by Apteva Node.</p>`}`,
    foot: (f) => (f.opts.settings ? "" : secondary(f.path === "standalone" ? "Skip" : "Skip for now", "next")) + primary("Continue", "next", `id="net-continue"`),
    mount(f) {
      if (f.snap.setup.network.supported) scan(f);
    },
    update(f) {
      const n = f.snap.setup.network;
      f.set("#net-status", netLine(n));
      const c = f.q<HTMLButtonElement>("#net-continue");
      if (c) c.disabled = !n.online && !f.opts.settings;
    },
    act(f, a, el) {
      if (a === "rescan") return scan(f);
      if (a === "cancel-join") {
        f.q("#net-join")!.hidden = true;
        f.q("#net-list")!.hidden = false;
        return;
      }
      if (a === "pick") {
        const ssid = el.dataset.ssid!;
        if (el.dataset.secure !== "1") return join(f, ssid, "");
        f.q("#net-list")!.hidden = true;
        const form = f.q<HTMLFormElement>("#net-join")!;
        form.hidden = false;
        form.dataset.ssid = ssid;
        f.q("#join-ssid")!.innerHTML = `Password for <b>${esc(ssid)}</b>`;
        f.q("#join-err")!.textContent = "";
        const pw = form.elements.namedItem("password") as HTMLInputElement;
        pw.value = "";
        pw.focus();
      }
    },
    async submit(f, form) {
      const pw = (form.elements.namedItem("password") as HTMLInputElement).value;
      await join(f, form.dataset.ssid!, pw);
    },
  },

  server: {
    title: () => "Your Apteva instance",
    body: (f) => `<form id="srv-form" autocomplete="off">
        <label class="field-label" for="srv-url">Address</label>
        <input class="input" id="srv-url" name="url" type="text" data-osk="url" enterkeyhint="go" spellcheck="false" autocapitalize="off"
          placeholder="agents.example.com" value="${esc(f.snap.setup.server_url ?? "")}">
        <div class="form-err" id="srv-err"></div>
        <p class="hint">The address you open the Apteva dashboard at. Nothing is shared until you approve this device there.</p>
      </form>`,
    foot: (f) => (f.opts.settings ? "" : secondary("Use on its own", "standalone")) + `<button class="btn primary" type="submit" form="srv-form" id="srv-go">Continue</button>`,
    mount(f) {
      if (!f.snap.setup.server_url) setTimeout(() => f.q<HTMLInputElement>("#srv-url")?.focus(), 50);
    },
    async submit(f, form) {
      const url = (form.elements.namedItem("url") as HTMLInputElement).value;
      const err = f.q("#srv-err")!;
      err.textContent = "";
      try {
        await f.run(f.q<HTMLButtonElement>("#srv-go"), "Checking…", async () => {
          await api("setup/server", { url });
        });
        f.path = "apteva";
        f.next();
      } catch (e) {
        err.textContent = (e as Error).message;
      }
    },
  },

  pair: {
    title: () => "Approve this device",
    body: () => `<div id="pair-body" class="pair"></div>`,
    foot: (f) => (f.opts.settings ? secondary("Cancel", "back") : secondary("Use on its own", "standalone")) + primary("Continue", "next", `id="pair-continue" disabled`),
    mount(f) {
      const p = f.snap.setup.pairing;
      if (p.status !== "waiting" && p.status !== "approved") startPair(f);
    },
    update(f) {
      const p = f.snap.setup.pairing;
      const btn = f.q<HTMLButtonElement>("#pair-continue");
      if (btn) btn.disabled = p.status !== "approved";
      let html: string;
      switch (p.status) {
        case "waiting": {
          const host = p.verify_url ? new URL(p.verify_url).host : "";
          html = `<div class="pair-grid">
              <div class="qr" aria-label="QR code to approve this device">${qr(p.verify_url ?? "")}</div>
              <div class="pair-text">
                <p>Scan with your phone, or open <b>${esc(host)}/pair</b> and enter:</p>
                <div class="pair-code">${esc(p.code ?? "")}</div>
                <p class="hint" id="pair-expiry"></p>
              </div>
            </div>
            <div class="waiting"><span class="spinner"></span>Waiting for approval in Apteva…</div>`;
          break;
        }
        case "approved":
          html = `<div class="pair-done"><span class="big-check">${ic.check}</span><div><b>Paired</b><p>Connected to ${esc(p.agent || "your agent")}.</p></div></div>`;
          break;
        case "denied":
        case "expired":
        case "error":
          html = `<div class="pair-fail"><b>${p.status === "denied" ? "Request denied" : p.status === "expired" ? "The code expired" : "Couldn't pair"}</b>
            <p>${esc(p.error || (p.status === "denied" ? "Someone declined this device in Apteva." : "Get a new code to try again."))}</p>
            ${secondary("Get a new code", "retry")}</div>`;
          break;
        default:
          html = `<div class="waiting"><span class="spinner"></span>Asking for a code…</div>`;
      }
      f.set("#pair-body", html);
    },
    act(f, a) {
      if (a === "retry") startPair(f);
    },
    leave(f) {
      if (f.snap.setup.pairing.status === "waiting") api("setup/pair/cancel").catch(() => {});
    },
  },

  hardware: {
    title: () => "Check the hardware",
    body: (f) => `<p class="hint">Try each part. Nothing here is shared with an agent.</p><div class="hw-list" id="hw-list"></div>`,
    foot: (f) => (f.opts.settings ? "" : secondary("Skip", "next")) + primary(f.opts.settings ? "Done" : "Looks good"),
    update(f) {
      const s = f.snap;
      const caps = s.capabilities ?? [];
      for (const c of caps) if (c.kind === "button" && c.pressed) f.pressedSeen.add(c.id);
      f.set("#hw-list", caps.length ? caps.map((c) => hwRow(f, c, s.node.sim)).join("") : `<p class="hint">node.yaml declares no hardware.</p>`);
    },
    async act(f, a, el) {
      if (a === "photo") {
        try {
          await f.run(el as HTMLButtonElement, "Taking…", () => api("setup/test-photo", { capability: el.dataset.cap }).then(() => {}));
        } catch (e) {
          f.host.toast((e as Error).message, "bad");
        }
      }
    },
  },

  pin: {
    title: (f) => (f.snap.setup.pin_set ? "Change PIN" : "Lock settings"),
    body: (f) => `<p class="hint">${f.opts.settings ? "" : "A PIN stops anyone at the device from changing its network or pairing. Using the hardware never needs it."}</p><div id="pin-root"></div>`,
    foot: (f) => (f.opts.settings ? (f.snap.setup.pin_set ? secondary("Remove PIN", "remove-pin") : "") : secondary("No PIN", "next")),
    mount(f) {
      let first = "";
      const ask = () =>
        pinPad(f.q("#pin-root")!, {
          title: first ? "Enter it again" : "Choose a PIN",
          sub: "4 to 8 digits",
          async onEnter(pin) {
            if (!first) {
              first = pin;
              return void ask();
            }
            if (pin !== first) {
              first = "";
              ask();
              return "Those didn't match — start again";
            }
            try {
              await api("setup/pin", { pin });
              f.host.toast("PIN set");
              f.next();
            } catch (e) {
              return (e as Error).message;
            }
          },
        });
      ask();
    },
    async act(f, a) {
      if (a === "remove-pin") {
        try {
          await api("setup/pin", { pin: "" });
          f.host.toast("PIN removed");
          f.exit(true);
        } catch (e) {
          f.host.toast((e as Error).message, "bad");
        }
      }
    },
  },

  done: {
    title: () => "All set",
    body: () => `<div class="summary" id="summary"></div><p class="hint">You can change any of this later in Settings.</p>`,
    update(f) {
      const s = f.snap.setup;
      const paired = f.path === "apteva" && s.pairing.status === "approved";
      const line = (ok: boolean, text: string) => `<div>${ok ? ic.check : `<span class="off-dot"></span>`}<span>${text}</span></div>`;
      f.set(
        "#summary",
        line(paired, paired ? `Connected to <b>${esc(s.pairing.agent || s.agent || "your agent")}</b>` : "Running on its own. Connect to Apteva later from Settings.") +
          line(s.network.online, s.network.online ? "Network ready" : "No network") +
          line(s.pin_set, s.pin_set ? "Settings locked with a PIN" : "No PIN on settings"),
      );
    },
    foot: () => primary("Finish", "finish", `id="finish"`),
    async act(f, a, el) {
      if (a !== "finish") return;
      await f.run(el as HTMLButtonElement, "Finishing…", async () => {
        if (f.path === "standalone" || f.snap.setup.pairing.status !== "approved") await api("setup/standalone");
        await api("setup/finish");
      });
      f.exit(true);
    },
  },
};

async function scan(f: Flow) {
  f.set("#net-list", `<div class="hint"><span class="spinner"></span>Looking for networks…</div>`);
  try {
    const { networks } = await api<{ networks: { ssid: string; signal: number; secure: boolean; in_use: boolean }[] }>("setup/wifi/scan");
    f.set(
      "#net-list",
      networks.length
        ? networks
            .map(
              (n) => `<button class="net${n.in_use ? " in-use" : ""}" data-act="pick" data-ssid="${esc(n.ssid)}" data-secure="${n.secure ? 1 : 0}">
              <span class="net-name">${esc(n.ssid)}</span>${n.in_use ? `<span class="net-tag">Connected</span>` : ""}
              ${n.secure ? `<span class="net-lock">${ic.lock}</span>` : ""}${bars(n.signal)}</button>`,
            )
            .join("")
        : `<div class="hint">No networks found.</div>`,
    );
  } catch (e) {
    f.set("#net-list", `<div class="form-err">${esc((e as Error).message)}</div>`);
  }
}

async function join(f: Flow, ssid: string, password: string) {
  const err = f.q("#join-err");
  if (err) err.textContent = "";
  try {
    await f.run(f.q<HTMLButtonElement>("#net-join button[type=submit]"), "Joining…", () => api("setup/wifi", { ssid, password }).then(() => {}));
    f.host.toast(`Connected to ${ssid}`);
    (document.activeElement as HTMLElement | null)?.blur();
    f.q("#net-join")!.hidden = true;
    f.q("#net-list")!.hidden = false;
    scan(f);
  } catch (e) {
    if (err) err.textContent = (e as Error).message;
    else f.host.toast((e as Error).message, "bad");
  }
}

async function startPair(f: Flow) {
  try {
    await api("setup/pair");
  } catch {
    // The error is in the snapshot's pairing view.
  }
}

function qr(text: string): string {
  if (!text) return "";
  const q = qrcode(0, "M");
  q.addData(text);
  q.make();
  return q.createSvgTag({ cellSize: 4, margin: 2, scalable: true });
}

function hwRow(f: Flow, c: NonNullable<Snap["capabilities"]>[number], sim: boolean): string {
  const name = `<div class="hw-name"><b>${esc(c.id)}</b><span>${esc(c.description ?? c.kind)}${c.pin !== undefined ? ` · GPIO ${c.pin}` : ""}</span></div>`;
  let ctl = "";
  switch (c.kind) {
    case "relay":
    case "led":
      ctl = `<button class="switch" role="switch" aria-checked="${!!c.on}" aria-label="Toggle ${esc(c.id)}" data-toggle="${esc(c.id)}"></button>`;
      break;
    case "button": {
      const seen = f.pressedSeen.has(c.id);
      ctl = `<span class="hw-state${c.pressed ? " live" : seen ? " ok" : ""}">${c.pressed ? "Pressed" : seen ? `${ic.check}Works` : "Press it"}</span>
        ${sim ? `<button class="pad mini" data-pad="${esc(c.id)}">Hold</button>` : ""}`;
      break;
    }
    case "i2c_sensor": {
      const r = c.reading ?? {};
      ctl = c.error
        ? `<span class="hw-state bad">${esc(c.error)}</span>`
        : `<span class="hw-state ok">${r.temperature_c !== undefined ? `${r.temperature_c.toFixed(1)} °C` : ""}${r.humidity_pct !== undefined ? ` · ${r.humidity_pct.toFixed(0)}%` : ""}</span>`;
      break;
    }
    case "camera":
      ctl = `${c.error ? `<span class="hw-state bad">${esc(c.error)}</span>` : ""}<button class="btn small" data-act="photo" data-cap="${esc(c.id)}">Test photo</button>`;
      break;
  }
  const photo = c.kind === "camera" && c.photo_at ? `<img class="hw-photo" alt="Test photo from ${esc(c.id)}" src="/photo/${esc(c.id)}?t=${encodeURIComponent(c.photo_at)}">` : "";
  return `<div class="hw-row"><div class="hw-main">${name}<div class="hw-ctl">${ctl}</div></div>${photo}</div>`;
}

// ---------- settings ----------

class Settings {
  private flow: Flow | null = null;
  private armed = "";
  private armTimer: ReturnType<typeof setTimeout> | undefined;
  private lastHTML = "";
  private pinShown = false;
  private forgot = false; // showing "Reset this device?" from the lock screen

  constructor(private root: HTMLElement, private host: Host) {
    root.addEventListener("held", async (ev) => {
      if ((ev.target as HTMLElement).dataset.holdReset === undefined) return;
      try {
        await api("setup/reset");
        this.close();
      } catch (e) {
        this.host.toast((e as Error).message, "bad");
      }
    });
    root.addEventListener("click", (ev) => {
      if (this.flow) return;
      const el = (ev.target as HTMLElement).closest<HTMLElement>("[data-act]");
      if (el) this.act(el.dataset.act!, el as HTMLButtonElement);
    });
  }

  get isOpen() { return !this.root.hidden; }

  open(sub?: "connect") {
    this.root.hidden = false;
    this.flow = null;
    this.lastHTML = "";
    this.pinShown = false;
    this.forgot = false;
    this.render();
    if (sub === "connect" && !this.locked()) this.startFlow(["server", "pair"]);
  }

  close() {
    this.flow?.exit(false);
    this.flow = null;
    this.root.hidden = true;
  }

  private locked() { return this.host.snap()!.setup.locked && !unlocked(); }

  update() {
    if (!this.isOpen) return;
    if (this.flow) return this.flow.update();
    this.render();
  }

  private render() {
    const s = this.host.snap();
    if (!s) return;
    if (this.locked()) {
      if (this.pinShown || this.forgot) return;
      this.pinShown = true;
      this.lastHTML = "";
      this.root.innerHTML = `<div class="step"><div class="step-top"><button class="icon-btn" data-act="close" aria-label="Close">${ic.close}</button><span></span><span class="icon-btn-space"></span></div>
        <div class="step-scroll" id="unlock-root"></div>
        <div class="step-foot forgot"><button class="link-btn" data-act="forgot">Forgot PIN?</button></div></div>`;
      pinPad($("unlock-root"), {
        title: "Enter PIN",
        sub: "Settings are locked",
        onEnter: async (pin) => {
          try {
            const r = await api<{ token: string; expires_at: string }>("unlock", { pin });
            unlock.token = r.token;
            unlock.exp = new Date(r.expires_at).getTime() - 5000;
            this.pinShown = false;
            this.render();
          } catch (e) {
            return (e as Error).message;
          }
        },
      });
      return;
    }
    this.pinShown = false;
    const st = s.setup;
    const linkText: Record<string, string> = { connected: "Online", connecting: "Connecting…", disconnected: "Offline — retrying", rejected: "Refused by the server" };
    const apteva =
      st.mode === "connected"
        ? `<div class="set-row"><span>Status</span><b>${esc(linkText[s.link.state] ?? s.link.state)}</b></div>
           <div class="set-row"><span>Agent</span><b>${esc(st.agent || "—")}</b></div>
           <div class="set-row"><span>Server</span><b class="mono">${esc(st.server_url ?? "")}</b></div>
           ${st.provisioned_by ? `<p class="hint">Set in ${esc(st.provisioned_by)}. Change it there.</p>` : this.armBtn("disconnect", "Disconnect from Apteva", "Tap again to disconnect")}`
        : `<p class="hint">Running on its own — only the hardware works, no agent.</p>
           <button class="btn primary" data-act="connect">Connect to Apteva</button>`;
    const n = st.network;
    const html = `<div class="step"><div class="step-top"><button class="icon-btn" data-act="close" aria-label="Close">${ic.close}</button><span class="step-top-title">Settings</span><span class="icon-btn-space"></span></div>
      <div class="step-scroll">
        ${st.notice ? `<div class="notice-card">${esc(st.notice)}</div>` : ""}
        <h2 class="section-title">Apteva</h2><div class="card set-card">${apteva}</div>
        <h2 class="section-title">Network</h2><div class="card set-card">
          <div class="set-row"><span>${netLine(n)}</span>${n.wifi_signal ? bars(n.wifi_signal) : ""}</div>
          ${n.supported ? `<button class="btn" data-act="wifi">Change Wi-Fi</button>` : ""}
          ${!n.clock_synced ? `<p class="hint">The clock isn't synced yet; secure connections may fail until it is.</p>` : ""}
        </div>
        <h2 class="section-title">Security</h2><div class="card set-card">
          <div class="set-row"><span>PIN for settings</span><b>${st.pin_set ? "On" : "Off"}</b></div>
          <button class="btn" data-act="pin">${st.pin_set ? "Change or remove PIN" : "Set a PIN"}</button>
        </div>
        <h2 class="section-title">Device</h2><div class="card set-card">
          <div class="set-row"><span>Name</span><b>${esc(s.node.name)}</b></div>
          <div class="set-row"><span>Software</span><b>v${esc(s.node.version)} · ${esc(s.node.backend)}</b></div>
          <button class="btn" data-act="hardware">Run hardware check</button>
          ${holdButton("Hold to reset device", "data-hold-reset", 2000, "danger")}
          <p class="hint">Erases pairing, PIN and setup. The hardware configuration stays.</p>
        </div>
      </div></div>`;
    if (html !== this.lastHTML) {
      this.lastHTML = html;
      this.root.innerHTML = html;
    }
  }

  // Destructive buttons need a second tap within 3 s.
  private armBtn(act: string, label: string, armedLabel: string, danger = false) {
    const on = this.armed === act;
    return `<button class="btn${danger ? " danger" : ""}${on ? " armed" : ""}" data-act="${act}">${on ? armedLabel : label}</button>`;
  }

  private arm(act: string): boolean {
    if (this.armed === act) {
      this.armed = "";
      clearTimeout(this.armTimer);
      return true;
    }
    this.armed = act;
    clearTimeout(this.armTimer);
    this.armTimer = setTimeout(() => ((this.armed = ""), this.render()), 3000);
    this.render();
    return false;
  }

  private startFlow(steps: StepName[]) {
    this.lastHTML = "";
    this.flow = new Flow(this.root, steps, this.host, {
      settings: true,
      onExit: () => {
        this.flow = null;
        this.render();
      },
    });
  }

  private async act(a: string, btn: HTMLButtonElement) {
    try {
      switch (a) {
        case "close":
          return this.close();
        case "forgot":
          // No PIN, no settings: the only way back in is a reset, which also
          // unpairs the device, so it can't be used to take one over.
          this.pinShown = false;
          this.forgot = true;
          this.root.innerHTML = `<div class="step"><div class="step-top"><button class="icon-btn" data-act="close" aria-label="Close">${ic.close}</button><span></span><span class="icon-btn-space"></span></div>
            <div class="step-scroll"><h1 class="step-title">Reset this device?</h1>
              <p class="lead">Without the PIN, the only way into settings is to reset. Pairing, PIN and setup are erased; the device starts setup again and must be approved in Apteva once more.</p>
              <p class="hint">The hardware configuration and saved Wi-Fi stay.</p></div>
            <div class="step-foot"><button class="btn" data-act="close">Cancel</button>${holdButton("Hold to reset", "data-hold-reset", 2000, "danger")}</div></div>`;
          return;
        case "connect":
          return this.startFlow(["server", "pair"]);
        case "wifi":
          return this.startFlow(["network"]);
        case "hardware":
          return this.startFlow(["hardware"]);
        case "pin":
          return this.startFlow(["pin"]);
        case "disconnect":
          if (this.arm(a)) {
            await api("setup/standalone");
            this.host.toast("Disconnected — running on its own");
          }
          return;
      }
    } catch (e) {
      if (e instanceof LockedError) return this.render();
      this.host.toast((e as Error).message, "bad");
    }
  }
}

// ---------- controller ----------

export class SetupUI {
  private flow: Flow | null = null;
  private settings: Settings;

  constructor(private host: Host) {
    this.settings = new Settings($("settings"), host);
  }

  openSettings(sub?: "connect") { this.settings.open(sub); }

  render() {
    const s = this.host.snap();
    if (!s) return;
    const setup = $("setup");
    if (s.setup.mode === "setup") {
      if (this.settings.isOpen) this.settings.close();
      setup.hidden = false;
      if (!this.flow) {
        this.flow = new Flow(setup, ["welcome", "network", "server", "pair", "hardware", "pin", "done"], this.host, {
          settings: false,
          onExit: () => {},
        });
      } else this.flow.update();
    } else {
      setup.hidden = true;
      this.flow = null;
      this.settings.update();
    }
    this.tick();
  }

  // Countdowns between snapshots.
  tick() {
    const s = this.host.snap();
    if (!s) return;
    const exp = document.getElementById("pair-expiry");
    if (exp && s.setup.pairing.expires_at) {
      const left = Math.max(0, Math.round((new Date(s.setup.pairing.expires_at).getTime() - Date.now()) / 1000));
      exp.textContent = `Code expires in ${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`;
    }
  }
}
