import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { appendUnreadBadge } from "./chat-unread.js";
import { mountContextMenu, closeContextMenu, contextMenuKey } from "./context-menu.js";
import "./chat-tabs.css";

// Layout contains stable references only. Membership and actions always come
// from the current server/identity, never from saved layout metadata.
export function createChatTabs(root, { onLayoutChange, onRefresh } = {}) {
    let items = [], layout = { order: [], pinned: [] }, scope, ready = false;
    let list, more, activeKey, menu = null, menuIsAll = false, drag = null;
    const pinned = key => layout.pinned.includes(key);
    const find = key => items.find(item => item.key === key);
    const element = key => [...root.querySelectorAll(".pm-tab")].find(el => el.dataset.chatKey === key);
    const select = key => element(key)?.querySelector(".pm-tab-select");
    const ordered = () => {
        const rank = new Map(layout.order.map((key, index) => [key, index]));
        return [...items].sort((a, b) => Number(pinned(b.key)) - Number(pinned(a.key)) ||
            (rank.get(a.key) ?? Infinity) - (rank.get(b.key) ?? Infinity));
    };
    function reveal(key) {
        const el = element(key);
        if (!el || !list) return;
        const bounds = el.getBoundingClientRect(), viewport = list.getBoundingClientRect();
        if (bounds.left < viewport.left + 8) list.scrollLeft -= viewport.left + 8 - bounds.left;
        else if (bounds.right > viewport.right - 8) list.scrollLeft += bounds.right - viewport.right + 8;
    }
    function focus(key, scroll = true) {
        const selected = select(key);
        for (const button of root.querySelectorAll(".pm-tab-select")) button.tabIndex = button === selected ? 0 : -1;
        selected?.focus({ preventScroll: true });
        if (scroll) reveal(key);
    }
    function description(item) {
        return [item.name, pinned(item.key) && t("chat.tabsPinned"), item.unread > 0 && t("chat.tabsUnread", { count: item.unread }), item.offline && t("chat.offline")].filter(Boolean).join(", ");
    }
    function save(order, pins, key) {
        if (!ready) return;
        // Retain absent peers' preferences; authority to reopen is separate.
        const keys = [...new Set([...order, ...layout.order])].slice(0, 256);
        onLayoutChange?.({ order: keys, pinned: pins.filter(id => keys.includes(id)).slice(0, 64) });
        focus(key);
    }
    function togglePin(key) {
        if (!find(key) || (!pinned(key) && layout.pinned.length >= 64)) return;
        save(ordered().map(item => item.key), pinned(key) ? layout.pinned.filter(id => id !== key) : [...layout.pinned, key], key);
    }
    function move(key, offset) {
        const peers = ordered().filter(item => pinned(item.key) === pinned(key));
        const index = peers.findIndex(item => item.key === key), target = peers[index + offset];
        if (!target) return;
        const keys = ordered().map(item => item.key), a = keys.indexOf(key), b = keys.indexOf(target.key);
        [keys[a], keys[b]] = [keys[b], keys[a]];
        save(keys, layout.pinned, key);
    }
    function mountMenu(trigger, actions, event) {
        closeContextMenu(menu);
        const captured = scope;
        menu = document.createElement("div"); menu.className = "ctx-menu chat-tabs-menu";
        const ownedMenu = menu;
        menuIsAll = trigger === more;
        for (const action of actions) {
            const button = document.createElement("button"); button.type = "button"; button.className = "ctx-action";
            button.textContent = action.label; button.disabled = !!action.disabled;
            if (action.danger) button.classList.add("ctx-danger");
            button.onclick = () => {
                if (menu !== ownedMenu || !ownedMenu.isConnected || scope !== captured) return;
                closeContextMenu(ownedMenu);
                action.run();
            };
            menu.append(button);
        }
        const key = trigger.closest(".pm-tab")?.dataset.chatKey;
        more?.setAttribute("aria-expanded", String(trigger === more));
        mountContextMenu(menu, {
            trigger, x: event?.clientX, y: event?.clientY,
            resolveTrigger: () => key ? select(key) : more,
            onClose: () => { more?.setAttribute("aria-expanded", "false"); menu = null; menuIsAll = false; },
        });
    }
    function context(key, event) {
        event?.preventDefault(); event?.stopPropagation();
        const item = find(key);
        if (!item) return;
        const peers = ordered().filter(peer => pinned(peer.key) === pinned(key));
        const index = peers.findIndex(peer => peer.key === key);
        const actions = [
            { label: t("context.openConversation"), run: () => find(key)?.open() },
            { label: t(pinned(key) ? "chat.tabsUnpin" : "chat.tabsPin"), disabled: !ready || (!pinned(key) && layout.pinned.length >= 64), run: () => togglePin(key) },
            { label: t("chat.tabsMoveLeft"), disabled: !ready || index < 1, run: () => move(key, -1) },
            { label: t("chat.tabsMoveRight"), disabled: !ready || index === peers.length - 1, run: () => move(key, 1) },
            { label: t("context.closeConversation"), disabled: !item.close, run: () => find(key)?.close?.() },
        ];
        if (item.clearHistory) actions.push({ label: t("context.clearHistory"), danger: true, run: () => find(key)?.clearHistory?.() });
        mountMenu(select(key), actions, event?.type === "contextmenu" ? event : null);
    }
    function render(nextItems, nextLayout, options = {}) {
        const changedScope = scope !== options.scope;
        const previous = root.contains(document.activeElement) ? document.activeElement.closest(".pm-tab")?.dataset.chatKey : null;
        const wasMore = document.activeElement === more;
        const oldIndex = ordered().findIndex(item => item.key === previous);
        const scroll = !changedScope && list ? list.scrollLeft : 0;
        if (changedScope) { closeContextMenu(menu); drag = null; activeKey = null; }
        // Replacing the gesture's source node cancels the drag as well.
        drag = null;
        scope = options.scope; ready = options.ready;
        const captured = scope;
        items = nextItems; layout = nextLayout;
        const nextActive = items.find(item => item.active)?.key;
        root.replaceChildren(); root.classList.toggle("hidden", items.length === 0);
        list = document.createElement("div"); list.className = "pm-tab-list";
        list.setAttribute("role", "tablist"); list.setAttribute("aria-label", t("chat.tabsLabel"));
        const tabs = ordered();
        const focusKey = !changedScope && find(previous) ? previous : nextActive || tabs[0]?.key;
        for (const [index, item] of tabs.entries()) {
            const tab = document.createElement("div"); tab.dataset.chatKey = item.key;
            const current = () => scope === captured && root.contains(tab);
            tab.className = "pm-tab" + (item.key.startsWith("ch:") ? " channel-tab" : "") + (item.active ? " active" : "") + (pinned(item.key) ? " pinned" : "");
            tab.setAttribute("role", "presentation"); tab.tabIndex = -1;
            tab.title = item.title || item.name;
            const button = document.createElement("button"); button.type = "button"; button.className = "pm-tab-select";
            button.id = `chat-conversation-${index}`;
            button.setAttribute("role", "tab"); button.setAttribute("aria-selected", String(!!item.active));
            button.setAttribute("aria-controls", "chat-wrap"); button.setAttribute("aria-label", description(item));
            button.tabIndex = item.key === focusKey ? 0 : -1;
            if (pinned(item.key)) { const pin = document.createElement("span"); pin.className = "pm-tab-pin"; pin.innerHTML = icon("pin"); pin.setAttribute("aria-hidden", "true"); button.append(pin); }
            const name = document.createElement("span"); name.className = "pm-tab-name"; name.textContent = item.name; button.append(name);
            tab.append(button);
            if (item.offline) { const badge = document.createElement("span"); badge.className = "pm-badge offline"; badge.textContent = t("chat.offline"); tab.append(badge); }
            if (item.unread > 0) appendUnreadBadge(tab, item.unread, item.arrivedAt, item.mention);
            if (item.close) {
                const close = document.createElement("button"); close.type = "button"; close.className = "pm-close"; close.textContent = "✕"; close.tabIndex = -1;
                close.setAttribute("aria-label", t("chat.tabsCloseNamed", { name: item.name })); close.title = t("chat.tabClose");
                close.onclick = e => { e.stopPropagation(); if (current()) find(item.key)?.close?.(); }; tab.append(close);
            }
            tab.onclick = () => { if (current()) find(item.key)?.open(); };
            tab.oncontextmenu = e => { if (current()) context(item.key, e); };
            tab.onkeydown = e => {
                if (!current()) return;
                if (e.target.closest(".pm-close")) return;
                if (contextMenuKey(e)) return context(item.key, e);
                if (e.ctrlKey && e.shiftKey && ["ArrowLeft", "ArrowRight"].includes(e.key)) { e.preventDefault(); move(item.key, e.key === "ArrowLeft" ? -1 : 1); return; }
                if (e.altKey || e.ctrlKey || e.metaKey) return;
                if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) {
                    e.preventDefault(); const peers = ordered(), index = peers.findIndex(peer => peer.key === item.key);
                    const next = e.key === "Home" ? 0 : e.key === "End" ? peers.length - 1 : (index + (e.key === "ArrowLeft" ? -1 : 1) + peers.length) % peers.length;
                    focus(peers[next].key);
                } else if (["Enter", " "].includes(e.key)) { e.preventDefault(); find(item.key)?.open(); }
                else if (e.key === "Delete") { e.preventDefault(); find(item.key)?.close?.(); }
            };
            tab.draggable = !!ready;
            tab.ondragstart = e => {
                if (!current() || !ready || e.target.closest(".pm-close")) { e.preventDefault(); return; }
                drag = { key: item.key, scope }; e.dataTransfer.effectAllowed = "move"; e.dataTransfer.setData("application/x-noxa-chat-tab", item.key);
            };
            tab.ondragover = e => { if (current() && drag?.scope === scope && find(drag.key) && pinned(drag.key) === pinned(item.key)) { e.preventDefault(); e.dataTransfer.dropEffect = "move"; tab.classList.add("drop-target"); } };
            tab.ondragleave = () => tab.classList.remove("drop-target");
            tab.ondragend = () => { if (current()) { drag = null; root.querySelectorAll(".drop-target").forEach(el => el.classList.remove("drop-target")); } };
            tab.ondrop = e => {
                e.preventDefault(); tab.classList.remove("drop-target");
                if (!current() || !ready || drag?.scope !== scope || !find(drag.key) || drag.key === item.key || pinned(drag.key) !== pinned(item.key)) return;
                const key = drag.key; drag = null;
                const keys = ordered().map(peer => peer.key).filter(id => id !== key), target = keys.indexOf(item.key);
                const bounds = tab.getBoundingClientRect();
                keys.splice(target + (e.clientX > bounds.left + bounds.width / 2 ? 1 : 0), 0, key);
                save(keys, layout.pinned, key);
            };
            list.append(tab);
        }
        more = document.createElement("button"); more.type = "button"; more.className = "pm-tabs-more";
        more.innerHTML = icon("chevron"); more.setAttribute("aria-label", t("chat.tabsAll")); more.title = t("chat.tabsAll");
        more.setAttribute("aria-haspopup", "menu"); more.setAttribute("aria-expanded", String(menuIsAll && !!menu?.isConnected));
        const total = items.reduce((count, item) => count + (item.unread || 0), 0);
        if (total) { const count = document.createElement("span"); count.className = "pm-tabs-total"; count.textContent = total > 99 ? "99+" : String(total); more.append(count); more.setAttribute("aria-label", `${t("chat.tabsAll")}, ${t("chat.tabsUnread", { count: total })}`); }
        const trigger = more;
        more.onclick = () => {
            if (scope === captured && root.contains(trigger)) mountMenu(trigger, ordered().map(item => ({ label: description(item), run: () => { find(item.key)?.open(); focus(item.key); } })));
        };
        root.append(list, more); list.scrollLeft = scroll;
        const panel = document.getElementById("chat-wrap");
        if (panel) {
            panel.setAttribute("role", "tabpanel");
            if (select(nextActive)) panel.setAttribute("aria-labelledby", select(nextActive).id);
            else { panel.removeAttribute("aria-labelledby"); panel.setAttribute("aria-label", t("chat.tabsLabel")); }
        }
        if (!changedScope && previous) focus(find(previous) ? previous : tabs[Math.min(Math.max(oldIndex, 0), tabs.length - 1)]?.key, !find(previous));
        else if (wasMore) more.focus({ preventScroll: true });
        if (nextActive !== activeKey) reveal(nextActive);
        activeKey = nextActive;
        onRefresh?.();
    }
    return { render, dispose() { closeContextMenu(menu); drag = null; items = []; root.replaceChildren(); } };
}
