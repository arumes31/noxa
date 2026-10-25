import { defineConfig, devices } from "@playwright/test";

// End-to-end tests run the real server binary (see tests/harness.mjs) and
// need NOXA_E2E_BIN and NOXA_E2E_DATABASE_URL; build the app first.
const media = {
    trace: "retain-on-failure",
    permissions: ["microphone"],
    // Fake microphones, and real local addresses so the SFU can be reached.
    launchOptions: { args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream", "--disable-features=WebRtcHideLocalIpsWithMdns"] },
};

export default defineConfig({
    outputDir: "test-results/e2e",
    testDir: "tests",
    testMatch: /.*\.spec\.js/,
    timeout: 60000,
    workers: 1,
    reporter: [["line"]],
    globalSetup: "./tests/global-setup.mjs",
    projects: [
        // Phone screens; the wide layout's spec runs in "tablet" only.
        { name: "phone", testIgnore: /wide\.spec\.js/, use: { ...devices["Pixel 7"], ...media } },
        { name: "tablet", testMatch: /wide\.spec\.js/, use: { ...devices["iPad Pro 11 landscape"], ...media, browserName: "chromium" } },
    ],
});
