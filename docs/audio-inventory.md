# noXa audio inventory

Generated with `node tools/audio-inventory.mjs`. Do not maintain a second hand-written asset inventory.

## Effects

All filenames are relative to `client/frontend/src/assets/sounds/`. The source catalog retains retired recordings; the application routes their IDs to fixed speech and excludes them from the effect bus and previews. Live effects: `ptt_on`, `ptt_off`, `mention`, `keyword`, `dm`, `channel_message`, `whisper`, `announcement`. Every design is a separate static WAV. Relative gain 1 preserves each file's authored level. Four system sources maximum, including speech; cooldowns are in milliseconds and scoped to tab/connection generation.

| Event | File | Runtime routing | Category | ms | Gain | Priority | Cooldown | Concurrency | Design | Call sites |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| connection_connected | connection_connected.wav | Speech: connection_connected | Connection | 260 | 1 | 2 | 100 | replace same event within session; priority admission | Firm control latch with a short paper settling texture | main.js:427, main.js:3104, tabs.js:23 |
| connection_reconnected | connection_reconnected.wav | Speech: connection_reconnected | Connection | 245 | 1 | 2 | 100 | replace same event within session; priority admission | Two dry contacts ending in a clear latch | main.js:641 |
| connection_disconnected | connection_disconnected.wav | Speech: connection_disconnected | Connection | 175 | 1 | 2 | 100 | replace same event within session; priority admission | Single calm dry control closure | main.js:1115 |
| connection_lost | connection_lost.wav | Speech: connection_lost | Connection | 255 | 1 | 3 | 100 | replace same event within session; priority admission | Brief paper interruption ending in a firm latch | main.js:986 |
| connection_reconnecting | connection_reconnecting.wav | Speech: connection_reconnecting | Connection | 105 | 1 | 1 | 5000 | replace same event within session; priority admission | Small mechanical detent | main.js:731, main.js:821 |
| connection_failed | connection_failed.wav | Speech: connection_failed | Connection | 280 | 1 | 3 | 100 | replace same event within session; priority admission | Two separated dry stops with a short paper edge | main.js:360, main.js:955, tabs.js:28 |
| server_error | server_error.wav | Speech: server_error | Connection | 190 | 1 | 3 | 100 | replace same event within session; priority admission | Compact crumpled-paper stop and button contact | main.js:1016 |
| own_channel_join | own_channel_join.wav | Speech: channel_join | Your channel | 275 | 1 | 2 | 100 | replace same event within session; priority admission | Signature control latch with a broad paper contact | main.js:1198, main.js:1348, main.js:1349 |
| own_channel_switch | own_channel_switch.wav | Speech: channel_join | Your channel | 205 | 1 | 2 | 100 | replace same event within session; priority admission | Crisp latch followed by a dry paper landing | main.js:1198, main.js:1348, main.js:1349 |
| own_channel_leave | own_channel_leave.wav | Speech: channel_leave | Your channel | 165 | 1 | 2 | 100 | replace same event within session; priority admission | Calm control release with a brief paper closure | Registry-selected alert or preview |
| user_join | user_join.wav | Speech: user_join | Other users | 100 | 1 | 0 | 180 | coalesce movement | Small clear dry tap | main.js:1301, notifications.js:127 |
| user_leave | user_leave.wav | Speech: user_leave | Other users | 95 | 1 | 0 | 180 | coalesce movement | Quiet paper contact with a short soft tail | main.js:1322, main.js:1379, notifications.js:127 |
| user_move_in | user_move_in.wav | Speech: user_join | Other users | 120 | 1 | 0 | 180 | coalesce movement | Light grainy control contact | main.js:1373 |
| user_move_out | user_move_out.wav | Speech: user_moved | Other users | 100 | 1 | 0 | 180 | coalesce movement | Quiet compact button release | main.js:1379 |
| mic_on | mic_on.wav | Speech: microphone_unmuted | Voice controls | 130 | 1 | 1 | 100 | replace same event within session; priority admission | Mechanical switch engagement | main.js:2835 |
| mic_off | mic_off.wav | Speech: microphone_muted | Voice controls | 140 | 1 | 1 | 100 | replace same event within session; priority admission | Darker mechanical release | main.js:2834 |
| deafen_on | deafen_on.wav | Speech: sound_muted | Voice controls | 140 | 1 | 1 | 100 | replace same event within session; priority admission | Muted double-action control closure | main.js:2854 |
| deafen_off | deafen_off.wav | Speech: sound_unmuted | Voice controls | 125 | 1 | 1 | 100 | replace same event within session; priority admission | Clearer control-surface release | main.js:2854 |
| ptt_on | ptt_on.wav | Effect | Voice controls | 35 | 1 | 1 | 0 | replace PTT | Tiny dry button engagement | main.js:2806 |
| ptt_off | ptt_off.wav | Effect | Voice controls | 25 | 1 | 1 | 0 | replace PTT | Tiny dry contact release | main.js:2806 |
| mention | mention.wav | Effect | Notifications | 245 | 1 | 2 | 100 | replace same event within session; priority admission | Distinct paper flick followed by a clear button contact | notifications.js:17, notifications.js:43, chat-ui.js:2275 |
| keyword | keyword.wav | Effect | Notifications | 180 | 1 | 1 | 100 | replace same event within session; priority admission | Short grainy paper contact | notifications.js:18, chat-ui.js:2262 |
| dm | dm.wav | Effect | Notifications | 200 | 1 | 1 | 100 | replace same event within session; priority admission | Broad paper landing with a small dry leading click | main.js:1766, main.js:1771, notifications.js:19, notifications.js:43, chat-ui.js:64, chat-ui.js:172, chat-ui.js:196, chat-ui.js:301, chat-ui.js:795, chat-ui.js:806, chat-ui.js:1415, chat-ui.js:1496, chat-ui.js:1501, chat-ui.js:1717, chat-ui.js:1795, chat-ui.js:1806, chat-ui.js:1954, chat-ui.js:2056, chat-ui.js:2204, chat-ui.js:2366, chat-ui.js:2368, chat-ui.js:2381, chat-ui.js:2383, chat-ui.js:2384, chat-ui.js:2413, chat-ui.js:2415, chat-ui.js:2426, chat-ui.js:2451, chat-ui.js:2568, chat-ui.js:3149, chat-ui.js:3331, chat-ui.js:3354, chat-ui.js:3456, chat-ui.js:3471, chat-ui.js:3484, chat-ui.js:3567, chat-ui.js:3576, chat-ui.js:3599, chat-ui.js:4083, chat-ui.js:4158, chat-ui.js:4239, chat-ui.js:4377, chat-ui.js:4385 |
| channel_message | channel_message.wav | Effect | Notifications | 80 | 1 | 0 | 250 | replace same event within session; priority admission | Tiny clean dry button contact | notifications.js:20, chat-ui.js:2249, chat-ui.js:2275, chat-ui.js:2284 |
| whisper | whisper.wav | Effect | Notifications | 155 | 1 | 2 | 100 | replace same event within session; priority admission | Soft paper texture ending in a light click | main.js:1494, main.js:1685, notifications.js:21, notifications.js:43 |
| poke | poke.wav | Speech: poke | Notifications | 600 | 1 | 2 | 100 | replace same event within session; priority admission | David says Wake up! without a trailing beep | main.js:1485, main.js:1487, notifications.js:22, notifications.js:43, notifications.js:102 |
| join_leave | join_leave.wav | Speech: user_join | Notifications | 90 | 1 | 0 | 250 | replace same event within session; priority admission | Quiet short contact for the compatibility event | main.js:1146, main.js:1299, main.js:1320, main.js:1371, main.js:1377, notifications.js:23, notifications.js:44 |
| buddy_online | buddy_online.wav | Speech: buddy_online | Notifications | 200 | 1 | 1 | 100 | replace same event within session; priority admission | Compact control contact with a soft paper edge | notifications.js:24, notifications.js:44, notifications.js:151 |
| kick | kick.wav | Speech: kicked | Notifications | 210 | 1 | 3 | 100 | replace same event within session; priority admission | Firm compact mechanical stop | main.js:1587, main.js:1588, notifications.js:25 |
| ban | ban.wav | Speech: banned | Notifications | 290 | 1 | 4 | 100 | replace same event within session; priority admission | Two separated dry stops with a brief crumpled-paper finish | main.js:1588 |
| announcement | announcement.wav | Effect | Notifications | 250 | 1 | 3 | 100 | replace same event within session; priority admission | Full paper contact ending in a crisp button closure | main.js:1573, notifications.js:26, chat-ui.js:3715 |
| channel_watch | channel_watch.wav | Speech: channel_watch | Notifications | 130 | 1 | 0 | 250 | replace same event within session; priority admission | Quiet short textured button pair | notifications.js:27, notifications.js:202 |
| stream_watch_started | stream_watch_started.wav | Speech: stream_watch_started | Notifications | 185 | 1 | 1 | 250 | replace same event within session; priority admission | Soft rising pair of short textured contacts | main.js:1603, main.js:1604 |

