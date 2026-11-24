// The Agents page: the account's projects and their agents as a 3×3 grid of
// tiles, and a page per agent. Picking "Talk" binds the device to that agent.

import { agentGlyph } from "./agentmark.ts";

export interface AgentInfo {
  id: string; name: string; icon?: string; role?: string; state: string; detail?: string;
  project_id?: string; project_name?: string;
}

interface Host {
  projects(): { id: string; name: string }[];
  activeProjectID(): string | undefined;
  selectProject(id: string): void;
  agents(): AgentInfo[];
  activeID(): string | undefined;
  online(): boolean;
  select(id: string): void; // bind the device, then show Talk
}

const $ = (id: string) => document.getElementById(id)!;

function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}

const stateLabel: Record<string, string> = { idle: "Idle", thinking: "Thinking", acting: "Working", speaking: "Speaking", paused: "Paused" };

// The mark's border shows the state, like on the presence screen.
const markState = (s: string) => (s === "paused" ? "offline" : s);

export class AgentsPage {
  private open: string | null = null;
  private lastGrid = "";
  private lastPage = "";

  constructor(private host: Host) {
    $("agents-project").addEventListener("change", event => { const id = (event.target as HTMLSelectElement).value; this.show(null); this.host.selectProject(id); });
    $("agents-grid").addEventListener("click", (ev) => {
      const t = (ev.target as HTMLElement).closest<HTMLElement>("[data-agent]");
      if (t) this.show(t.dataset.agent!);
    });
    $("agent-page").addEventListener("click", (ev) => {
      const t = (ev.target as HTMLElement).closest<HTMLElement>("[data-act]");
      if (!t) return;
      if (t.dataset.act === "back") return this.show(null);
      if (t.dataset.act === "talk" && this.open) {
        this.host.select(this.open);
        this.show(null);
      }
    });
  }

  show(id: string | null) {
    this.open = id;
    this.lastPage = "";
    this.render();
  }

  render() {
    const projects = this.host.projects();
    const projectID = this.host.activeProjectID() || "";
    const projectSelect = $("agents-project") as HTMLSelectElement;
    $("agents-project-label").hidden = projects.length === 0;
    const options = '<option value="">No project</option>' + projects.map(project => '<option value="' + esc(project.id) + '">' + esc(project.name) + '</option>').join("");
    if (projectSelect.innerHTML !== options) projectSelect.innerHTML = options;
    projectSelect.value = projectID;
    projectSelect.disabled = !this.host.online();
    const list = this.host.agents().filter(agent => !projects.length || (agent.project_id || "") === projectID);
    const active = this.host.activeID();
    const online = this.host.online();
    const busy = list.filter((a) => a.state === "thinking" || a.state === "acting" || a.state === "speaking").length;
    $("agents-sub").textContent = list.length ? `${list.length} agents · ${busy} working${online ? "" : " · offline, last known"}` : "";

    const grid = list.length
      ? list
          .map((a) => {
            const st = a.state === "idle" || !a.detail ? stateLabel[a.state] ?? a.state : a.detail;
            return `<button class="agent-tile${a.id === active ? " current" : ""}" data-agent="${esc(a.id)}" aria-label="${esc(a.name)}, ${esc(stateLabel[a.state] ?? a.state)}">
              <span class="mark" data-state="${markState(a.state)}">${agentGlyph(a.icon)}</span>
              <span class="agent-tile-name">${esc(a.name)}</span>${a.project_name ? '<span class="agent-tile-state">' + esc(a.project_name) + '</span>' : ""}
              <span class="agent-tile-state" data-state="${esc(a.state)}"><i></i>${esc(st)}</span>
            </button>`;
          })
          .join("")
      : `<div class="empty">${online ? "No accessible agents yet." : "Connect to Apteva to see your agents."}</div>`;
    if (grid !== this.lastGrid) {
      this.lastGrid = grid;
      $("agents-grid").innerHTML = grid;
    }

    const page = $("agent-page");
    const a = this.open ? list.find((x) => x.id === this.open) : undefined;
    page.hidden = !a;
    if (!a) return;
    const isActive = a.id === active;
    const html = `<div class="agent-page-top"><button class="icon-btn" data-act="back" aria-label="Back to agents"><svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path d="M15 5l-7 7 7 7" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/></svg></button><span>Agents</span></div>
      <div class="agent-page-body">
        <span class="mark big" data-state="${markState(a.state)}">${agentGlyph(a.icon)}</span>
        <div class="agent-page-name">${esc(a.name)}</div>
        ${a.role ? `<div class="agent-page-role">${esc(a.role)}</div>` : ""}
        <div class="agent-tile-state big" data-state="${esc(a.state)}"><i></i>${esc(stateLabel[a.state] ?? a.state)}${a.detail ? ` · ${esc(a.detail)}` : ""}</div>
        ${isActive ? `<div class="agent-page-note">Talking on this device now</div>` : ""}
      </div>
      <div class="agent-page-foot">
        <button class="btn primary" data-act="talk" ${online ? "" : "disabled"}>${isActive ? "Open conversation" : `Talk to ${esc(a.name)}`}</button>
      </div>`;
    if (html !== this.lastPage) {
      this.lastPage = html;
      page.innerHTML = html;
    }
  }
}
