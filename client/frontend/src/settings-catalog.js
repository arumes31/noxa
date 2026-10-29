// Settings dialog catalogue: the categories in the left rail, which saved
// preference keys each page owns, and the deep-link vocabulary other UI (menus,
// the quick switcher) uses to open a specific page.
//
// Pure data and helpers only, so unit tests can check that every saved
// preference stays reachable from the dialog.

export const SETTINGS_CATEGORIES = Object.freeze([
    { id: "general", icon: "settings", rail: "main", keys: [
        "language", "auto_away_minutes", "auto_away_message", "always_on_top", "close_to_tray", "minimize_to_tray",
        "updates_auto_install", "updates_auto_check", "updates_check_on_reconnect", "download_folder",
    ] },
    { id: "audio", icon: "mic", rail: "main", keys: [
        "activation_mode", "capture_device_id", "vad_threshold", "echo_cancellation", "noise_suppression",
        "ptt_release_delay_ms", "playback_device_id", "volume", "voice_limiter", "gain_normalize", "positional_audio",
    ] },
    { id: "streams", icon: "streams", rail: "main", keys: [
        "camera_device_id", "camera_background", "camera_background_scene", "camera_background_image",
        "camera_blur_strength", "camera_mirror_preview", "camera_fps",
    ] },
    { id: "overlay", icon: "layers", rail: "main", keys: [
        "gaming_overlay", "gaming_overlay_monitor", "gaming_overlay_position", "gaming_overlay_x", "gaming_overlay_y",
        "gaming_overlay_style", "gaming_overlay_animate", "gaming_overlay_opacity", "gaming_overlay_scale",
        "gaming_overlay_self_color", "gaming_overlay_blue_start", "gaming_overlay_blue_end",
        "gaming_overlay_game_ui", "gaming_overlay_avoid_zones",
        "gaming_overlay_only_speaking", "gaming_overlay_chat_peek", "gaming_overlay_auto_hide",
    ] },
    { id: "hotkeys", icon: "keyboard", rail: "main", keys: [
        "hotkey_ptt", "hotkey_mute", "hotkey_deafen", "whisper_reply_hotkey",
        "hotkey_compact", "hotkey_zen", "hotkey_profiles",
    ] },
    { id: "appearance", icon: "palette", rail: "main", keys: [
        "theme", "accent_color", "ui_font", "ui_font_size", "compact_mode", "reduce_motion", "window_opacity", "user_css",
    ] },
    { id: "privacy", icon: "lock", rail: "main", keys: [
        "identity_key_protection", "allow_plaintext", "hide_chat_activity", "log_channel_chat", "log_private_chat", "log_server_chat",
    ] },
    { id: "chat", icon: "chat", rail: "more", keys: [
        "link_previews", "chat_timestamps", "chat_density", "chat_layout", "chat_font_size", "sys_join_leave", "sys_kick",
        "chat_max_lines", "keywords", "chat_undo_seconds",
    ] },
    { id: "notifications", icon: "bell", rail: "more", keys: [
        "play_sounds", "sound_volume", "effects_enabled", "duck_effects_while_speaking", "spoken_messages",
        "speech_volume", "speech_language", "speech_connection", "speech_admin", "speech_removal",
        "speech_permissions", "speech_channel", "speech_events", "event_sounds", "chat_notification_level",
        "notify_matrix", "dnd_enabled", "dnd_from", "dnd_to", "notify_join_leave", "notify_connection",
        "warn_muted_talking", "warn_empty_channel",
    ] },
    { id: "whisper", icon: "whisper", rail: "more", keys: ["whisper_active", "whisper_clients", "whisper_channels"] },
    // Runtime server configuration (admins) is edited live on the server, not
    // in the local settings file, so it owns no local keys besides reconnect.
    { id: "connection", icon: "signal", rail: "more", keys: ["reconnect_on_loss"] },
].map(category => Object.freeze({ ...category, keys: Object.freeze(category.keys) })));

export const SETTINGS_PAGE_IDS = Object.freeze(SETTINGS_CATEGORIES.map(category => category.id));

