// admin-kit.js — small building blocks the Admin screens and the member
// sheet share: form fields, sheets with inputs, choosers, image picking,
// copying and sharing. Text is set as textContent only.
import { addStrings, t } from "../i18n.js";
import { bytesToBase64, MAX_IMAGE_BYTES } from "../core/admin.js";
import { h, toast } from "./dom.js";
import { header, nav } from "./common.js";
import "./admin.css";

addStrings({
    en: {
        "admin.cancel": "Cancel",
        "admin.save": "Save",
        "admin.saved": "Saved",
        "admin.delete": "Delete",
        "admin.loading": "Loading…",
        "admin.none": "Nothing here yet.",
        "admin.copy": "Copy",
        "admin.copied": "Copied",
        "admin.share": "Share",
        "admin.refresh": "Refresh",
        "admin.imageTooLarge": "The image is larger than {size} KiB.",
        "admin.imageType": "Choose a {types} image.",
        "admin.chooseImage": "Choose image",
        "admin.failed": "{error}",
        "admin.unavailable": "You cannot manage this here, or the server does not support it.",
    },
    de: {
        "admin.cancel": "Abbrechen",
        "admin.save": "Speichern",
        "admin.saved": "Gespeichert",
        "admin.delete": "Löschen",
        "admin.loading": "Lädt…",
        "admin.none": "Noch nichts vorhanden.",
        "admin.copy": "Kopieren",
        "admin.copied": "Kopiert",
        "admin.share": "Teilen",
        "admin.refresh": "Aktualisieren",
        "admin.imageTooLarge": "Das Bild ist größer als {size} KiB.",
        "admin.imageType": "Wähle ein Bild im Format {types}.",
        "admin.chooseImage": "Bild wählen",
        "admin.failed": "{error}",
        "admin.unavailable": "Du kannst das hier nicht verwalten, oder der Server unterstützt es nicht.",
    },
});

let ids = 0;
const nextID = prefix => `${prefix}-${++ids}`;

/** Shows a request failure; the server's message says why. */
export function fail(error) {
    toast(t("common.error", { error: error?.message || String(error) }), "warn");
}

/** An admin screen: header, scrolling body and the main navigation. */
export function adminScreen(root, title, { back = "#/admin", sub, action } = {}, ...body) {
    const content = h("div", { class: "screen-body admin-body" }, ...body);
    root.replaceChildren(h("section", { class: "screen admin" }, header(title, { back, sub, action }), content, nav("you")));
    return content;
}

/** A labelled control (input, select or textarea); hint is optional. */
export function field(label, control, hint) {
    const id = control.id || (control.id = nextID("admin-field"));
    const hintNode = hint ? h("p", { class: "hint", id: `${id}-hint` }, hint) : null;
    if (hintNode) control.setAttribute("aria-describedby", hintNode.id);
    return h("div", { class: "field admin-field" }, h("label", { for: id }, label), control, hintNode);
}

export function input(props = {}) {
    return h("input", { class: "input", type: "text", ...props });
}

export function textarea(props = {}) {
    return h("textarea", { class: "input admin-textarea", rows: "4", ...props });
}

/** options: [{value, label, disabled}] */
export function select(options, value, props = {}) {
    const node = h("select", { class: "input admin-select", ...props },
        options.map(o => h("option", { value: String(o.value), disabled: !!o.disabled }, o.label)));
    node.value = String(value);
    return node;
}

/** A switch row; onchange receives the new checked state. */
export function toggle(label, checked, onchange, { hint, disabled } = {}) {
    const control = h("input", { type: "checkbox", role: "switch", checked: !!checked, disabled: !!disabled });
    control.onchange = () => onchange?.(control.checked, control);
    return h("label", { class: "toggle" }, h("span", { class: "row-main" }, h("span", { class: "row-title admin-wrap" }, label), hint ? h("span", { class: "row-sub admin-wrap" }, hint) : null), control);
}

/** A navigation row of the admin lists. */
export function linkRow(href, title, sub, { iconNode, meta } = {}) {
    return h("li", {}, h("a", { class: "row admin-link", href }, iconNode || null,
        h("span", { class: "row-main" }, h("span", { class: "row-title" }, title), sub ? h("span", { class: "row-sub" }, sub) : null),
        meta ? h("span", { class: "row-meta" }, meta) : null, h("span", { class: "admin-chevron", "aria-hidden": "true" }, "›")));
}

/** Runs an action with the button disabled; failures are shown. Resolves with the result or undefined. */
export async function busy(button, action) {
    if (button?.disabled) return undefined;
    if (button) { button.disabled = true; button.setAttribute("aria-busy", "true"); }
    try {
        return await action();
    } catch (error) {
        fail(error);
        return undefined;
    } finally {
        if (button) { button.disabled = false; button.removeAttribute("aria-busy"); }
    }
}

/**
 * A bottom sheet with arbitrary content and buttons [{label, value, primary,
 * danger}]. Resolves with the pressed button's value (null when dismissed).
 * validate(value) may return false to keep the sheet open.
 */
