// Only search members already visible to this session; never enumerate accounts.
import { app } from "../app.js";
import { t } from "../i18n.js";
import { h, avatar } from "./dom.js";
import { dmHref, header, nav } from "./common.js";

export function visibleMembers(session) {
    const people = new Map();
    for (const member of session.clients()) {
        if (!member.unique_id || member.unique_id === session.uniqueID || member.is_guest || member.unique_id.startsWith("guest:")) continue;
        const old = people.get(member.unique_id);
        if (!old || (old.on_mobile && !member.on_mobile)) people.set(member.unique_id, member);
    }
    return [...people.values()].filter(member => member.nickname).sort((a,b) => a.nickname.localeCompare(b.nickname));
}

export function renderNewChat(root) {
    const session = app.session;
    const results = h("ul", { class: "list" });
    const empty = h("p", { class: "hint", role: "status" });
    const search = h("input", { type: "search", class: "input", placeholder: t("chatNew.search"), "aria-label": t("chatNew.search"), autocomplete: "off" });
    const refresh = () => {
        if (app.session !== session) return;
        const query = search.value.trim().toLocaleLowerCase();
        const matches = visibleMembers(session).filter(member => member.nickname.toLocaleLowerCase().includes(query));
        results.replaceChildren(...matches.map(member => h("li", {}, h("a", { class: "row", href: dmHref(member.unique_id) },
            avatar(member.nickname, "md"), h("span", { class: "row-title" }, member.nickname)))));
        empty.textContent = t(query ? "chatNew.noMatch" : "chatNew.empty");
        empty.hidden = matches.length > 0;
    };
    search.addEventListener("input", refresh);
    session.addEventListener("snapshot", refresh);
    root.replaceChildren(h("section", { class: "screen" }, header(t("chatNew.title")),
        h("div", { class: "screen-body" }, h("p", { class: "hint" }, t("chatNew.hint")), search, results, empty)), nav("home"));
    refresh();
    return () => session.removeEventListener("snapshot", refresh);
}
