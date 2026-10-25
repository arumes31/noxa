// files.js — the channel file browser (#/files/<channelID>), like the
// desktop's Files tab: virtual folders, filter and sort, quota bar,
// uploads through the shared queue, download, share, checksum, versions,
// rename, move (also to another channel), delete and temporary links.
import { app } from "../app.js";
import { breadcrumbs, ChannelFiles, checksum, formatBytes, isSealed, normalizeFolder, quotaPercent, splitPath, subfolders, visibleEntries } from "../core/channel-files.js";
import { onUploadsChanged } from "../core/upload-queue.js";
import { addStrings, t } from "../i18n.js";
import { h, sheet, toast, replaceContent } from "./dom.js";
import { icon } from "./icons.js";
import { header, nav, scopeName } from "./common.js";
import { uploadFiles } from "./upload-sheet.js";
import "./files.css";

addStrings({
    en: {
        "files.title": "Files",
        "files.open": "Files of {channel}",
        "files.root": "All files",
        "files.breadcrumb": "Folder path",
        "files.filter": "Filter files",
        "files.sort": "Sort by",
        "files.sort.name": "Name",
        "files.sort.size": "Size",
        "files.sort.date": "Date",
        "files.ascending": "Ascending",
        "files.descending": "Descending",
        "files.refresh": "Refresh",
        "files.upload": "Upload files",
        "files.uploadFolder": "Upload folder",
        "files.newFolder": "New folder",
        "files.newFolderHint": "Folders exist while they contain files. Upload into the new folder to keep it.",
        "files.folderName": "Folder name",
        "files.empty": "No files here yet.",
        "files.noMatches": "No files match the filter.",
        "files.loading": "Loading files…",
        "files.loadFailed": "Files could not be loaded: {error}",
        "files.quota": "{used} of {quota} used",
        "files.noQuota": "{used} used",
        "files.storage": "Channel storage",
        "files.folder": "Folder {name}",
        "files.actions": "Actions for {name}",
        "files.meta": "{size} · {date}",
        "files.by": "by {uploader}",
        "files.sealed": "Chat attachment (encrypted)",
        "files.sealedHint": "Encrypted chat attachments open from their message in the chat.",
        "files.download": "Download",
        "files.share": "Share",
        "files.downloading": "Downloading {name}… {progress}",
        "files.downloaded": "{name} downloaded.",
        "files.downloadFailed": "Download failed: {error}",
        "files.checksum": "Checksum",
        "files.checksumTitle": "SHA-256 of {name}",
        "files.noChecksum": "The server has no checksum for this file.",
        "files.copyChecksum": "Copy checksum",
        "files.checksumCopied": "Checksum copied.",
        "files.verify": "Verify",
        "files.verifying": "Downloading and checking…",
        "files.verifyOK": "Checksum matches. The file is intact.",
        "files.verifyBad": "Checksum does NOT match.",
        "files.versions": "Versions",
        "files.versionsTitle": "Older versions of {name}",
        "files.noVersions": "There are no older versions.",
        "files.downloadVersion": "Download version {name}",
        "files.link": "Temporary link",
        "files.linkTitle": "Download link for {name}",
        "files.linkExpires": "Valid until {time}. It also ends when you sign out or lose access.",
        "files.linkFailed": "No link: {error}",
        "files.copyLink": "Copy link",
        "files.linkCopied": "Link copied.",
        "files.rename": "Rename",
        "files.renameTitle": "Rename {name}",
        "files.renameLabel": "New name (folder/name moves it)",
        "files.renameFailed": "Rename failed: {error}",
        "files.move": "Move",
        "files.moveTitle": "Move {name}",
        "files.targetChannel": "Channel",
        "files.targetFolder": "Folder (empty = top level)",
        "files.moveFailed": "Move failed: {error}",
        "files.moved": "{name} moved to {channel}.",
        "files.delete": "Delete",
        "files.deleteTitle": "Delete {name}?",
        "files.deleteBody": "The file and its older versions are removed for everyone in this channel.",
        "files.deleteFailed": "Delete failed: {error}",
        "files.deleted": "{name} deleted.",
        "files.cancel": "Cancel",
        "files.save": "Save",
        "files.copyFailed": "Copying is not allowed here; select the text instead.",
        "files.chatLink": "Files",
    },
    de: {
        "files.title": "Dateien",
        "files.open": "Dateien von {channel}",
        "files.root": "Alle Dateien",
        "files.breadcrumb": "Ordnerpfad",
        "files.filter": "Dateien filtern",
        "files.sort": "Sortieren nach",
        "files.sort.name": "Name",
        "files.sort.size": "Größe",
        "files.sort.date": "Datum",
        "files.ascending": "Aufsteigend",
        "files.descending": "Absteigend",
        "files.refresh": "Aktualisieren",
        "files.upload": "Dateien hochladen",
        "files.uploadFolder": "Ordner hochladen",
        "files.newFolder": "Neuer Ordner",
        "files.newFolderHint": "Ordner bestehen, solange sie Dateien enthalten. Lade etwas in den neuen Ordner hoch, um ihn zu behalten.",
        "files.folderName": "Ordnername",
        "files.empty": "Hier gibt es noch keine Dateien.",
        "files.noMatches": "Keine Datei passt zum Filter.",
        "files.loading": "Dateien werden geladen…",
        "files.loadFailed": "Dateien konnten nicht geladen werden: {error}",
        "files.quota": "{used} von {quota} belegt",
        "files.noQuota": "{used} belegt",
        "files.storage": "Kanalspeicher",
        "files.folder": "Ordner {name}",
        "files.actions": "Aktionen für {name}",
        "files.meta": "{size} · {date}",
        "files.by": "von {uploader}",
        "files.sealed": "Chat-Anhang (verschlüsselt)",
        "files.sealedHint": "Verschlüsselte Chat-Anhänge öffnest du über ihre Nachricht im Chat.",
        "files.download": "Herunterladen",
        "files.share": "Teilen",
        "files.downloading": "{name} wird heruntergeladen… {progress}",
        "files.downloaded": "{name} heruntergeladen.",
        "files.downloadFailed": "Download fehlgeschlagen: {error}",
        "files.checksum": "Prüfsumme",
        "files.checksumTitle": "SHA-256 von {name}",
        "files.noChecksum": "Der Server hat keine Prüfsumme für diese Datei.",
        "files.copyChecksum": "Prüfsumme kopieren",
        "files.checksumCopied": "Prüfsumme kopiert.",
        "files.verify": "Prüfen",
        "files.verifying": "Wird heruntergeladen und geprüft…",
        "files.verifyOK": "Prüfsumme stimmt. Die Datei ist unverändert.",
        "files.verifyBad": "Prüfsumme stimmt NICHT.",
        "files.versions": "Versionen",
        "files.versionsTitle": "Ältere Versionen von {name}",
        "files.noVersions": "Es gibt keine älteren Versionen.",
        "files.downloadVersion": "Version {name} herunterladen",
        "files.link": "Temporärer Link",
        "files.linkTitle": "Download-Link für {name}",
        "files.linkExpires": "Gültig bis {time}. Er endet auch, wenn du dich abmeldest oder den Zugriff verlierst.",
        "files.linkFailed": "Kein Link: {error}",
        "files.copyLink": "Link kopieren",
        "files.linkCopied": "Link kopiert.",
        "files.rename": "Umbenennen",
        "files.renameTitle": "{name} umbenennen",
        "files.renameLabel": "Neuer Name (ordner/name verschiebt)",
        "files.renameFailed": "Umbenennen fehlgeschlagen: {error}",
        "files.move": "Verschieben",
        "files.moveTitle": "{name} verschieben",
        "files.targetChannel": "Kanal",
        "files.targetFolder": "Ordner (leer = oberste Ebene)",
        "files.moveFailed": "Verschieben fehlgeschlagen: {error}",
        "files.moved": "{name} nach {channel} verschoben.",
        "files.delete": "Löschen",
        "files.deleteTitle": "{name} löschen?",
        "files.deleteBody": "Die Datei und ihre älteren Versionen werden für alle in diesem Kanal entfernt.",
        "files.deleteFailed": "Löschen fehlgeschlagen: {error}",
        "files.deleted": "{name} gelöscht.",
        "files.cancel": "Abbrechen",
        "files.save": "Speichern",
        "files.copyFailed": "Kopieren ist hier nicht erlaubt; markiere den Text stattdessen.",
        "files.chatLink": "Dateien",
    },
});

