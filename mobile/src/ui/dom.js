// dom.js — tiny element builder. Text is always set as textContent, never
// parsed as HTML, so message bodies and names cannot inject markup.

export function h(tag, props = {}, ...children) {
    const node = document.createElement(tag);
    for (const [key, value] of Object.entries(props || {})) {
        if (value === undefined || value === null || value === false) continue;
        if (key === "class") node.className = value;
        else if (key === "text") node.textContent = value;
        else if (key.startsWith("on") && typeof value === "function") node.addEventListener(key.slice(2), value);
        else if (key === "dataset") Object.assign(node.dataset, value);
        else node.setAttribute(key, value === true ? "" : String(value));
    }
    for (const child of children.flat()) {
        if (child === null || child === undefined || child === false) continue;
        node.append(child instanceof Node ? child : document.createTextNode(String(child)));
    }
    return node;
}

/** Squircle avatar with two-letter initials, as in the Echo board. */
export function avatar(name, size = "md") {
    const letters = (name || "?").replace(/[^\p{L}\p{N}]/gu, "").slice(0, 2).toUpperCase() || "?";
    return h("span", { class: `avatar avatar-${size}`, "aria-hidden": "true" }, letters);
}

export function toast(message, kind = "info") {
    const host = document.getElementById("toasts");
    if (!host) return;
    const node = h("div", { class: `toast toast-${kind}` }, message);
    host.append(node);
    setTimeout(() => node.remove(), 4000);
}

export function clock(seconds) {
    const date = new Date(seconds * 1000);
    return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

/** Simple full-screen bottom sheet; resolves with the button value. */
export function sheet(title, body, buttons) {
    return new Promise(resolve => {
        const close = value => { overlay.remove(); resolve(value); };
        const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(null); } },
            h("div", { class: "sheet", role: "dialog", "aria-modal": "true", "aria-label": title },
                h("h2", { class: "sheet-title" }, title),
                body ? h("p", { class: "sheet-body" }, body) : null,
                h("div", { class: "sheet-actions" }, buttons.map(b => h("button", { class: b.primary ? "btn btn-primary" : b.danger ? "btn btn-danger" : "btn", onclick: () => close(b.value) }, b.label)))));
        document.body.append(overlay);
        overlay.querySelector(".btn-primary, .btn-danger, .btn")?.focus();
    });
}
