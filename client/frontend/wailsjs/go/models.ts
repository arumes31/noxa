export namespace authorization {

	export class AccessImpactChange {
	    capability: string;
	    before: boolean;
	    after: boolean;

	    static createFrom(source: any = {}) {
	        return new AccessImpactChange(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.capability = source["capability"];
	        this.before = source["before"];
	        this.after = source["after"];
	    }
	}
	export class CapabilityInfo {
	    key: string;
	    group: string;
	    en: string;
	    de: string;
	    channel: boolean;
	    requires?: string[];

	    static createFrom(source: any = {}) {
	        return new CapabilityInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.group = source["group"];
	        this.en = source["en"];
	        this.de = source["de"];
	        this.channel = source["channel"];
	        this.requires = source["requires"];
	    }
	}
	export class MemberAccessImpact {
	    user_id: number;
	    changes: AccessImpactChange[];

	    static createFrom(source: any = {}) {
	        return new MemberAccessImpact(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.user_id = source["user_id"];
	        this.changes = this.convertValues(source["changes"], AccessImpactChange);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChannelAccessImpact {
	    revision: number;
	    channel_id: number;
	    members: MemberAccessImpact[];

	    static createFrom(source: any = {}) {
	        return new ChannelAccessImpact(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.revision = source["revision"];
	        this.channel_id = source["channel_id"];
	        this.members = this.convertValues(source["members"], MemberAccessImpact);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RoleOverride {
	    role_id?: number;
	    user_id?: number;
	    capability: string;
	    effect: string;

	    static createFrom(source: any = {}) {
	        return new RoleOverride(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.role_id = source["role_id"];
	        this.user_id = source["user_id"];
	        this.capability = source["capability"];
	        this.effect = source["effect"];
	    }
	}
	export class ChannelPolicy {
	    channel_id: number;
	    parent_id: number;
	    synced: boolean;
	    overrides: RoleOverride[];

	    static createFrom(source: any = {}) {
	        return new ChannelPolicy(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.channel_id = source["channel_id"];
	        this.parent_id = source["parent_id"];
	        this.synced = source["synced"];
	        this.overrides = this.convertValues(source["overrides"], RoleOverride);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChannelTreeChange {
	    kind: string;
	    expected_revision: number;
	    channel_id: number;
	    parent_id: number;
	    temporary: boolean;
	    access: ChannelPolicy;
	    sync_to_parent: boolean;
	    order_index?: number;

	    static createFrom(source: any = {}) {
	        return new ChannelTreeChange(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.expected_revision = source["expected_revision"];
	        this.channel_id = source["channel_id"];
	        this.parent_id = source["parent_id"];
	        this.temporary = source["temporary"];
	        this.access = this.convertValues(source["access"], ChannelPolicy);
	        this.sync_to_parent = source["sync_to_parent"];
	        this.order_index = source["order_index"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

	export class MemberIdentity {
	    user_id: number;
	    unique_id: string;
	    nickname: string;
	    role_ids: number[];
	    manageable: boolean;

	    static createFrom(source: any = {}) {
	        return new MemberIdentity(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.user_id = source["user_id"];
	        this.unique_id = source["unique_id"];
	        this.nickname = source["nickname"];
	        this.role_ids = source["role_ids"];
	        this.manageable = source["manageable"];
	    }
	}
	export class MemberPage {
	    revision: number;
	    entries: MemberIdentity[];
	    more: boolean;

	    static createFrom(source: any = {}) {
	        return new MemberPage(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.revision = source["revision"];
	        this.entries = this.convertValues(source["entries"], MemberIdentity);
	        this.more = source["more"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class MemberQuery {
	    channel_id: number;
	    expected_revision: number;
	    search: string;
	    after_id: number;

	    static createFrom(source: any = {}) {
	        return new MemberQuery(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.channel_id = source["channel_id"];
	        this.expected_revision = source["expected_revision"];
	        this.search = source["search"];
	        this.after_id = source["after_id"];
	    }
	}
	export class Role {
	    id: number;
	    name: string;
	    position: number;
	    color: string;
	    icon: string;
	    hoist: boolean;
	    permissions: string[];

	    static createFrom(source: any = {}) {
	        return new Role(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.position = source["position"];
	        this.color = source["color"];
	        this.icon = source["icon"];
	        this.hoist = source["hoist"];
	        this.permissions = source["permissions"];
	    }
	}
	export class RoleChange {
	    kind: string;
	    expected_revision: number;
	    role: Role;
	    role_id: number;
	    role_ids: number[];
	    user_id: number;
	    channel: ChannelPolicy;

	    static createFrom(source: any = {}) {
	        return new RoleChange(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.expected_revision = source["expected_revision"];
	        this.role = this.convertValues(source["role"], Role);
	        this.role_id = source["role_id"];
	        this.role_ids = source["role_ids"];
	        this.user_id = source["user_id"];
	        this.channel = this.convertValues(source["channel"], ChannelPolicy);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RoleDecision {
	    allowed: boolean;
	    reason: string;
	    role_ids?: number[];
	    channel_id?: number;
	    requirement?: string;
	    revision: number;

	    static createFrom(source: any = {}) {
	        return new RoleDecision(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.allowed = source["allowed"];
	        this.reason = source["reason"];
	        this.role_ids = source["role_ids"];
	        this.channel_id = source["channel_id"];
	        this.requirement = source["requirement"];
	        this.revision = source["revision"];
	    }
	}
	export class RoleMember {
	    user_id: number;
	    role_ids: number[];

	    static createFrom(source: any = {}) {
	        return new RoleMember(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.user_id = source["user_id"];
	        this.role_ids = source["role_ids"];
	    }
	}

	export class RolePolicy {
	    revision: number;
	    owner_id: number;
	    everyone_id: number;
	    default_member_role_id: number;
	    roles: Role[];
	    members: RoleMember[];
	    channels: ChannelPolicy[];

	    static createFrom(source: any = {}) {
	        return new RolePolicy(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.revision = source["revision"];
	        this.owner_id = source["owner_id"];
	        this.everyone_id = source["everyone_id"];
	        this.default_member_role_id = source["default_member_role_id"];
	        this.roles = this.convertValues(source["roles"], Role);
	        this.members = this.convertValues(source["members"], RoleMember);
	        this.channels = this.convertValues(source["channels"], ChannelPolicy);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {

	export class Bookmark {
	    name: string;
	    addr: string;
	    nickname: string;
	    folder?: string;
	    order?: number;
	    color?: string;
	    auto_connect?: boolean;
	    profile?: string;
	    nickname_override?: string;
	    avatar_override_b64?: string;

	    static createFrom(source: any = {}) {
	        return new Bookmark(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.addr = source["addr"];
	        this.nickname = source["nickname"];
	        this.folder = source["folder"];
	        this.order = source["order"];
	        this.color = source["color"];
	        this.auto_connect = source["auto_connect"];
	        this.profile = source["profile"];
	        this.nickname_override = source["nickname_override"];
	        this.avatar_override_b64 = source["avatar_override_b64"];
	    }
	}
	export class ChannelOverride {
	    messages?: string;
	    mentions?: string;
	    joins?: string;
	    muted?: boolean;
	    watch_threshold?: number;

	    static createFrom(source: any = {}) {
	        return new ChannelOverride(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.messages = source["messages"];
	        this.mentions = source["mentions"];
	        this.joins = source["joins"];
	        this.muted = source["muted"];
	        this.watch_threshold = source["watch_threshold"];
	    }
	}
	export class ChatExportResult {
	    text: string;
	    messages: number;
	    undecryptable: number;
	    complete: boolean;

	    static createFrom(source: any = {}) {
	        return new ChatExportResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.text = source["text"];
	        this.messages = source["messages"];
	        this.undecryptable = source["undecryptable"];
	        this.complete = source["complete"];
	    }
	}
	export class ChatSearchResult {
	    messages: netproto.ChatHistoryEntry[];
	    scanned: number;
	    undecryptable: number;

	    static createFrom(source: any = {}) {
	        return new ChatSearchResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.messages = this.convertValues(source["messages"], netproto.ChatHistoryEntry);
	        this.scanned = source["scanned"];
	        this.undecryptable = source["undecryptable"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConnectTabResult {
	    tab_id: string;
	    error: string;

	    static createFrom(source: any = {}) {
	        return new ConnectTabResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tab_id = source["tab_id"];
	        this.error = source["error"];
	    }
	}
	export class Contact {
	    unique_id: string;
	    label?: string;
	    nick_history?: string[];
	    notify_online?: boolean;

	    static createFrom(source: any = {}) {
	        return new Contact(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.unique_id = source["unique_id"];
	        this.label = source["label"];
	        this.nick_history = source["nick_history"];
	        this.notify_online = source["notify_online"];
	    }
	}
	export class DMEntry {
	    seq: number;
	    from_unique_id: string;
	    from_nickname: string;
	    body: string;
	    sent_at: number;
	    self?: boolean;
	    client_msg_id?: string;
	    offline?: boolean;
	    enc_verified?: boolean;

	    static createFrom(source: any = {}) {
	        return new DMEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.seq = source["seq"];
	        this.from_unique_id = source["from_unique_id"];
	        this.from_nickname = source["from_nickname"];
	        this.body = source["body"];
	        this.sent_at = source["sent_at"];
	        this.self = source["self"];
	        this.client_msg_id = source["client_msg_id"];
	        this.offline = source["offline"];
	        this.enc_verified = source["enc_verified"];
	    }
	}
	export class DMHistoryContext {
	    tab_id: string;
	    identity_uid: string;
	    activation: string;
	    identity_revision: string;

	    static createFrom(source: any = {}) {
	        return new DMHistoryContext(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tab_id = source["tab_id"];
	        this.identity_uid = source["identity_uid"];
	        this.activation = source["activation"];
	        this.identity_revision = source["identity_revision"];
	    }
	}
	export class DMPeer {
	    unique_id: string;
	    nickname?: string;
	    messages: number;
	    last_at: number;

	    static createFrom(source: any = {}) {
	        return new DMPeer(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.unique_id = source["unique_id"];
	        this.nickname = source["nickname"];
	        this.messages = source["messages"];
	        this.last_at = source["last_at"];
	    }
	}
	export class E2EEDiagnostics {
	    cipher: string;
	    peer_unique_id?: string;
	    safety_number?: string;
	    peer_key_available: boolean;
	    cached_peers: number;
	    scope_keys: number;
	    refused_keys: number;
	    pending_key_pulls: number;

	    static createFrom(source: any = {}) {
	        return new E2EEDiagnostics(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cipher = source["cipher"];
	        this.peer_unique_id = source["peer_unique_id"];
	        this.safety_number = source["safety_number"];
	        this.peer_key_available = source["peer_key_available"];
	        this.cached_peers = source["cached_peers"];
	        this.scope_keys = source["scope_keys"];
	        this.refused_keys = source["refused_keys"];
	        this.pending_key_pulls = source["pending_key_pulls"];
	    }
	}
	export class HotkeyProfile {
	    ptt?: string;
	    mute_toggle?: string;
	    whisper_reply?: string;
	    quick_connect?: string;
	    compact_toggle?: string;
	    zen_toggle?: string;
	    deafen_toggle?: string;

	    static createFrom(source: any = {}) {
	        return new HotkeyProfile(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ptt = source["ptt"];
	        this.mute_toggle = source["mute_toggle"];
	        this.whisper_reply = source["whisper_reply"];
	        this.quick_connect = source["quick_connect"];
	        this.compact_toggle = source["compact_toggle"];
	        this.zen_toggle = source["zen_toggle"];
	        this.deafen_toggle = source["deafen_toggle"];
	    }
	}
	export class IdentityEntry {
	    id: string;
	    name: string;
	    unique_id: string;
	    active: boolean;
	    created_at?: string;
	    exported_at?: string;
	    security_level: number;
	    protection: string;
	    path: string;
	    error?: string;

	    static createFrom(source: any = {}) {
	        return new IdentityEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.unique_id = source["unique_id"];
	        this.active = source["active"];
	        this.created_at = source["created_at"];
	        this.exported_at = source["exported_at"];
	        this.security_level = source["security_level"];
	        this.protection = source["protection"];
	        this.path = source["path"];
	        this.error = source["error"];
	    }
	}
	export class IdentityLevelResult {
	    level: number;
	    counter: number;
	    error?: string;

	    static createFrom(source: any = {}) {
	        return new IdentityLevelResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.level = source["level"];
	        this.counter = source["counter"];
	        this.error = source["error"];
	    }
	}
	export class NotifyChannels {
	    toast: boolean;
	    sound: boolean;
	    flash: boolean;
	    native: boolean;

	    static createFrom(source: any = {}) {
	        return new NotifyChannels(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.toast = source["toast"];
	        this.sound = source["sound"];
	        this.flash = source["flash"];
	        this.native = source["native"];
	    }
	}
	export class RecentServer {
	    addr: string;
	    nickname: string;
	    last_used: number;

	    static createFrom(source: any = {}) {
	        return new RecentServer(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.addr = source["addr"];
	        this.nickname = source["nickname"];
	        this.last_used = source["last_used"];
	    }
	}
	export class SessionInfo {
	    authorization_model: string;
	    client_id: string;
	    is_guest: boolean;
	    connected: boolean;
	    security: string;

	    static createFrom(source: any = {}) {
	        return new SessionInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.authorization_model = source["authorization_model"];
	        this.client_id = source["client_id"];
	        this.is_guest = source["is_guest"];
	        this.connected = source["connected"];
	        this.security = source["security"];
	    }
	}
	export class Settings {
	    settings_version: number;
	    bookmarks: Bookmark[];
	    recents: RecentServer[];
	    always_on_top: boolean;
	    minimize_to_tray: boolean;
	    close_to_tray: boolean;
	    compact_mode: boolean;
	    theme: string;
	    accent_color: string;
	    user_css: string;
	    ui_font: string;
	    ui_font_size: number;
	    window_opacity: number;
	    language: string;
	    reduce_motion: boolean;
	    sidebar_width: number;
	    details_width: number;
	    idle_video_pause: boolean;
	    dnd_enabled: boolean;
	    dnd_from: string;
	    dnd_to: string;
	    capture_device_id: string;
	    activation_mode: string;
	    vad_threshold: number;
	    echo_cancellation: boolean;
	    noise_suppression: boolean;
	    playback_device_id: string;
	    volume: number;
	    hotkey_ptt: string;
	    hotkey_mute: string;
	    hotkey_deafen: string;
	    hotkey_quick_connect: string;
	    hotkey_zen: string;
	    hotkey_compact: string;
	    hotkey_profiles?: Record<string, HotkeyProfile>;
	    chat_max_lines: number;
	    log_channel_chat: boolean;
	    log_private_chat: boolean;
	    log_server_chat: boolean;
	    notify_join_leave: boolean;
	    notify_connection: boolean;
	    play_sounds: boolean;
	    whisper_sound: boolean;
	    chat_notification_level: string;
	    whisper_clients: string[];
	    whisper_channels: number[];
	    whisper_active: boolean;
	    download_folder: string;
	    reconnect_on_loss: boolean;
	    updates_auto_check: boolean;
	    user_volumes: Record<string, number>;
	    muted_users: string[];
	    ptt_release_delay_ms: number;
	    warn_muted_talking: boolean;
	    warn_empty_channel: boolean;
	    sound_pack: string;
	    sound_volume: number;
	    effects_enabled: boolean;
	    duck_effects_while_speaking: boolean;
	    spoken_messages: boolean;
	    speech_volume: number;
	    speech_language: string;
	    speech_connection: boolean;
	    speech_admin: boolean;
	    speech_removal: boolean;
	    speech_permissions: boolean;
	    speech_events?: Record<string, boolean>;
	    event_sounds: Record<string, boolean>;
	    whisper_reply_hotkey: string;
	    voice_limiter: boolean;
	    gain_normalize: boolean;
	    camera_fps: number;
	    low_bandwidth: boolean;
	    allow_plaintext: boolean;
	    e2ee_verified?: Record<string, string>;
	    chat_timestamps: string;
	    chat_density: string;
	    chat_font_size: number;
	    chat_layout: string;
	    sys_join_leave: boolean;
	    sys_kick: boolean;
	    last_read_channels: Record<string, number>;
	    dismissed_announcement: string;
	    contacts?: Contact[];
	    blocked_users?: string[];
	    user_notes?: Record<string, string>;
	    recent_channels?: Record<string, Array<number>>;
	    auto_away_minutes: number;
	    auto_away_message: string;
	    onboarding_done: boolean;
	    last_seen_version: string;
	    active_identity?: string;
	    identity_key_protection?: string;
	    notify_matrix?: Record<string, NotifyChannels>;
	    channel_notify?: Record<string, ChannelOverride>;
	    keywords?: Record<string, Array<string>>;
	    alpha_dismissed: string;

	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.settings_version = source["settings_version"];
	        this.bookmarks = this.convertValues(source["bookmarks"], Bookmark);
	        this.recents = this.convertValues(source["recents"], RecentServer);
	        this.always_on_top = source["always_on_top"];
	        this.minimize_to_tray = source["minimize_to_tray"];
	        this.close_to_tray = source["close_to_tray"];
	        this.compact_mode = source["compact_mode"];
	        this.theme = source["theme"];
	        this.accent_color = source["accent_color"];
	        this.user_css = source["user_css"];
	        this.ui_font = source["ui_font"];
	        this.ui_font_size = source["ui_font_size"];
	        this.window_opacity = source["window_opacity"];
	        this.language = source["language"];
	        this.reduce_motion = source["reduce_motion"];
	        this.sidebar_width = source["sidebar_width"];
	        this.details_width = source["details_width"];
	        this.idle_video_pause = source["idle_video_pause"];
	        this.dnd_enabled = source["dnd_enabled"];
	        this.dnd_from = source["dnd_from"];
	        this.dnd_to = source["dnd_to"];
	        this.capture_device_id = source["capture_device_id"];
	        this.activation_mode = source["activation_mode"];
	        this.vad_threshold = source["vad_threshold"];
	        this.echo_cancellation = source["echo_cancellation"];
	        this.noise_suppression = source["noise_suppression"];
	        this.playback_device_id = source["playback_device_id"];
	        this.volume = source["volume"];
	        this.hotkey_ptt = source["hotkey_ptt"];
	        this.hotkey_mute = source["hotkey_mute"];
	        this.hotkey_deafen = source["hotkey_deafen"];
	        this.hotkey_quick_connect = source["hotkey_quick_connect"];
	        this.hotkey_zen = source["hotkey_zen"];
	        this.hotkey_compact = source["hotkey_compact"];
	        this.hotkey_profiles = this.convertValues(source["hotkey_profiles"], HotkeyProfile, true);
	        this.chat_max_lines = source["chat_max_lines"];
	        this.log_channel_chat = source["log_channel_chat"];
	        this.log_private_chat = source["log_private_chat"];
	        this.log_server_chat = source["log_server_chat"];
	        this.notify_join_leave = source["notify_join_leave"];
	        this.notify_connection = source["notify_connection"];
	        this.play_sounds = source["play_sounds"];
	        this.whisper_sound = source["whisper_sound"];
	        this.chat_notification_level = source["chat_notification_level"];
	        this.whisper_clients = source["whisper_clients"];
	        this.whisper_channels = source["whisper_channels"];
	        this.whisper_active = source["whisper_active"];
	        this.download_folder = source["download_folder"];
	        this.reconnect_on_loss = source["reconnect_on_loss"];
	        this.updates_auto_check = source["updates_auto_check"];
	        this.user_volumes = source["user_volumes"];
	        this.muted_users = source["muted_users"];
	        this.ptt_release_delay_ms = source["ptt_release_delay_ms"];
	        this.warn_muted_talking = source["warn_muted_talking"];
	        this.warn_empty_channel = source["warn_empty_channel"];
	        this.sound_pack = source["sound_pack"];
	        this.sound_volume = source["sound_volume"];
	        this.effects_enabled = source["effects_enabled"];
	        this.duck_effects_while_speaking = source["duck_effects_while_speaking"];
	        this.spoken_messages = source["spoken_messages"];
	        this.speech_volume = source["speech_volume"];
	        this.speech_language = source["speech_language"];
	        this.speech_connection = source["speech_connection"];
	        this.speech_admin = source["speech_admin"];
	        this.speech_removal = source["speech_removal"];
	        this.speech_permissions = source["speech_permissions"];
	        this.speech_events = source["speech_events"];
	        this.event_sounds = source["event_sounds"];
	        this.whisper_reply_hotkey = source["whisper_reply_hotkey"];
	        this.voice_limiter = source["voice_limiter"];
	        this.gain_normalize = source["gain_normalize"];
	        this.camera_fps = source["camera_fps"];
	        this.low_bandwidth = source["low_bandwidth"];
	        this.allow_plaintext = source["allow_plaintext"];
	        this.e2ee_verified = source["e2ee_verified"];
	        this.chat_timestamps = source["chat_timestamps"];
	        this.chat_density = source["chat_density"];
	        this.chat_font_size = source["chat_font_size"];
	        this.chat_layout = source["chat_layout"];
	        this.sys_join_leave = source["sys_join_leave"];
	        this.sys_kick = source["sys_kick"];
	        this.last_read_channels = source["last_read_channels"];
	        this.dismissed_announcement = source["dismissed_announcement"];
	        this.contacts = this.convertValues(source["contacts"], Contact);
	        this.blocked_users = source["blocked_users"];
	        this.user_notes = source["user_notes"];
	        this.recent_channels = source["recent_channels"];
	        this.auto_away_minutes = source["auto_away_minutes"];
	        this.auto_away_message = source["auto_away_message"];
	        this.onboarding_done = source["onboarding_done"];
	        this.last_seen_version = source["last_seen_version"];
	        this.active_identity = source["active_identity"];
	        this.identity_key_protection = source["identity_key_protection"];
	        this.notify_matrix = this.convertValues(source["notify_matrix"], NotifyChannels, true);
	        this.channel_notify = this.convertValues(source["channel_notify"], ChannelOverride, true);
	        this.keywords = source["keywords"];
	        this.alpha_dismissed = source["alpha_dismissed"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TabInfo {
	    id: string;
	    addr: string;
	    nickname: string;
	    connected: boolean;
	    active: boolean;
	    unread: number;
	    mentions: number;

	    static createFrom(source: any = {}) {
	        return new TabInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.addr = source["addr"];
	        this.nickname = source["nickname"];
	        this.connected = source["connected"];
	        this.active = source["active"];
	        this.unread = source["unread"];
	        this.mentions = source["mentions"];
	    }
	}
	export class UpdateInfo {
	    available: boolean;
	    version: string;
	    url: string;
	    sha256url: string;
	    signatureUrl: string;
	    size: number;

	    static createFrom(source: any = {}) {
	        return new UpdateInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.version = source["version"];
	        this.url = source["url"];
	        this.sha256url = source["sha256url"];
	        this.signatureUrl = source["signatureUrl"];
	        this.size = source["size"];
	    }
	}
	export class identityInfo {
	    unique_id: string;
	    created_at?: string;
	    path: string;

	    static createFrom(source: any = {}) {
	        return new identityInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.unique_id = source["unique_id"];
	        this.created_at = source["created_at"];
	        this.path = source["path"];
	    }
	}

}

export namespace netproto {

	export class AccessCheck {
	    user_id: number;
	    channel_id: number;
	    capability: string;
	    expected_revision: number;

	    static createFrom(source: any = {}) {
	        return new AccessCheck(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.user_id = source["user_id"];
	        this.channel_id = source["channel_id"];
	        this.capability = source["capability"];
	        this.expected_revision = source["expected_revision"];
	    }
	}
	export class AccessCheckResult {
	    decision: authorization.RoleDecision;
	    can_manage_member: boolean;

	    static createFrom(source: any = {}) {
	        return new AccessCheckResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.decision = this.convertValues(source["decision"], authorization.RoleDecision);
	        this.can_manage_member = source["can_manage_member"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AuditEntry {
	    id: number;
	    actor: string;
	    action: string;
	    target: string;
	    detail: string;
	    created_at: number;
	    restricted?: boolean;
	    structured?: boolean;

	    static createFrom(source: any = {}) {
	        return new AuditEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.actor = source["actor"];
	        this.action = source["action"];
	        this.target = source["target"];
	        this.detail = source["detail"];
	        this.created_at = source["created_at"];
	        this.restricted = source["restricted"];
	        this.structured = source["structured"];
	    }
	}
	export class AuditLogResponse {
	    entries: AuditEntry[];
	    capabilities?: authorization.CapabilityInfo[];

	    static createFrom(source: any = {}) {
	        return new AuditLogResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.entries = this.convertValues(source["entries"], AuditEntry);
	        this.capabilities = this.convertValues(source["capabilities"], authorization.CapabilityInfo);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AvatarData {
	    unique_id: string;
	    data_base64: string;
	    content_type: string;

	    static createFrom(source: any = {}) {
	        return new AvatarData(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.unique_id = source["unique_id"];
	        this.data_base64 = source["data_base64"];
	        this.content_type = source["content_type"];
	    }
	}
	export class BanEntry {
	    id: number;
	    type: number;
	    value: string;
	    reason?: string;
	    banned_by?: string;
	    created_at: number;
	    expires_at?: number;

	    static createFrom(source: any = {}) {
	        return new BanEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.type = source["type"];
	        this.value = source["value"];
	        this.reason = source["reason"];
	        this.banned_by = source["banned_by"];
	        this.created_at = source["created_at"];
	        this.expires_at = source["expires_at"];
	    }
	}
	export class BanListResponse {
	    bans: BanEntry[];

	    static createFrom(source: any = {}) {
	        return new BanListResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bans = this.convertValues(source["bans"], BanEntry);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChannelAccessPreview {
	    scope_channel_id?: number;
	    change: authorization.RoleChange;
	    tree?: authorization.ChannelTreeChange;
	    user_ids: number[];

	    static createFrom(source: any = {}) {
	        return new ChannelAccessPreview(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.scope_channel_id = source["scope_channel_id"];
	        this.change = this.convertValues(source["change"], authorization.RoleChange);
	        this.tree = this.convertValues(source["tree"], authorization.ChannelTreeChange);
	        this.user_ids = source["user_ids"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChannelIconData {
	    channel_id: number;
	    data_base64: string;
	    content_type?: string;

	    static createFrom(source: any = {}) {
	        return new ChannelIconData(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.channel_id = source["channel_id"];
	        this.data_base64 = source["data_base64"];
	        this.content_type = source["content_type"];
	    }
	}
	export class ChannelKey {
	    channel_id: number;
	    key_id: number;
	    sealed_key: string;

	    static createFrom(source: any = {}) {
	        return new ChannelKey(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.channel_id = source["channel_id"];
	        this.key_id = source["key_id"];
	        this.sealed_key = source["sealed_key"];
	    }
	}
	export class ChatFilterResponse {
	    word_filter: string;
	    link_blacklist: string;
	    link_whitelist: string;
	    from_config?: boolean;

	    static createFrom(source: any = {}) {
	        return new ChatFilterResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.word_filter = source["word_filter"];
	        this.link_blacklist = source["link_blacklist"];
	        this.link_whitelist = source["link_whitelist"];
	        this.from_config = source["from_config"];
	    }
	}
	export class ChatHistoryEntry {
	    id: number;
	    from_unique_id: string;
	    from_nickname: string;
	    reply_to_id?: number;
	    version: number;
	    body_enc?: string;
	    key_id?: number;
	    body?: string;
	    enc_verified?: boolean;
	    sent_at: number;
	    edited_at?: number;
	    deleted?: boolean;
	    reactions?: Record<string, number>;

	    static createFrom(source: any = {}) {
	        return new ChatHistoryEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.from_unique_id = source["from_unique_id"];
	        this.from_nickname = source["from_nickname"];
	        this.reply_to_id = source["reply_to_id"];
	        this.version = source["version"];
	        this.body_enc = source["body_enc"];
	        this.key_id = source["key_id"];
	        this.body = source["body"];
	        this.enc_verified = source["enc_verified"];
	        this.sent_at = source["sent_at"];
	        this.edited_at = source["edited_at"];
	        this.deleted = source["deleted"];
	        this.reactions = source["reactions"];
	    }
	}
	export class ChatHistoryResponse {
	    channel_id: number;
	    messages: ChatHistoryEntry[];
	    keys?: ChannelKey[];
	    refused?: number[];
	    truncated?: boolean;

	    static createFrom(source: any = {}) {
	        return new ChatHistoryResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.channel_id = source["channel_id"];
	        this.messages = this.convertValues(source["messages"], ChatHistoryEntry);
	        this.keys = this.convertValues(source["keys"], ChannelKey);
	        this.refused = source["refused"];
	        this.truncated = source["truncated"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChatPinEntry {
	    message_id: number;
	    pinned_by: string;
	    pinned_at: number;
	    message?: ChatHistoryEntry;

	    static createFrom(source: any = {}) {
	        return new ChatPinEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.message_id = source["message_id"];
	        this.pinned_by = source["pinned_by"];
	        this.pinned_at = source["pinned_at"];
	        this.message = this.convertValues(source["message"], ChatHistoryEntry);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChatPinsResponse {
	    channel_id: number;
	    pins: ChatPinEntry[];
	    keys?: ChannelKey[];
	    refused?: number[];
	    truncated?: boolean;

	    static createFrom(source: any = {}) {
	        return new ChatPinsResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.channel_id = source["channel_id"];
	        this.pins = this.convertValues(source["pins"], ChatPinEntry);
	        this.keys = this.convertValues(source["keys"], ChannelKey);
	        this.refused = source["refused"];
	        this.truncated = source["truncated"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ClientInfoResponse {
	    client_id: string;
	    unique_id: string;
	    nickname: string;
	    channel_id: number;
	    connected_at: number;
	    idle_seconds: number;
	    ping_ms: number;
	    ip?: string;
	    port?: number;
	    bytes_in: number;
	    bytes_out: number;

	    static createFrom(source: any = {}) {
	        return new ClientInfoResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.client_id = source["client_id"];
	        this.unique_id = source["unique_id"];
	        this.nickname = source["nickname"];
	        this.channel_id = source["channel_id"];
	        this.connected_at = source["connected_at"];
	        this.idle_seconds = source["idle_seconds"];
	        this.ping_ms = source["ping_ms"];
	        this.ip = source["ip"];
	        this.port = source["port"];
	        this.bytes_in = source["bytes_in"];
	        this.bytes_out = source["bytes_out"];
	    }
	}
	export class ComplaintEntry {
	    target_unique_id: string;
	    target_nickname?: string;
	    from_unique_id: string;
	    from_nickname?: string;
	    reason: string;
	    created_at: number;

	    static createFrom(source: any = {}) {
	        return new ComplaintEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.target_unique_id = source["target_unique_id"];
	        this.target_nickname = source["target_nickname"];
	        this.from_unique_id = source["from_unique_id"];
	        this.from_nickname = source["from_nickname"];
	        this.reason = source["reason"];
	        this.created_at = source["created_at"];
	    }
	}
	export class Complaints {
	    entries: ComplaintEntry[];

	    static createFrom(source: any = {}) {
	        return new Complaints(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.entries = this.convertValues(source["entries"], ComplaintEntry);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class EmojiData {
	    name: string;
	    data_base64: string;
	    content_type: string;

	    static createFrom(source: any = {}) {
	        return new EmojiData(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.data_base64 = source["data_base64"];
	        this.content_type = source["content_type"];
	    }
	}
	export class EmojiEntry {
	    name: string;
	    file_name: string;

	    static createFrom(source: any = {}) {
	        return new EmojiEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.file_name = source["file_name"];
	    }
	}
	export class EmojiListResponse {
	    emojis: EmojiEntry[];

	    static createFrom(source: any = {}) {
	        return new EmojiListResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.emojis = this.convertValues(source["emojis"], EmojiEntry);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class FileEntry {
	    name: string;
	    folder?: string;
	    size: number;
	    sha256: string;
	    uploader?: string;
	    // Go type: time
	    uploaded_at: any;
	    encrypted?: boolean;

	    static createFrom(source: any = {}) {
	        return new FileEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.folder = source["folder"];
	        this.size = source["size"];
	        this.sha256 = source["sha256"];
	        this.uploader = source["uploader"];
	        this.uploaded_at = this.convertValues(source["uploaded_at"], null);
	        this.encrypted = source["encrypted"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class FileLinkResponse {
	    session_bound?: boolean;
	    path: string;
	    scheme?: string;
	    health_port: number;
	    expires_at: number;

	    static createFrom(source: any = {}) {
	        return new FileLinkResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.session_bound = source["session_bound"];
	        this.path = source["path"];
	        this.scheme = source["scheme"];
	        this.health_port = source["health_port"];
	        this.expires_at = source["expires_at"];
	    }
	}
	export class FileListResponse {
	    entries: FileEntry[];
	    folders?: string[];
	    used_bytes: number;
	    quota_bytes: number;

	    static createFrom(source: any = {}) {
	        return new FileListResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.entries = this.convertValues(source["entries"], FileEntry);
	        this.folders = source["folders"];
	        this.used_bytes = source["used_bytes"];
	        this.quota_bytes = source["quota_bytes"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class FileVersionsResponse {
	    entries: FileEntry[];

	    static createFrom(source: any = {}) {
	        return new FileVersionsResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.entries = this.convertValues(source["entries"], FileEntry);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ICEServer {
	    urls: string[];
	    username?: string;
	    credential?: string;

	    static createFrom(source: any = {}) {
	        return new ICEServer(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.urls = source["urls"];
	        this.username = source["username"];
	        this.credential = source["credential"];
	    }
	}
	export class MediaLimits {
	    video_max_bitrate: number;
	    video_max_width: number;
	    video_max_height: number;

	    static createFrom(source: any = {}) {
	        return new MediaLimits(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_max_bitrate = source["video_max_bitrate"];
	        this.video_max_width = source["video_max_width"];
	        this.video_max_height = source["video_max_height"];
	    }
	}
	export class MediaLimitsSaved {
	    revision: number;
	    video_max_bitrate: number;
	    video_max_width: number;
	    video_max_height: number;

	    static createFrom(source: any = {}) {
	        return new MediaLimitsSaved(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.revision = source["revision"];
	        this.video_max_bitrate = source["video_max_bitrate"];
	        this.video_max_width = source["video_max_width"];
	        this.video_max_height = source["video_max_height"];
	    }
	}
	export class MemberVoiceSet {
	    client_id: string;
	    channel_id: number;
	    muted?: boolean;
	    deafened?: boolean;

	    static createFrom(source: any = {}) {
	        return new MemberVoiceSet(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.client_id = source["client_id"];
	        this.channel_id = source["channel_id"];
	        this.muted = source["muted"];
	        this.deafened = source["deafened"];
	    }
	}
	export class MemberVoiceState {
	    revision: number;
	    client_id: string;
	    channel_id: number;
	    muted: boolean;
	    deafened: boolean;

	    static createFrom(source: any = {}) {
	        return new MemberVoiceState(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.revision = source["revision"];
	        this.client_id = source["client_id"];
	        this.channel_id = source["channel_id"];
	        this.muted = source["muted"];
	        this.deafened = source["deafened"];
	    }
	}
	export class RoleBanRemoved {
	    ban_id: number;

	    static createFrom(source: any = {}) {
	        return new RoleBanRemoved(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ban_id = source["ban_id"];
	    }
	}
	export class RoleChangeResult {
	    revision: number;
	    created_role_id?: number;
	    enforcement_pending?: boolean;

	    static createFrom(source: any = {}) {
	        return new RoleChangeResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.revision = source["revision"];
	        this.created_role_id = source["created_role_id"];
	        this.enforcement_pending = source["enforcement_pending"];
	    }
	}
	export class RoleChannelAccess {
	    synced: boolean;
	    overrides: authorization.RoleOverride[];

	    static createFrom(source: any = {}) {
	        return new RoleChannelAccess(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.synced = source["synced"];
	        this.overrides = this.convertValues(source["overrides"], authorization.RoleOverride);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RoleChannelSettings {
	    name: string;
	    topic: string;
	    description: string;
	    order_index: number;
	    max_clients: number;
	    slow_mode_seconds: number;
	    opus_bitrate: number;
	    opus_fec: boolean;
	    opus_dtx: boolean;
	    opus_stereo: boolean;

	    static createFrom(source: any = {}) {
	        return new RoleChannelSettings(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.topic = source["topic"];
	        this.description = source["description"];
	        this.order_index = source["order_index"];
	        this.max_clients = source["max_clients"];
	        this.slow_mode_seconds = source["slow_mode_seconds"];
	        this.opus_bitrate = source["opus_bitrate"];
	        this.opus_fec = source["opus_fec"];
	        this.opus_dtx = source["opus_dtx"];
	        this.opus_stereo = source["opus_stereo"];
	    }
	}
	export class RoleChannelChange {
	    kind: string;
	    expected_revision: number;
	    channel_id: number;
	    parent_id: number;
	    sync_to_parent: boolean;
	    order_index?: number;
	    access?: RoleChannelAccess;
	    settings?: RoleChannelSettings;
	    channel_type: number;
	    password?: string;

	    static createFrom(source: any = {}) {
	        return new RoleChannelChange(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.expected_revision = source["expected_revision"];
	        this.channel_id = source["channel_id"];
	        this.parent_id = source["parent_id"];
	        this.sync_to_parent = source["sync_to_parent"];
	        this.order_index = source["order_index"];
	        this.access = this.convertValues(source["access"], RoleChannelAccess);
	        this.settings = this.convertValues(source["settings"], RoleChannelSettings);
	        this.channel_type = source["channel_type"];
	        this.password = source["password"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RoleChannelIconSaved {
	    channel_id: number;

	    static createFrom(source: any = {}) {
	        return new RoleChannelIconSaved(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.channel_id = source["channel_id"];
	    }
	}
	export class RoleChannelOption {
	    id: number;
	    name: string;
	    can_sync: boolean;

	    static createFrom(source: any = {}) {
	        return new RoleChannelOption(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.can_sync = source["can_sync"];
	    }
	}
	export class RoleChannelQuery {
	    kind: string;
	    channel_id: number;

	    static createFrom(source: any = {}) {
	        return new RoleChannelQuery(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.channel_id = source["channel_id"];
	    }
	}
	export class RoleChannelResult {
	    revision: number;
	    channel_id: number;
	    enforcement_pending: boolean;

	    static createFrom(source: any = {}) {
	        return new RoleChannelResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.revision = source["revision"];
	        this.channel_id = source["channel_id"];
	        this.enforcement_pending = source["enforcement_pending"];
	    }
	}

	export class RoleChannelState {
	    impact_channel_ids: number[];
	    capabilities: authorization.CapabilityInfo[];
	    revision: number;
	    channel_id: number;
	    name: string;
	    settings: RoleChannelSettings;
	    affected_channels: number;
	    can_create_permanent: boolean;
	    can_create_temporary: boolean;
	    can_manage_access: boolean;
	    everyone_id: number;
	    roles: RoleChannelOption[];
	    grantable_capabilities: string[];
	    destinations: RoleChannelOption[];

	    static createFrom(source: any = {}) {
	        return new RoleChannelState(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.impact_channel_ids = source["impact_channel_ids"];
	        this.capabilities = this.convertValues(source["capabilities"], authorization.CapabilityInfo);
	        this.revision = source["revision"];
	        this.channel_id = source["channel_id"];
	        this.name = source["name"];
	        this.settings = this.convertValues(source["settings"], RoleChannelSettings);
	        this.affected_channels = source["affected_channels"];
	        this.can_create_permanent = source["can_create_permanent"];
	        this.can_create_temporary = source["can_create_temporary"];
	        this.can_manage_access = source["can_manage_access"];
	        this.everyone_id = source["everyone_id"];
	        this.roles = this.convertValues(source["roles"], RoleChannelOption);
	        this.grantable_capabilities = source["grantable_capabilities"];
	        this.destinations = this.convertValues(source["destinations"], RoleChannelOption);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RoleState {
	    impact_channel_ids: number[];
	    parent_access_available: boolean;
	    effective_overrides: authorization.RoleOverride[];
	    parent_overrides: authorization.RoleOverride[];
	    actor_id: number;
	    policy: authorization.RolePolicy;
	    capabilities: authorization.CapabilityInfo[];
	    manageable_role_ids: number[];
	    grantable_capabilities: string[];

	    static createFrom(source: any = {}) {
	        return new RoleState(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.impact_channel_ids = source["impact_channel_ids"];
	        this.parent_access_available = source["parent_access_available"];
	        this.effective_overrides = this.convertValues(source["effective_overrides"], authorization.RoleOverride);
	        this.parent_overrides = this.convertValues(source["parent_overrides"], authorization.RoleOverride);
	        this.actor_id = source["actor_id"];
	        this.policy = this.convertValues(source["policy"], authorization.RolePolicy);
	        this.capabilities = this.convertValues(source["capabilities"], authorization.CapabilityInfo);
	        this.manageable_role_ids = source["manageable_role_ids"];
	        this.grantable_capabilities = source["grantable_capabilities"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ServerBannerData {
	    data_base64: string;
	    content_type?: string;

	    static createFrom(source: any = {}) {
	        return new ServerBannerData(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.data_base64 = source["data_base64"];
	        this.content_type = source["content_type"];
	    }
	}
	export class ServerConfig {
	    max_clients: number;
	    client_timeout_seconds: number;
	    opus_bitrate: number;
	    opus_fec: boolean;
	    opus_dtx: boolean;
	    opus_stereo: boolean;
	    media_limits_management?: boolean;

	    static createFrom(source: any = {}) {
	        return new ServerConfig(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.max_clients = source["max_clients"];
	        this.client_timeout_seconds = source["client_timeout_seconds"];
	        this.opus_bitrate = source["opus_bitrate"];
	        this.opus_fec = source["opus_fec"];
	        this.opus_dtx = source["opus_dtx"];
	        this.opus_stereo = source["opus_stereo"];
	        this.media_limits_management = source["media_limits_management"];
	    }
	}
	export class ServerIconData {
	    data_base64?: string;
	    content_type?: string;

	    static createFrom(source: any = {}) {
	        return new ServerIconData(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.data_base64 = source["data_base64"];
	        this.content_type = source["content_type"];
	    }
	}
	export class ServerInfoResponse {
	    name: string;
	    version: string;
	    platform?: string;
	    uptime_seconds: number;
	    clients_online: number;
	    channels_online: number;
	    max_clients: number;
	    motd?: string;

	    static createFrom(source: any = {}) {
	        return new ServerInfoResponse(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.version = source["version"];
	        this.platform = source["platform"];
	        this.uptime_seconds = source["uptime_seconds"];
	        this.clients_online = source["clients_online"];
	        this.channels_online = source["channels_online"];
	        this.max_clients = source["max_clients"];
	        this.motd = source["motd"];
	    }
	}
	export class TrackSlot {
	    track_id: string;
	    slot: string;

	    static createFrom(source: any = {}) {
	        return new TrackSlot(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.track_id = source["track_id"];
	        this.slot = source["slot"];
	    }
	}

}
