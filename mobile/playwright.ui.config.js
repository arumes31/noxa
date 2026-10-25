import { defineConfig } from "@playwright/test";

export default defineConfig({
    outputDir: "test-results/ui",
    testDir: "ui-tests",
    use: { baseURL: "http://127.0.0.1:5174", serviceWorkers: "block", locale: "en-GB" },
    webServer: [{ command: "npx vite preview --host 127.0.0.1 --port 5174 --strictPort", url: "http://127.0.0.1:5174/app/" }, { command: "npx vite --host 127.0.0.1 --port 5175 --strictPort", url: "http://127.0.0.1:5175/app/" }],
});
