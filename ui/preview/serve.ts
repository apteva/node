// Device preview for development: the Node UI inside device frames, with
// the stand-in server's controls next to it.
// bun ui/preview/serve.ts  →  http://127.0.0.1:7470
const port = Number(process.env.PORT ?? 7470);
const NODE = process.env.NODE_UI ?? "http://127.0.0.1:7480";
const SIM = process.env.NODE_SIM ?? "http://127.0.0.1:7490";
const page = Bun.file(new URL("./index.html", import.meta.url).pathname);
const font = Bun.file(new URL("../../node_modules/@fontsource-variable/jetbrains-mono/files/jetbrains-mono-latin-wght-normal.woff2", import.meta.url).pathname);

// The device's setup API only takes calls from its own page, so the preview
// drives it from here (server side) rather than from the browser.
async function nodeAPI(path: string, body: unknown = {}) {
  const res = await fetch(`${NODE}/api/${path}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `${path}: HTTP ${res.status}`);
  return data;
}
const snapshot = async () => (await fetch(`${NODE}/api/snapshot`)).json();
const sim = (cmd: string) => fetch(`${SIM}/sim`, { method: "POST", body: cmd });

// Skip first-boot setup: pair with node-sim and approve, as a person would
// in the dashboard. This is the "signed in" path.
async function signIn(): Promise<string> {
  let s = await snapshot();
  if (s.setup.mode === "connected") return "already connected";
  if (s.setup.mode !== "setup" && s.setup.locked) throw new Error("settings are locked by a PIN — reset first");
  if (!s.setup.network.online) await nodeAPI("setup/wifi", { ssid: "Studio", password: "dev-wifi-pass" }).catch(() => {});
  await nodeAPI("setup/server", { url: SIM });
  await nodeAPI("setup/pair");
  await new Promise((r) => setTimeout(r, 300));
  await sim("approve");
  for (let i = 0; i < 40; i++) {
    await new Promise((r) => setTimeout(r, 250));
    s = await snapshot();
    if (s.link.state === "connected") break;
  }
  if (s.link.state !== "connected") throw new Error("paired, but the link didn't come up");
  await nodeAPI("setup/finish");
  return `connected to ${s.agent.name || "agent"}`;
}

Bun.serve({
  hostname: "127.0.0.1",
  port,
  async fetch(req) {
    const path = new URL(req.url).pathname;
    if (path === "/jetbrains-mono.woff2") return new Response(font, { headers: { "Content-Type": "font/woff2" } });
    if (req.method === "POST" && path.startsWith("/dev/")) {
      try {
        const msg = path === "/dev/sign-in" ? await signIn() : path === "/dev/reset" ? (await nodeAPI("setup/reset"), "reset to first boot") : null;
        if (msg === null) return new Response("not found", { status: 404 });
        return Response.json({ ok: true, message: msg });
      } catch (e) {
        return Response.json({ ok: false, message: (e as Error).message }, { status: 500 });
      }
    }
    return new Response(page, { headers: { "Content-Type": "text/html; charset=utf-8" } });
  },
});
console.log(`device preview on http://127.0.0.1:${port}`);
