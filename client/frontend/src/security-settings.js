import { currentLanguage, t } from "./i18n.js";
import { canFocusTarget, confirmDialog, promptDialog } from "./modal.js";
import { copyToClipboard } from "./clipboard.js";
import "./security-settings.css";

const el = (tag, text = "", cls = "") => {
    const node = document.createElement(tag);
    node.textContent = text;
    node.className = cls;
    return node;
};
const button = (key, action, cls = "") => {
    const node = el("button", t(key), cls);
    node.type = "button";
    node.onclick = action;
    return node;
};
const details = key => {
    const node = el("details", "", "security-details");
    node.append(el("summary", t(key)));
    return node;
};
const checkError = async promise => { const error = await promise; if (error) throw new Error(error); };
const exportDate = value => {
    const date = new Date(value.replace(" ", "T"));
    return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(currentLanguage(), { dateStyle: "medium", timeStyle: "short" }).format(date);
};

export function securitySettings(settings) {
    const app = window.go.main.App;
    const page = el("div", "", "security-page");
    const status = el("p", "", "security-status"); status.setAttribute("role", "status");
    const error = el("p", "", "security-error"); error.setAttribute("role", "alert");
    const identities = el("fieldset", "", "security-identities");
    identities.append(el("legend", t("security.identities")));
    const cards = el("div", "", "security-cards");
    const progressArea = el("div", "", "security-progress"); progressArea.hidden = true;
    const progressLabel = el("p"); progressLabel.setAttribute("role", "status");
    const progress = el("progress"); progress.setAttribute("aria-label", t("security.calculate"));
    const stop = button("security.cancelCalculation", async () => {
        stop.disabled = true;
        try { await app.CancelIdentityLevel(calculatingID); }
        catch (failure) { if (current()) error.textContent = t("security.failed", { error: String(failure) }); }
        finally { if (current()) stop.disabled = false; }
    });
    progressArea.append(progressLabel, progress, stop);
    let entries = [], busy = false, calculatingID = "", timer = null, disposed = false;
    const current = () => !disposed && page.isConnected;
    const setBusy = value => {
        busy = value; identities.disabled = value;
        const overlay = page.closest("#settings-overlay");
        if (!overlay) return;
        overlay.dataset.identityBusy = String(value);
        for (const node of overlay.querySelectorAll(".settings-footer button,#settings-search")) node.disabled = value;
        overlay.querySelector(".settings-nav").inert = value;
        preference.disabled = plaintext.disabled = value;
    };
    page.dispose = () => {
        disposed = true;
        clearInterval(timer);
        if (calculatingID) void app.CancelIdentityLevel(calculatingID).catch(() => {});
    };
    const load = async () => {
        const list = await app.ListIdentities();
        if (!current()) return;
        entries = list || [];
        renderCards();
        storageWarning.hidden = !entries.some(entry => !entry.error && entry.protection !== "dpapi");
    };
    const run = async (action, message) => {
        if (busy || !current()) return;
        const focused = document.activeElement;
        const focusIdentity = focused.closest(".identity-card")?.dataset.identityId;
        setBusy(true); error.textContent = ""; status.textContent = t("security.working");
        let completed = false;
        try {
            const changed = await action();
            completed = true;
            if (!current()) return;
            await load();
            if (current()) status.textContent = changed === false ? t("security.cancelled") : typeof message === "function" ? message() : t(message);
        } catch (failure) {
            if (current()) {
                status.textContent = "";
                error.textContent = completed ? t("security.reloadFailed") : t("security.failed", { error: failure.message || String(failure) });
            }
        } finally {
            if (current()) {
                setBusy(false);
                if (!focused.isConnected) {
                    const card = [...cards.children].find(node => node.dataset.identityId === focusIdentity);
                    const replacement = card && [...card.querySelectorAll("button,summary")].find(node => node.textContent === focused.textContent && canFocusTarget(node));
                    (replacement || card?.querySelector("h4") || create).focus();
                }
            }
        }
    };
    const ask = (title, message, confirmLabel, danger = false) => confirmDialog({ title: t(title), message,
        confirmLabel: t(confirmLabel), cancelLabel: t("common.cancel"), danger });
    const rename = async entry => {
        const name = await promptDialog({ title: t("security.rename"), message: t("security.renameHelp"), label: t("security.label"),
            value: entry.name, confirmLabel: t("common.save"), cancelLabel: t("common.cancel") });
        if (name === null || !current()) return;
        await run(() => checkError(app.RenameIdentity(entry.id, name.trim())), "security.renamed");
    };
    const calculate = async (entry, input) => {
        if (!input.reportValidity() || busy) return;
        const target = Number(input.value);
        let result;
        await run(async () => {
            calculatingID = entry.id;
            const start = Date.now();
            const update = () => { progressLabel.textContent = t("security.calculating", { name: entry.name, seconds: Math.floor((Date.now() - start) / 1000) }); };
            update(); timer = setInterval(update, 1000); progressArea.hidden = false;
            progressArea.scrollIntoView({ block: "nearest" });
            try {
                result = await app.ImproveIdentityLevel(entry.id, target, 30);
                if (result.error) throw new Error(result.error);
            } finally { calculatingID = ""; clearInterval(timer); progressArea.hidden = true; }
        }, () => t(result.cancelled ? "security.levelStopped" : result.level < target ? "security.levelLimited" : "security.levelDone", { level: result.level, target }));
    };
    const renderCards = () => {
        cards.replaceChildren();
        for (const entry of [...entries].sort((a, b) => Number(b.active) - Number(a.active))) {
            const card = el("section", "", "identity-card");
            card.dataset.identityId = entry.id;
            card.classList.toggle("identity-selected", entry.active);
            card.setAttribute("aria-label", entry.name);
            const heading = el("div", "", "identity-heading");
            const title = el("div"); title.append(el("h4", entry.name));
            title.querySelector("h4").tabIndex = -1;
            if (entry.active) title.append(el("span", t("security.selected"), "identity-selected-label"));
            const more = el("details", "", "identity-more");
            const summary = el("summary", "⋯"); summary.setAttribute("aria-label", t("security.more", { name: entry.name }));
            more.append(summary, button("security.rename", () => { more.open = false; void rename(entry); }), button("security.copy", async () => {
                more.open = false;
                const copied = await copyToClipboard(entry.unique_id, { isCurrent: () => false });
                if (current()) { status.textContent = copied ? t("security.copied") : ""; error.textContent = copied ? "" : t("clipboard.failed"); }
            }));
            if (entry.error) more.querySelector("button").disabled = true;
            more.querySelector("button:last-child").disabled = !entry.unique_id;
            heading.append(title, more); card.append(heading);
            card.append(el("p", t(entry.protection === "dpapi" ? "security.protected" : "security.unprotected"), "identity-protection"));
            card.append(el("p", entry.exported_at ? t("security.exported", { date: exportDate(entry.exported_at) }) : t("security.never"), entry.exported_at ? "identity-exported" : "identity-exported warn"));
            if (entry.error) card.append(el("p", t("security.unreadable"), "warn"));
            const actions = el("div", "", "security-actions");
            const backup = button("security.backup", async () => {
                if (await ask("security.backup", t("security.backupWarning"), "security.backup") && current()) await run(() => app.BackupIdentity(entry.id), "security.backedUp");
            }, "identity-backup");
            backup.disabled = !!entry.error;
            actions.append(backup);
            if (!entry.active) {
                const use = button("security.use", () => run(() => checkError(app.SwitchIdentity(entry.id)), "security.switched"));
                use.disabled = !!entry.error; actions.append(use);
            }
            card.append(actions);
            const advanced = details("security.advanced");
            advanced.append(el("p", entry.unique_id, "identity-uid"));
            advanced.append(el("h5", `${t("security.level")}: ${entry.security_level || 0}`), el("p", t("security.levelHelp")));
            const levelRow = el("div", "", "security-actions");
            const label = el("label", t("security.target"));
            const target = el("input"); target.type = "number"; target.min = "1"; target.max = "40"; target.step = "1"; target.required = true;
            target.value = String(Math.min(40, (entry.security_level || 0) + 4)); target.disabled = !!entry.error;
            label.append(target);
            const start = button("security.calculate", () => calculate(entry, target)); start.disabled = !!entry.error;
            levelRow.append(label, start); advanced.append(levelRow);
            const danger = details("security.danger");
            const reset = button("security.reset", async () => {
                if (await ask("security.reset", t("security.resetAsk", { name: entry.name }), "security.resetConfirm", true) && current()) {
                    await run(() => app.ResetIdentity(entry.id, entry.unique_id), "security.resetDone");
                }
            }, "danger-btn");
            const remove = button("security.delete", async () => {
                if (await ask("security.delete", t("security.deleteAsk", { name: entry.name }), "security.deleteConfirm", true) && current()) {
                    await run(() => checkError(app.DeleteIdentity(entry.id, true)), "security.deleted");
                }
            }, "danger-btn");
            reset.disabled = !!entry.error; remove.disabled = !!entry.error || entries.length < 2;
            const dangerousActions = el("div", "", "security-actions"); dangerousActions.append(reset, remove); danger.append(dangerousActions);
            if (entries.length < 2) danger.append(el("p", t("security.lastIdentity")));
            advanced.append(danger); card.append(advanced); cards.append(card);
        }
        if (!entries.length) cards.append(el("p", t("security.empty")));
    };
    const create = button("security.create", async () => {
        const name = await promptDialog({ title: t("security.create"), label: t("security.label"), message: t("security.renameHelp"),
            confirmLabel: t("common.ok"), cancelLabel: t("common.cancel") });
        if (name !== null && current()) await run(() => checkError(app.CreateIdentity(name.trim())), "security.created");
    });
    const importBackup = button("security.import", () => run(() => app.RestoreIdentity(), "security.imported"));
    const identityActions = el("div", "", "security-actions"); identityActions.append(create, importBackup);
    identities.append(el("p", t("security.intro")), el("p", t("security.immediate"), "security-timing"), cards, identityActions, el("p", t("security.backupHelp")));

    const storage = el("section", "", "security-section"); storage.append(el("h4", t("security.storage")));
    const prefLabel = el("label", t("security.storageLabel"));
    const preference = el("select");
    preference.setAttribute("aria-label", t("security.storageLabel"));
    for (const [value, key] of [["auto", "security.auto"], ["off", "security.off"]]) { const option = el("option", t(key)); option.value = value; preference.append(option); }
    preference.value = settings.identity_key_protection === "off" ? "off" : "auto";
    const pending = el("p", t("security.storagePending"), "security-timing");
    const updatePending = () => { pending.hidden = preference.value === (window.__noxa.state.settings.identity_key_protection === "off" ? "off" : "auto"); };
    preference.onchange = () => { settings.identity_key_protection = preference.value; updatePending(); };
    updatePending(); prefLabel.append(preference);
    const storageWarning = el("p", t("security.storageWarning"), "warn"); storageWarning.hidden = true;
    const learn = details("security.learn"); learn.append(el("p", t("security.storageDetails")));
    storage.append(prefLabel, el("p", t("security.storageHelp")), pending, storageWarning, learn);
    const connection = el("section", "", "security-section"); connection.append(el("h4", t("security.connection")), el("p", t("security.connectionHelp")));
    const developer = details("security.developer");
    const plainLabel = el("label", "", "security-checkbox");
    const plaintext = el("input"); plaintext.type = "checkbox"; plaintext.checked = !!settings.allow_plaintext;
    const plainWarning = el("p", t("security.plaintextWarning"), "warn"); plainWarning.hidden = !plaintext.checked;
    plaintext.onchange = () => { settings.allow_plaintext = plaintext.checked; plainWarning.hidden = !plaintext.checked; };
    plainLabel.append(plaintext, document.createTextNode(t("security.plaintext")));
    developer.append(plainLabel, el("p", t("settings.timing.saved"))); connection.append(plainWarning, developer);
    page.append(status, error, identities, progressArea, storage, connection);
    // Settings search builds detached pages: only load secrets for the visible page.
    setTimeout(async () => {
        if (!current()) return;
        setBusy(true); status.textContent = t("security.loading");
        try { await load(); if (current()) status.textContent = ""; }
        catch (failure) { if (current()) { status.textContent = ""; error.textContent = t("security.failed", { error: String(failure) }); } }
        finally { if (current()) setBusy(false); }
    }, 0);
    return page;
}