## Fixed speech

Paths are relative to `client/frontend/src/assets/speech/`. All German speech uses the selected synthetic AN06 reference. German uses the existing UI term ‘Channel’. The test recordings use the exact noXa test sentences from the brief. English is the deterministic choice for unsupported interface languages. Missing selected-language recordings are omitted; no runtime fallback exists. All genuine speech events default enabled, subject to master, category, event, matrix and speech gates. Test is preview-only. Cooldowns below are per event/tab/generation; queue expiry: 8 seconds. Reconnecting additionally plays once per outage.

| ID | English | German | Files (EN / DE) | Duration EN / DE | Priority | Cooldown ms | Trigger |
| --- | --- | --- | --- | --- | --- | --- | --- |
| microphone_muted | Microphone muted. | Mikrofon stummgeschaltet. | en/microphone_muted.wav / de/microphone_muted.wav | 1.138 / 2.320 s | 3 | 0 | Registry-selected alert or preview |
| microphone_unmuted | Microphone activated. | Mikrofon aktiviert. | en/microphone_unmuted.wav / de/microphone_unmuted.wav | 1.440 / 1.407 s | 3 | 0 | Registry-selected alert or preview |
| sound_muted | Sound muted. | Ton stummgeschaltet. | en/sound_muted.wav / de/sound_muted.wav | 1.057 / 1.201 s | 3 | 0 | Registry-selected alert or preview |
| sound_unmuted | Sound activated. | Ton aktiviert. | en/sound_unmuted.wav / de/sound_unmuted.wav | 1.370 / 1.201 s | 3 | 0 | Registry-selected alert or preview |
| client_closing | Closing. Good bye. | Wird geschlossen. Auf Wiedersehen. | en/client_closing.wav / de/client_closing.wav | 1.370 / 2.809 s | 8 | 10000 | Registry-selected alert or preview |
| banned | You were banned from the server. | Du wurdest vom Server gebannt. | en/banned.wav / de/banned.wav | 1.672 / 1.642 s | 7 | 10000 | Self kicked event with authoritative ban=true |
| kicked | You were kicked from the server. | Du wurdest vom Server entfernt. | en/kicked.wav / de/kicked.wav | 1.544 / 1.927 s | 6 | 10000 | Self kicked event with from_server=true, ban=false |
| kicked_channel | You were kicked from the channel. | Du wurdest aus dem Channel entfernt. | en/kicked_channel.wav / de/kicked_channel.wav | 1.440 / 2.220 s | 6 | 10000 | Self kicked event without server removal |
| server_shutdown | The server is shutting down. | Der Server wird heruntergefahren. | en/server_shutdown.wav / de/server_shutdown.wav | 1.892 / 1.861 s | 6 | 10000 | main.js:1576, main.js:1578 |
| reconnect_failed | Unable to reconnect to the server. | Die Verbindung zum Server konnte nicht wiederhergestellt werden. | en/reconnect_failed.wav / de/reconnect_failed.wav | 2.415 / 3.440 s | 6 | 10000 | main.js:831 |
| connection_lost | Connection to the server was lost. | Die Verbindung zum Server wurde unterbrochen. | en/connection_lost.wav / de/connection_lost.wav | 2.218 / 2.221 s | 5 | 10000 | main.js:986 |
| connection_reconnected | Reconnected to the server. | Verbindung zum Server wiederhergestellt. | en/connection_reconnected.wav / de/connection_reconnected.wav | 1.591 / 3.012 s | 3 | 1000 | main.js:641 |
| connection_connected | Connected to the server. | Mit dem Server verbunden. | en/connection_connected.wav / de/connection_connected.wav | 1.358 / 1.993 s | 2 | 100 | main.js:427, main.js:3104, tabs.js:23 |
| connection_disconnected | Disconnected from the server. | Vom Server getrennt. | en/connection_disconnected.wav / de/connection_disconnected.wav | 1.765 / 1.513 s | 2 | 100 | main.js:1115 |
| connection_reconnecting | Reconnecting to the server. | Verbindung zum Server wird wiederhergestellt. | en/connection_reconnecting.wav / de/connection_reconnecting.wav | 1.591 / 2.878 s | 1 | 5000 | main.js:731, main.js:821 |
| connection_failed | Connection to the server failed. | Verbindung zum Server fehlgeschlagen. | en/connection_failed.wav / de/connection_failed.wav | 1.881 / 2.516 s | 3 | 3000 | main.js:360, main.js:955, tabs.js:28 |
| disconnect_failed | Disconnecting from the server failed. | Trennen der Verbindung fehlgeschlagen. | en/disconnect_failed.wav / de/disconnect_failed.wav | 2.113 / 2.576 s | 3 | 3000 | main.js:955 |
| server_error | The server action failed. | Die Serveraktion ist fehlgeschlagen. | en/server_error.wav / de/server_error.wav | 1.904 / 1.979 s | 3 | 3000 | main.js:1016 |
| moved_by_admin | You were moved. | Du wurdest verschoben. | en/moved_by_admin.wav / de/moved_by_admin.wav | 0.952 / 1.315 s | 4 | 10000 | main.js:1348 |
| permission_denied | You do not have permission to perform this action. | Du hast keine Berechtigung für diese Aktion. | en/permission_denied.wav / de/permission_denied.wav | 2.856 / 3.032 s | 4 | 10000 | main.js:1015 |
| user_kicked | User was kicked from the server. | Ein Benutzer wurde vom Server entfernt. | en/user_kicked.wav / de/user_kicked.wav | 1.765 / 2.380 s | 3 | 1000 | main.js:1589 |
| user_kicked_channel | User was kicked from the channel. | Ein Benutzer wurde aus dem Channel entfernt. | en/user_kicked_channel.wav / de/user_kicked_channel.wav | 1.730 / 2.452 s | 3 | 1000 | main.js:1589 |
| channel_join | Channel joined. | Channel betreten. | en/channel_join.wav / de/channel_join.wav | 1.173 / 1.106 s | 2 | 1000 | main.js:1184, main.js:1198, main.js:1349 |
| channel_leave | Voice channel left. | Channel verlassen. | en/channel_leave.wav / de/channel_leave.wav | 1.382 / 0.967 s | 2 | 1000 | main.js:1201, main.js:1352, main.js:1430 |
| user_moved_out | User was moved from your channel. | Ein Benutzer wurde aus deinem Channel verschoben. | en/user_moved_out.wav / de/user_moved_out.wav | 1.892 / 2.820 s | 2 | 1000 | main.js:1379 |
| user_join | User joined your channel. | Benutzer ist dem Channel beigetreten. | en/user_join.wav / de/user_join.wav | 1.451 / 2.291 s | 1 | 1000 | main.js:1301, notifications.js:127 |
| user_leave | User left your channel. | Ein Benutzer hat deinen Channel verlassen. | en/user_leave.wav / de/user_leave.wav | 1.405 / 2.456 s | 1 | 1000 | main.js:1322, main.js:1379, notifications.js:127 |
| user_disconnected | User disconnected. | Ein Benutzer hat die Verbindung getrennt. | en/user_disconnected.wav / de/user_disconnected.wav | 1.254 / 2.471 s | 1 | 1000 | main.js:1322 |
| user_moved | User moved. | Ein Benutzer hat den Channel gewechselt. | en/user_moved.wav / de/user_moved.wav | 1.103 / 2.791 s | 1 | 1000 | main.js:1329, main.js:1379 |
| poke | You were poked. | Du wurdest angestupst. | en/poke.wav / de/poke.wav | 0.801 / 1.482 s | 2 | 5000 | main.js:1485, main.js:1487, notifications.js:22, notifications.js:43, notifications.js:102 |
| buddy_online | A watched contact is online. | Ein beobachteter Kontakt ist online. | en/buddy_online.wav / de/buddy_online.wav | 1.950 / 2.486 s | 1 | 5000 | notifications.js:24, notifications.js:44, notifications.js:151 |
| channel_watch | The requested channel user count has been reached. | Die gewünschte Benutzerzahl im Channel ist erreicht. | en/channel_watch.wav / de/channel_watch.wav | 3.402 / 3.092 s | 0 | 10000 | notifications.js:27, notifications.js:202 |
| stream_watch_started | Someone is watching your stream. | Jemand schaut deinem Stream zu. | en/stream_watch_started.wav / de/stream_watch_started.wav | 1.904 / 1.680 s | 1 | 3000 | main.js:1603, main.js:1604 |
| test | This is a noXa spoken notification. | Dies ist eine gesprochene noXa-Benachrichtigung. | en/test.wav / de/test.wav | 2.496 / 2.845 s | 4 | 10000 | Settings preview |

