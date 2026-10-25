// settings-rows.js — controls for settings rows bound to core/settings.js,
// for any module that registers rows with settings-registry.js.
import { get, set } from "../core/settings.js";
import { h } from "./dom.js";

let ids = 0;

/** A switch row for a boolean setting. */
export function toggleSetting(name, label, hint, onChange) {
    const input = h("input", { type: "checkbox", role: "switch", checked: !!get(name) });
    input.onchange = () => {
        set({ [name]: input.checked });
        onChange?.(input.checked);
    };
    return h("label", { class: "toggle" },
        h("span", { class: "row-main" }, h("span", { class: "row-title" }, label), hint ? h("span", { class: "row-sub settings-wrap" }, hint) : null),
        input);
}

/** A labelled <select>; options are [[value, label]]. */
export function choiceSetting(name, label, options, { hint, value, onChange } = {}) {
    const id = `setting-${++ids}`;
    const current = value !== undefined ? value : get(name);
    const select = h("select", { class: "input settings-select", id },
        options.map(([optionValue, optionLabel]) => h("option", { value: optionValue, selected: String(current) === String(optionValue) }, optionLabel)));
    select.onchange = () => {
        if (onChange) onChange(select.value);
        else set({ [name]: select.value });
    };
    return h("div", { class: "settings-field" },
        h("label", { for: id, class: "row-main" }, h("span", { class: "row-title" }, label), hint ? h("span", { class: "row-sub settings-wrap" }, hint) : null),
        select);
}

/** A row with a title, optional detail and trailing control. */
export function infoRow(label, detail, control = null) {
    return h("div", { class: "settings-field" },
        h("span", { class: "row-main" }, h("span", { class: "row-title" }, label), detail !== undefined && detail !== null ? h("span", { class: "row-sub settings-wrap" }, detail) : null),
        control);
}
