// account.js — account and server pieces shared by several screens: form
// sheets, friendly sign-in errors, the Connect screen's extra sign-in paths
// (sign in again, restore from backup, a pending invitation), the server
// rules gate, channel invitations and channel passwords.
import "./account-strings.js";
import "./account.css";
import { app, connect, disconnect, forgetDevice, rememberLinkedIdentity } from "../app.js";
import { BackupError, importBackup } from "../core/backup.js";
import { isForThisServer, parseInviteLink, pendingInvite, resolveInvite, setPendingInvite, webInviteURL } from "../core/invite.js";
import { classifySignIn } from "../core/login-errors.js";
import { get as setting, set as setSettings } from "../core/settings.js";
import { t } from "../i18n.js";
import { h, sheet, toast } from "./dom.js";

let sheetCount = 0;

/**
 * A bottom sheet with a form. fields: [{name, label, type, value, hint,
 * autocomplete, accept, options: [{value, label}]}]. validate(values)
 * returns an error message or "". Resolves with the values or null.
 */
export function formSheet({ title, body = "", fields = [], submit, danger = false, validate = () => "" }) {
    return new Promise(resolve => {
        const id = `form-sheet-${++sheetCount}`;
        const error = h("p", { class: "error", role: "alert" });
        const inputs = new Map();
        const rows = fields.map((field, index) => {
            const inputID = `${id}-${index}`;
            let input;
            if (field.options) {
                input = h("div", { class: "acct-choices", role: "radiogroup", "aria-label": field.label },
                    field.options.map((option, i) => h("label", { class: "acct-choice" },
                        h("input", { type: "radio", name: `${id}-${field.name}`, value: option.value, checked: (field.value ?? field.options[0].value) === option.value || (field.value === undefined && i === 0) }),
                        h("span", {}, option.label))));
                inputs.set(field.name, () => input.querySelector("input:checked")?.value ?? "");
                return input;
            }
            input = h("input", {
                class: "input", id: inputID, type: field.type || "text", inputmode: field.inputmode, placeholder: field.placeholder, value: field.value ?? "", autocomplete: field.autocomplete || "off",
                accept: field.accept, required: field.required === false ? null : "", spellcheck: "false", autocapitalize: "off",
                maxlength: field.maxlength, "aria-describedby": field.hint ? `${inputID}-hint` : null,
            });
            inputs.set(field.name, () => field.type === "file" ? input.files?.[0] || null : input.value);
            return h("div", { class: "field" }, h("label", { for: inputID }, field.label), input,
                field.hint ? h("p", { class: "hint", id: `${inputID}-hint` }, field.hint) : null);
        });
        const close = value => { overlay.remove(); document.removeEventListener("keydown", onKey); resolve(value); };
        const onKey = event => { if (event.key === "Escape") close(null); };
        const submitButton = h("button", { class: danger ? "btn btn-danger" : "btn btn-primary", type: "submit" }, submit);
        const form = h("form", { class: "acct-form", onsubmit: async event => {
            event.preventDefault();
            const values = {};
            for (const [name, read] of inputs) values[name] = read();
            const problem = await validate(values);
            if (problem) {
                error.textContent = problem;
                return;
            }
            close(values);
        } }, rows, error,
        h("div", { class: "sheet-actions" }, submitButton, h("button", { class: "btn", type: "button", onclick: () => close(null) }, t("acct.cancel"))));
        const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(null); } },
            h("div", { class: "sheet", role: "dialog", "aria-modal": "true", "aria-labelledby": `${id}-title` },
                h("h2", { class: "sheet-title", id: `${id}-title` }, title),
                body ? h("p", { class: "sheet-body" }, body) : null,
                form));
        document.body.append(overlay);
        document.addEventListener("keydown", onKey);
        (overlay.querySelector("input:not([type=radio]), input:checked") || submitButton).focus();
    });
}

/** A translated explanation of a sign-in failure. */
export function signInMessage(error) {
    const failure = classifySignIn(error);
    if (failure.kind === "unknown") return t("acct.fail.unknown", { error: error?.message || String(error) });
    return t(`acct.fail.${failure.kind}`, failure);
}

// --- Connect screen ------------------------------------------------------------

