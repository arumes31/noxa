// updater.js — Interactive and silent automatic update checks.
import { closeDialog, mountDialog } from "./modal.js";
import { t } from "./i18n.js";
import { formatBytes } from "./connection-stats.js";

const V = () => window.__noxa;

let modal = null;
let startupChecked = false;
let startupCheck = Promise.resolve(false);
let downloading = false;
let restarting = false;
let applied = false;
let automaticCheck = null;
const progressCleanup = new WeakMap();
const checking = new WeakSet();

function closeModal() {
    if (modal && !downloading && !restarting) {
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
                <div class="upd-speed mono"></div>
            </div>
            <div class="dlg-buttons">
                <button class="upd-retry hidden">${t("common.retry")}</button>
                <button class="upd-cancel hidden">${t("updater.cancelDownload")}</button>
                <button class="upd-update hidden">${t("updater.updateNow")}</button>
                <button class="upd-close">${t("common.close")}</button>
            </div>
        </div>`;
    modal.querySelector(".upd-close").onclick = closeModal;
    modal.onclick = (e) => { if (e.target === modal) closeModal(); };
    const mounted = modal;
    mounted.querySelector(".upd-retry").onclick = () => runCheck(mounted);
    mountDialog(modal, {
        onCancel: () => !downloading && !restarting,
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

async function startDownload(m, info, restartOnSuccess = false) {
    if (downloading || applied) return;
    downloading = true;
    const status = m.querySelector(".upd-status");
    const btn = m.querySelector(".upd-update");
    const prog = m.querySelector(".upd-progress");
    const cancel = m.querySelector(".upd-cancel");
    let phase = "downloading";
    let attemptActive = true;
    cancel.classList.remove("hidden");
    cancel.disabled = false;
    cancel.textContent = t("updater.cancelDownload");
    cancel.onclick = async () => {
        cancel.disabled = true;
        cancel.textContent = t("updater.cancelling");
        try {
            const accepted = await window.go.main.App.CancelUpdate();
            if (!attemptActive) return;
            if (!accepted) cancel.classList.add("hidden");
        } catch (error) {
            if (!attemptActive || phase !== "downloading") return;
            status.textContent = t("updater.cancelFailed", { error });
            cancel.disabled = false;
            cancel.textContent = t("updater.cancelDownload");
        }
    };
    btn.disabled = true;
    btn.textContent = t("updater.processing");
    m.querySelector(".upd-close").disabled = true;
    status.classList.remove("warn");
    prog.classList.remove("hidden");
    status.textContent = t("updater.downloading");

    const fill = m.querySelector(".upd-fill");
    const pct = m.querySelector(".upd-pct");
    const speed = m.querySelector(".upd-speed");
    const showSpeed = rate => { speed.textContent = t("updater.speed", { speed: rate === null ? "—" : `${formatBytes(rate)}/s` }); };
    let stallTimer = null;
    fill.style.width = "0%";
    pct.textContent = "0%";
    showSpeed(null);
    const onProgress = (p, details) => {
        if (phase !== "downloading") return;
        if (Number.isFinite(p)) {
            fill.style.width = Math.max(0, Math.min(100, p)) + "%";
            pct.textContent = p >= 0 ? Math.min(100, p) + "%" : "—";
        }
        clearTimeout(stallTimer);
        const rate = details?.bytes_per_second;
        if (Number.isFinite(rate) && rate >= 0) {
            showSpeed(rate);
            stallTimer = setTimeout(() => showSpeed(0), 1500);
        } else {
            showSpeed(null);
        }
    };
    let progressUnsub = window.runtime.EventsOn("update_progress", onProgress);
    let phaseUnsub = window.runtime.EventsOn("update_phase", nextPhase => {
        if (!["downloading", "verifying", "installing", "ready"].includes(nextPhase)) return;
        phase = nextPhase;
        status.textContent = t(`updater.${phase === "ready" ? "restartRequired" : phase}`);
        if (phase !== "downloading") {
            clearTimeout(stallTimer);
            cancel.classList.add("hidden");
            prog.classList.add("hidden");
        }
    });
    const unsubscribe = () => {
        clearTimeout(stallTimer);
        phaseUnsub?.();
        phaseUnsub = null;
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
            const cancelled = err === "update cancelled";
            status.textContent = cancelled ? t("updater.cancelled") : t("updater.updateFailed", { error: err });
            status.classList.toggle("warn", !cancelled);
            prog.classList.add("hidden");
            btn.disabled = false;
            return;
        }
        applied = true;
        showRestart(m);
        if (restartOnSuccess) return await restartUpdate(m);
    } catch (e) {
        unsubscribe();
        status.textContent = t("updater.updateFailed", { error: e });
        status.classList.add("warn");
        prog.classList.add("hidden");
        btn.disabled = false;
    } finally {
        attemptActive = false;
        downloading = false;
        cancel.classList.add("hidden");
        if (!applied) btn.textContent = t("updater.updateNow");
        m.querySelector(".upd-close").disabled = false;
    }
}

function showRestart(m) {
    const status = m.querySelector(".upd-status");
    const btn = m.querySelector(".upd-update");
    m.querySelector(".upd-progress").classList.add("hidden");
    status.textContent = t("updater.restartRequired");
    status.classList.remove("warn");
    btn.classList.remove("hidden");
    btn.textContent = t("updater.restartNow");
    btn.disabled = false;
    btn.onclick = () => restartUpdate(m);
}

async function restartUpdate(m) {
    const status = m.querySelector(".upd-status");
    const btn = m.querySelector(".upd-update");
    if (restarting) return;
    restarting = true;
    btn.disabled = true;
    btn.textContent = t("updater.processing");
    m.querySelector(".upd-close").disabled = true;
    status.textContent = t("updater.starting");
    try {
        const error = await window.go.main.App.ApplyAndRestart();
        if (error) throw new Error(error);
        return true;
    } catch (error) {
        status.textContent = t("updater.restartFailed", { error });
        status.classList.add("warn");
        btn.disabled = false;
        // A failed replacement may have been rolled back by the native
        // launcher. Offer a fresh download instead of restarting it again.
        try {
            if (await window.go.main.App.GetUpdatePhase() === "restored") {
                applied = false;
                btn.textContent = t("updater.updateNow");
                btn.classList.add("hidden");
                m.querySelector(".upd-retry").classList.remove("hidden");
            }
        } catch { /* Keep the original restart error visible. */ }
        return false;
    } finally {
        restarting = false;
        if (applied) btn.textContent = t("updater.restartNow");
        m.querySelector(".upd-close").disabled = false;
    }
}

async function checkForUpdatesInteractive() {
    if (modal) return;
    const m = showUpdateModal();
    await runCheck(m);
}

async function quietUpdateCheck(enabled, current = async () => true, autoInstall = () => false) {
    if (!enabled() || modal || downloading || restarting || applied) return;
    try {
        // Startup and simultaneous server recoveries share one metadata request.
        automaticCheck ||= Promise.resolve().then(() => window.go.main.App.CheckForUpdate())
            .finally(() => { automaticCheck = null; });
        const info = await automaticCheck;
        if (!info?.available || !enabled() || modal || applied || !await current()) return;
        if (enabled() && !modal && !applied) {
            const m = showUpdateModal();
            await runCheck(m, info);
            if (autoInstall() && m.isConnected && modal === m && !applied) {
                return await startDownload(m, info, true);
            }
        }
    } catch { /* Offline or no update source: stay quiet. */ }
}

// Only this once-per-app-start path may install automatically. Reconnects
// share metadata requests, never this installation permission.
export function startupAutoCheck() {
    if (startupChecked) return startupCheck;
    startupChecked = true;
    // A failed settings load cannot silently grant installation permission.
    const install = () => !!V().state.settings && V().state.settings.updates_auto_install !== false;
    startupCheck = quietUpdateCheck(() => install() || V().state.settings?.updates_auto_check !== false, undefined, install);
    return startupCheck;
}

export async function reconnectAutoCheck(tabID) {
    if (await startupCheck) return;
    const enabled = () => V().state.settings?.updates_check_on_reconnect !== false;
    if (!tabID || !enabled() || modal || downloading || restarting || applied) return;
    try {
        const session = await window.go.main.App.SessionInfoForTab(tabID);
        if (!session?.connected || !session.client_id) return;
        await quietUpdateCheck(enabled, async () => {
            const latest = await window.go.main.App.SessionInfoForTab(tabID);
            return latest?.connected && latest.client_id === session.client_id;
        });
    } catch { /* A closing or disconnected tab cannot offer an update. */ }
}

export function initUpdater() {
    window.__noxa.checkForUpdatesInteractive = checkForUpdatesInteractive;
    window.__noxa.startupAutoCheck = startupAutoCheck;
    window.__noxa.startupUpdateFinished = () => startupCheck;
    window.__noxa.reconnectAutoCheck = reconnectAutoCheck;
}
