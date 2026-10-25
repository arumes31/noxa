// frame.js — the noXa control framing: 4-byte big-endian length (type +
// payload), 2-byte big-endian type, JSON payload. WebSocket messages carry a
// byte stream, so frames may be split or merged across messages.

export const MAX_FRAME_SIZE = 1 << 20;

// Message types of the control protocol (internal/netproto/codec.go and the
// per-feature files beside it).
export const Msg = Object.freeze({
    Authenticate: 1, AuthResponse: 2, ChatSend: 5, ChatBroadcast: 6, Error: 7, Ping: 8, Pong: 9,
    JoinChannel: 10, MoveClient: 12, KickClient: 13, Snapshot: 14, Event: 15, AuthChallenge: 16, AuthSignature: 17,
    WebRTCOffer: 18, WebRTCAnswer: 19, ICECandidate: 20, WhisperSet: 21, VideoQuality: 23,
    FileTransferInit: 25, FileTransferInitResponse: 26, FileList: 27, FileListResponse: 28,
    AvatarSet: 29, AvatarGet: 30, AvatarData: 31, ChannelIconSet: 32, Complaint: 34, ScreenShare: 35,
    ClientInfoQuery: 38, ClientInfoResponse: 39, PrioritySpeaker: 41,
    KeyPublish: 42, KeyRequest: 43, KeyResponse: 44, ChannelKey: 45,
    ChatHistory: 46, ChatHistoryResponse: 47, ChatEdit: 48, ChatDelete: 49, ChatPin: 50, ChatPins: 51, ChatPinsResponse: 52,
    Typing: 53, ChatDelivered: 54, ChatRead: 55, EmojiUpload: 56, EmojiList: 57, EmojiListResponse: 58, ChatReact: 59,
    EmojiGet: 60, EmojiData: 61, AuditLog: 74, AuditLogResponse: 75, BanList: 81, BanListResponse: 82,
    FileDelete: 86, FileRename: 87, FileVersions: 88, FileVersionsResponse: 89, FileLink: 90, FileLinkResponse: 91,
    ServerIconSet: 92, ServerIconGet: 93, ServerIconData: 94, SetStatus: 95, Poke: 96, ServerInfoQuery: 97, ServerInfoResponse: 98,
    ChatKeyRequest: 99, ChatKeyBundle: 100, ChatFilterGet: 101, ChatFilterSet: 102, ChatFilterResponse: 103,
    ComplaintList: 107, Complaints: 108, ComplaintClear: 109, ChannelIconGet: 114, ChannelIconData: 115,
    ServerBannerSet: 116, ServerBannerGet: 117, ServerBannerData: 118, EmojiDelete: 119, EmojiRename: 120,
    ServerRules: 121, ServerRulesAccept: 122, ChannelSubscribe: 123, SubscriptionState: 124,
    ServerConfigQuery: 125, ServerConfigSet: 126, ServerConfigResponse: 127,
    RoleQuery: 133, RoleState: 134, RoleChange: 135, RoleChangeResult: 136, AccessCheck: 137, AccessCheckResult: 138,
    RoleMemberQuery: 139, RoleMembers: 140, MemberVoiceSet: 141, MemberVoiceState: 142,
    RoleChannelQuery: 143, RoleChannelState: 144, RoleChannelChange: 145, RoleChannelResult: 146,
    RoleBanRemove: 147, RoleBanRemoved: 148, RoleChannelIconSet: 149, RoleChannelIconSaved: 150,
    ChannelAccessPreview: 151, ChannelAccessImpact: 152, ChatMutationSaved: 153, ChatAccepted: 154, PokeAccepted: 155,
    ClientMoved: 156, ClientRemoved: 157, ChannelJoined: 158, AssetMutationSaved: 159, FileMutationSaved: 160,
    StatusSaved: 161, MediaControlSaved: 162, MediaLimitsChanged: 163, MediaLimitsSet: 164, MediaLimitsSaved: 165,
    VideoStreamControl: 166, VideoStreamResult: 167, PollRequest: 168, PollState: 169,
    ConversationRequest: 170, ConversationResult: 171, CallRequest: 172, CallResult: 173,
    DiscussionRequest: 180, DiscussionResult: 181, WebhookRequest: 182, WebhookResult: 183,
    DisplayNameSet: 184, DisplayNameSaved: 185, AudioStateSet: 186, AudioStateSaved: 187, VoiceTelemetry: 188,
    ChannelInviteRequest: 189, ChannelInviteResult: 190, VoiceQueueRequest: 191, VoiceQueueResult: 192,
    RelayRequest: 193, RelayResponse: 194, DeviceRequest: 195, DeviceResult: 196, DeviceEvent: 197,
    LinkPreviewRequest: 198, LinkPreviewResult: 199,
});

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/** Encodes one frame. */
export function encodeFrame(type, message) {
    const payload = encoder.encode(JSON.stringify(message ?? {}));
    if (payload.length + 2 > MAX_FRAME_SIZE) throw new Error("frame too large");
    const out = new Uint8Array(6 + payload.length);
    const view = new DataView(out.buffer);
    view.setUint32(0, payload.length + 2);
    view.setUint16(4, type);
    out.set(payload, 6);
    return out;
}

/** Accumulates stream bytes and yields complete frames. */
export class FrameReader {
    #buffer = new Uint8Array(0);

    /** Appends bytes and returns every complete frame as {type, payload}. */
    push(bytes) {
        const chunk = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
        const merged = new Uint8Array(this.#buffer.length + chunk.length);
        merged.set(this.#buffer);
        merged.set(chunk, this.#buffer.length);
        const frames = [];
        let offset = 0;
        while (merged.length - offset >= 4) {
            const view = new DataView(merged.buffer, merged.byteOffset + offset);
            const length = view.getUint32(0);
            if (length < 2 || length > MAX_FRAME_SIZE) throw new Error(`invalid frame length ${length}`);
            if (merged.length - offset < 4 + length) break;
            const type = view.getUint16(4);
            const body = merged.subarray(offset + 6, offset + 4 + length);
            frames.push({ type, payload: body.length ? JSON.parse(decoder.decode(body)) : {} });
            offset += 4 + length;
        }
        this.#buffer = merged.slice(offset);
        return frames;
    }
}
