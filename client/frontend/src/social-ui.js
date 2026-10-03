import { escapeHTML as escapeTranslation } from "./markdown.js";
import { t } from "./i18n.js";
// social-ui.js — wave-8b social/UX features: presence status picker +
// auto-away display (307-309), contacts & block list (316-318), hover cards
// (323/324), poke dialog (321/322), per-user notes + avatar lightbox + copy
// chips in Client Info (314/315/325), the server news pane (313), recent
// channels (320), tree filter + collapse/expand wiring (302/319), and
// multi-select batch actions (306).
import { humanBytes } from "./clientinfo.js";
import { setUserBlocked } from "./audio.js";
import { isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { setSafeImage } from "./safe-media.js";
import { capturePresenceScope, setPresence, presenceLabel } from "./presence.js";
import { updateLocalSettings } from "./settings-store.js";

const V = () => window.__noxa;
const App = () => window.go.main.App;


// ---------------------------------------------------------------------------
// Tree tools (302/319)
// ---------------------------------------------------------------------------

function initTreeTools() {
    const { state, $ } = V();
    $("tree-filter").oninput = (e) => {
        state.treeFilter = e.target.value.trim();
        V().renderTree();
    };
    $("tree-collapse").onclick = () => {
        for (const ch of state.channels) state.collapsedChannels.add(ch.ChannelID);
        V().renderTree();
    };
    $("tree-expand").onclick = () => {
        state.collapsedChannels.clear();
        V().renderTree();
    };
    // (302) double-click a channel row toggles its collapse; (349) in
    // windowed mode it toggles the explicit expansion instead.
    document.getElementById("channel-tree").addEventListener("dblclick", (e) => {
        const row = e.target.closest(".channel");
        if (!row) return;
        const id = Number(row.dataset.chid);
        V().setChannelExpanded(id, row.getAttribute("aria-expanded") !== "true");
    });
}

// ---------------------------------------------------------------------------
// Presence (307-309)
// ---------------------------------------------------------------------------

// openStatusPicker shows the status dialog (Self menu).
function openStatusPicker() {
    const { state } = V();
    const scope = capturePresenceScope();
    const canSetInvisible = state.canSetInvisible;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg">
            <h3>${escapeTranslation(t("desktop.my.status"))}</h3>
            <select class="dlg-input st-sel">
                <option value="online">${escapeTranslation(t("desktop.online"))}</option>
                <option value="away">${escapeTranslation(t("desktop.away"))}</option>
                <option value="busy">${escapeTranslation(t("desktop.busy"))}</option>
                ${canSetInvisible ? `<option value="invisible">${escapeTranslation(t("runtime.invisibleAdmin"))}</option>` : ""}
            </select>
            <label class="dlg-label">${escapeTranslation(t("desktop.status.message.optional"))}</label>
            <input class="dlg-input st-msg" maxlength="200" placeholder="${escapeTranslation(t("desktop.e.g.in.a.meeting"))}" />
            <div class="dlg-buttons">
                <button class="dlg-ok">${escapeTranslation(t("desktop.set"))}</button>
                <button class="dlg-cancel">${escapeTranslation(t("desktop.cancel"))}</button>
            </div>
        </div>`;
    const sel = overlay.querySelector(".st-sel");
    sel.value = state.myStatus || "online";
    overlay.querySelector(".dlg-ok").onclick = async () => {
        if (!isCurrentServerDialog(overlay)) return;
        const status = sel.value;
        const msg = overlay.querySelector(".st-msg").value.trim();
        overlay.remove();
        if (await setPresence(status, msg, scope)) {
            V().sysMsg(t("desktop.status") + presenceLabel(state.myStatus) + (msg ? " — " + msg : ""));
        }
    };
    overlay.querySelector(".dlg-cancel").onclick = () => overlay.remove();
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    mountServerDialog(overlay);
}

// ---------------------------------------------------------------------------
// Contacts + block list (316-318)
// ---------------------------------------------------------------------------

function openContacts() {
    const { state } = V();
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    const render = () => {
        const s = state.settings || {};
        const contacts = s.contacts || [];
        const list = overlay.querySelector(".ct-list");
        list.innerHTML = contacts.length ? "" : `<div class="empty-state">${escapeTranslation(t("desktop.no.contacts.yet"))}</div>`;
        for (const c of contacts) {
            const online = state.clients.find((x) => x.unique_id === c.unique_id);
            const row = document.createElement("div");
            row.className = "ct-row";
            row.innerHTML = `
                <span class="ct-dot ${online ? "on" : ""}" title="${escapeTranslation(online ? presenceLabel("") : t("runtime.offline"))}"></span>
                <span class="ct-name"></span>
                <span class="ct-hist mono" title="${escapeTranslation(t("desktop.nickname.history"))}"></span>
                <button class="ct-block" title="${escapeTranslation(t("desktop.block.unblock"))}"></button>
                <button class="ct-del" title="${escapeTranslation(t("desktop.remove"))}">✕</button>`;
            row.querySelector(".ct-name").textContent = (c.label || online?.nickname || c.unique_id.slice(0, 12)) + (online ? " — " + online.nickname : "");
            row.querySelector(".ct-hist").textContent = (c.nick_history || []).slice(-3).join(", ");
            const blocked = (s.blocked_users || []).includes(c.unique_id);
            // (383) buddy alert toggle.
            const watch = document.createElement("button");
            watch.textContent = c.notify_online ? "🔔" : "🔕";
            watch.title = c.notify_online ? t("desktop.notify.when.online.on") : t("desktop.notify.when.online.off");
            watch.onclick = async () => {
                try {
                    await updateLocalSettings(current => {
                        const contact = (current.contacts || []).find(x => x.unique_id === c.unique_id);
                        if (contact) contact.notify_online = !contact.notify_online;
                    });
                } catch (error) { V().toast(String(error), "error"); return; }
                if (!isCurrentServerDialog(overlay)) return;
                render();
            };
            row.insertBefore(watch, row.querySelector(".ct-block"));
            const blockBtn = row.querySelector(".ct-block");
            blockBtn.textContent = blocked ? "🚫" : "🔇";
            blockBtn.title = blocked ? t("desktop.unblock") : t("desktop.block.hide.chat.mute.voice");
            blockBtn.onclick = async () => {
                try { await setUserBlocked(c.unique_id, !blocked); }
                catch (error) { V().toast(String(error), "error"); return; }
                if (!isCurrentServerDialog(overlay)) return;
                render();
            };
            row.querySelector(".ct-del").onclick = async () => {
                try {
                    await updateLocalSettings(current => {
                        current.contacts = (current.contacts || []).filter(x => x.unique_id !== c.unique_id);
                    });
                } catch (error) { V().toast(String(error), "error"); return; }
                if (!isCurrentServerDialog(overlay)) return;
                render();
            };
            list.appendChild(row);
        }
    };
    overlay.innerHTML = `
        <div class="dlg dlg-wide">
            <h3>${escapeTranslation(t("desktop.contacts"))}</h3>
            <div class="ct-add">
                <input class="dlg-input ct-uid" placeholder="${escapeTranslation(t("desktop.unique.id.0d7a37"))}" />
                <input class="dlg-input ct-label" placeholder="${escapeTranslation(t("desktop.label.optional"))}" />
                <button class="ct-add-btn">${escapeTranslation(t("desktop.add"))}</button>
            </div>
            <div class="ct-list"></div>
            <div class="dlg-buttons"><button class="dlg-ok">${escapeTranslation(t("desktop.close"))}</button></div>
        </div>`;
    overlay.querySelector(".ct-add-btn").onclick = async () => {
        const s = state.settings || {};
        const uid = overlay.querySelector(".ct-uid").value.trim();
        if (!uid) return;
        if ((s.contacts || []).some((c) => c.unique_id === uid)) {
            V().toast(t("desktop.contact.already.exists"), "warn");
            return;
        }
        const label = overlay.querySelector(".ct-label").value.trim();
        try {
            await updateLocalSettings(current => {
                if ((current.contacts || []).some(c => c.unique_id === uid)) return;
                current.contacts = [...(current.contacts || []), { unique_id: uid, label }];
            });
        } catch (error) { V().toast(String(error), "error"); return; }
        if (!isCurrentServerDialog(overlay)) return;
        overlay.querySelector(".ct-uid").value = "";
        overlay.querySelector(".ct-label").value = "";
        render();
    };
    overlay.querySelector(".dlg-ok").onclick = () => overlay.remove();
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    mountServerDialog(overlay);
    render();
}

// ---------------------------------------------------------------------------
// Hover cards (323/324)
// ---------------------------------------------------------------------------

let hoverCard = null;
let hoverTimer = null;

function showHoverCard(x, y, html, avatarURL = null) {
    hideHoverCard();
    hoverCard = document.createElement("div");
    hoverCard.className = "hover-card";
    hoverCard.innerHTML = html;
    const avatarSlot = hoverCard.querySelector(".hc-avatar-slot");
    if (avatarSlot) {
        if (!setSafeImage(avatarSlot, avatarURL, { className: "hc-av" })) avatarSlot.remove();
    }
    hoverCard.style.left = Math.min(x, window.innerWidth - 260) + "px";
    hoverCard.style.top = Math.min(y, window.innerHeight - 160) + "px";
    document.body.appendChild(hoverCard);
}

function hideHoverCard() {
    if (hoverTimer) {
        clearTimeout(hoverTimer);
        hoverTimer = null;
    }
    if (hoverCard) {
        hoverCard.remove();
        hoverCard = null;
    }
}

function esc(s) {
    return String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

function initHoverCards() {
    const tree = document.getElementById("channel-tree");
    tree.addEventListener("mouseover", (e) => {
        const clientRow = e.target.closest(".client");
        const chRow = e.target.closest(".channel");
        hideHoverCard();
        if (clientRow) {
            const c = V().state.clients.find((x) => x.client_id === clientRow.dataset.clid);
            if (!c) return;
            hoverTimer = setTimeout(() => {
                const g = window.__noxaPerms.primaryGroup(c.unique_id);
                const av = V().state.avatars.get(c.unique_id);
                showHoverCard(e.clientX + 12, e.clientY + 12, `
                    <div class="hc-head">
                        <span class="hc-avatar-slot"></span>
                        <b>${esc(c.nickname || c.unique_id)}</b>
                    </div>
                    <div class="hc-line mono">${esc((c.unique_id || "").slice(0, 20))}…</div>
                    ${g ? `<div class="hc-line">${esc(t("runtime.groupValue", { name: g.name }))}</div>` : ""}
                    ${c.status ? `<div class="hc-line">${esc(t("runtime.statusValue", { status: presenceLabel(c.status) }))}${c.status_message ? " — " + esc(c.status_message) : ""}</div>` : ""}
                    <div class="hc-line">${esc(t("runtime.channelValue", { name: V().state.channels.find((x) => x.ChannelID === c.channel_id)?.Name || t("runtime.none") }))}</div>`, av);
            }, 500);
        } else if (chRow) {
            const ch = V().state.channels.find((x) => x.ChannelID === Number(chRow.dataset.chid));
            if (!ch) return;
            hoverTimer = setTimeout(() => {
                const quality = ch.OpusBitrate ? Math.round(ch.OpusBitrate / 1000) + " kbps" : t("runtime.defaultBitrate");
                showHoverCard(e.clientX + 12, e.clientY + 12, `
                    <div class="hc-head"><b># ${esc(ch.Name)}</b></div>
                    ${ch.Topic ? `<div class="hc-line">${esc(ch.Topic)}</div>` : ""}
                    <div class="hc-line">${esc(t("runtime.clientsValue", { count: ch.ClientCount }))}${ch.MaxClients ? "/" + ch.MaxClients : ""}${ch.HasPassword ? " · 🔒" : ""}</div>
                    <div class="hc-line">${esc(t("runtime.codecValue", { codec: "Opus " + quality }))}${ch.OpusStereo ? " stereo" : ""}${ch.OpusFEC ? " +FEC" : ""}</div>`);
            }, 500);
        }
    });
    tree.addEventListener("mouseout", hideHoverCard);
    tree.addEventListener("mousedown", hideHoverCard);
}

// ---------------------------------------------------------------------------
// Poke (321/322)
// ---------------------------------------------------------------------------

function openPoke(client) {
    const tabID = V().state.activeTabID, generation = V().state.serverGeneration;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg">
            <h3>${escapeTranslation(t("desktop.poke"))}</h3>
            <div class="dlg-text poke-target"></div>
            <input class="dlg-input poke-msg" maxlength="200" placeholder="${escapeTranslation(t("desktop.message.optional"))}" />
            <div class="dlg-buttons">
                <button class="dlg-ok">${escapeTranslation(t("desktop.poke"))}</button>
                <button class="dlg-cancel">${escapeTranslation(t("desktop.cancel"))}</button>
            </div>
        </div>`;
    overlay.querySelector(".poke-target").textContent = client.nickname || client.unique_id;
    overlay.querySelector(".dlg-ok").onclick = async () => {
        if (!isCurrentServerDialog(overlay)) return;
        const msg = overlay.querySelector(".poke-msg").value.trim();
        overlay.remove();
        try {
            const err = await App().PokeForTab(tabID, client.client_id, msg);
            if (err && generation === V().state.serverGeneration) V().toast(t("desktop.poke.failed") + err, "warn");
        } catch (err) {
            if (generation === V().state.serverGeneration) V().toast(t("desktop.poke.failed") + err, "warn");
        }
    };
    overlay.querySelector(".dlg-cancel").onclick = () => overlay.remove();
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    mountServerDialog(overlay);
    overlay.querySelector(".poke-msg").focus();
}

// ---------------------------------------------------------------------------
// News pane (313)
// ---------------------------------------------------------------------------

async function refreshNews() {
    const area = document.getElementById("news-area");
    const generation = V().state.serverGeneration;
    const tabID = V().state.activeTabID;
    if (!V().state.myClientID) {
        area.innerHTML = `<div class="empty-state">${escapeTranslation(t("desktop.offline"))}</div>`;
        return;
    }
    try {
        const [info, motd] = await Promise.all([App().ServerInfoForTab(tabID), App().MOTDForTab(tabID)]);
        if (generation !== V().state.serverGeneration) return;
        const up = Math.floor(info.uptime_seconds / 60);
        const serverName = document.getElementById("server-name");
        if (serverName && info.name) {
            serverName.textContent = info.name;
            serverName.title = `${info.name} — ${t("desktop.server.information")}`;
        }
        area.innerHTML = `
            <div class="news-line"><b>${esc(info.name)}</b></div>
            <div class="news-line mono">${esc(info.version)} · ${esc(t("runtime.serverSummary", { clients: info.clients_online, max: info.max_clients, channels: info.channels_online, minutes: up }))}</div>
            ${motd ? `<div class="news-motd">${esc(motd)}</div>` : ""}`;
    } catch {
        if (generation !== V().state.serverGeneration) return;
        area.innerHTML = `<div class="empty-state">${escapeTranslation(t("desktop.server.info.unavailable"))}</div>`;
    }
}

// ---------------------------------------------------------------------------
// Client Info additions (314/315/325)
// ---------------------------------------------------------------------------

// avatarLightbox shows the full avatar (314).
function avatarLightbox(dataUrl) {
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `<div class="dlg avatar-full"></div>`;
    if (!setSafeImage(overlay.querySelector(".avatar-full"), dataUrl)) return;
    overlay.onclick = () => overlay.remove();
    mountServerDialog(overlay);
}

function resetServerView() {
    hideHoverCard();
    const serverName = document.getElementById("server-name");
    if (serverName) serverName.textContent = t("desktop.server");
    const area = document.getElementById("news-area");
    if (area) area.innerHTML = `<div class="empty-state">${escapeTranslation(t("desktop.loading.server"))}</div>`;
}

// userNote loads/saves the local per-user note (315).
function userNote(uid) {
    return (V().state.settings?.user_notes || {})[uid] || "";
}

async function saveUserNote(uid, note) {
    await updateLocalSettings(s => {
        s.user_notes = s.user_notes || {};
        if (note) s.user_notes[uid] = note;
        else delete s.user_notes[uid];
    });
}

// ---------------------------------------------------------------------------
// init + exports
// ---------------------------------------------------------------------------

export function initSocialUI() {
    initTreeTools();
    initHoverCards();
    window.__noxaSocial = {
        openStatusPicker, openContacts, openPoke, refreshNews, resetServerView,
        avatarLightbox, userNote, saveUserNote, esc,
    };
}

void humanBytes; // shared formatting helper kept for future rows
