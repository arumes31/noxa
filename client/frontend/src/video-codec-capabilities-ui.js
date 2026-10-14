import { t } from "./i18n.js";
import { copyToClipboard } from "./clipboard.js";
import { checkVideoCodecCapabilities } from "./video-codec-capabilities.js";
import "./video-codec-capabilities.css";

const text = (key, values) => t(`videoCapabilities.${key}`, values);
const node = (tag, value, className) => {
    const element = document.createElement(tag);
    if (value) element.textContent = value;
    if (className) element.className = className;
    return element;
};
const prediction = value => text(value === true ? "yes" : value === false ? "no" : "unknown");

// Local, explicit capability prediction. This never starts media or changes a call.
export function createVideoCodecCapabilities({ check = checkVideoCodecCapabilities } = {}) {
    const root = node("details", "", "video-codec-capabilities");
    const summary = node("summary", text("title"));
    root.append(summary, node("p", text("help"), "set-hint"));
    const actions = node("div", "", "video-codec-capabilities-actions");
    const start = node("button", text("start")); start.type = "button";
    const cancel = node("button", text("cancel")); cancel.type = "button"; cancel.hidden = true;
    const copy = node("button", text("copy")); copy.type = "button"; copy.disabled = true;
    actions.append(start, cancel, copy);
    const status = node("p"); status.setAttribute("role", "status");
    const results = node("div", "", "video-codec-capabilities-results");
    root.append(actions, status, results);
    let operation = null, result = null;
    const render = report => {
        results.replaceChildren();
        results.append(node("p", text("counts", {
            checked: report.counts.selected_profiles, omitted: report.counts.omitted_profiles,
            invalid: report.counts.invalid_profiles, uninspected: report.counts.uninspected_entries,
        }), "set-hint"));
        for (const group of report.codecs) {
            const section = node("section");
            section.append(node("h4", `${group.codec} · ${text(group.direction)}`));
            if (!group.profiles.length) {
                section.append(node("p", text(group.advertised === false ? "notAdvertised" : "unknown")));
            }
            const list = node("ul");
            for (const profile of group.profiles) {
                const item = node("li");
                const parameters = Object.entries(profile.parameters).map(([key, value]) => `${key}=${value}`).join("; ");
                item.append(node("code", parameters || text("defaultProfile")));
                item.append(node("p", profile.status === "ok" ? text("prediction", {
                    supported: prediction(profile.supported), smooth: prediction(profile.smooth),
                    efficient: prediction(profile.power_efficient),
                }) : text(`query.${profile.status}`)));
                list.append(item);
            }
            if (list.childElementCount) section.append(list);
            results.append(section);
        }
    };
    const stop = () => {
        if (!operation) return;
        operation.cancelled = true;
        cancel.disabled = true;
        status.textContent = text("cancelled");
    };
    start.onclick = async () => {
        if (operation || !root.isConnected) return;
        const request = { cancelled: false }; operation = request;
        result = null; results.replaceChildren(); copy.disabled = true;
        start.disabled = true; cancel.hidden = false; cancel.disabled = false;
        status.textContent = text("running");
        const mounted = () => root.isConnected && operation === request;
        try {
            const report = await check({ current: () => mounted() && root.open && !request.cancelled });
            if (!mounted()) return;
            if (request.cancelled) { status.textContent = text("cancelled"); return; }
            if (report.status === "busy" || report.status === "cancelled") {
                status.textContent = text(report.status);
                return;
            }
            result = report;
            render(report);
            status.textContent = text(report.status);
            copy.disabled = false;
        } catch {
            if (mounted()) status.textContent = text(request.cancelled ? "cancelled" : "failed");
        } finally {
            if (mounted()) { start.disabled = false; cancel.hidden = true; }
            if (operation === request) operation = null;
        }
    };
    cancel.onclick = stop;
    // toggle is queued: cancel before the default summary action closes details,
    // so a native result cannot start the next query in that intervening task.
    summary.onclick = () => { if (root.open) stop(); };
    root.ontoggle = () => { if (!root.open) stop(); };
    copy.onclick = () => {
        if (!root.isConnected || !result || operation) return;
        const snapshot = result;
        void copyToClipboard(JSON.stringify(snapshot, null, 2), {
            isCurrent: () => root.isConnected && result === snapshot && !operation,
        });
    };
    return root;
}
