import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { fileURLToPath } from "node:url";
import { test as base } from "./fixtures.js";

export const test = base.extend({
    callRelay: [async ({}, use) => {
        const child = spawn("go", ["run", "./internal/turn/testdata/browser_fixture"], {
            cwd: fileURLToPath(new URL("../../../", import.meta.url)), stdio: ["pipe", "pipe", "pipe"],
        });
        let errors = "";
        child.stderr.on("data", data => { errors += data; });
        const lines = createInterface({ input: child.stdout });
        try {
            const servers = await new Promise((resolve, reject) => {
                const timer = setTimeout(() => reject(new Error(`TURN fixture startup timed out: ${errors}`)), 60000);
                child.once("error", error => { clearTimeout(timer); reject(error); });
                child.once("exit", code => { clearTimeout(timer); reject(new Error(`TURN fixture exited (${code}): ${errors}`)); });
                lines.once("line", line => { clearTimeout(timer); try { resolve(JSON.parse(line)); } catch (error) { reject(error); } });
            });
            await use(servers);
        } finally {
            lines.close();
            // EOF closes the relay even when go run owns a separate child process.
            child.stdin.end();
            if (child.exitCode === null) await new Promise(resolve => child.once("exit", resolve));
        }
    }, { scope: "worker", timeout: 70000 }],
    page: async ({ page, callRelay }, use) => {
        await page.addInitScript(servers => { window.__callIceServers = servers; }, callRelay);
        await use(page);
    },
});
