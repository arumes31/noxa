// member-picker.js — choose a member by name instead of pasting an account ID.
//
// The pure part (buildMemberCandidates, rankMemberCandidates, candidateContext,
// looksLikeAccountID) merges what the client already knows — members online
// on this server, saved contacts ("friends"), open direct conversations and
// private-group memberships — into one candidate list, ranks it against a
// typed query and lists the facts that tell two "Alex" apart (server, short
// account ID, friend, voice channel, shared group, existing conversation).
// No server call is involved: there is no member directory API, so people who
// are offline and neither contacts nor past conversation partners can only be
// reached through the "paste ID" fallback.
//
// openMemberPicker renders that as a keyboard-navigable combobox dialog.
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { closeDialog, mountServerDialog } from "./modal.js";
import { avatarColor } from "./workspace-ui.js";
import { setSafeImage } from "./safe-media.js";
import { normalizeSearchText } from "./search-synonyms.js";
import { buildMemberCandidates, candidateContext, duplicateNames, looksLikeAccountID, rankMemberCandidates, shortAccountID } from "./member-picker-model.js";
import "./member-picker.css";

export { buildMemberCandidates, candidateContext, duplicateNames, looksLikeAccountID, rankMemberCandidates, shortAccountID };

const V = () => window.__noxa;

function factText(fact) {
    switch (fact.kind) {
        case "conversation": return t("picker.existing");
        case "friend": return t("picker.friend");
        case "channel": return t("picker.inChannel", { channel: fact.value });
        case "group": return t("picker.inGroup", { group: fact.value });
        case "server": return fact.value;
        default: return fact.value;
    }
}

function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
}

function avatar(entry) {
    const face = element("span", "avatar member-picker-avatar");
    face.setAttribute("aria-hidden", "true");
    face.style.setProperty("--avatar-color", avatarColor(entry.uid || entry.name));
    const image = V()?.state?.avatars?.get(entry.uid);
    if (!image || !setSafeImage(face, image, { avatar: true })) face.textContent = (V()?.initials?.(entry.name || "?") || String(entry.name || "?").slice(0, 1)).toUpperCase();
    if (V()?.state?.avatars && !V().state.avatars.has(entry.uid)) V().fetchAvatar?.(entry.uid);
    return face;
}

