import { currentLanguage } from "./i18n.js";
import { copyToClipboard } from "./clipboard.js";
import { benchmarkMessages, benchmarkResultRows, benchmarkSummary } from "./connection-benchmark-result.js";
import "./connection-benchmark.css";

const state = () => window.__noxa?.state || {};
const bridge = () => window.go?.main?.App;
const text = key => (benchmarkMessages[currentLanguage()] || benchmarkMessages.en)[key];
const element = (name, content, className) => {
    const node = document.createElement(name);
    if (content) node.textContent = content;
    if (className) node.className = className;
    return node;
};

export function createConnectionBenchmark() {
    const root = element("section", "", "connection-benchmark");
    root.append(element("h3", text("title")), element("p", text("help")), element("p", text("limit"), "set-hint"));
    const actions = element("div", "", "connection-benchmark-actions");
    const start = element("button", text("start")); start.type = "button";
    const cancel = element("button", text("cancel")); cancel.type = "button"; cancel.hidden = true;
    const copy = element("button", text("copy")); copy.type = "button"; copy.hidden = true;
    actions.append(start, cancel, copy);
    const status = element("p"); status.setAttribute("role", "status");
    const progress = element("progress"); progress.max = 22; progress.hidden = true; progress.setAttribute("aria-label", text("title"));
    const results = element("div", "", "connection-benchmark-results");
    root.append(actions, status, progress, results);
    const origin = { tab: state().activeTabID, generation: state().serverGeneration, client: state().myClientID };
    const current = () => root.isConnected && state().activeTabID === origin.tab && state().serverGeneration === origin.generation && state().myClientID === origin.client;
    let operation = null, timer = null, result = null, busy = false;
    const available = !!origin.tab && !!origin.client && typeof bridge()?.StartConnectionBenchmark === "function";
    start.disabled = !available;
    if (!available) status.textContent = text("unavailable");

    const stop = () => { clearTimeout(timer); timer = null; };
    const cancelOwned = async () => {
        if (operation) { try { await bridge().CancelConnectionBenchmark(origin.tab, operation); } catch { /* native lifetime is independently bounded */ } }
    };
    const finish = sample => {
        stop(); busy = false; start.disabled = !current(); cancel.hidden = true; progress.hidden = true;
        status.textContent = text(sample.phase) || text("failed");
        if (sample.phase !== "complete" || !sample.result) return;
        result = benchmarkSummary(sample.result); copy.hidden = false;
        const list = element("dl");
        for (const [label, value] of benchmarkResultRows(sample.result, currentLanguage())) list.append(element("dt", label), element("dd", value));
        results.replaceChildren(list, element("p", text("tailHelp"), "set-hint"), element("p", text("disclaimer"), "set-hint"));
        if (sample.result.returned === 0) results.prepend(element("p", text("noReturns"), "warn"));
    };
    const poll = async () => {
        if (!current()) { stop(); await cancelOwned(); return; }
        try {
            const sample = await bridge().ConnectionBenchmarkStatusForTab(origin.tab, operation);
            if (!current()) { stop(); await cancelOwned(); return; }
            if (sample.id !== operation) { await cancelOwned(); finish({ phase: "failed" }); return; }
            if (["complete", "failed", "cancelled"].includes(sample.phase)) { finish(sample); return; }
            status.textContent = `${text(sample.phase) || text("connecting")} ${sample.elapsed_seconds || 0} / 20 s`;
            if (sample.phase === "connecting") progress.removeAttribute("value");
            else progress.value = sample.phase === "draining" ? 21 : Math.max(0, Math.min(20, sample.elapsed_seconds || 0));
            timer = setTimeout(() => { void poll(); }, 300);
        } catch { await cancelOwned(); if (current()) finish({ phase: "failed" }); else stop(); }
    };
    start.onclick = async () => {
        if (busy || !current()) return;
        busy = true; start.disabled = true; copy.hidden = true; results.replaceChildren(); result = null;
        status.textContent = text("connecting"); progress.hidden = false; progress.removeAttribute("value");
        try {
            const sample = await bridge().StartConnectionBenchmark(origin.tab);
            operation = sample.id;
            if (!current()) { await cancelOwned(); return; }
            cancel.hidden = false; cancel.disabled = false;
            await poll();
        } catch { if (current()) finish({ phase: "failed" }); }
    };
    cancel.onclick = async () => { cancel.disabled = true; await cancelOwned(); };
    copy.onclick = () => { if (current() && result) void copyToClipboard(JSON.stringify(result, null, 2), { isCurrent: current }); };
    return root;
}
