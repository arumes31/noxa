import { closeDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { copyToClipboard } from "./clipboard.js";
import { roleElement, roleButton } from "./role-editor-view.js";
import { buildChannelInvite } from "./channel-invite-links.js";

export function channelInvitePanel({ tabID, channelID, current }) {
    const section = roleElement("section", "access-section access-invite");
    const title = roleElement("h4", "access-section-title"); title.innerHTML = icon("link"); title.append(document.createTextNode(t("midnight.inviteTitle")));
    const fields = roleElement("div", "access-invite-fields");
    const label = roleElement("label", "role-field", t("midnight.inviteLink"));
    const row = roleElement("div", "access-invite-link");
    const link = roleElement("input", "dlg-input"); link.readOnly = true; link.setAttribute("aria-label", t("midnight.inviteLink"));
    const copy = roleButton(t("midnight.copyInvite"), () => { if (current() && link.value) void copyToClipboard(link.value, { isCurrent: current }); });
    copy.innerHTML = icon("copy"); copy.title = t("midnight.copyInvite"); copy.setAttribute("aria-label", copy.title); copy.disabled = true;
    row.append(link, copy); label.append(row);
    const accessLabel = roleElement("label", "role-field", t("midnight.inviteAccess"));
    const access = roleElement("select", "dlg-input");
    for (const kind of ["guest", "member"]) { const option = roleElement("option", "", t("midnight.invite." + kind)); option.value = kind; access.append(option); }
    accessLabel.append(access); fields.append(label, accessLabel);
    const status = roleElement("p", "role-hint"); status.setAttribute("role", "status");
    const actions = roleElement("div", "access-invite-actions");
    let busy = false, active = false;
    const canManage = () => !!window.__noxa.state.channels.find(c => c.ChannelID === channelID)?.Access?.can_manage_access;
    const update = () => { regenerate.disabled = revoke.disabled = access.disabled = busy || !canManage(); revoke.hidden = !active; copy.disabled = busy || !link.value; };
    const request = async action => {
        if (busy || !current() || action !== "get" && !canManage()) return;
        busy = true; update();
        try {
            const result = await window.go.main.App.ChannelInviteForTab(tabID, { action, channel_id: channelID, ...(action === "regenerate" ? { access: access.value } : {}) });
            if (!current()) return;
            active = result.active; if (result.access) access.value = result.access;
            link.value = result.token ? buildChannelInvite(window.__noxa.state.lastConnect?.addr || "", channelID, result.token) || "" : "";
            link.placeholder = t(active ? "midnight.inviteExisting" : "midnight.inviteNone");
            status.textContent = result.token ? t("midnight.inviteReady") : action === "revoke" ? t("midnight.inviteRevoked") : "";
            section.title = t("midnight.inviteHelp");
        } catch { if (current()) status.textContent = t("midnight.inviteFailed"); }
        finally { if (current()) { busy = false; update(); } }
    };
    const regenerate = roleButton(t("midnight.regenerateInvite"), () => void request("regenerate"));
    const revoke = roleButton(t("midnight.revokeInvite"), () => void request("revoke")); revoke.hidden = true;
    actions.append(regenerate, revoke);
    section.append(title, fields, roleElement("p", "role-hint access-invite-help", t("midnight.inviteHelpShort")), actions, status);
    update();
    queueMicrotask(() => void request("get"));
    return section;
}

export function openChannelInvite(channelID) {
    const tabID = window.__noxa.state.activeTabID;
    const overlay = roleElement("div", "dlg-overlay");
    const dialog = roleElement("section", "dlg channel-invite-dialog");
    dialog.append(roleElement("h3", "", t("midnight.inviteTitle")), channelInvitePanel({ tabID, channelID, current: () => isCurrentServerDialog(overlay) }), roleButton(t("common.close"), () => closeDialog(overlay)));
    overlay.append(dialog); mountServerDialog(overlay);
}
