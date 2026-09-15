// Original VOICX contact sounds, MIT (repository LICENSE). No sampled material.
// Run: node tools/generate-sounds.mjs. Fixed filters, no oscillators or pitch sweeps.
import { mkdirSync, writeFileSync } from "node:fs";
import assert from "node:assert/strict";
const root = new URL("../client/frontend/src/", import.meta.url);
const rate = 48000;
// Fixed-bandwidth noise contacts: broad-spectrum body without sustained pitch.
const contact = (start, length, cutoff, gain = 1, decay = 6, attack = 2) =>
    ({ start, length, cutoff, gain, decay, attack });
const groups = [
    ["Connection", [
        ["connection_connected", "Connected", "Soft console contact with a short full confirmation body", 210, 2, -36, [contact(0,205,1150,1,5,3), contact(8,100,470,.7,5,4)]],
        ["connection_reconnected", "Reconnected", "Shorter light contact with a compact settled body", 135, 2, -38, [contact(0,130,1150,1,7,2), contact(6,70,470,.5)]],
        ["connection_disconnected", "Disconnected", "Single damped closure with a rounded soft attack", 160, 2, -37, [contact(0,155,720,1,7,4)]],
        ["connection_lost", "Connection lost", "Interrupted coarse contact and a muted second stop", 225, 3, -33, [contact(0,95,1550,1,7,2), contact(100,120,570,.9,8,2)]],
        ["connection_reconnecting", "Reconnecting", "Quiet single neutral status contact", 110, 1, -44, [contact(0,105,850,1,6,3)]],
        ["connection_failed", "Connection failed", "Dry rejected contact with a dense low body", 180, 3, -33, [contact(0,175,750,1,9,1.7), contact(0,60,1700,.25,8)]],
        ["server_error", "Server action error", "Two compact dry refusal ticks", 145, 3, -35, [contact(0,55,1250,1,7,1.5), contact(65,75,1250,.65,9,1.5)]],
    ]],
    ["Your channel", [
        ["own_channel_join", "Joined channel", "Soft voice-path contact with a short rounded confirmation tail", 185, 2, -38, [contact(0,180,1350,1,6,2.5), contact(5,110,540,.7,5,3)]],
        ["own_channel_switch", "Switched channel", "Muted transition contact followed by a small firmer contact", 150, 2, -39, [contact(0,65,700,.8,7,2), contact(55,90,1450,1,7,2)]],
        ["own_channel_leave", "Left channel", "Single felt-damped closure with a short final decay", 120, 2, -39, [contact(0,115,630,1,9,3)]],
    ]],
    ["Other users", [
        ["user_join", "User joined", "Quiet clean presence tick", 90, 0, -43, [contact(0,85,1200,1,7,2)]],
        ["user_leave", "User left", "Related presence tick with a softer damped edge", 95, 0, -43, [contact(0,90,700,1,9,3.5)]],
        ["user_move_in", "User moved in", "Small paired contacts with a clean second edge", 130, 0, -43, [contact(0,48,750,.65,7,2), contact(42,83,1200,1,8,2)]],
        ["user_move_out", "User moved out", "Related paired contacts ending in a damped edge", 135, 0, -43, [contact(0,60,1200,.8,8,2), contact(52,78,650,1,9,3)]],
    ]],
    ["Voice controls", [
        ["mic_on", "Microphone on", "Small clean control-surface contact", 70, 1, -44, [contact(0,65,1450,1,8,1.5), contact(0,40,550,.4)]],
        ["mic_off", "Microphone off", "Muted control-surface release", 75, 1, -44, [contact(0,70,800,1,10,2.5), contact(0,40,550,.4)]],
        ["deafen_on", "Deafened", "Cushioned double contact with dark filtering", 120, 1, -42, [contact(0,55,620,.8,8,3), contact(48,67,620,1,10,3)]],
        ["deafen_off", "Undeafened", "Matching double contact with a clearer open edge", 125, 1, -42, [contact(0,55,1250,.8,8,2), contact(48,72,1250,1,7,2)]],
        ["ptt_on", "Push-to-talk on", "Tiny dry talkback contact", 28, 1, -49, [contact(0,28,1100,1,5,1.2)]],
        ["ptt_off", "Push-to-talk off", "Tiny damped talkback release", 34, 1, -49, [contact(0,34,650,1,7,1.8)]],
    ]],
    ["Notifications", [
        ["mention", "Mention", "Defined single desktop tap with a brief supporting body", 165, 2, -36, [contact(0,160,1750,1,7,1.5), contact(3,90,700,.6,6,2)]],
        ["keyword", "Keyword highlight", "Compact textured tap with a softened attack", 120, 1, -40, [contact(0,115,1550,1,8,3)]],
        ["dm", "Direct message", "Two close dry desk contacts", 185, 1, -40, [contact(0,70,1000,1,7,2), contact(85,95,1000,.85,7,2)]],
        ["channel_message", "Channel message", "Very quiet single muted tick", 85, 0, -45, [contact(0,80,1050,1,9,2.5)]],
        ["whisper", "Voice whisper", "Close dry soft contact and a tiny adjacent contact", 140, 2, -39, [contact(0,80,780,1,7,3), contact(32,103,780,.45,8,3)]],
        ["poke", "Poke", "One firm controlled physical tap", 115, 2, -36, [contact(0,110,1800,1,9,1.2), contact(0,65,500,.6,7,2)]],
        ["join_leave", "Join/leave (your channel)", "Small neutral presence contact", 100, 0, -44, [contact(0,95,950,1,8,3)]],
        ["buddy_online", "Watched contact online", "Warmer, slightly fuller presence contact", 150, 1, -41, [contact(0,145,1050,1,6,3), contact(0,100,430,.6,5,4)]],
        ["kick", "Kick/ban", "Firm low contact with a short abrupt cushioned stop", 155, 3, -33, [contact(0,150,1050,1,11,1.5), contact(0,85,430,.9,7,2)]],
        ["ban", "Banned", "Low dry stop followed by a subdued final contact", 220, 3, -33, [contact(0,105,800,1,9,2), contact(125,90,600,.7,10,2)]],
        ["announcement", "Announcement", "Broader clean contact with a brief dry body", 240, 3, -34, [contact(0,235,1400,1,5,3.5), contact(5,150,650,.65,4,4)]],
        ["channel_watch", "Channel watch", "Quiet separated pair of damped status ticks", 150, 0, -44, [contact(0,50,850,.7,8,2), contact(80,65,850,1,8,2)]],
    ]],
];

