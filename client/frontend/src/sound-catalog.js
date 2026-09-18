// Authored offline by tools/master-sounds.py; finished static noXa assets.
export const SOUND_DEFINITIONS = {
    "connection_connected": {
        "label": "Connected",
        "category": "Connection",
        "character": "Firm control latch with a short paper settling texture",
        "duration": 0.26,
        "priority": 2,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "connection_reconnected": {
        "label": "Reconnected",
        "category": "Connection",
        "character": "Two dry contacts ending in a clear latch",
        "duration": 0.245,
        "priority": 2,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "connection_disconnected": {
        "label": "Disconnected",
        "category": "Connection",
        "character": "Single calm dry control closure",
        "duration": 0.175,
        "priority": 2,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "connection_lost": {
        "label": "Connection lost",
        "category": "Connection",
        "character": "Brief paper interruption ending in a firm latch",
        "duration": 0.255,
        "priority": 3,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "connection_reconnecting": {
        "label": "Reconnecting",
        "category": "Connection",
        "character": "Small mechanical detent",
        "duration": 0.105,
        "priority": 1,
        "gain": 1,
        "cooldown": 5000,
        "concurrency": "replace same event within session; priority admission"
    },
    "connection_failed": {
        "label": "Connection failed",
        "category": "Connection",
        "character": "Two separated dry stops with a short paper edge",
        "duration": 0.28,
        "priority": 3,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "server_error": {
        "label": "Server action error",
        "category": "Connection",
        "character": "Compact crumpled-paper stop and button contact",
        "duration": 0.19,
        "priority": 3,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "own_channel_join": {
        "label": "Joined channel",
        "category": "Your channel",
        "character": "Signature control latch with a broad paper contact",
        "duration": 0.275,
        "priority": 2,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "own_channel_switch": {
        "label": "Switched channel",
        "category": "Your channel",
        "character": "Crisp latch followed by a dry paper landing",
        "duration": 0.205,
        "priority": 2,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "own_channel_leave": {
        "label": "Left channel",
        "category": "Your channel",
        "character": "Calm control release with a brief paper closure",
        "duration": 0.165,
        "priority": 2,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "user_join": {
        "label": "User joined",
        "category": "Other users",
        "character": "Small clear dry tap",
        "duration": 0.1,
        "priority": 0,
        "gain": 1,
        "cooldown": 180,
        "concurrency": "coalesce movement"
    },
    "user_leave": {
        "label": "User left",
        "category": "Other users",
        "character": "Quiet paper contact with a short soft tail",
        "duration": 0.095,
        "priority": 0,
        "gain": 1,
        "cooldown": 180,
        "concurrency": "coalesce movement"
    },
    "user_move_in": {
        "label": "User moved in",
        "category": "Other users",
        "character": "Light grainy control contact",
        "duration": 0.12,
        "priority": 0,
        "gain": 1,
        "cooldown": 180,
        "concurrency": "coalesce movement"
    },
    "user_move_out": {
        "label": "User moved out",
        "category": "Other users",
        "character": "Quiet compact button release",
        "duration": 0.1,
        "priority": 0,
        "gain": 1,
        "cooldown": 180,
        "concurrency": "coalesce movement"
    },
    "mic_on": {
        "label": "Microphone on",
        "category": "Voice controls",
        "character": "Mechanical switch engagement",
        "duration": 0.13,
        "priority": 1,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "mic_off": {
        "label": "Microphone off",
        "category": "Voice controls",
        "character": "Darker mechanical release",
        "duration": 0.14,
        "priority": 1,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "deafen_on": {
        "label": "Deafened",
        "category": "Voice controls",
        "character": "Muted double-action control closure",
        "duration": 0.14,
        "priority": 1,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "deafen_off": {
        "label": "Undeafened",
        "category": "Voice controls",
        "character": "Clearer control-surface release",
        "duration": 0.125,
        "priority": 1,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "ptt_on": {
        "label": "Push-to-talk on",
        "category": "Voice controls",
        "character": "Tiny dry button engagement",
        "duration": 0.035,
        "priority": 1,
        "gain": 1,
        "cooldown": 0,
        "concurrency": "replace PTT"
    },
    "ptt_off": {
        "label": "Push-to-talk off",
        "category": "Voice controls",
        "character": "Tiny dry contact release",
        "duration": 0.025,
        "priority": 1,
        "gain": 1,
        "cooldown": 0,
        "concurrency": "replace PTT"
    },
    "mention": {
        "label": "Mention",
        "category": "Notifications",
        "character": "Distinct paper flick followed by a clear button contact",
        "duration": 0.245,
        "priority": 2,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "keyword": {
        "label": "Keyword highlight",
        "category": "Notifications",
        "character": "Short grainy paper contact",
        "duration": 0.18,
        "priority": 1,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "dm": {
        "label": "Direct message",
        "category": "Notifications",
        "character": "Broad paper landing with a small dry leading click",
        "duration": 0.2,
        "priority": 1,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "channel_message": {
        "label": "Channel message",
        "category": "Notifications",
        "character": "Tiny clean dry button contact",
        "duration": 0.08,
        "priority": 0,
        "gain": 1,
        "cooldown": 250,
        "concurrency": "replace same event within session; priority admission"
    },
    "whisper": {
        "label": "Voice whisper",
        "category": "Notifications",
        "character": "Soft paper texture ending in a light click",
        "duration": 0.155,
        "priority": 2,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "poke": {
        "label": "Poke",
        "category": "Notifications",
        "character": "Focused dry control snap",
        "duration": 0.165,
        "priority": 2,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "join_leave": {
        "label": "Join/leave (your channel)",
        "category": "Notifications",
        "character": "Quiet short contact for the compatibility event",
        "duration": 0.09,
        "priority": 0,
        "gain": 1,
        "cooldown": 250,
        "concurrency": "replace same event within session; priority admission"
    },
    "buddy_online": {
        "label": "Watched contact online",
        "category": "Notifications",
        "character": "Compact control contact with a soft paper edge",
        "duration": 0.2,
        "priority": 1,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "kick": {
        "label": "Kicked",
        "category": "Notifications",
        "character": "Firm compact mechanical stop",
        "duration": 0.21,
        "priority": 3,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "ban": {
        "label": "Banned",
        "category": "Notifications",
        "character": "Two separated dry stops with a brief crumpled-paper finish",
        "duration": 0.29,
        "priority": 4,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "announcement": {
        "label": "Announcement",
        "category": "Notifications",
        "character": "Full paper contact ending in a crisp button closure",
        "duration": 0.25,
        "priority": 3,
        "gain": 1,
        "cooldown": 100,
        "concurrency": "replace same event within session; priority admission"
    },
    "channel_watch": {
        "label": "Channel watch",
        "category": "Notifications",
        "character": "Quiet short textured button pair",
        "duration": 0.13,
        "priority": 0,
        "gain": 1,
        "cooldown": 250,
        "concurrency": "replace same event within session; priority admission"
    }
};
export const SOUND_URLS = {
    connection_connected: new URL("./assets/sounds/connection_connected.wav", import.meta.url).href,
    connection_reconnected: new URL("./assets/sounds/connection_reconnected.wav", import.meta.url).href,
    connection_disconnected: new URL("./assets/sounds/connection_disconnected.wav", import.meta.url).href,
    connection_lost: new URL("./assets/sounds/connection_lost.wav", import.meta.url).href,
    connection_reconnecting: new URL("./assets/sounds/connection_reconnecting.wav", import.meta.url).href,
    connection_failed: new URL("./assets/sounds/connection_failed.wav", import.meta.url).href,
    server_error: new URL("./assets/sounds/server_error.wav", import.meta.url).href,
    own_channel_join: new URL("./assets/sounds/own_channel_join.wav", import.meta.url).href,
    own_channel_switch: new URL("./assets/sounds/own_channel_switch.wav", import.meta.url).href,
    own_channel_leave: new URL("./assets/sounds/own_channel_leave.wav", import.meta.url).href,
    user_join: new URL("./assets/sounds/user_join.wav", import.meta.url).href,
    user_leave: new URL("./assets/sounds/user_leave.wav", import.meta.url).href,
    user_move_in: new URL("./assets/sounds/user_move_in.wav", import.meta.url).href,
    user_move_out: new URL("./assets/sounds/user_move_out.wav", import.meta.url).href,
    mic_on: new URL("./assets/sounds/mic_on.wav", import.meta.url).href,
    mic_off: new URL("./assets/sounds/mic_off.wav", import.meta.url).href,
    deafen_on: new URL("./assets/sounds/deafen_on.wav", import.meta.url).href,
    deafen_off: new URL("./assets/sounds/deafen_off.wav", import.meta.url).href,
    ptt_on: new URL("./assets/sounds/ptt_on.wav", import.meta.url).href,
    ptt_off: new URL("./assets/sounds/ptt_off.wav", import.meta.url).href,
    mention: new URL("./assets/sounds/mention.wav", import.meta.url).href,
    keyword: new URL("./assets/sounds/keyword.wav", import.meta.url).href,
    dm: new URL("./assets/sounds/dm.wav", import.meta.url).href,
    channel_message: new URL("./assets/sounds/channel_message.wav", import.meta.url).href,
    whisper: new URL("./assets/sounds/whisper.wav", import.meta.url).href,
    poke: new URL("./assets/sounds/poke.wav", import.meta.url).href,
    join_leave: new URL("./assets/sounds/join_leave.wav", import.meta.url).href,
    buddy_online: new URL("./assets/sounds/buddy_online.wav", import.meta.url).href,
    kick: new URL("./assets/sounds/kick.wav", import.meta.url).href,
    ban: new URL("./assets/sounds/ban.wav", import.meta.url).href,
    announcement: new URL("./assets/sounds/announcement.wav", import.meta.url).href,
    channel_watch: new URL("./assets/sounds/channel_watch.wav", import.meta.url).href,
};
export const SOUND_EVENTS = Object.keys(SOUND_DEFINITIONS);
export const SOUND_EVENT_GROUPS = [
    {
        "label": "Connection",
        "events": [
            [
                "connection_connected",
                "Connected"
            ],
            [
                "connection_reconnected",
                "Reconnected"
            ],
            [
                "connection_disconnected",
                "Disconnected"
            ],
            [
                "connection_lost",
                "Connection lost"
            ],
            [
                "connection_reconnecting",
                "Reconnecting"
            ],
            [
                "connection_failed",
                "Connection failed"
            ],
            [
                "server_error",
                "Server action error"
            ]
        ]
    },
    {
        "label": "Your channel",
        "events": [
            [
                "own_channel_join",
                "Joined channel"
            ],
            [
                "own_channel_switch",
                "Switched channel"
            ],
            [
                "own_channel_leave",
                "Left channel"
            ]
        ]
    },
    {
        "label": "Other users",
        "events": [
            [
                "user_join",
                "User joined"
            ],
            [
                "user_leave",
                "User left"
            ],
            [
                "user_move_in",
                "User moved in"
            ],
            [
                "user_move_out",
                "User moved out"
            ]
        ]
    },
    {
        "label": "Voice controls",
        "events": [
            [
                "mic_on",
                "Microphone on"
            ],
            [
                "mic_off",
                "Microphone off"
            ],
            [
                "deafen_on",
                "Deafened"
            ],
            [
                "deafen_off",
                "Undeafened"
            ],
            [
                "ptt_on",
                "Push-to-talk on"
            ],
            [
                "ptt_off",
                "Push-to-talk off"
            ]
        ]
    },
    {
        "label": "Notifications",
        "events": [
            [
                "mention",
                "Mention"
            ],
            [
                "keyword",
                "Keyword highlight"
            ],
            [
                "dm",
                "Direct message"
            ],
            [
                "channel_message",
                "Channel message"
            ],
            [
                "whisper",
                "Voice whisper"
            ],
            [
                "poke",
                "Poke"
            ],
            [
                "join_leave",
                "Join/leave (your channel)"
            ],
            [
                "buddy_online",
                "Watched contact online"
            ],
            [
                "kick",
                "Kicked"
            ],
            [
                "ban",
                "Banned"
            ],
            [
                "announcement",
                "Announcement"
            ],
            [
                "channel_watch",
                "Channel watch"
            ]
        ]
    }
];
