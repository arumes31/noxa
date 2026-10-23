import { currentLanguage, t } from "./i18n.js";
import { auditActionLabel, auditChanges, auditResource, parseAuditDetail } from "./audit-format.js";

const node = (tag, cls = "", text = "") => { const el = document.createElement(tag); el.className = cls; el.textContent = text; return el; };

// The caller owns scope/revocation. Disposal invalidates outstanding requests
// and removes every record, including the inspector's selected entry.
export function mountActivityAudit(host, isCurrent) {
    const tabID = window.__noxa.state.activeTabID;
    let disposed = false, entries = [], capabilities = [], oldest = 0, busy = false, done = false;
    const current = () => !disposed && host.isConnected && isCurrent();
    host.replaceChildren();
    const controls = node("div", "activity-audit-controls");
    const search = node("input", "activity-search"); search.type = "search"; search.placeholder = t("audit.search"); search.setAttribute("aria-label", t("audit.search"));
    const action = node("select", "activity-search"); action.setAttribute("aria-label", t("activity.action"));
    const all = node("option", "", t("activity.all")); all.value = ""; action.append(all);
    const status = node("p", "activity-empty"); status.setAttribute("role", "status");
    const layout = node("div", "activity-audit-layout"), table = node("table", "activity-audit-table"), head = node("thead"), titles = node("tr"), body = node("tbody");
    for (const key of ["time", "actor", "action", "target"]) { const th = node("th", "", t(`activity.${key}`)); th.scope = "col"; titles.append(th); }
    head.append(titles); table.append(head, body);
    const tableWrap = node("div", "activity-audit-table-wrap"); tableWrap.append(table);
    const inspector = node("section", "activity-audit-inspector"); inspector.setAttribute("aria-label", t("audit.title"));
    const older = node("button", "activity-button", t("audit.older")); older.type = "button";
    controls.append(search, action); layout.append(tableWrap, inspector); host.append(controls, status, layout, older);
    const inspect = entry => {
        inspector.replaceChildren();
        if (!entry) { inspector.append(node("p", "activity-empty", t("activity.selectAudit"))); return; }
        inspector.append(node("h3", "", entry.restricted ? t("audit.restricted") : auditActionLabel(entry.action)));
        if (entry.restricted) { inspector.append(node("p", "", t("audit.restrictedHelp"))); return; }
        const detail = parseAuditDetail(entry.detail, entry.structured === true);
        inspector.append(node("p", "", t("audit.actor", { actor: entry.actor || t("audit.none") })));
        if (detail.text) inspector.append(node("p", "activity-audit-reason", detail.text));
        const changes = auditChanges(detail.before, detail.after, capabilities);
        if (changes.length) {
            const diff = node("table", "audit-changes"), header = node("thead"), labels = node("tr"), rows = node("tbody");
            for (const key of ["field", "before", "after"]) { const th = node("th", "", t(`audit.${key}`)); th.scope = "col"; labels.append(th); }
            header.append(labels);
            for (const change of changes) {
                const row = node("tr"), field = node("th", "", change.label); field.scope = "row";
                row.append(field, node("td", "", change.before), node("td", "", change.after)); rows.append(row);
            }
            diff.append(header, rows); inspector.append(diff);
        }
        if (detail.revision) inspector.append(node("p", "activity-empty", t("audit.revision", { revision: detail.revision })));
    };
    const render = () => {
        body.replaceChildren(); inspect(null);
        const query = search.value.toLocaleLowerCase();
        for (const entry of entries) {
            // Restricted records never contribute hidden actor/detail text to search.
            const label = entry.restricted ? t("audit.restricted") : auditActionLabel(entry.action);
            if (action.value && (entry.restricted || entry.action !== action.value)) continue;
            if (query && !(entry.restricted ? label : `${label} ${entry.actor} ${entry.target} ${entry.detail}`).toLocaleLowerCase().includes(query)) continue;
            const row = node("tr");
            const time = node("td", "", new Date(entry.created_at * 1000).toLocaleString(currentLanguage()));
            const actor = node("td", "", entry.restricted ? "—" : entry.actor || t("audit.none"));
            const cell = node("td"), select = node("button", "activity-link", label); select.type = "button";
            select.onclick = () => { for (const r of body.children) r.classList.toggle("selected", r === row); inspect(entry); };
            cell.append(select);
            const detail = entry.restricted ? {} : parseAuditDetail(entry.detail, entry.structured === true);
            const target = node("td", "", entry.restricted ? "—" : auditResource(entry.action, detail) || entry.target || "—");
            row.append(time, actor, cell, target); body.append(row);
        }
        if (!body.childElementCount && !busy) status.textContent = t("audit.empty");
    };
    const load = async () => {
        if (!current() || busy || done) return;
        busy = true; older.disabled = true; status.textContent = t("audit.loading");
        try {
            const result = await window.go.main.App.AuditLogForTab(tabID, oldest, 50);
            if (!current()) return;
            const page = result.entries || [];
            done = !page.length;
            if (page.length) oldest = page.at(-1).id;
            const known = new Set(entries.map(e => e.id)); entries.push(...page.filter(e => !known.has(e.id)));
            capabilities = result.capabilities || capabilities;
            const selected = action.value; action.replaceChildren(all);
            for (const key of new Set(entries.filter(e => !e.restricted).map(e => e.action))) { const option = node("option", "", auditActionLabel(key)); option.value = key; action.append(option); }
            action.value = selected;
            busy = false; status.textContent = ""; render();
        } catch {
            if (current()) { entries = []; oldest = 0; capabilities = []; body.replaceChildren(); inspect(null); status.textContent = t("audit.failed"); }
        } finally { if (current()) { busy = false; older.disabled = done; } }
    };
    search.oninput = render; action.onchange = render; older.onclick = load;
    void load();
    return () => { disposed = true; entries = []; capabilities = []; host.replaceChildren(); };
}