function render(id, duration, targetRmsDB, layers) {
    const samples = new Float64Array(Math.round(duration * rate));
    let seed = [...id].reduce((n, c) => (Math.imul(n, 31) + c.charCodeAt(0)) >>> 0, 913);
    const noise = () => { seed ^= seed << 13; seed ^= seed >>> 17; seed ^= seed << 5; return (seed >>> 0) / 2147483648 - 1; };
    for (const l of layers) {
        const start = Math.round(l.start * rate / 1000), count = Math.round(l.length * rate / 1000);
        const lowAlpha = 1 - Math.exp(-2 * Math.PI * l.cutoff / rate);
        const highAlpha = 1 - Math.exp(-2 * Math.PI * 180 / rate);
        let low1 = 0, low2 = 0, low3 = 0, high = 0;
        for (let j = 0; j < count && start + j < samples.length; j++) {
            low1 += (noise() - low1) * lowAlpha;
            low2 += (low1 - low2) * lowAlpha;
            low3 += (low2 - low3) * lowAlpha;
            high += (low3 - high) * highAlpha;
            const attack = Math.sin(Math.min(1, j / (rate * l.attack / 1000)) * Math.PI / 2) ** 2;
            const release = Math.sin(Math.min(1, (count - 1 - j) / (rate * .006)) * Math.PI / 2) ** 2;
            const envelope = attack * release * Math.exp(-l.decay * j / (count - 1));
            samples[start + j] += (low3 - high) * envelope * l.gain;
        }
    }
    // Smoothly round rare noise peaks before normalization, without hard clipping.
    const knee = 3.5 * Math.sqrt(samples.reduce((sum, value) => sum + value * value, 0) / samples.length);
    for (let i = 0; i < samples.length; i++) samples[i] = knee * Math.tanh(samples[i] / knee);
    // Correct DC within the contact, preserving silence and avoiding a long added tail.
    let sum = 0, weight = 0;
    for (const value of samples) { sum += value; weight += Math.abs(value); }
    let energy = 0, peak = 0;
    for (let i = 0; i < samples.length; i++) {
        samples[i] -= sum / weight * Math.abs(samples[i]);
        assert.ok(Number.isFinite(samples[i]), id + ": non-finite sample");
        energy += samples[i] ** 2; peak = Math.max(peak, Math.abs(samples[i]));
    }
    assert.ok(energy > 0 && peak > 0, id + ": empty sound");
    const gain = Math.min(10 ** (targetRmsDB / 20) / Math.sqrt(energy / samples.length), .115 / peak);
    const wav = Buffer.alloc(44 + samples.length * 2);
    wav.write("RIFF"); wav.writeUInt32LE(wav.length - 8, 4); wav.write("WAVEfmt ", 8);
    wav.writeUInt32LE(16, 16); wav.writeUInt16LE(1, 20); wav.writeUInt16LE(1, 22);
    wav.writeUInt32LE(rate, 24); wav.writeUInt32LE(rate * 2, 28); wav.writeUInt16LE(2, 32); wav.writeUInt16LE(16, 34);
    wav.write("data", 36); wav.writeUInt32LE(samples.length * 2, 40);
    let actualEnergy = 0, actualPeak = 0, actualSum = 0;
    for (let i = 0; i < samples.length; i++) {
        const pcm = Math.round(samples[i] * gain * 32767);
        assert.ok(Number.isFinite(pcm) && Math.abs(pcm) < 32767, id + ": invalid PCM");
        wav.writeInt16LE(pcm, 44 + i * 2);
        const value = pcm / 32768;
        actualEnergy += value ** 2; actualPeak = Math.max(actualPeak, Math.abs(value)); actualSum += value;
    }
    const rmsDB = 20 * Math.log10(Math.sqrt(actualEnergy / samples.length));
    assert.ok(actualPeak <= .1151 && Math.abs(actualSum / samples.length) < .0001, id + ": unsafe level/DC");
    assert.ok(rmsDB <= targetRmsDB + .1 && rmsDB >= targetRmsDB - 3, id + ": target RMS missed (" + rmsDB + ")");
    assert.equal(wav.readInt16LE(44), 0); assert.equal(wav.readInt16LE(wav.length - 2), 0);
    return { wav, peak: actualPeak, rmsDB, targetRmsDB };
}

