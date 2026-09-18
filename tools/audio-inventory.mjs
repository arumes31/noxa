// Generate the review inventory from production registries and actual call sites.
import { readFileSync, writeFileSync } from "node:fs";
import { SOUND_DEFINITIONS } from "../client/frontend/src/sound-catalog.js";
import { SPEECH_ASSETS } from "../client/frontend/src/speech-catalog.js";
import { SPEECH_EVENTS } from "../client/frontend/src/speech-queue.js";

const sourceRoot = new URL("../client/frontend/src/", import.meta.url);
const files = ["main.js", "notifications.js", "chat-ui.js", "tabs.js"];
function callers(id) {
    const matches = [];
    for (const file of files) {
        readFileSync(new URL(file, sourceRoot), "utf8").split(/\r?\n/).forEach((line, index) => {
            if (line.includes('"'+id+'"')) matches.push(`${file}:${index+1}`);
        });
    }
    return matches.join(", ") || "Registry-selected alert or preview";
}
const table = (headers, rows) => [headers, headers.map(()=>"---"), ...rows].map(row => "| "+row.join(" | ")+" |").join("\n");
let report = "# noXa audio inventory\n\nGenerated with `node tools/audio-inventory.mjs`. Do not maintain a second hand-written asset inventory.\n\n";
report += "## Effects\n\nAll filenames are relative to `client/frontend/src/assets/sounds/`. Every design is a separate static WAV. Relative gain 1 preserves each file's authored level. Four system sources maximum, including speech; cooldowns are in milliseconds and scoped to tab/connection generation.\n\n";
report += table(["Event", "File", "Category", "ms", "Gain", "Priority", "Cooldown", "Concurrency", "Design", "Call sites"],
    Object.entries(SOUND_DEFINITIONS).map(([id,d])=>[id,id+".wav",d.category,Math.round(d.duration*1000),d.gain,d.priority,d.cooldown,d.concurrency,d.character,callers(id)]));
report += "\n\n## Fixed speech\n\nPaths are relative to `client/frontend/src/assets/speech/`. German uses the existing UI term ‘Channel’. The test recordings use the exact noXa test sentences from the brief. English is the deterministic choice for unsupported interface languages. Missing selected-language recordings are omitted; no runtime fallback exists. All genuine speech events default enabled, subject to master, category, event, matrix and speech gates. Test is preview-only. Cooldown: 10 seconds per event/tab/generation; queue expiry: 8 seconds.\n\n";
report += table(["ID", "English", "German", "Files (EN / DE)", "Duration EN / DE", "Priority", "Trigger"],
    Object.entries(SPEECH_EVENTS).map(([id,d])=>[id,SPEECH_ASSETS.en[id].transcript,SPEECH_ASSETS.de[id].transcript,
        `en/${id}.wav / de/${id}.wav`,`${SPEECH_ASSETS.en[id].duration.toFixed(3)} / ${SPEECH_ASSETS.de[id].duration.toFixed(3)} s`,d.priority,
        id==="test"?"Settings preview":id==="banned"?"Self kicked event with authoritative ban=true":id==="kicked"?"Self kicked event with from_server=true, ban=false":id==="kicked_channel"?"Self kicked event without server removal":callers(id)]));
report += "\n\n## Source semantics and gates\n\n";
report += "- Connection: successful connect/finalization; successful reconnect clears obsolete connection speech; intentional_disconnect is voluntary; disconnected with reconnect intent is loss. Retry exhaustion triggers reconnect_failed once. Reconnecting is one cue per retry series, not a loop. Session ownership checks guard asynchronous connection results.\n";
report += "- Own channel: joined/switch/leave transitions and live self user_moved. Forced move requires a different authoritative by_client_id and a different positive destination. Initial tab replay stays silent.\n";
report += "- Other users: user_joined/user_left/user_moved in the active channel. The join_leave matrix row gates specific movement cues; the generic cue is not stacked.\n";
report += "- Controls: setPTT applies native microphone/voice state before scheduling the cue; mic/deafen setters emit feedback on the actual state change. VAD does not trigger PTT clicks.\n";
report += "- Messaging: notify dispatch handles mentions, keywords, DMs, channel messages, incoming whisper/poke and announcements. checkBuddyOnline and checkChannelWatch provide watch events. Master, DND, per-event, channel overrides and notification matrix remain in that path. Native Windows notifications are silent.\n";
report += "- Kick/ban: authoritative self-removal cancels reconnect intent; reasons stay in the toast/live region. Observed removal of another participant has an effect only. All self-removal sound uses the kick notification-matrix row.\n";
report += "- Permission: only user-visible servererror messages with explicit permission-rejection prefixes qualify. Explicit server_shutdown frames are supported by this repository; generic loss never implies shutdown. No fake recording events are added.\n";
report += "- All production audio respects play_sounds, event_sounds, DND and history replay. effects_enabled controls effects independently; spoken_messages, speech_volume, speech_connection, speech_admin, speech_removal, speech_permissions and speech_events further control speech. SoundEngine and SpeechQueue use activeTabID/serverGeneration, with bounded per-scope dedup maps.\n";
writeFileSync(new URL("../docs/audio-inventory.md",import.meta.url),report+"\n");
console.log("Updated noXa audio inventory from registries and call sites.");
