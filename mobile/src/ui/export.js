// export.js — "Export history" from a chat's header menu: the loaded
// conversation (after loading older pages) as a text transcript, as JSON, or
// as JSON encrypted with a passphrase (core/chat-export.js has the format).
import { exportFileName, exportJSON, sealExport, transcript } from "../core/chat-export.js";
import { t } from "../i18n.js";
import { h, toast } from "./dom.js";
import "./rich-strings.js";

function isIOS() {
    return /iP(hone|ad|od)/.test(navigator.userAgent) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
}

/** Hands a file to the user: the share sheet on iOS (no downloads there), else a download. */
export async function saveFile(name, contents, type) {
    const blob = new Blob([contents], { type });
    if (isIOS() && navigator.canShare) {
        const file = new File([blob], name, { type });
        if (navigator.canShare({ files: [file] })) {
            try {
                await navigator.share({ files: [file], title: name });
                return;
            } catch (error) {
                if (error?.name === "AbortError") return;
                // Activation expired while loading history; fall back to a download.
            }
        }
    }
    const url = URL.createObjectURL(blob);
    const link = h("a", { href: url, download: name, hidden: true });
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60000);
}

/**
 * Opens the export dialog. load() resolves with {messages, complete}, the
 * normalized messages oldest first (see core/chat-export.js).
 */
export function openExport({ title, kind, load }) {
    const opener = document.activeElement;
    const format = name => h("label", { class: "export-option" },
        h("input", { type: "radio", name: "export-format", value: name, checked: name === "txt" }),
        h("span", {}, t(`rich.export.${name}`)));
    const passphrase = h("input", { type: "password", class: "export-pass", autocomplete: "new-password", "aria-label": t("rich.export.passphrase"), placeholder: t("rich.export.passphrase") });
    const confirm = h("input", { type: "password", class: "export-pass", autocomplete: "new-password", "aria-label": t("rich.export.confirm"), placeholder: t("rich.export.confirm") });
    const secret = h("div", { class: "export-secret", hidden: true }, passphrase, confirm, h("p", { class: "hint" }, t("rich.export.encryptedHint")));
    const plainHint = h("p", { class: "hint" }, t("rich.export.plainHint"));
    const status = h("p", { class: "hint", role: "status" });
    const go = h("button", { class: "btn btn-primary", type: "submit" }, t("rich.export.action"));
    const cancel = h("button", { class: "btn", type: "button", onclick: () => close() }, t("link.cancel"));
    const selected = () => form.querySelector("input[name=export-format]:checked")?.value || "txt";
    const form = h("form", { class: "sheet export-sheet", role: "dialog", "aria-modal": "true", "aria-label": t("rich.export.title"), onsubmit: async event => {
        event.preventDefault();
        const choice = selected();
        if (choice === "enc") {
            if (passphrase.value.length < 8) { status.textContent = t("rich.export.short"); passphrase.focus(); return; }
            if (passphrase.value !== confirm.value) { status.textContent = t("rich.export.mismatch"); confirm.focus(); return; }
        }
        go.disabled = true;
        status.textContent = t("rich.export.loading");
        try {
            const { messages, complete } = await load();
            const meta = { title, kind, complete };
            let name, contents, type;
            if (choice === "txt") {
                [name, contents, type] = [exportFileName(title, "txt"), transcript(messages, meta), "text/plain;charset=utf-8"];
            } else if (choice === "json") {
                [name, contents, type] = [exportFileName(title, "json"), exportJSON(messages, meta), "application/json"];
            } else {
                status.textContent = t("rich.export.encrypting");
                [name, contents, type] = [exportFileName(`${title}-encrypted`, "json"), await sealExport(exportJSON(messages, meta), passphrase.value), "application/json"];
            }
            await saveFile(name, contents, type);
            toast(t("rich.export.done", { count: messages.length }));
            close();
        } catch (error) {
            status.textContent = t("common.error", { error: error.message });
            go.disabled = false;
        }
    } },
    h("h2", { class: "sheet-title" }, t("rich.export.title")),
    h("fieldset", { class: "export-formats" }, h("legend", { class: "hint" }, t("rich.export.format")), format("txt"), format("json"), format("enc")),
    plainHint, secret, status,
    h("div", { class: "sheet-actions" }, go, cancel));
    form.addEventListener("change", () => {
        const encrypted = selected() === "enc";
        secret.hidden = !encrypted;
        plainHint.hidden = encrypted;
    });
    const onKey = event => { if (event.key === "Escape") close(); };
    const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(); } }, form);
    function close() {
        overlay.remove();
        document.removeEventListener("keydown", onKey);
        opener?.focus?.();
    }
    document.addEventListener("keydown", onKey);
    document.body.append(overlay);
    form.querySelector("input")?.focus();
}