## Source semantics and gates

- Connection: successful connect/finalization; successful reconnect clears obsolete connection speech; intentional_disconnect is voluntary; disconnected with reconnect intent is loss. Retry exhaustion triggers reconnect_failed once. Reconnecting is one cue per retry series, not a loop. Session ownership checks guard asynchronous connection results.
- Own channel: joined/switch/leave transitions and live self user_moved. Forced move requires a different authoritative by_client_id and a different positive destination. Initial tab replay stays silent.
- Other users: user_joined/user_left/user_moved in the active channel. A different authoritative by_client_id and positive destination select user_moved_out when a member is moved from your channel; voluntary changes retain join/leave speech. The join_leave matrix row gates specific movement cues; the generic cue is not stacked.
- Controls: setPTT applies native microphone/voice state before scheduling the cue; mic/deafen setters emit feedback on the actual state change. VAD does not trigger PTT clicks.
- Messaging: mentions, keywords, DMs, channel messages, whispers and announcements keep short effects. Poke, buddy-online, channel-watch and viewer-start events use fixed speech. Master, DND, per-event, channel overrides and notification matrix remain in their existing paths. Native Windows notifications are silent.
- Kick/ban: authoritative self-removal cancels reconnect intent; reasons stay in the toast/live region. Observed removal of another participant uses user_kicked or user_kicked_channel speech. All removal sound uses the kick notification-matrix row.
- Permission: only user-visible servererror messages with explicit permission-rejection prefixes qualify. Explicit server_shutdown frames are supported by this repository; generic loss never implies shutdown. No fake recording events are added.
- All production audio respects play_sounds, event_sounds, DND and history replay. effects_enabled controls effects independently; spoken_messages, speech_volume, speech_connection, speech_channel, speech_admin, speech_removal, speech_permissions and speech_events further control speech. SoundEngine and SpeechQueue use activeTabID/serverGeneration, with bounded per-scope dedup maps.

