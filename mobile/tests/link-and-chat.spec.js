import { expect, test } from "@playwright/test";

import { ChatSession } from "../src/core/chat-session.js";
import { keyFingerprint, linkClaimTranscript, linkSAS, publicKeyPEM, toBase64, toBase64URL, utf8 } from "../src/core/crypto.js";
import { announcedBinding, linkBindingTranscript } from "../src/core/link-binding.js";
import { Msg } from "../src/core/frame.js";
import { desktopSession, sign } from "./harness.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
});

function nextDeviceEvent(conn, kind) {
    return new Promise(resolve => {
        const stop = conn.on(Msg.DeviceEvent, event => {
            if (event.event === kind) {
                stop();
                resolve(event);
            }
        });
    });
}

async function approve(desktop, event, { bind = false } = {}) {
    const list = await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    const self = list.devices.find(d => d.current);
    const transcript = utf8(`noxa-device-approval-v1\n${desktop.uniqueID}\n${self.device_id}\n${event.ed25519_public_key}\n${event.x25519_public_key}`);
    const binding = bind ? announcedBinding(desktop.auth.tls_fingerprint) : "";
    if (bind) expect(binding).toMatch(/^tls-sha256:[0-9a-f]{64}$/);
    return desktop.conn.request(Msg.DeviceRequest, {
        ...(binding ? { server_binding: binding, binding_signature: toBase64(sign(linkBindingTranscript(desktop.uniqueID, self.device_id, event.ed25519_public_key, event.x25519_public_key, binding), desktop.identity.edSecret)) } : {}),
        action: "link_decide", link_id: event.link_id, approve: true, signature: toBase64(sign(transcript, desktop.identity.edSecret)),
    }, Msg.DeviceResult, { accept: r => r.action === "link_decide" });
}