// Outline glyphs of this screen, drawn like ui/icons.js.
const GLYPHS = {
    folder: "M3 6a1 1 0 0 1 1-1h5l2 2h9a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1z",
    file: "M6 3h8l4 4v14H6zM14 3v4h4",
    upload: "M12 16V4M7 9l5-5 5 5M4 20h16",
    refresh: "M20 11a8 8 0 1 0-2.3 5.7M20 4v7h-7",
};

export function glyph(name) {
    const svg = icon("link");
    svg.querySelector("path").setAttribute("d", GLYPHS[name]);
    return svg;
}

const IOS = /iP(hone|ad|od)/.test(globalThis.navigator?.userAgent || "") || (globalThis.navigator?.platform === "MacIntel" && navigator.maxTouchPoints > 1);

/** Folder upload: Android Chrome and desktop browsers; iOS has no folder picker. */
export function folderUploadSupported() {
    return !IOS && "webkitdirectory" in document.createElement("input");
}

function formatDate(value) {
    const date = new Date(value || 0);
    return Number.isNaN(date.getTime()) || date.getTime() <= 0 ? "" : date.toLocaleString([], { dateStyle: "short", timeStyle: "short" });
}

async function copyText(text, success) {
    try {
        await navigator.clipboard.writeText(text);
        toast(success);
        return true;
    } catch {
        toast(t("files.copyFailed"), "warn");
        return false;
    }
}

