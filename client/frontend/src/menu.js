// menu.js — TS3-style menu bar with dropdown menus.
import { isActivationKey, wrappedIndex } from "./a11y.js";
import { closeDialog, mountDialog } from "./modal.js";

const V = () => window.__noxa;

let openMenu = null;
let documentClickBound = false;
let dndSaving = false;
const focusoutBoundBars = new WeakSet();

function closeMenus(restoreFocus = false) {
    const trigger = openMenu;
    document.querySelectorAll(".menu-dropdown.open").forEach((d) => d.classList.remove("open"));
    document.querySelectorAll(".menu-item.open").forEach((d) => {
        d.classList.remove("open");
        d.setAttribute("aria-expanded", "false");
    });
    openMenu = null;
    if (restoreFocus) trigger?.focus();
}

function menuAction(label, fn, opts = {}) {
    const a = document.createElement("a");
    a.textContent = typeof label === "function" ? label() : label;
    a.setAttribute("role", "menuitem");
    a.tabIndex = -1;
    if (opts.disabled) {
        a.className = "disabled";
        a.title = opts.tooltip || t("menu.comingSoon");
        a.setAttribute("aria-disabled", "true");
        return a;
    }
    a.refreshMenuState = () => {
        if (typeof label === "function") a.textContent = label();
        a.classList.toggle("disabled", false);
        a.setAttribute("aria-disabled", "false");
    };
    a.onclick = (event) => {
        event.preventDefault();
        event.stopPropagation();
        a.refreshMenuState();
        if (a.getAttribute("aria-disabled") === "true") return;
        closeMenus();
        fn();
    };
    a.addEventListener("keydown", (event) => {
        if (event.key === "Tab") {
            closeMenus();
            return;
        }
        if (isActivationKey(event.key)) {
            event.preventDefault();
            event.stopPropagation();
            a.click();
            return;
        }
        if (event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            closeMenus(true);
            return;
        }
        if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
        event.preventDefault();
        event.stopPropagation();
        const items = [...a.closest(".menu-dropdown").querySelectorAll('[role="menuitem"]')]
            .filter((item) => item.getAttribute("aria-disabled") !== "true");
        const next = wrappedIndex(items.indexOf(a), items.length, event.key === "ArrowDown" ? 1 : -1);
        items[next]?.focus();
    });
    return a;
}

function divider() {
    const d = document.createElement("div");
    d.className = "menu-divider";
    d.setAttribute("role", "separator");
    return d;
}

function buildMenu(label, items) {
    const item = document.createElement("div");
    item.className = "menu-item";
    item.tabIndex = 0;
    item.setAttribute("role", "menuitem");
    item.setAttribute("aria-haspopup", "menu");
    item.setAttribute("aria-expanded", "false");
    item.innerHTML = `<span>${label}</span>`;
    const drop = document.createElement("div");
    drop.className = "menu-dropdown";
    drop.setAttribute("role", "menu");
    drop.setAttribute("aria-label", label);
    for (const it of items) drop.appendChild(it);
    item.appendChild(drop);
    item.onclick = (e) => {
        e.stopPropagation();
        const wasOpen = openMenu === item;
        closeMenus();
        if (!wasOpen) {
            for (const entry of items) entry.refreshMenuState?.();
            item.classList.add("open");
            drop.classList.add("open");
            item.setAttribute("aria-expanded", "true");
            openMenu = item;
        }
    };
    item.addEventListener("keydown", (event) => {
        if (event.key === "Tab") {
            closeMenus();
            return;
        }
        if (event.key === "Escape") {
            closeMenus(true);
            return;
        }
        if (isActivationKey(event.key) || event.key === "ArrowDown") {
            event.preventDefault();
            if (openMenu !== item) item.click();
            const first = [...drop.querySelectorAll('[role="menuitem"]')]
                .find((entry) => entry.getAttribute("aria-disabled") !== "true");
            first?.focus();
            return;
        }
        if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
        event.preventDefault();
        const menus = [...document.querySelectorAll('#menubar > [role="menuitem"]')];
        const next = wrappedIndex(menus.indexOf(item), menus.length, event.key === "ArrowRight" ? 1 : -1);
        closeMenus();
        menus[next]?.focus();
    });
    return item;
}

