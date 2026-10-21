import { t } from "./i18n.js";
import { roleElement } from "./role-editor-view.js";
import { icon } from "./icons.js";

export function roleCapabilityAllowed(snapshot, overrides, role, capability, seen = new Set()) {
    if (seen.has(capability)) return false;
    const info = snapshot.capabilities.find(item => item.key === capability);
    if (!info) return false;
    const everyone = snapshot.policy.roles.find(item => item.id === snapshot.policy.everyone_id);
    let allowed = [everyone, role].some(item => item?.permissions?.includes(capability));
    if (role.permissions?.includes("administrator")) return true;
    for (const id of [...new Set([snapshot.policy.everyone_id, role.id])]) {
        const override = overrides.find(item => item.role_id === id && !item.user_id && item.capability === capability);
        if (override) allowed = override.effect === "allow";
    }
    return allowed && (info.requires || []).every(key => roleCapabilityAllowed(snapshot, overrides, role, key, new Set([...seen, capability])));
}

export function channelPermissionMatrix({ snapshot, overrides, disabled, change }) {
    const wrap = roleElement("div", "access-matrix-wrap");
    const table = roleElement("table", "access-matrix");
    const caption = roleElement("caption", "sr-only", t("midnight.permissions"));
    const head = roleElement("thead"), heading = roleElement("tr");
    const title = roleElement("th", "", t("midnight.permission")); title.scope = "col"; heading.append(title);
    const preferred = [snapshot.policy.default_member_role_id, snapshot.policy.everyone_id];
    const roles = [...snapshot.policy.roles].sort((a, b) => {
        const rank = role => preferred.indexOf(role.id) < 0 ? 2 + role.position : preferred.indexOf(role.id);
        return rank(a) - rank(b);
    });
    for (const role of roles) {
        const cell = roleElement("th", "", role.name); cell.scope = "col";
        cell.append(roleElement("small", "", t(role.id === snapshot.policy.everyone_id ? "midnight.guestBaseline" : role.id === snapshot.policy.default_member_role_id ? "midnight.memberBaseline" : "midnight.customRole")));
        heading.append(cell);
    }
    head.append(heading); const body = roleElement("tbody");
    const rows = [
        ["voice", "mic", ["connect", "speak"]], ["video", "camera", ["share_camera"]],
        ["screen", "screen", ["share_screen"]], ["messages", "chat", ["send_messages"]],
        ["files", "attach", ["upload_files"]], ["invite", "users", ["invite_others"]],
    ];
    for (const [key, glyph, capabilities] of rows) {
        const row = roleElement("tr"), label = t("midnight.permission." + key);
        const cell = roleElement("th", "access-permission-label"); cell.scope = "row";
        const image = roleElement("span", ""); image.innerHTML = icon(glyph);
        const copy = roleElement("span", ""); copy.append(roleElement("strong", "", label), roleElement("small", "", t("midnight.permissionHint." + key)));
        cell.append(image, copy); row.append(cell);
        for (const role of roles) {
            const supported = capabilities.every(capability => snapshot.capabilities.some(item => item.key === capability && item.channel));
            const td = roleElement("td");
            const toggle = roleElement("button", "permission-switch"); toggle.type = "button"; toggle.setAttribute("role", "switch");
            toggle.dataset.role = String(role.id); toggle.dataset.capability = capabilities.at(-1);
            toggle.setAttribute("aria-label", t("midnight.permissionCell", { capability: label, role: role.name }));
            const allowed = supported && capabilities.every(capability => roleCapabilityAllowed(snapshot, overrides, role, capability));
            toggle.setAttribute("aria-checked", String(allowed));
            const dependencies = new Set(capabilities);
            const collect = capability => { for (const required of snapshot.capabilities.find(item => item.key === capability)?.requires || []) if (!dependencies.has(required)) { dependencies.add(required); collect(required); } };
            capabilities.forEach(collect);
            toggle.disabled = disabled || !supported || [...dependencies].some(capability => !snapshot.grantable_capabilities.includes(capability)) ||
                (role.id !== snapshot.policy.everyone_id && !snapshot.manageable_role_ids.includes(role.id));
            toggle.title = supported ? t(allowed ? "roles.allow" : "roles.deny") : t("midnight.serverUpdateRequired");
            toggle.onclick = () => { if (toggle.isConnected) change({ role_id: role.id }, allowed ? capabilities : [...dependencies], allowed ? "deny" : "allow", capabilities.at(-1)); };
            const custom = capabilities.some(capability => overrides.some(entry => entry.role_id === role.id && !entry.user_id && entry.capability === capability));
            const inherit = roleElement("button", "permission-inherit", t(custom ? "midnight.resetRule" : "roles.inherit")); inherit.type = "button";
            inherit.disabled = toggle.disabled || !custom;
            inherit.setAttribute("aria-label", t("midnight.resetRuleFor", { capability: label, role: role.name }));
            inherit.onclick = () => { if (inherit.isConnected) change({ role_id: role.id }, [...dependencies], "inherit", capabilities.at(-1)); };
            td.append(toggle, inherit); row.append(td);
        }
        body.append(row);
    }
    table.append(caption, head, body); wrap.append(table); return wrap;
}
