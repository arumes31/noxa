# noXa audio inventory

Generated with `node tools/audio-inventory.mjs`. Do not maintain a second hand-written asset inventory.

## Effects

All filenames are relative to `client/frontend/src/assets/sounds/`. Every design is a separate static WAV. Relative gain 1 preserves each file's authored level. Four system sources maximum, including speech; cooldowns are in milliseconds and scoped to tab/connection generation.

| Event | File | Category | ms | Gain | Priority | Cooldown | Concurrency | Design | Call sites |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| connection_connected | connection_connected.wav | Connection | 260 | 1 | 2 | 100 | replace same event within session; priority admission | Firm control latch with a short paper settling texture | main.js:362, main.js:3351, tabs.js:19 |
| connection_reconnected | connection_reconnected.wav | Connection | 245 | 1 | 2 | 100 | replace same event within session; priority admission | Two dry contacts ending in a clear latch | main.js:568 |
| connection_disconnected | connection_disconnected.wav | Connection | 175 | 1 | 2 | 100 | replace same event within session; priority admission | Single calm dry control closure | main.js:752 |
| connection_lost | connection_lost.wav | Connection | 255 | 1 | 3 | 100 | replace same event within session; priority admission | Brief paper interruption ending in a firm latch | main.js:758 |
| connection_reconnecting | connection_reconnecting.wav | Connection | 105 | 1 | 1 | 5000 | replace same event within session; priority admission | Small mechanical detent | main.js:657 |
| connection_failed | connection_failed.wav | Connection | 280 | 1 | 3 | 100 | replace same event within session; priority admission | Two separated dry stops with a short paper edge | main.js:290, main.js:740, tabs.js:24 |
| server_error | server_error.wav | Connection | 190 | 1 | 3 | 100 | replace same event within session; priority admission | Compact crumpled-paper stop and button contact | main.js:784 |
| own_channel_join | own_channel_join.wav | Your channel | 275 | 1 | 2 | 100 | replace same event within session; priority admission | Signature control latch with a broad paper contact | main.js:1021, main.js:1035, main.js:1196, main.js:1198 |
| own_channel_switch | own_channel_switch.wav | Your channel | 205 | 1 | 2 | 100 | replace same event within session; priority admission | Crisp latch followed by a dry paper landing | main.js:1034, main.js:1196, main.js:1197 |
| own_channel_leave | own_channel_leave.wav | Your channel | 165 | 1 | 2 | 100 | replace same event within session; priority admission | Calm control release with a brief paper closure | main.js:1038, main.js:1201, main.js:1277 |
| user_join | user_join.wav | Other users | 100 | 1 | 0 | 180 | coalesce movement | Small clear dry tap | main.js:1154 |
| user_leave | user_leave.wav | Other users | 95 | 1 | 0 | 180 | coalesce movement | Quiet paper contact with a short soft tail | main.js:1175 |
| user_move_in | user_move_in.wav | Other users | 120 | 1 | 0 | 180 | coalesce movement | Light grainy control contact | main.js:1222 |
| user_move_out | user_move_out.wav | Other users | 100 | 1 | 0 | 180 | coalesce movement | Quiet compact button release | main.js:1227 |
| mic_on | mic_on.wav | Voice controls | 130 | 1 | 1 | 100 | replace same event within session; priority admission | Mechanical switch engagement | main.js:3110 |
| mic_off | mic_off.wav | Voice controls | 140 | 1 | 1 | 100 | replace same event within session; priority admission | Darker mechanical release | main.js:3109 |
| deafen_on | deafen_on.wav | Voice controls | 140 | 1 | 1 | 100 | replace same event within session; priority admission | Muted double-action control closure | main.js:3121 |
| deafen_off | deafen_off.wav | Voice controls | 125 | 1 | 1 | 100 | replace same event within session; priority admission | Clearer control-surface release | main.js:3121 |
| ptt_on | ptt_on.wav | Voice controls | 35 | 1 | 1 | 0 | replace PTT | Tiny dry button engagement | main.js:3085 |
| ptt_off | ptt_off.wav | Voice controls | 25 | 1 | 1 | 0 | replace PTT | Tiny dry contact release | main.js:3085 |
| mention | mention.wav | Notifications | 245 | 1 | 2 | 100 | replace same event within session; priority admission | Distinct paper flick followed by a clear button contact | notifications.js:14, notifications.js:40, chat-ui.js:1722 |
| keyword | keyword.wav | Notifications | 180 | 1 | 1 | 100 | replace same event within session; priority admission | Short grainy paper contact | notifications.js:15, chat-ui.js:1709 |
| dm | dm.wav | Notifications | 200 | 1 | 1 | 100 | replace same event within session; priority admission | Broad paper landing with a small dry leading click | main.js:2013, main.js:2018, notifications.js:16, notifications.js:40, chat-ui.js:33, chat-ui.js:122, chat-ui.js:144, chat-ui.js:500, chat-ui.js:1126, chat-ui.js:1150, chat-ui.js:1155, chat-ui.js:1290, chat-ui.js:1306, chat-ui.js:1337, chat-ui.js:1474, chat-ui.js:1532, chat-ui.js:1653, chat-ui.js:1802, chat-ui.js:1811, chat-ui.js:1825, chat-ui.js:1827, chat-ui.js:1838, chat-ui.js:1857, chat-ui.js:1859, chat-ui.js:1865, chat-ui.js:1973, chat-ui.js:2484, chat-ui.js:2634, chat-ui.js:2761, chat-ui.js:2775, chat-ui.js:2788, chat-ui.js:2876, chat-ui.js:2892 |
| channel_message | channel_message.wav | Notifications | 80 | 1 | 0 | 250 | replace same event within session; priority admission | Tiny clean dry button contact | notifications.js:17, chat-ui.js:1696, chat-ui.js:1722, chat-ui.js:1731 |
| whisper | whisper.wav | Notifications | 155 | 1 | 2 | 100 | replace same event within session; priority admission | Soft paper texture ending in a light click | main.js:1343, main.js:1934, notifications.js:18, notifications.js:40 |
| poke | poke.wav | Notifications | 165 | 1 | 2 | 100 | replace same event within session; priority admission | Focused dry control snap | main.js:1334, main.js:1336, notifications.js:19, notifications.js:40 |
| join_leave | join_leave.wav | Notifications | 90 | 1 | 0 | 250 | replace same event within session; priority admission | Quiet short contact for the compatibility event | main.js:1152, main.js:1173, main.js:1220, main.js:1225, notifications.js:20, notifications.js:41 |
| buddy_online | buddy_online.wav | Notifications | 200 | 1 | 1 | 100 | replace same event within session; priority admission | Compact control contact with a soft paper edge | notifications.js:21, notifications.js:41, notifications.js:142 |
| kick | kick.wav | Notifications | 210 | 1 | 3 | 100 | replace same event within session; priority admission | Firm compact mechanical stop | main.js:1408, main.js:1409, notifications.js:22 |
| ban | ban.wav | Notifications | 290 | 1 | 4 | 100 | replace same event within session; priority admission | Two separated dry stops with a brief crumpled-paper finish | main.js:1409 |
| announcement | announcement.wav | Notifications | 250 | 1 | 3 | 100 | replace same event within session; priority admission | Full paper contact ending in a crisp button closure | main.js:1392, notifications.js:23, chat-ui.js:3007 |
| channel_watch | channel_watch.wav | Notifications | 130 | 1 | 0 | 250 | replace same event within session; priority admission | Quiet short textured button pair | notifications.js:24, notifications.js:193 |