// --- small dialogs -----------------------------------------------------------

function dlgPrompt(title, label, initial, cb) {
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg">
            <h3></h3>
            <label class="dlg-label"></label>
            <input type="text" class="dlg-input" />
            <div class="dlg-buttons">
                <button class="dlg-ok">${t("common.ok")}</button>
                <button class="dlg-cancel">${t("common.cancel")}</button>
            </div>
        </div>`;
    overlay.querySelector("h3").textContent = title;
    overlay.querySelector(".dlg-label").textContent = label;
    const input = overlay.querySelector(".dlg-input");
    input.value = initial || "";
    overlay.querySelector(".dlg-ok").onclick = () => { cb(input.value.trim()); overlay.remove(); };
    overlay.querySelector(".dlg-cancel").onclick = () => overlay.remove();
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    mountDialog(overlay);
    input.focus();
}

function dlgAbout() {
    const { state, $ } = V();
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg">
            <h3>${t("menu.about")}</h3>
            <div class="about-body">
                <div class="wordmark" style="font-size:26px">noXa</div>
                <div class="mono about-version"></div>
                <div class="mono about-uid"></div>
                <div class="about-links">
                    <a href="https://github.com/arumes31/noxa" target="_blank" rel="noopener noreferrer">${t("menu.project")}</a> ·
                    <a href="https://github.com/arumes31/noxa/issues" target="_blank" rel="noopener noreferrer">${t("menu.issues")}</a>
                </div>
            </div>
            <div class="dlg-buttons"><button class="dlg-ok">${t("common.close")}</button></div>
        </div>`;
    const versionEl = overlay.querySelector(".about-version");
    const setVersion = (text) => {
        if (overlay.isConnected && versionEl.isConnected) versionEl.textContent = text;
    };
    window.go.main.App.ClientVersion()
        .then((v) => setVersion(t("menu.version", { version: v })))
        .catch(() => setVersion(t("menu.versionUnavailable")));
    const uidEl = overlay.querySelector(".about-uid");
    uidEl.textContent = state.myUniqueID || t("menu.disconnected");
    for (const link of overlay.querySelectorAll(".about-links a")) {
        link.onclick = (event) => {
            event.preventDefault();
            event.stopPropagation();
            // Keep the Wails webview on the app. The bridge owns external
            // browser opening, and the ignored rejection is only a failed
            // hand-off after this dialog has already handled the click.
            void Promise.resolve()
                .then(() => window.runtime.BrowserOpenURL(link.href))
                .catch(() => {});
        };
    }
    overlay.querySelector(".dlg-ok").onclick = () => overlay.remove();
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    mountDialog(overlay);
}

// --- bookmarks ---------------------------------------------------------------

function currentSettings() {
    return V().state.settings || {};
}

async function saveSettings(patch, onError = error => V().toast(t("menu.saveFailed", { error }), "warn")) {
    try {
        const s = Object.assign({}, currentSettings(), patch);
        const err = await window.go.main.App.SaveSettings(s);
        if (err) throw new Error(err);
        // Re-read merged settings so concurrent Go-owned updates survive.
        V().state.settings = await window.go.main.App.GetSettings();
        return true;
    } catch (error) {
        onError(error.message || String(error));
        return false;
    }
}

async function bookmarkCurrent() {
    const { state, toast } = V();
    if (!state.lastConnect) {
        toast(t("menu.notConnected"), "warn");
        return;
    }
    const c = state.lastConnect;
    const name = c.nick + " @ " + c.addr;
    const bookmarks = (currentSettings().bookmarks || []).filter((b) => !(b.addr === c.addr && b.nickname === c.nick));
    bookmarks.push({ name, addr: c.addr, nickname: c.nick });
    if (await saveSettings({ bookmarks })) toast(t("menu.bookmarkSaved", { name }));
}

