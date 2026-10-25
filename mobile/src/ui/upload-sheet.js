// upload-sheet.js — the upload queue panel: progress per file with cancel,
// retry and dismiss. It docks above the navigation while uploads are
// listed and collapses to one line. Composers can reuse it:
//
//     import { uploadFiles } from "./upload-sheet.js";
//     const [item] = uploadFiles([file], { channelID, sealed: true });
//     const { token } = await item.done;
import { app } from "../app.js";
import { cancelUpload, clearFinished, dismissUpload, enqueueUpload, onUploadsChanged, retryUpload, setUploadConnection, uploads } from "../core/upload-queue.js";
import { addStrings, t } from "../i18n.js";
import { h, replaceContent } from "./dom.js";
import { formatBytes } from "../core/channel-files.js";
import "./upload-sheet.css";

addStrings({
    en: {
        "uploads.title": "Uploads",
        "uploads.summary": "{done} of {count} uploaded",
        "uploads.expand": "Show uploads",
        "uploads.collapse": "Hide uploads",
        "uploads.queued": "Waiting",
        "uploads.active": "{sent} of {total}",
        "uploads.done": "Uploaded",
        "uploads.error": "Failed: {error}",
        "uploads.canceled": "Cancelled",
        "uploads.cancel": "Cancel upload of {name}",
        "uploads.retry": "Retry upload of {name}",
        "uploads.dismiss": "Remove {name} from the list",
        "uploads.clear": "Clear finished",
        "uploads.progress": "Upload of {name}",
    },
    de: {
        "uploads.title": "Uploads",
        "uploads.summary": "{done} von {count} hochgeladen",
        "uploads.expand": "Uploads zeigen",
        "uploads.collapse": "Uploads ausblenden",
        "uploads.queued": "Wartet",
        "uploads.active": "{sent} von {total}",
        "uploads.done": "Hochgeladen",
        "uploads.error": "Fehlgeschlagen: {error}",
        "uploads.canceled": "Abgebrochen",
        "uploads.cancel": "Upload von {name} abbrechen",
        "uploads.retry": "Upload von {name} wiederholen",
        "uploads.dismiss": "{name} aus der Liste entfernen",
        "uploads.clear": "Erledigte entfernen",
        "uploads.progress": "Upload von {name}",
    },
});

setUploadConnection(() => app.session?.conn);

let panel = null;
let expanded = true;

function statusText(item) {
    if (item.status === "active") return t("uploads.active", { sent: formatBytes(item.sent), total: formatBytes(item.total) });
    if (item.status === "error") return t("uploads.error", { error: item.error });
    return t(`uploads.${item.status}`);
}

function itemRow(item) {
    const percent = item.total ? Math.round((item.sent / item.total) * 100) : (item.status === "done" ? 100 : 0);
    const button = (label, text, onclick, extra = "") => h("button", { class: `btn upload-action ${extra}`, type: "button", "aria-label": label, onclick }, text);
    return h("li", { class: `upload-item upload-${item.status}`, "data-upload": item.id },
        h("div", { class: "upload-line" },
            h("span", { class: "row-main" },
                h("span", { class: "row-title" }, item.name),
                h("span", { class: "row-sub" }, statusText(item))),
            item.status === "queued" || item.status === "active" ? button(t("uploads.cancel", { name: item.name }), "✕", () => cancelUpload(item.id)) : null,
            item.status === "error" || item.status === "canceled" ? button(t("uploads.retry", { name: item.name }), "↻", () => retryUpload(item.id)) : null,
            item.status !== "queued" && item.status !== "active" ? button(t("uploads.dismiss", { name: item.name }), "🗑", () => dismissUpload(item.id), "upload-dismiss") : null),
        h("div", { class: "upload-bar", role: "progressbar", "aria-label": t("uploads.progress", { name: item.name }), "aria-valuemin": "0", "aria-valuemax": "100", "aria-valuenow": String(percent) },
            h("span", { style: `width:${percent}%` })));
}

function render() {
    const list = uploads();
    if (!list.length) {
        panel?.remove();
        panel = null;
        return;
    }
    if (!panel) {
        panel = h("section", { class: "upload-panel", "aria-label": t("uploads.title") });
        document.body.append(panel);
    }
    const done = list.filter(item => item.status === "done").length;
    const toggle = h("button", { class: "upload-head", type: "button", "aria-expanded": String(expanded), "aria-label": expanded ? t("uploads.collapse") : t("uploads.expand"),
        onclick: () => { expanded = !expanded; render(); } },
        h("span", { class: "row-title" }, t("uploads.title")),
        h("span", { class: "row-meta", role: "status" }, t("uploads.summary", { done, count: list.length })));
    replaceContent(panel, toggle,
        expanded ? h("ul", { class: "list upload-list" }, list.map(itemRow)) : null,
        expanded && done ? h("button", { class: "btn upload-clear", type: "button", onclick: clearFinished }, t("uploads.clear")) : null);
}

onUploadsChanged(render);

/** Queues files for one target and shows the panel; returns the queue items. */
export function uploadFiles(files, target) {
    expanded = true;
    const items = [...files].map(file => enqueueUpload(file, { ...target, folder: target.folderFor ? target.folderFor(file) : target.folder }));
    render();
    return items;
}

export { onUploadsChanged };