## Fixed speech

Paths are relative to `client/frontend/src/assets/speech/`. German uses the existing UI term ‘Channel’. The test recordings use the exact noXa test sentences from the brief. English is the deterministic choice for unsupported interface languages. Missing selected-language recordings are omitted; no runtime fallback exists. All genuine speech events default enabled, subject to master, category, event, matrix and speech gates. Test is preview-only. Cooldown: 10 seconds per event/tab/generation; queue expiry: 8 seconds.

| ID | English | German | Files (EN / DE) | Duration EN / DE | Priority | Trigger |
| --- | --- | --- | --- | --- | --- | --- |
| banned | You were banned from the server. | Du wurdest vom Server gebannt. | en/banned.wav / de/banned.wav | 1.672 / 1.398 s | 7 | Self kicked event with authoritative ban=true |
| kicked | You were kicked from the server. | Du wurdest vom Server entfernt. | en/kicked.wav / de/kicked.wav | 1.544 / 1.617 s | 6 | Self kicked event with from_server=true, ban=false |
| kicked_channel | You were removed from the channel. | Du wurdest aus dem Channel entfernt. | en/kicked_channel.wav / de/kicked_channel.wav | 1.556 / 1.723 s | 6 | Self kicked event without server removal |
| server_shutdown | The server is shutting down. | Der Server wird heruntergefahren. | en/server_shutdown.wav / de/server_shutdown.wav | 1.892 / 1.610 s | 6 | main.js:1395, main.js:1399 |
| reconnect_failed | Unable to reconnect to the server. | Die Verbindung zum Server konnte nicht wiederhergestellt werden. | en/reconnect_failed.wav / de/reconnect_failed.wav | 2.415 / 2.911 s | 6 | main.js:640 |
| connection_lost | Connection to the server was lost. | Die Verbindung zum Server wurde unterbrochen. | en/connection_lost.wav / de/connection_lost.wav | 2.218 / 2.075 s | 5 | main.js:758 |
| moved_by_admin | You were moved to another channel. | Du wurdest in einen anderen Channel verschoben. | en/moved_by_admin.wav / de/moved_by_admin.wav | 1.858 / 2.145 s | 4 | main.js:1196 |
| permission_denied | You do not have permission to perform this action. | Du hast keine Berechtigung für diese Aktion. | en/permission_denied.wav / de/permission_denied.wav | 2.856 / 2.074 s | 4 | main.js:783 |
| test | This is a noXa spoken notification. | Dies ist eine gesprochene noXa-Benachrichtigung. | en/test.wav / de/test.wav | 2.496 / 2.500 s | 4 | Settings preview |