// Saved fields the dialog deliberately does not edit, with the place that
// owns them. Every Settings JSON field is either here or on exactly one page.
export const SETTINGS_OUTSIDE_DIALOG = Object.freeze({
    settings_base: "edit transaction baseline",
    settings_version: "migration generation",
    bookmarks: "bookmark manager",
    recents: "connect dialog history (Go-owned)",
    display_name: "profile / connect dialog",
    sidebar_width: "workspace splitter",
    details_width: "workspace splitter",
    notification_snooze_until: "notification snooze menu",
    user_volumes: "member volume menu",
    user_share_volumes: "stream volume menu",
    muted_share_users: "stream context menu",
    muted_users: "member context menu",
    whisper_sound: "legacy; superseded by event_sounds.whisper",
    sound_pack: "fixed to the noxa pack",
    low_bandwidth: "voice controls",
    e2ee_verified: "safety-number verification dialog",
    last_read_channels: "chat read markers",
    dismissed_announcement: "announcement banner",
    contacts: "contacts list",
    blocked_users: "member context menu",
    muted_conversations: "conversation menu",
    user_notes: "member notes",
    recent_channels: "channel switcher history",
    onboarding_done: "first-run tour",
    last_seen_version: "what's-new dialog (Go-owned)",
    active_identity: "identity manager on the Privacy page (Go-owned)",
    channel_notify: "channel notification menu",
    alpha_dismissed: "alpha notice",
    mobile_hint: "noXa Mobile sidebar hint (device dialog / Help menu)",
    secure_notice_seen: "secure messaging upgrade notice",
});

// Lists and maps the user curates by hand survive "Reset to default".
export const RESET_PRESERVES = Object.freeze(new Set(["hotkey_profiles", "keywords", "whisper_clients", "whisper_channels"]));

// Page ids used before the redesign. Menus and older callers still pass
// them; each maps to its new page and, where useful, a section on it.
export const LEGACY_SETTINGS_PAGES = Object.freeze({
    application: { page: "general" },
    capture: { page: "audio", section: "input" },
    playback: { page: "audio", section: "output" },
    camera: { page: "streams" },
    downloads: { page: "general", section: "downloads" },
    security: { page: "privacy" },
    server: { page: "connection", section: "server" },
});

// resolveSettingsTarget turns a deep link into {page, section, setting}.
// Accepts a page id (new or legacy), "page/section", or an options object.
export function resolveSettingsTarget(target = "general", options = {}) {
    let request = typeof target === "object" && target ? { ...target } : { page: target ? String(target) : "" };
    if (typeof request.page === "string" && request.page.includes("/")) {
        const [page, section] = request.page.split("/", 2);
        request = { ...request, page, section: request.section || section };
    }
    request = { ...request, ...options };
    let page = request.page || "general";
    let section = request.section || "";
    if (request.setting && !request.page) page = pageForSetting(request.setting) || page;
    const legacy = LEGACY_SETTINGS_PAGES[page];
    if (legacy) { page = legacy.page; section ||= legacy.section || ""; }
    if (!SETTINGS_PAGE_IDS.includes(page)) page = (request.setting && pageForSetting(request.setting)) || "general";
    return { page, section, setting: request.setting || "" };
}

export function pageForSetting(key) {
    return SETTINGS_CATEGORIES.find(category => category.keys.includes(key))?.id || "";
}

// resetPageDraft copies a page's defaults into the draft. Keys absent from
// the defaults (omitempty fields such as notify_matrix) are removed so the
// page falls back to its built-in seeding. Returns the keys that changed.
export function resetPageDraft(draft, defaults, pageId) {
    const category = SETTINGS_CATEGORIES.find(item => item.id === pageId);
    if (!category || !draft || !defaults || typeof defaults !== "object") return [];
    const changed = [];
    for (const key of category.keys) {
        if (RESET_PRESERVES.has(key)) continue;
        const before = JSON.stringify(draft[key]);
        if (Object.hasOwn(defaults, key)) draft[key] = structuredClone(defaults[key]);
        else delete draft[key];
        if (JSON.stringify(draft[key]) !== before) changed.push(key);
    }
    return changed;
}
