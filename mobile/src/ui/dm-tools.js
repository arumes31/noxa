// dm-tools.js — the direct message header menu: safety number, call
// history, export and deleting this device's copy of the conversation.
import { app, startCall } from "../app.js";
import { callsWith, clearCalls } from "../core/call-log.js";
import { Trust, exportConversation } from "../core/direct.js";
import { formatDuration } from "../core/voice-notes.js";
import { t } from "../i18n.js";
import { clock, h, sheet, toast } from "./dom.js";
import { icon } from "./icons.js";
import { downloadText, panel, safeFileName } from "./conversation-tools.js";

/** A fingerprint in groups of four for reading aloud. */
function grouped(fingerprint) {
    return String(fingerprint || "").replace(/[^0-9a-f]/gi, "").match(/.{1,4}/g)?.join(" ") || "—";
}

/**
 * The safety number sheet: both fingerprints and "Mark as verified".
 * onChange runs after the verification changed.
 */
export async function showSafetyNumber(peer, name, onChange) {
    const dm = app.dm;
    const session = app.session;
    const [contact, own] = await Promise.all([dm.devicesOf(peer), dm.devicesOf(session.uniqueID)]);
    const trusted = contact.trust === Trust.ok;
    const input = h("input", { type: "checkbox", role: "switch", checked: trusted && contact.verified, disabled: !trusted });
    input.onchange = async () => {
        input.disabled = true;
        try {
            await dm.setVerified(peer, input.checked);
            onChange?.();
        } catch (error) {
            input.checked = !input.checked;
            toast(t("common.error", { error: error.message }), "warn");
        }
        input.disabled = false;
    };
    const fingerprint = (label, value) => h("div", { class: "safety-row" },
        h("span", { class: "section-label" }, label),
        h("p", { class: "safety-number mono" }, grouped(value)));
    panel(t("safety.title"), h("div", { class: "safety" },
        h("p", { class: "hint" }, t("safety.hint", { name })),
        fingerprint(t("safety.contact", { name }), contact.fingerprint),
        fingerprint(t("safety.own"), own.fingerprint),
        trusted ? null : h("p", { class: "error" }, t(contact.trust === Trust.changed ? "safety.changed" : "safety.unavailable", { name })),
        h("label", { class: "toggle" },
            h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("safety.verify")), h("span", { class: "row-sub" }, t("safety.verifyHint"))),
            input)));
}

function callLine(record) {
    const label = t(`calls.${record.direction}.${record.outcome}`);
    const when = new Date(record.startedAt);
    return h("li", { class: "row call-record" },
        h("span", { class: `call-icon ${record.outcome}`, "aria-hidden": "true" }, icon("phone")),
        h("span", { class: "row-main" },
            h("span", { class: "row-title" }, label),
            h("span", { class: "row-sub" }, [when.toLocaleDateString(), clock(record.startedAt / 1000), record.duration ? formatDuration(record.duration) : ""].filter(Boolean).join(" · "))));
}

/** The calls with peer, newest first, with a call-back button. */
export async function showCalls(peer, name) {
    const records = [...await callsWith(peer)].reverse();
    const callBack = h("button", { class: "btn btn-primary btn-block", type: "button" }, icon("phone"), " ", t("calls.callBack", { name }));
    const view = panel(t("calls.title"), h("div", { class: "call-history" },
        callBack,
        records.length ? h("ul", { class: "list" }, records.map(callLine)) : h("p", { class: "hint" }, t("calls.empty"))));
    callBack.onclick = () => {
        view.close();
        startCall(peer).catch(error => toast(error.code ? t(`call.error.${error.code}`) : error.message, "warn"));
    };
}

/** Downloads the conversation as a text file. */
export function exportDM(peer, name) {
    const conversation = app.dm.conversations.get(peer);
    if (!conversation?.messages.length) {
        toast(t("dmTools.exportEmpty"));
        return;
    }
    const text = exportConversation(conversation, { selfName: app.session.nickname, peerName: name, format: date => date.toLocaleString() });
    downloadText(`noxa-${safeFileName(name)}.txt`, text);
}

/** Deletes this device's copy of the conversation and its calls after confirming. */
export async function deleteLocalHistory(peer, name) {
    const sure = await sheet(t("dmTools.deleteTitle"), t("dmTools.deleteBody", { name }), [
        { label: t("link.cancel"), value: false },
        { label: t("dmTools.deleteConfirm"), value: true, danger: true },
    ]);
    if (!sure) return false;
    await app.dm.deleteHistory(peer);
    await clearCalls(peer);
    toast(t("dmTools.deleted"));
    return true;
}