## Source semantics and gates

- Connection: successful connect/finalization; successful reconnect clears obsolete connection speech; intentional_disconnect is voluntary; disconnected with reconnect intent is loss. Retry exhaustion triggers reconnect_failed once. Reconnecting is one cue per retry series, not a loop. Session ownership checks guard asynchronous connection results.
- Own channel: joined/switch/leave transitions and live self user_moved. Forced move requires a different authoritative by_client_id and a different positive destination. Initial tab replay stays silent.
- Other users: user_joined/user_left/user_moved in the active channel. The join_leave matrix row gates specific movement cues; the generic cue is not stacked.
- Controls: setPTT applies native microphone/voice state before scheduling the cue; mic/deafen setters emit feedback on the actual state change. VAD does not trigger PTT clicks.
- Messaging: notify dispatch handles mentions, keywords, DMs, channel messages, incoming whisper/poke and announcements. checkBuddyOnline and checkChannelWatch provide watch events. Master, DND, per-event, channel overrides and notification matrix remain in that path. Native Windows notifications are silent.
- Kick/ban: authoritative self-removal cancels reconnect intent; reasons stay in the toast/live region. Observed removal of another participant has an effect only. All self-removal sound uses the kick notification-matrix row.
- Permission: only user-visible servererror messages with explicit permission-rejection prefixes qualify. Explicit server_shutdown frames are supported by this repository; generic loss never implies shutdown. No fake recording events are added.
- All production audio respects play_sounds, event_sounds, DND and history replay. effects_enabled controls effects independently; spoken_messages, speech_volume, speech_connection, speech_admin, speech_removal, speech_permissions and speech_events further control speech. SoundEngine and SpeechQueue use activeTabID/serverGeneration, with bounded per-scope dedup maps.

