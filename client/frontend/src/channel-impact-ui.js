import { currentLanguage, t } from "./i18n.js";
import { roleButton, roleElement } from "./role-editor-view.js";

// This is a page of member decisions for one draft and channel, not a count of
// every affected identity or descendant. It never impersonates a member.
export function channelImpactPanel({ tabID, channelID, rosterChannelID = channelID, tree = false, snapshot, current, change, enabled, reviewed }) {
    const element = roleElement("section", "channel-impact");
    element.append(roleElement("h4", "", t("roles.impact.title")), roleElement("p", "role-hint", t("roles.impact.help")));
    if (tree && channelID === 0) element.append(roleElement("p", "role-hint", t("roles.impact.newChannel")));
    const status = roleElement("p", "role-hint"); status.setAttribute("role", "status");
    const rows = roleElement("div", "channel-impact-members");
    let serial = 0, busy = false, afterID = 0, more = false, scopeID = channelID, scopeSelect;
    const active = () => current() && element.isConnected;
    const update = () => {
        // Keep the initiating control in the keyboard focus order while waiting.
        preview.disabled = !enabled(); next.disabled = !more || !enabled();
        preview.setAttribute("aria-disabled", String(busy || preview.disabled));
        next.setAttribute("aria-disabled", String(busy || next.disabled));
        if (scopeSelect) scopeSelect.disabled = !enabled();
    };
    const invalidate = () => {
        serial++; busy = false; more = false; afterID = 0;
        rows.replaceChildren(); status.textContent = ""; update();
    };
    const load = async (append) => {
        if (!active() || busy || !enabled()) return;
        const request = ++serial, draft = structuredClone(change()), fingerprint = JSON.stringify(draft), requestedScope = scopeID;
        const valid = () => active() && request === serial && requestedScope === scopeID && fingerprint === JSON.stringify(change());
        busy = true; reviewed(false); update(); status.textContent = t("roles.impact.loading");
        rows.replaceChildren();
        try {
            const page = await window.go.main.App.RoleMembersForTab(tabID, { channel_id: scopeID === channelID ? rosterChannelID : scopeID,
                expected_revision: snapshot.policy.revision, after_id: append ? afterID : 0, search: "" });
            if (!valid()) return;
            if (page.revision !== snapshot.policy.revision || !Array.isArray(page.entries) || page.entries.length > 100) throw new Error("invalid member page");
            const members = [{ user_id: 0, nickname: t("roles.guest") }, ...page.entries];
            const ids = members.map(member => member.user_id);
            const result = await window.go.main.App.PreviewChannelAccessForTab(tabID, { [tree ? "tree" : "change"]: draft, user_ids: ids,
                ...(scopeID === channelID ? {} : { scope_channel_id: scopeID }) });
            if (!valid()) return;
            if (result.revision !== snapshot.policy.revision || result.channel_id !== requestedScope ||
                result.members?.length !== members.length || result.members.some((member, i) => member.user_id !== ids[i] || !Array.isArray(member.changes))) throw new Error("invalid impact response");
            for (let i = 0; i < members.length; i++) {
                const member = members[i], impact = result.members[i];
                const row = roleElement("article", "channel-impact-member");
                row.append(roleElement("h5", "", member.nickname || member.unique_id || t("roles.memberReference", { id: member.user_id })));
                if (!impact.changes.length) row.append(roleElement("p", "role-hint", t("roles.impact.unchanged")));
                else {
                    const list = roleElement("ul");
                    for (const item of impact.changes) {
                        const capability = snapshot.capabilities.find(c => c.key === item.capability);
                        if (!capability || typeof item.before !== "boolean" || typeof item.after !== "boolean") throw new Error("invalid impact capability");
                        list.append(roleElement("li", "", `${capability[currentLanguage()] || capability.en}: ${t(item.before ? "roles.allowed" : "roles.denied")} → ${t(item.after ? "roles.allowed" : "roles.denied")}`));
                    }
                    row.append(list);
                }
                rows.append(row);
            }
            afterID = page.entries.at(-1)?.user_id || 0; more = !!page.more;
            if (more && !afterID) throw new Error("invalid member cursor");
            status.textContent = t(more ? "roles.impact.more" : "roles.impact.page", { count: page.entries.length, revision: result.revision });
            reviewed(true);
        } catch {
            if (valid()) { rows.replaceChildren(); more = false; status.textContent = t("roles.impact.failed"); }
        } finally { if (valid()) { busy = false; update(); } }
    };
    if (channelID > 0 && snapshot.impact_channel_ids?.length) {
        const label = roleElement("label", "role-field", t("roles.impact.scope"));
        scopeSelect = roleElement("select", "dlg-input");
        const own = roleElement("option", "", t("roles.impact.source")); own.value = String(channelID); scopeSelect.append(own);
        for (const id of snapshot.impact_channel_ids) {
            const name = window.__noxa.state.channels?.find(channel => channel.ChannelID === id)?.Name;
            const option = roleElement("option", "", name ? `${name} (#${id})` : t("roles.impact.descendant", { id }));
            option.value = String(id); scopeSelect.append(option);
        }
        scopeSelect.onchange = () => { scopeID = Number(scopeSelect.value); invalidate(); reviewed(false); };
        label.append(scopeSelect); element.append(label, roleElement("p", "role-hint", t("roles.impact.scopeHelp")));
    }
    const preview = roleButton(t("roles.impact.preview"), () => load(false));
    const next = roleButton(t("roles.impact.next"), () => load(true), true);
    const actions = roleElement("div", "roles-header"); actions.append(preview, next);
    element.append(actions, status, rows); update();
    return { element, invalidate, update };
}
