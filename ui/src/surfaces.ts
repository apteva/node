// Native-surface widget host. Renders apteva-native-surface/v1 documents
// with presentation "widget" — the same documents apps already ship for the
// dashboard's native widgets. The app owns data routes, mappings and
// actions; this host owns layout, type, colour, loading, errors and icons.
//
// The document is data only: selectors ("$.a.b"), bindings ("$settings.x")
// and templates ("{item.title}"). There is no expression language, so
// nothing here evaluates code from an app.

export const SCHEMA = "apteva-native-surface/v1";

export interface WidgetView {
  id: string;
  app: string;
  component: string;
  size: "half" | "full";
  settings?: Record<string, unknown>;
  refresh_topics?: string[];
  surface: Surface;
  rev: number;
}

interface Surface {
  schema: string;
  id: string;
  version: string;
  title: string;
  description?: string;
  icon: string;
  presentation?: string;
  data_sources?: Record<string, DataSource>;
  actions?: Record<string, Action>;
  blocks?: Block[];
}

interface Request {
  method: string;
  path: string;
  encoding?: string;
  query?: Record<string, unknown>;
  body?: Record<string, unknown>;
}

interface DataSource {
  request: Request;
  response: { item?: string; items?: string; result?: string };
}

interface Value {
  value: string;
  label?: string;
  format?: string;
  fallback?: string;
}

interface Block {
  id: string;
  type: "metric" | "metrics" | "text" | "status" | "progress" | "list" | "empty_state" | "divider" | "action";
  title?: string;
  label?: string;
  source?: string;
  value?: string;
  text?: string;
  format?: string;
  icon?: string;
  items?: string;
  item?: { id: string; title: string; subtitle?: Value[]; icon?: Value; badge?: Value };
  metrics?: { label: string; value: string; format?: string; icon?: string }[];
  limit?: number;
  action?: string;
  empty?: { title?: string; description?: string; icon?: string };
  visible_if?: string;
}

interface Input {
  name: string;
  type: string;
  label?: string;
  placeholder?: string;
  required?: boolean;
  default?: unknown;
  options?: { label: string; value: unknown }[];
}

interface Action {
  type: "request" | "form" | "file_upload" | "open_url" | "copy_result" | "share_result" | "confirm";
  label: string;
  icon?: string;
  destructive?: boolean;
  request: Request;
  fields?: Input[];
  confirmation?: { title: string; message?: string; confirm_label?: string };
  success?: { refresh?: string[]; dismiss?: boolean; toast?: string };
}

export interface AppResult {
  status: number;
  body?: unknown;
  error?: string;
  stale?: boolean;
  cached_at?: string;
}

/** What the host page provides: transport plus its own trusted dialogs. */
export interface HostServices {
  request(app: string, req: { method: string; path: string; query?: Record<string, unknown>; body?: unknown }): Promise<AppResult>;
  toast(message: string, tone?: "ok" | "bad"): void;
  confirm(opts: { app: string; title: string; message?: string; confirmLabel?: string; destructive?: boolean }): Promise<boolean>;
  form(opts: { app: string; title: string; fields: Input[] }): Promise<Record<string, unknown> | null>;
}

// Inputs this device can collect. Files and resource pickers need a richer
// host than a 4" touchscreen, so actions using them are shown as unavailable.
const DEVICE_INPUTS = new Set(["string", "multiline", "url", "integer", "number", "boolean", "choice"]);
const DEVICE_ACTIONS = new Set(["request", "confirm", "form"]);

// ---------- selectors, bindings, templates ----------

type Scope = Record<string, unknown>;

function walk(root: unknown, path: string[]): unknown {
  let cur = root;
  for (const k of path) {
    if (cur == null || typeof cur !== "object") return undefined;
    cur = (cur as Record<string, unknown>)[k];
  }
  return cur;
}

/** "$" or "$.a.b" against a source response. */
export function select(root: unknown, selector: string): unknown {
  if (selector === "$") return root;
  if (!selector.startsWith("$.")) return undefined;
  return walk(root, selector.slice(2).split("."));
}

const NAMESPACES = ["context", "settings", "state", "item", "input", "result"];

/** "$settings.x" style whole-value binding. */
function direct(expr: string, scope: Scope): { ok: boolean; value?: unknown } {
  const m = /^\$([a-z]+)((?:\.[A-Za-z_][A-Za-z0-9_]*)+)$/.exec(expr);
  if (!m || !NAMESPACES.includes(m[1])) return { ok: false };
  return { ok: true, value: walk(scope[m[1]], m[2].slice(1).split(".")) };
}

