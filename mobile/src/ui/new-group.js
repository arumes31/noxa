import { app } from "../app.js";
import { t } from "../i18n.js";
import { h, avatar } from "./dom.js";
import { header, nav } from "./common.js";
import { visibleMembers } from "./new-chat.js";

export function renderNewGroup(root) {
    const session = app.session, groups = app.groups;
    const selected = new Map();
    let created = null, busy = false;
    const name = h("input", { class: "input", required: true, maxlength: "80", "aria-label": t("group.name"), placeholder: t("group.name") });
    const search = h("input", { class: "input", type: "search", "aria-label": t("chatNew.search"), placeholder: t("chatNew.search") });
    const members = h("ul", { class: "list" });
    const error = h("p", { class: "error", role: "alert" });
    const count = h("p", { class: "hint", role: "status" });
    const submit = h("button", { class: "btn btn-primary", type: "submit" }, t("group.create"));
    const open = h("a", { class: "btn", hidden: true }, t("group.open"));
    const refresh = () => {
        if (app.session !== session || busy) return;
        const visible = visibleMembers(session);
        for (const uid of selected.keys()) if (!visible.some(member => member.unique_id === uid)) selected.delete(uid);
        const query = search.value.trim().toLocaleLowerCase();
        members.replaceChildren(...visible.filter(member => member.nickname.toLocaleLowerCase().includes(query)).map(member => {
            const checkbox = h("input", { type: "checkbox", checked: selected.has(member.unique_id), disabled: selected.size >= 15 && !selected.has(member.unique_id), onchange: event => {
                if (event.target.checked) selected.set(member.unique_id, member.nickname); else selected.delete(member.unique_id);
                refresh();
            } });
            return h("li", {}, h("label", { class: "row" }, checkbox, avatar(member.nickname,"sm"), h("span",{class:"row-title"},member.nickname)));
        }));
        count.textContent = t("group.selected", { count: selected.size });
    };
    search.addEventListener("input", refresh);
    session.addEventListener("snapshot", refresh);
    const form = h("form", { class: "screen-body", onsubmit: async event => {
        event.preventDefault();
        if (busy || app.session !== session || !name.value.trim()) return;
        refresh();
        busy = true; submit.disabled = true; search.disabled = true; name.disabled = true;
        members.querySelectorAll("input").forEach(input => { input.disabled = true; });
        error.textContent = "";
        try {
            if (!created) created = await groups.create(name.value);
            open.href = `#/group/${encodeURIComponent(created.id)}`; open.hidden = false;
            for (const uid of selected.keys()) {
                if (app.session !== session || !root.contains(form)) return;
                await groups.invite(created.id, uid);
                selected.delete(uid);
            }
            if (app.session === session && root.contains(form)) location.hash = open.getAttribute("href");
        } catch (failure) {
            error.textContent = t(created ? "group.partial" : "common.error", { error: failure.message });
            submit.textContent = t(created ? "group.retry" : "group.create");
        } finally {
            busy = false; submit.disabled = false; search.disabled = false; name.disabled = !!created; refresh();
        }
    } }, name, h("p", { class:"hint" }, t("group.inviteHint")), search, count, members, error, submit, open);
    root.replaceChildren(h("section", {class:"screen"}, header(t("group.create"), {back:"#/home"}), form), nav("home"));
    refresh();
    return () => session.removeEventListener("snapshot", refresh);
}
