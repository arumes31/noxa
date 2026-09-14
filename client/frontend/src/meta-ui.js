// meta-ui.js — wave-8b meta features: debug console (327), connection stats
// page (328), onboarding wizard (329), what's-new dialog (330), crash report
// toast (331).
import { closeDialog, mountDialog, mountServerDialog } from "./modal.js";
import { openServerInfo } from "./server-info.js";

const V = () => window.__voicx;
const App = () => window.go.main.App;

// ---------------------------------------------------------------------------
// Debug console (327)
// ---------------------------------------------------------------------------

const dbg = { open: false, paused: false, filter: "", rows: [] };

function openDebugConsole() {
    if (dbg.open) return;
    dbg.open = true;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg debug-console">
            <div class="pm-head">
                <h3>Debug console</h3>
                <button class="icon-btn dbg-close" title="Close">✕</button>
            </div>
            <div class="dbg-toolbar">
                <input class="dlg-input dbg-filter" placeholder="filter by type…" />
                <button class="dbg-pause">⏸ pause</button>
                <button class="dbg-clear">clear</button>
            </div>
            <div class="dbg-list mono"></div>
        </div>`;
    dbg.overlay = overlay;
    const close = () => closeDialog(overlay);
    const cleanup = (reason) => {
        dbg.open = false;
        if (dbg.overlay === overlay) dbg.overlay = null;
        // A tab reset has already activated another backend; never send the
        // old dialog's teardown command to that newly active connection.
        if (reason !== "server-change") App().SetDebugFrames(false);
    };
    overlay.querySelector(".dbg-close").onclick = close;
    overlay.onclick = (e) => { if (e.target === overlay) close(); };
    overlay.querySelector(".dbg-filter").oninput = (e) => {
        dbg.filter = e.target.value.trim().toLowerCase();
        renderDbg();
    };
    overlay.querySelector(".dbg-pause").onclick = (e) => {
        dbg.paused = !dbg.paused;
        e.target.textContent = dbg.paused ? "▶ resume" : "⏸ pause";
    };
    overlay.querySelector(".dbg-clear").onclick = () => {
        dbg.rows = [];
        renderDbg();
    };
    mountServerDialog(overlay, { onClose: cleanup });
    App().SetDebugFrames(true);
    renderDbg();
}

function renderDbg() {
    if (!dbg.open) return;
    const list = dbg.overlay.querySelector(".dbg-list");
    const rows = dbg.rows.filter((r) => !dbg.filter || r.type.toLowerCase().includes(dbg.filter)).slice(-200);
    list.innerHTML = rows.map((r) =>
        `<div class="dbg-row ${r.dir}"><span class="dbg-dir">${r.dir === "in" ? "◀" : "▶"}</span> <b>${escapeHtml(r.type)}</b> <span class="dbg-payload">${escapeHtml(r.payload)}</span></div>`).join("");
    list.scrollTop = list.scrollHeight;
}

function escapeHtml(s) {
    return String(s ?? "").replace(/[&<>]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" }[c]));
}

// ---------------------------------------------------------------------------
// Connection stats page (328)
// ---------------------------------------------------------------------------

const openStatsPage = openServerInfo;

// ---------------------------------------------------------------------------
// Onboarding (329) + what's new (330) + crash toast (331)
// ---------------------------------------------------------------------------

// maybeOnboard shows the first-run wizard (skipped when done before).
function maybeOnboard() {
    const s = V().state.settings;
    if (!s || s.onboarding_done || document.querySelector(".dlg.onboarding")) return;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    let step = 0;
    const steps = [
        { title: "Welcome to voicx", body: `<p>Your <b>identity</b> is an Ed25519 key pair generated locally — it is your account for guest logins and challenge auth. It never leaves this machine unless you export it.</p><p>Pick a nickname for your first connect:</p><input class="dlg-input ob-nick" placeholder="nickname" />` },
        { title: "Microphone check", body: `<p>Open Settings → Capture and use the <b>mic test</b> to verify your input level.</p><button class="dlg-ok ob-mic">Open capture settings</button>` },
        { title: "Connect", body: `<p>Enter a server address and connect — bookmarks and recents make the next time one click.</p>` },
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
                    <button class="dlg-cancel ob-skip">Skip</button>
                    <button class="dlg-ok ob-next">${step === steps.length - 1 ? "Done" : "Next"}</button>
                </div>
                <div class="ob-dots">${steps.map((_, i) => i === step ? "●" : "○").join(" ")}</div>
            </div>`;
        const mic = overlay.querySelector(".ob-mic");
        if (mic) mic.onclick = () => {
            closeDialog(overlay);
            void done();
            window.__voicx.openSettings("capture");
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

// maybeWhatsNew shows release notes after an update (330).
async function maybeWhatsNew() {
    try {
        const notes = await App().WhatsNew();
        if (!notes) return;
        const overlay = document.createElement("div");
        overlay.className = "dlg-overlay";
        overlay.innerHTML = `
            <div class="dlg">
                <h3>What's new</h3>
                <div class="dlg-text whatsnew-body"></div>
                <div class="dlg-buttons"><button class="dlg-ok">Nice</button></div>
            </div>`;
        overlay.querySelector(".whatsnew-body").textContent = notes;
        overlay.querySelector(".dlg-ok").onclick = () => overlay.remove();
        overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
        mountDialog(overlay);
    } catch { /* best-effort */ }
}

// maybeCrashToast reports a previous crash (331).
async function maybeCrashToast() {
    try {
        const crash = await App().LastCrash();
        if (!crash) return;
        V().toast("voicx crashed last time — a crash log was saved (Help → Export logs)", "warn", "conn");
        V().sysMsg("previous crash detected; export logs via Help → Export logs");
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
    window.__voicxMeta = { openDebugConsole, openStatsPage, openServerInfo, maybeOnboard };
    for (const id of ["server-name", "voice-latency"]) {
        document.getElementById(id)?.addEventListener("click", openServerInfo);
    }
    // Startup flows: crash report, what's new, onboarding (in that order).
    setTimeout(() => {
        maybeCrashToast();
        maybeWhatsNew();
        setTimeout(maybeOnboard, 400);
    }, 600);
}