/** Saves bytes: the share sheet on iOS (Safari ignores download links in apps), a download link elsewhere. */
async function saveBytes(bytes, name) {
    const blob = new Blob([bytes], { type: "application/octet-stream" });
    if (IOS && navigator.canShare) {
        const file = new File([blob], name, { type: blob.type });
        if (navigator.canShare({ files: [file] })) {
            try { await navigator.share({ files: [file] }); return; } catch (error) { if (error?.name === "AbortError") return; }
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
 * A bottom sheet with arbitrary content (or content(close)); resolves with
 * the clicked button's value, null when dismissed.
 */
function panel(title, content, buttons = [{ label: t("common.close"), value: null }]) {
    return new Promise(resolve => {
        const close = value => { overlay.remove(); document.removeEventListener("keydown", onKey); resolve(value); };
        const onKey = event => { if (event.key === "Escape") close(null); };
        const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(null); } },
            h("div", { class: "sheet files-sheet", role: "dialog", "aria-modal": "true", "aria-label": title },
                h("h2", { class: "sheet-title" }, title),
                typeof content === "function" ? content(close) : content,
                h("div", { class: "sheet-actions" }, buttons.map(b => h("button", { type: "button", class: b.primary ? "btn btn-primary" : b.danger ? "btn btn-danger" : "btn",
                    onclick: async () => { if (b.before && (await b.before()) === false) return; close(typeof b.value === "function" ? b.value() : b.value); } }, b.label)))));
        document.addEventListener("keydown", onKey);
        document.body.append(overlay);
        (overlay.querySelector("input, select") || overlay.querySelector(".btn"))?.focus();
    });
}

/** Fields for sheets: a labelled input or select. */
function field(label, control) {
    const id = `files-field-${Math.random().toString(36).slice(2)}`;
    control.id = id;
    return h("div", { class: "field" }, h("label", { for: id }, label), control);
}