export function formSheet(title, content, buttons, { validate, mount } = {}) {
    return new Promise(resolve => {
        const previous = document.activeElement;
        const close = value => {
            overlay.remove();
            document.removeEventListener("keydown", onKey);
            previous?.focus?.();
            resolve(value);
        };
        const onKey = event => { if (event.key === "Escape" && overlay.isConnected && overlay === [...document.querySelectorAll(".sheet-overlay")].at(-1)) close(null); };
        const titleID = nextID("admin-sheet");
        const form = h("form", { class: "sheet admin-sheet", role: "dialog", "aria-modal": "true", "aria-labelledby": titleID, onsubmit: event => {
            event.preventDefault();
            const primary = buttons.find(b => b.primary);
            if (primary && (!validate || validate(primary.value) !== false)) close(primary.value);
        } },
        h("h2", { class: "sheet-title", id: titleID }, title),
        content,
        h("div", { class: "sheet-actions" }, buttons.map(b => h("button", {
            class: b.primary ? "btn btn-primary" : b.danger ? "btn btn-danger" : "btn",
            type: b.primary ? "submit" : "button",
            onclick: b.primary ? null : () => { if (!validate || b.value === null || validate(b.value) !== false) close(b.value); },
        }, b.label))));
        const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(null); } }, form);
        document.body.append(overlay);
        document.addEventListener("keydown", onKey);
        mount?.(close);
        (form.querySelector("input:not([type=checkbox]), textarea, select") || form.querySelector(".btn-primary, .btn-danger, .btn"))?.focus();
    });
}

/** Asks for confirmation; resolves true or false. */
export async function confirmSheet(title, body, label, { danger = true } = {}) {
    const value = await formSheet(title, body ? h("p", { class: "sheet-body admin-wrap" }, body) : null, [
        { label, value: true, primary: !danger, danger },
        { label: t("admin.cancel"), value: false },
    ]);
    if (danger && value === null) return false;
    return !!value;
}

/** Asks for one line of text; resolves the trimmed text or null. */
export async function textSheet(title, label, { value = "", hint, confirm, danger = false, required = false, maxLength = 500, multiline = false } = {}) {
    const control = multiline ? textarea({ maxlength: String(maxLength) }) : input({ maxlength: String(maxLength) });
    control.value = value;
    const result = await formSheet(title, field(label, control, hint), [
        { label: confirm || t("admin.save"), value: "ok", primary: !danger, danger },
        { label: t("admin.cancel"), value: null },
    ], { validate: v => v !== "ok" || !required || control.value.trim() !== "" || (control.focus(), false) });
    return result === "ok" ? control.value.trim() : null;
}

/** Lets the user pick one option: [{value, label, sub, disabled}]. Resolves the value or null. */
export function chooseSheet(title, options, { body } = {}) {
    let closeWith;
    const list = h("ul", { class: "list admin-choices" }, options.map(o => h("li", {}, h("button", {
        class: "row", type: "button", disabled: !!o.disabled, onclick: () => closeWith(o.value),
    }, o.iconNode || null, h("span", { class: "row-main" }, h("span", { class: "row-title" }, o.label), o.sub ? h("span", { class: "row-sub" }, o.sub) : null)))));
    return formSheet(title, h("div", {}, body ? h("p", { class: "sheet-body" }, body) : null, list), [{ label: t("admin.cancel"), value: null }],
        { mount: close => { closeWith = close; queueMicrotask(() => list.querySelector("button:not(:disabled)")?.focus()); } });
}

/**
 * Opens the file picker for an image. Resolves {base64, type, name, url} or
 * null; refuses types outside the list and files over the server's limit.
 */
export function pickImage(types, maxBytes = MAX_IMAGE_BYTES) {
    return new Promise(resolve => {
        const picker = h("input", { type: "file", accept: types.join(","), hidden: true });
        picker.onchange = async () => {
            const file = picker.files?.[0];
            picker.remove();
            if (!file) return resolve(null);
            if (!types.includes(file.type)) {
                toast(t("admin.imageType", { types: types.map(type => type.split("/")[1].toUpperCase()).join(", ") }), "warn");
                return resolve(null);
            }
            if (file.size > maxBytes) {
                toast(t("admin.imageTooLarge", { size: Math.round(maxBytes / 1024) }), "warn");
                return resolve(null);
            }
            const bytes = new Uint8Array(await file.arrayBuffer());
            const base64 = bytesToBase64(bytes);
            resolve({ base64, type: file.type, name: file.name, url: `data:${file.type};base64,${base64}` });
        };
        picker.addEventListener("cancel", () => { picker.remove(); resolve(null); });
        document.body.append(picker);
        picker.click();
    });
}

/** Copies text, falling back to selecting it in the given input. */
export async function copyText(text, fallbackInput) {
    try {
        await navigator.clipboard.writeText(text);
        toast(t("admin.copied"));
    } catch {
        fallbackInput?.focus();
        fallbackInput?.select();
    }
}

/** A read-only value with Copy (and Share when the browser can). */
export function copyBox(label, value, { share } = {}) {
    const box = input({ readonly: true, value, class: "input mono admin-copy-input" });
    box.value = value;
    const actions = h("div", { class: "admin-actions" },
        h("button", { class: "btn", type: "button", onclick: () => copyText(box.value, box) }, t("admin.copy")),
        share && navigator.share ? h("button", { class: "btn", type: "button", onclick: () => navigator.share({ title: share, url: box.value }).catch(() => {}) }, t("admin.share")) : null);
    return h("div", { class: "admin-copy" }, field(label, box), actions);
}

/** Placeholder text while a screen loads, and an empty-list note. */
export const loadingNote = () => h("p", { class: "hint", role: "status" }, t("admin.loading"));
export const emptyNote = text => h("p", { class: "hint admin-empty" }, text || t("admin.none"));

/** A date and time in the phone's format. */
export function when(seconds) {
    return seconds ? new Date(seconds * 1000).toLocaleString([], { dateStyle: "medium", timeStyle: "short" }) : "";
}

/** A nickname for a unique ID from the live tree, else a shortened ID. */
export function nameOf(session, uid) {
    if (!uid) return "";
    return session.clients().find(c => c.unique_id === uid)?.nickname || (uid.length > 14 ? `${uid.slice(0, 12)}…` : uid);
}
