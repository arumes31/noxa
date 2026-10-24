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

/** uid -> data: URL ("" = no picture), filled by the avatar source. */
const avatarCache = new Map();
let avatarSource = null;

/** Sets the lookup of avatar pictures: uid -> Promise<data: URL or "">. */
export function setAvatarSource(source) {
    avatarSource = source;
    avatarCache.clear();
}

function fillAvatar(node, url) {
    if (!url) return;
    node.replaceChildren(h("img", { src: url, alt: "", draggable: "false" }));
    node.classList.add("has-picture");
}

/**
 * Squircle avatar with two-letter initials, as in the Echo board. With a
 * uid the user's picture replaces the initials once it has loaded.
 */
export function avatar(name, size = "md", uid = "") {
    const letters = (name || "?").replace(/[^\p{L}\p{N}]/gu, "").slice(0, 2).toUpperCase() || "?";
    const node = h("span", { class: `avatar avatar-${size}`, "aria-hidden": "true", dataset: uid ? { uid } : undefined }, letters);
    if (uid && avatarSource) {
        if (avatarCache.has(uid)) fillAvatar(node, avatarCache.get(uid));
        else Promise.resolve(avatarSource(uid)).then(url => {
            avatarCache.set(uid, url || "");
            fillAvatar(node, url);
        }).catch(() => {});
    }
    return node;
}

/** A user changed their picture: reload it in every avatar on screen. */
export function refreshAvatars(uid) {
    avatarCache.delete(uid);
    if (!avatarSource) return;
    Promise.resolve(avatarSource(uid)).then(url => {
        avatarCache.set(uid, url || "");
        for (const node of document.querySelectorAll(`.avatar[data-uid="${CSS.escape(uid)}"]`)) {
            if (url) fillAvatar(node, url);
        }
    }).catch(() => {});
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
