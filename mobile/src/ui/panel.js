// panel.js — a bottom sheet with arbitrary content (profile cards, the
// avatar editor, the poke popup). Closes on the backdrop, Escape or
// close(); focus returns to where it was.
import { h } from "./dom.js";

export function openPanel({ title, className = "", onClose } = {}, build) {
    const previous = document.activeElement;
    let closed = false;
    const close = value => {
        if (closed) return;
        closed = true;
        overlay.remove();
        document.removeEventListener("keydown", onKey);
        onClose?.(value);
        if (previous?.isConnected) previous.focus?.();
    };
    const onKey = event => { if (event.key === "Escape") close(null); };
    const body = h("div", { class: `sheet panel ${className}`, role: "dialog", "aria-modal": "true", "aria-label": title });
    const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(null); } }, body);
    body.append(...[build(close)].flat().filter(Boolean));
    document.body.append(overlay);
    document.addEventListener("keydown", onKey);
    (body.querySelector("[autofocus]") || body.querySelector("button, [href], input, select, textarea"))?.focus();
    return { close, body };
}

/** Asks for one line of text; resolves with it or null. */
export function promptText(title, { value = "", label = title, placeholder = "", maxLength = 200, confirm, cancel, hint = "" } = {}) {
    return new Promise(resolve => {
        openPanel({ title, onClose: resolve }, close => {
            const input = h("input", { class: "input", type: "text", value, placeholder, maxlength: maxLength, "aria-label": label, autofocus: true, enterkeyhint: "done" });
            const form = h("form", { class: "panel-form", onsubmit: event => { event.preventDefault(); close(input.value); } },
                h("h2", { class: "sheet-title" }, title),
                hint ? h("p", { class: "hint" }, hint) : null,
                input,
                h("div", { class: "panel-buttons" },
                    h("button", { class: "btn", type: "button", onclick: () => close(null) }, cancel),
                    h("button", { class: "btn btn-primary", type: "submit" }, confirm)));
            return form;
        });
    });
}
