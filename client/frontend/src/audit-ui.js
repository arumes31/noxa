import { currentLanguage, t } from "./i18n.js";
import { closeDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { roleButton, roleElement } from "./role-editor-view.js";
import { auditActionLabel, auditChanges, auditResource, parseAuditDetail } from "./audit-format.js";
import "./roles.css";

let openOverlay = null;

export function openAuditLog() {
    if (openOverlay?.isConnected) return;
    const tabID = window.__noxa.state.activeTabID;
    const overlay = roleElement("div", "dlg-overlay");
    openOverlay = overlay;
    const dialog = roleElement("section", "dlg roles-dialog audit-dialog");
    const header = roleElement("header", "roles-header");
    header.append(roleElement("h3", "", t("audit.title")), roleButton(t("common.close"), () => closeDialog(overlay)));
    const label = roleElement("label", "role-field", t("audit.search"));
    const filter = roleElement("input", "dlg-input");
    filter.type = "search";
    label.append(filter);
    const list = roleElement("div", "audit-records");
    const status = roleElement("p", "role-status");
    status.setAttribute("role", "status");
    const error = roleElement("p", "role-error");
    error.setAttribute("role", "alert");
    const model = { entries: [], capabilities: [], oldest: 0, busy: false, done: false };
    const current = () => isCurrentServerDialog(overlay);
    const render = () => {
        list.replaceChildren();
        const search = filter.value.toLocaleLowerCase();
        const entries = model.entries.filter((e) => !search || `${auditActionLabel(e.action)} ${e.actor} ${e.target} ${e.detail}`.toLocaleLowerCase().includes(search));
        for (const entry of entries) {
            const card = roleElement("article", "audit-record");
            const date = new Date(entry.created_at * 1000);
            const time = roleElement("time", "role-hint", Number.isNaN(date.valueOf()) ? "" : date.toLocaleString(currentLanguage()));
            if (!Number.isNaN(date.valueOf())) time.dateTime = date.toISOString();
            card.append(time, roleElement("h4", "", entry.restricted ? t("audit.restricted") : auditActionLabel(entry.action)));
            if (entry.restricted) {
                card.append(roleElement("p", "role-hint", t("audit.restrictedHelp")));
                list.append(card);
                continue;
            }
            card.append(roleElement("p", "role-hint", t("audit.actor", { actor: entry.actor || t("audit.none") })));
            const detail = parseAuditDetail(entry.detail, entry.structured === true);
            const target = auditResource(entry.action, detail) || (entry.target === "authorization" ? t("audit.authorization") : entry.target);
            if (target) card.append(roleElement("p", "role-hint", t("audit.target", { target })));
            if (detail.text) card.append(roleElement("p", "audit-text", detail.text));
            const changes = auditChanges(detail.before, detail.after, model.capabilities);
            if (changes.length) {
                const table = roleElement("table", "audit-changes");
                const head = roleElement("thead"), titles = roleElement("tr");
                for (const key of ["audit.field", "audit.before", "audit.after"]) {
                    const cell = roleElement("th", "", t(key)); cell.scope = "col"; titles.append(cell);
                }
                head.append(titles);
                const body = roleElement("tbody");
                for (const change of changes) {
                    const row = roleElement("tr"), field = roleElement("th", "", change.label);
                    field.scope = "row";
                    row.append(field, roleElement("td", "", change.before), roleElement("td", "", change.after));
                    body.append(row);
                }
                table.append(head, body);
                card.append(table);
            }
            if (detail.revision) card.append(roleElement("p", "role-hint", t("audit.revision", { revision: detail.revision })));
            list.append(card);
        }
        if (!entries.length && !model.busy) list.append(roleElement("p", "role-hint", t("audit.empty")));
    };
    const load = async () => {
        if (!current() || model.busy || model.done) return;
        model.busy = true;
        older.disabled = true;
        status.textContent = t("audit.loading");
        error.textContent = "";
        try {
            const response = await window.go.main.App.AuditLogForTab(tabID, model.oldest, 50);
            if (!current()) return;
            const entries = response.entries || [];
            model.capabilities = response.capabilities || model.capabilities;
            model.done = !entries.length;
            if (entries.length) model.oldest = entries[entries.length - 1].id;
            model.entries.push(...entries);
        } catch {
            if (current()) { model.entries = []; model.oldest = 0; error.textContent = t("audit.failed"); }
        } finally {
            if (current()) {
                model.busy = false;
                status.textContent = "";
                older.disabled = model.done;
                render();
            }
        }
    };
    const older = roleButton(t("audit.older"), load);
    filter.oninput = render;
    const footer = roleElement("footer", "roles-footer");
    footer.append(status, older);
    dialog.append(header, label, error, list, footer);
    overlay.append(dialog);
    mountServerDialog(overlay);
    load();
}
