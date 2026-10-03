import { escapeHTML as escapeTranslation } from "./markdown.js";
// meta-ui.js — wave-8b meta features: debug console (327), connection stats
// page (328), onboarding wizard (329), what's-new dialog (330), crash report
// toast (331).
import { closeDialog, mountDialog, mountServerDialog } from "./modal.js";
import { openServerInfo } from "./server-info.js";
import { t } from "./i18n.js";

const V = () => window.__noxa;
const App = () => window.go.main.App;

// ---------------------------------------------------------------------------
// Debug console (327)
// ---------------------------------------------------------------------------

const dbg = { open: false, paused: false, filter: "", rows: [] };

function openDebugConsole() {
    if (dbg.open) return;
    dbg.open = true;
    dbg.rowElements = new Map();
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg debug-console">
            <div class="pm-head">
                <h3>${escapeTranslation(t("desktop.debug.console"))}</h3>
                <button class="icon-btn dbg-close" title="${escapeTranslation(t("desktop.close"))}">✕</button>
            </div>
            <div class="dbg-toolbar">
                <input class="dlg-input dbg-filter" placeholder="${escapeTranslation(t("desktop.filter.by.type"))}" />
                <button class="dbg-pause">${escapeTranslation(t("desktop.pause"))}</button>
                <button class="dbg-clear">${escapeTranslation(t("desktop.clear"))}</button>
            </div>
            <div class="dbg-list mono" tabindex="0"></div>
            <div class="dbg-follow"><button class="dbg-jump hidden" type="button"></button></div>
        </div>`;
    dbg.overlay = overlay;
    const close = () => closeDialog(overlay);
    const cleanup = (reason) => {
        dbg.open = false;
        dbg.rowElements.clear();
        if (dbg.overlay === overlay) dbg.overlay = null;
        // A tab reset has already activated another backend; never send the
        // old dialog's teardown command to that newly active connection.
        if (reason !== "server-change") App().SetDebugFrames(false);
    };
    overlay.querySelector(".dbg-close").onclick = close;
    overlay.onclick = (e) => { if (e.target === overlay) close(); };
    overlay.querySelector(".dbg-filter").oninput = (e) => {
        dbg.filter = e.target.value.trim().toLowerCase();
        renderDbg(true);
    };
    overlay.querySelector(".dbg-pause").onclick = (e) => {
        dbg.paused = !dbg.paused;
        e.target.textContent = dbg.paused ? t("desktop.resume") : t("desktop.pause");
    };
    overlay.querySelector(".dbg-clear").onclick = () => {
        dbg.rows = [];
        renderDbg(true);
    };
    const list = overlay.querySelector(".dbg-list");
    list.setAttribute("aria-label", t("debug.events"));
    const jump = overlay.querySelector(".dbg-jump");
    jump.textContent = t("debug.jumpLatest");
    list.onscroll = updateDebugJump;
    jump.onclick = () => {
        list.scrollTop = list.scrollHeight;
        list.focus({ preventScroll: true });
        updateDebugJump();
    };
    mountServerDialog(overlay, { onClose: cleanup });
    App().SetDebugFrames(true);
    renderDbg(true);
}

function debugAtBottom(list) {
    return list.scrollHeight - list.clientHeight - list.scrollTop <= 4;
}

function updateDebugJump() {
    if (!dbg.open) return;
    const list = dbg.overlay.querySelector(".dbg-list");
    dbg.overlay.querySelector(".dbg-jump").classList.toggle("hidden", debugAtBottom(list));
}

function renderDbg(jumpToLatest = false) {
    if (!dbg.open) return;
    const list = dbg.overlay.querySelector(".dbg-list");
    const followLatest = jumpToLatest || debugAtBottom(list);
    const anchor = !followLatest
        ? [...list.children].find(row => row.getBoundingClientRect().bottom > list.getBoundingClientRect().top)
        : null;
    const anchorTop = anchor?.getBoundingClientRect().top;
    const rows = dbg.rows.filter((r) => !dbg.filter || r.type.toLowerCase().includes(dbg.filter)).slice(-200);
    for (const [frame, row] of dbg.rowElements) {
        if (rows.includes(frame)) continue;
        row.remove();
        dbg.rowElements.delete(frame);
    }
    for (const frame of rows) {
        if (dbg.rowElements.has(frame)) continue;
        const row = document.createElement("div");
        row.className = "dbg-row " + (frame.dir === "in" ? "in" : "out");
        row.innerHTML = `<span class="dbg-dir">${frame.dir === "in" ? "◀" : "▶"}</span> <b>${escapeHtml(frame.type)}</b> <span class="dbg-payload">${escapeHtml(frame.payload)}</span>`;
        dbg.rowElements.set(frame, row);
        list.insertBefore(row, list.children[rows.indexOf(frame)] || null);
    }
    if (followLatest) list.scrollTop = list.scrollHeight;
    else if (anchor?.isConnected) list.scrollTop += anchor.getBoundingClientRect().top - anchorTop;
    updateDebugJump();
}

function escapeHtml(s) {
    return String(s ?? "").replace(/[&<>]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" }[c]));
}

// ---------------------------------------------------------------------------
// Connection stats page (328)
// ---------------------------------------------------------------------------

const openStatsPage = openServerInfo;

// ---------------------------------------------------------------------------
// Onboarding (329) + crash toast (331)
// ---------------------------------------------------------------------------

// maybeOnboard shows the first-run wizard (skipped when done before).
function maybeOnboard() {
    const s = V().state.settings;
    if (!s || s.onboarding_done || document.querySelector(".dlg.onboarding")) return;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    let step = 0;
    const steps = [
        { title: t("desktop.welcome.to.noxa"), body: `<p>${escapeTranslation(t("desktop.your"))} <b>${escapeTranslation(t("desktop.identity"))}</b> ${escapeTranslation(t("desktop.is.an.ed25519.key.pair.generated.locally.it.is.your"))}</p><p>${escapeTranslation(t("desktop.pick.a.nickname.for.your.first.connect"))}</p><input class="dlg-input ob-nick" placeholder="${escapeTranslation(t("desktop.nickname"))}" />` },
        { title: t("desktop.microphone.check"), body: `<p>${escapeTranslation(t("desktop.open.settings.capture.and.use.the"))} <b>${escapeTranslation(t("desktop.mic.test"))}</b> ${escapeTranslation(t("desktop.to.verify.your.input.level"))}</p><button class="dlg-ok ob-mic">${escapeTranslation(t("desktop.open.capture.settings"))}</button>` },
        { title: t("desktop.connect"), body: `<p>${escapeTranslation(t("desktop.enter.a.server.address.and.connect.bookmarks.and.recents.make"))}</p>` },
    ];
    let completed = false;
    const done = async () => {
        if (completed) return;
        completed = true;
        s.onboarding_done = true;
        await App().SaveSettings(s);
    };
    const render = () => {
        const st = steps[step];
        overlay.innerHTML = `
            <div class="dlg onboarding">
                <h3>${st.title}</h3>
                <div class="dlg-text ob-body">${st.body}</div>
                <div class="dlg-buttons">
                    <button class="dlg-cancel ob-skip">${escapeTranslation(t("desktop.skip"))}</button>
                    <button class="dlg-ok ob-next">${escapeTranslation(t(step === steps.length - 1 ? "runtime.done" : "runtime.next"))}</button>
                </div>
                <div class="ob-dots">${steps.map((_, i) => i === step ? "●" : "○").join(" ")}</div>
            </div>`;
        const mic = overlay.querySelector(".ob-mic");
        if (mic) mic.onclick = () => {
            closeDialog(overlay);
            void done();
            window.__noxa.openSettings("capture");
        };
        overlay.querySelector(".ob-skip").onclick = () => {
            closeDialog(overlay);
            void done();
        };
        overlay.querySelector(".ob-next").onclick = () => {
            const nick = overlay.querySelector(".ob-nick");
            if (nick && nick.value.trim()) document.getElementById("login-nick").value = nick.value.trim();
            step++;
            if (step >= steps.length) {
                closeDialog(overlay);
                void done();
            } else {
                render();
            }
        };
    };
    render();
    mountDialog(overlay, { onClose: () => { void done(); } });
}

// maybeCrashToast reports a previous crash (331).
async function maybeCrashToast() {
    try {
        const crash = await App().LastCrash();
        if (!crash) return;
        V().toast(t("desktop.noxa.crashed.last.time.a.crash.log.was.saved.help"), "warn", "conn");
        V().sysMsg(t("runtime.previousCrash"));
    } catch { /* best-effort */ }
}

// ---------------------------------------------------------------------------

export function initMetaUI() {
    window.runtime.EventsOn("debug_frame", (r) => {
        if (!dbg.open || dbg.paused) return;
        dbg.rows.push(r);
        if (dbg.rows.length > 500) dbg.rows.shift();
        renderDbg();
    });
    window.__noxaMeta = { openDebugConsole, openStatsPage, openServerInfo, maybeOnboard };
    for (const id of ["server-name", "voice-latency"]) {
        document.getElementById(id)?.addEventListener("click", openServerInfo);
    }
    // Startup flows: crash report and onboarding.
    setTimeout(() => {
        maybeCrashToast();
        setTimeout(maybeOnboard, 400);
    }, 600);
}
