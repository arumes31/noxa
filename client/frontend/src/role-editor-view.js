import { roleIconElement, prepareRoleIcon, ROLE_ICON_TOTAL_MAX } from "./role-icons.js";
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
        const button = roleButton(role.name, () => select(role.id), model.busy);
        button.className = "role-select";
        button.setAttribute("aria-pressed", String(role.id === model.draft?.id));
        const symbol = roleIconElement(role);
        if (/^#[a-f\d]{6}$/i.test(role.color || "")) symbol.style.color = role.color;
        button.prepend(symbol);
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
            draft.deletion_protected = select.value === "administrator";
            draft.icon = ""; draft.icon_image = "";
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
    const icon = field(cosmetics, t("roles.icon"), "text", draft.icon, (value) => { draft.icon = value; imageEditor.querySelector(".role-image-preview").replaceChildren(roleIconElement(draft)); changed(); });
    icon.maxLength = 16;
    icon.placeholder = t("roles.iconFallback");
    const imageEditor = renderRoleImageEditor(model, form, changed);
    form.append(cosmetics, imageEditor);
    field(form, t("roles.hoist"), "checkbox", draft.hoist, (value) => { draft.hoist = value; changed(); });
    if (!draft.id || draft.id !== snapshot.policy.everyone_id) field(form, t("roles.mentionable"), "checkbox", draft.mentionable, (value) => { draft.mentionable = value; changed(); });
    if (draft.id !== snapshot.policy.everyone_id) {
        const protect = field(form, t("roles.protectDeletion"), "checkbox", draft.deletion_protected, (value) => { draft.deletion_protected = value; changed(); });
        protect.disabled = snapshot.actor_id !== snapshot.policy.owner_id;
        form.append(roleElement("p", "role-hint", t("roles.protectDeletionHelp")));
    }
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

function renderRoleImageEditor(model, form, changed) {
    const draft = model.draft;
    const box = roleElement("div", "role-image-editor");
    const preview = roleElement("div", "role-image-preview");
    const controls = roleElement("div", "role-image-controls");
    const label = roleElement("label", "role-field", t("roles.imageUpload"));
    const upload = roleElement("input", "dlg-input");
    upload.type = "file"; upload.accept = "image/png,.png";
    label.append(upload);
    const error = roleElement("p", "role-error"); error.setAttribute("role", "alert");
    let sequence = 0;
    const refresh = () => preview.replaceChildren(roleIconElement(draft));
    const reset = roleButton(t("roles.imageReset"), () => {
        sequence++; draft.icon_image = ""; draft.icon = "";
        error.textContent = ""; changed(true);
    });
    upload.onchange = async () => {
        const file = upload.files[0], request = ++sequence;
        if (!file || model.busy || form.disabled) return;
        const current = () => sequence === request && model.draft === draft && form.isConnected && !model.busy && !form.disabled;
        upload.disabled = true; error.textContent = "";
        try {
            const image = await prepareRoleIcon(file);
            if (!current()) return;
            const otherBytes = model.snapshot.policy.roles.reduce((sum, role) => sum + (role.id === draft.id ? 0 : (role.icon_image || "").length), 0);
            if (otherBytes + image.length > ROLE_ICON_TOTAL_MAX) { error.textContent = t("roles.imageTotalError"); return; }
            draft.icon_image = image; refresh(); changed();
        } catch { if (current()) error.textContent = t("roles.imageInvalid"); }
        finally { if (current()) { upload.disabled = false; upload.value = ""; } }
    };
    refresh();
    controls.append(label, roleElement("p", "role-hint", t("roles.imageLimits")), reset, error);
    box.append(preview, controls);
    return box;
}
