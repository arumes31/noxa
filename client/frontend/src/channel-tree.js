// channel-tree.js — channel/member rendering and tree interaction.
import { filterConversations, privateGroupViewToken } from "./conversations.js";
import { isUserMuted } from "./audio.js";
import * as chatUI from "./chat-ui.js";
import { isClientMicrophoneMuted, publishAudioState } from "./audio-state.js";
import { roleChip } from "./role-presentation.js";
import { setSafeImage } from "./safe-media.js";
import { t } from "./i18n.js";
import { presenceLabel } from "./presence.js";
import { icon } from "./icons.js";
import { renderWorkspace } from "./workspace-ui.js";
import { captureScope, scopeIsCurrent } from "./scoped-actions.js";

export function createChannelTree({ P, $, syncTrayVoice, state, toast, fetchAvatar, renderClientCard, setDetailsOpen, renderDirectTargets }) {
    const readEchoScope = () => ({ tabID: state.activeTabID, generation: state.serverGeneration, session: state.sessionGeneration });
    let echoInfo = null;

    async function refreshEchoChannel() {
        const request = { scope: captureScope(readEchoScope), channelID: 0 };
        echoInfo = request;
        if (!request.scope.tabID) return;
        try {
            const info = await window.go.main.App.ServerInfoForTab(request.scope.tabID);
            if (echoInfo !== request || !scopeIsCurrent(request.scope, readEchoScope)) return;
            if (Number.isSafeInteger(info?.echo_channel_id) && info.echo_channel_id > 0) {
                request.channelID = info.echo_channel_id;
                renderTree();
            }
        } catch { /* Optional metadata: keep the regular channel icon when unavailable. */ }
    }

    function recomputeClientCounts() {
        const counts = new Map();
        for (const c of state.clients) counts.set(c.channel_id, (counts.get(c.channel_id) || 0) + 1);
        for (const ch of state.channels) ch.ClientCount = counts.get(ch.ChannelID) || 0;
    }

    // (302) expandMyBranch un-collapses my channel and its ancestors: a move into
    // a branch the user collapsed earlier would otherwise hide the channel they
    // are actually in.
    function expandMyBranch() {
        let id = state.myChannelID;
        let guard = 0;
        while (id && guard++ < 1000) {
            state.collapsedChannels.delete(id);
            id = state.channels.find((c) => c.ChannelID === id)?.ParentID || 0;
        }
    }

    function renderTree() {
        recomputeClientCounts();
        const root = $("channel-tree");
        const focusState = captureTreeFocus(root);
        root.innerHTML = "";
        // (179) Hoisted server groups render as their own sections above the
        // channels; (177) section headers carry the group icon; (178) nickname
        // colors come from the first applicable group (clientRow).
        for (const h of P().hoistedGroups()) {
            const sec = document.createElement("div");
            sec.className = "hoist-section";
            const head = document.createElement("div");
            head.className = "hoist-head";
            head.innerHTML = `<span class="hoist-icon"></span><span class="hoist-name"></span>`;
            head.querySelector(".hoist-name").textContent = h.group.name;
            if (h.group.color) head.style.color = h.group.color;
            if (h.group.icon) head.querySelector(".hoist-icon").textContent = h.group.icon;
            sec.appendChild(head);
            for (const c of h.members) sec.appendChild(clientRow(c));
            root.appendChild(sec);
        }
        if (state.channels.length === 0) {
            const empty = document.createElement("div");
            empty.className = "empty-state";
            empty.textContent = t("runtime.noChannels");
            root.appendChild(empty);
        }
        const byParent = new Map();
        for (const ch of state.channels) {
            const key = ch.ParentID || 0;
            if (!byParent.has(key)) byParent.set(key, []);
            byParent.get(key).push(ch);
        }
        // (163) the manual sort index decides sibling order; sort is stable, so
        // equal indices keep the order the snapshot delivered them in.
        for (const list of byParent.values()) {
            list.sort((a, b) => (a.OrderIndex || 0) - (b.OrderIndex || 0));
        }
        // (319) live tree filter: matching channels/users stay; a channel stays
        // when it or a descendant or one of its users matches.
        if (state.treeFilter) {
            const q = state.treeFilter.toLowerCase();
            const keep = new Set();
            const matchCh = (ch) => {
                let ok = ch.Name.toLowerCase().includes(q) ||
                    state.clients.some((c) => c.channel_id === ch.ChannelID &&
                        (c.nickname || c.unique_id || "").toLowerCase().includes(q));
                for (const child of byParent.get(ch.ChannelID) || []) ok = matchCh(child) || ok;
                if (ok) keep.add(ch.ChannelID);
                return ok;
            };
            for (const ch of byParent.get(0) || []) matchCh(ch);
            for (const [p, list] of byParent) {
                byParent.set(p, list.filter((ch) => keep.has(ch.ChannelID)));
            }
        }
        for (const ch of byParent.get(0) || []) renderChannel(root, ch, byParent, 0);
        if (state.treeFilter && !root.querySelector(".channel, .client")) {
            const empty = document.createElement("div");
            empty.className = "empty-state";
            empty.setAttribute("role", "status");
            empty.textContent = t("runtime.noMatch");
            root.appendChild(empty);
        }
        renderDirectTargets();
        filterConversations();
        renderClientCard();
        chatUI.refreshHeader(); // (111) topic/title follows tree + channel updates
        renderWorkspace();
        publishAudioState();
        syncTrayVoice();
        restoreTreeFocus(root, focusState);
    }

    function captureTreeFocus(root) {
        const active = document.activeElement;
        if (!(active instanceof HTMLElement) || !root.contains(active)) return null;
        const kind = active.classList.contains("channel") ? "channel" : active.classList.contains("client") ? "client" : "";
        if (!kind) return null;
        const dataKey = kind === "channel" ? "chid" : "clid";
        const key = active.dataset[dataKey];
        const matches = [...root.querySelectorAll(`.${kind}`)].filter((row) => row.dataset[dataKey] === key);
        return { kind, key, occurrence: Math.max(0, matches.indexOf(active)) };
    }

    function restoreTreeFocus(root, focusState) {
        if (!focusState) return;
        const dataKey = focusState.kind === "channel" ? "chid" : "clid";
        const matches = [...root.querySelectorAll(`.${focusState.kind}`)]
            .filter((row) => row.dataset[dataKey] === focusState.key);
        const target = matches[focusState.occurrence] || matches.at(-1);
        target?.focus({ preventScroll: true });
    }

    function setChannelExpanded(channelID, expanded) {
        if (expanded) {
            state.collapsedChannels.delete(channelID);
            state.expandedVirtual.add(channelID);
        } else {
            state.collapsedChannels.add(channelID);
            state.expandedVirtual.delete(channelID);
        }
        renderTree();
    }

    function renderChannel(parentEl, ch, byParent, depth) {
        const tabID = state.activeTabID, generation = state.serverGeneration;
        const node = document.createElement("div");
        node.className = "channel-node";
        node.style.setProperty("--depth", depth);
        const el = document.createElement("div");
        el.className = "channel" + (ch.ChannelID === state.myChannelID ? " mine" : "");
        el.dataset.chid = ch.ChannelID;
        el.tabIndex = 0; // (298) keyboard navigation
        el.setAttribute("role", "treeitem"); // (343)
        const restricted = ch.Access?.restricted === true;
        const cannotJoin = ch.Access?.can_connect === false;
        const isEcho = echoInfo?.channelID > 0 && echoInfo.channelID === ch.ChannelID && scopeIsCurrent(echoInfo.scope, readEchoScope);
        const accessLabels = [];
        if (isEcho) accessLabels.push(t("echo.channelHint"));
        if (restricted) accessLabels.push(t("channel.access.private"));
        if (ch.HasPassword) accessLabels.push(t("channel.access.password"));
        if (cannotJoin) accessLabels.push(t("channel.access.denied"));
        el.classList.toggle("access-denied", cannotJoin);
        el.setAttribute("aria-label", t("runtime.channelName", { name: ch.Name }) + (accessLabels.length ? ", " + accessLabels.join(", ") : ""));
        el.title = accessLabels.join(" · ") || t("channel.access.open");
        el.innerHTML = `<span class="ch-disclosure" aria-hidden="true">${icon("chevron")}</span><span class="ch-icon">${icon(isEcho ? "echo" : "speaker")}</span><span class="ch-name"></span>`;
        el.querySelector(".ch-name").textContent = ch.Name;
        // (387) muted channel icon.
        if (window.__noxaNotify?.channelOverride?.(ch.ChannelID)?.muted) {
            const mute = document.createElement("span");
            mute.className = "ch-lock";
            mute.textContent = " 🔕";
            mute.title = t("runtime.channelMuted");
            el.querySelector(".ch-name").appendChild(mute);
        }
        if (restricted || cannotJoin || ch.HasPassword) {
            const badges = document.createElement("span");
            badges.className = "ch-access-badges";
            badges.setAttribute("aria-hidden", "true");
            if (restricted || cannotJoin) badges.innerHTML += `<span class="ch-access-lock">${icon("lock")}</span>`;
            if (ch.HasPassword) badges.innerHTML += `<span class="ch-access-key">${icon("key")}</span>`;
            el.querySelector(".ch-icon").appendChild(badges);
        }
        if (ch.ClientCount > 0 || ch.MaxClients > 0) {
            const count = document.createElement("span");
            count.className = "ch-count mono";
            count.textContent = `${ch.ClientCount}${ch.MaxClients > 0 ? "/" + ch.MaxClients : ""}`;
            el.appendChild(count);
        }
        // (104) unread badge; accent variant when it includes a mention of me.
        const ub = chatUI.unreadFor(ch.ChannelID);
        if (ub && ub.n > 0) {
            const badge = document.createElement("span");
            badge.className = "ch-unread" + (ub.mention ? " mention" : "");
            badge.textContent = ub.n > 99 ? "99+" : ub.n;
            el.appendChild(badge);
        }
        el.onclick = async () => {
            if (generation !== state.serverGeneration) return;
            if (cannotJoin) { toast(t("channel.access.denied"), "warn"); return; }
            const groupView = privateGroupViewToken();
            try {
                const err = await window.go.main.App.JoinChannelForTab(tabID, ch.ChannelID);
                if (err && generation === state.serverGeneration) toast(t("runtime.joinFailed", { error: String(err) }), "warn");
                if (!err && generation === state.serverGeneration && tabID === state.activeTabID && groupView && groupView === privateGroupViewToken()) await chatUI.openChannelTab(ch.ChannelID);
            } catch (err) {
                if (generation === state.serverGeneration) toast(t("runtime.joinFailed", { error: String(err) }), "warn");
            }
        };
        // (163) drag a channel onto another to take that channel's slot among its
        // siblings. The header is separate from its member/child branch, so a
        // member drag can never be relabelled as a channel drag.
        el.draggable = true;
        el.addEventListener("dragstart", (e) => {
            if (e.target !== el) return;
            e.dataTransfer.setData("text/noxa-chid", String(ch.ChannelID));
            e.dataTransfer.setData("text/noxa-tab", tabID);
            e.dataTransfer.setData("text/noxa-generation", String(generation));
            e.dataTransfer.effectAllowed = "move";
        });
        // (305) drag users onto a channel to move them there.
        el.addEventListener("dragover", (e) => {
            if (e.dataTransfer.types.includes("text/noxa-uid") ||
                e.dataTransfer.types.includes("text/noxa-chid")) {
                e.preventDefault();
                el.classList.add("drop-active");
            }
        });
        el.addEventListener("dragleave", () => el.classList.remove("drop-active"));
        el.addEventListener("drop", async (e) => {
            e.preventDefault();
            // sub-channels are nested inside their parent's element, so without
            // this a drop on a child also fires every ancestor's handler.
            e.stopPropagation();
            el.classList.remove("drop-active");
            if (generation !== state.serverGeneration || e.dataTransfer.getData("text/noxa-tab") !== tabID ||
                e.dataTransfer.getData("text/noxa-generation") !== String(generation)) return;
            const chid = Number(e.dataTransfer.getData("text/noxa-chid"));
            if (chid) {
                await reorderChannel(chid, ch, tabID, generation);
                return;
            }
            const uid = e.dataTransfer.getData("text/noxa-uid");
            const clientID = e.dataTransfer.getData("text/noxa-client");
            const target = state.clients.find((c) => c.client_id === clientID && c.unique_id === uid);
            if (!target) return;
            try {
                const err = target.client_id === state.myClientID
                    ? await window.go.main.App.JoinChannelForTab(tabID, ch.ChannelID)
                    : await window.go.main.App.MoveClientForTab(tabID, target.client_id, ch.ChannelID);
                if (err && generation === state.serverGeneration) toast(t("runtime.moveFailed", { error: String(err) }), "warn");
            } catch (err) {
                if (generation === state.serverGeneration) toast(t("runtime.moveFailed", { error: String(err) }), "warn");
            }
        });
        node.appendChild(el);
        parentEl.appendChild(node);

        // (302) collapsed channels hide their members and children; (349) in
        // windowed mode every channel off my branch counts as collapsed unless
        // the user explicitly expanded it (double-click).
        let collapsed = state.collapsedChannels.has(ch.ChannelID);
        if (window.__noxaPolish?.virtualizeEnabled?.()) {
            collapsed = state.collapsedChannels.has(ch.ChannelID) ||
                (!window.__noxaPolish.myBranchIDs().has(ch.ChannelID) &&
                    !state.expandedVirtual.has(ch.ChannelID));
        }
        const expandable = state.clients.some((c) => c.channel_id === ch.ChannelID) ||
            (byParent.get(ch.ChannelID) || []).length > 0;
        if (expandable) el.setAttribute("aria-expanded", String(!collapsed));
        el.querySelector(".ch-disclosure").onclick = (event) => {
            event.stopPropagation();
            if (expandable) setChannelExpanded(ch.ChannelID, collapsed);
        };
        if (!collapsed) {
            const members = state.clients.filter((c) => c.channel_id === ch.ChannelID);
            if (members.length > 0) {
                const list = document.createElement("div");
                list.className = "channel-members";
                list.setAttribute("role", "group");
                list.setAttribute("aria-label", t("runtime.members", { name: ch.Name }));
                for (const c of members) list.appendChild(clientRow(c));
                node.appendChild(list);
            }
            const children = byParent.get(ch.ChannelID) || [];
            if (children.length > 0) {
                const branch = document.createElement("div");
                branch.className = "channel-children";
                branch.setAttribute("role", "group");
                branch.setAttribute("aria-label", t("runtime.subchannels", { name: ch.Name }));
                for (const child of children) renderChannel(branch, child, byParent, depth + 1);
                node.appendChild(branch);
            }
        }
    }

    // (163) reorderChannel puts the dragged channel strictly before or after the
    // drop target according to its current direction of travel. The named field
    // list is deliberate: parent_id
    // 0 is the legitimate "move to root" value and so has no sentinel, and a
    // re-parent drops the server's whole permission cache — so "parent" is only
    // named when the drop actually changes the parent.
    async function reorderChannel(draggedID, target, tabID, generation) {
        if (draggedID === target.ChannelID) return;
        const dragged = state.channels.find((c) => c.ChannelID === draggedID);
        if (!dragged) return;
        const reparent = (dragged.ParentID || 0) !== (target.ParentID || 0);
        const draggedPos = state.channels.findIndex((c) => c.ChannelID === draggedID);
        const targetPos = state.channels.findIndex((c) => c.ChannelID === target.ChannelID);
        const movingDown = draggedPos >= 0 && targetPos >= 0 && draggedPos < targetPos;
        const targetOrder = target.OrderIndex || 0;
        const order = movingDown ? targetOrder + 1 : targetOrder - 1;
        const current = () => generation === state.serverGeneration && tabID === state.activeTabID;
        if (!Number.isInteger(order) || order < -2147483648 || order > 2147483647) {
            toast(t("roles.channel.orderLimit"), "warn");
            return;
        }
        try {
            const { openRoleChannel } = await import("./channel-lifecycle-ui.js");
            if (!current()) return;
            openRoleChannel(reparent ? "channel_move" : "channel_edit", draggedID, {
                destinationID: target.ParentID || 0, orderIndex: order,
            });
        } catch (err) {
            if (current()) toast(t("runtime.reorderFailed", { error: String(err) }), "warn");
        }
    }

    function clientRow(c) {
        const tabID = state.activeTabID, generation = state.serverGeneration;
        const row = document.createElement("div");
        const speakingHere = state.myChannelID !== 0 && c.channel_id === state.myChannelID && c.is_speaking && !isClientMicrophoneMuted(c, state);
        row.className = "client" + (speakingHere ? " speaking" : "") +
            (state.multiSelect.has(c.client_id) ? " selected" : "") +
            (c.status === "away" || c.status === "busy" || c.status === "invisible" ? " " + c.status : "");
        row.dataset.clid = c.client_id;
        row.tabIndex = 0; // (298) keyboard navigation
        row.setAttribute("role", "treeitem"); // (343)
        row.setAttribute("aria-selected", String(state.multiSelect.has(c.client_id)));
        row.setAttribute("aria-label", [c.nickname || c.unique_id || t("runtime.user"),
            c.status && presenceLabel(c.status), speakingHere && t("runtime.speaking"),
            c.priority_speaker && t("runtime.priority"),
            c.client_id === state.myClientID && state.muted && t("runtime.muted"),
            c.client_id === state.myClientID && state.deafened && t("runtime.deaf")].filter(Boolean).join(", "));
        // (140/305) users are draggable (group assign in the manager; move by
        // dropping onto a channel).
        if (c.unique_id) {
            row.draggable = true;
            row.addEventListener("dragstart", (e) => {
                e.dataTransfer.setData("text/noxa-uid", c.unique_id);
                e.dataTransfer.setData("text/noxa-tab", tabID);
                e.dataTransfer.setData("text/noxa-generation", String(generation));
                e.dataTransfer.setData("text/noxa-client", c.client_id);
                e.dataTransfer.effectAllowed = "copy";
            });
        }
        const av = document.createElement("span");
        av.className = "avatar";
        av.dataset.uid = c.unique_id;
        const dataUrl = state.avatars.get(c.unique_id);
        if (dataUrl) {
            setSafeImage(av, dataUrl);
        } else {
            av.textContent = initials(c.nickname || c.unique_id || "?");
            fetchAvatar(c.unique_id);
        }
        const name = document.createElement("span");
        name.className = "client-name";
        name.textContent = (c.nickname || c.unique_id) + (c.client_id === state.myClientID ? t("workspace.you") : "");
        // (178) nickname color from the first applicable server group.
        const gColor = P().groupColorFor(c.unique_id);
        if (gColor) name.style.color = gColor;
        const g = P().primaryGroup(c.unique_id);
        if (g) name.title = `${t("roles.title")}: ${(c.roles || []).map((r) => r.name).join(", ")}`;
        row.appendChild(av);
        row.appendChild(name);
        // (310) group badge next to the name. Groups without an icon get a text
        // chip in the group colour instead of nothing — colour and hoisting are
        // settable on their own, so an icon is not what makes a group visible.
        if (g) {
            row.appendChild(roleChip(g));
        }
        // (307) priority speaker: the flag is broadcast for every client, so it
        // belongs on their row and not only on my own voice-bar button.
        if (c.priority_speaker) {
            const pr = document.createElement("span");
            pr.className = "status-icons";
            pr.textContent = " ★";
            pr.title = t("runtime.priority");
            row.appendChild(pr);
        }
        // (307-309) presence status icons; (381) invisible marker (admin view).
        if (c.status === "away" || c.status === "busy" || c.status === "invisible") {
            const st = document.createElement("span");
            st.className = "status-icons";
            st.textContent = c.status === "away" ? " 🕐" : c.status === "busy" ? " ⛔" : " 👻";
            st.title = presenceLabel(c.status) + (c.status_message ? ": " + c.status_message : "");
            row.appendChild(st);
        }
        // (10) Own status icons: muted / deafened / screen sharing.
        if (c.client_id !== state.myClientID) {
            for (const [visible, glyph, key] of [[c.server_muted || c.self_muted || c.self_deafened, "micOff", c.server_muted ? "workspace.voice.serverMuted" : "workspace.voice.muted"],
                [c.server_deafened || c.self_deafened, "headphonesOff", c.server_deafened ? "workspace.voice.serverDeafened" : "audioState.deafened"]]) {
                if (!visible) continue;
                const status = document.createElement("span");
                status.className = "status-icons";
                status.innerHTML = icon(glyph);
                status.title = t(key);
                status.setAttribute("aria-label", t(key));
                row.append(status);
            }
        }
        if (c.client_id === state.myClientID) {
            const icons = document.createElement("span");
            icons.className = "status-icons";
            icons.innerHTML = icon(state.muted ? "micOff" : "mic") +
                (state.deafened ? icon("headphonesOff") : "") + (state.screenSharing ? icon("screen") : "");
            row.appendChild(icons);
            // (347) DND shows on own status icons.
            if (window.__noxaPolish?.dndActive?.()) {
                const dnd = document.createElement("span");
                dnd.className = "status-icons";
                dnd.textContent = " 🌙";
                dnd.title = t("runtime.dnd");
                row.appendChild(dnd);
            }
        } else if (isUserMuted(c.unique_id)) {
            // (2) Local-mute icon for users muted locally by me.
            const icons = document.createElement("span");
            icons.className = "status-icons";
            icons.textContent = " 🔕";
            icons.title = t("runtime.mutedLocally");
            row.appendChild(icons);
        }
        if (speakingHere) {
            const voice = document.createElement("span");
            voice.className = "client-voice-state";
            voice.title = t("runtime.talkingHere");
            voice.setAttribute("aria-label", t("runtime.talking"));
            voice.innerHTML = "<i></i><i></i><i></i>";
            row.appendChild(voice);
        }
        row.onclick = (e) => {
            e.stopPropagation();
            // (306) ctrl/shift multi-select; plain click selects just this user.
            if (e.ctrlKey || e.metaKey) {
                if (state.multiSelect.has(c.client_id)) state.multiSelect.delete(c.client_id);
                else state.multiSelect.add(c.client_id);
            } else if (e.shiftKey && state.selectedClientID) {
                const rows = state.clients.filter((x) => x.channel_id === c.channel_id);
                const ids = rows.map((x) => x.client_id);
                const a = ids.indexOf(state.selectedClientID);
                const b = ids.indexOf(c.client_id);
                if (a >= 0 && b >= 0) {
                    for (const id of ids.slice(Math.min(a, b), Math.max(a, b) + 1)) state.multiSelect.add(id);
                }
            } else {
                state.multiSelect = new Set([c.client_id]);
            }
            state.selectedClientID = c.client_id;
            setDetailsOpen(true);
            renderTree();
        };
        return row;
    }

    function initials(name) {
        const parts = name.trim().split(/\s+/);
        return (parts[0][0] + (parts.length > 1 ? parts[1][0] : "")).toUpperCase();
    }

    function clientName(clientID) {
        const c = state.clients.find((c) => c.client_id === clientID);
        return c ? (c.nickname || c.unique_id) : clientID;
    }

    return { expandMyBranch, renderTree, refreshEchoChannel, setChannelExpanded, initials, clientName };
}
