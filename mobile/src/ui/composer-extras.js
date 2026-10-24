// composer-extras.js — the composer's attach and emoji buttons, shared by
// channel chat, direct messages and groups. Attaching offers the photo
// library, the camera and plain files; all three use one hidden file input
// whose accept/capture attributes are set right before it opens (inside the
// tap, so mobile browsers allow the picker).
import { insertText } from "../core/message-format.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";
import { icon } from "./icons.js";
import "./rich-strings.js";

const SOURCES = [
    { value: "library", accept: "image/*,video/*", capture: null },
    { value: "camera", accept: "image/*", capture: "environment" },
    { value: "file", accept: null, capture: null },
];

/** The action sheet of attachment sources; calls choose(source) inside the tap. */
export function attachSheet(choose) {
    const opener = document.activeElement;
    const close = () => {
        overlay.remove();
        document.removeEventListener("keydown", onKey);
    };
    const onKey = event => { if (event.key === "Escape") { close(); opener?.focus?.(); } };
    const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(); } },
        h("div", { class: "sheet", role: "dialog", "aria-modal": "true", "aria-label": t("file.attach") },
            h("h2", { class: "sheet-title" }, t("file.attach")),
            h("div", { class: "sheet-actions attach-actions" },
                SOURCES.map(source => h("button", { class: "btn", type: "button", onclick: () => { close(); choose(source); } }, t(`rich.attach.${source.value}`))),
                h("button", { class: "btn", type: "button", onclick: () => { close(); opener?.focus?.(); } }, t("link.cancel")))));
    document.addEventListener("keydown", onKey);
    document.body.append(overlay);
    overlay.querySelector(".btn")?.focus();
}

/**
 * The attach button and its file input. onFile(file) runs for a picked file
 * while the button shows busy.
 */
export function attachControls(onFile) {
    const picker = h("input", { type: "file", hidden: true, "aria-hidden": "true", tabindex: "-1" });
    const button = h("button", { class: "attach", type: "button", "aria-label": t("file.attach"), "aria-haspopup": "dialog" }, icon("attach"));
    button.onclick = () => attachSheet(source => {
        source.accept ? picker.setAttribute("accept", source.accept) : picker.removeAttribute("accept");
        source.capture ? picker.setAttribute("capture", source.capture) : picker.removeAttribute("capture");
        picker.click();
    });
    picker.onchange = async () => {
        const file = picker.files?.[0];
        picker.value = "";
        if (!file) return;
        button.disabled = true;
        button.setAttribute("aria-busy", "true");
        try {
            await onFile(file);
        } finally {
            button.disabled = false;
            button.removeAttribute("aria-busy");
        }
    };
    return { button, picker };
}

/** The emoji button: opens the picker and inserts at the caret. */
export function emojiButton(input, session) {
    return h("button", { class: "attach emoji-btn", type: "button", "aria-label": t("rich.emoji.insert"), "aria-haspopup": "dialog", onclick: async () => {
        const start = input.selectionStart ?? input.value.length;
        const end = input.selectionEnd ?? start;
        const { openEmojiPicker } = await import("./emoji-picker.js");
        const emoji = await openEmojiPicker({ session });
        if (!emoji) return;
        const next = insertText(input.value, start, end, emoji);
        input.value = next.value;
        input.focus();
        input.setSelectionRange(next.caret, next.caret);
        input.dispatchEvent(new Event("input", { bubbles: true }));
    } }, icon("emoji"));
}