test("a phone links by QR, compares the code, signs in and chats with the desktop", async ({ page }) => {
    const desktop = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    const deskChat = new ChatSession(desktop.conn, desktop.identity, desktop.auth);
    await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    const link = await desktop.conn.request(Msg.DeviceRequest, { action: "link_create" }, Msg.DeviceResult, { accept: r => r.action === "link_create" });
    const secret = Uint8Array.from(Buffer.from(link.secret, "base64"));
    const claimed = nextDeviceEvent(desktop.conn, "link_claimed");

    page.on("pageerror", error => console.log("pageerror:", error.message));
    await page.goto(`${server.webURL}/link#id=${link.link_id}&k=${toBase64URL(secret)}`);
    await page.getByRole("button", { name: "Continue" }).click();
    const event = await claimed;
    expect(event.platform).toContain("Android");
    const desktopSAS = linkSAS(secret, linkClaimTranscript(event.link_id, event.ed25519_public_key, event.x25519_public_key));
    await expect(page.locator(".sas")).toHaveText(desktopSAS);
    await expect(page.getByText(/Desktop fingerprint: [0-9a-f]{4} /)).toBeVisible();
    expect(page.url()).not.toContain(link.link_id);

    const decided = await approve(desktop, event);
    expect(decided.state).toBe("approved");

    // Signed in: the passkey offer appears first.
    // Two "Not now" buttons can show: the passkey offer (a dialog) and the install banner.
    await page.getByRole("dialog").getByRole("button", { name: "Not now" }).click({ timeout: 15000 }).catch(() => {});
    await page.locator("#install-banner").getByRole("button", { name: "Not now" }).click({ timeout: 2000 }).catch(() => {});
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible({ timeout: 15000 });

    await page.getByRole("link", { name: "Servers", exact: true }).click();
    await page.getByRole("link", { name: "Server chat" }).click();
    const composer = page.getByRole("textbox", { name: /Message/ });
    await composer.fill("hello from the phone");
    const fromPhone = new Promise(resolve => deskChat.addEventListener("messages", () => {
        const found = (deskChat.messages.get(0) || []).find(m => m.text === "hello from the phone");
        if (found) resolve(found);
    }));
    await composer.press("Enter");
    const received = await fromPhone;
    expect(received.from.replace(/#M\d+$/, "")).toBe(server.accounts.owner.nickname);

    await deskChat.send(0, "hi phone, desktop here");
    await expect(page.getByRole("log")).toContainText("hi phone, desktop here");

    // Channel chat works through subscription, without joining voice.
    await page.getByRole("link", { name: "Back" }).click();
    await page.getByRole("link", { name: "Public1" }).click();
    await deskChat.subscribe([2]);
    await deskChat.send(2, "channel hello");
    await expect(page.getByRole("log", { name: "# Public1" })).toContainText("channel hello");

    // The phone's device is listed, and revoking it signs the phone out.
    await page.getByRole("link", { name: "Back" }).click();
    await page.getByRole("link", { name: "You", exact: true }).click();
    await expect(page.getByText("This device", { exact: false }).first()).toBeVisible();
    const list = await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    // Other tests link phones to the same account; this one is the newest.
    const phone = list.devices.filter(d => d.kind === "web").sort((a, b) => b.created_at - a.created_at)[0];
    await desktop.conn.request(Msg.DeviceRequest, { action: "revoke", device_id: phone.device_id }, Msg.DeviceResult, { accept: r => r.action === "revoke" });
    await expect(page.getByRole("button", { name: "Sign in with passkey" })).toBeVisible({ timeout: 15000 });
    deskChat.close();
});

test("a typed code works and a denial returns to the connect screen", async ({ page }) => {
    const desktop = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    const link = await desktop.conn.request(Msg.DeviceRequest, { action: "link_create" }, Msg.DeviceResult, { accept: r => r.action === "link_create" });
    const claimed = nextDeviceEvent(desktop.conn, "link_claimed");

    await page.goto(`${server.webURL}/app/`);
    await page.getByRole("textbox", { name: "Code from your desktop" }).fill(link.code.toLowerCase());
    await page.getByRole("button", { name: "Continue" }).click();
    const event = await claimed;
    expect(event.via).toBe("code");
    const sas = linkSAS(utf8(link.code.replace("-", "")), linkClaimTranscript(event.link_id, event.ed25519_public_key, event.x25519_public_key));
    await expect(page.locator(".sas")).toHaveText(sas);
    await desktop.conn.request(Msg.DeviceRequest, { action: "link_decide", link_id: event.link_id }, Msg.DeviceResult, { accept: r => r.action === "link_decide" });
    await expect(page.getByText("Your desktop denied this sign-in.")).toBeVisible({ timeout: 15000 });
    await expect(page.getByRole("button", { name: "Continue" })).toBeVisible();
    desktop.conn.close();
});


test("signed QR linking and relinking open secure messaging and survive a reload", async ({ page, browserName }) => {
    const desktop = await desktopSession(server.controlAddr, browserName === "webkit" ? server.accounts.kai : server.accounts.rowan, { version: info.version });
    const errors = []; page.on("pageerror", error => errors.push(error.message));
    try {
        await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
        for (let round = 0; round < 2; round++) {
            const link = await desktop.conn.request(Msg.DeviceRequest, { action: "link_create" }, Msg.DeviceResult, { accept: r => r.action === "link_create" });
            const secret = Uint8Array.from(Buffer.from(link.secret, "base64"));
            const claimed = nextDeviceEvent(desktop.conn, "link_claimed");
            const anchor = keyFingerprint(publicKeyPEM(desktop.identity.edPublic));
            await page.goto(`${server.webURL}/link#id=${link.link_id}&k=${toBase64URL(secret)}&a=${encodeURIComponent(anchor)}`);
            await page.getByRole("button", { name: "Continue" }).click();
            const event = await claimed;
            await expect(page.locator(".sas")).toHaveText(linkSAS(secret, linkClaimTranscript(event.link_id, event.ed25519_public_key, event.x25519_public_key)));
            expect((await approve(desktop, event, { bind: true })).state).toBe("approved");
            await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible({ timeout: 30000 });
            await expect.poll(() => secureState(page)).toEqual({ linked: true, guest: false, ready: true, error: "" });
            await page.reload();
            await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible({ timeout: 30000 });
            await expect.poll(() => secureState(page)).toEqual({ linked: true, guest: false, ready: true, error: "" });
        }
        expect(errors).toEqual([]);
    } finally { desktop.conn.close(); }
});

async function secureState(page) {
    return page.evaluate(async () => {
        const scripts = performance.getEntriesByType("resource").filter(r => r.name.includes("/app/assets/") && r.name.endsWith(".js"));
        for (const script of scripts) {
            const exports = await import(script.name);
            const app = Object.values(exports).find(value => value && typeof value === "object" && "identity" in value && "meta" in value && "ratchet" in value && value.session?.conn);
            if (app) return { linked: !!app.meta.linked, guest: !!app.meta.guest, ready: !!app.ratchet, error: app.ratchetError?.code || "" };
        }
        return null;
    });
}


test("microphone capture can recover on the same encrypted voice connection", async ({ page, browserName }) => {
    test.skip(browserName !== "chromium", "Requires the Chromium fake microphone used by the voice harness");
    await page.addInitScript(() => {
        localStorage.setItem("noxa.settings", JSON.stringify({activation:"continuous",secureMessagingNoticeSeen:true}));
        const getUserMedia=navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
        window.__micAttempts=0;
        navigator.mediaDevices.getUserMedia=(...args)=>{
            if(++window.__micAttempts===1) return Promise.reject(new DOMException("Microphone busy", "NotReadableError"));
            return getUserMedia(...args);
        };
        window.__voicePeers=[];
        const Peer=window.RTCPeerConnection;
        window.RTCPeerConnection=new Proxy(Peer,{construct(target,args){const pc=Reflect.construct(target,args);window.__voicePeers.push(pc);return pc;}});
    });
    const desktop=await desktopSession(server.controlAddr,server.accounts.ivy,{version:info.version});
    try {
        await desktop.conn.request(Msg.DeviceRequest,{action:"list"},Msg.DeviceResult,{accept:r=>r.action==="list"});
        const link=await desktop.conn.request(Msg.DeviceRequest,{action:"link_create"},Msg.DeviceResult,{accept:r=>r.action==="link_create"});
        const secret=Uint8Array.from(Buffer.from(link.secret,"base64")), claimed=nextDeviceEvent(desktop.conn,"link_claimed");
        const anchor=keyFingerprint(publicKeyPEM(desktop.identity.edPublic));
        await page.goto(`${server.webURL}/link#id=${link.link_id}&k=${toBase64URL(secret)}&a=${encodeURIComponent(anchor)}`);
        await page.getByRole("button", { name: "Continue" }).click();
        const event=await claimed;
        await expect(page.locator(".sas")).toHaveText(linkSAS(secret,linkClaimTranscript(event.link_id,event.ed25519_public_key,event.x25519_public_key)));
        expect((await approve(desktop,event,{bind:true})).state).toBe("approved");
        await expect(page.getByRole("navigation",{name:"Main"})).toBeVisible({timeout:30000});
        for(let i=0;i<4;i++) {
            const dialog=page.getByRole("dialog").last();
            if(!await dialog.isVisible())break;
            const close=dialog.getByRole("button",{name:/^(Close|Not now)$/}).first();
            if(await close.isVisible()) await close.click(); else break;
        }
        await page.getByRole("link",{name:"Servers",exact:true}).click();
        await page.getByRole("button",{name:"Join Public1",exact:true}).click();
        const warning=page.locator(".vc-mic-unavailable");
        await expect(warning).toContainText("Others cannot hear you");
        await warning.getByRole("button",{name:"Reconnect microphone",exact:true}).click();
        await expect(warning).toBeHidden();
        await expect.poll(()=>page.evaluate(async()=>{
            const pc=window.__voicePeers[0];if(pc?.connectionState!=="connected")return false;
            return [...(await pc.getStats()).values()].some(row=>row.type==="outbound-rtp"&&row.kind==="audio"&&row.packetsSent>10);
        }),{timeout:30000}).toBe(true);
        expect(await page.evaluate(()=>({peers:window.__voicePeers.length,microphones:window.__micAttempts}))).toEqual({peers:1,microphones:2});
        await page.getByRole("button",{name:"Leave",exact:true}).click();
    } finally {desktop.conn.close();}
});
