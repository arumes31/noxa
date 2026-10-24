import { expect, test } from "@playwright/test";

import { ChatSession } from "../src/core/chat-session.js";
import { fromBase64, sealDM } from "../src/core/crypto.js";
import { Msg } from "../src/core/frame.js";
import { desktopSession } from "./harness.mjs";
import { linkPhone, openServerToMembers, publishKeyRecord, registerDesktop } from "./helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
    // Pokes need the poke_members permission.
    const owner = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    await openServerToMembers(owner);
    // Profile pictures need upload_avatar, which a fresh server does not grant.
    const policy = (await owner.conn.request(Msg.RoleQuery, { channel_id: 0 }, Msg.RoleState)).policy;
    const everyone = policy.roles.find(r => r.id === policy.everyone_id);
    if (!(everyone.permissions || []).includes("upload_avatar")) {
        await owner.conn.request(Msg.RoleChange, { kind: "role_update", expected_revision: policy.revision,
            role: { ...everyone, permissions: [...(everyone.permissions || []), "upload_avatar"] } }, Msg.RoleChangeResult);
    }
    owner.conn.close();
});

function nextEvent(conn, type, match = () => true) {
    return new Promise(resolve => {
        const stop = conn.on(Msg.Event, event => {
            if (event.type === type && match(event.data || {})) {
                stop();
                resolve(event.data);
            }
        });
    });
}

/** Signs a member in on a desktop session with a tree of its own. */
async function member(name) {
    const desktop = await desktopSession(server.controlAddr, server.accounts[name], { version: info.version });
    const chat = new ChatSession(desktop.conn, desktop.identity, desktop.auth);
    return { ...desktop, chat };
}

/** The phone's session as another member sees it in the tree. */
async function phoneClient(observer, uid) {
    await expect.poll(() => observer.chat.clients().find(c => c.unique_id === uid && c.on_mobile)?.client_id || "", { timeout: 15000 }).not.toBe("");
    return observer.chat.clients().find(c => c.unique_id === uid && c.on_mobile);
}

/** A 600×400 PNG with a red left half, drawn by the page. */
async function samplePNG(page) {
    const base64 = await page.evaluate(() => {
        const canvas = document.createElement("canvas");
        canvas.width = 600;
        canvas.height = 400;
        const context = canvas.getContext("2d");
        context.fillStyle = "#2050c0";
        context.fillRect(0, 0, 600, 400);
        context.fillStyle = "#e02020";
        context.fillRect(0, 0, 300, 400);
        return canvas.toDataURL("image/png").split(",")[1];
    });
    return Buffer.from(base64, "base64");
}

