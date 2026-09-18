// files-ui.js — wave-7 files & media: the channel file browser (256) with
// folders (261), drag & drop upload queue (257/260) with progress and
// cancel (258), quota bar (265), versions (264), rename/delete (262/263),
// download links (267), checksum display + verify (279/280), and the
// transfers window with a throughput sparkline (277/278). Folders are
// virtual (derived from file rows — empty folders do not persist).
import { humanBytes } from "./clientinfo.js";
import { copyToClipboard } from "./clipboard.js";
import { pickIcon } from "./image-tools.js";
import { closeDialog, confirmDialog, isCurrentServerDialog, mountServerDialog, promptDialog } from "./modal.js";
import { wrappedIndex } from "./a11y.js";
import { imageDataURL } from "./safe-media.js";
import { parseRuntimeObject } from "./runtime-json.js";
import { captureScope, runScopedDialogAction, scopeIsCurrent } from "./scoped-actions.js";
import { buildFileLink } from "./file-links.js";
import { icon, labelButton } from "./icons.js";
import { formatTransferETA } from "./ui-format.js";
import { t } from "./i18n.js";

const V = () => window.__noxa;
const App = () => window.go.main.App;

const fb = {
    channelID: 0,
    folder: "",
    folders: [],
    entries: [],
    used: 0,
    quota: 0,
    open: false,
    filter: "",
    sort: "name",
    descending: false,
    loaded: false,
};
let serverViewGeneration = 0;

function readFileView() {
    return {
        generation: serverViewGeneration,
        channelID: fb.channelID,
        folder: fb.folder,
    };
}

function fileViewIsCurrent(scope) {
    return scopeIsCurrent(scope, readFileView);
}

// activateWorkspaceTab owns the accessible selection and panel visibility for
// the workspace tabs. Other modes can safely restore the chat surface through
// this one path before hiding the tab controls themselves.
export function activateWorkspaceTab(name, { focus = false } = {}) {
    if (name !== "chat" && name !== "files") return false;
    const files = name === "files";
    const tabChat = document.getElementById("tab-chat");
    const tabFiles = document.getElementById("tab-files");
    const chatPane = document.getElementById("chat-pane");
    const filesPane = document.getElementById("files-pane");
    if (!tabChat || !tabFiles || !chatPane || !filesPane) return false;

    fb.open = files;
    tabChat.classList.toggle("active", !files);
    tabFiles.classList.toggle("active", files);
    tabChat.setAttribute("aria-selected", String(!files));
    tabFiles.setAttribute("aria-selected", String(files));
    tabChat.tabIndex = files ? -1 : 0;
    tabFiles.tabIndex = files ? 0 : -1;
    chatPane.hidden = files;
    filesPane.hidden = !files;
    window.__noxaChat?.refreshHeader?.();
    if (files) {
        fb.channelID = V().state.myChannelID;
        refreshFiles();
    }
    if (focus) (files ? tabFiles : tabChat).focus();
    return true;
}

function isVisibleFocusTarget(element) {
    if (!element?.isConnected || element === document.body || element.hidden || element.closest("[hidden]")) return false;
    const style = window.getComputedStyle(element);
    return style.display !== "none" && style.visibility !== "hidden" && element.getClientRects().length > 0;
}

// restoreVisibleWorkspaceFocus is called after compact/zen have changed the
// layout. Switching Files back to Chat before hiding the tabs can otherwise
// leave the browser's active element inside a hidden subtree.
export function restoreVisibleWorkspaceFocus() {
    const active = document.activeElement;
    const inactiveTab = active?.getAttribute?.("role") === "tab" && active.tabIndex === -1;
    if (isVisibleFocusTarget(active) && !inactiveTab) return false;

    const candidates = ["voice-mute", "ptt-btn", "voice-prio", "center"];
    for (const id of candidates) {
        const target = document.getElementById(id);
        if (!isVisibleFocusTarget(target) || target.disabled || typeof target.focus !== "function") continue;
        target.focus({ preventScroll: true });
        return true;
    }
    return false;
}

// ---------------------------------------------------------------------------
// Transfers registry (278) + sparkline data (277)
// ---------------------------------------------------------------------------

const transfers = new Map(); // id -> record
const RECENT_KEEP = 20;
let sparkSamples = []; // aggregate bytes/sec samples (~1 per progress event burst)
let sparkTimer = null;

function trackTransfer(p) {
    let t = transfers.get(p.id);
    if (!t) {
        t = { id: p.id, direction: p.direction, name: p.name, history: [], started: Date.now() };
        transfers.set(p.id, t);
    }
    Object.assign(t, {
        transferred: p.transferred, total: p.total, bps: p.bytes_per_sec,
        status: p.status, error: p.error || "", resumed: p.resumed || 0,
    });
    if (p.status === "active") {
        t.history.push(p.bytes_per_sec || 0);
        if (t.history.length > 60) t.history.shift();
    }
    trimTransfers();
    if (trWin.open) renderTransfers();
}

function trimTransfers() {
    const done = [...transfers.values()].filter((t) => t.status !== "active");
    if (done.length > RECENT_KEEP) {
        done.sort((a, b) => b.started - a.started);
        for (const t of done.slice(RECENT_KEEP)) transfers.delete(t.id);
    }
}

// activeBps aggregates the throughput of active transfers (sparkline).
function activeBps() {
    let sum = 0;
    for (const t of transfers.values()) if (t.status === "active") sum += t.bps || 0;
    return sum;
}

// ---------------------------------------------------------------------------
// File browser pane (256)
// ---------------------------------------------------------------------------

