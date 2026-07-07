#!/usr/bin/env node
// Renders demo/visual/index.html to toll-demo.mp4 (plus a thumbnail PNG)
// using headless Chrome over CDP: one navigation, then window.__setTime(t)
// per frame + Page.captureScreenshot, assembled by ffmpeg.
// Adapted from fair/demo/fairness_visual/render_video.mjs.

import { spawn } from "node:child_process";
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import net from "node:net";

const __dirname = dirname(fileURLToPath(import.meta.url));
const chromePath = process.env.CHROME_PATH || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const demoPath = resolve(__dirname, "index.html");
const outputPath = resolve(__dirname, "toll-demo.mp4");
const thumbPath = resolve(__dirname, "toll-demo-thumbnail.png");
const framesDir = resolve(tmpdir(), "toll-demo-frames");
const width = Number(process.env.TOLL_DEMO_WIDTH || 1920);
const height = Number(process.env.TOLL_DEMO_HEIGHT || 1080);
const fps = Number(process.env.TOLL_DEMO_FPS || 16);
const durationSeconds = Number(process.env.TOLL_DEMO_DURATION || 36);
const thumbnailAt = Number(process.env.TOLL_DEMO_THUMB_T || 15);
const frameCount = Math.round(fps * durationSeconds);

if (!existsSync(chromePath)) throw new Error(`Chrome not found at ${chromePath}`);

function getFreePort() {
  return new Promise((resolvePort, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      server.close(() => resolvePort(address.port));
    });
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitForJSON(url, timeoutMs = 10000) {
  const start = Date.now();
  let lastError;
  while (Date.now() - start < timeoutMs) {
    try {
      const response = await fetch(url);
      if (response.ok) return response.json();
      lastError = new Error(`${response.status} ${response.statusText}`);
    } catch (error) {
      lastError = error;
    }
    await sleep(100);
  }
  throw new Error(`Timed out waiting for ${url}: ${lastError?.message || "unknown"}`);
}

function connectCDP(webSocketURL) {
  const socket = new WebSocket(webSocketURL);
  let nextID = 1;
  const pending = new Map();

  socket.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    if (message.id && pending.has(message.id)) {
      const { resolve: res, reject } = pending.get(message.id);
      pending.delete(message.id);
      if (message.error) reject(new Error(message.error.message));
      else res(message.result || {});
    }
  });

  function send(method, params = {}) {
    const id = nextID++;
    socket.send(JSON.stringify({ id, method, params }));
    return new Promise((res, reject) => {
      pending.set(id, { resolve: res, reject });
      setTimeout(() => {
        if (!pending.has(id)) return;
        pending.delete(id);
        reject(new Error(`CDP timeout: ${method}`));
      }, 30000);
    });
  }

  return new Promise((res, reject) => {
    socket.addEventListener("open", () => res({ send, close: () => socket.close() }));
    socket.addEventListener("error", reject);
  });
}

function run(command, args) {
  return new Promise((res, reject) => {
    const child = spawn(command, args, { stdio: "inherit" });
    child.on("error", reject);
    child.on("exit", (code) => (code === 0 ? res() : reject(new Error(`${command} exited ${code}`))));
  });
}

const frameName = (i) => resolve(framesDir, `frame-${String(i).padStart(4, "0")}.png`);

async function main() {
  const port = await getFreePort();
  const profileDir = mkdtempSync(resolve(tmpdir(), "toll-demo-chrome-"));
  const demoURL = `${pathToFileURL(demoPath).href}?recording=1&t=0`;

  rmSync(framesDir, { force: true, recursive: true });
  mkdirSync(framesDir, { recursive: true });

  const chrome = spawn(chromePath, [
    "--headless=new", "--disable-gpu", "--disable-background-networking",
    "--disable-component-update", "--disable-default-apps", "--disable-sync",
    "--hide-scrollbars", "--no-default-browser-check", "--no-first-run",
    `--remote-debugging-port=${port}`, `--user-data-dir=${profileDir}`, demoURL,
  ], { stdio: ["ignore", "ignore", "inherit"] });

  try {
    const targets = await waitForJSON(`http://127.0.0.1:${port}/json/list`);
    const page = targets.find((t) => t.type === "page") || targets[0];
    if (!page?.webSocketDebuggerUrl) throw new Error("no Chrome page target");

    const cdp = await connectCDP(page.webSocketDebuggerUrl);
    await cdp.send("Page.enable");
    await cdp.send("Runtime.enable");
    await cdp.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: false });
    // wait for page + fonts
    for (let i = 0; i < 100; i++) {
      const { result } = await cdp.send("Runtime.evaluate", { expression: "!!window.__READY" });
      if (result?.value === true) break;
      await sleep(100);
    }
    await cdp.send("Runtime.evaluate", {
      expression: "document.fonts ? document.fonts.ready : Promise.resolve()",
      awaitPromise: true,
    });

    let thumbFrame = 0;
    for (let i = 0; i < frameCount; i++) {
      const t = i / fps;
      await cdp.send("Runtime.evaluate", { expression: `window.__setTime(${t})` });
      const shot = await cdp.send("Page.captureScreenshot", { format: "png" });
      writeFileSync(frameName(i), Buffer.from(shot.data, "base64"));
      if (t <= thumbnailAt) thumbFrame = i;
      if (i % 64 === 0) console.log(`frame ${i}/${frameCount} (t=${t.toFixed(1)}s)`);
    }

    copyFileSync(frameName(thumbFrame), thumbPath);
    cdp.close();
  } finally {
    chrome.kill("SIGTERM");
    await sleep(500);
    rmSync(profileDir, { force: true, recursive: true });
  }

  await run("ffmpeg", [
    "-y", "-framerate", String(fps), "-i", resolve(framesDir, "frame-%04d.png"),
    "-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart", outputPath,
  ]);
  rmSync(framesDir, { force: true, recursive: true });
  console.log(`wrote ${outputPath} and ${thumbPath}`);
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
