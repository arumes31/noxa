// emoji-picker.js — a bottom sheet of emoji: the quick row, the built-in
// groups, the server's custom emoji and a name search. Resolves with the
// picked emoji (a character, or ":name:" for a custom one) or null.
// The composer inserts the result; reactions can reuse the same sheet.
import { EMOJI_GROUPS, QUICK_EMOJI } from "../core/markdown.js";
import { searchEmoji } from "../core/message-format.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";
import "./rich-strings.js";
import "./emoji-picker.css";

function customButton(session, name, pick) {
    const img = h("img", { alt: "", width: "28", height: "28", loading: "lazy" });
    session.emojiImage(name).then(url => { if (url.startsWith("data:image/")) img.src = url; }).catch(() => {});
    return h("button", { class: "emoji-cell", type: "button", title: `:${name}:`, "aria-label": `:${name}:`, onclick: () => pick(`:${name}:`) }, img);
}

/** Opens the picker. options: {session, title, quick (default true)}. */
export function openEmojiPicker({ session = null, title = t("rich.emoji.title"), quick = true } = {}) {
    return new Promise(resolve => {
        const opener = document.activeElement;
        const close = value => {
            overlay.remove();
            document.removeEventListener("keydown", onKey);
            opener?.focus?.();
            resolve(value);
        };
        const pick = value => close(value);
        const onKey = event => { if (event.key === "Escape") close(null); };
        const cell = char => h("button", { class: "emoji-cell", type: "button", "aria-label": char, onclick: () => pick(char) }, char);
        let custom = [];
        const search = h("input", { class: "emoji-search", type: "search", placeholder: t("rich.emoji.search"), "aria-label": t("rich.emoji.search"), enterkeyhint: "search" });
        const results = h("div", { class: "emoji-results" });
        const serverGroup = h("section", { class: "emoji-group", hidden: true, "aria-label": t("rich.emoji.server") });
        const groups = h("div", { class: "emoji-groups" },
            quick ? h("section", { class: "emoji-group", "aria-label": t("rich.emoji.quick") },
                h("h3", { class: "emoji-group-title" }, t("rich.emoji.quick")),
                h("div", { class: "emoji-grid" }, QUICK_EMOJI.map(cell))) : null,
            serverGroup,
            EMOJI_GROUPS.map(group => h("section", { class: "emoji-group", "aria-label": t(`rich.emoji.group.${group.key}`) },
                h("h3", { class: "emoji-group-title" }, t(`rich.emoji.group.${group.key}`)),
                h("div", { class: "emoji-grid" }, group.chars.map(cell)))));
        const renderSearch = () => {
            const query = search.value.trim();
            groups.hidden = !!query;
            results.hidden = !query;
            if (!query) return;
            const found = searchEmoji(query, custom);
            const cells = [
                ...found.custom.map(name => customButton(session, name, pick)),
                ...found.builtin.map(({ name, char }) => h("button", { class: "emoji-cell", type: "button", title: `:${name}:`, "aria-label": `:${name}:`, onclick: () => pick(char) }, char)),
            ];
            results.replaceChildren(cells.length ? h("div", { class: "emoji-grid" }, cells) : h("p", { class: "hint" }, t("rich.emoji.none")));
        };
        search.addEventListener("input", renderSearch);
        results.hidden = true;
        const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(null); } },
            h("div", { class: "sheet emoji-sheet", role: "dialog", "aria-modal": "true", "aria-label": title },
                h("div", { class: "emoji-head" },
                    h("h2", { class: "sheet-title" }, title),
                    h("button", { class: "btn", type: "button", onclick: () => close(null) }, t("common.close"))),
                search, results, groups));
        document.addEventListener("keydown", onKey);
        document.body.append(overlay);
        if (session) {
            session.emojiList().then(list => {
                custom = list.map(e => e.name).sort();
                if (!custom.length) return;
                serverGroup.replaceChildren(h("h3", { class: "emoji-group-title" }, t("rich.emoji.server")),
                    h("div", { class: "emoji-grid" }, custom.map(name => customButton(session, name, pick))));
                serverGroup.hidden = false;
                renderSearch();
            }).catch(() => {});
        }
        // Focusing the search would pop the keyboard over the grid on phones.
        overlay.querySelector(".emoji-cell, .btn")?.focus({ preventScroll: true });
    });
}
