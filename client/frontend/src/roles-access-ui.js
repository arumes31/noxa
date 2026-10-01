// roles-access-ui.js — roles-v1 access and moderation surfaces.
import { closeDialog, isCurrentServerDialog, mountServerDialog, registerDialogLifecycle } from "./modal.js";
import { memberRoles, hoistedRoles, roleColor } from "./role-presentation.js";
import { t } from "./i18n.js";

const V = () => window.__noxa;
const App = () => window.go.main.App;

function modal(cls, html) {
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = '<div class="dlg ' + cls + '"></div>';
    const dialog = overlay.firstElementChild;
    dialog.innerHTML = html;
    overlay.onclick = (event) => { if (event.target === overlay) closeDialog(overlay, "cancel"); };
    mountServerDialog(overlay);
    return { overlay, dlg: dialog, q: (selector) => dialog.querySelector(selector) };
}

function confirmDlg(title, bodyHtml, okLabel, danger) {
    return new Promise((resolve) => {
        let result = false;
        let settled = false;
        const { overlay, dlg, q } = modal("confirm-dlg", `
            <h3></h3>
            <div class="dlg-text confirm-body"></div>
            <div class="dlg-buttons">
                <button class="dlg-cancel">Cancel</button>
                <button class="dlg-ok ${danger ? "danger-btn" : ""}"></button>
            </div>`);
        dlg.querySelector("h3").textContent = title;
        q(".confirm-body").innerHTML = bodyHtml;
        q(".dlg-ok").textContent = okLabel;
        registerDialogLifecycle(overlay, {
            onCancel: () => { result = false; },
            onClose: () => {
                if (settled) return;
                settled = true;
                resolve(result);
            },
        });
        q(".dlg-ok").onclick = () => { result = true; closeDialog(overlay); };
        q(".dlg-cancel").onclick = () => closeDialog(overlay, "cancel");
    });
}

