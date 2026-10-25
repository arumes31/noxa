import { t } from "./i18n.js";

export function formatTransferETA(seconds) {
    if (!Number.isFinite(seconds) || seconds < 0) return "";
    if (seconds < 60) return t("eta.soon");
    if (seconds < 3600) {
        const count = Math.ceil(seconds / 60);
        return t(count === 1 ? "eta.minute" : "eta.minutes", { count });
    }
    const count = Math.ceil(seconds / 3600);
    return t(count === 1 ? "eta.hour" : "eta.hours", { count });
}

// Shows the full text as a tooltip only once the element ellipsizes it; the
// measurement runs on hover because rows are built before they are attached.
// `extra` is tooltip text that is shown regardless of truncation.
export function titleWhenTruncated(element, text, extra = "") {
    if (!element) return;
    const update = () => {
        const clipped = element.scrollWidth > element.clientWidth + 1;
        const title = [clipped ? text : "", extra].filter(Boolean).join("\n");
        if (title) element.title = title;
        else element.removeAttribute("title");
    };
    element.dataset.fullText = text;
    element.addEventListener("mouseenter", update);
    update();
    return update;
}

// "Parent › Child" for a channel using the snapshot's ParentID links.
export function channelPath(channels, channelID) {
    const byID = new Map((channels || []).map(channel => [channel.ChannelID, channel]));
    const names = [];
    let current = byID.get(channelID), guard = 0;
    while (current && guard++ < 1000) {
        names.unshift(current.Name);
        current = current.ParentID ? byID.get(current.ParentID) : null;
    }
    return names.join(" › ");
}