async function connectBookmark(b) {
    const { state, toast, $ } = V();
    $("login-addr").value = b.addr;
    // (334) per-server nickname override applies at connect.
    $("login-nick").value = b.nickname_override || b.nickname;
    $("login-serverpw").value = "";
    $("login-accountpw").value = "";
    state.lastConnect = null;
    V().showLogin();
    // (334) the override is what gets sent as the login nickname, so the
    // connect must carry the bookmark name to stay identifiable. Stashed
    // after showLogin, which drops the previous login's stash.
    state.pendingBookmark = { name: b.name, addr: b.addr };
    toast(t("menu.bookmarkLoaded"));
}

// sortedBookmarks returns bookmarks grouped by folder, ordered within (284).
function sortedBookmarks() {
    const bms = [...(currentSettings().bookmarks || [])];
    bms.sort((a, b) =>
        (a.folder || "").localeCompare(b.folder || "") ||
        (a.order || 0) - (b.order || 0) ||
        a.name.localeCompare(b.name));
    return bms;
}

function manageBookmarks() {
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    const persist = async (bookmarks = currentSettings().bookmarks, onError) => {
        const next = bookmarks.map(bookmark => ({ ...bookmark }));
        // Renumber the order within each folder (284).
        const groups = {};
        for (const b of next) {
            const f = b.folder || "";
            groups[f] = groups[f] || [];
            groups[f].push(b);
        }
        for (const f of Object.keys(groups)) groups[f].forEach((b, i) => { b.order = i; });
        return saveSettings({ bookmarks: next }, onError);
    };
    const render = () => {
        const bms = sortedBookmarks();
        const list = overlay.querySelector(".bm-list");
        list.innerHTML = bms.length === 0 ? `<div class="empty-state">${t("menu.bookmarksEmpty")}</div>` : "";
        let lastFolder = null;
        bms.forEach((b) => {
            const folder = b.folder || "";
            if (folder !== lastFolder) {
                lastFolder = folder;
                const head = document.createElement("div");
                head.className = "bm-folder";
                head.textContent = folder || t("menu.ungrouped");
                list.appendChild(head);
            }
            const idx = currentSettings().bookmarks.indexOf(b);
            const row = document.createElement("div");
            row.className = "bm-row";
            row.innerHTML = `
                <span class="bm-dot" title="${t("menu.bookmarkColor")}"></span>
                <span class="bm-name"></span>
                <span class="bm-addr mono"></span>
                <button class="bm-up" title="${t("menu.moveUp")}">↑</button>
                <button class="bm-down" title="${t("menu.moveDown")}">↓</button>
                <button class="bm-edit">${t("menu.edit")}</button>
                <button class="bm-del">${t("common.delete")}</button>`;
            row.querySelector(".bm-name").textContent = b.name;
            row.querySelector(".bm-addr").textContent = b.addr + (b.auto_connect ? " ⚡" : "");
            const dot = row.querySelector(".bm-dot");
            dot.style.background = b.color || "var(--text-faint)";
            dot.onclick = () => {
                const c = prompt(t("menu.colorPrompt"), b.color || "");
                if (c === null) return;
                b.color = c.trim();
                persist().then(render);
            };
            const s = currentSettings();
            row.querySelector(".bm-up").onclick = () => {
                if (idx > 0) {
                    [s.bookmarks[idx - 1], s.bookmarks[idx]] = [s.bookmarks[idx], s.bookmarks[idx - 1]];
                    persist().then(render);
                }
            };
            row.querySelector(".bm-down").onclick = () => {
                if (idx < s.bookmarks.length - 1) {
                    [s.bookmarks[idx + 1], s.bookmarks[idx]] = [s.bookmarks[idx], s.bookmarks[idx + 1]];
                    persist().then(render);
                }
            };
            row.querySelector(".bm-edit").onclick = () => editBookmark(b, persist, render);
            row.querySelector(".bm-del").onclick = () => {
                s.bookmarks.splice(idx, 1);
                persist().then(render);
            };
            list.appendChild(row);
        });
    };
    overlay.innerHTML = `
        <div class="dlg dlg-wide">
            <h3>${t("menu.bookmarksTitle")}</h3>
            <div class="bm-list"></div>
            <div class="dlg-buttons"><button class="dlg-ok">${t("common.close")}</button></div>
        </div>`;
    overlay.querySelector(".dlg-ok").onclick = () => overlay.remove();
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    mountDialog(overlay);
    render();
}