test("status, display name and picture reach other members", async ({ page }) => {
    page.on("pageerror", error => console.log("pageerror:", error.message));
    const pia = await member("pia");
    await linkPhone(page, pia, server.webURL, expect);
    const quinn = await member("quinn");
    const phone = await phoneClient(quinn, pia.uniqueID);

    // Busy with a message, from the You screen's profile card.
    await page.getByRole("link", { name: "You", exact: true }).click();
    await page.getByRole("button", { name: "Set status" }).click();
    await page.getByRole("button", { name: "Busy" }).click();
    await page.getByLabel("Status message").fill("in a meeting");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    // Roles-v1 servers deliver presence changes as a fresh tree.
    const seen = () => quinn.chat.clients().find(c => c.client_id === phone.client_id) || {};
    await expect.poll(() => `${seen().status}|${seen().status_message}`).toBe("busy|in a meeting");
    await expect(page.locator(".me-status")).toHaveText("Busy · in a meeting");

    // Lou's status shows in the member list.
    await quinn.chat.setStatus("away", "lunch");
    await page.goto(`${server.webURL}/app/#/members/0`);
    await expect(page.getByRole("button", { name: `Open ${server.accounts.quinn.nickname}'s profile` }).first()).toContainText("Away · lunch");

    // A new display name.
    await page.getByRole("link", { name: "You", exact: true }).click();
    await page.getByRole("button", { name: "Change display name" }).click();
    await page.getByRole("textbox", { name: "Change display name" }).fill("Kai Phone");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect.poll(() => (seen().nickname || "").replace(/#M\d+$/, "")).toBe("Kai Phone");
    await expect(page.locator(".me-name")).toHaveText(/^Kai Phone(#M\d+)?$/);

    // A picture from the gallery, cropped to 256×256 and visible to others.
    const chooser = page.waitForEvent("filechooser");
    await page.getByRole("button", { name: "Change picture" }).last().click();
    await page.getByRole("button", { name: "Choose from gallery" }).click();
    await (await chooser).setFiles({ name: "me.png", mimeType: "image/png", buffer: await samplePNG(page) });
    await expect(page.getByRole("img", { name: "Picture preview" })).toBeVisible();
    await page.getByRole("dialog", { name: "Your picture" }).getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "Your picture" })).toHaveCount(0);
    const avatar = await quinn.conn.request(Msg.AvatarGet, { unique_id: pia.uniqueID }, Msg.AvatarData, { accept: r => r.unique_id === pia.uniqueID });
    expect(avatar.content_type).toBe("image/png");
    const png = Buffer.from(avatar.data_base64, "base64");
    expect([png.readUInt32BE(16), png.readUInt32BE(20)]).toEqual([256, 256]);
    await expect(page.locator(".me-card .avatar img")).toBeVisible();

    quinn.chat.close();
    pia.chat.close();
});

test("members, profile cards and pokes both ways", async ({ page }) => {
    page.on("pageerror", error => console.log("pageerror:", error.message));
    const remy = await member("remy");
    await linkPhone(page, remy, server.webURL, expect);
    const sol = await member("sol");
    const quinn = await member("quinn");
    const phone = await phoneClient(sol, remy.uniqueID);

    // The member list shows Noa; her card pokes her desktop.
    await page.goto(`${server.webURL}/app/#/members/0`);
    await expect(page.getByRole("heading", { name: "Members" })).toBeVisible({ timeout: 15000 });
    await page.getByRole("button", { name: `Open ${server.accounts.sol.nickname}'s profile` }).click();
    const card = page.getByRole("dialog", { name: `${server.accounts.sol.nickname}'s profile` });
    await expect(card).toContainText(sol.uniqueID);
    const poked = nextEvent(sol.conn, "poke", data => data.from_client_id === phone.client_id);
    await card.getByRole("button", { name: "Poke" }).click();
    await page.getByPlaceholder("Optional message").fill("lunch?");
    await page.getByRole("dialog", { name: `Poke ${server.accounts.sol.nickname}` }).getByRole("button", { name: "Poke" }).click();
    expect((await poked).message).toBe("lunch?");
    await card.getByRole("button", { name: "Close" }).last().click();

    // Lou pokes the phone: a popup with the message, logged in the activity center.
    await quinn.chat.poke(phone.client_id, "on my way");
    const popup = page.getByRole("dialog", { name: `${server.accounts.quinn.nickname} poked you` });
    await expect(popup).toContainText("on my way");
    const back = nextEvent(quinn.conn, "poke", data => data.from_client_id === phone.client_id);
    await popup.getByRole("button", { name: "Poke back" }).click();
    await back;
    await expect(popup).toHaveCount(0);
    await page.goto(`${server.webURL}/app/#/activity`);
    await page.getByRole("tab", { name: /Pokes/ }).click();
    await expect(page.getByRole("tabpanel")).toContainText("on my way");

    quinn.chat.close();
    sol.chat.close();
    remy.chat.close();
});

test("a blocked user's direct messages are hidden", async ({ page }) => {
    page.on("pageerror", error => console.log("pageerror:", error.message));
    const tess = await desktopSession(server.controlAddr, server.accounts.tess, { version: info.version });
    await registerDesktop(tess);
    await publishKeyRecord(tess);
    await linkPhone(page, tess, server.webURL, expect);
    const uma = await desktopSession(server.controlAddr, server.accounts.uma, { version: info.version });
    const umaDevice = await registerDesktop(uma);
    await publishKeyRecord(uma);
    const send = async (id, text) => {
        const devices = (await uma.conn.request(Msg.KeyRequest, { unique_id: tess.uniqueID, devices: true }, Msg.KeyResponse, { accept: r => r.unique_id === tess.uniqueID })).devices;
        const copies = [...devices, { device_id: umaDevice, x25519_public_key: btoa(String.fromCharCode(...uma.identity.xPublic)) }]
            .map(d => ({ device_id: d.device_id, text: sealDM(text, fromBase64(d.x25519_public_key), uma.identity.xSecret) }));
        await uma.conn.request(Msg.ChatSend, { to_unique_id: tess.uniqueID, enc: true, ack_requested: true, client_msg_id: id, copies }, Msg.ChatAccepted, { accept: r => r.client_msg_id === id });
    };
    await send("block-1", "first from Ivy");
    const row = page.getByRole("link", { name: new RegExp(server.accounts.uma.nickname) });
    await expect(row).toContainText("first from Ivy");

    // Block Ivy from her card in the member list.
    await page.goto(`${server.webURL}/app/#/members/0`);
    await page.getByRole("button", { name: `Open ${server.accounts.uma.nickname}'s profile` }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Block" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Close" }).last().click();
    await page.goto(`${server.webURL}/app/#/home`);
    await expect(page.getByRole("link", { name: new RegExp(server.accounts.uma.nickname) })).toHaveCount(0);

    // A new message neither shows nor counts: after unblocking, the
    // conversation is back with it, still one unread (the first).
    await send("block-2", "second from Ivy");
    await page.getByRole("link", { name: "You", exact: true }).click();
    await page.getByText("Blocked users (1)").click();
    await page.getByRole("button", { name: "Unblock" }).click();
    await page.getByRole("link", { name: "Home", exact: true }).click();
    const back = page.getByRole("link", { name: new RegExp(server.accounts.uma.nickname) });
    await expect(back).toContainText("second from Ivy");
    await expect(back.getByLabel("1 unread")).toBeVisible();
    tess.conn.close();
    uma.conn.close();
});

