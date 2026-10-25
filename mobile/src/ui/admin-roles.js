// admin-roles.js — roles (list, editor, order, default role, ownership) and
// role members (search, assign, batch add/remove). Every change carries the
// policy revision it was based on; a conflict reloads instead of guessing.
import { app } from "../app.js";
import {
    capabilityLabel, groupCapabilities, memberCounts, reorderedRoles, ROLE_TEMPLATES, roleDirty, roleDraft, roleTemplate, withRole,
} from "../core/access-matrix.js";
import { addStrings, lang, t } from "../i18n.js";
import { h, toast, replaceContent } from "./dom.js";
import { icon } from "./icons.js";
import {
    adminScreen, busy, chooseSheet, confirmSheet, fail, field, formSheet, input, loadingNote, select, toggle,
} from "./admin-kit.js";

addStrings({
    en: {
        "roles.title": "Roles",
        "roles.new": "New role",
        "roles.template": "Start from",
        "roles.template.blank": "Blank",
        "roles.template.member": "Member",
        "roles.template.moderator": "Moderator",
        "roles.template.administrator": "Administrator",
        "roles.everyone": "Everyone, including guests",
        "roles.memberCount": "{count} members",
        "roles.position": "Position {position}",
        "roles.protected": "Protected",
        "roles.moveUp": "Move {name} up",
        "roles.moveDown": "Move {name} down",
        "roles.defaultRole": "Default member role",
        "roles.defaultRoleHint": "Every registered member gets this role in addition to @everyone.",
        "roles.defaultNone": "None",
        "roles.transfer": "Transfer ownership…",
        "roles.transferSearch": "Who becomes the owner?",
        "roles.transferPick": "New owner",
        "roles.transferConfirm": "Transfer ownership to {name}",
        "roles.transferBody": "You will no longer be the owner. Type the server name “{server}” to confirm.",
        "roles.transferName": "Server name",
        "roles.transferred": "{name} is now the owner.",
        "roles.name": "Name",
        "roles.colour": "Colour",
        "roles.noColour": "No colour",
        "roles.icon": "Emoji icon",
        "roles.iconHint": "Shown next to the name, up to a few characters.",
        "roles.hoist": "Show separately in the member list",
        "roles.mentionable": "Anyone can @mention this role",
        "roles.permissions": "Permissions",
        "roles.searchPermissions": "Search permissions",
        "roles.noPermissions": "No permission matches.",
        "roles.group.server": "Server",
        "roles.group.access": "Channel access",
        "roles.group.text": "Text",
        "roles.group.voice": "Voice and video",
        "roles.group.files": "Files",
        "roles.group.moderation": "Moderation",
        "roles.group.profile": "Profile",
        "roles.create": "Create role",
        "roles.created": "Role created",
        "roles.deleteRole": "Delete role",
        "roles.deleteAsk": "Delete the role “{name}”? Members lose its permissions.",
        "roles.deleted": "Role deleted",
        "roles.readOnly": "You cannot change this role.",
        "roles.conflict": "Permissions changed meanwhile; the screen was refreshed.",
        "roles.pending": "Saved; the server is still applying it.",
        "roles.members": "Members and roles",
        "roles.search": "Search members",
        "roles.searchHint": "Name or unique ID",
        "roles.more": "Load more",
        "roles.noMembers": "No members found.",
        "roles.select": "Select {name}",
        "roles.selected": "{count} selected",
        "roles.addRole": "Add role…",
        "roles.removeRole": "Remove role…",
        "roles.batchDone": "Roles changed for {count} members.",
        "roles.memberRoles": "Roles of {name}",
        "roles.notManageable": "You cannot change this member's roles.",
        "roles.noRoles": "No roles",
    },
    de: {
        "roles.title": "Rollen",
        "roles.new": "Neue Rolle",
        "roles.template": "Ausgehend von",
        "roles.template.blank": "Leer",
        "roles.template.member": "Mitglied",
        "roles.template.moderator": "Moderation",
        "roles.template.administrator": "Administration",
        "roles.everyone": "Alle, auch Gäste",
        "roles.memberCount": "{count} Mitglieder",
        "roles.position": "Position {position}",
        "roles.protected": "Geschützt",
        "roles.moveUp": "{name} nach oben",
        "roles.moveDown": "{name} nach unten",
        "roles.defaultRole": "Standardrolle für Mitglieder",
        "roles.defaultRoleHint": "Jedes registrierte Mitglied erhält diese Rolle zusätzlich zu @everyone.",
        "roles.defaultNone": "Keine",
        "roles.transfer": "Eigentum übertragen…",
        "roles.transferSearch": "Wer wird Eigentümer?",
        "roles.transferPick": "Neuer Eigentümer",
        "roles.transferConfirm": "Eigentum an {name} übertragen",
        "roles.transferBody": "Du bist danach nicht mehr Eigentümer. Gib zur Bestätigung den Servernamen „{server}“ ein.",
        "roles.transferName": "Servername",
        "roles.transferred": "{name} ist jetzt Eigentümer.",
        "roles.name": "Name",
        "roles.colour": "Farbe",
        "roles.noColour": "Keine Farbe",
        "roles.icon": "Emoji-Symbol",
        "roles.iconHint": "Wird neben dem Namen gezeigt, höchstens ein paar Zeichen.",
        "roles.hoist": "In der Mitgliederliste getrennt anzeigen",
        "roles.mentionable": "Alle dürfen diese Rolle @erwähnen",
        "roles.permissions": "Berechtigungen",
        "roles.searchPermissions": "Berechtigungen suchen",
        "roles.noPermissions": "Keine Berechtigung passt.",
        "roles.group.server": "Server",
        "roles.group.access": "Kanalzugriff",
        "roles.group.text": "Text",
        "roles.group.voice": "Sprache und Video",
        "roles.group.files": "Dateien",
        "roles.group.moderation": "Moderation",
        "roles.group.profile": "Profil",
        "roles.create": "Rolle erstellen",
        "roles.created": "Rolle erstellt",
        "roles.deleteRole": "Rolle löschen",
        "roles.deleteAsk": "Die Rolle „{name}“ löschen? Mitglieder verlieren ihre Berechtigungen.",
        "roles.deleted": "Rolle gelöscht",
        "roles.readOnly": "Du kannst diese Rolle nicht ändern.",
        "roles.conflict": "Die Berechtigungen haben sich zwischenzeitlich geändert; die Ansicht wurde aktualisiert.",
        "roles.pending": "Gespeichert; der Server wendet es noch an.",
        "roles.members": "Mitglieder und Rollen",
        "roles.search": "Mitglieder suchen",
        "roles.searchHint": "Name oder eindeutige ID",
        "roles.more": "Mehr laden",
        "roles.noMembers": "Keine Mitglieder gefunden.",
        "roles.select": "{name} auswählen",
        "roles.selected": "{count} ausgewählt",
        "roles.addRole": "Rolle hinzufügen…",
        "roles.removeRole": "Rolle entfernen…",
        "roles.batchDone": "Rollen für {count} Mitglieder geändert.",
        "roles.memberRoles": "Rollen von {name}",
        "roles.notManageable": "Du kannst die Rollen dieses Mitglieds nicht ändern.",
        "roles.noRoles": "Keine Rollen",
    },
});

