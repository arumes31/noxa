import { defineConfig, devices } from "@playwright/test";

// End-to-end tests run the real server binary (see tests/harness.mjs) and
// need NOXA_E2E_BIN and NOXA_E2E_DATABASE_URL; build the app first.
export default defineConfig({
    testDir: "tests",
    testMatch: /.*\.spec\.js/,
    timeout: 60000,
    workers: 1,
    reporter: [["line"]],
    globalSetup: "./tests/global-setup.mjs",
    use: {
        ...devices["Pixel 7"],
        trace: "retain-on-failure",
        permissions: ["microphone"],
        // Fake microphones, and real local addresses so the SFU can be reached.
        launchOptions: { args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream", "--disable-features=WebRtcHideLocalIpsWithMdns"] },
    },
});
