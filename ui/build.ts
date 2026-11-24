// Builds the device UI into ui/dist, which the Go binary embeds.
import { cp, mkdir, rm } from "node:fs/promises";

const dist = new URL("./dist/", import.meta.url).pathname;
const src = new URL("./src/", import.meta.url).pathname;
const watch = process.argv.includes("--watch");

async function build() {
  await rm(dist, { recursive: true, force: true });
  await mkdir(dist, { recursive: true });
  const result = await Bun.build({
    entrypoints: [src + "main.ts"],
    outdir: dist,
    target: "browser",
    // cog/WPE on Raspberry Pi OS is recent WebKit; no need to go older.
    minify: !watch,
    sourcemap: watch ? "inline" : "none",
  });
  if (!result.success) {
    for (const log of result.logs) console.error(log);
    if (!watch) process.exit(1);
    return;
  }
  await cp(src + "index.html", dist + "index.html");
  await cp(src + "styles.css", dist + "styles.css");
  // Same face as the dashboard's terminal theme, bundled for offline use.
  await cp(new URL("../node_modules/@fontsource-variable/jetbrains-mono/files/jetbrains-mono-latin-wght-normal.woff2", import.meta.url).pathname, dist + "jetbrains-mono.woff2");
  await Bun.write(dist + ".gitkeep", "");
  console.log(`ui built → ${dist}`);
}

await build();

if (watch) {
  const { watch: fsWatch } = await import("node:fs");
  let timer: ReturnType<typeof setTimeout> | undefined;
  fsWatch(src, { recursive: true }, () => {
    clearTimeout(timer);
    timer = setTimeout(build, 80);
  });
  console.log("watching ui/src …");
}