const isConflict = error => error?.code === 7 || /changed|refresh|conflict/i.test(error?.message || "");

/** Sends a role change; reports pending enforcement; a conflict says so. */
async function commit(admin, change) {
    try {
        const result = await admin.roleChange(change);
        if (result.enforcement_pending) toast(t("roles.pending"), "warn");
        return result;
    } catch (error) {
        if (isConflict(error)) { toast(t("roles.conflict"), "warn"); return null; }
        throw error;
    }
}

function swatch(role) {
    return h("span", { class: "role-swatch", style: role.color ? `background:${role.color}` : null, "aria-hidden": "true" }, role.icon || "");
}

/** Searches role members and lets the user pick one. Resolves the member or null. */
async function pickMember(admin, state, title) {
    const query = input({ type: "search", maxlength: "128", autocomplete: "off" });
    const search = await formSheet(title, field(t("roles.search"), query, t("roles.searchHint")), [
        { label: t("roles.search"), value: "go", primary: true }, { label: t("admin.cancel"), value: null },
    ]);
    if (search !== "go") return null;
    const page = await admin.roleMembers({ revision: state.policy.revision, search: query.value.trim() });
    const entries = (page.entries || []).filter(m => m.user_id > 0);
    if (!entries.length) { toast(t("roles.noMembers"), "warn"); return null; }
    const id = await chooseSheet(title, entries.map(m => ({ value: m.user_id, label: m.nickname || m.unique_id, sub: m.unique_id })));
    return entries.find(m => m.user_id === id) || null;
}

