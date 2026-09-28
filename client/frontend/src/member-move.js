import { t } from "./i18n.js";
import { closeDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { roleButton, roleElement } from "./role-editor-view.js";

export function openMemberMove(member) {
    const v = window.__noxa, tabID = v.state.activeTabID;
    const overlay = roleElement("div", "dlg-overlay");
    const dialog = roleElement("section", "dlg");
    dialog.append(roleElement("h3", "", t("context.move")), roleElement("p", "", member.nickname || member.unique_id));
    const label = roleElement("label", "dlg-label", t("context.searchChannels"));
    const search = roleElement("input", "dlg-input"); search.type = "search"; label.append(search);
    const list = roleElement("div", "member-move-list");
    const error = roleElement("p", "role-error"); error.setAttribute("role", "alert");
    let busy = false;
    const render = () => {
        list.replaceChildren();
        const current = v.state.clients.find(c => c.client_id === member.client_id && c.unique_id === member.unique_id);
        if (!current) { error.textContent = t("context.memberGone"); return; }
        const channels = v.state.channels.filter(channel => channel.ChannelID !== current.channel_id && channel.Name.toLocaleLowerCase().includes(search.value.toLocaleLowerCase()));
        for (const channel of channels) {
            list.append(roleButton(channel.Name, async () => {
                if (busy || !isCurrentServerDialog(overlay)) return;
                if (!v.state.clients.some(c => c.client_id === member.client_id && c.unique_id === member.unique_id)) { render(); return; }
                busy = true; error.textContent = ""; render(); search.disabled = true;
                try {
                    // The server rechecks source, destination and member hierarchy.
                    const result = await window.go.main.App.MoveClientForTab(tabID, member.client_id, channel.ChannelID);
                    if (!isCurrentServerDialog(overlay)) return;
                    if (result) throw new Error(result);
                    closeDialog(overlay);
                } catch (failure) { if (isCurrentServerDialog(overlay)) error.textContent = String(failure); }
                finally { busy = false; search.disabled = false; if (isCurrentServerDialog(overlay)) render(); }
            }, busy));
        }
        if (!channels.length) list.append(roleElement("p", "", t("context.noChannels")));
    };
    search.oninput = render;
    dialog.append(label, list, error, roleButton(t("common.close"), () => closeDialog(overlay)));
    overlay.append(dialog); mountServerDialog(overlay); render(); search.focus();
}