/** "Hello {item.name}" interpolation. Missing values become "". */
export function template(text: string, scope: Scope): string {
  return text.replace(/\{([a-z]+)((?:\.[A-Za-z_][A-Za-z0-9_]*)*)\}/g, (whole, ns: string, rest: string) => {
    if (!NAMESPACES.includes(ns)) return whole;
    const v = rest ? walk(scope[ns], rest.slice(1).split(".")) : scope[ns];
    return v == null ? "" : String(v);
  });
}

/** A block/metric value: selector into the source, a binding, a template, or literal text. */
function resolve(expr: string | undefined, source: unknown, scope: Scope): unknown {
  if (expr == null || expr === "") return undefined;
  if (expr === "$" || expr.startsWith("$.")) return select(source, expr);
  const d = direct(expr, scope);
  if (d.ok) return d.value;
  if (expr.includes("{")) return template(expr, scope);
  return expr;
}

/** Bind request query/body values recursively; non-strings pass through. */
function bindAll(v: unknown, scope: Scope): unknown {
  if (typeof v === "string") {
    const d = direct(v, scope);
    if (d.ok) return d.value;
    return v.includes("{") ? template(v, scope) : v;
  }
  if (Array.isArray(v)) return v.map((x) => bindAll(x, scope));
  if (v && typeof v === "object") {
    return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, bindAll(x, scope)]));
  }
  return v;
}

// ---------- formatting ----------

function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}

function relTime(d: Date): string {
  const s = Math.round((d.getTime() - Date.now()) / 1000);
  const a = Math.abs(s);
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  if (a < 45) return rtf.format(0, "second");
  if (a < 3600) return rtf.format(Math.round(s / 60), "minute");
  if (a < 86400) return rtf.format(Math.round(s / 3600), "hour");
  return rtf.format(Math.round(s / 86400), "day");
}

export function format(v: unknown, fmt?: string): string {
  if (v == null || v === "") return "";
  switch (fmt) {
    case "number":
    case "count": {
      const n = Number(v);
      return Number.isFinite(n) ? n.toLocaleString() : String(v);
    }
    case "currency": {
      // v1 carries no currency code; show an amount, not a guessed symbol.
      const n = Number(v);
      return Number.isFinite(n) ? n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 }) : String(v);
    }
    case "date_time": {
      const d = new Date(String(v));
      if (isNaN(d.getTime())) return String(v);
      const sameDay = d.toDateString() === new Date().toDateString();
      return sameDay
        ? d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
        : d.toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" });
    }
    case "relative_time": {
      const d = new Date(String(v));
      return isNaN(d.getTime()) ? String(v) : relTime(d);
    }
    case "file_size": {
      let n = Number(v);
      if (!Number.isFinite(n)) return String(v);
      const units = ["B", "KB", "MB", "GB", "TB"];
      let i = 0;
      while (n >= 1024 && i < units.length - 1) {
        n /= 1024;
        i++;
      }
      return `${n.toFixed(n < 10 && i ? 1 : 0)} ${units[i]}`;
    }
    case "tags":
      return Array.isArray(v) ? v.join(" · ") : String(v);
    default:
      return String(v);
  }
}

// Status words → semantic tone. Apps send words, the host picks colour.
function tone(word: string): "ok" | "warn" | "bad" | "neutral" {
  const w = word.toLowerCase();
  if (/^(open|running|active|ok|online|healthy|success|succeeded|completed|done|live|up)$/.test(w)) return "ok";
  if (/^(paused|pending|scheduled|waiting|queued|degraded|draft|idle)$/.test(w)) return "warn";
  if (/^(failed|error|blocked|offline|down|closed|cancelled|canceled|expired|urgent|critical)$/.test(w)) return "bad";
  return "neutral";
}

// ---------- icons (semantic name → host glyph) ----------

const GLYPHS: Record<string, string> = {
  "list-checks": "M4 6h1.5M4 12h1.5M4 18h1.5M9 6h11M9 12h11M9 18h11",
  "check-circle": "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM8.5 12.5l2.5 2.5 4.5-5",
  calendar: "M5 6h14v14H5zM5 10h14M9 3v4M15 3v4",
  history: "M4 12a8 8 0 1 0 2.3-5.6M4 5v4h4M12 8v4l3 2",
  inbox: "M4 13l2.5-7h11L20 13v6H4zM4 13h5l1 2h4l1-2h5",
  "shopping-bag": "M6 8h12l-1 12H7zM9 8V6a3 3 0 0 1 6 0v2",
  "trending-up": "M4 17l6-6 4 4 6-7M15 8h5v5",
  pause: "M9 6v12M15 6v12",
  play: "M8 5l11 7-11 7z",
  edit: "M5 19h4L19 9l-4-4L5 15zM13 7l4 4",
  alert: "M12 4l9 16H3zM12 10v4M12 17v.5",
  clock: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 7v5l3 2",
  folder: "M3 7h6l2 2h10v10H3z",
  activity: "M3 12h4l3-7 4 14 3-7h4",
  plus: "M12 5v14M5 12h14",
};

