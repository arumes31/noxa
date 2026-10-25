// contact-key-change-ui.js — the dialog shown when a contact's signing key
// changed (item 241). The Go side blocks the contact and waits for
// AnswerContactIdentityChange; closing the dialog any other way keeps the
// safe default (verify later, stay blocked).
import { t } from "./i18n.js";
import { closeDialog, mountServerDialog } from "./modal.js";
import { differingGroups, groupFingerprint, FINGERPRINT_GROUPS_PER_LINE } from "./contact-key-change.js";
import "./contact-key-change.css";

const app = () => window.go?.main?.App;

function el(tag, className = "", text) {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (text !== undefined) element.textContent = text;
    return element;
}

function button(label, className) {
    const element = el("button", className, label);
    element.type = "button";
    return element;
}

// fingerprintBlock renders a fingerprint as 4-character groups, eight per
// row. Changed groups carry a visible marker and screen-reader text, so the
// difference never depends on color alone.
function fingerprintBlock(labelKey, value, marks = []) {
    const wrap = el("div", "key-change-fingerprint");
    const label = el("span", "dlg-label", t(labelKey));
    const output = el("output", "key-change-fingerprint-value");
    output.setAttribute("aria-label", t(labelKey));
    const groups = groupFingerprint(value);
    groups.forEach((group, index) => {
        const span = el("span", "key-change-group", group);
        if (marks[index]) {
            span.classList.add("key-change-group-changed");
            span.title = t("keyChange.changedBlock");
            span.append(el("span", "sr-only", " (" + t("keyChange.changedBlock") + ")"));
        }
        output.append(span);
        if ((index + 1) % FINGERPRINT_GROUPS_PER_LINE === 0 && index < groups.length - 1) output.append(el("br"));
    });
    if (!groups.length) output.textContent = t("keyChange.unknownFingerprint");
    wrap.append(label, output);
    return wrap;
}

export function openContactKeyChangeDialog(event = {}) {
    const requestID = String(event?.request_id || "");
    if (!requestID) return null;
    const contact = String(event.contact || "");
    const server = String(event.server || "");
    const name = window.__noxa?.state?.clients?.find(client => client.unique_id === contact)?.nickname || contact;
    let answered = false;
    const answer = async (trust) => {
        if (answered) return;
        answered = true;
        try {
            const error = await app()?.AnswerContactIdentityChange?.(requestID, trust);
            if (error && trust) window.__noxa?.toast?.(t("keyChange.expired"), "warn");
        } catch { /* the backend already fell back to blocking */ }
    };

    const overlay = el("div", "dlg-overlay");
    const dialog = el("div", "dlg key-change-dlg");
    const title = el("h3", "", t("keyChange.title", { name }));
    const what = el("p", "dlg-text", t("keyChange.what", { name, server }));
    const benign = el("p", "dlg-text", t("keyChange.benign", { name }));
    const risky = el("p", "set-hint warn key-change-risky", t("keyChange.risky", { name }));
    const todo = el("p", "dlg-text", t("keyChange.todo", { name }));
    const fingerprints = el("div", "key-change-fingerprints");
    fingerprints.append(
        fingerprintBlock("keyChange.oldFingerprint", event.old_fingerprint),
        fingerprintBlock("keyChange.newFingerprint", event.new_fingerprint, differingGroups(event.old_fingerprint, event.new_fingerprint)),
    );
    const legend = el("p", "key-change-legend", t("keyChange.legend"));
    const laterHelp = el("p", "key-change-help", t("keyChange.laterHelp", { name }));
    const buttons = el("div", "dlg-buttons key-change-buttons");
    // The safe choice is the primary, default-focused action; trusting the
    // new key is deliberately secondary and never focused first.
    const later = button(t("keyChange.later"), "dlg-ok key-change-later");
    later.onclick = () => { void answer(false); closeDialog(overlay, "cancel"); };
    const trust = button(t("keyChange.trust"), "danger-btn key-change-trust");
    trust.onclick = () => { void answer(true); closeDialog(overlay, "close"); };
    buttons.append(trust, later);
    dialog.append(title, what, benign, risky, todo, fingerprints, legend, laterHelp, buttons);
    overlay.append(dialog);
    overlay.onclick = (clickEvent) => { if (clickEvent.target === overlay) closeDialog(overlay, "cancel"); };
    mountServerDialog(overlay, { initialFocus: later, onClose: () => { void answer(false); } });
    return overlay;
}
