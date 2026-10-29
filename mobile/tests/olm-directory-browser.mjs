import assert from "node:assert/strict";
import { test } from "node:test";
import { createServer } from "node:http";
import { readFile, readdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromium } from "@playwright/test";
const root = fileURLToPath(new URL("../../", import.meta.url));
test(
    "packaged Chromium Olm directory verifies the shared native fixtures",
    { timeout: 30000 },
    async () => {
        const assets = path.join(root, "web/app/assets");
        const entry = (await readdir(assets)).find((n) =>
            /^directory-[\w-]+\.js$/.test(n),
        );
        assert.ok(entry, "build PWA first");
        const notices = await readFile(
            path.join(root, "web/app/olm-directory-licenses.txt"),
            "utf8",
        );
        assert.match(notices, /@noble\/curves 2\.4\.0/);
        assert.match(notices, /@noble\/hashes 2\.4\.0/);
        assert.equal(
            (notices.match(/Permission is hereby granted/g) || []).length,
            2,
        );
        const fixture = JSON.parse(
            await readFile(
                path.join(
                    root,
                    "internal/e2ee/olmdir/testdata/directory-v1.json",
                ),
                "utf8",
            ),
        );
        const server = createServer(async (req, res) => {
            const url = new URL(req.url, "http://localhost");
            if (url.pathname === "/") {
                res.setHeader("Content-Type", "text/html");
                res.setHeader(
                    "Content-Security-Policy",
                    "script-src 'self'; object-src 'none'",
                );
                res.end(
                    "<!doctype html><title>Directory fixture</title>" +
                        "<script>globalThis.directoryInlineProbe = true;</script>",
                );
                return;
            }
            const file = path.resolve(root, "web", `.${url.pathname}`);
            if (!file.startsWith(assets + path.sep)) {
                res.writeHead(404).end();
                return;
            }
            try {
                res.setHeader("Content-Type", "application/javascript");
                res.end(await readFile(file));
            } catch {
                res.writeHead(404).end();
            }
        });
        let browser;
        try {
            await new Promise((resolve) =>
                server.listen(0, "127.0.0.1", resolve),
            );
            browser = await chromium.launch({ headless: true });
            const page = await browser.newPage();
            // Observe the isolated fixture's document policy, not production CSP.
            await page.addInitScript(() => {
                globalThis.directoryCspViolations = [];
                document.addEventListener("securitypolicyviolation", (event) => {
                    globalThis.directoryCspViolations.push(event.blockedURI);
                });
            });
            const response = await page.goto(
                `http://127.0.0.1:${server.address().port}/`,
            );
            assert.equal(
                response.headers()["content-security-policy"],
                "script-src 'self'; object-src 'none'",
            );
            await page.waitForFunction(() =>
                globalThis.directoryCspViolations.includes("inline"),
            );
            assert.equal(
                await page.evaluate(() => globalThis.directoryInlineProbe),
                undefined,
                "document CSP must block the inline probe",
            );
            const result = await page.evaluate(
                async ({ entry, f }) => {
                    const { verifyOlmClaim, bundleTranscript, serverBinding } =
                        await import(`/app/assets/${entry}`);
                    const bytes = (s) =>
                        Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
                    const expected = {
                        server: f.server,
                        principal: f.principal,
                        deviceID: f.device_id,
                        rootSigning: bytes(f.root_signing),
                        rootEncryption: bytes(f.root_encryption),
                        devices: f.devices,
                        pinnedOlmIdentity: f.bundle.identity,
                        keyID: f.key_id,
                        now: f.now,
                    };
                    const good = verifyOlmClaim(f.bundle, expected).id;
                    const rejects = f.bundle_cases.map((c) => {
                        try {
                            verifyOlmClaim(c.bundle, expected);
                            return false;
                        } catch {
                            return true;
                        }
                    });
                    const chains = f.chain_cases.map((c) => {
                        try {
                            verifyOlmClaim(f.bundle, {
                                ...expected,
                                devices: c.devices,
                            });
                            return true;
                        } catch {
                            return false;
                        }
                    });
                    return {
                        good,
                        rejects,
                        chains,
                        transcript: btoa(
                            String.fromCharCode(...bundleTranscript(f.bundle)),
                        ),
                        binding: serverBinding(Array(32).fill("AA").join(":")),
                    };
                },
                { entry, f: fixture },
            );
            assert.equal(result.good, fixture.key_id);
            assert.equal(result.transcript, fixture.transcript);
            assert.equal(result.binding, fixture.server);
            assert.ok(result.rejects.every(Boolean));
            assert.deepEqual(
                result.chains,
                fixture.chain_cases.map((c) => c.ids.includes("phone")),
            );
        } finally {
            await browser?.close();
            await new Promise((resolve) => server.close(resolve));
        }
    },
);