export function glyph(name?: string, size = 16): string {
  const d = (name && GLYPHS[name]) || "M12 12m-3 0a3 3 0 1 0 6 0a3 3 0 1 0-6 0";
  return `<svg class="glyph" viewBox="0 0 24 24" width="${size}" height="${size}" aria-hidden="true"><path d="${d}"/></svg>`;
}

// ---------- host ----------

interface SourceState {
  body?: unknown;
  error?: string;
  stale?: boolean;
  cachedAt?: string;
  loading: boolean;
}

interface Mounted {
  view: WidgetView;
  el: HTMLElement;
  sources: Record<string, SourceState>;
  loadedRev: number;
  surfaceKey: string;
  busy: Set<string>; // actions in flight
  html: string;
}

export class WidgetHost {
  private mounted = new Map<string, Mounted>();

  constructor(private root: HTMLElement, private host: HostServices) {
    root.addEventListener("click", (ev) => {
      const btn = (ev.target as HTMLElement).closest<HTMLElement>("[data-w-action],[data-w-retry]");
      if (!btn) return;
      const card = btn.closest<HTMLElement>("[data-widget]");
      const m = card && this.mounted.get(card.dataset.widget!);
      if (!m) return;
      if (btn.dataset.wRetry !== undefined) this.load(m);
      else if (btn.dataset.wAction) this.runAction(m, btn.dataset.wAction);
    });
  }

  /** Reconcile with the daemon's widget list. Refetches on rev bump or new document. */
  update(views: WidgetView[]) {
    const keep = new Set(views.map((v) => v.id));
    for (const [id, m] of this.mounted) {
      if (!keep.has(id)) {
        m.el.remove();
        this.mounted.delete(id);
      }
    }
    views.forEach((view, i) => {
      const key = JSON.stringify(view.surface) + JSON.stringify(view.settings ?? {});
      let m = this.mounted.get(view.id);
      if (!m) {
        const el = document.createElement("section");
        el.className = "widget";
        el.dataset.widget = view.id;
        m = { view, el, sources: {}, loadedRev: -1, surfaceKey: "", busy: new Set(), html: "" };
        this.mounted.set(view.id, m);
      }
      m.el.classList.toggle("full", view.size === "full");
      if (this.root.children[i] !== m.el) this.root.insertBefore(m.el, this.root.children[i] ?? null);
      const changed = key !== m.surfaceKey;
      m.view = view;
      if (changed || view.rev !== m.loadedRev) {
        m.surfaceKey = key;
        m.loadedRev = view.rev;
        if (changed) m.sources = {};
        this.load(m);
      }
    });
  }

  /** Re-render labels that depend on the clock ("5 minutes ago"). */
  tick() {
    for (const m of this.mounted.values()) this.render(m);
  }

  private supported(view: WidgetView): string | null {
    if (view.surface?.schema !== SCHEMA) return `Needs a newer device (${view.surface?.schema ?? "unknown"})`;
    if (view.surface.presentation !== "widget" || !Array.isArray(view.surface.blocks)) return "Not a widget";
    return null;
  }

  private scope(m: Mounted, extra: Scope = {}): Scope {
    return { settings: m.view.settings ?? {}, context: {}, state: {}, ...extra };
  }

  private usedSources(m: Mounted): string[] {
    const set = new Set<string>();
    for (const b of m.view.surface.blocks ?? []) if (b.source && this.visible(m, b)) set.add(b.source);
    return [...set];
  }