// editBookmark edits one bookmark's extended fields (283/284/286/300).
function editBookmark(bookmark, persist, render) {
    const b = { ...bookmark };
    let lastSubmittedName = bookmark.name;
    let saving = false;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg">
            <h3>${t("menu.editBookmark")}</h3>
            <label class="dlg-label">${t("menu.bookmarkName")}</label>
            <input class="dlg-input bm-f-name" />
            <label class="dlg-label">${t("menu.bookmarkFolder")}</label>
            <input class="dlg-input bm-f-folder" placeholder="${t("menu.bookmarkFolderExample")}" />
            <label class="dlg-label">${t("menu.hotkeyProfile")}</label>
            <input class="dlg-input bm-f-profile" placeholder="${t("menu.defaultProfile")}" />
            <label class="dlg-label">${t("menu.nicknameOverride")}</label>
            <input class="dlg-input bm-f-nick" placeholder="${t("menu.useBookmarkNickname")}" />
            <label class="dlg-label">${t("menu.avatarOverride")}</label>
            <div class="bm-f-avatar-row">
                <button class="icon-btn bm-f-avatar-btn">${t("menu.chooseImage")}</button>
                <span class="bm-f-avatar-state mono"></span>
            </div>
            <label class="dlg-label"><input type="checkbox" class="bm-f-auto" /> ${t("menu.autoConnect")}</label>
            <div class="bookmark-save-status dlg-text" role="status" hidden></div>
            <div class="dlg-buttons">
                <button class="dlg-ok">${t("common.save")}</button>
                <button class="dlg-cancel">${t("common.cancel")}</button>
            </div>
        </div>`;
    const q = (sel) => overlay.querySelector(sel);
    q(".bm-f-name").value = b.name;
    q(".bm-f-folder").value = b.folder || "";
    q(".bm-f-profile").value = b.profile || "";
    q(".bm-f-nick").value = b.nickname_override || "";
    q(".bm-f-auto").checked = !!b.auto_connect;
    q(".bm-f-avatar-state").textContent = b.avatar_override_b64 ? t("menu.avatarSize", { size: Math.round(b.avatar_override_b64.length / 1366) }) : t("menu.avatarNone");
    q(".bm-f-avatar-btn").onclick = async () => {
        const img = await pickIcon(256, 0.85);
        if (!img) return;
        b.avatar_override_b64 = img.dataBase64;
        q(".bm-f-avatar-state").textContent = t("menu.avatarSet");
    };
    q(".dlg-ok").onclick = async () => {
        if (saving) return;
        b.name = q(".bm-f-name").value.trim() || b.name;
        b.folder = q(".bm-f-folder").value.trim();
        b.profile = q(".bm-f-profile").value.trim();
        b.nickname_override = q(".bm-f-nick").value.trim();
        b.auto_connect = q(".bm-f-auto").checked;
        const bookmarks = currentSettings().bookmarks || [];
        const index = bookmarks.findIndex(entry => entry === bookmark || (
            entry.addr === bookmark.addr && entry.nickname === bookmark.nickname &&
            (entry.name === bookmark.name || entry.name === lastSubmittedName)
        ));
        const status = q(".bookmark-save-status");
        status.hidden = false;
        if (index < 0) {
            status.textContent = t("menu.bookmarkChanged");
            status.classList.add("warn");
            return;
        }
        const next = bookmarks.map((entry, i) => i === index ? { ...b } : entry);
        // A settings_update may already contain the rename even if the
        // subsequent refresh fails. Keep that identity available for retry.
        lastSubmittedName = b.name;
        saving = true;
        overlay.setAttribute("aria-busy", "true");
        const controls = [...overlay.querySelectorAll("input, button")];
        for (const control of controls) control.disabled = true;
        q(".dlg-ok").textContent = t("common.saving");
        status.textContent = t("common.saving");
        status.classList.remove("warn");
        try {
            if (await persist(next, error => {
                status.textContent = t("menu.saveFailed", { error });
                status.classList.add("warn");
            })) {
                closeDialog(overlay);
                render();
            }
        } finally {
            saving = false;
            overlay.removeAttribute("aria-busy");
            for (const control of controls) control.disabled = false;
            q(".dlg-ok").textContent = t("common.save");
            if (overlay.isConnected) q(".dlg-ok").focus();
        }
    };
    const cancel = () => { if (!saving) closeDialog(overlay, "cancel"); };
    q(".dlg-cancel").onclick = cancel;
    overlay.onclick = (e) => { if (e.target === overlay) cancel(); };
    mountDialog(overlay, { onCancel: () => !saving });
}

// --- Self actions --------------------------------------------------------------

// menu.js — TS3-style menu bar with dropdown menus.
import { t } from "./i18n.js";
import { pickAvatar, pickIcon } from "./image-tools.js";

// --- Self actions --------------------------------------------------------------

// setAvatarFile opens the avatar crop dialog (268): preview, zoom/reposition,
// 256x256 canvas resize; animated GIF/WebP pass through untouched (269).
async function setAvatarFile() {
    const tabID = V().state.activeTabID;
    const generation = V().state.serverGeneration;
    const img = await pickAvatar({ serverScoped: true, serverGeneration: generation });
    if (!img || generation !== V().state.serverGeneration) return;
    const err = await window.go.main.App.SetAvatarForTab(tabID, img.dataBase64);
    if (generation !== V().state.serverGeneration) return;
    if (err) V().toast(t("menu.avatarFailed", { error: err }), "warn");
    else V().toast(t("menu.avatarUpdated"));
}

// setServerIcon uploads a compressed server icon (admin, 270/274).
async function setServerIcon() {
    const tabID = V().state.activeTabID;
    const generation = V().state.serverGeneration;
    const img = await pickIcon();
    if (!img || generation !== V().state.serverGeneration) return;
    const err = await window.go.main.App.ServerIconSetForTab(tabID, img.dataBase64);
    if (generation !== V().state.serverGeneration) return;
    if (err) {
        V().toast(t("menu.serverIconFailed", { error: err }), "warn");
        return;
    }
    V().toast(t("menu.serverIconUpdated"));
    window.__noxaFiles.loadServerIcon(tabID);
}

// --- menu bar -------------------------------------------------------------------

export function initMenu() {
    const { $ } = V();
    $("menubar").innerHTML = ""; // rebuilds on language change (336)

    const connections = buildMenu(t("menu.connections"), [
        menuAction(t("menu.connect"), () => V().showLogin()),
        menuAction(t("menu.disconnect"), () => V().disconnect()),
        menuAction(t("menu.serverInfo"), () => window.__noxaMeta.openServerInfo()),
        divider(),
        menuAction(t("menu.quit"), () => window.runtime.Quit()),
    ]);

    const bookmarkItems = [menuAction(t("menu.bookmarkCurrent"), bookmarkCurrent), divider()];
    const bookmarkList = document.createElement("div");
    bookmarkList.className = "bm-menu-list";
    const renderBookmarkMenu = () => {
        bookmarkList.innerHTML = "";
        let lastFolder = null;
        for (const b of sortedBookmarks()) {
            const folder = b.folder || "";
            if (folder !== lastFolder) {
                lastFolder = folder;
                const head = document.createElement("div");
                head.className = "bm-menu-folder";
                head.textContent = folder;
                bookmarkList.appendChild(head);
            }
            const item = menuAction(b.name, () => connectBookmark(b));
            if (b.color) {
                // (284) same swatch the tab bar uses, so a bookmark reads the
                // same in the menu as it does once connected.
                const dot = document.createElement("span");
                dot.className = "srv-tab-dot";
                dot.style.background = b.color;
                item.prepend(dot);
            }
            bookmarkList.appendChild(item);
        }
    };
    const bookmarks = buildMenu(t("menu.bookmarks"), [
        ...bookmarkItems,
        bookmarkList,
        menuAction(t("menu.manageBookmarks"), manageBookmarks),
    ]);
    const bmItem = bookmarks;
    const origClick = bmItem.onclick;
    bmItem.onclick = (e) => { renderBookmarkMenu(); origClick(e); };

    const self = buildMenu(t("menu.self"), [
        menuAction(t("menu.changeNickname"), () => {
            dlgPrompt(t("menu.nicknameTitle"), t("menu.nicknamePrompt"), V().state.myNickname, (v) => {
                if (v) {
                    $("login-nick").value = v;
                    V().state.myNickname = v;
                    V().sysMsg(t("menu.nicknameSet", { nickname: v }));
                }
            });
        }),
        menuAction(t("menu.setStatus"), () => {
            if (!V().state.myClientID) return V().toast(t("menu.notConnected"), "warn");
            window.__noxaSocial.openStatusPicker();
        }),
        menuAction(t("menu.contacts"), () => window.__noxaSocial.openContacts()),
        menuAction(t("menu.setAvatar"), setAvatarFile),
        menuAction(t("menu.setServerIcon"), () => {
            if (!V().state.myClientID) return V().toast(t("menu.notConnected"), "warn");
            setServerIcon();
        }),
        divider(),
        menuAction(t("menu.toggleMute"), () => $("voice-mute").click()),
        menuAction(t("menu.toggleDeafen"), () => {
            const { state, setDeafened, sysMsg } = V();
            setDeafened(!state.deafened);
            sysMsg(state.deafened ? t("menu.deafened") : t("menu.undeafened"));
        }),
    ]);

    const permissions = buildMenu(t("menu.permissions"), [
        menuAction(() => t("roles.myRoles"), () => {
            V().refreshPermissions();
            V().setDetailsOpen(true);
        }),
        menuAction(() => t("roles.title"), async () => {
            if (!V().state.myClientID) return V().toast(t("menu.notConnected"), "warn");
            const generation = V().state.serverGeneration;
            try {
                const { openRolesManager } = await import("./roles-ui.js");
                if (generation === V().state.serverGeneration) openRolesManager();
            } catch { if (generation === V().state.serverGeneration) V().toast(t("roles.unavailable"), "warn"); }
        }),
    ]);

    const tools = buildMenu(t("menu.tools"), [
        menuAction(t("menu.settings"), () => window.__noxa.openSettings("application")),
        menuAction(t("menu.whisperLists"), () => window.__noxa.openSettings("whisper")),
        divider(),
        menuAction(t("menu.auditLog"), () => {
            if (!V().state.myClientID) return V().toast(t("menu.notConnected"), "warn");
            window.__noxaPerms.openAuditViewer();
        }),
        menuAction(t("menu.bans"), () => {
            if (!V().state.myClientID) return V().toast(t("menu.notConnected"), "warn");
            window.__noxaPerms.openBanList();
        }),
        menuAction(t("menu.chatFilters"), () => {
            if (!V().state.myClientID) return V().toast(t("menu.notConnected"), "warn");
            window.__noxaPerms.openChatFilters();
        }),
        // (173) complaint review, gated the same way as the audit viewer.
        menuAction(t("menu.complaints"), () => {
            if (!V().state.myClientID) return V().toast(t("menu.notConnected"), "warn");
            window.__noxaPerms.openComplaints();
        }),
        divider(),
        menuAction(t("menu.debugConsole"), () => {
            if (!V().state.myClientID) return V().toast(t("menu.notConnected"), "warn");
            window.__noxaMeta.openDebugConsole();
        }),
        menuAction(t("menu.connStats"), () => {
            if (!V().state.myClientID) return V().toast(t("menu.notConnected"), "warn");
            window.__noxaMeta.openStatsPage();
        }),
    ]);

    // View menu: window/integration toggles (wave 8a/8c).
    const view = buildMenu(t("menu.view"), [
        menuAction(t("menu.toggleDetails"), () => {
            V().setDetailsOpen(document.body.classList.contains("details-collapsed"));
        }),
        divider(),
        menuAction(t("menu.compact"), () => V().toggleCompact()),
        menuAction(t("menu.zen"), () => window.__noxaPolish.toggleZen()),
        menuAction(t("menu.popOutChat"), () => window.__noxaPolish.toggleChatPopout()),
        menuAction(t("menu.dnd"), async () => {
            if (dndSaving) return;
            dndSaving = true;
            const enabled = !V().state.settings?.dnd_enabled;
            try {
                if (await saveSettings({ dnd_enabled: enabled }, error => {
                    V().toast(t("menu.saveFailed", { error }), "warn", "alert", { bypassDND: true });
                })) {
                    V().toast(t(V().state.settings.dnd_enabled ? "menu.dndOn" : "menu.dndOff"), "info", "alert", { bypassDND: true });
                    V().renderTree();
                }
            } finally {
                dndSaving = false;
            }
        }),
        menuAction(t("menu.alwaysOnTop"), async () => {
            const on = !(V().state.settings?.always_on_top);
            await window.go.main.App.SetAlwaysOnTop(on);
            if (V().state.settings) V().state.settings.always_on_top = on;
            V().toast(t(on ? "menu.alwaysOnTopOn" : "menu.alwaysOnTopOff"));
        }),
        menuAction(t("menu.opacity"), () => {}, {
            disabled: true,
            tooltip: t("menu.opacityUnsupported"),
        }),
        divider(),
        menuAction(t("menu.themeDark"), () => setTheme("dark")),
        menuAction(t("menu.themeLight"), () => setTheme("light")),
        menuAction(t("menu.themeContrast"), () => setTheme("hc")),
    ]);

    const help = buildMenu(t("menu.help"), [
        menuAction(t("menu.checkUpdates"), () => window.__noxa.checkForUpdatesInteractive()),
        menuAction(t("menu.exportLogs"), async () => {
            const err = await window.go.main.App.ExportLogs();
            if (err) V().toast(t("menu.exportFailed", { error: err }), "warn");
            else V().toast(t("menu.logsExported"));
        }),
        divider(),
        menuAction(t("menu.about"), dlgAbout),
        menuAction(t("menu.openLogFolder"), async () => {
            const err = await window.go.main.App.OpenLogFolder();
            if (err) V().toast(t("menu.openLogFolderFailed", { error: err }), "warn");
        }),
    ]);

    const bar = $("menubar");
    bar.setAttribute("role", "menubar");
    for (const m of [connections, bookmarks, self, view, permissions, tools, help]) bar.appendChild(m);
    if (!focusoutBoundBars.has(bar)) {
        focusoutBoundBars.add(bar);
        bar.addEventListener("focusout", () => {
            setTimeout(() => {
                if (openMenu && !openMenu.contains(document.activeElement)) closeMenus();
            }, 0);
        });
    }
    if (!documentClickBound) {
        document.addEventListener("click", () => closeMenus());
        document.addEventListener("focusin", (event) => {
            if (openMenu && !openMenu.contains(event.target)) closeMenus();
        });
        documentClickBound = true;
    }
}

// setTheme switches the UI theme (294/295) and persists it.
async function setTheme(theme) {
    const s = Object.assign({}, V().state.settings || {}, { theme });
    const err = await window.go.main.App.SaveSettings(s);
    if (err) {
        V().toast(t("menu.saveFailed", { error: err }), "warn");
        return;
    }
    // (282) same as saveSettings: the merged blob is the authoritative cache.
    V().state.settings = await window.go.main.App.GetSettings();
    V().applyAppearance();
}