async function restoreFromBackup(values, { onSignedIn, error }) {
    const text = await values.file.text();
    let restored;
    try {
        restored = await importBackup(text, values.passphrase);
    } catch (err) {
        error.textContent = err instanceof BackupError ? t(err.code === "passphrase" ? "acct.backupWrongPass" : "acct.backupBadFile") : t("common.error", { error: err.message });
        return;
    }
    if (restored.server && restored.server !== location.origin) {
        const ok = await sheet(t("acct.restore"), t("acct.restoreOtherServer", { server: restored.server }), [
            { label: t("acct.restoreAnyway"), value: true, primary: true }, { label: t("acct.cancel"), value: false }]);
        if (!ok) return;
    }
    await rememberLinkedIdentity(restored.identity, restored.meta);
    try {
        await connect();
        onSignedIn({ offerPasskey: false });
    } catch (err) {
        // A device that no longer exists cannot come back; forget it again.
        if (!err.revoked) await forgetDevice().catch(() => {});
        error.textContent = signInMessage(err);
    }
}

/**
 * Extra parts of the Connect screen: sign in again with the stored device,
 * restore a backed-up device and a waiting invitation.
 */
export function connectExtras({ onSignedIn, error }) {
    const parts = [];
    if (pendingInvite()) parts.push(h("p", { class: "card acct-invite-note", role: "status" }, t("acct.inviteSignIn")));
    if (app.identity && app.meta.linked) {
        const again = h("button", { class: "btn btn-primary btn-block", type: "button", onclick: async () => {
            again.disabled = true;
            error.textContent = "";
            try {
                await connect();
                onSignedIn({ offerPasskey: false });
            } catch (err) {
                error.textContent = signInMessage(err);
            } finally { again.disabled = false; }
        } }, t("acct.signInAgain"));
        parts.push(h("div", { class: "acct-again" }, again, h("p", { class: "hint" }, t("acct.signInAgainHint"))));
    }
    return parts;
}

/** The "Restore from backup" section of the Connect card. */
export function restoreSection({ onSignedIn, error }) {
    const file = h("input", { class: "input acct-file", id: "restore-file", type: "file", accept: ".json,application/json", required: "" });
    const passphrase = h("input", { class: "input", id: "restore-passphrase", type: "password", autocomplete: "current-password", required: "" });
    const button = h("button", { class: "btn btn-block", type: "submit" }, t("acct.restoreAction"));
    return h("details", { class: "connect-guest acct-restore" },
        h("summary", {}, t("acct.restore")),
        h("form", { class: "connect-link", onsubmit: async event => {
            event.preventDefault();
            if (!file.files?.[0]) return;
            button.disabled = true;
            error.textContent = "";
            try {
                await restoreFromBackup({ file: file.files[0], passphrase: passphrase.value }, { onSignedIn, error });
            } finally { button.disabled = false; }
        } },
        h("p", { class: "hint" }, t("acct.restoreHint")),
        h("div", { class: "field" }, h("label", { for: "restore-file" }, t("acct.backupFile")), file),
        h("div", { class: "field" }, h("label", { for: "restore-passphrase" }, t("acct.passphrase")), passphrase),
        button));
}

// --- Server rules --------------------------------------------------------------

let rulesOverlay = null;

function closeRules() {
    rulesOverlay?.remove();
    rulesOverlay = null;
}

/** Shows the rules sheet for the session's pending rules, or closes it. */
function showRules(session) {
    const rules = session.pendingRules;
    if (!rules || app.session !== session) return closeRules();
    const text = h("div", { class: "acct-rules-text", tabindex: "0", role: "document", "aria-label": t("acct.rulesTitle") }, rules.text);
    const status = h("p", { class: "error", role: "alert" });
    const accept = h("button", { class: "btn btn-primary", type: "button", onclick: async () => {
        accept.disabled = true;
        try {
            if (await session.acceptRules(rules.hash)) closeRules();
        } catch (error) {
            // A changed text arrives as a new "rules" event and re-renders.
            if (session.pendingRules?.hash !== rules.hash) toast(t("acct.rulesChanged"), "warn");
            else status.textContent = t("common.error", { error: error.message });
        } finally { accept.disabled = false; }
    } }, t("acct.rulesAccept"));
    const decline = h("button", { class: "btn btn-danger", type: "button", onclick: async () => {
        closeRules();
        if (app.meta.guest) await forgetDevice();
        else disconnect();
        toast(t("acct.rulesDeclined"), "warn");
    } }, t("acct.rulesDecline"));
    const content = h("div", { class: "sheet acct-rules", role: "alertdialog", "aria-modal": "true", "aria-labelledby": "rules-title", "aria-describedby": "rules-hint" },
        h("h2", { class: "sheet-title", id: "rules-title" }, t("acct.rulesTitle")),
        h("p", { class: "sheet-body", id: "rules-hint" }, t("acct.rulesHint")),
        text, status,
        h("div", { class: "sheet-actions" }, accept, decline));
    if (rulesOverlay) rulesOverlay.replaceChildren(content);
    else {
        rulesOverlay = h("div", { class: "sheet-overlay acct-rules-overlay" }, content);
        document.body.append(rulesOverlay);
    }
    accept.focus();
}

