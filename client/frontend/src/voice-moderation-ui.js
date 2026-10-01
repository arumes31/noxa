import { t } from "./i18n.js";
import { closeDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { roleButton, roleElement } from "./role-editor-view.js";
import "./roles.css";

export function openVoiceModeration(member) {
    const tabID = window.__noxa.state.activeTabID;
    const overlay = roleElement("div", "dlg-overlay");
    const dialog = roleElement("section", "dlg roles-dialog voice-moderation-dialog");
    const header = roleElement("header", "roles-header");
    header.append(roleElement("h3", "", t("roles.voice.title")), roleButton(t("common.close"), () => closeDialog(overlay)));
    dialog.append(header, roleElement("p", "", member.nickname || member.unique_id), roleElement("p", "role-hint", t("roles.voice.help")));
    const field = (key, checked) => {
        const label = roleElement("label", "role-field", t(key));
        const input = roleElement("input", "role-checkbox");
        input.type = "checkbox";
        input.checked = !!checked;
        label.append(input);
        dialog.append(label);
        return input;
    };
    const mute = field("roles.voice.mute", member.server_muted);
    const deafen = field("roles.voice.deafen", member.server_deafened);
    const status = roleElement("p", "role-hint");
    status.setAttribute("role", "status");
    const error = roleElement("p", "role-error");
    error.setAttribute("role", "alert");
    let baseline = { muted: !!member.server_muted, deafened: !!member.server_deafened }, busy = false;
    const update = () => {
        mute.disabled = deafen.disabled = busy;
        save.disabled = busy || (mute.checked === baseline.muted && deafen.checked === baseline.deafened);
    };
    const save = roleButton(t("common.save"), async () => {
        if (busy || !isCurrentServerDialog(overlay)) return;
        const change = { client_id: member.client_id, channel_id: member.channel_id };
        if (mute.checked !== baseline.muted) change.muted = mute.checked;
        if (deafen.checked !== baseline.deafened) change.deafened = deafen.checked;
        busy = true;
        error.textContent = "";
        status.textContent = t("roles.saving");
        update();
        try {
            const result = await window.go.main.App.SetMemberVoiceForTab(tabID, change);
            if (!isCurrentServerDialog(overlay)) return;
            const current = window.__noxa.state.clients?.find((c) => c.client_id === result.client_id);
            const newer = current && (current.voice_revision || 0) > result.revision;
            baseline = newer ? { muted: !!current.server_muted, deafened: !!current.server_deafened } : { muted: result.muted, deafened: result.deafened };
            mute.checked = baseline.muted;
            deafen.checked = baseline.deafened;
            status.textContent = t("roles.voice.saved");
            // Only acknowledged state may enter the participant presentation.
            if (current && !newer) {
                current.server_muted = result.muted;
                current.server_deafened = result.deafened;
                current.voice_revision = result.revision;
                window.__noxa.renderTree?.();
            }
        } catch {
            if (isCurrentServerDialog(overlay)) {
                status.textContent = "";
                error.textContent = t("roles.voice.failed");
            }
        } finally {
            busy = false;
            if (isCurrentServerDialog(overlay)) update();
        }
    });
    mute.onchange = deafen.onchange = update;
    dialog.append(status, error, save);
    overlay.append(dialog);
    mountServerDialog(overlay);
    update();
}
