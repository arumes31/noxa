import assert from "node:assert/strict";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { chromium } from "@playwright/test";

test("PWA encrypted renderers and preview API keep URLs private; channels request sanitized metadata", { timeout: 60000 }, async () => {
    const server = await createServer({ base: "/", root: fileURLToPath(new URL("../", import.meta.url)), server: { host: "127.0.0.1", port: 0 }, logLevel: "error" });
    await server.listen();
    const browser = await chromium.launch();
    try {
        const page = await browser.newPage();
        await page.route(server.resolvedUrls.local[0], route => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>preview privacy</title>" }));
        await page.goto(server.resolvedUrls.local[0]);
        const result = await page.evaluate(async () => {
            const { richBody } = await import("/src/ui/rich-text.js");
            const { ChatSession } = await import("/src/core/chat-session.js");
            const { Msg } = await import("/src/core/frame.js");
            const requests = [];
            const conn = { on: () => () => {}, request: async (type, body) => {
                requests.push({ type, body });
                return { title: "Channel preview", thumbnail: body.resource.includes("raster") ? "data:image/jpeg;base64,/9j/2Q==" : "https://remote.example/private.jpg" };
            } };
            const session = new ChatSession(conn, {}, { capabilities: ["server_link_previews_v1"] });
            const providers = ["https://example.test/private", "https://youtu.be/dQw4w9WgXcQ", "https://x.com/alex/status/123", "https://www.twitch.tv/twitch"];
            for (const conversation of [undefined, "direct", "group", "unknown"]) {
                for (const url of providers) {
                    const node = richBody(url, { session, conversation, channelID: 1 });
                    document.body.append(node);
                    node.querySelector("a")?.dispatchEvent(new MouseEvent("mouseover", { bubbles: true }));
                    if (node.querySelector(".link-preview")) throw new Error("private preview card");
                    if (!node.querySelector("a[href]")) throw new Error("explicit link removed");
                }
                try { await session.linkPreview("web", "https://example.test/private", conversation); throw new Error("private API accepted"); }
                catch (error) { if (!error.message.includes("disabled")) throw error; }
            }
            await new Promise(resolve => setTimeout(resolve, 30));
            const privateCount = requests.length;
            const channel = richBody(providers[0], { session, conversation: "channel", channelID: 1 });
            document.body.append(channel);
            const raster = richBody("https://example.test/raster", { session, conversation: "channel", channelID: 1 });
            document.body.append(raster);
            await new Promise(resolve => setTimeout(resolve, 30));
            return { privateCount, requests, frame: Msg.LinkPreviewRequest, title: channel.textContent,
                remoteImages: [...document.querySelectorAll("img")].map(img => img.getAttribute("src")) };
        });
        assert.equal(result.privateCount, 0);
        assert.equal(result.requests.length, 2);
        assert.equal(result.requests[0].type, result.frame);
        assert.match(result.title, /Channel preview/);
        assert.deepEqual(result.remoteImages, ["data:image/jpeg;base64,/9j/2Q=="]);
    } finally { await browser.close(); await server.close(); }
});
