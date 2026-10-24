// files-link.js — the folder button that opens a channel's files, for the
// chat header and the Servers list (kept apart from files.js so those
// screens do not load the browser itself).
import { addStrings, t } from "../i18n.js";
import { h } from "./dom.js";
import { icon } from "./icons.js";

addStrings({
    en: { "files.openShort": "Files", "files.openNamed": "Files of {name}" },
    de: { "files.openShort": "Dateien", "files.openNamed": "Dateien von {name}" },
});

const FOLDER = "M3 6a1 1 0 0 1 1-1h5l2 2h9a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1z";

export function folderIcon() {
    const svg = icon("link");
    svg.querySelector("path").setAttribute("d", FOLDER);
    return svg;
}

/**
 * An icon link to #/files/<channelID>. Its name is just "Files" (it follows
 * the channel's own link, which carries the name); the channel is in the
 * title, so a search for the channel name finds only the chat link.
 */
export function filesButton(channelID, name) {
    return h("a", { class: "icon-btn", href: `#/files/${channelID}`, "aria-label": t("files.openShort"), title: t("files.openNamed", { name }), "data-files-link": String(channelID) }, folderIcon());
}
