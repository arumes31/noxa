// presence-ui.js — status dots and labels, the status picker, display name
// and avatar editors of the You screen's profile card.
import { app } from "../app.js";
import { AVATAR_SIZE, cropSquare, dataURLBase64, panBy, zoomTo, MAX_ZOOM } from "../core/avatar-crop.js";
import { STATUSES, presenceOf, statusOf } from "../core/presence.js";
import { setAvatar } from "../core/social.js";
import { t } from "../i18n.js";
import { notifier } from "../notifier.js";
import { h, refreshAvatars, toast } from "./dom.js";
import { openPanel, promptText } from "./panel.js";
import "./social-strings.js";
import "./social.css";

/** A small coloured dot for a status ("offline" for nobody online). */
export function statusDot(status, { label = true } = {}) {
    return h("span", { class: `status-dot status-${status}`, role: label ? "img" : null, "aria-label": label ? t(`status.${status}`) : null, "aria-hidden": label ? null : "true", title: t(`status.${status}`) });
}

/** "Away · back at 3" for a client, or "". */
export function statusLine(client) {
    if (!client) return "";
    const status = statusOf(client);
    return [status !== "online" ? t(`status.${status}`) : "", client.status_message || ""].filter(Boolean).join(" · ");
}

/** The header subtitle of a direct conversation: presence of the peer. */
export function presenceSub(uid) {
    const presence = presenceOf(app.session, uid);
    if (!presence.online) return t("status.offline");
    return [t(`status.${presence.status}`), presence.message].filter(Boolean).join(" · ");
}

/** Picks this phone's status and status message. */
export function statusSheet() {
    const session = app.session;
    if (!session) return;
    const self = session.self();
    let chosen = statusOf(self);
    const allowed = STATUSES.filter(s => s !== "invisible" || session.snapshot?.can_set_invisible);
    openPanel({ title: t("status.title") }, close => {
        const message = h("input", { class: "input", type: "text", maxlength: 200, value: self?.status_message || "", placeholder: t("status.messagePlaceholder"), "aria-label": t("status.message") });
        const options = allowed.map(status => {
            const button = h("button", { class: "row status-option", type: "button", "aria-pressed": String(status === chosen), onclick: () => {
                chosen = status;
                options.forEach(b => b.setAttribute("aria-pressed", String(b === button)));
            } }, statusDot(status, { label: false }), h("span", { class: "row-title" }, t(`status.${status}`)));
            return button;
        });
        const save = async event => {
            event.preventDefault();
            try {
                await notifier.setStatus(chosen, message.value.trim());
                close(true);
            } catch (error) {
                toast(t("common.error", { error: error.message }), "warn");
            }
        };
        return h("form", { class: "panel-form", onsubmit: save },
            h("h2", { class: "sheet-title" }, t("status.title")),
            h("div", { class: "status-options", role: "group", "aria-label": t("status.title") }, options),
            h("label", { class: "field" }, h("span", {}, t("status.message")), message),
            h("div", { class: "panel-buttons" },
                h("button", { class: "btn", type: "button", onclick: () => close(null) }, t("home.cancel")),
                h("button", { class: "btn btn-primary", type: "submit" }, t("social.save"))));
    });
}