export function renderRoles({ root, admin }) {
    const body = adminScreen(root, t("roles.title"), {}, loadingNote());
    let state;
    const load = async () => {
        try {
            state = await admin.roleState(0);
        } catch (error) {
            replaceContent(body, h("p", { class: "error" }, error.message), h("p", { class: "hint" }, t("admin.unavailable")));
            return;
        }
        render();
    };
    const render = () => {
        const policy = state.policy;
        const owner = state.actor_id === policy.owner_id;
        const counts = memberCounts(policy);
        const manageable = new Set(state.manageable_role_ids || []);
        const ordered = [...policy.roles].sort((a, b) => b.position - a.position);
        const move = (role, direction) => {
            const order = reorderedRoles(policy.roles, role.id, direction);
            if (!order) return null;
            return h("button", { class: "icon-btn", type: "button", disabled: !owner && !manageable.has(role.id), "aria-label": t(direction > 0 ? "roles.moveUp" : "roles.moveDown", { name: role.name }), onclick: event => busy(event.currentTarget, async () => {
                await commit(admin, { kind: "roles_reorder", expected_revision: policy.revision, role_ids: order });
                await load();
            }) }, icon(direction > 0 ? "up" : "down"));
        };
        const defaultSelect = select([{ value: 0, label: t("roles.defaultNone") },
            ...policy.roles.filter(r => r.id !== policy.everyone_id && !(r.permissions || []).includes("administrator")).map(r => ({ value: r.id, label: r.name }))],
        policy.default_member_role_id || 0);
        defaultSelect.onchange = () => busy(defaultSelect, async () => {
            await commit(admin, { kind: "default_member_role_set", expected_revision: policy.revision, role_id: Number(defaultSelect.value) });
            await load();
        });
        replaceContent(body,
            h("div", { class: "admin-actions" },
                h("button", { class: "btn btn-primary", type: "button", onclick: async () => {
                    const template = await chooseSheet(t("roles.template"), ROLE_TEMPLATES.map(key => ({ value: key, label: t(`roles.template.${key}`),
                        disabled: key === "administrator" && !owner })));
                    if (template) location.hash = `#/admin/role/new/${template}`;
                } }, icon("plus"), t("roles.new")),
                h("a", { class: "btn", href: "#/admin/members" }, t("roles.members"))),
            h("ul", { class: "list admin-roles" }, ordered.map(role => {
                const everyone = role.id === policy.everyone_id;
                return h("li", { class: "admin-role-row" },
                    h("a", { class: "row admin-link", href: `#/admin/role/${role.id}` }, swatch(role),
                        h("span", { class: "row-main" }, h("span", { class: "row-title", style: role.color ? `color:${role.color}` : null }, role.name),
                            h("span", { class: "row-sub" }, [everyone ? t("roles.everyone") : t("roles.memberCount", { count: counts.get(role.id) || 0 }),
                                t("roles.position", { position: role.position }), role.deletion_protected ? t("roles.protected") : ""].filter(Boolean).join(" · ")))),
                    everyone ? null : h("span", { class: "admin-reorder" }, move(role, 1), move(role, -1)));
            })),
            owner ? field(t("roles.defaultRole"), defaultSelect, t("roles.defaultRoleHint")) : null,
            owner ? h("button", { class: "btn btn-danger btn-block admin-gap", type: "button", onclick: event => busy(event.currentTarget, () => transfer(admin, state, load)) }, t("roles.transfer")) : null);
    };
    load();
}

