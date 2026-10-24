import { expect, test } from "@playwright/test";

import { fromBase64, openDM, sealDM, toBase64 } from "../src/core/crypto.js";
import { Msg } from "../src/core/frame.js";
import { desktopSession } from "./harness.mjs";
import { linkPhone, publishKeyRecord, registerDesktop } from "./helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
});

const group = (desktop, request) => desktop.conn.request(170, request, 171, { accept: r => r.action === request.action });

function directory(desktop, uid) {
    return desktop.conn.request(Msg.KeyRequest, { unique_id: uid, devices: true }, Msg.KeyResponse, { accept: r => r.unique_id === uid });
}

test("a phone reads and writes an end-to-end encrypted group", async ({ page }) => {
    page.on("pageerror", error => console.log("pageerror:", error.message));
    const mia = await desktopSession(server.controlAddr, server.accounts.mia, { version: info.version });
    await registerDesktop(mia);
    await publishKeyRecord(mia);
    await linkPhone(page, mia, server.webURL, expect);
    const noa = await desktopSession(server.controlAddr, server.accounts.noa, { version: info.version });
    await registerDesktop(noa);
    await publishKeyRecord(noa);

    // Noa creates a group and invites Mia, who joins on her phone.
    let crew = (await group(noa, { action: "create", name: "Crew" })).conversations[0];
    await group(noa, { action: "invite", id: crew.id, revision: crew.revision, target: mia.uniqueID });
    await page.getByRole("button", { name: "Join" }).click({ timeout: 15000 });
    crew = (await group(noa, { action: "get", id: crew.id })).conversations[0];
    expect(crew.members.filter(m => !m.pending).length).toBe(2);

    // Noa's desktop writes: account envelopes plus one for Mia's phone.
    const miaDevices = (await directory(noa, mia.uniqueID)).devices;
    const phone = miaDevices.find(d => d.kind === "web");
    const plain = to => JSON.stringify({ conversation_id: crew.id, epoch: crew.epoch, reference: "ref-1", from: noa.uniqueID, to, text: "welcome to the crew" });
    const sent = await group(noa, { action: "send", id: crew.id, revision: crew.revision, message: {
        epoch: crew.epoch, reference: "ref-1",
        envelopes: {
            [noa.uniqueID]: sealDM(plain(noa.uniqueID), noa.identity.xPublic, noa.identity.xSecret),
            [mia.uniqueID]: sealDM(plain(mia.uniqueID), mia.identity.xPublic, noa.identity.xSecret),
        },
        device_envelopes: { [`${mia.uniqueID}/${phone.device_id}`]: sealDM(plain(mia.uniqueID), fromBase64(phone.x25519_public_key), noa.identity.xSecret) },
    } });
    expect(sent.message_id).toBeGreaterThan(0);

    const row = page.getByRole("link", { name: /Crew/ });
    await expect(row).toBeVisible({ timeout: 15000 });
    await row.click();
    await expect(page.getByRole("log")).toContainText("welcome to the crew", { timeout: 15000 });

    // The phone replies; Noa's desktop opens it with the phone's device key.
    const changed = new Promise(resolve => noa.conn.on(Msg.Event, event => { if (event.type === "conversation_changed") resolve(event.data); }));
    const composer = page.getByRole("textbox", { name: /Message/ });
    await composer.fill("thanks, from my phone");
    await composer.press("Enter");
    await changed;
    const history = await group(noa, { action: "history", id: crew.id });
    const reply = history.messages.find(m => m.from_unique_id === mia.uniqueID);
    expect(reply.device_envelope).toBeFalsy();
    expect(reply.sender_device_id).toBe(phone.device_id);
    const opened = JSON.parse(openDM(reply.body, fromBase64(reply.sender_device_key), noa.identity.xSecret));
    expect(opened.text).toBe("thanks, from my phone");
    expect(opened.to).toBe(noa.uniqueID);
    expect(toBase64(fromBase64(reply.sender_device_key))).toBe(phone.x25519_public_key);

    // The phone rereads its own message after a reload.
    await page.reload();
    await expect(page.getByRole("log")).toContainText("thanks, from my phone", { timeout: 15000 });
    mia.conn.close();
    noa.conn.close();
});