/** Changes the public display name. */
export async function nameSheet() {
    const session = app.session;
    // The server marks phone sessions with a "#M<n>" suffix; it is not part of the name.
    const current = session.nickname.replace(/#M\d+$/, "");
    const name = await promptText(t("profile.changeName"), { value: current, maxLength: 32, confirm: t("social.save"), cancel: t("home.cancel") });
    if (name === null || !name.trim() || name.trim() === current) return;
    try {
        await session.setDisplayName(name.trim());
        const self = session.self();
        if (self) self.nickname = session.nickname;
        app.meta.nickname = session.nickname;
        session.dispatchEvent(new CustomEvent("snapshot", { detail: session.snapshot }));
        toast(t("profile.nameSaved"));
    } catch (error) {
        toast(t("common.error", { error: error.message }), "warn");
    }
}

function readImage(file) {
    return new Promise((resolve, reject) => {
        const url = URL.createObjectURL(file);
        const image = new Image();
        image.onload = () => resolve({ image, url });
        image.onerror = () => { URL.revokeObjectURL(url); reject(new Error(t("profile.badImage"))); };
        image.src = url;
    });
}

/** Renders the crop into a canvas of size pixels. */
function drawCrop(canvas, image, crop, size) {
    const square = cropSquare(image.naturalWidth, image.naturalHeight, crop);
    canvas.width = size;
    canvas.height = size;
    const context = canvas.getContext("2d");
    context.imageSmoothingQuality = "high";
    context.clearRect(0, 0, size, size);
    context.drawImage(image, square.sx, square.sy, square.size, square.size, 0, 0, size, size);
}

/** Chooses a picture (camera or gallery), crops it and uploads 256×256 PNG. */
export function avatarEditor({ camera = false } = {}) {
    const input = h("input", { type: "file", accept: "image/*", capture: camera ? "user" : null, hidden: true });
    document.body.append(input);
    input.onchange = async () => {
        const file = input.files?.[0];
        input.remove();
        if (!file) return;
        let loaded;
        try {
            loaded = await readImage(file);
        } catch (error) {
            toast(error.message, "warn");
            return;
        }
        const { image, url } = loaded;
        const width = image.naturalWidth, height = image.naturalHeight;
        let crop = { zoom: 1, x: 0, y: 0 };
        openPanel({ title: t("profile.avatarTitle"), className: "avatar-editor", onClose: () => URL.revokeObjectURL(url) }, close => {
            const preview = h("canvas", { class: "avatar-crop", role: "img", "aria-label": t("profile.avatarPreview") });
            const view = () => preview.getBoundingClientRect().width || 240;
            const redraw = () => drawCrop(preview, image, crop, 480);
            const zoom = h("input", { type: "range", min: 1, max: MAX_ZOOM, step: 0.01, value: 1, "aria-label": t("profile.zoom"), oninput: () => {
                crop = zoomTo(width, height, crop, Number(zoom.value));
                redraw();
            } });
            let drag = null;
            preview.addEventListener("pointerdown", event => { drag = { x: event.clientX, y: event.clientY }; preview.setPointerCapture?.(event.pointerId); });
            preview.addEventListener("pointermove", event => {
                if (!drag) return;
                crop = panBy(width, height, crop, event.clientX - drag.x, event.clientY - drag.y, view());
                drag = { x: event.clientX, y: event.clientY };
                redraw();
            });
            const stop = () => { drag = null; };
            preview.addEventListener("pointerup", stop);
            preview.addEventListener("pointercancel", stop);
            // Arrow keys pan for keyboard users.
            preview.tabIndex = 0;
            preview.addEventListener("keydown", event => {
                const step = { ArrowLeft: [8, 0], ArrowRight: [-8, 0], ArrowUp: [0, 8], ArrowDown: [0, -8] }[event.key];
                if (!step) return;
                event.preventDefault();
                crop = panBy(width, height, crop, step[0], step[1], view());
                redraw();
            });
            const save = h("button", { class: "btn btn-primary", type: "button", onclick: async () => {
                save.disabled = true;
                try {
                    const out = h("canvas");
                    drawCrop(out, image, crop, AVATAR_SIZE);
                    // The server takes 256 KiB; a busy photo can exceed that as PNG.
                    let data = dataURLBase64(out.toDataURL("image/png"));
                    if (data.length * 3 / 4 > 250 * 1024) data = dataURLBase64(out.toDataURL("image/jpeg", 0.9));
                    await setAvatar(app.session, data);
                    refreshAvatars(app.session.uniqueID);
                    toast(t("profile.avatarSaved"));
                    close(true);
                } catch (error) {
                    save.disabled = false;
                    toast(t("common.error", { error: error.message }), "warn");
                }
            } }, t("social.save"));
            requestAnimationFrame(redraw);
            return [
                h("h2", { class: "sheet-title" }, t("profile.avatarTitle")),
                h("p", { class: "hint" }, t("profile.avatarHint")),
                preview,
                h("label", { class: "field" }, h("span", {}, t("profile.zoom")), zoom),
                h("div", { class: "panel-buttons" }, h("button", { class: "btn", type: "button", onclick: () => close(null) }, t("home.cancel")), save),
            ];
        });
    };
    input.click();
}
