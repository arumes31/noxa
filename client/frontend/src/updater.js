// updater.js — Interactive and silent automatic update checks.
import { closeDialog, mountDialog } from "./modal.js";
import { t } from "./i18n.js";
import { formatBytes } from "./connection-stats.js";
import { createUpdateCountdown } from "./update-countdown.js";
import { measuredUpdateProgress } from "./update-core.js";
import { createUpdateLogo } from "./update-logo.js";
import "./updater.css";

const V = () => window.__noxa;

let modal = null;
let startupChecked = false;
let startupCheck = Promise.resolve(false);
let downloading = false;
let restarting = false;
let restartCompleted = false;
let applied = false;
let automaticCheck = null;
const progressCleanup = new WeakMap();
const checking = new WeakSet();
const cores = new WeakMap();
const countdowns = new WeakMap();

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
        <div class="dlg dlg-wide update-dlg" data-view="offer">
            <header class="upd-header">
                <div class="upd-brand"><img src="/branding/icon-192.png" alt="" width="43" height="43"><span>noXa</span></div>
                <h3>${t("updater.title")}</h3>
                <div class="upd-route mono"></div>
            </header>
            <div class="upd-body">
                <div class="upd-offer hidden">
                    <h4>${t("updater.offerTitle")}</h4>
                    <div class="upd-versions">
                        <div class="upd-version"><span>${t("updater.currentLabel")}</span><strong class="upd-current mono"></strong></div>
                        <span class="upd-version-arrow" aria-hidden="true">→</span>
                        <div class="upd-version upd-version-new"><span>${t("updater.newLabel")}</span><strong class="upd-next mono">—</strong></div>
                    </div>
                </div>
                <div class="upd-stage" aria-hidden="true"><canvas class="upd-core"></canvas></div>
                <div class="upd-status" role="status" aria-live="polite"></div>
                <div class="upd-progress hidden" role="progressbar" aria-label="${t("updater.progressLabel")}" aria-valuemin="0" aria-valuemax="100">
                    <div class="upd-metrics"><div><div class="upd-speed mono"></div><div class="upd-bits mono"></div></div><div class="upd-pct mono">—</div></div>
                    <div class="upd-details"><div class="upd-size"><span class="upd-size-label">${t("updater.downloadedLabel")}</span><span class="upd-bytes mono">—</span></div><span class="upd-blocks mono">— / 80</span></div>
                    <div class="upd-bar"><div class="upd-fill"></div></div>
                </div>
                <div class="upd-countdown hidden" role="status" aria-live="polite" aria-atomic="true"></div>
            </div>
            <footer class="upd-footer">
                <span class="upd-block-size"></span>
                <div class="dlg-buttons">
                    <button class="upd-retry hidden">${t("common.retry")}</button>
                    <button class="upd-cancel hidden">${t("updater.cancelDownload")}</button>
                    <button class="upd-close">${t("common.close")}</button>
                    <button class="upd-update hidden">${t("updater.updateNow")}</button>
                </div>
            </footer>
        </div>`;
    modal.querySelector(".upd-close").onclick = closeModal;
    modal.onclick = (e) => { if (e.target === modal) closeModal(); };
    const mounted = modal;
    cores.set(mounted, createUpdateLogo(mounted.querySelector(".upd-core")));
    mounted.querySelector(".upd-retry").onclick = () => runCheck(mounted);
    mountDialog(modal, {
        onCancel: () => !downloading && !restarting,
        onClose: () => {
            countdowns.get(mounted)?.cancel();
            countdowns.delete(mounted);
            cores.get(mounted)?.dispose();
            cores.delete(mounted);
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
    m.querySelector(".update-dlg").dataset.view = "offer";
    const cur = m.querySelector(".upd-current");
    const retry = m.querySelector(".upd-retry");
    cur.textContent = V().state.clientVersion || "dev";
    cur.setAttribute("aria-label", t("updater.currentBuild", { version: cur.textContent }));
    if (applied) {
        showRestart(m);
        return;
    }
    m.querySelector(".upd-offer").classList.add("hidden");
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

    m.querySelector(".upd-offer").classList.remove("hidden");
    showRoute(m, V().state.clientVersion || "dev", info.version || t("updater.unknownVersion"));
    m.querySelector(".upd-next").textContent = String(info.version || t("updater.unknownVersion"));
    const version = document.createElement("b");
    version.textContent = String(info.version || t("updater.unknownVersion"));
    const size = Number(info.size);
    const sizeLabel = Number.isFinite(size) && size > 0 ? `${(size / 1024 / 1024).toFixed(1)} MiB` : t("updater.unknownSize");
    status.replaceChildren(t("updater.available") + " ", version, ` (${sizeLabel})`);
    const btn = m.querySelector(".upd-update");
    btn.classList.remove("hidden");
    btn.onclick = () => startDownload(m, info);
}

async function startDownload(m, info) {
    if (downloading || applied || modal !== m || !m.isConnected) return;
    countdowns.get(m)?.cancel();
    countdowns.delete(m);
    m.querySelector(".upd-countdown").classList.add("hidden");
    m.querySelector(".update-dlg").dataset.view = "download";
    m.querySelector(".update-dlg").dataset.phase = "downloading";
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
    const showSpeed = rate => {
        const value = rate === null ? "—" : `${formatBytes(rate)}/s`;
        // Keep the localized label available to screen readers and older clients.
        speed.replaceChildren();
        const label = document.createElement("span");
        label.className = "upd-speed-label";
        label.textContent = t("updater.speed", { speed: "" });
        const number = document.createElement("strong");
        number.textContent = value;
        speed.append(label, number);
        m.querySelector(".upd-bits").textContent = rate === null ? "— Mbit/s" : `${(rate * 8 / 1000000).toFixed(2)} Mbit/s`;
    };
    let stallTimer = null;
    fill.style.width = "0%";
    pct.textContent = "—";
    prog.removeAttribute("aria-valuenow");
    m.querySelector(".upd-bytes").textContent = "—";
    m.querySelector(".upd-blocks").textContent = t("updater.blocks", { count: "—" });
    m.querySelector(".upd-block-size").textContent = t("updater.unknownSize");
    cores.get(m)?.update(null);
    showSpeed(null);
    const onProgress = (p, details) => {
        if (!attemptActive || phase !== "downloading" || !m.isConnected) return;
        const measured = measuredUpdateProgress(p, details);
        const percent = measured.ratio === null ? null : Math.floor(measured.ratio * 100);
        fill.style.width = `${percent ?? 0}%`;
        pct.textContent = percent === null ? "—" : `${percent}%`;
        if (percent === null) prog.removeAttribute("aria-valuenow");
        else prog.setAttribute("aria-valuenow", String(percent));
        m.querySelector(".upd-bytes").textContent = measured.downloaded === null ? "—"
            : `${formatBytes(measured.downloaded)} / ${measured.total === null ? "—" : formatBytes(measured.total)}`;
        m.querySelector(".upd-blocks").textContent = t("updater.blocks", { count: measured.blocks ?? "—" });
        m.querySelector(".upd-block-size").textContent = measured.total === null ? t("updater.unknownSize")
            : t("updater.blockSize", { size: formatBytes(measured.total / 80) });
        cores.get(m)?.update(measured.ratio);
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
        if (!attemptActive || !m.isConnected || !["downloading", "verifying", "installing", "ready"].includes(nextPhase)) return;
        phase = nextPhase;
        m.querySelector(".update-dlg").dataset.phase = phase;
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
            // The native updater refuses to install a different release from
            // the one shown. Discard this stale offer and require a fresh check
            // and confirmation instead of repeatedly submitting its version.
            if (err.startsWith("update changed from ") || err === "update is no longer available") {
                btn.classList.add("hidden");
                btn.onclick = null;
                m.querySelector(".upd-retry").classList.remove("hidden");
            }
            return;
        }
        applied = true;
        m.querySelector(".update-dlg").dataset.phase = "ready";
        cores.get(m)?.update(1);
        // Native success means verification AND staging have completed. A progress
        // event (even ready/100%) never grants permission to restart.
        if (!m.isConnected || modal !== m) return false;
        downloading = false;
        attemptActive = false;
        cancel.classList.add("hidden");
        m.querySelector(".upd-close").disabled = false;
        showRestart(m);
        const countdown = createUpdateCountdown({
            current: () => applied && modal === m && m.isConnected,
            tick: seconds => {
                const label = m.querySelector(".upd-countdown");
                label.classList.toggle("hidden", seconds === 0);
                label.textContent = seconds ? t("updater.countdown", { seconds }) : "";
            },
            restart: () => restartUpdate(m),
        });
        countdowns.set(m, countdown);
        btn.onclick = () => countdown.startNow();
        return await countdown.done;
    } catch (e) {
        unsubscribe();
        status.textContent = t("updater.updateFailed", { error: e });
        status.classList.add("warn");
        prog.classList.add("hidden");
        btn.disabled = false;
    } finally {
        attemptActive = false;
        m.querySelector(".update-dlg").dataset.phase = applied ? "ready" : "error";
        downloading = false;
        cancel.classList.add("hidden");
        if (!applied) btn.textContent = t("updater.updateNow");
        m.querySelector(".upd-close").disabled = false;
    }
}

// The splash keeps the old → new route on screen for the whole download.
function showRoute(m, current, next) {
    const route = m.querySelector(".upd-route");
    const from = document.createElement("span");
    from.className = "upd-route-from";
    from.textContent = String(current);
    const arrow = document.createElement("span");
    arrow.className = "upd-route-arrow";
    arrow.setAttribute("aria-hidden", "true");
    arrow.textContent = "→";
    const to = document.createElement("span");
    to.className = "upd-route-to";
    to.textContent = String(next);
    route.replaceChildren(from, arrow, to);
    route.setAttribute("aria-label", t("updater.route", { current, next }));
}

function showRestart(m) {
    cores.get(m)?.update(1);
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
    if (restarting || restartCompleted || !applied || modal !== m || !m.isConnected) return false;
    m.querySelector(".upd-countdown").classList.add("hidden");
    restarting = true;
    btn.disabled = true;
    btn.textContent = t("updater.processing");
    m.querySelector(".upd-close").disabled = true;
    status.textContent = t("updater.starting");
    try {
        const error = await window.go.main.App.ApplyAndRestart();
        if (error) throw new Error(error);
        restartCompleted = true;
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
        if (applied) {
            btn.textContent = t("updater.restartNow");
            btn.disabled = restartCompleted;
            btn.onclick = () => restartUpdate(m);
        }
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
                return await startDownload(m, info);
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