/** Follows a session's rules gate; call once per session. */
export function watchRules(session) {
    session.addEventListener("rules", () => showRules(session));
    if (session.pendingRules) showRules(session);
    else closeRules();
}

/** Resolves once the session may use channels (rules accepted). */
function rulesSettled(session) {
    if (!session.pendingRules) return Promise.resolve();
    return new Promise(resolve => {
        const check = () => {
            if (session.pendingRules) return;
            session.removeEventListener("rules", check);
            resolve();
        };
        session.addEventListener("rules", check);
    });
}

// --- Invitations ---------------------------------------------------------------

/** Redeems an invitation for this server and opens the channel's chat. */
export async function redeemInvite(session, invite) {
    await rulesSettled(session);
    try {
        await resolveInvite(session.conn, invite);
    } catch (error) {
        // Permission denied (4) and unknown tokens both read as an invalid invitation.
        toast(error?.code === 4 || /invitation/i.test(error?.message || "") ? t("acct.inviteInvalid") : t("common.error", { error: error.message }), "warn");
        return false;
    }
    const unsubscribed = setting("unsubscribedChannels") || [];
    if (unsubscribed.includes(invite.channelID)) setSettings({ unsubscribedChannels: unsubscribed.filter(id => id !== invite.channelID) });
    const channel = session.channels().find(c => c.id === invite.channelID);
    if (channel?.chat && !session.subscriptions.has(channel.id)) await session.subscribe([channel.id]).catch(() => {});
    toast(t("acct.inviteJoined", { name: channel?.name || invite.channelID }));
    if (app.session === session) location.hash = channel?.chat ? `#/chat/${channel.id}` : "#/servers";
    return true;
}

/** Redeems an invitation that waited for sign-in. */
export async function redeemPendingInvite(session) {
    const invite = pendingInvite();
    if (!invite) return;
    setPendingInvite(null);
    await redeemInvite(session, invite);
}

/** Asks for an invitation link and joins it (or opens its server). */
export async function pasteInvite(session) {
    const values = await formSheet({
        title: t("acct.invitePaste"), submit: t("acct.join"),
        fields: [{ name: "link", label: t("acct.inviteLabel"), inputmode: "url", autocomplete: "off" }],
        validate: values => parseInviteLink(values.link) ? "" : t("acct.inviteBad"),
    });
    if (!values) return;
    const invite = parseInviteLink(values.link);
    if (isForThisServer(invite)) return redeemInvite(session, invite);
    const host = invite.web ? invite.host : invite.hostname;
    const open = await sheet(t("acct.invitePaste"), t("acct.inviteOther", { host }), [{ label: t("acct.open"), value: true, primary: true }, { label: t("acct.cancel"), value: false }]);
    if (open) location.assign(webInviteURL(invite));
}

// --- Channel passwords ---------------------------------------------------------

/** The password for a protected channel: "" when it has none, null when cancelled. */
export async function channelPassword(channel) {
    if (!channel?.hasPassword) return "";
    const values = await formSheet({
        title: t("acct.passwordTitle"), body: t("acct.passwordHint", { name: channel.name }), submit: t("acct.join"),
        fields: [{ name: "password", label: t("acct.password"), type: "password", autocomplete: "off" }],
        validate: values => values.password ? "" : t("acct.password"),
    });
    return values ? values.password : null;
}

// --- Session start -------------------------------------------------------------

/**
 * Devices of this account linked while the app runs, offered on Home for
 * confirmation and history copy: device_id -> DeviceInfo.
 */
export const deviceOffers = new Map();

function offersChanged() {
    app.dispatchEvent(new CustomEvent("account"));
}

export function dismissOffer(deviceID) {
    if (deviceOffers.delete(deviceID)) offersChanged();
}

/** Starts the account features of a new session (rules, invitation, device offers). */
export function startSession(session) {
    watchRules(session);
    redeemPendingInvite(session).catch(() => {});
    session.addEventListener("device", event => {
        const { event: kind, device } = event.detail || {};
        if (!device?.device_id || device.device_id === session.deviceID || app.session !== session) return;
        if (kind === "linked") {
            deviceOffers.set(device.device_id, device);
            offersChanged();
        } else if (kind === "endorsed" && deviceOffers.has(device.device_id)) {
            deviceOffers.set(device.device_id, { ...deviceOffers.get(device.device_id), ...device });
            offersChanged();
        } else if (kind === "revoked") {
            dismissOffer(device.device_id);
        }
    });
}