function esc(value) {
    return String(value ?? "").replace(/[&<>"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[char]));
}

function fmtTime(unix) {
    if (!unix) return "";
    return new Date(unix * 1000).toLocaleString();
}

function toastAudit(what) {
    V().toast(what + " — recorded in the audit log");
}

function primaryGroup(uid) {
    return memberRoles(V().state, uid)[0] || null;
}

function groupColorFor(uid) {
    return memberRoles(V().state, uid).map((role) => roleColor(role.color)).find(Boolean) || "";
}

function hoistedGroups() {
    return hoistedRoles(V().state);
}

async function openPermissionManager() {
    const generation = V().state.serverGeneration;
    try {
        const { openRolesManager } = await import("./roles-ui.js");
        if (generation === V().state.serverGeneration) openRolesManager();
    } catch {
        if (generation === V().state.serverGeneration) V().toast(t("roles.unavailable"), "warn");
    }
}

async function openAuditViewer() {
    const generation = V().state.serverGeneration;
    try {
        const { openAuditLog } = await import("./audit-ui.js");
        if (generation === V().state.serverGeneration) openAuditLog();
    } catch {
        if (generation === V().state.serverGeneration) V().toast(t("roles.unavailable"), "warn");
    }
}

async function openChatFilters() {
    const tabID = V().state.activeTabID;
    const { overlay, q } = modal("audit", `
        <div class="pm-head">
            <h3>Chat Filters</h3>
            <button class="icon-btn cf-close" title="Close">✕</button>
        </div>
        <div class="cf-source empty-state"></div>
        <div class="cf-body">
            <div class="cf-field">
                <b>Word filter</b>
                <div class="pm-dim">comma-separated; each entry is a case-insensitive substring of the message</div>
                <textarea class="dlg-input cf-words" rows="3"></textarea>
            </div>
            <div class="cf-field">
                <b>Link blacklist</b>
                <div class="pm-dim">comma-separated hosts; matches the host itself or any subdomain of it</div>
                <textarea class="dlg-input cf-black" rows="3"></textarea>
            </div>
            <div class="cf-field">
                <b>Link whitelist</b>
                <div class="pm-dim">comma-separated hosts; while non-empty EVERY link in a message must match one</div>
                <textarea class="dlg-input cf-white" rows="3"></textarea>
            </div>
        </div>
        <div class="dlg-buttons">
            <button class="dlg-cancel cf-reload">Reload</button>
            <button class="dlg-ok cf-save">Save</button>
        </div>`);
    q(".cf-close").onclick = () => closeDialog(overlay);
    let lastFilters = null;
    let busy = false;
    let needsReload = true;
    const controls = () => {
        for (const input of overlay.querySelectorAll("textarea")) input.disabled = busy || !lastFilters;
        q(".cf-reload").disabled = busy;
        q(".cf-save").disabled = busy || needsReload || !lastFilters;
    };
    const fill = (response) => {
        lastFilters = response;
        q(".cf-words").value = response.word_filter || "";
        q(".cf-black").value = response.link_blacklist || "";
        q(".cf-white").value = response.link_whitelist || "";
        q(".cf-source").textContent = response.from_config
            ? "In force from config.yaml. Saving copies these lists into the server database."
            : "In force from the server database.";
    };
    const load = async () => {
        if (busy || !isCurrentServerDialog(overlay)) return;
        busy = true;
        needsReload = true;
        controls();
        try {
            const response = await App().ChatFilterGetForTab(tabID);
            if (!isCurrentServerDialog(overlay)) return;
            fill(response);
            needsReload = false;
        } catch (error) {
            if (isCurrentServerDialog(overlay)) q(".cf-source").textContent = "loading filters failed: " + error;
        } finally {
            busy = false;
            if (isCurrentServerDialog(overlay)) controls();
        }
    };
    q(".cf-reload").onclick = load;
    q(".cf-save").onclick = async () => {
        if (busy || needsReload || !lastFilters || !isCurrentServerDialog(overlay)) return;
        if (lastFilters.from_config && !confirm("Saving will override config.yaml with database settings. Proceed?")) return;
        busy = true;
        controls();
        try {
            const response = await App().ChatFilterSetForTab(
                tabID, q(".cf-words").value, q(".cf-black").value, q(".cf-white").value);
            if (!isCurrentServerDialog(overlay)) return;
            fill(response);
            needsReload = false;
            toastAudit("chat filters updated");
        } catch (error) {
            needsReload = true;
            if (isCurrentServerDialog(overlay)) {
                q(".cf-source").textContent = "Reload the server filters before saving again.";
                V().toast("chat filter save failed: " + error, "warn");
            }
        } finally {
            busy = false;
            if (isCurrentServerDialog(overlay)) controls();
        }
    };
    load();
}

async function openBanList() {
    const tabID = V().state.activeTabID;
    const { overlay, dlg, q } = modal("audit", `
        <div class="pm-head">
            <h3>Bans</h3>
            <button class="icon-btn ban-close" title="Close">✕</button>
        </div>
        <div class="ban-list"></div>`);
    q(".ban-close").onclick = () => closeDialog(overlay);
    const reload = document.createElement("button");
    reload.textContent = t("roles.ban.reload");
    const status = document.createElement("p");
    status.setAttribute("role", "status");
    dlg.append(status, reload);
    let busy = false;
    let needsRefresh = false;
    const controls = () => {
        reload.disabled = busy;
        for (const button of dlg.querySelectorAll(".ban-lift")) button.disabled = busy || needsRefresh;
    };
    const render = async () => {
        if (busy || !isCurrentServerDialog(overlay)) return;
        busy = true;
        controls();
        const list = q(".ban-list");
        let bans;
        try {
            const response = await App().BanListForTab(tabID);
            if (!isCurrentServerDialog(overlay)) return;
            bans = response.bans || [];
            needsRefresh = false;
            status.textContent = "";
        } catch (error) {
            if (!isCurrentServerDialog(overlay)) return;
            list.innerHTML = `<div class="empty-state">ban list failed: ${esc(error)}</div>`;
            needsRefresh = true;
            status.textContent = "";
            return;
        } finally {
            busy = false;
            if (isCurrentServerDialog(overlay)) controls();
        }
        list.innerHTML = bans.length ? "" : '<div class="empty-state">no bans</div>';
        const table = document.createElement("table");
        table.className = "perm-grid audit-grid";
        table.innerHTML = "<thead><tr><th>target</th><th>reason</th><th>banned by</th><th>expires</th><th></th></tr></thead><tbody></tbody>";
        const body = table.querySelector("tbody");
        for (const ban of bans) {
            const row = document.createElement("tr");
            if (ban.expires_at && ban.expires_at * 1000 < Date.now()) row.className = "pm-dim";
            row.innerHTML = `
                <td class="mono ban-value"></td>
                <td class="ban-reason"></td>
                <td class="mono ban-author"></td>
                <td class="mono ban-expiry"></td>
                <td><button class="mem-del ban-lift" title="Lift ban">✕</button></td>`;
            row.querySelector(".ban-value").textContent = (ban.value || "").slice(0, 16) + "…";
            row.querySelector(".ban-value").title = ban.value || "";
            row.querySelector(".ban-reason").textContent = ban.reason || "";
            row.querySelector(".ban-author").textContent = (ban.banned_by || "").slice(0, 10);
            row.querySelector(".ban-expiry").textContent = ban.expires_at ? fmtTime(ban.expires_at) : "permanent";
            row.querySelector(".ban-lift").onclick = async () => {
                if (busy || needsRefresh || !isCurrentServerDialog(overlay)) return;
                const confirmed = await confirmDlg("Lift ban", "Lift this ban?", "Lift", true);
                if (!confirmed || !isCurrentServerDialog(overlay)) return;
                busy = true;
                controls();
                status.textContent = t("roles.ban.working");
                try {
                    await App().RemoveRoleBanForTab(tabID, ban.id);
                    if (!isCurrentServerDialog(overlay)) return;
                    busy = false;
                    await render();
                } catch {
                    if (isCurrentServerDialog(overlay)) {
                        needsRefresh = true;
                        status.textContent = t("roles.ban.failed");
                    }
                } finally {
                    busy = false;
                    if (isCurrentServerDialog(overlay)) controls();
                }
            };
            body.appendChild(row);
        }
        list.appendChild(table);
        controls();
    };
    reload.onclick = render;
    render();
}

async function openComplaints() {
    const tabID = V().state.activeTabID;
    const { overlay, q } = modal("audit", `
        <div class="pm-head">
            <h3>Complaints</h3>
            <button class="icon-btn cp-close" title="Close">✕</button>
        </div>
        <div class="cp-list"></div>`);
    q(".cp-close").onclick = () => closeDialog(overlay);
    const render = (entries) => {
        const list = q(".cp-list");
        list.innerHTML = entries.length ? "" : '<div class="empty-state">no complaints</div>';
        if (!entries.length) return;
        const table = document.createElement("table");
        table.className = "perm-grid audit-grid";
        table.innerHTML = "<thead><tr><th>against</th><th>from</th><th>reason</th><th>filed</th><th></th></tr></thead><tbody></tbody>";
        const body = table.querySelector("tbody");
        for (const entry of entries) {
            const row = document.createElement("tr");
            row.innerHTML = `
                <td class="cp-target"></td><td class="cp-from"></td><td class="cp-reason"></td>
                <td class="mono">${fmtTime(entry.created_at)}</td>
                <td><button class="mem-del cp-one" title="Clear this complaint">✕</button>
                    <button class="mem-del cp-all" title="Clear every complaint against this user">✕ all</button></td>`;
            row.querySelector(".cp-target").textContent = entry.target_nickname || entry.target_unique_id;
            row.querySelector(".cp-target").title = entry.target_unique_id;
            row.querySelector(".cp-from").textContent = entry.from_nickname || entry.from_unique_id;
            row.querySelector(".cp-from").title = entry.from_unique_id;
            row.querySelector(".cp-reason").textContent = entry.reason || "";
            row.querySelector(".cp-one").onclick = () => clear(entry.target_unique_id, entry.from_unique_id);
            row.querySelector(".cp-all").onclick = async () => {
                const confirmed = await confirmDlg("Clear complaints",
                    `Clear every complaint against <span class="mono">${esc(entry.target_unique_id)}</span>?`,
                    "Clear all", true);
                if (confirmed) clear(entry.target_unique_id, "");
            };
            body.appendChild(row);
        }
        list.appendChild(table);
    };
    const clear = async (target, from) => {
        if (!isCurrentServerDialog(overlay)) return;
        try {
            const response = await App().ComplaintClearForTab(tabID, target, from);
            if (!isCurrentServerDialog(overlay)) return;
            render(response.entries || []);
            toastAudit("complaints cleared");
        } catch (error) {
            if (isCurrentServerDialog(overlay)) V().toast("clear failed: " + error, "warn");
        }
    };
    try {
        const response = await App().ComplaintListForTab(tabID);
        if (isCurrentServerDialog(overlay)) render(response.entries || []);
    } catch (error) {
        if (isCurrentServerDialog(overlay)) {
            q(".cp-list").innerHTML = `<div class="empty-state">complaint list failed: ${esc(error)}</div>`;
        }
    }
}

export function initPermsUI() {
    window.__noxaPerms = {
        openPermissionManager,
        openAuditViewer,
        openBanList,
        openChatFilters,
        openComplaints,
        groupColorFor,
        primaryGroup,
        hoistedGroups,
        esc,
    };
}
