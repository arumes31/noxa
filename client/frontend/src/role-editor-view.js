import { currentLanguage, t } from "./i18n.js";
import { roleTemplate } from "./role-editor-state.js";

export function roleElement(tag, className = "", text = "") {
    const element = document.createElement(tag);
    element.className = className;
    element.textContent = text;
    return element;
}

export function roleButton(label, action, disabled = false) {
    const button = roleElement("button", "", label);
    button.type = "button";
    button.disabled = disabled;
    button.onclick = action;
    return button;
}

function field(parent, labelText, type, value, change) {
    const label = roleElement("label", "role-field", labelText);
    const input = roleElement("input", type === "checkbox" ? "role-checkbox" : "dlg-input");
    input.type = type;
    if (type === "checkbox") input.checked = !!value;
    else input.value = value;
    input.oninput = () => change(type === "checkbox" ? input.checked : input.value);
    label.append(input);
    parent.append(label);
    return input;
}

export function renderRoleList(host, model, select, move) {
    host.replaceChildren();
    for (const role of [...model.snapshot.policy.roles].sort((a, b) => b.position - a.position)) {
        const row = roleElement("div", "role-list-row");
        const button = roleButton(`${role.icon || ""} ${role.name}`.trim(), () => select(role.id), model.busy);
        button.className = "role-select";
        button.setAttribute("aria-pressed", String(role.id === model.draft?.id));
        const dot = roleElement("span", "role-color-dot");
        if (/^#[a-f\d]{6}$/i.test(role.color || "")) dot.style.backgroundColor = role.color;
        dot.setAttribute("aria-hidden", "true");
        button.prepend(dot);
        row.append(button);
        if (role.id !== model.snapshot.policy.everyone_id && model.snapshot.manageable_role_ids.includes(role.id)) {
            for (const [direction, key, symbol] of [[1, "roles.up", "↑"], [-1, "roles.down", "↓"]]) {
                const neighbor = model.snapshot.policy.roles.find((r) => r.position === role.position + direction);
                const canMove = neighbor && neighbor.id !== model.snapshot.policy.everyone_id &&
                    model.snapshot.manageable_role_ids.includes(neighbor.id);
                const control = roleButton(symbol, () => move(role.id, direction), model.busy || model.dirty || !canMove);
                control.setAttribute("aria-label", `${t(key)}: ${role.name}`);
                control.title = control.getAttribute("aria-label");
                row.append(control);
            }
        }
        host.append(row);
    }
}

export function renderRoleForm(host, model, changed) {
    host.replaceChildren();
    const { draft, snapshot } = model;
    if (!draft) return;
    const editable = !draft.id || snapshot.manageable_role_ids.includes(draft.id);
    const form = roleElement("fieldset", "role-form");
    form.disabled = model.busy || !editable;
    if (!editable) host.append(roleElement("p", "role-hint", t("roles.readOnly")));
    if (draft.id === snapshot.policy.everyone_id) host.append(roleElement("p", "role-hint", t("roles.everyone")));
    if (!draft.id) {
        const label = roleElement("label", "role-field", t("roles.template"));
        const select = roleElement("select", "dlg-input");
        for (const [value, key] of [["blank", "roles.blank"], ["member", "roles.member"], ["moderator", "roles.moderator"], ["administrator", "roles.admin"]]) {
            if (value === "administrator" && snapshot.actor_id !== snapshot.policy.owner_id) continue;
            const option = roleElement("option", "", t(key));
            option.value = value;
            select.append(option);
        }
        select.onchange = () => {
            draft.permissions = roleTemplate(select.value, snapshot.grantable_capabilities || []);
            draft.name = select.options[select.selectedIndex].textContent;
            changed(true);
        };
        label.append(select);
        form.append(label);
    }
    const name = field(form, t("roles.name"), "text", draft.name, (value) => { draft.name = value; changed(); });
    name.maxLength = 100;
    name.disabled = draft.id === snapshot.policy.everyone_id;
    const cosmetics = roleElement("div", "role-cosmetics");
    const color = field(cosmetics, t("roles.color"), "text", draft.color, (value) => { draft.color = value; changed(); });
    color.placeholder = "#RRGGBB";
    color.maxLength = 7;
    const icon = field(cosmetics, t("roles.icon"), "text", draft.icon, (value) => { draft.icon = value; changed(); });
    icon.maxLength = 16;
    form.append(cosmetics);
    field(form, t("roles.hoist"), "checkbox", draft.hoist, (value) => { draft.hoist = value; changed(); });
    const search = field(form, t("roles.search"), "search", "", () => {});
    const list = roleElement("div", "role-capabilities");
    const capabilityName = (key) => snapshot.capabilities.find((c) => c.key === key)?.[currentLanguage()] || key;
    const renderCapabilities = () => {
        list.replaceChildren();
        let group = "";
        let matches = 0;
        for (const capability of snapshot.capabilities) {
            const title = capability[currentLanguage()] || capability.en;
            if (!`${title} ${capability.key}`.toLowerCase().includes(search.value.toLowerCase().trim())) continue;
            matches++;
            if (capability.group !== group) {
                group = capability.group;
                list.append(roleElement("h4", "role-group-title", t(`roles.${group}`)));
            }
            const row = roleElement("label", "role-capability");
            const control = roleElement("input");
            control.type = "checkbox";
            control.checked = draft.permissions.includes(capability.key);
            control.disabled = (!control.checked && !(snapshot.grantable_capabilities || []).includes(capability.key)) ||
                (capability.key === "administrator" && (snapshot.actor_id !== snapshot.policy.owner_id || draft.id === snapshot.policy.everyone_id));
            control.onchange = () => {
                draft.permissions = draft.permissions.filter((key) => key !== capability.key);
                if (control.checked) draft.permissions.push(capability.key);
                changed();
            };
            const copy = roleElement("span", "", title);
            if (capability.key === "administrator") copy.append(roleElement("small", "role-hint", t("roles.ownerOnly")));
            else if (capability.requires?.length) copy.append(roleElement("small", "role-hint", t("roles.requires", { names: capability.requires.map(capabilityName).join(", ") })));
            row.append(control, copy);
            list.append(row);
        }
        if (!matches) list.append(roleElement("p", "role-hint", t("roles.noMatches")));
    };
    search.oninput = renderCapabilities;
    renderCapabilities();
    form.append(list);
    host.append(form);
}