mkdirSync(new URL("assets/sounds/", root), { recursive: true });
const definitions = {}, metrics = {}, urls = [];
for (const [category, entries] of groups) for (const [id, label, character, ms, priority, targetRmsDB, layers] of entries) {
    const duration = ms / 1000;
    assert.ok(ms >= 20 && ms <= 300, id + ": duration out of range");
    const result = render(id, duration, targetRmsDB, layers);
    writeFileSync(new URL(`assets/sounds/${id}.wav`, root), result.wav);
    definitions[id] = { label, category, character, duration, priority, cooldown: id === "connection_reconnecting" ? 5000 : category === "Other users" ? 180 : id.startsWith("ptt_") ? 0 : priority === 0 ? 250 : 100 };
    metrics[id] = { duration, peak: result.peak, rmsDB: result.rmsDB, targetRmsDB };
    urls.push(`    ${id}: new URL("./assets/sounds/${id}.wav", import.meta.url).href,`);
}
writeFileSync(new URL("sound-catalog.js", root), `// Generated by tools/generate-sounds.mjs; edit the authored recipes there.\nexport const SOUND_DEFINITIONS = ${JSON.stringify(definitions, null, 4)};\nexport const SOUND_URLS = {\n${urls.join("\n")}\n};\nexport const SOUND_EVENTS = Object.keys(SOUND_DEFINITIONS);\nexport const SOUND_EVENT_GROUPS = ${JSON.stringify(groups.map(([label, entries]) => ({ label, events: entries.map(([id, name]) => [id, name]) })), null, 4)};\n`);
writeFileSync(new URL("assets/sounds/metrics.json", root), JSON.stringify(metrics, null, 2) + "\n");
writeFileSync(new URL("assets/sounds/README.md", root), "# VOICX original sound set\n\n32 original dry contact sounds: fixed-filter noise impulses, muted taps and console clicks.\nNo oscillators, pitch sweeps, melodies, reverb or external samples. Mono PCM, 48 kHz/16 bit.\nMIT licensed under the repository LICENSE. Regenerate: `node tools/generate-sounds.mjs`.\n\nGeneration validates finite PCM, duration, DC, endpoints, RMS and peak limits.\nMetrics record measured whole-cue RMS (not integrated LUFS) and intended RMS.\nPTT is quietest; ordinary messages/presence stay below direct attention and warnings.\nMaximum asset peak: 0.115. Four voices at 200% sum to at most 0.92.\n\nHuman headphone/speaker listening is required to assess fatigue and semantic recognition;\nwaveform measurements and automated playback cannot certify those qualities.\n");
console.log(`Generated ${Object.keys(definitions).length} original VOICX contact cues.`);