  private async load(m: Mounted, only?: string[]) {
    if (this.supported(m.view)) return this.render(m);
    // success.refresh may name data sources or (in widgets) block ids.
    const blocks = m.view.surface.blocks ?? [];
    const names = only?.length
      ? [...new Set(only.map((n) => (m.view.surface.data_sources?.[n] ? n : blocks.find((b) => b.id === n)?.source)).filter((n): n is string => !!n))]
      : this.usedSources(m);
    for (const name of names) m.sources[name] = { ...(m.sources[name] ?? {}), loading: true };
    this.render(m);
    await Promise.all(
      names.map(async (name) => {
        const ds = m.view.surface.data_sources![name];
        const scope = this.scope(m);
        try {
          const r = await this.host.request(m.view.app, {
            method: ds.request.method,
            path: template(ds.request.path, scope),
            query: bindAll(ds.request.query ?? {}, scope) as Record<string, unknown>,
            body: ds.request.body ? bindAll(ds.request.body, scope) : undefined,
          });
          if (r.status >= 200 && r.status < 300) {
            const sel = ds.response.item ?? ds.response.items ?? ds.response.result ?? "$";
            m.sources[name] = { body: select(r.body, sel), stale: r.stale, cachedAt: r.cached_at, loading: false };
          } else {
            m.sources[name] = { ...m.sources[name], error: r.error || `The app answered ${r.status}`, loading: false };
          }
        } catch (e) {
          m.sources[name] = { ...m.sources[name], error: (e as Error).message, loading: false };
        }
      }),
    );
    this.render(m);
  }

  private visible(m: Mounted, b: Block): boolean {
    if (!b.visible_if) return true;
    const d = direct(b.visible_if, this.scope(m));
    return d.ok ? d.value !== false : true;
  }

  private render(m: Mounted) {
    const { view } = m;
    const s = view.surface;
    const unsupported = this.supported(view);
    const states = Object.values(m.sources);
    const stale = states.find((x) => x.stale);
    const head = `<header class="w-head">
        <span class="w-icon">${glyph(s?.icon, 15)}</span>
        <span class="w-title">${esc(s?.title ?? view.component)}</span>
        <span class="w-app" title="Provided by the ${esc(view.app)} app">${esc(view.app)}</span>
      </header>`;
    let body: string;
    if (unsupported) {
      body = `<div class="w-empty">${glyph("alert", 18)}<b>${esc(unsupported)}</b></div>`;
    } else if (states.length && states.every((x) => x.loading && x.body === undefined)) {
      body = `<div class="w-skeleton"><i></i><i></i><i class="short"></i></div>`;
    } else {
      body = (s.blocks ?? [])
        .filter((b) => this.visible(m, b))
        .map((b) => this.block(m, b))
        .join("");
    }
    const foot = stale ? `<footer class="w-stale">${glyph("clock", 12)}Offline · updated ${esc(format(stale.cachedAt, "relative_time"))}</footer>` : "";
    const html = head + `<div class="w-body">${body}</div>` + foot;
    if (html !== m.html) {
      m.html = html;
      m.el.innerHTML = html;
    }
  }

  private sourceError(m: Mounted, b: Block): string | null {
    const st = b.source ? m.sources[b.source] : undefined;
    if (st?.error && st.body === undefined) {
      return `<div class="w-error"><span>${esc(st.error)}</span><button class="w-link" data-w-retry>Retry</button></div>`;
    }
    return null;
  }

