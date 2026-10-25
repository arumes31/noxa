// my-devices.js — "My devices" (442/443/459): the account's own devices with
// online state, last use, "this device", and an inline rename. Mounted in the
// "PWA / Mobile login" dialog. Every rename (here or elsewhere) announces
// DEVICE_LIST_CHANGED so other views refresh without a restart.
import "./my-devices.css";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { relativeTime } from "./dm-model.js";
import { defaultDeviceName, mergeDevicePresence, renameDeviceLocally, validDeviceName } from "./my-devices-model.js";
import { sessionUserID } from "./session-identity.js";

const app = () => window.go.main.App;
export const DEVICE_LIST_CHANGED = "noxa-device-list-changed";

// announceDeviceListChanged tells every open device view that the account's
// device list (names, membership) changed.
export function announceDeviceListChanged(detail = {}) {
    window.dispatchEvent(new CustomEvent(DEVICE_LIST_CHANGED, { detail }));
}

function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
}

// mountMyDevices renders the section into `container`. `live()` gates every
// asynchronous continuation (the dialog may have closed or switched servers).
// `setDevices` accepts a list another caller already fetched; `refresh`
// fetches one. Presence is re-read from state on every render.
export function mountMyDevices(container, { tabID, live = () => true, fetchDevices = true } = {}) {
    const v = window.__noxa;
    let devices = [];
    let editing = "";
    let busy = false;
    const section = el("section", "my-devices");
    section.setAttribute("aria-labelledby", "my-devices-title");
    const title = el("h4", "my-devices-title", t("devices.mine"));
    title.id = "my-devices-title";
    const hint = el("p", "hint", t("devices.mineHint"));
    const list = el("ul", "my-devices-list");
    list.setAttribute("aria-live", "polite");
    section.append(title, hint, list);
    container.append(section);

    const state = () => v?.state || {};

    function render() {
        if (!live()) return;
        const rows = mergeDevicePresence(devices, state().clients, { myUniqueID: sessionUserID(state()), myClientID: state().myClientID });
        if (!rows.length) { list.replaceChildren(el("li", "hint", t("devices.empty"))); return; }
        list.replaceChildren(...rows.map(row => {
            const item = el("li", "my-device");
            item.dataset.deviceId = row.deviceID;
            item.classList.toggle("is-online", row.online);
            item.classList.toggle("is-current", row.current);
            const glyph = el("span", "my-device-icon");
            glyph.innerHTML = icon(row.kind === "web" ? "phone" : "screen");
            glyph.setAttribute("aria-hidden", "true");
            const body = el("div", "my-device-body");
            const shown = defaultDeviceName({ name: row.name, kind: row.kind }, t);
            if (editing === row.deviceID) body.append(renameForm(row, shown));
            else {
                const name = el("button", "my-device-name", shown);
                name.type = "button";
                name.title = t("devices.renameInline", { name: shown });
                name.setAttribute("aria-label", t("devices.renameInline", { name: shown }));
                name.onclick = () => { editing = row.deviceID; render(); };
                body.append(name);
            }
            const tags = [t(`devices.kind.${row.kind}`)];
            if (row.current) tags.push(t("devices.current"));
            const meta = el("span", "my-device-meta", tags.join(" · "));
            const status = el("span", "my-device-status", row.online
                ? t("devices.online")
                : row.lastSeenAt ? `${t("devices.offline")} · ${t("devices.lastSeen", { when: relativeTime(row.lastSeenAt * 1000) })}` : t("devices.offline"));
            status.dataset.online = String(row.online);
            body.append(meta, status);
            item.append(glyph, body);
            return item;
        }));
        const input = list.querySelector("input");
        if (input) { input.focus(); input.select(); }
    }

    function renameForm(row, shown) {
        const form = el("form", "my-device-rename");
        const input = el("input", "dlg-input");
        input.type = "text";
        input.maxLength = 64;
        input.value = row.name && row.name !== "Phone" ? row.name : shown;
        input.setAttribute("aria-label", t("devices.renamePrompt"));
        const save = el("button", "dlg-ok", t("devices.renameSave"));
        save.type = "submit";
        const cancel = el("button", "dlg-cancel", t("devices.renameCancel"));
        cancel.type = "button";
        const stop = () => { editing = ""; render(); };
        cancel.onclick = stop;
        input.onkeydown = event => { if (event.key === "Escape") { event.preventDefault(); stop(); } };
        form.onsubmit = async event => {
            event.preventDefault();
            if (busy) return;
            const name = validDeviceName(input.value);
            if (!name) { v?.toast(t("devices.renameInvalid"), "warn"); return; }
            if (name === row.name) { stop(); return; }
            busy = true;
            save.disabled = cancel.disabled = true;
            try {
                await app().RenameDeviceForTab(tabID, row.deviceID, name);
                if (!live()) return;
                devices = renameDeviceLocally(devices, row.deviceID, name);
                editing = "";
                render();
                v?.toast(t("devices.renamed", { name }));
                announceDeviceListChanged({ tabID, deviceID: row.deviceID, name, source: "my-devices" });
            } catch (error) {
                if (live()) v?.toast(t("devices.failed", { error: error?.message || String(error) }), "warn");
            } finally { busy = false; save.disabled = cancel.disabled = false; }
        };
        form.append(input, save, cancel);
        return form;
    }

    async function refresh() {
        try {
            const next = await app().DevicesForTab(tabID);
            if (!live()) return;
            devices = Array.isArray(next) ? next : [];
            render();
        } catch (error) {
            if (live()) list.replaceChildren(el("li", "dlg-error", t("devices.failed", { error: error?.message || String(error) })));
        }
    }

    const onChanged = event => { if (event?.detail?.source !== "my-devices") void refresh(); };
    window.addEventListener(DEVICE_LIST_CHANGED, onChanged);
    const onLanguage = () => render();
    window.addEventListener("noxa-language-changed", onLanguage);
    // Presence changes have no event of their own; re-read it while mounted.
    const presenceTimer = setInterval(() => { if (live() && !editing) render(); else if (!live()) dispose(); }, 5000);

    function dispose() {
        clearInterval(presenceTimer);
        window.removeEventListener(DEVICE_LIST_CHANGED, onChanged);
        window.removeEventListener("noxa-language-changed", onLanguage);
    }

    if (fetchDevices) void refresh();
    return {
        element: section,
        refresh,
        dispose,
        setDevices(next) { devices = Array.isArray(next) ? next : []; render(); },
    };
}
