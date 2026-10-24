// device-link-ui.js — "Other login for this server": link a phone with a QR
// code or hidden code, compare the two-digit code, approve, and manage the
// account's linked devices.
import { closeDialog, confirmDialog, isCurrentServerDialog, mountServerDialog, promptDialog } from "./modal.js";
import { relativeTime } from "./dm-model.js";
import { t } from "./i18n.js";

const app = () => window.go.main.App;

function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
}

function button(label, className, onClick) {
    const node = el("button", className, label);
    node.type = "button";
    node.onclick = onClick;
    return node;
}

// watchHistoryCopy announces the outcome of a background history copy. It
// outlives the dialog, which may be closed while the copy uploads.
function watchHistoryCopy(deviceID) {
    const stop = window.runtime.EventsOn("device_history", event => {
        if (!event || event.device_id !== deviceID || event.state === "progress") return;
        if (typeof stop === "function") stop();
        if (event.state === "done") window.__noxa.toast(t("devices.historyDone", { messages: event.messages }));
        else window.__noxa.toast(t("devices.historyFailed", { error: event.error || "" }), "warn");
    });
}

export function openDeviceLinkDialog() {
    const v = window.__noxa;
    if (!v.state.myClientID) return v.toast(t("menu.notConnected"), "warn");
    const tabID = v.state.activeTabID;
    const overlay = el("div", "dlg-overlay");
    const dialog = el("div", "dlg dlg-wide device-link");
    dialog.setAttribute("role", "dialog");
    dialog.setAttribute("aria-labelledby", "device-link-title");
    const title = el("h3", "", t("devices.title"));
    title.id = "device-link-title";
    const linkPane = el("section", "device-link-pane");
    const listTitle = el("h4", "device-list-title", t("devices.listTitle"));
    const list = el("ul", "device-list");
    const close = button(t("common.close"), "dlg-cancel", () => closeDialog(overlay));
    const footer = el("div", "dlg-buttons");
    footer.append(close);
    dialog.append(title, linkPane, listTitle, list, footer);
    overlay.append(dialog);

    let link = null;
    let countdown = 0;
    let decided = false;
    const stopTimer = () => { if (countdown) clearInterval(countdown); countdown = 0; };
    const live = () => isCurrentServerDialog(overlay);

    const showError = error => {
        linkPane.replaceChildren(el("p", "dlg-error", t("devices.failed", { error: error?.message || String(error) })));
    };

    const renderExpired = () => {
        stopTimer();
        linkPane.replaceChildren(el("p", "device-link-status", t("devices.expired")), button(t("devices.newCode"), "dlg-ok", createLink));
    };

    const renderLink = () => {
        const intro = el("p", "hint", t("devices.intro"));
        const image = el("img", "device-link-qr");
        image.src = link.qr;
        image.alt = t("devices.qrAlt");
        const code = el("output", "device-link-code", link.code);
        code.setAttribute("aria-label", t("devices.codeLabel"));
        code.dataset.hidden = "true";
        const reveal = button(t("devices.showCode"), "device-link-reveal", () => {
            const hidden = code.dataset.hidden !== "true";
            code.dataset.hidden = String(hidden);
            reveal.textContent = t(hidden ? "devices.showCode" : "devices.hideCode");
            reveal.setAttribute("aria-pressed", String(!hidden));
        });
        reveal.setAttribute("aria-pressed", "false");
        const codeRow = el("div", "device-link-code-row");
        codeRow.append(el("span", "dlg-label", t("devices.codeLabel")), code, reveal);
        const timer = el("p", "device-link-timer");
        timer.setAttribute("aria-live", "off");
        const status = el("p", "device-link-status", t("devices.waiting"));
        status.setAttribute("aria-live", "polite");
        linkPane.replaceChildren(intro, image, codeRow, timer, status);
        const tick = () => {
            const seconds = Math.max(0, Math.round(link.expires_at - Date.now() / 1000));
            timer.textContent = t("devices.expiresIn", { seconds });
            if (seconds === 0) renderExpired();
        };
        stopTimer();
        tick();
        countdown = setInterval(() => { if (live()) tick(); else stopTimer(); }, 1000);
    };

    const renderClaim = event => {
        stopTimer();
        const platform = event.platform || t("devices.unknownPlatform");
        const question = el("p", "device-link-status", t("devices.claimed", { platform, name: v.state.myNickname || "" }));
        const sas = el("output", "device-link-sas", event.sas);
        sas.setAttribute("aria-label", t("devices.compare"));
        const compare = el("p", "hint", t("devices.compare"));
        const fingerprint = el("p", "device-link-fingerprint", t("devices.fingerprint", { fingerprint: event.fingerprint }));
        // History copy: re-encrypt recent direct messages to the new device.
        const history = el("div", "device-link-history");
        const copyLabel = el("label", "dlg-check");
        const copy = el("input");
        copy.type = "checkbox";
        copy.checked = true;
        copyLabel.append(copy, document.createTextNode(" " + t("devices.copyHistory")));
        const range = el("select", "dlg-input device-link-history-range");
        range.setAttribute("aria-label", t("devices.historyRange"));
        for (const [value, key] of [["7", "7"], ["30", "30"], ["90", "90"], ["-1", "all"]]) {
            const option = el("option", "", t(`devices.historyRange.${key}`));
            option.value = value;
            range.append(option);
        }
        range.value = "90";
        copy.onchange = () => { range.disabled = !copy.checked; };
        history.append(copyLabel, el("span", "dlg-label", t("devices.historyRange")), range);
        const actions = el("div", "dlg-buttons");
        const decide = async approve => {
            if (decided) return;
            decided = true;
            actions.querySelectorAll("button").forEach(b => { b.disabled = true; });
            try {
                const result = await app().DecideDeviceLinkForTab(tabID, event.link_id, approve, event.ed25519_public_key, event.x25519_public_key);
                if (!live()) return;
                link = null;
                const approved = result.state === "approved";
                const text = approved
                    ? t("devices.approved", { name: result.device?.name || platform })
                    : t("devices.denied");
                if (approved && copy.checked && result.device?.device_id) {
                    const name = result.device.name || platform;
                    watchHistoryCopy(result.device.device_id);
                    app().CopyHistoryToDeviceForTab(tabID, result.device.device_id, Number(range.value))
                        .then(() => v.toast(t("devices.historyCopying", { name })))
                        .catch(error => v.toast(t("devices.historyFailed", { error: error?.message || String(error) }), "warn"));
                }
                linkPane.replaceChildren(el("p", "device-link-status", text), button(t("devices.newCode"), "", createLink));
                refreshList();
            } catch (error) {
                if (live()) showError(error);
            }
        };
        actions.append(button(t("devices.deny"), "dlg-cancel", () => decide(false)), button(t("devices.approve"), "dlg-ok", () => decide(true)));
        linkPane.replaceChildren(question, compare, sas, fingerprint, history, actions);
        actions.querySelector(".dlg-ok").focus();
    };

    async function createLink() {
        decided = false;
        try {
            if (link) await app().CancelDeviceLinkForTab(tabID, link.link_id).catch(() => {});
            const created = await app().CreateDeviceLinkForTab(tabID);
            if (!live()) return;
            link = created;
            renderLink();
        } catch (error) {
            if (!live()) return;
            const message = String(error?.message || error);
            if (/not enabled the mobile web app|does not support linked devices/.test(message)) {
                linkPane.replaceChildren(el("p", "device-link-status", t("devices.unsupported")));
            } else showError(error);
        }
    }

    async function refreshList() {
        try {
            const devices = await app().DevicesForTab(tabID);
            if (!live()) return;
            renderList(devices || []);
        } catch (error) {
            if (live()) list.replaceChildren(el("li", "dlg-error", t("devices.failed", { error: error?.message || String(error) })));
        }
    }

    function renderList(devices) {
        if (!devices.length) {
            list.replaceChildren(el("li", "hint", t("devices.empty")));
            return;
        }
        list.replaceChildren(...devices.map(device => {
            const item = el("li", "device-row");
            const info = el("div", "device-row-info");
            const name = el("strong", "", device.name);
            const tags = [t(`devices.kind.${device.kind}`)];
            if (device.current) tags.push(t("devices.current"));
            if (device.first_device) tags.push(t("devices.first"));
            const meta = el("span", "device-row-meta", `${tags.join(" · ")} · ${device.fingerprint}`);
            const seen = el("span", "device-row-meta", t("devices.lastSeen", { when: relativeTime(device.last_seen_at * 1000) }));
            info.append(name, meta, seen);
            if (device.expiry_warned_at) {
                const date = new Date((device.last_seen_at + 180 * 86400) * 1000).toLocaleDateString();
                info.append(el("span", "device-row-warning", t("devices.expiryWarning", { date })));
            }
            const actions = el("div", "device-row-actions");
            if (device.needs_confirm) {
                info.append(el("span", "device-row-warning", t("devices.unconfirmed")));
                actions.append(button(t("devices.confirm"), "dlg-ok", async () => {
                    try {
                        await app().ConfirmDeviceForTab(tabID, device.device_id);
                        v.toast(t("devices.confirmed", { name: device.name }));
                    } catch (error) { v.toast(t("devices.failed", { error: error?.message || String(error) }), "warn"); }
                    if (live()) refreshList();
                }));
            }
            actions.append(button(t("devices.rename"), "", async () => {
                const next = await promptDialog({ title: t("devices.rename"), label: t("devices.renamePrompt"), value: device.name, serverScoped: true });
                if (!next || !next.trim() || !live()) return;
                try { await app().RenameDeviceForTab(tabID, device.device_id, next.trim()); }
                catch (error) { v.toast(t("devices.failed", { error: error?.message || String(error) }), "warn"); }
                if (live()) refreshList();
            }));
            if (!device.current) {
                actions.append(button(t("devices.revoke"), "danger", async () => {
                    const confirmed = await confirmDialog({
                        title: t("devices.revoke"), message: t("devices.revokeConfirm", { name: device.name }),
                        confirmLabel: t("devices.revoke"), danger: true, serverScoped: true,
                    });
                    if (!confirmed || !live()) return;
                    try {
                        await app().RevokeDeviceForTab(tabID, device.device_id);
                        v.toast(t("devices.revoked", { name: device.name }));
                    } catch (error) { v.toast(t("devices.failed", { error: error?.message || String(error) }), "warn"); }
                    if (live()) refreshList();
                }));
            }
            item.append(info, actions);
            return item;
        }));
    }

    const stopEvents = window.runtime.EventsOn("device_event", event => {
        if (!live() || !event) return;
        if (event.event === "link_claimed" && link && event.link_id === link.link_id) renderClaim(event);
        else if (event.event !== "link_claimed") refreshList();
    });

    mountServerDialog(overlay, {
        initialFocus: close,
        onClose: () => {
            stopTimer();
            if (typeof stopEvents === "function") stopEvents();
            if (link) app().CancelDeviceLinkForTab(tabID, link.link_id).catch(() => {});
        },
    });
    createLink();
    refreshList();
}
