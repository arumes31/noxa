import { t } from "./i18n.js";
import { closeDialog, confirmDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { roleButton, roleElement } from "./role-editor-view.js";
import { roleChip } from "./role-presentation.js";

export function openRoleMembers() {
    const app = window.go.main.App;
    const tabID = window.__noxa.state.activeTabID;
    const overlay = roleElement("div", "dlg-overlay");
    const dialog = roleElement("section", "dlg roles-dialog");
    const header = roleElement("header", "roles-header");
    header.append(roleElement("h3", "", t("roles.members")), roleButton(t("common.close"), () => closeDialog(overlay)));
    const searchLabel = roleElement("label", "role-field", t("roles.searchMembers"));
    const search = roleElement("input", "dlg-input");
    search.type = "search";
    searchLabel.append(search);
    const list = roleElement("div", "role-member-list");
    const status = roleElement("p", "role-hint");
    status.setAttribute("role", "status");
    const error = roleElement("p", "role-error");
    error.setAttribute("role", "alert");
    const roleLabel = roleElement("label", "role-field", t("roles.choose"));
    const roleSelect = roleElement("select", "dlg-input");
    roleLabel.append(roleSelect);
    let snapshot, entries = [], more = false, busy = false, needsRefresh = false, transferred = false, serial = 0, timer;
    const selected = new Set();
    const results = new Map();
    const current = () => isCurrentServerDialog(overlay);
    const updateControls = () => {
        status.textContent = transferred ? t("roles.ownerTransferred") : busy ? t("roles.loading") : t("roles.selected", { count: selected.size });
        add.disabled = remove.disabled = busy || needsRefresh || transferred || !selected.size || !roleSelect.value;
        transfer.hidden = !snapshot || snapshot.actor_id !== snapshot.policy.owner_id;
        transfer.disabled = busy || needsRefresh || transferred || selected.size !== 1;
        refreshButton.disabled = busy || transferred;
        moreButton.hidden = !more;
        moreButton.disabled = busy || transferred;
        search.disabled = roleSelect.disabled = busy || transferred;
    };
    const render = () => {
        list.replaceChildren();
        for (const member of entries) {
            const row = roleElement("label", "role-member-row");
            const checkbox = roleElement("input");
            checkbox.type = "checkbox";
            checkbox.checked = selected.has(member.user_id);
            checkbox.disabled = busy || transferred || !member.manageable;
            checkbox.onchange = () => { if (checkbox.checked) selected.add(member.user_id); else selected.delete(member.user_id); updateControls(); };
            const name = roleElement("span", "role-member-name", member.nickname || member.unique_id);
            const chips = roleElement("span", "role-chips");
            for (const role of snapshot.policy.roles.filter((r) => member.role_ids.includes(r.id)).sort((a, b) => b.position - a.position)) chips.append(roleChip(role));
            const result = results.get(member.user_id);
            row.append(checkbox, name, chips);
            if (result) row.append(roleElement("span", "role-hint", t(result)));
            list.append(row);
        }
        if (!entries.length) list.append(roleElement("p", "role-hint", t("roles.noMembers")));
        updateControls();
    };
    const load = async (append = false) => {
        if (busy || transferred || !current()) return;
        const request = ++serial;
        busy = true;
        error.textContent = "";
        render();
        try {
            if (!append) {
                const fresh = await app.RoleStateForTab(tabID, 0);
                if (!current() || request !== serial) return;
                snapshot = fresh;
                roleSelect.replaceChildren();
                for (const role of snapshot.policy.roles) {
                    if (role.id === snapshot.policy.everyone_id || !snapshot.manageable_role_ids.includes(role.id)) continue;
                    const option = roleElement("option", "", role.name);
                    option.value = String(role.id);
                    roleSelect.append(option);
                }
            }
            const page = await app.RoleMembersForTab(tabID, { search: search.value, after_id: append ? entries.at(-1)?.user_id || 0 : 0, expected_revision: snapshot.policy.revision });
            if (!current() || request !== serial) return;
            entries = append ? [...entries, ...page.entries] : page.entries;
            more = page.more;
            needsRefresh = false;
            if (!append) { selected.clear(); results.clear(); }
        } catch { if (current() && request === serial) error.textContent = t("roles.unavailable"); }
        finally { if (current() && request === serial) { busy = false; render(); } }
    };
    const apply = async (adding) => {
        if (busy || needsRefresh || transferred || !current()) return;
        clearTimeout(timer);
        const roleID = Number(roleSelect.value);
        const members = entries.filter((m) => selected.has(m.user_id));
        busy = true;
        render();
        error.textContent = "";
        for (const member of members) {
            const ids = member.role_ids.filter((id) => id !== roleID);
            if (adding) ids.push(roleID);
            try {
                const ack = await app.RoleChangeForTab(tabID, { kind: "member_roles_set", expected_revision: snapshot.policy.revision, user_id: member.user_id, role_ids: ids });
                if (!current()) return;
                snapshot.policy.revision = ack.revision;
                member.role_ids = ids;
                results.set(member.user_id, "roles.applied");
                selected.delete(member.user_id);
                if (ack.enforcement_pending) {
                    needsRefresh = true;
                    error.textContent = t("roles.enforcementPending");
                    break;
                }
            } catch {
                if (!current()) return;
                results.set(member.user_id, "roles.notApplied");
                needsRefresh = true;
                error.textContent = t("roles.batchStopped");
                break;
            }
        }
        if (current()) { busy = false; render(); }
    };
    const add = roleButton(t("roles.assign"), () => apply(true), true);
    const remove = roleButton(t("roles.unassign"), () => apply(false), true);
    const transfer = roleButton(t("roles.transferOwner"), async () => {
        if (busy || needsRefresh || transferred || selected.size !== 1 || !current() || snapshot.actor_id !== snapshot.policy.owner_id) return;
        const member = entries.find((m) => selected.has(m.user_id));
        if (!member || member.user_id === snapshot.policy.owner_id) return;
        clearTimeout(timer);
        busy = true;
        render();
        const accepted = await confirmDialog({ title: t("roles.transferOwner"),
            message: t("roles.transferOwnerAsk", { name: member.nickname || member.unique_id, id: member.user_id }),
            confirmLabel: t("roles.transferOwner"), cancelLabel: t("common.cancel"), serverScoped: true });
        if (!current()) return;
        if (!accepted) { busy = false; render(); return; }
        error.textContent = "";
        try {
            const ack = await app.RoleChangeForTab(tabID, { kind: "owner_transfer", expected_revision: snapshot.policy.revision, user_id: member.user_id });
            if (!current()) return;
            snapshot.policy.revision = ack.revision;
            snapshot.policy.owner_id = member.user_id;
            transferred = true;
            selected.clear();
            if (ack.enforcement_pending) error.textContent = t("roles.enforcementPending");
        } catch {
            if (!current()) return;
            needsRefresh = true;
            error.textContent = t("roles.transferOwnerFailed");
        } finally { if (current()) { busy = false; render(); } }
    });
    const moreButton = roleButton(t("roles.more"), () => load(true));
    const refreshButton = roleButton(t("roles.refresh"), () => load());
    moreButton.hidden = true;
    search.oninput = () => { clearTimeout(timer); timer = setTimeout(() => load(), 250); };
    roleSelect.onchange = updateControls;
    const footer = roleElement("footer", "roles-footer");
    footer.append(status, refreshButton, add, remove, transfer);
    dialog.append(header, searchLabel, roleLabel, list, moreButton, error, footer);
    overlay.append(dialog);
    mountServerDialog(overlay, { onClose: () => { serial++; clearTimeout(timer); } });
    load();
}