function esc(s) {
    return String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

function fmtDate(ts) {
    if (!ts) return "";
    return new Date(ts).toLocaleDateString() + " " + new Date(ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

// refreshFiles reloads the current folder listing.
async function refreshFiles() {
    fb.loaded = false;
    const pane = document.getElementById("files-pane");
    const list = pane.querySelector(".fb-list");
    if (!fb.channelID) {
        list.removeAttribute("aria-busy");
        list.innerHTML = `<div class="empty-state">${t("files.join")}</div>`;
        renderFbChrome();
        return;
    }
    const generation = serverViewGeneration;
    const channelID = fb.channelID;
    const folder = fb.folder;
    list.setAttribute("aria-busy", "true");
    list.innerHTML = `<div class="empty-state" role="status">${t("files.loading")}</div>`;
    try {
        const resp = await App().FileList(channelID, folder);
        if (generation !== serverViewGeneration || channelID !== fb.channelID || folder !== fb.folder) return;
        fb.entries = resp.entries || [];
        fb.folders = resp.folders || [];
        fb.used = resp.used_bytes || 0;
        fb.quota = resp.quota_bytes || 0;
    } catch (err) {
        if (generation !== serverViewGeneration || channelID !== fb.channelID || folder !== fb.folder) return;
        list.innerHTML = `<div class="empty-state" role="alert">${esc(t("files.listFailed", { error: String(err) }))}</div>`;
        return;
    } finally {
        if (generation === serverViewGeneration && channelID === fb.channelID && folder === fb.folder) {
            list.removeAttribute("aria-busy");
        }
    }
    renderFbChrome();
    fb.loaded = true;
    renderFbList();
}

// subfoldersOf returns the direct child folders of the current folder.
function subfoldersOf() {
    const prefix = fb.folder ? fb.folder + "/" : "";
    const subs = new Set();
    for (const f of fb.folders) {
        if (!f.startsWith(prefix)) continue;
        const rest = f.slice(prefix.length);
        if (rest && !rest.includes("/")) subs.add(rest);
        else if (rest) subs.add(rest.split("/")[0]);
    }
    return [...subs].sort();
}

function renderFbChrome() {
    const pane = document.getElementById("files-pane");
    const filter = pane.querySelector(".fb-filter");
    filter.placeholder = t("files.filter");
    filter.setAttribute("aria-label", t("files.filter"));
    for (const [selector, key] of [[".fb-refresh", "files.refreshLabel"], [".fb-upload", "files.uploadFiles"], [".fb-mkdir", "files.newFolderHelp"], [".fb-emoji", "files.manageEmoji"], [".fb-banner", "files.setBanner"], [".fb-transfers", "files.openTransfers"]]) {
        const button = pane.querySelector(selector);
        button.title = t(key);
        button.setAttribute("aria-label", t(key));
    }
    pane.querySelector(".fb-upload span").textContent = t("files.upload");
    pane.querySelector(".fb-list").setAttribute("aria-label", t("files.channelFiles"));
    const ch = V().state.channels.find((c) => c.ChannelID === fb.channelID);
    // Breadcrumb.
    const crumb = pane.querySelector(".fb-crumb");
    crumb.innerHTML = "";
    const mk = (label, folder) => {
        const a = document.createElement("a");
        a.href = "#";
        a.textContent = label;
        a.onclick = (event) => { event.preventDefault(); fb.folder = folder; refreshFiles(); };
        crumb.appendChild(a);
        crumb.appendChild(document.createTextNode(" / "));
    };
    mk(ch ? "# " + ch.Name : "channel", "");
    const segs = fb.folder ? fb.folder.split("/") : [];
    segs.forEach((s, i) => mk(s, segs.slice(0, i + 1).join("/")));
    if (crumb.lastChild) crumb.removeChild(crumb.lastChild);

    // Quota bar (265).
    const q = pane.querySelector(".fb-quota");
    if (fb.quota > 0) {
        const pct = Math.min(100, Math.round(fb.used / fb.quota * 100));
        q.innerHTML = `<div class="fb-quota-fill${pct > 90 ? " hot" : ""}" style="width:${pct}%"></div>`;
        q.title = t("files.quota", { used: humanBytes(fb.used), quota: humanBytes(fb.quota), percent: pct });
        q.setAttribute("role", "progressbar");
        q.setAttribute("aria-label", t("files.storage"));
        q.setAttribute("aria-valuemin", "0");
        q.setAttribute("aria-valuemax", "100");
        q.setAttribute("aria-valuenow", String(pct));
        q.classList.remove("hidden");
    } else {
        q.classList.add("hidden");
        q.title = t("files.noQuota", { used: humanBytes(fb.used) });
        for (const attr of ["role", "aria-label", "aria-valuemin", "aria-valuemax", "aria-valuenow"]) {
            q.removeAttribute(attr);
        }
    }
}

function renderFbList() {
    if (!fb.loaded) return;
    const list = document.getElementById("files-pane").querySelector(".fb-list");
    list.innerHTML = "";
    const query = fb.filter.trim().toLowerCase();
    const byName = (a, b) => a.localeCompare(b, undefined, { numeric: true, sensitivity: "base" });
    const subs = subfoldersOf().filter(name => name.toLowerCase().includes(query))
        .sort((a, b) => byName(a, b) * (fb.sort === "name" && fb.descending ? -1 : 1));
    const entries = fb.entries.filter(entry => (entry.name || "").toLowerCase().includes(query)).sort((a, b) => {
        let order = 0;
        if (fb.sort === "size") order = (Number(a.size) || 0) - (Number(b.size) || 0);
        if (fb.sort === "date") order = (new Date(a.uploaded_at || 0).getTime() || 0) - (new Date(b.uploaded_at || 0).getTime() || 0);
        return (order || byName(a.name || "", b.name || "")) * (fb.descending ? -1 : 1);
    });
    if (!query && subs.length === 0 && entries.length === 0) {
        list.innerHTML = `<div class="empty-state">${t("files.empty")}</div>`;
        return;
    }
    const table = document.createElement("table");
    table.className = "perm-grid fb-grid";
    table.innerHTML = `<thead><tr><th data-sort="name"></th><th data-sort="size"></th><th>${t("files.uploader")}</th><th data-sort="date"></th><th><span class="sr-only">${t("files.moreActions")}</span></th></tr></thead><tbody></tbody>`;
    for (const header of table.querySelectorAll("[data-sort]")) {
        const key = header.dataset.sort;
        const selected = key === fb.sort;
        header.setAttribute("aria-sort", selected ? (fb.descending ? "descending" : "ascending") : "none");
        const button = document.createElement("button");
        button.type = "button";
        button.className = "fb-sort";
        button.setAttribute("aria-label", t(`files.sort.${key}`));
        button.textContent = t(`files.column.${key}`) + (selected ? (fb.descending ? " ↓" : " ↑") : "");
        button.onclick = () => {
            fb.descending = selected ? !fb.descending : false;
            fb.sort = key;
            renderFbList();
            list.querySelector(`[data-sort="${key}"] button`).focus({ preventScroll: true });
        };
        header.appendChild(button);
    }
    const tbody = table.querySelector("tbody");

    for (const sub of subs) {
        const tr = document.createElement("tr");
        tr.className = "fb-folder";
        tr.innerHTML = `<td colspan="5">${icon("folder")} <a class="fb-folder-link"></a></td>`;
        const link = tr.querySelector(".fb-folder-link");
        link.href = "#";
        link.textContent = sub + "/";
        link.onclick = (event) => {
            event.preventDefault();
            fb.folder = (fb.folder ? fb.folder + "/" : "") + sub;
            refreshFiles();
        };
        tbody.appendChild(tr);
    }

    for (const e of entries) {
        tbody.appendChild(fileRow(e));
    }
    list.appendChild(table);
    if (!subs.length && !entries.length) {
        const empty = document.createElement("div");
        empty.className = "empty-state";
        empty.setAttribute("role", "status");
        empty.textContent = t("files.noMatches");
        list.appendChild(empty);
    }
}

// isChatAttachment reports a row the browser cannot make sense of: a chat
// attachment is sealed with a per-file key that exists only inside the
// encrypted chat message linking it, so downloading it here yields ciphertext
// that looks like corruption (91-135). The server's flag is authoritative
// when set, but it is omitempty on the wire — false and absent look identical
// — so the content-derived ".vcx" suffix stays as the fallback. Both err
// toward showing the lock, which is the safe direction.
function isChatAttachment(e) {
    return !!e.encrypted || (e.name || "").toLowerCase().endsWith(".vcx");
}

function fileRow(e) {
    const tr = document.createElement("tr");
    tr.innerHTML = `<td class="fb-name"></td><td class="mono">${humanBytes(e.size)}</td>
        <td class="mono fb-up"></td><td class="mono fb-date">${fmtDate(e.uploaded_at)}</td><td class="fb-actions"></td>`;
    const sealed = isChatAttachment(e);
    const nameCell = tr.querySelector(".fb-name");
    const name = document.createElement("span");
    name.className = "fb-filename";
    name.textContent = sealed ? t("files.sealedAttachment") : e.name;
    name.title = e.name;
    nameCell.appendChild(name);
    const details = document.createElement("details");
    details.className = "fb-details";
    details.innerHTML = `<summary>${t("files.details")}</summary><div class="fb-checksum"><span>SHA-256</span><code class="fb-sha"></code><button type="button" class="fb-copy-sha file-action">${t("files.copyChecksum")}</button></div>`;
    const sha = details.querySelector(".fb-sha");
    sha.textContent = e.sha256 || t("files.noChecksum");
    sha.setAttribute("role", "status");
    const copy = details.querySelector(".fb-copy-sha");
    copy.disabled = !e.sha256;
    copy.onclick = () => copyToClipboard(e.sha256, { success: t("files.checksumCopied"), isCurrent: () => sha.isConnected });
    nameCell.appendChild(details);
    const up = tr.querySelector(".fb-up");
    up.textContent = (e.uploader || "").slice(0, 8) + (e.uploader ? "…" : "");
    up.title = e.uploader || "";
    const act = tr.querySelector(".fb-actions");
    const btn = (parent, glyph, key, fn) => {
        const button = document.createElement("button");
        button.type = "button";
        button.className = "file-action";
        labelButton(button, glyph, t(key));
        button.title = t(key);
        button.setAttribute("aria-label", t(key));
        button.onclick = fn;
        parent.appendChild(button);
        return button;
    };
    const dl = btn(act, "download", "files.download", () => downloadFile(e));
    const menu = document.createElement("details");
    menu.className = "fb-action-menu";
    menu.innerHTML = `<summary aria-label="${t("files.moreActions")}">${icon("more")}</summary><div class="fb-action-list"></div>`;
    act.appendChild(menu);
    const actions = menu.querySelector(".fb-action-list");
    menu.addEventListener("toggle", () => {
        if (!menu.open) return;
        for (const other of document.querySelectorAll(".fb-action-menu[open]")) if (other !== menu) other.open = false;
        const anchor = menu.querySelector("summary").getBoundingClientRect();
        const container = menu.closest(".fb-list").getBoundingClientRect();
        actions.style.maxHeight = Math.max(0, container.height - 16) + "px";
        const bounds = actions.getBoundingClientRect();
        actions.style.left = Math.max(8, Math.min(anchor.right - bounds.width, innerWidth - bounds.width - 8)) + "px";
        actions.style.top = Math.max(container.top + 8, Math.min(anchor.bottom + 4, container.bottom - bounds.height - 8)) + "px";
    });
    menu.addEventListener("keydown", event => {
        if (event.key === "Escape") { event.preventDefault(); menu.open = false; menu.querySelector("summary").focus(); }
    });
    const action = (glyph, key, fn) => btn(actions, glyph, key, () => {
        menu.open = false;
        menu.querySelector("summary").focus();
        return fn();
    });
    const link = action("link", "files.copyLink", () => linkFile(e));
    if (sealed) {
        dl.disabled = true;
        dl.title = t("files.openInChat");
        link.disabled = true;
        link.title = t("files.noSealedLink");
        const hint = document.createElement("span");
        hint.className = "fb-sealed-hint";
        hint.textContent = t("files.openInChat");
        nameCell.appendChild(hint);
    }
    action("check", "files.verify", () => { details.open = true; return verifyFile(e, tr); }).disabled = !e.sha256;
    action("file", "files.versions", () => toggleVersions(e, tr));
    action("edit", "files.rename", () => renameFile(e));
    action("transfer", "files.move", () => moveToChannel(e));
    action("trash", "files.delete", () => deleteFile(e)).classList.add("danger-action");
    return tr;
}

// --- row actions ------------------------------------------------------------

let xferSeq = 0;

// startDownload runs one download attempt and remembers its arguments so the
// transfer list can retry it — a retry into the same destination resumes from
// the partial file rather than starting over (259).
async function startDownload(args, id) {
    if (args.generation !== undefined && args.generation !== serverViewGeneration) return;
    const generation = serverViewGeneration;
    id = id || `dl-${++xferSeq}`;
    const err = await App().DownloadFileProgress(id, args.channelID, args.folder, args.name, args.path, args.size || 0);
    if (generation !== serverViewGeneration) return;
    if (err) {
        V().toast(t("files.downloadFailed", { error: String(err) }), "warn");
        return;
    }
    downloadArgs.set(id, args);
    V().toast(t("files.downloading", { name: args.name }));
}

// downloadArgs keeps the retry payload per transfer id.
const downloadArgs = new Map();

async function downloadFile(e) {
    const generation = serverViewGeneration;
    const channelID = fb.channelID;
    const folder = fb.folder;
    // The configured download folder (Downloads settings) wins; only fall back
    // to the save dialog when the user has not set one.
    let path = await App().DownloadPath(e.name);
    if (generation !== serverViewGeneration) return;
    if (!path) path = await App().PickSavePath(e.name);
    if (!path || generation !== serverViewGeneration) return;
    startDownload({ channelID, folder, name: e.name, path, size: e.size || 0, generation });
}

async function linkFile(e) {
    const scope = captureScope(readFileView);
    const controlAddress = V().state.lastConnect?.addr;
    try {
        const resp = await App().FileLink(scope.channelID, scope.folder, e.name);
        if (!fileViewIsCurrent(scope)) return;
        const url = buildFileLink(controlAddress, resp);
        if (!url) {
            V().toast(t("files.badLink"), "warn");
            return;
        }
        await copyToClipboard(url, {
            success: "download link copied (valid until " + fmtDate(resp.expires_at * 1000) + ")",
            isCurrent: () => fileViewIsCurrent(scope),
        });
    } catch (err) {
        if (fileViewIsCurrent(scope)) V().toast(t("files.linkFailed", { error: String(err) }), "warn");
    }
}

async function verifyFile(e, tr) {
    const generation = serverViewGeneration;
    const channelID = fb.channelID;
    const folder = fb.folder;
    const sha = tr.querySelector(".fb-sha");
    const current = () => generation === serverViewGeneration && channelID === fb.channelID &&
        folder === fb.folder && tr.isConnected && sha?.isConnected && tr.querySelector(".fb-sha") === sha;
    if (!current()) return;
    sha.textContent = "…";
    try {
        const ok = await App().VerifyFile(channelID, folder, e.name, e.sha256);
        if (!current()) return;
        sha.textContent = ok ? t("files.verifyOK") : t("files.verifyBad");
        sha.className = "mono fb-sha " + (ok ? "verify-ok" : "verify-bad");
        setTimeout(() => {
            if (!current()) return;
            sha.textContent = e.sha256;
            sha.className = "mono fb-sha";
        }, 4000);
    } catch (err) {
        if (!current()) return;
        sha.textContent = t("files.verifyError");
        V().toast(t("files.verifyFailed", { error: String(err) }), "warn");
    }
}

async function toggleVersions(e, tr) {
    const next = tr.nextSibling;
    if (next && next.classList && next.classList.contains("fb-versions")) {
        next.remove();
        return;
    }
    const vr = document.createElement("tr");
    vr.className = "fb-versions";
    vr.innerHTML = `<td colspan="5"><div class="fb-ver-list">${t("files.loadingShort")}</div></td>`;
    tr.after(vr);
    const generation = serverViewGeneration;
    const channelID = fb.channelID;
    const folder = fb.folder;
    try {
        const resp = await App().FileVersions(channelID, folder, e.name);
        if (generation !== serverViewGeneration || !vr.isConnected) return;
        const list = vr.querySelector(".fb-ver-list");
        if (!resp.entries || resp.entries.length === 0) {
            list.textContent = t("files.noOldVersions");
            return;
        }
        list.innerHTML = "";
        for (const v of resp.entries) {
            const row = document.createElement("div");
            row.className = "fb-ver-row";
            row.innerHTML = `<span class="mono fb-ver-name"></span><span class="mono">${humanBytes(v.size)}</span><span class="mono fb-date">${fmtDate(v.uploaded_at)}</span>`;
            row.querySelector(".fb-ver-name").textContent = v.name;
            const dl = document.createElement("button");
            dl.className = "icon-btn";
            dl.innerHTML = icon("download");
            dl.setAttribute("aria-label", t("files.downloadVersion"));
            dl.title = t("files.downloadVersion");
            dl.onclick = () => downloadFile({ ...e, name: v.name, size: v.size });
            row.appendChild(dl);
            list.appendChild(row);
        }
    } catch (err) {
        if (generation !== serverViewGeneration || !vr.isConnected) return;
        vr.querySelector(".fb-ver-list").textContent = t("files.versionsFailed", { error: String(err) });
    }
}

async function renameFile(e) {
    await runScopedDialogAction({
        readScope: readFileView,
        openDialog: (scope) => promptDialog({
            title: t("files.renameTitle"),
            label: t("files.renameLabel"),
            value: (scope.folder ? scope.folder + "/" : "") + e.name,
            confirmLabel: t("files.renameConfirm"),
            serverScoped: true,
        }),
        isAccepted: (name) => !!name && name !== e.name,
        perform: async (scope, name) => {
            let newFolder = "";
            let newName = name;
            const index = name.lastIndexOf("/");
            if (index >= 0) {
                newFolder = name.slice(0, index);
                newName = name.slice(index + 1);
            }
            const err = await App().FileRename(scope.channelID, scope.folder, e.name, newFolder, newName, 0);
            if (!fileViewIsCurrent(scope)) return;
            if (err) V().toast(t("files.renameFailed", { error: String(err) }), "warn");
            setTimeout(refreshFiles, 400);
        },
    });
}

// moveToChannel moves a file into another channel (262). The server checks
// both channels, so a target the user cannot upload to is refused there.
async function moveToChannel(e) {
    const scope = captureScope(readFileView);
    const channels = (V().state.channels || []).filter((c) => c.ChannelID !== scope.channelID);
    if (channels.length === 0) {
        V().toast(t("files.noOtherChannel"), "warn");
        return;
    }
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg">
            <h3>${t("files.moveTitle")}</h3>
            <div class="dlg-text fb-move-what"></div>
            <label class="dlg-label">${t("files.targetChannel")}</label>
            <select class="dlg-input fb-move-ch"></select>
            <label class="dlg-label">${t("files.targetFolder")}</label>
            <input class="dlg-input fb-move-folder" placeholder="docs/2024" />
            <div class="dlg-buttons">
                <button class="dlg-ok">${t("files.moveConfirm")}</button>
                <button class="dlg-cancel">${t("common.cancel")}</button>
            </div>
        </div>`;
    overlay.querySelector(".fb-move-what").textContent = e.name;
    const sel = overlay.querySelector(".fb-move-ch");
    for (const c of channels) {
        const opt = document.createElement("option");
        opt.value = String(c.ChannelID);
        opt.textContent = c.Name;
        sel.appendChild(opt);
    }
    const close = () => overlay.remove();
    overlay.querySelector(".dlg-cancel").onclick = close;
    overlay.onclick = (ev) => { if (ev.target === overlay) close(); };
    overlay.querySelector(".dlg-ok").onclick = async () => {
        if (!isCurrentServerDialog(overlay) || !fileViewIsCurrent(scope)) return;
        const target = parseInt(sel.value, 10);
        const folder = overlay.querySelector(".fb-move-folder").value.replace(/^\/+|\/+$/g, "");
        const targetName = sel.options[sel.selectedIndex].textContent;
        close();
        const err = await App().FileRename(scope.channelID, scope.folder, e.name, folder, e.name, target);
        if (!fileViewIsCurrent(scope)) return;
        if (err) V().toast(t("files.moveFailed", { error: String(err) }), "warn");
        else V().toast(t("files.moved", { name: e.name, channel: targetName }));
        setTimeout(refreshFiles, 400);
    };
    mountServerDialog(overlay);
}

async function deleteFile(e) {
    await runScopedDialogAction({
        readScope: readFileView,
        openDialog: () => confirmDialog({
            title: t("files.deleteTitle"),
            message: t("files.deleteWarning", { name: e.name }),
            confirmLabel: t("files.deleteConfirm"),
            danger: true,
            serverScoped: true,
        }),
        isAccepted: Boolean,
        perform: async (scope) => {
            const err = await App().FileDelete(scope.channelID, scope.folder, e.name);
            if (!fileViewIsCurrent(scope)) return;
            if (err) V().toast(t("files.deleteFailed", { error: String(err) }), "warn");
            setTimeout(refreshFiles, 400);
        },
    });
}

// --- upload queue (257/260) -----------------------------------------------------

const uploadQueue = [];
let uploadActive = false;
const UPLOAD_WAIT_TIMEOUT_MS = 10 * 60 * 1000;

// DROP_MAX caps the browser-File route: a dropped File has no path on disk we
// can hand to Go, so its bytes must travel base64 inside one IPC argument.
// Past a few MiB that argument stops arriving at all, so refuse it loudly and
// point at the picker, which streams (259).
const DROP_MAX = 4 * 1024 * 1024;

// queueUploads takes browser File objects (drag & drop).
function queueUploads(files) {
    if (!fb.channelID) {
        V().toast(t("files.joinFirst"), "warn");
        return;
    }
    let queued = 0;
    for (const f of files) {
        if (f.size > DROP_MAX) {
            V().toast(t("files.tooLarge", { name: f.name, size: humanBytes(f.size) }), "warn");
            continue;
        }
        uploadQueue.push({ file: f, channelID: fb.channelID, folder: fb.folder, generation: serverViewGeneration });
        queued++;
    }
    if (queued) V().toast(t("files.queued", { count: queued }));
    pumpUploads();
}

// queueUploadPaths takes native paths from the picker: those stream off disk.
function queueUploadPaths(paths) {
    if (!fb.channelID) {
        V().toast(t("files.joinFirst"), "warn");
        return;
    }
    for (const p of paths) uploadQueue.push({ path: p, channelID: fb.channelID, folder: fb.folder, generation: serverViewGeneration });
    V().toast(t("files.queued", { count: paths.length }));
    pumpUploads();
}

// awaitUpload keeps the queue sequential (260): the next file starts only once
// this one has left the active state.
function awaitUpload(id, generation) {
    const deadline = Date.now() + UPLOAD_WAIT_TIMEOUT_MS;
    const check = setInterval(() => {
        if (generation !== serverViewGeneration) {
            clearInterval(check);
            uploadActive = false;
            pumpUploads();
            return;
        }
        const t = transfers.get(id);
        if (t && t.status !== "active") {
            clearInterval(check);
            uploadActive = false;
            if (t.status === "done") refreshFiles();
            pumpUploads();
        } else if (Date.now() >= deadline) {
            clearInterval(check);
            uploadActive = false;
            V().toast(t("files.uploadTimeout"), "warn");
            pumpUploads();
        }
    }, 300);
}

function bytesToBase64(bytes) {
    const chunks = [];
    const chunkSize = 0x8000;
    for (let offset = 0; offset < bytes.length; offset += chunkSize) {
        chunks.push(String.fromCharCode(...bytes.subarray(offset, offset + chunkSize)));
    }
    return btoa(chunks.join(""));
}

// Export the data-only attachment boundaries so their behavior can be checked
// without constructing the complete file-browser DOM.
export { bytesToBase64, isChatAttachment };

async function pumpUploads() {
    if (uploadActive || uploadQueue.length === 0) return;
    uploadActive = true;
    const { file, path, channelID, folder, generation } = uploadQueue.shift();
    if (generation !== serverViewGeneration) {
        uploadActive = false;
        pumpUploads();
        return;
    }
    const id = `up-${++xferSeq}`;
    const label = path ? path.split(/[\\/]/).pop() : file.name;
    try {
        let err;
        if (path) {
            err = await App().UploadPathProgress(id, channelID, folder, path);
        } else {
            const buf = await file.arrayBuffer();
            if (generation !== serverViewGeneration) {
                uploadActive = false;
                pumpUploads();
                return;
            }
            const b64 = bytesToBase64(new Uint8Array(buf));
            err = await App().UploadFileProgress(id, channelID, folder, file.name, b64);
        }
        if (generation !== serverViewGeneration) {
            uploadActive = false;
            pumpUploads();
            return;
        }
        if (err) {
            V().toast(t("files.uploadNamedFailed", { name: label, error: String(err) }), "warn");
            uploadActive = false;
            pumpUploads();
            return;
        }
        awaitUpload(id, generation);
    } catch (err) {
        V().toast(t("files.readFailed", { name: label, error: String(err) }), "warn");
        uploadActive = false;
        pumpUploads();
    }
}

// --- transfers window (278) + sparkline (277) -------------------------------------

const trWin = { open: false, overlay: null };

function openTransfers() {
    if (trWin.open) return;
    trWin.open = true;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg transfers">
            <div class="pm-head">
                <h3>${t("files.transfers")}</h3>
                <button class="icon-btn tr-close" title="${t("common.close")}" aria-label="${t("common.close")}">${icon("close")}</button>
            </div>
            <canvas class="tr-spark" width="640" height="48"></canvas>
            <div class="tr-list"></div>
        </div>`;
    trWin.overlay = overlay;
    overlay.querySelector(".tr-close").onclick = closeTransfers;
    overlay.onclick = (e) => { if (e.target === overlay) closeDialog(overlay, "cancel"); };
    mountServerDialog(overlay, {
        onClose: () => {
            trWin.open = false;
            if (sparkTimer) clearInterval(sparkTimer);
            sparkTimer = null;
            if (trWin.overlay === overlay) trWin.overlay = null;
        },
    });
    renderTransfers();
    sparkTimer = setInterval(() => {
        sparkSamples.push(activeBps());
        if (sparkSamples.length > 60) sparkSamples.shift();
        drawSpark();
    }, 1000);
}

function closeTransfers() {
    if (!trWin.open) return;
    closeDialog(trWin.overlay);
}

function drawSpark() {
    if (!trWin.open) return;
    const canvas = trWin.overlay.querySelector(".tr-spark");
    const ctx = canvas.getContext("2d");
    ctx.fillStyle = "#0b0f14";
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    const max = Math.max(1, ...sparkSamples);
    ctx.strokeStyle = "#2ee6a8";
    ctx.lineWidth = 1.5;
    ctx.beginPath();
    sparkSamples.forEach((v, i) => {
        const x = i / 59 * canvas.width;
        const y = canvas.height - (v / max) * (canvas.height - 4) - 2;
        if (i === 0) ctx.moveTo(x, y);
        else ctx.lineTo(x, y);
    });
    ctx.stroke();
    ctx.fillStyle = "#8494a6";
    ctx.font = "10px 'JetBrains Mono Variable', monospace";
    ctx.fillText(humanBytes(activeBps()) + "/s", 6, 12);
}

function renderTransfers() {
    if (!trWin.open) return;
    const list = trWin.overlay.querySelector(".tr-list");
    list.innerHTML = "";
    const rows = [...transfers.values()].sort((a, b) => b.started - a.started);
    if (rows.length === 0) {
        list.innerHTML = `<div class="empty-state">${t("files.noTransfers")}</div>`;
        return;
    }
    for (const transfer of rows) {
        const row = document.createElement("div");
        row.className = "tr-row " + transfer.status;
        const pct = transfer.total > 0 ? Math.min(100, Math.round(transfer.transferred / transfer.total * 100)) : 0;
        const eta = transfer.status === "active" && transfer.bps > 0 && transfer.total > 0
            ? formatTransferETA((transfer.total - transfer.transferred) / transfer.bps)
            : "";
        const resumed = transfer.resumed > 0 ? t("files.resumed", { size: humanBytes(transfer.resumed) }) : "";
        row.innerHTML = `
            <span class="tr-dir">${icon(transfer.direction === "upload" ? "upload" : "download")}</span>
            <span class="tr-name" title="${esc(transfer.name)}">${esc(transfer.name)}</span>
            <span class="tr-bar"><span class="tr-fill" style="width:${pct}%"></span></span>
            <span class="tr-meta mono">${pct}% · ${humanBytes(transfer.bps || 0)}/s ${eta ? "· " + eta : ""}${resumed}</span>
            <span class="tr-status mono">${esc(t("files.transfer." + (["active", "done", "error", "failed", "cancelled", "canceled", "queued"].includes(transfer.status) ? transfer.status : "unknown")))}${transfer.error ? ": " + esc(transfer.error) : ""}</span>`;
        if (transfer.status === "active") {
            const cancel = document.createElement("button");
            cancel.className = "icon-btn";
            cancel.innerHTML = icon("close");
            cancel.setAttribute("aria-label", t("files.cancelTransfer"));
            cancel.title = t("files.cancelTransfer");
            cancel.onclick = () => App().CancelTransfer(transfer.id);
            row.appendChild(cancel);
        } else if (transfer.status !== "done" && downloadArgs.has(transfer.id)) {
            // (259) retrying into the same destination picks up where the
            // interrupted attempt stopped instead of re-fetching the whole file.
            const retry = document.createElement("button");
            retry.className = "icon-btn";
            labelButton(retry, "refresh", t("files.resume"));
            retry.title = t("files.resumeHelp");
            retry.onclick = () => {
                const args = downloadArgs.get(transfer.id);
                transfer.history = [];
                startDownload(args, transfer.id);
            };
            row.appendChild(retry);
        }
        list.appendChild(row);
    }
}

// --- server icon + banner (270) ------------------------------------------------

async function loadServerIcon() {
    const generation = serverViewGeneration;
    try {
        const data = await App().ServerIconGet();
        if (generation !== serverViewGeneration) return;
        const el = document.getElementById("server-icon");
        const url = imageDataURL(data);
        if (url) {
            el.src = url;
            el.classList.remove("hidden");
        } else {
            el.removeAttribute("src");
            el.classList.add("hidden");
        }
    } catch {
        if (generation !== serverViewGeneration) return;
        const el = document.getElementById("server-icon");
        el.removeAttribute("src");
        el.classList.add("hidden");
    }
    // Both halves of the server's branding load on the same trigger (connect
    // and menu refresh), so the banner rides along here.
    if (generation === serverViewGeneration) loadServerBanner();
}

// bannerEl returns the banner image, creating it under the sidebar brand on
// first use. It is built here rather than in the page markup so the whole
// feature stays in one file.
function bannerEl() {
    let el = document.getElementById("server-banner");
    if (el) return el;
    const sidebar = document.getElementById("sidebar");
    const brand = sidebar && sidebar.querySelector(".brand");
    if (!brand) return null;
    el = document.createElement("img");
    el.id = "server-banner";
    el.alt = "";
    el.title = t("files.banner");
    el.style.cssText = "display:none;width:100%;max-height:96px;object-fit:cover;border-radius:6px;margin:6px 0";
    brand.after(el);
    return el;
}

async function loadServerBanner() {
    const generation = serverViewGeneration;
    const el = bannerEl();
    if (!el) return;
    try {
        const data = await App().ServerBannerGet();
        if (generation !== serverViewGeneration) return;
        const url = imageDataURL(data);
        if (url) {
            el.src = url;
            el.style.display = "";
        } else {
            el.removeAttribute("src");
            el.style.display = "none";
        }
    } catch {
        if (generation !== serverViewGeneration) return;
        el.removeAttribute("src");
        el.style.display = "none";
    }
}

// setServerBanner uploads a new banner (admin only). Reachable from the files
// toolbar; the server refuses non-admins.
async function setServerBanner() {
    // Banners are wide, so allow more pixels than a 256px icon; the quality
    // loop still keeps it under the server's 256 KiB cap (274).
    const generation = serverViewGeneration;
    const img = await pickIcon(1600, 0.85);
    if (!img || generation !== serverViewGeneration) return;
    const err = await App().ServerBannerSet(img.dataBase64);
    if (generation !== serverViewGeneration) return;
    if (err) {
        V().toast(t("files.bannerFailed", { error: String(err) }), "warn");
        return;
    }
    V().toast(t("files.bannerUpdated"));
    setTimeout(loadServerBanner, 400);
}

// --- channel icons (271) --------------------------------------------------------

// channelIcons caches one entry per channel: a data URL, or null for "asked,
// none set". Without the cache every tree re-render would re-fetch.
const channelIcons = new Map();

// decorateChannelIcons swaps the placeholder glyph in the channel tree for the
// real icon. The tree is rendered elsewhere and re-rendered often, so this
// runs from an observer rather than being woven into the render.
function decorateChannelIcons() {
    const rows = document.querySelectorAll("#channel-tree .channel[data-chid]");
    for (const row of rows) {
        const id = Number(row.dataset.chid);
        const slot = row.querySelector(".ch-icon");
        if (!slot || slot.dataset.iconFor === String(id)) continue;
        if (!channelIcons.has(id)) {
            const generation = serverViewGeneration;
            channelIcons.set(id, null); // claim it so concurrent passes do not refetch
            App().ChannelIconGet(id).then((d) => {
                if (generation !== serverViewGeneration) return;
                const url = imageDataURL(d);
                if (url) {
                    channelIcons.set(id, url);
                    decorateChannelIcons();
                }
            }).catch(() => { /* no icon */ });
            continue;
        }
        const url = channelIcons.get(id);
        if (!url) continue;
        slot.dataset.iconFor = String(id);
        slot.textContent = "";
        const img = document.createElement("img");
        img.src = url;
        img.alt = "";
        img.style.cssText = "width:14px;height:14px;border-radius:3px;object-fit:cover;vertical-align:-2px";
        slot.appendChild(img);
    }
}

// watchChannelIcons re-decorates after any tree re-render.
function watchChannelIcons() {
    const tree = document.getElementById("channel-tree");
    if (!tree) return;
    let queued = false;
    new MutationObserver(() => {
        if (queued) return;
        queued = true;
        setTimeout(() => { queued = false; decorateChannelIcons(); }, 50);
    }).observe(tree, { childList: true, subtree: true });
    decorateChannelIcons();
}

function resetServerView() {
    serverViewGeneration++;
    channelIcons.clear();
    fb.channelID = 0;
    fb.folder = "";
    fb.folders = [];
    fb.entries = [];
    fb.loaded = false;
    fb.used = 0;
    fb.quota = 0;
    const icon = document.getElementById("server-icon");
    if (icon) {
        icon.removeAttribute("src");
        icon.classList.add("hidden");
    }
    const banner = document.getElementById("server-banner");
    if (banner) {
        banner.removeAttribute("src");
        banner.style.display = "none";
    }
    if (fb.open) refreshFiles();
}

// --- emoji manager (272) --------------------------------------------------------

// The picker and its quick-upload live in the chat pane; this is the full
// asset manager for the same files — previews, rename, delete and upload in
// one place, reached from the media (files) pane.
async function openEmojiManager() {
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg">
            <div class="pm-head">
                <h3>${t("files.customEmoji")}</h3>
                <button class="icon-btn em-close" title="${t("common.close")}" aria-label="${t("common.close")}">${icon("close")}</button>
            </div>
            <div class="em-list">${t("files.loadingShort")}</div>
            <div class="dlg-buttons">
                <button class="em-add">${t("files.uploadEmoji")}</button>
            </div>
        </div>`;
    const close = () => overlay.remove();
    overlay.querySelector(".em-close").onclick = close;
    overlay.onclick = (e) => { if (e.target === overlay) close(); };
    overlay.querySelector(".em-add").onclick = () => addEmoji(overlay);
    mountServerDialog(overlay);
    renderEmojiList(overlay);
}

async function renderEmojiList(overlay) {
    const list = overlay.querySelector(".em-list");
    let resp;
    try {
        resp = await App().EmojiList();
    } catch (err) {
        if (!isCurrentServerDialog(overlay)) return;
        list.textContent = t("files.emojiListFailed", { error: String(err) });
        return;
    }
    if (!isCurrentServerDialog(overlay)) return;
    const emojis = resp.emojis || [];
    if (emojis.length === 0) {
        list.innerHTML = `<div class="empty-state">${t("files.noEmoji")}</div>`;
        return;
    }
    list.innerHTML = "";
    for (const e of emojis) {
        const row = document.createElement("div");
        row.className = "em-row";
        row.style.cssText = "display:flex;align-items:center;gap:8px;padding:4px 0";
        const img = document.createElement("img");
        img.style.cssText = "width:24px;height:24px;object-fit:contain";
        img.alt = e.name;
        const generation = serverViewGeneration;
        App().EmojiGet(e.name)
            .then((d) => {
                if (generation !== serverViewGeneration) return;
                const url = imageDataURL(d);
                if (url) img.src = url;
            })
            .catch(() => { /* preview is optional */ });
        const name = document.createElement("span");
        name.className = "mono";
        name.style.flex = "1";
        name.textContent = ":" + e.name + ":";
        const ren = document.createElement("button");
        ren.className = "icon-btn";
        ren.innerHTML = icon("edit");
        ren.setAttribute("aria-label", t("files.emojiRename"));
        ren.title = t("files.emojiRenameHelp");
        ren.onclick = async () => {
            if (!isCurrentServerDialog(overlay)) return;
            const generation = serverViewGeneration;
            const next = await promptDialog({
                title: t("files.emojiRename"),
                label: t("files.emojiNewName"),
                value: e.name,
                confirmLabel: t("files.renameConfirm"),
                serverScoped: true,
            });
            if (!next || next === e.name || generation !== serverViewGeneration || !isCurrentServerDialog(overlay)) return;
            const err = await App().EmojiRename(e.name, next);
            if (generation !== serverViewGeneration || !isCurrentServerDialog(overlay)) return;
            if (err) V().toast(t("files.renameFailed", { error: String(err) }), "warn");
            setTimeout(() => renderEmojiList(overlay), 400);
        };
        const del = document.createElement("button");
        del.className = "icon-btn";
        del.innerHTML = icon("trash");
        del.setAttribute("aria-label", t("files.emojiDelete"));
        del.title = t("files.delete");
        del.onclick = async () => {
            if (!isCurrentServerDialog(overlay)) return;
            const generation = serverViewGeneration;
            const confirmed = await confirmDialog({
                title: t("files.emojiDeleteTitle"),
                message: t("files.emojiDeleteWarning", { name: e.name }),
                confirmLabel: t("files.emojiDelete"),
                danger: true,
                serverScoped: true,
            });
            if (!confirmed || generation !== serverViewGeneration || !isCurrentServerDialog(overlay)) return;
            const err = await App().EmojiDelete(e.name);
            if (generation !== serverViewGeneration || !isCurrentServerDialog(overlay)) return;
            if (err) V().toast(t("files.deleteFailed", { error: String(err) }), "warn");
            setTimeout(() => renderEmojiList(overlay), 400);
        };
        row.append(img, name, ren, del);
        list.appendChild(row);
    }
}

async function addEmoji(overlay) {
    const generation = serverViewGeneration;
    const name = await promptDialog({
        title: t("files.emojiUpload"),
        label: t("files.emojiName"),
        confirmLabel: t("files.continue"),
        serverScoped: true,
    });
    if (!name || generation !== serverViewGeneration || !isCurrentServerDialog(overlay)) return;
    const img = await pickIcon(128, 0.9);
    if (!img || generation !== serverViewGeneration || !isCurrentServerDialog(overlay)) return;
    const err = await App().EmojiUpload(name, img.dataBase64);
    if (generation !== serverViewGeneration || !isCurrentServerDialog(overlay)) return;
    if (err) {
        V().toast(t("files.uploadFailed", { error: String(err) }), "warn");
        return;
    }
    setTimeout(() => renderEmojiList(overlay), 400);
}

// --- wiring ---------------------------------------------------------------------

export function initFilesUI() {
    const pane = document.getElementById("files-pane");
    document.addEventListener("pointerdown", event => {
        for (const menu of pane.querySelectorAll(".fb-action-menu[open]")) if (!menu.contains(event.target)) menu.open = false;
    });
    pane.innerHTML = `
        <div class="fb-toolbar">
            <span class="fb-crumb"></span>
            <span class="fb-spacer"></span>
            <button class="icon-btn fb-refresh" title="${t("files.refresh")}" aria-label="${t("files.refreshLabel")}">${icon("refresh")}</button>
            <button class="icon-btn fb-upload" title="${t("files.uploadFiles")}">${icon("upload")}<span>${t("files.upload")}</span></button>
            <button class="icon-btn fb-mkdir" title="${t("files.newFolderHelp")}" aria-label="${t("files.createFolder")}">${icon("folderPlus")}</button>
            <button class="icon-btn fb-emoji" title="${t("files.emojiManager")}" aria-label="${t("files.manageEmoji")}">${icon("smile")}</button>
            <button class="icon-btn fb-banner" title="${t("files.setBannerHelp")}" aria-label="${t("files.setBanner")}">${icon("image")}</button>
            <button class="icon-btn fb-transfers" title="Transfers" aria-label="${t("files.openTransfers")}">${icon("transfer")}</button>
        </div>
        <input class="fb-filter dlg-input" type="search" />
        <div class="fb-quota hidden"><div class="fb-quota-fill"></div></div>
        <div class="fb-list" aria-label="${t("files.channelFiles")}"></div>`;

    const filter = pane.querySelector(".fb-filter");
    filter.placeholder = t("files.filter");
    filter.setAttribute("aria-label", t("files.filter"));
    filter.oninput = () => { fb.filter = filter.value; renderFbList(); };
    filter.onkeydown = (event) => {
        if (event.key !== "Escape") return;
        event.preventDefault();
        filter.value = "";
        fb.filter = "";
        renderFbList();
    };
    pane.querySelector(".fb-refresh").onclick = refreshFiles;
    pane.querySelector(".fb-transfers").onclick = openTransfers;
    pane.querySelector(".fb-emoji").onclick = openEmojiManager;
    pane.querySelector(".fb-banner").onclick = setServerBanner;
    pane.querySelector(".fb-upload").onclick = async () => {
        // Native picker, not <input type=file>: it yields paths, which upload
        // by streaming instead of by base64 blob (259).
        const generation = serverViewGeneration;
        const paths = await App().PickUploadPaths();
        if (generation === serverViewGeneration && paths && paths.length) queueUploadPaths(paths);
    };
    pane.querySelector(".fb-mkdir").onclick = async () => {
        await runScopedDialogAction({
            readScope: readFileView,
            openDialog: () => promptDialog({
                title: t("files.createFolder"),
                label: t("files.folderName"),
                message: t("files.virtualFolder"),
                confirmLabel: t("files.createFolder"),
                serverScoped: true,
            }),
            isAccepted: Boolean,
            perform: async (scope, name) => {
                const clean = name.replace(/^\/+|\/+$/g, "");
                if (!clean || clean.includes("..") || clean.includes("\\")) {
                    V().toast(t("files.invalidFolder"), "warn");
                    return;
                }
                fb.folder = scope.folder ? scope.folder + "/" + clean : clean;
                refreshFiles();
                V().toast(t("files.folderReady"));
            },
        });
    };

    // (257) drag & drop upload onto the whole pane.
    pane.addEventListener("dragover", (e) => {
        e.preventDefault();
        pane.classList.add("drop-active");
    });
    pane.addEventListener("dragleave", () => pane.classList.remove("drop-active"));
    pane.addEventListener("drop", (e) => {
        e.preventDefault();
        pane.classList.remove("drop-active");
        if (e.dataTransfer.files.length) queueUploads([...e.dataTransfer.files]);
    });

    // Tab switching (chat <-> files).
    const tabChat = document.getElementById("tab-chat");
    const tabFiles = document.getElementById("tab-files");
    const tabs = [tabChat, tabFiles];
    tabChat.onclick = () => activateWorkspaceTab("chat");
    tabFiles.onclick = () => activateWorkspaceTab("files");
    tabs.forEach((tab, index) => {
        tab.addEventListener("keydown", (event) => {
            if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
            event.preventDefault();
            const next = event.key === "Home" ? 0
                : event.key === "End" ? tabs.length - 1
                    : wrappedIndex(index, tabs.length, event.key === "ArrowRight" ? 1 : -1);
            activateWorkspaceTab(next === 1 ? "files" : "chat", { focus: true });
        });
    });
    document.getElementById("tab-transfers").onclick = openTransfers;

    window.addEventListener("noxa-language-changed", () => {
        renderFbChrome();
        if (fb.loaded) renderFbList();
        else if (!fb.channelID) pane.querySelector(".fb-list").innerHTML = `<div class="empty-state">${t("files.join")}</div>`;
        if (trWin.open) {
            trWin.overlay.querySelector("h3").textContent = t("files.transfers");
            renderTransfers();
        }
    });
    window.runtime.EventsOn("ft_progress", trackTransfer);

    // (270/271) branding changes announced by the server: drop the cached copy
    // so the next paint shows the new image instead of waiting for a reconnect.
    window.runtime.EventsOn("event", (json) => {
        const env = parseRuntimeObject(json);
        if (!env) return;
        if (env.type === "server_banner_changed") {
            loadServerBanner();
        } else if (env.type === "channel_icon_changed") {
            const id = (env.data || {}).channel_id;
            if (!id) return;
            channelIcons.delete(id);
            document.querySelectorAll(`#channel-tree .channel[data-chid="${id}"] .ch-icon`)
                .forEach((el) => { delete el.dataset.iconFor; });
            decorateChannelIcons();
        }
    });
    watchChannelIcons();

    window.__noxaFiles = {
        activateWorkspaceTab, restoreVisibleWorkspaceFocus, refreshFiles, openTransfers, loadServerIcon, resetServerView,
        // Follows channel changes (256): browsing follows the channel I'm in.
        onChannelChanged() {
            if (fb.open) {
                fb.channelID = V().state.myChannelID;
                fb.folder = "";
                refreshFiles();
            }
        },
    };
}