  private block(m: Mounted, b: Block): string {
    const src = b.source ? m.sources[b.source]?.body : undefined;
    const scope = this.scope(m);
    const err = b.source ? this.sourceError(m, b) : null;
    // One error notice per source, on its first block.
    if (err) {
      const first = (m.view.surface.blocks ?? []).find((x) => x.source === b.source && this.visible(m, x));
      return first?.id === b.id ? err : "";
    }
    switch (b.type) {
      case "metric": {
        const v = format(resolve(b.value, src, scope), b.format);
        return `<div class="b-metric">
          <span class="b-label">${b.icon ? glyph(b.icon, 13) : ""}${esc(b.label ?? b.title ?? "")}</span>
          <b>${esc(v || "—")}</b></div>`;
      }
      case "metrics":
        return `<div class="b-metrics">${(b.metrics ?? [])
          .map((mt) => `<div><b>${esc(format(resolve(mt.value, src, scope), mt.format) || "—")}</b><span>${esc(mt.label)}</span></div>`)
          .join("")}</div>`;
      case "text": {
        const title = b.title ? `<div class="b-text-title">${esc(template(b.title, scope))}</div>` : "";
        const text = b.value ? resolve(b.value, src, scope) : b.text ? template(b.text, scope) : "";
        return `<div class="b-text">${title}${text ? `<p>${esc(String(text))}</p>` : ""}</div>`;
      }
      case "status": {
        const v = String(resolve(b.value, src, scope) ?? "");
        return `<div class="b-status"><span>${esc(b.label ?? b.title ?? "Status")}</span>
          <span class="pill ${tone(v)}"><i></i>${esc(v ? v[0].toUpperCase() + v.slice(1) : "—")}</span></div>`;
      }
      case "progress": {
        let n = Number(resolve(b.value, src, scope));
        if (!Number.isFinite(n)) n = 0;
        const frac = Math.max(0, Math.min(1, n > 1 ? n / 100 : n));
        return `<div class="b-progress"><div class="b-progress-head"><span>${esc(b.label ?? b.title ?? "")}</span><b>${Math.round(frac * 100)}%</b></div>
          <div class="bar"><i style="width:${(frac * 100).toFixed(1)}%"></i></div></div>`;
      }
      case "list": {
        const raw = resolve(b.items, src, scope);
        const items = (Array.isArray(raw) ? raw : []).slice(0, b.limit || 5);
        const title = b.title ? `<div class="b-list-title">${esc(b.title)}</div>` : "";
        if (!items.length) return `<div class="b-list">${title}${this.empty(b.empty)}</div>`;
        const rows = items
          .map((it) => {
            const sc = { ...scope, item: it };
            const subtitle = (b.item!.subtitle ?? [])
              .map((v) => {
                const raw = format(resolve(v.value, src, sc), v.format);
                return raw || (v.fallback ? template(v.fallback, sc) : "");
              })
              .filter(Boolean)
              .join(" · ");
            const badge = b.item!.badge ? String(resolve(b.item!.badge.value, src, sc) ?? "") : "";
            return `<li><div class="li-main"><span class="li-title">${esc(String(resolve(b.item!.title, src, sc) ?? ""))}</span>
              ${subtitle ? `<span class="li-sub">${esc(subtitle)}</span>` : ""}</div>
              ${badge ? `<span class="pill small ${tone(badge)}">${esc(badge)}</span>` : ""}</li>`;
          })
          .join("");
        return `<div class="b-list">${title}<ul>${rows}</ul></div>`;
      }
      case "empty_state":
        return this.empty(b.empty ?? { title: b.title, description: b.text, icon: b.icon });
      case "divider":
        return `<hr class="b-divider">`;
      case "action": {
        const a = m.view.surface.actions?.[b.action ?? ""];
        if (!a) return "";
        const why = this.unavailable(a);
        const busy = m.busy.has(b.action!);
        return `<button class="b-action${a.destructive ? " danger" : ""}" data-w-action="${esc(b.action!)}"
          ${why || busy ? "disabled" : ""} title="${esc(why ?? "")}">${glyph(a.icon, 15)}<span>${esc(a.label)}</span>
          ${why ? `<small>Not on this device</small>` : ""}</button>`;
      }
      default:
        return ""; // unknown block types are skipped, never guessed at
    }
  }

  private empty(e?: { title?: string; description?: string; icon?: string }): string {
    return `<div class="w-empty">${glyph(e?.icon ?? "inbox", 18)}<b>${esc(e?.title ?? "Nothing here")}</b>${e?.description ? `<span>${esc(e.description)}</span>` : ""}</div>`;
  }

  private unavailable(a: Action): string | null {
    if (!DEVICE_ACTIONS.has(a.type)) return `${a.type} actions need a phone or computer`;
    const bad = (a.fields ?? []).find((f) => !DEVICE_INPUTS.has(f.type));
    if (bad) return `The "${bad.label ?? bad.name}" field needs a phone or computer`;
    return null;
  }

  private async runAction(m: Mounted, name: string) {
    const a = m.view.surface.actions?.[name];
    if (!a || this.unavailable(a) || m.busy.has(name)) return;
    const app = m.view.app;

    let input: Record<string, unknown> = {};
    if (a.type === "form") {
      const values = await this.host.form({ app, title: a.label, fields: a.fields ?? [] });
      if (!values) return;
      input = values;
    }
    if (a.type === "confirm" || a.destructive || a.confirmation) {
      const c = a.confirmation ?? { title: a.label };
      const yes = await this.host.confirm({ app, title: c.title, message: c.message, confirmLabel: c.confirm_label ?? a.label, destructive: a.destructive });
      if (!yes) return;
    }

    m.busy.add(name);
    this.render(m);
    try {
      const scope = this.scope(m, { input });
      const r = await this.host.request(app, {
        method: a.request.method,
        path: template(a.request.path, scope),
        query: bindAll(a.request.query ?? {}, scope) as Record<string, unknown>,
        body: a.request.body ? bindAll(a.request.body, scope) : undefined,
      });
      if (r.status >= 200 && r.status < 300 && !r.stale) {
        if (a.success?.toast) this.host.toast(a.success.toast);
        await this.load(m, a.success?.refresh);
      } else {
        this.host.toast(r.error || `${a.label} failed (${r.status})`, "bad");
      }
    } catch (e) {
      this.host.toast((e as Error).message === "offline" ? "Offline — try again when connected" : (e as Error).message, "bad");
    } finally {
      m.busy.delete(name);
      this.render(m);
    }
  }
}
