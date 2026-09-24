import { t } from "./i18n.js";
import { closeDialog, confirmDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { roleButton, roleElement } from "./role-editor-view.js";
import { applyChannelPreset } from "./channel-access-state.js";
import { openChannelIcon } from "./channel-icon-ui.js";
import { channelImpactPanel } from "./channel-impact-ui.js";
import "./roles.css";

// One acknowledged operation per dialog. IDs and revision are captured from
// the same server-scoped query; a rejected or uncertain save requires reload.
export function openRoleChannel(kind, channelID = 0, { destinationID, orderIndex } = {}) {
    const app = window.go.main.App;
    const tabID = window.__noxa.state.activeTabID;
    const overlay = roleElement("div", "dlg-overlay");
    const dialog = roleElement("form", "dlg roles-dialog channel-lifecycle-dialog");
    dialog.onsubmit = (event) => event.preventDefault();
    const header = roleElement("header", "roles-header");
    const close = roleButton(t("common.close"), () => closeDialog(overlay, "cancel"));
    header.append(roleElement("h3", "", t(`roles.channel.${kind}`)), close);
    const content = roleElement("div", "channel-access-content");
    const form = roleElement("fieldset", "role-form");
    const error = roleElement("p", "role-error"); error.setAttribute("role", "alert");
    const status = roleElement("p", "role-status"); status.setAttribute("role", "status");
    let snapshot, draft, busy = false, dirty = false, needsRefresh = false, committed = false;
    let preset = "inherit", selectedRoles = new Set(), destination = 0, sync = false;
    let impactView, reviewedTree = "";
    const access = () => {
        const overrides = applyChannelPreset([], snapshot.everyone_id, preset);
        if (preset === "private") for (const roleID of selectedRoles) overrides.push({ role_id: roleID, capability: "view_channel", effect: "allow" });
        return { synced: preset === "inherit", overrides };
    };
    const treeChange = () => ({ kind, expected_revision: snapshot.revision, channel_id: kind === "channel_create" ? 0 : channelID,
        parent_id: kind === "channel_create" ? channelID : destination,
        ...(kind === "channel_create" ? { temporary: draft.channel_type === 0, access: { channel_id: 0, parent_id: channelID, ...access() } } : { sync_to_parent: sync }) });
    const requiresReview = () => (kind === "channel_create" && preset !== "inherit") || (kind === "channel_move" && sync);
    const reviewed = () => !requiresReview() || reviewedTree === JSON.stringify(treeChange());
    const invalidateImpact = () => { reviewedTree = ""; impactView?.invalidate(); };
    const current = () => isCurrentServerDialog(overlay);
    const confirm = (message) => confirmDialog({ title: t(`roles.channel.${kind}`), message,
        confirmLabel: t("common.apply"), cancelLabel: t("common.cancel"), serverScoped: true });
    const controls = () => {
        form.disabled = busy || !snapshot || committed;
        save.disabled = busy || !snapshot || needsRefresh || committed || !reviewed() || (kind === "channel_move" && !snapshot.destinations.some(entry => entry.id === destination));
        refresh.disabled = busy || committed;
        close.disabled = busy;
        overlay.dataset.blocking = String(busy);
        status.textContent = busy ? t(snapshot ? "roles.saving" : "roles.channel.loading") : committed ? t("roles.enforcementPending") : "";
        impactView?.update();
    };
    const field = (key, type = "text", min = null, max = null, host = form) => {
        const label = roleElement("label", "role-field", t(`roles.channel.${key}`));
        const input = roleElement(key === "description" ? "textarea" : "input", "dlg-input");
        if (input.tagName === "INPUT") input.type = type;
        input.value = draft[key] ?? "";
        if (min !== null) input.min = String(min);
        if (max !== null) input.max = String(max);
        if (key === "name") input.maxLength = 255;
        if (key === "password") { input.maxLength = 4096; input.autocomplete = "new-password"; }
        const validateText = () => {
            const limit = key === "name" ? 255 : key === "password" ? 4096 : null;
            const value = key === "name" ? input.value.trim() : input.value;
            input.setCustomValidity(limit !== null && new TextEncoder().encode(value).length > limit ? t("roles.channel.tooLong") : "");
        };
        validateText();
        input.oninput = () => { draft[key] = type === "number" ? Number(input.value) : input.value; dirty = true; validateText(); };
        label.append(input); host.append(label);
    };
    const select = (labelKey, options, value, update) => {
        const label = roleElement("label", "role-field", t(labelKey));
        const input = roleElement("select", "dlg-input");
        input.dataset.field = labelKey;
        for (const [id, name, disabled] of options) {
            const option = roleElement("option", "", name); option.value = String(id); option.disabled = !!disabled; input.append(option);
        }
        input.value = String(value);
        input.onchange = () => { dirty = true; invalidateImpact(); update(input.value); controls(); };
        label.append(input); form.append(label);
        return input;
    };
    const render = () => {
        const focusedField = form.contains(document.activeElement) ? document.activeElement.dataset.field : null;
        form.replaceChildren();
        if (!snapshot) return;
        form.append(roleElement("p", "role-hint", snapshot.name || t("roles.channel.root")));
        if (kind === "channel_create" || kind === "channel_edit") {
            field("name"); field("topic"); field("description");
            const advanced = roleElement("details", "channel-disclosure");
            advanced.open = orderIndex !== undefined;
            advanced.append(roleElement("summary", "", t("roles.channel.advanced")));
            const settings = roleElement("div", "channel-disclosure-body");
            field("max_clients", "number", 0, 2147483647, settings); field("slow_mode_seconds", "number", 0, 2147483647, settings);
            field("order_index", "number", -2147483648, 2147483647, settings);
            field("opus_bitrate", "number", 0, 510000, settings);
            for (const key of ["opus_fec", "opus_dtx", "opus_stereo"]) {
                const label = roleElement("label", "role-checkbox");
                const input = roleElement("input"); input.type = "checkbox"; input.checked = !!draft[key];
                input.onchange = () => { draft[key] = input.checked; dirty = true; };
                label.append(input, document.createTextNode(t(`roles.channel.${key}`))); settings.append(label);
            }
            advanced.append(settings); form.append(advanced);
            if (kind === "channel_edit") form.append(roleButton(t("roles.iconEditor.title"), () => {
                if (!busy && current()) openChannelIcon(channelID);
            }));
        }
        if (kind === "channel_create") {
            select("roles.channel.type", [[0, t("roles.channel.temporary"), !snapshot.can_create_temporary],
                [1, t("roles.channel.semiPermanent"), !snapshot.can_create_permanent], [2, t("roles.channel.permanent"), !snapshot.can_create_permanent]],
            draft.channel_type, (value) => { draft.channel_type = Number(value); });
            field("password", "password");
            const presets = ["inherit", "public", "private", "readOnlyPreset", "listenOnly"];
            select("roles.channel.access", presets.map((key) => {
                const required = applyChannelPreset([], snapshot.everyone_id, key).map((entry) => entry.capability);
                return [key, t(key === "inherit" ? "roles.channel.inherit" : `roles.${key}`), key !== "inherit" &&
                    (!snapshot.can_manage_access || required.some((capability) => !snapshot.grantable_capabilities.includes(capability)))];
            }), preset, (value) => { preset = value; render(); controls(); });
            if (preset !== "inherit") form.append(roleElement("p", "role-hint", t(`roles.channel.preview.${preset}`)));
            if (preset === "private") {
                form.append(roleElement("p", "role-hint", t("roles.channel.privateHelp")));
                for (const role of snapshot.roles) {
                    const label = roleElement("label", "role-checkbox");
                    const input = roleElement("input"); input.type = "checkbox"; input.checked = selectedRoles.has(role.id);
                    input.onchange = () => { input.checked ? selectedRoles.add(role.id) : selectedRoles.delete(role.id); dirty = true; invalidateImpact(); controls(); };
                    label.append(input, document.createTextNode(role.name)); form.append(label);
                }
            }
        } else if (kind === "channel_move") {
            field("order_index", "number", -2147483648, 2147483647);
            select("roles.channel.destination", snapshot.destinations.map((entry) => [entry.id, entry.name || t("roles.channel.root")]), destination,
                (value) => { destination = Number(value); sync = false; render(); controls(); });
            const canSync = snapshot.destinations.find((entry) => entry.id === destination)?.can_sync;
            select("roles.channel.access", [["keep", t("roles.channel.keep")], ["sync", t("roles.channel.sync"), !canSync]],
                sync ? "sync" : "keep", (value) => { sync = value === "sync"; render(); controls(); });
            form.append(roleElement("p", "role-hint", t(sync ? "roles.channel.syncHelp" : "roles.channel.keepHelp")));
            if (!snapshot.destinations.some(entry => entry.id === destination)) form.append(roleElement("p", "role-hint",
                t(snapshot.destinations.length ? "roles.channel.destinationUnavailable" : "roles.channel.noDestinations")));
        } else if (kind === "channel_delete") {
            form.append(roleElement("p", "role-hint", t("roles.channel.deleteHelp", { name: snapshot.name, count: snapshot.affected_channels })));
        }
        impactView?.element.remove(); impactView = null;
        if (snapshot.can_manage_access && (kind === "channel_create" || kind === "channel_move")) {
            impactView = channelImpactPanel({ tabID, channelID: kind === "channel_create" ? 0 : channelID, rosterChannelID: channelID,
                tree: true, snapshot: { policy: { revision: snapshot.revision }, capabilities: snapshot.capabilities || [], impact_channel_ids: snapshot.impact_channel_ids }, current, change: treeChange,
                enabled: () => !busy && !needsRefresh && !committed,
                reviewed: value => { reviewedTree = value ? JSON.stringify(treeChange()) : ""; controls(); } });
            content.append(impactView.element);
        }
        if (focusedField) [...form.querySelectorAll("select")].find((input) => input.dataset.field === focusedField)?.focus();
    };
    const load = async () => {
        busy = true; controls();
        try {
            const next = await app.RoleChannelStateForTab(tabID, { kind, channel_id: channelID });
            if (!current()) return;
            snapshot = next; draft = structuredClone(next.settings);
            draft.channel_type = next.can_create_permanent ? 2 : 0; draft.password = "";
            preset = "inherit"; selectedRoles = new Set(); sync = false;
            destination = destinationID ?? next.destinations[0]?.id ?? 0;
            if (orderIndex !== undefined) draft.order_index = orderIndex;
            dirty = orderIndex !== undefined; needsRefresh = false; reviewedTree = ""; error.textContent = "";
            render();
        } catch { if (current()) { needsRefresh = true; error.textContent = t("roles.unavailable"); } }
        finally { if (current()) { busy = false; controls(); } }
    };
    const save = roleButton(t(`roles.channel.action.${kind}`), async () => {
        if (busy || committed || needsRefresh || !snapshot || !reviewed() || !current()) return;
        if (!dialog.checkValidity() || ((kind === "channel_create" || kind === "channel_edit") && !draft.name.trim())) {
            const invalid = form.querySelector("input:invalid,select:invalid,textarea:invalid");
            error.textContent = invalid?.validity.customError ? invalid.validationMessage : t("roles.channel.invalid");
            if (invalid?.closest("details")) invalid.closest("details").open = true;
            invalid?.focus(); return;
        }
        const request = { kind, expected_revision: snapshot.revision, channel_id: kind === "channel_create" ? 0 : channelID };
        if (kind === "channel_create" || kind === "channel_edit") {
            const { channel_type: channelType, password, ...settings } = draft;
            request.settings = { ...settings, name: settings.name.trim() };
            if (kind === "channel_create") {
                request.channel_type = channelType; request.password = password; request.parent_id = channelID;
                if (preset !== "inherit") {
                    request.access = access();
                }
            }
        } else if (kind === "channel_move") {
            request.parent_id = destination; request.sync_to_parent = sync;
            if (draft.order_index !== snapshot.settings.order_index) request.order_index = draft.order_index;
            const fingerprint = JSON.stringify(treeChange());
            if (sync && (!await confirm(t("roles.channel.syncConfirm")) || !current() || fingerprint !== JSON.stringify(treeChange()) || !reviewed())) return;
        }
        busy = true; error.textContent = ""; controls();
        try {
            const result = await app.ChangeRoleChannelForTab(tabID, request);
            if (!current()) return;
            committed = true; dirty = false;
            if (!result.enforcement_pending) closeDialog(overlay, "saved");
        } catch { if (current()) { needsRefresh = true; error.textContent = t("roles.channel.failed"); } }
        finally { if (current()) { busy = false; controls(); } }
    });
    save.type = "submit";
    if (kind === "channel_delete") save.classList.add("danger-btn");
    const refresh = roleButton(t("roles.refresh"), async () => {
        if ((!dirty || await confirm(t("roles.channel.discard"))) && current()) load();
    });
    const footer = roleElement("footer", "roles-footer"); footer.append(status, refresh, save);
    content.append(form); dialog.append(header, content, error, footer); overlay.append(dialog);
    mountServerDialog(overlay, { onCancel: () => {
        if (busy) return false;
        if (!dirty || committed) return true;
        confirm(t("roles.channel.discard")).then((accepted) => { if (accepted && current()) closeDialog(overlay); });
        return false;
    } });
    load();
}
