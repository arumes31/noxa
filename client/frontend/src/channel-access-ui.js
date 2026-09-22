import { currentLanguage, t } from "./i18n.js";
import { closeDialog, confirmDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { roleButton, roleElement } from "./role-editor-view.js";
import { applyChannelPreset, channelOverrideChanges, setChannelOverride } from "./channel-access-state.js";
import { accessCheckPanel } from "./access-check-ui.js";
import { channelImpactPanel } from "./channel-impact-ui.js";
import "./roles.css";

export function openChannelAccess(channelID) {
    const app = window.go.main.App;
    const tabID = window.__noxa.state.activeTabID;
    const overlay = roleElement("div", "dlg-overlay");
    const dialog = roleElement("section", "dlg roles-dialog");
    const header = roleElement("header", "roles-header");
    header.append(roleElement("h3", "", t("roles.accessTitle")), roleButton(t("common.close"), () => closeDialog(overlay, "cancel")));
    const content = roleElement("div", "channel-access-content");
    const status = roleElement("p", "role-hint", t("roles.loading"));
    status.setAttribute("role", "status");
    const error = roleElement("p", "role-error");
    error.setAttribute("role", "alert");
    let snapshot, saved, draft, busy = false, dirty = false, needsRefresh = false, subject = { role_id: 0 }, searchSerial = 0;
    let impactView, reviewedDraft = "";
    const memberNames = new Map();
    const current = () => isCurrentServerDialog(overlay);
    const confirm = (message) => confirmDialog({ title: t("roles.accessTitle"), message,
        confirmLabel: t("common.apply"), cancelLabel: t("common.cancel"), serverScoped: true });
    const setDirty = () => { dirty = JSON.stringify(saved) !== JSON.stringify(draft); reviewedDraft = ""; impactView?.invalidate(); buttons(); };
    const change = () => ({ kind: "channel_access_set", expected_revision: snapshot.policy.revision, channel: draft });
    const syncedOverrides = () => saved.synced ? snapshot.effective_overrides || snapshot.parent_overrides || [] : snapshot.parent_overrides || [];
    const syncDescription = () => {
        const changes = channelOverrideChanges(draft.overrides || [], snapshot.parent_overrides || []);
        const lines = changes.map((change) => {
            const name = change.user_id ? memberNames.get(change.user_id) || t("roles.memberReference", { id: change.user_id }) :
                snapshot.policy.roles.find((role) => role.id === change.role_id)?.name || String(change.role_id);
            const capability = snapshot.capabilities.find((item) => item.key === change.capability);
            return `${name} · ${capability?.[currentLanguage()] || capability?.en || change.capability}: ${t(`roles.${change.before}`)} → ${t(`roles.${change.after}`)}`;
        });
        return [t("roles.syncAsk"), lines.length ? lines.join("\n") : t("roles.noOverrideChanges"), t("roles.syncImpact")].join("\n\n");
    };
    const buttons = () => {
        save.disabled = busy || !dirty || needsRefresh || reviewedDraft !== JSON.stringify(draft);
        discard.disabled = busy || !dirty;
        refreshButton.disabled = busy;
        status.textContent = busy ? t("roles.saving") : dirty ? t("roles.unsaved") : "";
        impactView?.update();
    };
    const render = () => {
        content.replaceChildren();
        if (!snapshot) return;
        const syncRow = roleElement("div", "roles-header");
        syncRow.append(roleElement("p", "role-hint", t(draft.synced ? "roles.synced" : "roles.custom")));
        syncRow.append(roleButton(t(draft.synced ? "roles.customize" : "roles.sync"), async () => {
            if (!draft.synced && (!await confirm(syncDescription()) || !current())) return;
            draft = { ...draft, synced: !draft.synced, overrides: draft.synced ? structuredClone(syncedOverrides()) : [] };
            setDirty(); render();
        }, busy || (!draft.synced && (!draft.parent_id || snapshot.parent_access_available === false))));
        content.append(syncRow);
        if (snapshot.parent_access_available === false && draft.parent_id) content.append(roleElement("p", "role-hint", t("roles.parentAccessUnavailable")));
        const form = roleElement("fieldset", "role-form");
        form.disabled = busy || draft.synced;
        const presets = roleElement("div", "roles-header");
        for (const preset of ["public", "private", "readOnlyPreset", "listenOnly"]) presets.append(roleButton(t(`roles.${preset}`), () => {
            draft.overrides = applyChannelPreset(draft.overrides || [], snapshot.policy.everyone_id, preset);
            subject = { role_id: snapshot.policy.everyone_id };
            setDirty(); render();
        }));
        form.append(roleElement("h4", "", t("roles.presets")), presets, roleElement("p", "role-hint", t("roles.presetHelp")));
        const label = roleElement("label", "role-field", t("roles.subject"));
        const subjects = roleElement("select", "dlg-input");
        for (const role of snapshot.policy.roles) {
            const option = roleElement("option", "", role.name);
            option.value = `role:${role.id}`;
            option.disabled = role.id !== snapshot.policy.everyone_id && !snapshot.manageable_role_ids.includes(role.id);
            subjects.append(option);
        }
        const members = new Set((draft.overrides || []).filter((o) => o.user_id).map((o) => o.user_id));
        if (subject.user_id) members.add(subject.user_id);
        for (const id of members) {
            const option = roleElement("option", "", memberNames.get(id) || t("roles.memberReference", { id }));
            option.value = `member:${id}`;
            subjects.append(option);
        }
        subjects.value = subject.user_id ? `member:${subject.user_id}` : `role:${subject.role_id}`;
        subjects.onchange = () => { const [kind, id] = subjects.value.split(":"); subject = kind === "member" ? { user_id: Number(id) } : { role_id: Number(id) }; render(); };
        label.append(subjects);
        form.append(label);
        const memberLabel = roleElement("label", "role-field", t("roles.searchMembers"));
        const search = roleElement("input", "dlg-input");
        search.type = "search";
        const searchResults = roleElement("div", "role-search-results");
        search.onchange = async () => {
            const serial = ++searchSerial;
            try {
                const result = await app.RoleMembersForTab(tabID, { channel_id: channelID, search: search.value, expected_revision: snapshot.policy.revision });
                if (!current() || serial !== searchSerial || !search.isConnected) return;
                searchResults.replaceChildren();
                for (const member of result.entries) {
                    memberNames.set(member.user_id, member.nickname || member.unique_id);
                    searchResults.append(roleButton(member.nickname || member.unique_id, () => { subject = { user_id: member.user_id }; render(); }, !member.manageable));
                }
            } catch { if (current()) error.textContent = t("roles.unavailable"); }
        };
        memberLabel.append(search);
        form.append(memberLabel, searchResults);
        const overrides = draft.synced ? syncedOverrides() : draft.overrides || [];
        for (const capability of snapshot.capabilities.filter((c) => c.channel)) {
            const row = roleElement("label", "access-override", capability[currentLanguage()] || capability.en);
            const select = roleElement("select", "dlg-input");
            select.disabled = !snapshot.grantable_capabilities.includes(capability.key);
            for (const effect of ["inherit", "allow", "deny"]) {
                const option = roleElement("option", "", t(`roles.${effect}`)); option.value = effect; select.append(option);
            }
            select.value = overrides.find((o) => Number(o.role_id || 0) === Number(subject.role_id || 0) && Number(o.user_id || 0) === Number(subject.user_id || 0) && o.capability === capability.key)?.effect || "inherit";
            select.onchange = () => { draft.overrides = setChannelOverride(draft.overrides || [], subject, capability.key, select.value); setDirty(); };
            row.append(select); form.append(row);
        }
        content.append(form);
        impactView = channelImpactPanel({ tabID, channelID, snapshot, current, change,
            enabled: () => !busy && dirty && !needsRefresh,
            reviewed: value => { reviewedDraft = value ? JSON.stringify(draft) : ""; buttons(); } });
        // A rebuilt panel returns to the edited channel and has no displayed
        // review. Do not retain approval from a previously selected descendant.
        reviewedDraft = "";
        buttons();
        content.append(impactView.element);
        content.append(accessCheckPanel({ tabID, channelID, snapshot, current, memberID: () => subject.user_id || 0,
            memberName: subject.user_id ? memberNames.get(subject.user_id) || t("roles.memberReference", { id: subject.user_id }) : t("roles.guest") }));
    };
    const load = async () => {
        busy = true; buttons();
        try {
            const next = await app.RoleStateForTab(tabID, channelID);
            if (!current()) return;
            const channel = next.policy.channels.find((ch) => ch.channel_id === channelID);
            if (!channel) throw new Error("channel unavailable");
            snapshot = next; saved = structuredClone(channel); draft = structuredClone(channel);
            dirty = needsRefresh = false; reviewedDraft = ""; subject = { role_id: next.policy.everyone_id }; error.textContent = "";
        } catch { if (current()) error.textContent = t("roles.unavailable"); }
        finally { if (current()) { busy = false; buttons(); render(); } }
    };
    const save = roleButton(t("common.save"), async () => {
        if (busy || needsRefresh || !dirty || reviewedDraft !== JSON.stringify(draft) || !current()) return;
        busy = true; buttons(); render();
        try {
            const result = await app.RoleChangeForTab(tabID, change());
            if (!current()) return;
            needsRefresh = true;
            dirty = false;
            if (result.enforcement_pending) {
                error.textContent = t("roles.enforcementPending");
                return;
            }
            await load();
        } catch { if (current()) { needsRefresh = true; error.textContent = t("roles.conflict"); } }
        finally { if (current()) { busy = false; buttons(); render(); } }
    });
    const discard = roleButton(t("roles.discard"), () => { draft = structuredClone(saved); dirty = false; buttons(); render(); });
    const refreshButton = roleButton(t("roles.refresh"), async () => { if ((!dirty || await confirm(t("roles.discardAsk"))) && current()) load(); });
    const footer = roleElement("footer", "roles-footer");
    footer.append(status, refreshButton, discard, save);
    dialog.append(header, content, error, footer); overlay.append(dialog);
    mountServerDialog(overlay, { onCancel: () => {
        if (!dirty) return true;
        confirm(t("roles.discardAsk")).then((accepted) => { if (accepted && current()) closeDialog(overlay); });
        return false;
    }, onClose: () => searchSerial++ });
    load();
}
