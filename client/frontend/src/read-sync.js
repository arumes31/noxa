// read-sync.js — applies read positions the account's OTHER devices report
// (458). The server relays a phone's or another desktop's read as a
// "device_event" {event: "read", read: {scope, id, message_id}} to every
// other session of the account. Positions merge forward only (read-markers.js).
import { remoteReadMarker } from "./read-markers.js";
import { applyRemoteRead } from "./chat-ui.js";
import { conversationChanged } from "./conversations.js";

export function initReadSync() {
    window.runtime.EventsOn("device_event", event => {
        const marker = remoteReadMarker(event);
        if (!marker) return;
        // Private groups keep their cursor on the server (GREATEST on write);
        // re-list to pick up the merged position.
        if (marker.scope === "conversation") { void conversationChanged({}); return; }
        applyRemoteRead(marker);
    });
}
