import { sessionUserID } from "./session-identity.js";
import { t } from "./i18n.js";
import { closeContextMenu } from "./context-menu.js";

// Resolve authorization before exposing moderation entries. The mutation repeats
// these checks against the live target under the server's pinned policy and registry lock.
export function attachRelayActions(menu, member, tabID, current) {
    const app = window.go.main.App;
    if (!member.client_id || !app.SessionInfoForTab || !app.RevokeRelayForTab) return;
    void (async () => {
        const session = await app.SessionInfoForTab(tabID);
        if (!current() || !session?.managed_relay) return;
        if (session.client_id !== member.client_id && member.unique_id !== sessionUserID(window.__noxa.state)) {
            const snapshot = await app.RoleStateForTab(tabID, 0);
            if (!current()) return;
            const revision = snapshot.policy.revision;
            const [kick, info, page] = await Promise.all([
                app.CheckAccessForTab(tabID, { user_id: 0, channel_id: 0, capability: "kick_members", expected_revision: revision }),
                app.CheckAccessForTab(tabID, { user_id: 0, channel_id: 0, capability: "view_connection_info", expected_revision: revision }),
                app.RoleMembersForTab(tabID, { search: member.unique_id, expected_revision: revision }),
            ]);
            if (!current() || !kick.decision?.allowed || !info.decision?.allowed || page.revision !== revision || !page.entries?.some(entry => entry.unique_id === member.unique_id && entry.manageable)) return;
        }
        for (const all of [false, true]) {
            const button = document.createElement("button"); button.type = "button"; button.className = "ctx-action";
            button.setAttribute("role", "menuitem"); button.tabIndex = -1;
            button.textContent = t(all ? "relay.revokeAll" : "relay.revokeSession");
            button.title = t("relay.revokeHelp");
            button.onclick = async () => {
                if (!current()) return;
                closeContextMenu(menu);
                try { await app.RevokeRelayForTab(tabID, member.client_id, all); window.__noxa.toast(t("relay.revoked")); }
                catch (error) { window.__noxa.toast(String(error), "warn"); }
            };
            menu.append(button);
        }
    })().catch(() => {});
}