export function renderFiles(root, channelID) {
    const session = app.session;
    const files = new ChannelFiles(session.conn, channelID);
    const state = { folder: "", filter: "", sort: "name", descending: false, entries: [], folders: [], used: 0, quota: 0, loaded: false, error: "" };
    let alive = true;
    let loadToken = 0;
    const channelName = scopeName(session, channelID);

    const status = h("p", { class: "files-status hint", role: "status" });
    const crumbs = h("nav", { class: "files-crumbs", "aria-label": t("files.breadcrumb") });
    const quota = h("div", { class: "files-quota" });
    const list = h("ul", { class: "list files-list", "aria-label": t("files.title") });
    const filter = h("input", { class: "input files-filter", type: "search", placeholder: t("files.filter"), "aria-label": t("files.filter"), autocomplete: "off" });
    const sort = h("select", { class: "input files-sort", "aria-label": t("files.sort") }, ["name", "size", "date"].map(key => h("option", { value: key }, t(`files.sort.${key}`))));
    const direction = h("button", { class: "btn files-direction", type: "button" });
    const picker = h("input", { type: "file", multiple: true, hidden: true, "data-files-upload": "" });
    const folderPicker = h("input", { type: "file", multiple: true, hidden: true, webkitdirectory: true, "data-files-folder": "" });

    filter.oninput = () => { state.filter = filter.value; renderList(); };
    sort.onchange = () => { state.sort = sort.value; renderList(); };
    const drawDirection = () => {
        direction.textContent = state.descending ? "↓" : "↑";
        direction.setAttribute("aria-label", state.descending ? t("files.descending") : t("files.ascending"));
        direction.setAttribute("aria-pressed", String(state.descending));
    };
    direction.onclick = () => { state.descending = !state.descending; drawDirection(); renderList(); };
    drawDirection();

    const upload = (fileList, relative = false) => {
        const chosen = [...(fileList || [])];
        if (!chosen.length) return;
        const base = state.folder;
        uploadFiles(chosen, {
            channelID,
            folder: base,
            // A folder upload keeps the picked tree below the current folder.
            folderFor: file => {
                if (!relative || !file.webkitRelativePath) return base;
                const parts = file.webkitRelativePath.split("/").slice(0, -1).join("/");
                return normalizeFolder(base ? `${base}/${parts}` : parts);
            },
        });
    };
    picker.onchange = () => { upload(picker.files); picker.value = ""; };
    folderPicker.onchange = () => { upload(folderPicker.files, true); folderPicker.value = ""; };

    async function load() {
        const token = ++loadToken;
        status.textContent = state.loaded ? "" : t("files.loading");
        try {
            const result = await files.list(state.folder);
            if (!alive || token !== loadToken) return;
            Object.assign(state, result, { loaded: true, error: "" });
            status.textContent = "";
        } catch (error) {
            if (!alive || token !== loadToken) return;
            state.error = error.message;
            state.loaded = true;
            status.textContent = t("files.loadFailed", { error: error.message });
        }
        renderChrome();
        renderList();
    }

    const openFolder = folder => { state.folder = normalizeFolder(folder); state.entries = []; state.loaded = false; renderChrome(); renderList(); load(); };

    function renderChrome() {
        crumbs.replaceChildren(
            h("button", { type: "button", class: "files-crumb", "aria-current": state.folder ? null : "location", onclick: () => openFolder("") }, t("files.root")),
            ...breadcrumbs(state.folder).flatMap(part => [h("span", { "aria-hidden": "true" }, "/"),
                h("button", { type: "button", class: "files-crumb", "aria-current": part.folder === state.folder ? "location" : null, onclick: () => openFolder(part.folder) }, part.label)]));
        const percent = quotaPercent(state.used, state.quota);
        replaceContent(quota,
            h("span", { class: "row-meta" }, percent === null ? t("files.noQuota", { used: formatBytes(state.used) }) : t("files.quota", { used: formatBytes(state.used), quota: formatBytes(state.quota) })),
            percent === null ? null : h("div", { class: `files-quota-bar${percent > 90 ? " hot" : ""}`, role: "progressbar", "aria-label": t("files.storage"), "aria-valuemin": "0", "aria-valuemax": "100", "aria-valuenow": String(percent) },
                h("span", { style: `width:${percent}%` })));
    }

    function renderList() {
        if (!state.loaded) { list.replaceChildren(); return; }
        const query = state.filter.trim().toLowerCase();
        const folders = subfolders(state.folders, state.folder).filter(name => name.toLowerCase().includes(query));
        if (state.sort === "name" && state.descending) folders.reverse();
        const entries = visibleEntries(state.entries, state);
        const rows = [
            ...folders.map(name => h("li", {}, h("button", { type: "button", class: "row files-folder", "aria-label": t("files.folder", { name }),
                onclick: () => openFolder(state.folder ? `${state.folder}/${name}` : name) },
                h("span", { class: "hash" }, glyph("folder")), h("span", { class: "row-main" }, h("span", { class: "row-title" }, `${name}/`))))),
            ...entries.map(entry => fileRow(entry)),
        ];
        if (!rows.length && !state.error) rows.push(h("li", { class: "hint files-empty", role: "status" }, query ? t("files.noMatches") : t("files.empty")));
        list.replaceChildren(...rows);
    }

    function fileRow(entry) {
        const sealed = isSealed(entry);
        const meta = [t("files.meta", { size: formatBytes(entry.size), date: formatDate(entry.uploaded_at) }), entry.uploader ? t("files.by", { uploader: entry.uploader.length > 12 ? `${entry.uploader.slice(0, 10)}…` : entry.uploader }) : ""].filter(Boolean).join(" · ");
        return h("li", { "data-file": entry.name }, h("button", { type: "button", class: "row files-file", "aria-label": t("files.actions", { name: entry.name }), onclick: () => actions(entry) },
            h("span", { class: "hash" }, glyph("file")),
            h("span", { class: "row-main" },
                h("span", { class: "row-title" }, sealed ? t("files.sealed") : entry.name),
                h("span", { class: "row-sub" }, meta))));
    }

    // --- Actions -----------------------------------------------------------------

    async function fetchBytes(entry, name = entry.name) {
        const label = name;
        status.textContent = t("files.downloading", { name: label, progress: "" });
        try {
            const bytes = await files.download(state.folder, name, {
                onProgress: received => { if (alive) status.textContent = t("files.downloading", { name: label, progress: entry.size ? `${Math.min(100, Math.round((received / entry.size) * 100))} %` : formatBytes(received) }); },
            });
            if (alive) status.textContent = "";
            return bytes;
        } catch (error) {
            if (alive) status.textContent = "";
            throw error;
        }
    }

    async function download(entry, name = entry.name) {
        try {
            const bytes = await fetchBytes(entry, name);
            await saveBytes(bytes, name);
            if (alive) status.textContent = t("files.downloaded", { name });
        } catch (error) {
            toast(t("files.downloadFailed", { error: error.message }), "warn");
        }
    }

    async function shareFile(entry) {
        try {
            const bytes = await fetchBytes(entry);
            const file = new File([bytes], entry.name, { type: "application/octet-stream" });
            if (navigator.canShare?.({ files: [file] })) await navigator.share({ files: [file] });
            else await saveBytes(bytes, entry.name);
        } catch (error) {
            if (error?.name !== "AbortError") toast(t("files.downloadFailed", { error: error.message }), "warn");
        }
    }

    async function showChecksum(entry) {
        const result = h("p", { class: "files-verify", role: "status" });
        const sha = h("code", { class: "files-sha mono", "data-checksum": "" }, entry.sha256 || t("files.noChecksum"));
        const verify = h("button", { type: "button", class: "btn", disabled: !entry.sha256 }, t("files.verify"));
        verify.onclick = async () => {
            verify.disabled = true;
            result.className = "files-verify";
            result.textContent = t("files.verifying");
            try {
                const bytes = await files.download(state.folder, entry.name);
                const ok = checksum(bytes) === String(entry.sha256).toLowerCase();
                result.textContent = ok ? t("files.verifyOK") : t("files.verifyBad");
                result.className = `files-verify ${ok ? "ok" : "bad"}`;
            } catch (error) {
                result.textContent = t("files.downloadFailed", { error: error.message });
                result.className = "files-verify bad";
            }
            verify.disabled = false;
        };
        const copy = h("button", { type: "button", class: "btn", disabled: !entry.sha256, onclick: () => copyText(entry.sha256, t("files.checksumCopied")) }, t("files.copyChecksum"));
        await panel(t("files.checksumTitle", { name: entry.name }), h("div", { class: "files-sheet-body" }, sha, h("div", { class: "files-sheet-row" }, copy, verify), result));
    }

    async function showVersions(entry) {
        const body = h("ul", { class: "list files-versions", "aria-busy": "true" }, h("li", { class: "hint" }, t("files.loading")));
        const done = panel(t("files.versionsTitle", { name: entry.name }), body);
        try {
            const versions = await files.versions(state.folder, entry.name);
            body.removeAttribute("aria-busy");
            body.replaceChildren(...(versions.length ? versions.map(version => h("li", { class: "row" },
                h("span", { class: "row-main" }, h("span", { class: "row-title mono" }, version.name), h("span", { class: "row-sub" }, t("files.meta", { size: formatBytes(version.size), date: formatDate(version.uploaded_at) }))),
                h("button", { type: "button", class: "btn", "aria-label": t("files.downloadVersion", { name: version.name }), onclick: () => download({ ...entry, size: version.size }, version.name) }, t("files.download"))))
                : [h("li", { class: "hint", role: "status" }, t("files.noVersions"))]));
        } catch (error) {
            body.replaceChildren(h("li", { class: "error", role: "status" }, t("common.error", { error: error.message })));
        }
        await done;
    }

    async function showLink(entry) {
        let link;
        try {
            link = await files.link(state.folder, entry.name);
        } catch (error) {
            toast(t("files.linkFailed", { error: error.message }), "warn");
            return;
        }
        const input = h("input", { class: "input mono", readonly: true, value: link.url, "aria-label": t("files.link"), "data-file-link": "" });
        input.onfocus = () => input.select();
        const buttons = [
            { label: t("files.copyLink"), value: null, primary: true, before: () => copyText(link.url, t("files.linkCopied")) },
            navigator.share ? { label: t("files.share"), value: null, before: () => navigator.share({ title: entry.name, url: link.url }).catch(() => {}) } : null,
            { label: t("common.close"), value: null },
        ].filter(Boolean);
        await panel(t("files.linkTitle", { name: entry.name }), h("div", { class: "files-sheet-body" }, input,
            h("p", { class: "hint" }, t("files.linkExpires", { time: new Date(link.expiresAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) }))), buttons);
    }

    async function rename(entry) {
        const input = h("input", { class: "input", value: state.folder ? `${state.folder}/${entry.name}` : entry.name, autocomplete: "off", spellcheck: "false" });
        const value = await panel(t("files.renameTitle", { name: entry.name }), field(t("files.renameLabel"), input), [
            { label: t("files.save"), value: () => input.value, primary: true },
            { label: t("files.cancel"), value: null },
        ]);
        if (!value) return;
        const target = splitPath(value);
        if (!target.name || (target.name === entry.name && target.folder === state.folder)) return;
        try {
            await files.rename(state.folder, entry.name, { newName: target.name, newFolder: target.folder });
        } catch (error) {
            toast(t("files.renameFailed", { error: error.message }), "warn");
        }
        load();
    }

    async function move(entry) {
        const channels = [{ id: 0, name: t("channels.global") }, ...session.channels().map(c => ({ id: c.id, name: c.name }))];
        const select = h("select", { class: "input" }, channels.map(c => h("option", { value: String(c.id), selected: c.id === channelID }, c.id === 0 ? c.name : `# ${c.name}`)));
        const folder = h("input", { class: "input", value: state.folder, placeholder: "docs/2026", autocomplete: "off", spellcheck: "false" });
        const choice = await panel(t("files.moveTitle", { name: entry.name }), h("div", { class: "files-sheet-body" }, field(t("files.targetChannel"), select), field(t("files.targetFolder"), folder)), [
            { label: t("files.move"), value: () => ({ channel: Number(select.value), folder: normalizeFolder(folder.value) }), primary: true },
            { label: t("files.cancel"), value: null },
        ]);
        if (!choice || (choice.channel === channelID && choice.folder === state.folder)) return;
        try {
            await files.rename(state.folder, entry.name, { newFolder: choice.folder, newChannelID: choice.channel });
            toast(t("files.moved", { name: entry.name, channel: channels.find(c => c.id === choice.channel)?.name || choice.channel }));
        } catch (error) {
            toast(t("files.moveFailed", { error: error.message }), "warn");
        }
        load();
    }

    async function remove(entry) {
        const ok = await sheet(t("files.deleteTitle", { name: entry.name }), t("files.deleteBody"), [
            { label: t("files.delete"), value: true, danger: true },
            { label: t("files.cancel"), value: false },
        ]);
        if (!ok) return;
        try {
            await files.delete(state.folder, entry.name);
            toast(t("files.deleted", { name: entry.name }));
        } catch (error) {
            toast(t("files.deleteFailed", { error: error.message }), "warn");
        }
        load();
    }

    async function actions(entry) {
        const sealed = isSealed(entry);
        const items = [
            sealed ? null : ["download", t("files.download"), () => download(entry)],
            sealed || !navigator.share ? null : ["share", t("files.share"), () => shareFile(entry)],
            sealed ? null : ["link", t("files.link"), () => showLink(entry)],
            ["checksum", t("files.checksum"), () => showChecksum(entry)],
            ["versions", t("files.versions"), () => showVersions(entry)],
            ["rename", t("files.rename"), () => rename(entry)],
            ["move", t("files.move"), () => move(entry)],
            ["delete", t("files.delete"), () => remove(entry)],
        ].filter(Boolean);
        const chosen = await panel(sealed ? t("files.sealed") : entry.name, close => h("div", { class: "files-sheet-body" },
            h("p", { class: "hint" }, sealed ? t("files.sealedHint") : t("files.meta", { size: formatBytes(entry.size), date: formatDate(entry.uploaded_at) })),
            h("div", { class: "files-action-list" }, items.map(([key, label]) => h("button", { type: "button", class: `btn files-action${key === "delete" ? " btn-danger" : ""}`, "data-action": key,
                onclick: () => close(key) }, label)))),
            [{ label: t("files.cancel"), value: null }]);
        if (alive) items.find(item => item[0] === chosen)?.[2]();
    }

    async function newFolder() {
        const input = h("input", { class: "input", autocomplete: "off", spellcheck: "false" });
        const name = await panel(t("files.newFolder"), h("div", { class: "files-sheet-body" }, field(t("files.folderName"), input), h("p", { class: "hint" }, t("files.newFolderHint"))), [
            { label: t("files.newFolder"), value: () => input.value, primary: true },
            { label: t("files.cancel"), value: null },
        ]);
        const folder = normalizeFolder(name || "");
        if (!folder) return;
        const path = normalizeFolder(state.folder ? `${state.folder}/${folder}` : folder);
        if (!state.folders.includes(path)) state.folders = [...state.folders, path];
        openFolder(path);
    }

    // Uploads finished into this channel refresh the list.
    let refreshTimer = 0;
    const stopUploads = onUploadsChanged(item => {
        if (item?.status !== "done" || item.target.sealed || item.target.channelID !== channelID) return;
        clearTimeout(refreshTimer);
        refreshTimer = setTimeout(() => { if (alive) load(); }, 150);
    });

    const toolbar = h("div", { class: "files-toolbar" },
        h("button", { type: "button", class: "btn btn-primary", onclick: () => picker.click() }, glyph("upload"), t("files.upload")),
        folderUploadSupported() ? h("button", { type: "button", class: "btn", onclick: () => folderPicker.click() }, t("files.uploadFolder")) : null,
        h("button", { type: "button", class: "btn", onclick: newFolder }, t("files.newFolder")));
    root.replaceChildren(h("section", { class: "screen files-screen" },
        header(t("files.title"), { back: `#/chat/${channelID}`, sub: channelName,
            action: h("button", { class: "icon-btn", type: "button", "aria-label": t("files.refresh"), onclick: () => load() }, glyph("refresh")) }),
        h("div", { class: "screen-body" },
            crumbs, quota, toolbar,
            h("div", { class: "files-controls" }, filter, sort, direction),
            status, list, picker, folderPicker)),
        nav("servers"));
    renderChrome();
    load();
    return () => {
        alive = false;
        clearTimeout(refreshTimer);
        stopUploads();
    };
}
