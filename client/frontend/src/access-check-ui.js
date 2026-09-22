import { currentLanguage, t } from "./i18n.js";
import { roleButton, roleElement } from "./role-editor-view.js";

// Access preview reads only authorization decisions; it never joins a channel,
// subscribes to content or requests encryption keys on behalf of the subject.
export function accessCheckPanel({ tabID, channelID, snapshot, current, memberID, memberName }) {
    const panel = roleElement("section", "access-check");
    panel.append(roleElement("h4", "", t("roles.check")));
    panel.append(roleElement("p", "role-hint", t("roles.checkSaved")));
    const memberLabel = roleElement("label", "role-field", t("roles.previewSubject"));
    const members = roleElement("select", "dlg-input");
    const guest = roleElement("option", "", t("roles.guest"));
    guest.value = "0";
    members.append(guest);
    const initialID = memberID?.() || 0;
    if (initialID > 0) {
        const option = roleElement("option", "", memberName || t("roles.memberReference", { id: initialID }));
        option.value = String(initialID);
        members.append(option);
        members.value = option.value;
    }
    memberLabel.append(members);
    const searchLabel = roleElement("label", "role-field", t("roles.previewSearch"));
    const search = roleElement("input", "dlg-input");
    search.type = "search";
    searchLabel.append(search);
    const searchStatus = roleElement("p", "role-hint");
    searchStatus.setAttribute("role", "status");
    const label = roleElement("label", "role-field", t("roles.previewCapability"));
    const select = roleElement("select", "dlg-input");
    for (const capability of snapshot.capabilities.filter((c) => !channelID || c.channel)) {
        const option = roleElement("option", "", capability[currentLanguage()] || capability.en);
        option.value = capability.key;
        select.append(option);
    }
    label.append(select);
    const result = roleElement("p", "role-hint");
    result.setAttribute("role", "status");
    const hierarchy = roleElement("p", "role-hint");
    let serial = 0, searchSerial = 0;
    const active = () => current() && panel.isConnected;
    const clearResult = () => { serial++; result.textContent = hierarchy.textContent = ""; check.disabled = false; };
    members.onchange = select.onchange = clearResult;
    search.onchange = async () => {
        const request = ++searchSerial;
        searchStatus.textContent = t("roles.loading");
        try {
            const page = await window.go.main.App.RoleMembersForTab(tabID, { channel_id: channelID, search: search.value, expected_revision: snapshot.policy.revision });
            if (!active() || request !== searchSerial) return;
            const selected = members.selectedOptions[0].cloneNode(true);
            members.replaceChildren(guest);
            for (const entry of page.entries || []) {
                const option = roleElement("option", "", `${entry.nickname || entry.unique_id} (#${entry.user_id})`);
                option.value = String(entry.user_id);
                members.append(option);
            }
            if (selected.value !== "0" && ![...members.options].some((o) => o.value === selected.value)) members.append(selected);
            members.value = selected.value;
            searchStatus.textContent = t(page.more ? "roles.previewMore" : "roles.previewFound", { count: page.entries?.length || 0 });
        } catch { if (active() && request === searchSerial) searchStatus.textContent = t("roles.conflict"); }
    };
    const check = roleButton(t("roles.check"), async () => {
        const target = Number(members.value), capability = select.value;
        if (!active() || !Number.isSafeInteger(target) || target < 0) return;
        const request = ++serial;
        check.disabled = true;
        result.textContent = t("roles.loading");
        hierarchy.textContent = "";
        try {
            const response = await window.go.main.App.CheckAccessForTab(tabID, { user_id: target, channel_id: channelID,
                capability, expected_revision: snapshot.policy.revision });
            if (!active() || request !== serial) return;
            const decision = response.decision;
            if (decision.revision !== snapshot.policy.revision) throw new Error("stale decision");
            const reason = decision.reason === "requires" ? "requiresReason" : decision.reason;
            result.textContent = `${t(decision.allowed ? "roles.allowed" : "roles.denied")} — ${t(`roles.${reason}`)}`;
            if (decision.requirement) {
                const prerequisite = snapshot.capabilities.find((c) => c.key === decision.requirement);
                result.textContent += ` (${prerequisite?.[currentLanguage()] || decision.requirement})`;
            }
            if (decision.role_ids?.length) result.textContent += `: ${decision.role_ids.map((id) => snapshot.policy.roles.find((r) => r.id === id)?.name || "").filter(Boolean).join(", ")}`;
            result.textContent += ` · ${t("roles.checkedRevision", { revision: decision.revision })}`;
            hierarchy.textContent = t(response.can_manage_member ? "roles.hierarchyAllowed" : "roles.hierarchyDenied");
        } catch { if (active() && request === serial) result.textContent = t("roles.conflict"); }
        finally { if (active() && request === serial) check.disabled = false; }
    });
    panel.append(searchLabel, searchStatus, memberLabel, label, check, result, hierarchy);
    return panel;
}