test("notification matrix and quiet hours persist; the activity center lists a mention; sounds play", async ({ page }) => {
    page.on("pageerror", error => console.log("pageerror:", error.message));
    // Count AudioContexts and started sources.
    await page.addInitScript(() => {
        window.__audio = { contexts: 0, starts: 0 };
        const Original = window.AudioContext;
        window.AudioContext = class extends Original {
            constructor(...args) { super(...args); window.__audio.contexts++; }
        };
        const start = AudioBufferSourceNode.prototype.start;
        AudioBufferSourceNode.prototype.start = function (...args) { window.__audio.starts++; return start.apply(this, args); };
    });
    const vic = await member("vic");
    await linkPhone(page, vic, server.webURL, expect);

    await page.getByRole("link", { name: "You", exact: true }).click();
    const pokeSound = page.getByRole("checkbox", { name: "Sound for Pokes" });
    await pokeSound.uncheck();
    await page.getByLabel("From", { exact: true }).fill("22:30");
    await page.getByLabel("To", { exact: true }).fill("06:45");
    await page.getByRole("combobox", { name: "Chats that may alert" }).selectOption("mentions");
    // The test sound goes through the engine (AudioContext + start()).
    await page.getByRole("button", { name: "Play a test sound" }).click();
    await expect.poll(() => page.evaluate(() => window.__audio.starts), { timeout: 10000 }).toBeGreaterThan(0);
    expect(await page.evaluate(() => window.__audio.contexts)).toBe(1);

    await page.reload();
    await page.getByRole("link", { name: "You", exact: true }).click({ timeout: 15000 });
    await expect(page.getByRole("checkbox", { name: "Sound for Pokes" })).not.toBeChecked();
    await expect(page.getByLabel("From", { exact: true })).toHaveValue("22:30");
    await expect(page.getByLabel("To", { exact: true })).toHaveValue("06:45");
    await expect(page.getByRole("combobox", { name: "Chats that may alert" })).toHaveValue("mentions");

    // A mention in the server chat appears under Mentions.
    const quinn = await member("quinn");
    await quinn.chat.send(0, `@${server.accounts.vic.nickname} the build is green`);
    await page.getByRole("link", { name: "Home", exact: true }).click();
    await page.getByRole("link", { name: /^Activity/ }).click();
    await page.getByRole("tab", { name: /Mentions/ }).click();
    await expect(page.getByRole("tabpanel")).toContainText("the build is green");
    await page.getByRole("button", { name: "Mark all as read" }).click();
    await expect(page.locator(".activity-entry.unread")).toHaveCount(0);

    quinn.chat.close();
    vic.chat.close();
});
