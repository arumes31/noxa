// clientinfo.js — right-click context menu on channel-tree users and the
// TS3-style Client Info dialog (live-refreshing).
import { getUserVolume, isUserMuted, setUserMuted, setUserVolume, setUserBlocked } from "./audio.js";
import { copyToClipboard } from "./clipboard.js";
import { pickIcon } from "./image-tools.js";
import { closeDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { t } from "./i18n.js";
import { roleChip } from "./role-presentation.js";
import { startPrivateCall } from "./private-calls.js";
import { updateLocalSettings } from "./settings-store.js";

const V = () => window.__noxa;

async function roleChannelDialog(kind, channelID) {
    const generation = V().state.serverGeneration;
    try {
        const { openRoleChannel } = await import("./channel-lifecycle-ui.js");
        if (generation === V().state.serverGeneration) openRoleChannel(kind, channelID);
    } catch { if (generation === V().state.serverGeneration) V().toast(t("roles.unavailable"), "warn"); }
}

let menuEl = null;

function closeMenu() {
    if (menuEl) {
        menuEl.remove();
        menuEl = null;
    }
}

function openContextMenu(x, y, client) {
    closeMenu();
    const tabID = V().state.activeTabID;
    const disconnectTarget = { clientID: client.client_id, channelID: client.channel_id };
    const { $ } = V();
    const P = window.__noxaPerms;
    // (306) multi-select: with several users selected, batch actions apply
    // to all of them.
    const sel = V().state.multiSelect;
    if (sel && sel.size > 1 && sel.has(client.client_id)) {
        return openBatchMenu(x, y, [...sel]);
    }
    const muted = isUserMuted(client.unique_id);
    const volPct = Math.round(getUserVolume(client.unique_id) * 100);
    // (169-171) moderation entries are pre-gated by the caller's resolved
    // powers; the server re-checks and errors still toast.
    const mod = [];
    if (client.client_id !== V().state.myClientID) {
        mod.push(`<a data-act="poke">Poke…</a>`);
        const isContact = (V().state.settings?.contacts || []).some((c) => c.unique_id === client.unique_id);
        mod.push(`<a data-act="contact">${isContact ? "✓ " : ""}Add to contacts</a>`);
        const isBlocked = (V().state.settings?.blocked_users || []).includes(client.unique_id);
        mod.push(`<a data-act="block">${isBlocked ? "✓ " : ""}Block (hide chat + mute)</a>`);
        if (disconnectTarget.channelID > 0) mod.push(`<a data-act="kick-ch">Kick from channel…</a>`);
        mod.push(`<a data-act="kick-srv">Kick from server…</a>`);
        mod.push(`<a data-act="ban">Ban…</a>`);
    }
    menuEl = document.createElement("div");
    menuEl.className = "ctx-menu";
    menuEl.innerHTML = `
        <a data-act="info">Client Info</a>
        <a data-act="pm">Send private message</a>
        <a data-act="copy">Copy unique ID</a>
        <div class="ctx-divider"></div>
        <a data-act="mute">${muted ? "✓ " : ""}Mute locally</a>
        <div class="ctx-volume">
            <span>Volume <span class="mono ctx-vol-pct">${volPct}%</span></span>
            <input type="range" min="0" max="200" value="${volPct}" />
        </div>
        ${mod.length ? `<div class="ctx-divider"></div>${mod.join("")}` : ""}`;
    menuEl.style.left = Math.min(x, window.innerWidth - 240) + "px";
    menuEl.style.top = Math.min(y, window.innerHeight - 260) + "px";
    menuEl.onclick = (e) => e.stopPropagation();

    if (client.unique_id && client.client_id !== V().state.myClientID) {
        const generation = V().state.serverGeneration;
        const call = document.createElement("button");
        call.type = "button"; call.className = "ctx-action"; call.textContent = t("call.start");
        call.onclick = () => {
            closeMenu();
            if (tabID === V().state.activeTabID && generation === V().state.serverGeneration) void startPrivateCall(client.unique_id);
        };
        menuEl.append(call);
    }
    if (client.channel_id > 0 && client.client_id !== V().state.myClientID) {
        const voice = document.createElement("button");
        voice.type = "button";
        voice.className = "ctx-action";
        voice.textContent = t("roles.voice.title");
        voice.onclick = async () => {
            const generation = V().state.serverGeneration;
            closeMenu();
            try {
                const { openVoiceModeration } = await import("./voice-moderation-ui.js");
                if (generation === V().state.serverGeneration) openVoiceModeration(client);
            } catch {
                if (generation === V().state.serverGeneration) V().toast(t("roles.voice.failed"));
            }
        };
        menuEl.append(voice);
    }
    menuEl.querySelector('[data-act="info"]').onclick = () => {
        closeMenu();
        openClientInfo(client);
    };
    menuEl.querySelector('[data-act="pm"]').onclick = () => {
        closeMenu();
        // wave 5b: open a PM tab (falls back to the plain direct-scope form).
        if (V().openPM) {
            V().openPM(client.unique_id, client.nickname);
            return;
        }
        $("chat-scope").value = "direct";
        V().setDirectTargetVisible(true);
        $("chat-target").value = client.unique_id;
        $("chat-text").focus();
    };
    menuEl.querySelector('[data-act="copy"]').onclick = () => {
        closeMenu();
        void copyToClipboard(client.unique_id, { success: "unique ID copied" });
    };
    menuEl.querySelector('[data-act="mute"]').onclick = async () => {
        closeMenu();
        try {
            await setUserMuted(client.unique_id, !muted);
            V().toast((isUserMuted(client.unique_id) ? "muted " : "unmuted ") + (client.nickname || client.unique_id) + " locally");
        } catch (error) { V().toast(String(error), "error"); }
    };
    // (170) kick with reason dialog; (171) ban with duration presets.
    const pokeAct = menuEl.querySelector('[data-act="poke"]');
    if (pokeAct) pokeAct.onclick = () => {
        closeMenu();
        window.__noxaSocial.openPoke(client);
    };
    const contactAct = menuEl.querySelector('[data-act="contact"]');
    if (contactAct) contactAct.onclick = async () => {
        closeMenu();
        const s = V().state.settings;
        if ((s.contacts || []).some((c) => c.unique_id === client.unique_id)) {
            V().toast("already a contact");
            return;
        }
        try {
            await updateLocalSettings(current => {
                if ((current.contacts || []).some(c => c.unique_id === client.unique_id)) return;
                current.contacts = [...(current.contacts || []), { unique_id: client.unique_id, label: "" }];
            });
            V().toast("contact added");
        } catch (error) { V().toast(String(error), "error"); }
    };
    const blockAct = menuEl.querySelector('[data-act="block"]');
    if (blockAct) blockAct.onclick = async () => {
        closeMenu();
        const s = V().state.settings;
        const blocked = (s.blocked_users || []).includes(client.unique_id);
        try {
            await setUserBlocked(client.unique_id, !blocked);
            V().toast(blocked ? "unblocked" : "blocked — chat hidden, voice muted locally");
        } catch (error) { V().toast(String(error), "error"); }
    };
    const kickAct = menuEl.querySelector('[data-act="kick-ch"]');
    if (kickAct) kickAct.onclick = () => {
        closeMenu();
        reasonDialog("Kick from channel", client, (reason) =>
            window.go.main.App.DisconnectMemberForTab(tabID, disconnectTarget.clientID, disconnectTarget.channelID, reason));
    };
    const kickSrvAct = menuEl.querySelector('[data-act="kick-srv"]');
    if (kickSrvAct) kickSrvAct.onclick = () => {
        closeMenu();
        reasonDialog("Kick from server", client, (reason) =>
            window.go.main.App.KickClientForTab(tabID, client.client_id, true, false, reason, 0));
    };
    const banAct = menuEl.querySelector('[data-act="ban"]');
    if (banAct) banAct.onclick = () => {
        closeMenu();
        banDialog(client, tabID);
    };
    const slider = menuEl.querySelector('.ctx-volume input');
    slider.oninput = () => {
        menuEl.querySelector(".ctx-vol-pct").textContent = slider.value + "%";
    };
    slider.onchange = async () => {
        try { await setUserVolume(client.unique_id, parseInt(slider.value, 10)); }
        catch (error) { V().toast(String(error), "error"); }
    };

    document.body.appendChild(menuEl);
}

// openBatchMenu is the multi-select context menu (306): actions apply to
// every selected user.
function openBatchMenu(x, y, clientIDs) {
    closeMenu();
    const tabID = V().state.activeTabID, generation = V().state.serverGeneration;
    const myID = V().state.myClientID;
    const others = clientIDs.filter((id) => id !== myID);
    const disconnectTargets = others.map(clientID => ({ clientID, channelID: V().state.clients.find(c => c.client_id === clientID)?.channel_id }));
    menuEl = document.createElement("div");
    menuEl.className = "ctx-menu";
    const entries = [`<a data-act="count" class="ctx-head">${clientIDs.length} selected</a>`];
    entries.push(`<a data-act="mute">Mute all locally</a>`);
    if (others.length) entries.push(`<a data-act="kick">Kick all from channel</a>`);
    menuEl.innerHTML = entries.join("");
    menuEl.style.left = Math.min(x, window.innerWidth - 240) + "px";
    menuEl.style.top = Math.min(y, window.innerHeight - 200) + "px";
    menuEl.onclick = (e) => e.stopPropagation();

    menuEl.querySelector('[data-act="mute"]').onclick = async () => {
        closeMenu();
        try {
            for (const id of clientIDs) {
                const c = V().state.clients.find((x) => x.client_id === id);
                if (c) await setUserMuted(c.unique_id, true);
            }
            V().toast("muted " + clientIDs.length + " users locally");
        } catch (error) { V().toast(String(error), "error"); }
    };
    const kick = menuEl.querySelector('[data-act="kick"]');
    if (kick) kick.onclick = async () => {
        closeMenu();
        for (const target of disconnectTargets) {
            if (generation !== V().state.serverGeneration) return;
            try {
                const err = await window.go.main.App.DisconnectMemberForTab(tabID, target.clientID, target.channelID || 0, "");
                if (generation !== V().state.serverGeneration) return;
                if (err) { V().toast(err, "warn"); return; }
            } catch (err) {
                if (generation === V().state.serverGeneration) V().toast("kick failed: " + err, "warn");
                return;
            }
        }
        V().toast("kick requests sent for " + others.length + " users");
    };
    document.body.appendChild(menuEl);
}

// inboundAudioByPublisher maps a publisher's client ID -> its inbound-rtp
// audio stat. The SFU gives every publisher its own MediaStream and sets the
// track ID to the publisher's client ID, so a stat is attributable via
// trackIdentifier (or the legacy "track" stat it references) (57).
function inboundAudioByPublisher(stats) {
    const byID = new Map();
    stats.forEach((r) => byID.set(r.id, r));
    const map = new Map();
    stats.forEach((r) => {
        if (r.type !== "inbound-rtp") return;
        if (r.kind !== "audio" && r.mediaType !== "audio") return;
        const tid = r.trackIdentifier || (r.trackId ? byID.get(r.trackId)?.trackIdentifier : "");
        if (tid) map.set(String(tid), r);
    });
    return map;
}

// refreshVoiceStats fills the Voice section from RTCPeerConnection.getStats()
// for the ONE client the dialog was opened for (57): the receive stream
// published by that client, or — when the dialog is our own — the outgoing
// stream as the server's RTCP receiver reports describe it.
async function refreshVoiceStats(overlay, client) {
    const { state } = V();
    const setVal = (f, text, cls) => {
        const el = overlay.querySelector(`[data-f="${f}"]`);
        if (el) {
            el.textContent = text;
            if (cls) el.className = "ci-val " + cls;
        }
    };
    const blank = (loss) => {
        setVal("loss", loss);
        setVal("jitter", "—");
        setVal("jbd", "—");
        setVal("conceal", "—");
        setVal("packets", "—");
        setVal("level", "—");
    };
    if (!state.pc) {
        blank("— (no voice)");
        return;
    }
    try {
        const stats = await state.pc.getStats();
        if (!isCurrentServerDialog(overlay)) return;
        if (client.client_id === state.myClientID) {
            refreshOwnVoiceStats(stats, setVal, blank);
            return;
        }
        const byPub = inboundAudioByPublisher(stats);
        let inbound = byPub.get(String(client.client_id));
        // fall back to the track registry: a reconnect can leave the dialog's
        // client ID stale while the attributed track is still live.
        if (!inbound && client.unique_id) {
            for (const [trackID, u] of state.trackUsers || []) {
                if (u.unique_id === client.unique_id && byPub.has(String(trackID))) {
                    inbound = byPub.get(String(trackID));
                    break;
                }
            }
        }
        if (!inbound) {
            blank("— no stream from this user");
            return;
        }
        const lost = inbound.packetsLost || 0;
        const recv = inbound.packetsReceived || 0;
        const total = lost + recv;
        setVal("loss", (total > 0 ? (lost / total * 100).toFixed(1) : "0.0") + " %");
        setVal("jitter", ((inbound.jitter || 0) * 1000).toFixed(1) + " ms");
        // (58) jitterBufferDelay/jitterBufferTargetDelay are CUMULATIVE sums of
        // seconds: the average delay is the sum over jitterBufferEmittedCount.
        const emitted = inbound.jitterBufferEmittedCount || 0;
        if (inbound.jitterBufferDelay != null && emitted > 0) {
            let txt = (inbound.jitterBufferDelay / emitted * 1000).toFixed(0) + " ms avg";
            if (inbound.jitterBufferTargetDelay != null) {
                txt += " (target " + (inbound.jitterBufferTargetDelay / emitted * 1000).toFixed(0) + " ms)";
            }
            setVal("jbd", txt);
        } else {
            setVal("jbd", "—");
        }
        // (58) concealment is what the loss actually cost: samples the jitter
        // buffer had to invent.
        const samples = inbound.totalSamplesReceived || 0;
        setVal("conceal", samples > 0
            ? ((inbound.concealedSamples || 0) / samples * 100).toFixed(2) + " % (" + (inbound.concealmentEvents || 0) + " events)"
            : "—");
        setVal("packets", recv + " recv / " + lost + " lost");
        setVal("level", inbound.audioLevel != null ? (inbound.audioLevel * 100).toFixed(0) + " %" : "—");
    } catch { /* stats unavailable */ }
}

// refreshOwnVoiceStats fills the Voice section for our own row: there is no
// inbound stream for oneself, so loss/jitter come from the server's receiver
// reports (remote-inbound-rtp) and the level from the local media source.
function refreshOwnVoiceStats(stats, setVal, blank) {
    let outbound = null, remoteIn = null, source = null;
    stats.forEach((r) => {
        const audio = r.kind === "audio" || r.mediaType === "audio";
        if (r.type === "outbound-rtp" && audio && !outbound) outbound = r;
        if (r.type === "remote-inbound-rtp" && audio && !remoteIn) remoteIn = r;
        if (r.type === "media-source" && audio && !source) source = r;
    });
    if (!outbound && !source) {
        blank("— not publishing");
        return;
    }
    if (remoteIn) {
        const lost = remoteIn.packetsLost || 0;
        const sent = outbound?.packetsSent || 0;
        setVal("loss", sent > 0 ? (lost / sent * 100).toFixed(1) + " % (reported by server)" : "—");
        setVal("jitter", ((remoteIn.jitter || 0) * 1000).toFixed(1) + " ms");
    } else {
        setVal("loss", "— (no receiver report yet)");
        setVal("jitter", "—");
    }
    setVal("jbd", "— (outgoing)");
    setVal("conceal", "— (outgoing)");
    setVal("packets", (outbound?.packetsSent || 0) + " sent");
    const level = source?.audioLevel ?? outbound?.audioLevel;
    setVal("level", level != null ? (level * 100).toFixed(0) + " %" : "—");
}

// --- Client Info dialog --------------------------------------------------------

// humanBytes renders a byte count in B/KiB/MiB (shared with files-ui).
export function humanBytes(n) {
    if (n < 1024) return n + " B";
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KiB";
    return (n / 1024 / 1024).toFixed(1) + " MiB";
}

function humanDuration(sec) {
    sec = Math.max(0, Math.floor(sec));
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    const s = sec % 60;
    if (h > 0) return `${h}h ${m}m ${s}s`;
    if (m > 0) return `${m}m ${s}s`;
    return `${s}s`;
}

// Re-resolve the displayed session on every snapshot so an open profile cannot
// retain revoked cosmetics or use another session of a newly hidden member.
export function renderClientRoles(row, client) {
    const { state } = V();
    const roles = [...(state.clients.find((c) => c.client_id === client.client_id)?.roles || [])].sort((a, b) => b.position - a.position);
    row.hidden = !roles.length;
    row.querySelector(".ci-label").textContent = t("roles.title");
    const chips = row.querySelector(".ci-val");
    chips.replaceChildren();
    chips.append(...roles.map(roleChip));
}

function openClientInfo(client) {
    const tabID = V().state.activeTabID;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg client-info">
            <div class="ci-title">
                <span role="heading" aria-level="3">Connection Info</span>
                <span class="ci-nick"></span>
            </div>
            <div class="ci-grid">
                <div class="ci-label">Client name</div><div class="ci-val" data-f="nick"></div>
                <div class="ci-label">Unique ID</div>
                <div class="ci-val">
                    <span class="mono" data-f="uid"></span>
                    <button class="ci-copy" title="copy">⧉</button>
                </div>
                <div class="ci-label">Connection time</div><div class="ci-val" data-f="conn"></div>
                <div class="ci-label">Idle time</div><div class="ci-val" data-f="idle"></div>
                <div class="ci-label">Ping</div><div class="ci-val" data-f="ping"></div>
                <div class="ci-label">Client address</div><div class="ci-val" data-f="addr"></div>
                <div class="ci-label">Transfer in</div><div class="ci-val" data-f="bin"></div>
                <div class="ci-label">Transfer out</div><div class="ci-val" data-f="bout"></div>
            </div>
            <div class="ci-voice-head">Voice</div>
            <div class="ci-grid">
                <div class="ci-label">Packet loss</div><div class="ci-val" data-f="loss"></div>
                <div class="ci-label">Jitter</div><div class="ci-val" data-f="jitter"></div>
                <div class="ci-label">Jitter buffer</div><div class="ci-val" data-f="jbd"></div>
                <div class="ci-label">Concealment</div><div class="ci-val" data-f="conceal"></div>
                <div class="ci-label">Packets</div><div class="ci-val" data-f="packets"></div>
                <div class="ci-label">Audio level</div><div class="ci-val" data-f="level"></div>
            </div>
            <div class="dlg-buttons"><button class="dlg-ok">Close</button></div>
        </div>`;

    overlay.querySelector(".ci-nick").textContent = client.nickname || client.unique_id;
    overlay.querySelector(".ci-copy").onclick = () => {
        void copyToClipboard(client.unique_id, { success: "unique ID copied", isCurrent: () => overlay.isConnected });
    };
    // (314) avatar full view on click; (325) click-to-copy chips.
    const card = document.querySelector(`#client-card .card-avatar img`);
    if (card) {
        card.style.cursor = "zoom-in";
        card.onclick = () => window.__noxaSocial.avatarLightbox(card.src);
    }
    // (315) local per-user note editor.
    const noteRow = document.createElement("div");
    noteRow.className = "ci-note";
    noteRow.innerHTML = `
        <div class="ci-label">Local note</div>
        <div class="ci-val"><input class="dlg-input ci-note-input" placeholder="only you see this…" /></div>`;
    const noteInput = noteRow.querySelector(".ci-note-input");
    noteInput.value = window.__noxaSocial.userNote(client.unique_id);
    noteInput.onchange = () => {
        window.__noxaSocial.saveUserNote(client.unique_id, noteInput.value.trim())
            .then(() => V().toast("note saved"))
            .catch(error => V().toast(String(error), "error"));
    };
    overlay.querySelector(".ci-grid").appendChild(noteRow);
    // Current roles from the recipient-filtered member snapshot.
    const gRow = document.createElement("div");
    gRow.className = "ci-note ci-member-roles";
    gRow.innerHTML = `<div class="ci-label"></div><div class="ci-val role-chips"></div>`;
    const renderRoles = () => renderClientRoles(gRow, client);
    renderRoles();
    overlay.querySelector(".ci-grid").appendChild(gRow);

    const setVal = (f, text, cls) => {
        const el = overlay.querySelector(`[data-f="${f}"]`);
        el.textContent = text;
        if (cls) el.className = "ci-val " + cls;
    };

    const refresh = async () => {
        if (!isCurrentServerDialog(overlay)) return;
        let info;
        try {
            info = await window.go.main.App.GetClientInfoForTab(tabID, client.client_id);
        } catch {
            return; // transient; try again next tick
        }
        if (!isCurrentServerDialog(overlay)) return;
        setVal("nick", info.nickname);
        overlay.querySelector('[data-f="uid"]').textContent = info.unique_id;
        setVal("conn", humanDuration(Date.now() / 1000 - info.connected_at));
        setVal("idle", humanDuration(info.idle_seconds));
        setVal("ping", info.ping_ms >= 0 ? info.ping_ms + " ms" : "unknown");
        if (info.ip) {
            setVal("addr", info.ip + ":" + info.port);
        } else {
            setVal("addr", "hidden — requires b_client_remoteaddress_view", "ci-muted");
        }
        setVal("bin", humanBytes(info.bytes_in));
        setVal("bout", humanBytes(info.bytes_out));

        // (57/58) Voice stats from getStats(), scoped to this dialog's client.
        refreshVoiceStats(overlay, client);
    };

    let refreshTimer = null;
    const stopRefresh = () => {
        if (refreshTimer) {
            clearInterval(refreshTimer);
            refreshTimer = null;
        }
    };
    const close = () => closeDialog(overlay);
    overlay.querySelector(".dlg-ok").onclick = close;
    overlay.onclick = (e) => { if (e.target === overlay) closeDialog(overlay, "cancel"); };

    const stopRoles = window.runtime.EventsOn("snapshot", () => { if (isCurrentServerDialog(overlay)) renderRoles(); });
    mountServerDialog(overlay, { onClose: () => { stopRefresh(); stopRoles?.(); } });
    refresh();
    refreshTimer = setInterval(refresh, 2000);
}

// --- moderation dialogs (170/171) -------------------------------------------

// reasonDialog prompts for a kick reason and invokes cb(reason).
function reasonDialog(title, client, cb) {
    const generation = V().state.serverGeneration;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg">
            <h3></h3>
            <div class="dlg-text kick-target"></div>
            <input type="text" class="dlg-input reason" placeholder="reason (optional)" />
            <div class="dlg-buttons">
                <button class="dlg-ok">Kick</button>
                <button class="dlg-cancel">Cancel</button>
            </div>
        </div>`;
    overlay.querySelector("h3").textContent = title;
    overlay.querySelector(".kick-target").textContent = client.nickname || client.unique_id;
    overlay.querySelector(".dlg-ok").onclick = async () => {
        if (!isCurrentServerDialog(overlay)) return;
        const reason = overlay.querySelector(".reason").value.trim();
        overlay.remove();
        try {
            const err = await cb(reason);
            if (err && generation === V().state.serverGeneration) V().toast(err, "warn");
        } catch (err) {
            if (generation === V().state.serverGeneration) V().toast("kick failed: " + err, "warn");
        }
    };
    overlay.querySelector(".dlg-cancel").onclick = () => overlay.remove();
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    mountServerDialog(overlay);
    overlay.querySelector(".reason").focus();
}

// banDialog prompts for reason + duration preset and bans the client (171).
const BAN_DURATIONS = [
    { label: "5 minutes", seconds: 300 },
    { label: "1 hour", seconds: 3600 },
    { label: "1 day", seconds: 86400 },
    { label: "permanent", seconds: 0 },
];

function banDialog(client, tabID) {
    const generation = V().state.serverGeneration;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg">
            <h3>Ban client</h3>
            <div class="dlg-text kick-target"></div>
            <input type="text" class="dlg-input reason" placeholder="reason (optional)" />
            <label class="dlg-label">Duration</label>
            <select class="dlg-input duration">
                ${BAN_DURATIONS.map((d, i) => `<option value="${i}">${d.label}</option>`).join("")}
            </select>
            <div class="dlg-buttons">
                <button class="dlg-ok danger-btn">Ban</button>
                <button class="dlg-cancel">Cancel</button>
            </div>
        </div>`;
    overlay.querySelector(".kick-target").textContent = client.nickname || client.unique_id;
    overlay.querySelector(".dlg-ok").onclick = async () => {
        if (!isCurrentServerDialog(overlay)) return;
        const reason = overlay.querySelector(".reason").value.trim();
        const dur = BAN_DURATIONS[parseInt(overlay.querySelector(".duration").value, 10) || 0];
        overlay.remove();
        try {
            const err = await window.go.main.App.KickClientForTab(tabID, client.client_id, true, true, reason, dur.seconds);
            if (generation !== V().state.serverGeneration) return;
            if (err) V().toast(err, "warn");
            else V().toast("ban requested for " + (client.nickname || client.unique_id) + " (" + dur.label + ")");
        } catch (err) {
            if (generation === V().state.serverGeneration) V().toast("ban failed: " + err, "warn");
        }
    };
    overlay.querySelector(".dlg-cancel").onclick = () => overlay.remove();
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    mountServerDialog(overlay);
    overlay.querySelector(".reason").focus();
}

// --- Channel context menu & edit dialog (24) ---------------------------------

function openChannelMenu(x, y, channel) {
    closeMenu();
    const { activeTabID: tabID, serverGeneration: generation } = V().state;
    const isCurrent = channel.ChannelID === V().state.myChannelID;
    const isSubscribed = !!window.__noxaChat?.isSubscribed?.(channel.ChannelID);
    // (320) recent channels for quick rejoin.
    const recent = (V().recentChannels ? V().recentChannels() : [])
        .map((id) => V().state.channels.find((c) => c.ChannelID === id))
        .filter(Boolean)
        .filter((c) => c.ChannelID !== channel.ChannelID)
        .map((c) => `<a data-act="recent-${c.ChannelID}">↩ # ${c.Name}</a>`);
    menuEl = document.createElement("div");
    menuEl.className = "ctx-menu";
    menuEl.innerHTML = `
        <a data-act="open-chat">Open chat tab</a>
        <a data-act="subscription" class="${isCurrent ? "disabled" : ""}">${isCurrent ? "✓ Joined (always subscribed)" : isSubscribed ? "✓ Unsubscribe" : "Subscribe"}</a>
        <div class="ctx-divider"></div>
        <a data-act="edit">Edit channel</a>
        <a data-act="notify">Notifications…</a>
        <a data-act="create-sub">Create sub-channel…</a>
        <a data-act="copy-id">Copy channel ID</a>
        <a data-act="copy-addr">Copy server address</a>
        ${recent.length ? `<div class="ctx-divider"></div>${recent.join("")}` : ""}
        <div class="ctx-divider"></div>
        <a data-act="delete" class="ctx-danger">Delete channel…</a>`;
    menuEl.style.left = Math.min(x, window.innerWidth - 240) + "px";
    menuEl.style.top = Math.min(y, window.innerHeight - 260) + "px";
    menuEl.onclick = (e) => e.stopPropagation();
    menuEl.querySelector('[data-act="open-chat"]').onclick = () => {
        closeMenu();
        window.__noxaChat?.openChannelTab?.(channel.ChannelID);
    };
    menuEl.querySelector('[data-act="subscription"]').onclick = () => {
        closeMenu();
        if (!isCurrent) window.__noxaChat?.setChannelSubscription?.(channel.ChannelID, !isSubscribed);
    };
    menuEl.querySelector('[data-act="edit"]').onclick = () => {
        closeMenu();
        openChannelEdit(channel);
    };
    const access = document.createElement("a");
    access.textContent = t("roles.accessTitle");
    access.onclick = async () => {
        closeMenu();
        const generation = V().state.serverGeneration;
        try {
            const { openChannelAccess } = await import("./channel-access-ui.js");
            if (generation === V().state.serverGeneration) openChannelAccess(channel.ChannelID);
        } catch { if (generation === V().state.serverGeneration) V().toast(t("roles.unavailable"), "warn"); }
    };
    menuEl.querySelector('[data-act="edit"]').after(access);
    const move = document.createElement("a");
    move.textContent = t("roles.channel.channel_move");
    move.onclick = () => { closeMenu(); roleChannelDialog("channel_move", channel.ChannelID); };
    access.after(move);
    menuEl.querySelector('[data-act="create-sub"]').onclick = () => {
        closeMenu();
        openChannelCreate(channel);
    };
    menuEl.querySelector('[data-act="notify"]').onclick = () => {
        closeMenu();
        openChannelNotify(channel);
    };
    menuEl.querySelector('[data-act="copy-id"]').onclick = () => {
        closeMenu();
        void copyToClipboard(String(channel.ChannelID), { success: "channel ID copied" });
    };
    menuEl.querySelector('[data-act="copy-addr"]').onclick = () => {
        closeMenu();
        void copyToClipboard(V().state.lastConnect?.addr || "", { success: "server address copied" });
    };
    for (const a of menuEl.querySelectorAll('[data-act^="recent-"]')) {
        a.onclick = async () => {
            closeMenu();
            if (generation !== V().state.serverGeneration) return;
            try {
                const err = await window.go.main.App.JoinChannelForTab(tabID, Number(a.dataset.act.slice(7)));
                if (err && generation === V().state.serverGeneration) V().toast(err, "warn");
            } catch (err) {
                if (generation === V().state.serverGeneration) V().toast(String(err), "warn");
            }
        };
    }
    menuEl.querySelector('[data-act="delete"]').onclick = () => {
        closeMenu();
        confirmChannelDelete(channel);
    };
    document.body.appendChild(menuEl);
}

// (320) recentChannels returns the current server's recent channel IDs.
function recentChannels() {
    if (!V().state.lastConnect || !V().state.settings?.recent_channels) return [];
    return V().state.settings.recent_channels[V().state.lastConnect.addr] || [];
}

// openChannelNotify edits the per-channel notification overrides
// (386/387/389): inherit/on/off per event class, mute, and watch threshold.
function openChannelNotify(channel) {
    const N = window.__noxaNotify;
    const ov = N.channelOverride(channel.ChannelID) || {};
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    const sel = (cls, val) => `
        <select class="dlg-input ${cls}">
            ${["inherit", "on", "off"].map((v) => `<option value="${v}" ${v === (val || "inherit") ? "selected" : ""}>${v}</option>`).join("")}
        </select>`;
    overlay.innerHTML = `
        <div class="dlg">
            <h3>Notifications: #${window.__noxaSocial.esc(channel.Name)}</h3>
            <label class="dlg-label">Messages</label>${sel("cn-messages", ov.messages)}
            <label class="dlg-label">Mentions & keywords</label>${sel("cn-mentions", ov.mentions)}
            <label class="dlg-label">Joins & leaves</label>${sel("cn-joins", ov.joins)}
            <label class="dlg-label"><input type="checkbox" class="cn-muted" ${ov.muted ? "checked" : ""} /> Mute channel entirely</label>
            <label class="dlg-label">Watch: toast when user count reaches (0 = off)</label>
            <input type="number" class="dlg-input cn-watch" min="0" value="${ov.watch_threshold || 0}" />
            <div class="dlg-buttons">
                <button class="dlg-ok">Save</button>
                <button class="dlg-cancel">Cancel</button>
            </div>
        </div>`;
    const q = (s) => overlay.querySelector(s);
    q(".dlg-ok").onclick = async () => {
        await N.saveChannelOverride(channel.ChannelID, {
            messages: q(".cn-messages").value === "inherit" ? "" : q(".cn-messages").value,
            mentions: q(".cn-mentions").value === "inherit" ? "" : q(".cn-mentions").value,
            joins: q(".cn-joins").value === "inherit" ? "" : q(".cn-joins").value,
            muted: q(".cn-muted").checked,
            watch_threshold: parseInt(q(".cn-watch").value, 10) || 0,
        });
        overlay.remove();
        V().toast("channel notifications saved");
    };
    q(".dlg-cancel").onclick = () => overlay.remove();
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    mountServerDialog(overlay);
}

function confirmChannelDelete(channel) {
    return roleChannelDialog("channel_delete", channel.ChannelID);
}

function openChannelCreate(parent) {
    return roleChannelDialog("channel_create", parent?.ChannelID || 0);
}

export { humanDuration, inboundAudioByPublisher };

export function openChannelEdit(channel) {
    return roleChannelDialog("channel_edit", channel.ChannelID);
}

// --- wiring -------------------------------------------------------------------

export function initClientInfo() {
    window.__noxa.openClientInfo = openClientInfo;
    const tree = document.getElementById("channel-tree");
    // (164) root channel creation via the sidebar + button.
    document.getElementById("channel-create-btn").onclick = (e) => {
        e.stopPropagation();
        openChannelCreate(null);
    };
    tree.addEventListener("contextmenu", (e) => {
        e.preventDefault();
        const row = e.target.closest(".client");
        if (row && row.dataset.clid) {
            const client = V().state.clients.find((c) => c.client_id === row.dataset.clid);
            if (client) openContextMenu(e.clientX, e.clientY, client);
            return;
        }
        const chRow = e.target.closest(".channel");
        if (!chRow || !chRow.dataset.chid) return;
        const channel = V().state.channels.find((c) => c.ChannelID === Number(chRow.dataset.chid));
        if (channel) openChannelMenu(e.clientX, e.clientY, channel);
    });
    document.addEventListener("click", closeMenu);
    window.runtime.EventsOn("tab_reset", closeMenu);
    document.addEventListener("keydown", (e) => {
        if (e.key === "Escape") closeMenu();
    });
}
