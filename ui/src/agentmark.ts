// The Apteva agent mark, ported from ui-kit/src/AgentMark.tsx so the device
// shows agents exactly like the dashboard: one robot silhouette, plus a small
// badge for the agent's role. Keep the paths in sync with ui-kit.

export const AGENT_ICONS = [
  "robot", "assistant", "manager", "code", "social", "research", "writer", "design",
  "analyst", "support", "sales", "marketing", "finance", "operations", "security", "planner",
] as const;
export type AgentIconId = (typeof AGENT_ICONS)[number];

export function suggestedAgentIcon(icon?: string): AgentIconId {
  const mapped: Record<string, AgentIconId> = {
    user: "assistant", search: "research", pen: "writer", mail: "operations",
    message: "support", chart: "analyst", automation: "code", teacher: "assistant",
  };
  const s = mapped[icon || ""] || icon;
  return (AGENT_ICONS as readonly string[]).includes(s ?? "") ? (s as AgentIconId) : "robot";
}

const roleSymbols: Record<Exclude<AgentIconId, "robot">, string> = {
  assistant: `<path d="M5 6h14a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H10l-5 3v-3a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2Z"/><path d="M8 12h.01M12 12h.01M16 12h.01" stroke-width="3"/>`,
  manager: `<rect x="3" y="8" width="18" height="13" rx="2"/><path d="M8 8V5a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v3M3 13h18M10 13v3h4v-3"/>`,
  code: `<path d="m8 5-6 7 6 7M16 5l6 7-6 7M14 3l-4 18"/>`,
  social: `<path d="M3 10h4l11-6v16L7 14H3zM7 14l2 6h4M21 8l2-2M21 12h2M21 16l2 2"/>`,
  research: `<circle cx="10" cy="10" r="7"/><path d="m15.5 15.5 6 6"/>`,
  writer: `<path d="m4 20 4.5-1 12-12a2.1 2.1 0 0 0-3-3l-12 12L4 20ZM15 6l3 3"/>`,
  design: `<path d="M12 3a9 9 0 1 0 0 18h2a2 2 0 0 0 1-3.7 2 2 0 0 1 1-3.7h2A4 4 0 0 0 22 10c0-4-4-7-10-7Z"/><path d="M7 10h.01M11 7h.01M16 8h.01M8 15h.01" stroke-width="3.5"/>`,
  analyst: `<path d="M3 21h18M5 13h3v8H5zM11 8h3v13h-3zM17 3h3v18h-3z"/>`,
  support: `<path d="M4 13v-2a8 8 0 0 1 16 0v2M4 13H2v5h4v-5H4ZM20 13h2v5h-4v-5h2ZM19 18a5 5 0 0 1-5 4h-2"/><rect x="9" y="20" width="4" height="3" rx="1.5"/>`,
  sales: `<path d="M3 12.5V4a1 1 0 0 1 1-1h8.5L22 12.5a2 2 0 0 1 0 3L15.5 22a2 2 0 0 1-3 0L3 12.5Z"/><circle cx="8" cy="8" r="1.5"/>`,
  marketing: `<circle cx="11" cy="13" r="9"/><circle cx="11" cy="13" r="5"/><path d="m11 13 10-10M18 3h3v3"/>`,
  finance: `<circle cx="12" cy="12" r="10"/><path d="M15.5 7.5c-.8-1-2-1.5-3.5-1.5-2 0-3.5 1-3.5 2.7 0 3.8 7 1.4 7 5.5 0 1.7-1.5 2.8-3.5 2.8-1.5 0-2.8-.5-3.7-1.7M12 4v16"/>`,
  operations: `<path d="M10 2h4l.7 2.5 2 1 2.3-.9 2 3.4-1.7 1.8v2.4l1.7 1.8-2 3.4-2.3-.9-2 1L14 20h-4l-.7-2.5-2-1-2.3.9-2-3.4 1.7-1.8V9.8L3 8l2-3.4 2.3.9 2-1L10 2Z" transform="translate(0 1)"/><circle cx="12" cy="12" r="3"/>`,
  security: `<path d="m12 2 9 4v6c0 6-4 9-9 11-5-2-9-5-9-11V6l9-4Z"/><path d="m8 12 3 3 5-5"/>`,
  planner: `<rect x="3" y="5" width="18" height="17" rx="2"/><path d="M7 2v6M17 2v6M3 10h18M7 14h2M12 14h2M17 14h.01M7 18h2M12 18h2"/>`,
};

// The glyph only; the tile around it (border, background, size) is CSS.
export function agentGlyph(icon?: string): string {
  const id = suggestedAgentIcon(icon);
  const badge = id === "robot" ? "" :
    `<circle cx="29" cy="29" r="8.2" fill="var(--input)" stroke="none"/>
     <svg x="20" y="20" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round">${roleSymbols[id]}</svg>`;
  return `<svg viewBox="0 0 40 40" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <g${id === "robot" ? ` transform="translate(0 4)"` : ""}>
      <rect x="9" y="5" width="22" height="22" rx="6"/><rect x="6" y="12" width="3" height="9" rx="1.5"/><rect x="31" y="12" width="3" height="9" rx="1.5"/>
      <path d="M16 14v5M24 14v5" stroke-width="3.5"/>
    </g>${badge}</svg>`;
}
