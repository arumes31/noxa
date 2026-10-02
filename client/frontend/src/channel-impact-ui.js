import { currentLanguage, t } from "./i18n.js";
import { roleButton, roleElement } from "./role-editor-view.js";

// Role baselines and optional member lookups use the server's draft evaluator.
export function channelImpactPanel({ tabID, channelID, rosterChannelID = channelID, tree = false, snapshot, current, change, enabled, reviewed }) {
    const element = roleElement("section", "channel-impact");
    element.append(roleElement("h4", "", t("roles.impact.title")), roleElement("p", "role-hint", t("roles.impact.help")));
    if (tree && channelID === 0) element.append(roleElement("p", "role-hint", t("roles.impact.newChannel")));
    const status = roleElement("p", "role-hint"); status.setAttribute("role", "status");
    const rows = roleElement("div", "channel-impact-members");
    const memberRows = roleElement("div", "channel-impact-members");
    const searchLabel = roleElement("label", "role-field", t("roles.impact.search"));
    const search = roleElement("input", "dlg-input"); search.type = "search"; search.maxLength = 128;
    searchLabel.append(search);
    let serial = 0, busy = false, afterID = 0, more = false, scopeID = channelID, scopeSelect;
    const active = () => current() && element.isConnected;
    const update = () => {
        // Keep the initiating control in the keyboard focus order while waiting.
        preview.disabled = !enabled(); next.disabled = !more || !enabled();
        next.hidden = !more;
        lookup.disabled = !enabled() || !search.value.trim();
        lookup.setAttribute("aria-disabled", String(busy || lookup.disabled));
        preview.setAttribute("aria-disabled", String(busy || preview.disabled));
        next.setAttribute("aria-disabled", String(busy || next.disabled));
        if (scopeSelect) scopeSelect.disabled = !enabled();
    };
    const invalidate = () => {
        serial++; busy = false; more = false; afterID = 0;
        rows.replaceChildren(); memberRows.replaceChildren(); status.textContent = ""; update();
    };
    const renderImpact = (name, decisions, host) => {
        const row = roleElement("article", "channel-impact-member");
        row.append(roleElement("h5", "", name));
        if (!decisions.length) row.append(roleElement("p", "role-hint", t("roles.impact.unchanged")));
        else {
            const list = roleElement("ul");
            for (const item of decisions) {
                const capability = snapshot.capabilities.find(c => c.key === item.capability);
                if (!capability || typeof item.before !== "boolean" || typeof item.after !== "boolean") throw new Error("invalid impact capability");
                list.append(roleElement("li", "", `${capability[currentLanguage()] || capability.en}: ${t(item.before ? "roles.allowed" : "roles.denied")} → ${t(item.after ? "roles.allowed" : "roles.denied")}`));
            }
            row.append(list);
        }
        host.append(row);
    };
    const load = async (membersOnly = false, append = false) => {
        if (!active() || busy || !enabled()) return;
        const query = membersOnly ? search.value.trim() : "";
        if (membersOnly && !query) return;
        const request = ++serial, draft = structuredClone(change()), fingerprint = JSON.stringify(draft), requestedScope = scopeID;
        const valid = () => active() && request === serial && requestedScope === scopeID && fingerprint === JSON.stringify(change());
        busy = true; reviewed(false); update(); status.textContent = t("roles.impact.loading");
        rows.replaceChildren(); memberRows.replaceChildren(); more = false;
        try {
            const page = membersOnly ? await window.go.main.App.RoleMembersForTab(tabID, { channel_id: scopeID === channelID ? rosterChannelID : scopeID,
                expected_revision: snapshot.policy.revision, after_id: append ? afterID : 0, search: query }) : { revision: snapshot.policy.revision, entries: [] };
            if (!valid()) return;
            if (page.revision !== snapshot.policy.revision || !Array.isArray(page.entries) || page.entries.length > 100) throw new Error("invalid member page");
            const members = page.entries;
            const ids = members.length ? members.map(member => member.user_id) : [0];
            const result = await window.go.main.App.PreviewChannelAccessForTab(tabID, { [tree ? "tree" : "change"]: draft, user_ids: ids,
                ...(scopeID === channelID ? {} : { scope_channel_id: scopeID }) });
            if (!valid()) return;
            if (result.revision !== snapshot.policy.revision || result.channel_id !== requestedScope ||
                !Array.isArray(result.roles) || !result.roles.length || result.members?.length !== ids.length ||
                result.members.some((member, i) => member.user_id !== ids[i] || !Array.isArray(member.changes))) throw new Error("invalid impact response");
            for (const role of result.roles) {
                if (typeof role.name !== "string" || !Array.isArray(role.changes)) throw new Error("invalid role impact");
                renderImpact(role.name, role.changes, rows);
            }
            for (let i = 0; i < members.length; i++) {
                const member = members[i], impact = result.members[i];
                if (!Array.isArray(impact.permissions)) throw new Error("invalid member permissions");
                renderImpact(member.nickname || member.unique_id || t("roles.memberReference", { id: member.user_id }), impact.permissions, memberRows);
            }
            afterID = page.entries.at(-1)?.user_id || 0; more = !!page.more;
            if (more && !afterID) throw new Error("invalid member cursor");
            status.textContent = t(membersOnly ? (more ? "roles.impact.more" : "roles.impact.page") : "roles.impact.roles", { count: membersOnly ? page.entries.length : result.roles.length, revision: result.revision });
            if (membersOnly && !members.length) memberRows.append(roleElement("p", "role-hint", t("roles.noMembers")));
            reviewed(true);
        } catch {
            if (valid()) { rows.replaceChildren(); memberRows.replaceChildren(); more = false; status.textContent = t("roles.impact.failed"); }
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
    const lookup = roleButton(t("roles.impact.lookup"), () => load(true));
    const next = roleButton(t("roles.impact.next"), () => load(true, true), true);
    search.oninput = () => { serial++; busy = false; more = false; memberRows.replaceChildren(); status.textContent = ""; update(); };
    search.onkeydown = event => { if (event.key === "Enter") { event.preventDefault(); void load(true); } };
    const actions = roleElement("div", "roles-header"); actions.append(lookup, next);
    element.append(preview, status, rows, searchLabel, actions, memberRows); update();
    return { element, invalidate, update };
}