// openMemberPicker opens the dialog. `candidates` is an array or a function
// returning one; `onPick(entry)` receives a candidate, or {uid, name, manual:
// true} from the paste-ID fallback. `exclude` lists account IDs to hide (own
// ID, current group members). Returns the overlay element.
export function openMemberPicker({ title, hint, candidates, onPick, exclude = [], allowManual = true, current = () => true } = {}) {
    const list = typeof candidates === "function" ? candidates : () => candidates || [];
    const overlay = element("div", "dlg-overlay member-picker-overlay");
    const box = element("div", "dlg member-picker");
    box.setAttribute("role", "dialog");
    box.setAttribute("aria-modal", "true");
    const heading = element("h3", "member-picker-title", title || t("picker.title"));
    heading.id = `member-picker-title-${Date.now().toString(36)}`;
    box.setAttribute("aria-labelledby", heading.id);
    const help = element("p", "set-hint member-picker-hint", hint || t("picker.hint"));
    help.id = heading.id + "-hint";
    const input = element("input", "dlg-input member-picker-input");
    input.type = "text"; input.autocomplete = "off"; input.spellcheck = false;
    input.placeholder = t("picker.placeholder");
    input.setAttribute("role", "combobox");
    input.setAttribute("aria-label", t("picker.placeholder"));
    input.setAttribute("aria-autocomplete", "list");
    input.setAttribute("aria-expanded", "true");
    input.setAttribute("aria-describedby", help.id);
    const results = element("div", "member-picker-list");
    results.id = heading.id + "-list";
    results.setAttribute("role", "listbox");
    results.setAttribute("aria-label", t("picker.results"));
    input.setAttribute("aria-controls", results.id);
    const status = element("p", "sr-only"); status.setAttribute("role", "status");
    const valid = () => overlay.isConnected && current();
    const finish = entry => { if (!valid()) return; closeDialog(overlay); onPick?.(entry); };

    let selected = 0, shown = [], manual = null;
    const render = () => {
        const all = list();
        const duplicates = duplicateNames(all);
        shown = rankMemberCandidates(all, input.value, { exclude });
        selected = Math.min(selected, Math.max(0, shown.length - 1));
        results.replaceChildren();
        shown.forEach((entry, index) => {
            const row = element("div", "member-picker-row" + (index === selected ? " sel" : ""));
            row.id = `${results.id}-${index}`;
            row.setAttribute("role", "option");
            row.setAttribute("aria-selected", String(index === selected));
            row.dataset.uid = entry.uid;
            const text = element("span", "member-picker-text");
            const name = element("span", "member-picker-name", entry.name);
            const presence = element("span", "member-picker-presence"); presence.dataset.presence = entry.presence; presence.setAttribute("aria-hidden", "true");
            name.prepend(presence);
            const facts = candidateContext(entry, { sameName: duplicates.has(normalizeSearchText(entry.name)) });
            const context = element("span", "member-picker-context", facts.map(factText).join(" · "));
            text.append(name, context);
            row.append(avatar(entry), text);
            row.onmousedown = event => event.preventDefault();
            row.onclick = () => finish(entry);
            results.append(row);
        });
        if (shown.length) { input.setAttribute("aria-activedescendant", `${results.id}-${selected}`); results.querySelector(".sel")?.scrollIntoView({ block: "nearest" }); }
        else { input.removeAttribute("aria-activedescendant"); results.append(element("p", "set-hint member-picker-empty", input.value.trim() ? t("picker.empty", { query: input.value.trim() }) : t("picker.nobody"))); }
        status.textContent = shown.length ? t("picker.count", { count: shown.length }) : "";
        if (manual && input.value.trim() && looksLikeAccountID(input.value) && !shown.some(entry => entry.uid === input.value.trim())) manual.open = true;
    };
    input.oninput = () => { selected = 0; render(); };
    input.onkeydown = event => {
        event.stopPropagation();
        if (event.key === "Escape") { event.preventDefault(); closeDialog(overlay); }
        else if (event.key === "ArrowDown") { event.preventDefault(); selected = Math.min(selected + 1, Math.max(0, shown.length - 1)); render(); }
        else if (event.key === "ArrowUp") { event.preventDefault(); selected = Math.max(selected - 1, 0); render(); }
        else if (event.key === "Enter") {
            event.preventDefault();
            if (shown[selected]) finish(shown[selected]);
            else if (allowManual && looksLikeAccountID(input.value)) finish({ uid: input.value.trim(), name: "", manual: true });
        }
    };

    box.append(heading, help, input, results, status);
    if (allowManual) {
        manual = element("details", "member-picker-manual");
        const summary = element("summary", "", t("picker.advanced"));
        const form = element("form", "member-picker-manual-form");
        const idInput = element("input", "dlg-input"); idInput.type = "text"; idInput.autocomplete = "off"; idInput.spellcheck = false; idInput.maxLength = 128;
        idInput.placeholder = t("picker.idPlaceholder"); idInput.setAttribute("aria-label", t("picker.idPlaceholder"));
        const use = element("button", "ui-button", t("picker.useID")); use.type = "submit";
        form.append(idInput, use);
        form.onsubmit = event => { event.preventDefault(); const value = idInput.value.trim(); if (value) finish({ uid: value, name: "", manual: true }); };
        manual.append(summary, form);
        manual.ontoggle = () => { if (manual.open && looksLikeAccountID(input.value) && !idInput.value) idInput.value = input.value.trim(); };
        box.append(manual);
    }
    const actions = element("div", "dlg-actions member-picker-actions");
    const cancel = element("button", "ui-button", t("common.cancel")); cancel.type = "button"; cancel.onclick = () => closeDialog(overlay);
    actions.append(cancel);
    box.append(actions);
    overlay.append(box);
    overlay.onclick = event => { if (event.target === overlay) closeDialog(overlay); };
    const search = element("span", "member-picker-search-icon"); search.innerHTML = icon("search"); search.setAttribute("aria-hidden", "true");
    input.before(search);
    mountServerDialog(overlay, { initialFocus: input });
    render();
    return overlay;
}
