// updater.js — Check for updates modal + startup auto-check.
import { closeDialog, mountDialog } from "./modal.js";
import { t } from "./i18n.js";

const V = () => window.__voicx;

let modal = null;
let startupChecked = false;
let downloading = false;
let applied = false;
const progressCleanup = new WeakMap();
const checking = new WeakSet();

function closeModal() {
    if (modal && !downloading) {
        closeDialog(modal);
    }
}

function showUpdateModal() {
    if (modal) return modal;
    modal = document.createElement("div");
    modal.className = "dlg-overlay";
    modal.innerHTML = `
        <div class="dlg dlg-wide update-dlg">
            <h3>${t("updater.title")}</h3>
            <div class="upd-current mono"></div>
            <div class="upd-status" role="status" aria-live="polite"></div>
            <div class="upd-progress hidden">
                <div class="upd-bar"><div class="upd-fill"></div></div>
                <div class="upd-pct mono"></div>
            </div>
            <div class="dlg-buttons">
                <button class="upd-retry hidden">${t("common.retry")}</button>
                <button class="upd-update hidden">${t("updater.updateNow")}</button>
                <button class="upd-close">${t("common.close")}</button>
            </div>
        </div>`;
    modal.querySelector(".upd-close").onclick = closeModal;
    modal.onclick = (e) => { if (e.target === modal) closeModal(); };
    const mounted = modal;
    mounted.querySelector(".upd-retry").onclick = () => runCheck(mounted);
    mountDialog(modal, {
        onCancel: () => !downloading,
        onClose: () => {
            progressCleanup.get(mounted)?.();
            progressCleanup.delete(mounted);
            if (modal === mounted) modal = null;
        },
    });
    return modal;
}

async function runCheck(m, knownInfo) {
    if (checking.has(m) || !m.isConnected) return;
    const status = m.querySelector(".upd-status");
    const cur = m.querySelector(".upd-current");
    const retry = m.querySelector(".upd-retry");
    cur.textContent = t("updater.currentBuild", { version: V().state.clientVersion || "dev" });
    if (applied) {
        showRestart(m);
        return;
    }
    checking.add(m);
    retry.disabled = true;
    status.classList.remove("warn");
    status.textContent = t("updater.checking");

    let info;
    try {
        info = knownInfo || await window.go.main.App.CheckForUpdate();
    } catch (e) {
        if (!m.isConnected) return;
        status.textContent = t("updater.checkFailed", { error: e });
        status.classList.add("warn");
        retry.classList.remove("hidden");
        return;
    } finally {
        checking.delete(m);
        retry.disabled = false;
    }
    if (!m.isConnected) return;
    retry.classList.add("hidden");
    if (!info.available) {
        status.textContent = t("updater.upToDate", { version: info.version || "dev" });
        return;
    }

    const version = document.createElement("b");
    version.textContent = String(info.version || t("updater.unknownVersion"));
    const size = Number(info.size);
    const sizeMiB = Number.isFinite(size) && size >= 0 ? (size / 1024 / 1024).toFixed(1) : "0.0";
    status.replaceChildren(t("updater.available") + " ", version, ` (${sizeMiB} MiB)`);
    const btn = m.querySelector(".upd-update");
    btn.classList.remove("hidden");
    btn.onclick = () => startDownload(m, info);
}

async function startDownload(m, info) {
    if (downloading || applied) return;
    downloading = true;
    const status = m.querySelector(".upd-status");
    const btn = m.querySelector(".upd-update");
    const prog = m.querySelector(".upd-progress");
    btn.disabled = true;
    m.querySelector(".upd-close").disabled = true;
    status.classList.remove("warn");
    prog.classList.remove("hidden");
    status.textContent = t("updater.downloading");

    const fill = m.querySelector(".upd-fill");
    const pct = m.querySelector(".upd-pct");
    fill.style.width = "0%";
    pct.textContent = "0%";
    const onProgress = (p) => {
        if (p >= 0) {
            fill.style.width = p + "%";
            pct.textContent = p + "%";
        }
    };
    let progressUnsub = window.runtime.EventsOn("update_progress", onProgress);
    const unsubscribe = () => {
        if (!progressUnsub) return;
        progressUnsub();
        progressUnsub = null;
        if (progressCleanup.get(m) === unsubscribe) progressCleanup.delete(m);
    };
    progressCleanup.set(m, unsubscribe);

    try {
        const err = await window.go.main.App.DownloadAndApply(info);
        unsubscribe();
        if (err) {
            status.textContent = t("updater.updateFailed", { error: err });
            status.classList.add("warn");
            prog.classList.add("hidden");
            btn.disabled = false;
            return;
        }
        applied = true;
        showRestart(m);
    } catch (e) {
        unsubscribe();
        status.textContent = t("updater.updateFailed", { error: e });
        status.classList.add("warn");
        prog.classList.add("hidden");
        btn.disabled = false;
    } finally {
        downloading = false;
        m.querySelector(".upd-close").disabled = false;
    }
}

function showRestart(m) {
    const status = m.querySelector(".upd-status");
    const btn = m.querySelector(".upd-update");
    status.textContent = t("updater.restartRequired");
    status.classList.remove("warn");
    btn.classList.remove("hidden");
    btn.textContent = t("updater.restartNow");
    btn.disabled = false;
    btn.onclick = async () => {
        btn.disabled = true;
        try {
            const error = await window.go.main.App.ApplyAndRestart();
            if (error) throw new Error(error);
        } catch (error) {
            status.textContent = t("updater.restartFailed", { error });
            status.classList.add("warn");
            btn.disabled = false;
        }
    };
}

async function checkForUpdatesInteractive() {
    if (modal) return;
    const m = showUpdateModal();
    await runCheck(m);
}

// startupAutoCheck runs once when the login screen is ready (Application setting:
// updates.auto_check, default on).
export function startupAutoCheck() {
    if (startupChecked) return;
    startupChecked = true;
    const s = V().state.settings;
    if (s && s.updates_auto_check === false) return;
    window.go.main.App.CheckForUpdate().then((info) => {
        if (info.available && !modal) {
            return runCheck(showUpdateModal(), info);
        }
    }).catch(() => { /* offline or no update source: stay quiet */ });
}

export function initUpdater() {
    window.__voicx.checkForUpdatesInteractive = checkForUpdatesInteractive;
    window.__voicx.startupAutoCheck = startupAutoCheck;
}