async function transfer(admin, state, reload) {
    const member = await pickMember(admin, state, t("roles.transferSearch"));
    if (!member) return;
    const server = app.info?.server_name || "noXa";
    const typed = input({ autocomplete: "off" });
    const name = member.nickname || member.unique_id;
    const ok = await formSheet(t("roles.transferConfirm", { name }), h("div", { class: "admin-stack" },
        h("p", { class: "sheet-body admin-wrap" }, t("roles.transferBody", { server })), field(t("roles.transferName"), typed)), [
        { label: t("roles.transferConfirm", { name }), value: true, danger: true }, { label: t("admin.cancel"), value: null },
    ], { validate: value => value !== true || typed.value.trim() === server || (typed.setAttribute("aria-invalid", "true"), typed.focus(), false) });
    if (!ok) return;
    if (await commit(admin, { kind: "owner_transfer", expected_revision: state.policy.revision, user_id: member.user_id })) toast(t("roles.transferred", { name }));
    await reload();
}

export function renderRoleEditor({ root, admin }, id, template) {
    const isNew = id === "new";
    const body = adminScreen(root, isNew ? t("roles.new") : t("roles.title"), { back: "#/admin/roles" }, loadingNote());
    let state, saved, draft, query = "";
    const permissions = h("div", { class: "admin-permissions" });
    const renderPermissions = () => {
        const grantable = new Set(state.grantable_capabilities || []);
        const editable = isNew || (state.manageable_role_ids || []).includes(draft.id);
        const groups = groupCapabilities(state.capabilities, query, lang());
        permissions.replaceChildren(...(groups.length ? groups.map(({ group, items }) => h("section", { class: "admin-group" },
            h("h3", { class: "section-label" }, t(`roles.group.${group}`) === `roles.group.${group}` ? group : t(`roles.group.${group}`)),
            items.map(info => {
                const has = draft.permissions.includes(info.key);
                return toggle(capabilityLabel(info, lang()), has, checked => {
                    draft.permissions = checked ? [...new Set([...draft.permissions, info.key])].sort() : draft.permissions.filter(k => k !== info.key);
                    update();
                }, { disabled: !editable || (!has && !grantable.has(info.key)) });
            }))) : [h("p", { class: "hint" }, t("roles.noPermissions"))]));
    };
    const saveButton = h("button", { class: "btn btn-primary btn-block", type: "submit" }, isNew ? t("roles.create") : t("admin.save"));
    const update = () => { saveButton.disabled = !draft.name.trim() || (!isNew && !roleDirty(saved, draft)); };
    const load = async (roleID = id) => {
        try {
            state = await admin.roleState(0);
        } catch (error) {
            replaceContent(body, h("p", { class: "error" }, error.message), h("p", { class: "hint" }, t("admin.unavailable")));
            return;
        }
        const role = state.policy.roles.find(r => String(r.id) === String(roleID));
        if (!isNew && !role) { location.hash = "#/admin/roles"; return; }
        saved = isNew ? roleDraft({ permissions: roleTemplate(template || "blank", state.grantable_capabilities || []) }) : roleDraft(role);
        draft = roleDraft(saved);
        render();
    };
    const render = () => {
        const policy = state.policy;
        const everyone = draft.id === policy.everyone_id;
        const editable = isNew || (state.manageable_role_ids || []).includes(draft.id);
        const name = input({ value: draft.name, maxlength: "100", required: true, disabled: !editable || everyone, autocomplete: "off" });
        name.value = draft.name;
        name.oninput = () => { draft.name = name.value; update(); };
        const colour = input({ type: "color", class: "input admin-colour", disabled: !editable });
        colour.value = draft.color || "#34d9ef";
        const noColour = toggle(t("roles.noColour"), !draft.color, checked => { draft.color = checked ? "" : colour.value; update(); }, { disabled: !editable });
        colour.oninput = () => { draft.color = colour.value; noColour.querySelector("input").checked = false; update(); };
        const emoji = input({ value: draft.icon, maxlength: "16", disabled: !editable, autocomplete: "off" });
        emoji.value = draft.icon;
        emoji.oninput = () => { draft.icon = [...emoji.value].slice(0, 16).join(""); update(); };
        const search = input({ type: "search", placeholder: t("roles.searchPermissions"), "aria-label": t("roles.searchPermissions") });
        search.value = query;
        search.oninput = () => { query = search.value; renderPermissions(); };
        const form = h("form", { class: "admin-stack", onsubmit: event => {
            event.preventDefault();
            busy(saveButton, async () => {
                const role = roleDraft(draft);
                role.name = role.name.trim();
                const result = await commit(admin, isNew ? { kind: "role_create", expected_revision: policy.revision, role: { ...role, id: 0 } }
                    : { kind: "role_update", expected_revision: policy.revision, role });
                if (!result) return load(draft.id || id);
                toast(t(isNew ? "roles.created" : "admin.saved"));
                if (isNew && result.created_role_id) location.hash = `#/admin/role/${result.created_role_id}`;
                else await load(draft.id);
            });
        } },
        editable ? null : h("p", { class: "hint" }, t("roles.readOnly")),
        field(t("roles.name"), name),
        h("div", { class: "admin-row-fields" }, field(t("roles.colour"), colour), field(t("roles.icon"), emoji, t("roles.iconHint"))),
        noColour,
        everyone ? null : toggle(t("roles.hoist"), draft.hoist, checked => { draft.hoist = checked; update(); }, { disabled: !editable }),
        everyone ? null : toggle(t("roles.mentionable"), draft.mentionable, checked => { draft.mentionable = checked; update(); }, { disabled: !editable }),
        h("p", { class: "section-label" }, t("roles.permissions")),
        search, permissions,
        h("div", { class: "admin-sticky" }, saveButton),
        !isNew && editable && !everyone && !draft.deletion_protected ? h("button", { class: "btn btn-danger btn-block", type: "button", onclick: event => busy(event.currentTarget, async () => {
            if (!await confirmSheet(t("roles.deleteRole"), t("roles.deleteAsk", { name: saved.name }), t("admin.delete"))) return;
            if (await commit(admin, { kind: "role_delete", expected_revision: policy.revision, role_id: draft.id })) {
                toast(t("roles.deleted"));
                location.hash = "#/admin/roles";
            } else await load(draft.id);
        }) }, t("roles.deleteRole")) : null);
        replaceContent(body, form);
        renderPermissions();
        update();
    };
    load();
}

