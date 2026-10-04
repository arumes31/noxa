import { SOUND_EVENTS, SOUND_EVENT_GROUPS } from "./sound-catalog.js";

// Legacy action IDs still occur in notification previews and older callers.
// Route them to speech and exclude their retired effects from the audio bus.
// Generic errors, first-connect failures and intentional disconnects retain
// their own effects: they have no corresponding spoken announcement.
export const SPOKEN_ACTIONS = Object.freeze({
    mic_off: "microphone_muted",
    mic_on: "microphone_unmuted",
    deafen_on: "sound_muted",
    deafen_off: "sound_unmuted",
    connection_lost: "connection_lost",
    connection_reconnected: "connection_reconnected",
    own_channel_join: "channel_join",
    own_channel_switch: "channel_join",
    own_channel_leave: "channel_leave",
    user_join: "user_join",
    user_leave: "user_leave",
    user_move_in: "user_join",
    user_move_out: "user_moved",
    join_leave: "user_join",
    kick: "kicked",
    ban: "banned",
});

export const EFFECT_EVENTS = SOUND_EVENTS.filter(event => !Object.hasOwn(SPOKEN_ACTIONS, event));
export const EFFECT_GROUPS = SOUND_EVENT_GROUPS.map(group => ({
    ...group, key: group.events[0][0], events: group.events.filter(([event]) => !Object.hasOwn(SPOKEN_ACTIONS, event)),
})).filter(group => group.events.length);
