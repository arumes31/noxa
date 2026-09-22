import { t } from "./i18n.js";
import { closeDialog, confirmDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { roleDraft, roleIsDirty, reorderedRoles } from "./role-editor-state.js";
import { renderRoleForm, renderRoleList, roleButton, roleElement } from "./role-editor-view.js";
import "./roles.css";
import { openRoleMembers } from "./role-members-ui.js";

let openOverlay = null;

export function openRolesManager() {
    if (openOverlay?.isConnected) return;
    const app = window.go.main.App;
    const tabID = window.__noxa.state.activeTabID;
    const overlay = roleElement("div", "dlg-overlay");
    openOverlay = overlay;
    const dialog = roleElement("section", "dlg roles-dialog");
    const header = roleElement("header", "roles-header");
    header.append(roleElement("h3", "", t("roles.title")), roleButton(t("roles.members"), openRoleMembers), roleButton(t("common.close"), () => closeDialog(overlay, "cancel")));
    const help = roleElement("p", "role-hint", t("roles.help"));
    const defaultRoleField = roleElement("label", "role-field");
    const defaultRoleSelect = roleElement("select");
    defaultRoleSelect.setAttribute("aria-label", t("roles.defaultRole"));
    defaultRoleField.append(roleElement("span", "", t("roles.defaultRole")), defaultRoleSelect,
        roleElement("span", "role-hint", t("roles.defaultRoleHelp")));
    defaultRoleField.hidden = true;
    const body = roleElement("div", "roles-body");
    const sidebar = roleElement("aside", "roles-sidebar");
    const list = roleElement("nav", "role-list");
    list.setAttribute("aria-label", t("roles.title"));
    const editor = roleElement("div", "roles-editor");
    const footer = roleElement("footer", "roles-footer");
    const status = roleElement("p", "role-status", t("roles.loading"));
    status.setAttribute("role", "status");
    const error = roleElement("p", "role-error");
    error.setAttribute("role", "alert");
    const model = { snapshot: null, draft: null, saved: null, dirty: false, busy: true, needsRefresh: false };
    const current = () => isCurrentServerDialog(overlay);
    const ask = (message, danger = false) => confirmDialog({ title: t("roles.title"), message, danger,
        confirmLabel: danger ? t("common.delete") : t("roles.discard"), cancelLabel: t("common.cancel"), serverScoped: true });
    const discardConfirmed = async () => !model.dirty || await ask(t("roles.discardAsk"));
    let confirmingClose = false;
    const lifecycle = { onCancel: () => {
        if (!model.dirty) return true;
        if (!confirmingClose) {
            confirmingClose = true;
            discardConfirmed().then((confirmed) => { confirmingClose = false; if (confirmed && current()) closeDialog(overlay); });
        }
        return false;
    } };
    const select = async (id) => {
        if (!await discardConfirmed() || !current()) return;
        const selected = model.snapshot.policy.roles.find((r) => r.id === id);
        model.saved = roleDraft(selected);
        model.draft = roleDraft(selected);
        model.dirty = !selected;
        render();
        editor.querySelector("input")?.focus();
    };
    const refresh = async (selectedID = model.draft?.id) => {
        const snapshot = await app.RoleStateForTab(tabID, 0);
        if (!current()) return;
        model.snapshot = snapshot;
        model.needsRefresh = false;
        const selected = snapshot.policy.roles.find((r) => r.id === selectedID) || snapshot.policy.roles[0];
        model.saved = roleDraft(selected);
        model.draft = roleDraft(selected);
        model.dirty = false;
    };
    const commit = async (change) => {
        if (!current() || model.busy || model.needsRefresh) return;
        model.busy = true;
        error.textContent = "";
        render();
        let acknowledged = false;
        try {
            const result = await app.RoleChangeForTab(tabID, { ...change, expected_revision: model.snapshot.policy.revision });
            acknowledged = true;
            if (!current()) return;
            if (result.enforcement_pending) {
                model.needsRefresh = true;
                model.dirty = false;
                error.textContent = t("roles.enforcementPending");
                return;
            }
            await refresh(result.created_role_id || model.draft?.id);
            if (!current()) return;
            status.textContent = t("roles.saved");
        } catch (failure) {
            if (!current()) return;
            model.needsRefresh = acknowledged;
            error.textContent = t(acknowledged ? "roles.refreshFailed" : /conflict|changed|refresh/i.test(String(failure)) ? "roles.conflict" : "roles.failed");
        } finally {
            if (current()) { model.busy = false; render(); }
        }
    };
    const create = roleButton(t("roles.new"), () => select(0));
    defaultRoleSelect.addEventListener("change", () => {
        if (model.dirty || model.snapshot?.actor_id !== model.snapshot?.policy.owner_id) return;
        commit({ kind: "default_member_role_set", role_id: Number(defaultRoleSelect.value) });
    });
    const refreshButton = roleButton(t("roles.refresh"), async () => {
        if (!await discardConfirmed() || !current()) return;
        load();
    });
    const save = roleButton(t("common.save"), () => commit({ kind: model.draft.id ? "role_update" : "role_create", role: roleDraft(model.draft) }));
    const discard = roleButton(t("roles.discard"), () => { model.draft = roleDraft(model.saved); model.dirty = false; error.textContent = ""; render(); });
    const remove = roleButton(t("common.delete"), async () => {
        const id = model.draft.id;
        if (!await ask(t("roles.deleteAsk", { name: model.draft.name }), true) || !current() || id !== model.draft.id) return;
        commit({ kind: "role_delete", role_id: id });
    });
    const updateButtons = () => {
        save.disabled = model.busy || !model.dirty || !model.draft?.name.trim() || model.needsRefresh;
        discard.disabled = model.busy || !model.dirty;
        create.disabled = model.busy || !model.snapshot || model.needsRefresh;
        refreshButton.disabled = model.busy;
        defaultRoleSelect.disabled = model.busy || model.dirty || model.needsRefresh;
        remove.disabled = model.busy || model.dirty || !model.draft?.id || model.draft.id === model.snapshot?.policy.everyone_id ||
            !model.snapshot?.manageable_role_ids.includes(model.draft.id) || model.needsRefresh;
        if (model.busy) status.textContent = t(model.snapshot ? "roles.saving" : "roles.loading");
        else if (model.dirty) status.textContent = t("roles.unsaved");
        else if (status.textContent !== t("roles.saved")) status.textContent = "";
    };
    const render = () => {
        updateButtons();
        if (!model.snapshot) return;
        defaultRoleField.hidden = model.snapshot.actor_id !== model.snapshot.policy.owner_id;
        const noExtraRole = roleElement("option", "", t("roles.defaultRoleNone"));
        noExtraRole.value = "0";
        defaultRoleSelect.replaceChildren(noExtraRole);
        for (const role of model.snapshot.policy.roles) {
            if (role.id === model.snapshot.policy.everyone_id || (role.permissions || []).includes("administrator")) continue;
            const option = roleElement("option", "", role.name);
            option.value = String(role.id);
            defaultRoleSelect.append(option);
        }
        defaultRoleSelect.value = String(model.snapshot.policy.default_member_role_id || 0);
        renderRoleList(list, model, select, (id, direction) => {
            const order = reorderedRoles(model.snapshot.policy.roles, id, direction);
            if (order) commit({ kind: "roles_reorder", role_ids: order });
        });
        renderRoleForm(editor, model, (rebuild = false) => {
            model.dirty = !model.draft.id || roleIsDirty(model.saved, model.draft);
            if (rebuild) render(); else updateButtons();
        });
    };
    const load = async () => {
        model.busy = true;
        error.textContent = "";
        render();
        try { await refresh(); }
        catch { if (current()) error.textContent = t("roles.unavailable"); }
        finally { if (current()) { model.busy = false; render(); } }
    };
    sidebar.append(create, list, refreshButton);
    body.append(sidebar, editor);
    footer.append(status, remove, discard, save);
    dialog.append(header, help, defaultRoleField, body, error, footer);
    overlay.append(dialog);
    mountServerDialog(overlay, lifecycle);
    load();
}