export function renderMembers({ root, admin }) {
    const body = adminScreen(root, t("roles.members"), { back: "#/admin/roles" }, loadingNote());
    let state, entries = [], more = false, query = "";
    const selected = new Map();
    const list = h("ul", { class: "list" });
    const status = h("p", { class: "hint", role: "status" });
    const batch = h("div", { class: "admin-batch", hidden: true });
    const roleName = id => state.policy.roles.find(r => r.id === id)?.name || `#${id}`;
    const manageableRoles = () => state.policy.roles.filter(r => r.id !== state.policy.everyone_id && (state.manageable_role_ids || []).includes(r.id));
    const setRoles = async (member, roleIDs) => {
        const change = { kind: "member_roles_set", expected_revision: state.policy.revision, role_ids: roleIDs };
        if (member.user_id > 0) change.user_id = member.user_id; else change.member_unique_id = member.unique_id;
        const result = await commit(admin, change);
        if (!result) { await refreshState(); return false; }
        state.policy.revision = result.revision;
        return true;
    };
    const refreshState = async () => { state = await admin.roleState(0); };
    const search = async (append = false) => {
        const page = await admin.roleMembers({ revision: state.policy.revision, search: query, afterID: append ? entries.at(-1)?.user_id || 0 : 0 })
            .catch(async error => {
                if (!isConflict(error)) throw error;
                await refreshState();
                return admin.roleMembers({ revision: state.policy.revision, search: query });
            });
        entries = append ? [...entries, ...(page.entries || [])] : page.entries || [];
        more = !!page.more;
        renderList();
    };
    const renderBatch = () => {
        batch.hidden = !selected.size;
        batch.replaceChildren(h("span", { class: "row-meta" }, t("roles.selected", { count: selected.size })),
            ...[["roles.addRole", true], ["roles.removeRole", false]].map(([key, add]) => h("button", { class: "btn", type: "button", onclick: event => busy(event.currentTarget, async () => {
                const roleID = await chooseSheet(t(key), manageableRoles().map(r => ({ value: r.id, label: r.name, iconNode: swatch(r) })));
                if (roleID === null) return;
                let count = 0;
                for (const member of selected.values()) if (await setRoles(member, withRole(member.role_ids, roleID, add))) count++;
                toast(t("roles.batchDone", { count }));
                selected.clear();
                await search();
            }) }, t(key))));
    };
    const memberSheet = async member => {
        const name = member.nickname || member.unique_id;
        if (!member.manageable) { toast(t("roles.notManageable"), "warn"); return; }
        const chosen = new Set(member.role_ids || []);
        const content = h("div", { class: "admin-stack" }, manageableRoles().map(role => toggle(role.name, chosen.has(role.id), checked => { checked ? chosen.add(role.id) : chosen.delete(role.id); })));
        if (await formSheet(t("roles.memberRoles", { name }), content, [{ label: t("admin.save"), value: true, primary: true }, { label: t("admin.cancel"), value: null }])) {
            // Roles the actor cannot manage stay as they were.
            const kept = (member.role_ids || []).filter(id => !manageableRoles().some(r => r.id === id));
            if (await setRoles(member, [...new Set([...kept, ...chosen])].sort((a, b) => a - b))) toast(t("admin.saved"));
            await search();
        }
    };
    const renderList = () => {
        list.replaceChildren(...entries.map(member => {
            const name = member.nickname || member.unique_id;
            const key = member.user_id || member.unique_id;
            const box = h("input", { type: "checkbox", class: "admin-check", "aria-label": t("roles.select", { name }), checked: selected.has(key), disabled: !member.manageable });
            box.onchange = () => { box.checked ? selected.set(key, member) : selected.delete(key); renderBatch(); };
            return h("li", { class: "admin-member" }, box,
                h("button", { class: "row", type: "button", onclick: () => memberSheet(member) },
                    h("span", { class: "row-main" }, h("span", { class: "row-title" }, name),
                        h("span", { class: "row-sub" }, (member.role_ids || []).length ? member.role_ids.map(roleName).join(", ") : t("roles.noRoles")))));
        }));
        status.textContent = entries.length ? "" : t("roles.noMembers");
        loadMore.hidden = !more;
        renderBatch();
    };
    const loadMore = h("button", { class: "btn btn-block", type: "button", hidden: true, onclick: event => busy(event.currentTarget, () => search(true)) }, t("roles.more"));
    const box = input({ type: "search", maxlength: "128", autocomplete: "off", placeholder: t("roles.searchHint") });
    const form = h("form", { class: "admin-search", role: "search", onsubmit: event => {
        event.preventDefault();
        query = box.value.trim();
        selected.clear();
        busy(form.querySelector("button"), () => search());
    } }, field(t("roles.search"), box), h("button", { class: "btn", type: "submit" }, t("roles.search")));
    (async () => {
        try { await refreshState(); } catch (error) {
            replaceContent(body, h("p", { class: "error" }, error.message), h("p", { class: "hint" }, t("admin.unavailable")));
            return;
        }
        replaceContent(body, form, batch, status, list, loadMore);
        await search().catch(fail);
    })();
}

