import { t } from "./i18n.js";
import { closeDialog } from "./modal.js";
import "./network-echo.css";

const V = () => window.__noxa;
const app = () => window.go.main.App;
let session = null;
const scope = () => {
    const s = V().state;
    return { tab: s.activeTabID, generation: s.serverGeneration, connection: s.sessionGeneration, client: s.myClientID };
};
const current = owner => Object.entries(owner.scope).every(([key, value]) => scope()[key] === value);
function element(tag, text) {
    const node = document.createElement(tag);
    if (text) node.textContent = text;
    return node;
}
function dispose(owner) {
    clearInterval(owner.timer); owner.banner?.remove();
    window.removeEventListener("noxa-language-changed", owner.translate);
    if (session === owner) session = null;
}
async function confirmedChannel(owner, destination) {
    for (let attempt = 0; attempt < 100; attempt++) {
        if (!current(owner)) return false;
        if (V().state.myChannelID === destination) return true;
        await new Promise(resolve => setTimeout(resolve, 50));
    }
    throw new Error(t("echo.timeout"));
}
function showSession(owner) {
    const banner = element("section"); banner.className = "network-echo-session";
    const status = element("span", t("echo.running")); status.setAttribute("role", "status");
    const back = element("button", t(owner.previous ? "echo.return" : "echo.leave")); back.type = "button";
    owner.translate = () => {
        status.textContent = owner.error ? t("echo.returnFailed", { error: owner.error }) : t(owner.pending ? "echo.pending" : "echo.running");
        back.textContent = t(owner.previous ? "echo.return" : "echo.leave");
    };
    window.addEventListener("noxa-language-changed", owner.translate);
    banner.append(status, back); owner.banner = banner;
    (document.getElementById("voice-bar") || document.body).prepend(banner);
    owner.timer = setInterval(() => {
        if (!current(owner) || (!owner.pending && V().state.myChannelID !== owner.channel)) dispose(owner);
    }, 200);
    back.onclick = async () => {
        if (!current(owner) || owner.pending) return;
        owner.pending = true; owner.error = ""; back.disabled = true; status.textContent = t("echo.pending");
        try {
            const error = await app().JoinChannelForTab(owner.scope.tab, owner.previous);
            if (!current(owner)) { dispose(owner); return; }
            if (error) throw new Error(error);
            if (await confirmedChannel(owner, owner.previous)) dispose(owner);
        } catch (error) {
            if (current(owner)) { owner.error = String(error.message || error); owner.translate(); }
        } finally { owner.pending = false; back.disabled = false; }
    };
}

export function createNetworkEchoTest() {
    const root = element("section"); root.className = "network-echo";
    root.append(element("h3", t("echo.title")), element("p", t("echo.help")));
    const status = element("p", t("echo.loading")); status.setAttribute("role", "status");
    const start = element("button", t("echo.start")); start.type = "button"; start.disabled = true;
    root.append(start, status);
    const origin = { scope: scope() };
    const ownsUI = () => root.isConnected && current(origin);
    const accessible = info => Number.isSafeInteger(info?.echo_channel_id) && info.echo_channel_id > 0 && V().state.channels.some(channel => channel.ChannelID === info.echo_channel_id);
    const available = info => accessible(info) && info.echo_private === true;
    const unavailable = info => t(accessible(info) && info.echo_private !== true ? "echo.updateRequired" : "echo.unavailable");
    const read = () => app().ServerInfoForTab(origin.scope.tab);
    if (origin.scope.client && origin.scope.tab) {
        void Promise.resolve().then(read).then(info => {
            if (!ownsUI()) return;
            start.disabled = !!session || !available(info); status.textContent = session ? t("echo.running") : start.disabled ? unavailable(info) : "";
        }).catch(() => { if (ownsUI()) status.textContent = t("echo.unavailable"); });
    } else status.textContent = t("echo.unavailable");
    start.onclick = async () => {
        if (!ownsUI() || session) return;
        if (document.querySelector(".private-call-panel")) { status.textContent = t("echo.callActive"); return; }
        const owner = { scope: origin.scope, previous: V().state.myChannelID || 0, pending: true };
        session = owner; start.disabled = true; status.textContent = t("echo.joining");
        try {
            const info = await read();
            if (!ownsUI() || session !== owner) { dispose(owner); return; }
            if (!available(info)) throw new Error(unavailable(info));
            owner.channel = info.echo_channel_id;
            if (owner.previous === owner.channel) owner.previous = 0;
            const error = await app().JoinChannelForTab(owner.scope.tab, owner.channel);
            if (!current(owner)) { dispose(owner); return; }
            if (error) throw new Error(error);
            if (!await confirmedChannel(owner, owner.channel)) { dispose(owner); return; }
            owner.pending = false; showSession(owner);
            const dialog = root.closest(".dlg-overlay");
            if (dialog) closeDialog(dialog);
            status.textContent = "";
        } catch (error) {
            if (ownsUI()) status.textContent = t("echo.failed", { error: String(error.message || error) });
            dispose(owner);
        } finally { start.disabled = !ownsUI() || !!session; }
    };
    return root;
}
