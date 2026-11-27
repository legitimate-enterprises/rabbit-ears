import { defineConfig } from "@playwright/test";
import { API_BASE } from "./src/api/client";

// Real server (built from ../server, fresh database, the fixtures as media)
// behind the Vite dev server, which proxies /api to it.
const API = 18477;
const WEB = 15173;
const data = `${process.env.TMPDIR ?? "/tmp"}/rabbit-ears-e2e`;

export default defineConfig({
  testDir: "e2e",
  testMatch: "*.e2e.ts",
  timeout: 90_000,
  workers: 1,
  use: { baseURL: `http://localhost:${WEB}` },
  // Google Chrome, not the bundled Chromium, which has no H.264 or HLS. The
  // admin spec sets the server up, so it runs once, first.
  projects: [
    {
      name: "chrome",
      use: {
        channel: "chrome",
        launchOptions: { args: ["--autoplay-policy=no-user-gesture-required"] },
      },
    },
    {
      name: "firefox",
      testMatch: "viewer.e2e.ts",
      dependencies: ["chrome"],
      use: {
        browserName: "firefox",
        launchOptions: { firefoxUserPrefs: { "media.autoplay.default": 0 } },
      },
    },
    {
      name: "webkit",
      testMatch: "viewer.e2e.ts",
      dependencies: ["chrome"],
      use: { browserName: "webkit" },
    },
  ],
  webServer: [
    {
      command: `rm -rf ${data} && mkdir -p ${data} && (cd ../server && go build -o ${data}/rabbit-ears ./cmd/rabbit-ears) && RABBIT_EARS_PORT=${API} RABBIT_EARS_DB=${data}/re.db RABBIT_EARS_DATA=${data} ${data}/rabbit-ears`,
      url: `http://localhost:${API}${API_BASE}/health`,
      timeout: 180_000,
    },
    {
      command: `RABBIT_EARS_API=http://localhost:${API} npx vite --port ${WEB} --strictPort`,
      url: `http://localhost:${WEB}/admin/`,
    },
  ],
});
