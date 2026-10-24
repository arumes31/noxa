import { installViewport } from "../src/ui/viewport.js";
installViewport();
import "../src/styles.css";
import { app } from "../src/app.js";
import { renderNewGroup } from "../src/ui/new-group.js";
import { renderNewChat } from "../src/ui/new-chat.js";
import { renderHome } from "../src/ui/home.js";
import { renderServers } from "../src/ui/servers.js";
import { renderVoice } from "../src/ui/voice.js";
import { renderChat } from "../src/ui/chat.js";
import { renderDM } from "../src/ui/dm.js";
import { renderGroup } from "../src/ui/group.js";
import { renderCall } from "../src/ui/call.js";
import { renderYou } from "../src/ui/you.js";
const view = new URL(location.href).searchParams.get("view") || "home";
const clients = [{ unique_id: "self", client_id: "self", nickname: "Daniel" }, { unique_id: "peer", client_id: "peer", nickname: "Avery" }];
const channels = [{ id: 3, name: "Lobby", depth: 0, home: true, chat: false, clients: [] },{ id: 1, name: "Public1", depth: 0, chat: true, clients }, { id: 2, name: "A very long channel name that should fit on a phone", depth: 1, chat: true, clients: [] }];
const messages = Array.from({ length: 60 }, (_, i) => ({ id: String(i), from: "Avery", fromNickname: "Avery", from_unique_id: "peer", fromUniqueID: "peer", text: "A sample message for the layout check.", body: "A sample message for the layout check.", sentAt: 1700000000 + i, created_at: 1700000000 + i }));
const session = Object.assign(new EventTarget(), {
    uniqueID: "self", clientID: "self", deviceID: "current", nickname: "Daniel", channels: () => channels, clients: () => clients,
    chatScopes: () => [1], current: new Map([[1, 1]]), messages: new Map([[1, messages]]), typing: new Map(), loadHistory: async () => false,
    conn: { request: async () => ({ devices: [
        { device_id: "master", name: "Master desktop", kind: "desktop", first_device: true, fingerprint: "aa:bb" },
        { device_id: "current", name: "This phone", kind: "web", current: true, fingerprint: "aa:cc" },
        { device_id: "other", name: "Other desktop", kind: "desktop", fingerprint: "aa:dd" },
    ], passkeys: [{ passkey_id: "key", name: "Phone passkey", created_at: 1700000000 }] }) },
});
if (view === "presence") {
    const mobile = { unique_id: "self", client_id: "phone", nickname: "Daniel mobile", on_mobile: true, channel_id: 0 };
    channels[0].clients = [mobile];
    session.clients = () => [...clients, mobile];
}
app.session = session; app.info = { server_name: "The Common Room" };
app.dm = Object.assign(new EventTarget(), { ready: Promise.resolve(), conversations: new Map([["peer", { nickname: "Avery", messages }]]), list: () => [{ peer: "peer", nickname: "Avery", messages }], devicesOf: async () => ({ fingerprint: "verified", devices: [{ deviceID: "current" }], trust: "trusted" }) });
app.groups = Object.assign(new EventTarget(), { list: new Map([["group", { id: "group", name: "Weekend crew", members: [{ unique_id: "self" }, { unique_id: "peer" }] }]]), messages: new Map([["group", messages]]), get: async () => {}, loadNewer: async () => {}, markRead: () => {} });
app.voice = Object.assign(new EventTarget(), { active: view === "voice-active", channelID: 1, speaking: new Set(["peer"]), camera: null, bytes: { sent: 0, received: 0 }, streams: async () => [], outputs: async () => [], watching: new Map(), video: new Map(), setTalking: () => {} });
app.calls = Object.assign(new EventTarget(), { active: view === "call", incoming: view === "incoming", peers: new Map(), camera: null, uid: "self", usage: async () => ({ sent: 0, received: 0 }), call: { id: "call", caller: "peer", participants: [{ unique_id: "self", state: "accepted" }, { unique_id: "peer", state: "accepted" }] } });
if (view.startsWith("signout")) {
    const request = session.conn.request;
    session.conn.request = async (type, payload) => {
        if (payload.action === "list") return request();
        document.documentElement.dataset.deviceRequest = JSON.stringify(payload);
        if (view === "signout-error") throw new Error("Server unavailable");
        return { action: payload.action };
    };
    for (const object of [session, app.dm, app.groups, app.voice, app.calls]) object.close = () => {};
}
const root = document.getElementById("app");
switch (view) {
    case "home": renderHome(root); break;
    case "new-group": renderNewGroup(root); break;
    case "new-chat": renderNewChat(root); break;
    case "servers": case "presence": renderServers(root); break;
    case "you": case "signout": case "signout-error": renderYou(root); break;
    case "voice": case "voice-active": renderVoice(root); break;
    case "chat": renderChat(root, 1); break;
    case "dm": renderDM(root, "peer"); break;
    case "group": renderGroup(root, "group"); break;
    case "incoming": case "call": renderCall(root, "call"); break;
    default: throw new Error("Unknown fixture view");
}
document.documentElement.dataset.ready = "true";
