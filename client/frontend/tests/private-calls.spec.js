import { expect, test } from "./fixtures.js";

test.use({ launchOptions: { args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream", "--autoplay-policy=no-user-gesture-required"] }, permissions: ["microphone", "camera"] });

for (const signalingMode of ["normal", "gathering", "candidates-first", "group-media", "capture-recovery"]) {
test(`real peer call captures only after acceptance and tears down without joining a channel${signalingMode === "normal" ? "" : ` (${signalingMode})`}`, async ({ newIsolatedPage }) => {
    test.setTimeout(60000);
    const pages = new Map();
    const heldDescriptions = new Map();
    const candidateBatches = [];
    const participantIDs = signalingMode === "group-media" ? ["alice", "bob", "charlie"] : ["alice", "bob"];
    let call = null;
    let revision = 0;
    const notify = () => { for (const page of pages.values()) void page.evaluate(id => window.__callsModule.privateCallChanged({ id }), call.id).catch(() => {}); };
    for (const uid of participantIDs) {
        const page = await newIsolatedPage({ permissions: ["microphone", "camera"] }); pages.set(uid, page);
        await page.route("**/__call_test__", route => route.fulfill({ contentType: "text/html", body: '<!doctype html><title>Call test</title><link rel="stylesheet" href="/src/private-calls.css">' }));
        await page.exposeFunction("requestCall", async request => {
            if (request.action === "start") call = { id: "call-1", caller: uid, revision: ++revision, created_at: Math.floor(Date.now()/1000), ring_until: Math.floor(Date.now()/1000)+30, ended_at: 0, participants: participantIDs.map(id => ({ unique_id: id, client_id: id, state: id === uid ? "accepted" : "ringing" })) };
            if (request.action === "accept") { call.participants.find(participant => participant.unique_id === uid).state = "accepted"; call.revision = ++revision; }
            if (request.action === "start" || request.action === "accept") setTimeout(notify, 0);
            return { action: request.action, call: structuredClone(call) };
        });
        await page.exposeFunction("relayDescription", async (id, target, type, sdp) => {
            const targetPage = pages.get(target);
            // Force trickle candidates to carry connectivity in both regression
            // modes, even if a fast local host candidate reached the SDP first.
            if (signalingMode !== "normal" && type !== "candidates") sdp = sdp.split(/\r?\n/).filter(line => !line.startsWith("a=candidate:") && line !== "a=end-of-candidates").join("\r\n");
            const signal = { call_id: id, from: uid, to: target, description: { call_id: id, from: uid, to: target, type, sdp } };
            const key = `${uid}:${target}`;
            if (signalingMode === "candidates-first" && type !== "candidates") { heldDescriptions.set(key, signal); return; }
            if (type === "candidates") candidateBatches.push({ from: uid, to: target, candidates: JSON.parse(sdp) });
            if (signalingMode === "candidates-first" && heldDescriptions.has(key)) {
                // Deliver and finish handling the candidate batch before the
                // encrypted offer/answer, exercising the remote ICE queue.
                await targetPage.evaluate(value => window.__callsModule.privateCallSignal(value), signal);
                const description = heldDescriptions.get(key); heldDescriptions.delete(key);
                void targetPage.evaluate(value => window.__callsModule.privateCallSignal(value), description).catch(() => {});
                return;
            }
            void targetPage.evaluate(value => window.__callsModule.privateCallSignal(value), signal).catch(() => {});
        });
        await page.exposeFunction("stopCall", async () => { call.ended_at = Math.floor(Date.now()/1000); call.revision = ++revision; setTimeout(notify, 0); });
        await page.goto("http://127.0.0.1:12364/__call_test__");
        await page.evaluate(async ({ uid, signalingMode, participantIDs }) => {
            window.__captures = 0; window.__streams = []; window.__peers = []; window.__channelMoves = 0; window.__warnings = []; window.__hostCandidates = 0; window.__callGains = [];
            const createGain = AudioContext.prototype.createGain;
            AudioContext.prototype.createGain = function () { const gain = createGain.call(this); window.__callGains.push(gain); return gain; };
            const getMedia = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
            navigator.mediaDevices.getUserMedia = async constraints => {
                window.__captures++;
                if (signalingMode === "capture-recovery" && window.__captures === 1) throw new DOMException("Microphone temporarily unavailable", "NotReadableError");
                const stream = await getMedia(constraints); window.__streams.push(stream); return stream;
            };
            const OriginalPeer = window.RTCPeerConnection;
            window.RTCPeerConnection = class extends OriginalPeer {
                constructor(...args) {
                    super(...args); window.__peers.push(this);
                    this.addEventListener("icecandidate", event => { if (event.candidate) window.__hostCandidates++; });
                    if (signalingMode === "gathering") Object.defineProperty(this, "iceGatheringState", { get: () => "gathering" });
                }
            };
            window.__noxa = { state: { activeTabID: "server-a", serverGeneration: 1, myUniqueID: "local-device-key", myClientID: uid, myChannelID: 0, muted: false, deafened: false, settings: { activation_mode: "continuous", blocked_users: [] }, clients: participantIDs.map(id => ({ client_id: id, unique_id: id, nickname: id[0].toUpperCase() + id.slice(1) })) }, toast: message => window.__warnings.push(message), resetVoiceSession() {} };
            window.go = { main: { App: {
                PrivateCallForTab: (_tab, request) => window.requestCall(request),
                GetICEServersForTab: async () => [],
                SendPrivateCallDescriptionForTab: (_tab, id, target, type, sdp) => window.relayDescription(id, target, type, sdp),
                OpenPrivateCallDescriptionForTab: async (_tab, signal) => signal.description,
                StopPrivateCallForTab: () => window.stopCall(),
                JoinChannelForTab: async () => { window.__channelMoves++; return ""; },
                SaveSettings: async value => { window.__savedSettings = structuredClone(value); return ""; },
                GetSettings: async () => structuredClone(window.__savedSettings),
            } } };
            window.__callsModule = await import("/src/private-calls.js");
        }, { uid, signalingMode, participantIDs });
    }
    const alice = pages.get("alice"), bob = pages.get("bob");
    try {
        await alice.evaluate(() => window.__callsModule.startPrivateCall("bob"));
        await expect(bob.getByRole("button", { name: "Accept", exact: true })).toBeVisible();
        expect(await alice.evaluate(() => window.__captures)).toBe(0);
        expect(await bob.evaluate(() => window.__captures)).toBe(0);
        await bob.getByRole("button", { name: "Accept", exact: true }).click();
        if (pages.has("charlie")) {
            await pages.get("charlie").getByRole("button", { name: "Accept", exact: true }).click();
            await pages.get("charlie").getByRole("button", { name: "Mute microphone", exact: true }).click();
        }
        for (const page of pages.values()) {
            await expect.poll(() => page.evaluate(() => window.__peers.map(peer => peer.connectionState)), { timeout: 15000 }).toEqual(participantIDs.slice(1).map(() => "connected"));
            expect(await page.evaluate(() => window.__captures)).toBe(1);
            expect(await page.evaluate(() => window.__channelMoves)).toBe(0);
            if (signalingMode !== "normal") {
                expect(await page.evaluate(() => window.__hostCandidates)).toBeGreaterThan(0);
                if (signalingMode === "gathering") expect(await page.evaluate(() => window.__peers[0].iceGatheringState)).toBe("gathering");
            }
            if (signalingMode === "capture-recovery") {
                await page.evaluate(() => window.__callsModule.applyPrivateCallAudioSettings());
                expect(await page.evaluate(() => window.__captures)).toBe(2);
                expect(await page.evaluate(() => window.__peers[0].getSenders().some(sender => sender.track === window.__streams[0].getAudioTracks()[0]))).toBe(true);
            }
        }
        for (const page of pages.values()) {
            await expect.poll(() => page.evaluate(async () => {
                const stats = await window.__peers[0].getStats();
                return [...stats.values()].filter(report => report.type === "inbound-rtp" && report.kind === "audio").reduce((sum, report) => sum + (report.bytesReceived || 0), 0);
            })).toBeGreaterThan(0);
        }
        if (signalingMode !== "normal") {
            expect(candidateBatches.some(batch => batch.from === "alice" && batch.candidates.length > 0)).toBe(true);
            expect(candidateBatches.some(batch => batch.from === "bob" && batch.candidates.length > 0)).toBe(true);
            expect(heldDescriptions.size).toBe(0);
        }
        // Read the actual processed playback stream, not just gain settings or
        // network counters: the receive graph must deliver decoded samples.
        // Chrome's fake microphone has long silent intervals. Send a steady
        // tone over the real peer connection for deterministic level checks.
        await alice.evaluate(async () => {
            const context = new AudioContext(); await context.resume();
            const tone = context.createOscillator(), gain = context.createGain(), output = context.createMediaStreamDestination();
            tone.frequency.value = 440; gain.gain.value = 0.1;
            tone.connect(gain); gain.connect(output); tone.start();
            const sender = window.__peers[0].getSenders().find(sender => sender.track?.kind === "audio");
            window.__testTone = { context, tone, output, sender, original: sender.track };
            await sender.replaceTrack(output.stream.getAudioTracks()[0]);
        });
        await bob.evaluate(async () => {
            const context = new AudioContext(); await context.resume();
            const source = context.createMediaStreamSource(document.querySelector("audio").srcObject);
            const analyser = context.createAnalyser(); source.connect(analyser);
            window.__playbackMeter = { context, source, analyser };
        });
        const playbackLevel = () => bob.evaluate(() => {
            const analyser = window.__playbackMeter.analyser;
            const samples = new Float32Array(analyser.fftSize); analyser.getFloatTimeDomainData(samples);
            return Math.sqrt(samples.reduce((sum, sample) => sum + sample * sample, 0) / samples.length);
        });
        await expect.poll(playbackLevel).toBeGreaterThan(0.001);
        // Playback settings must apply to an existing private call without
        // replacing the peer, including master mute and per-user attenuation.
        for (const [master, user, expected] of [[0, 100, 0], [50, 50, 0.25], [100, 200, 2], [200, 200, 4], [100, 100, 1]]) {
            await bob.evaluate(({ master, user }) => {
                window.__noxa.state.settings.volume = master;
                window.__noxa.state.settings.user_volumes = { alice: user };
            }, { master, user });
            await expect.poll(() => bob.evaluate(() => document.querySelector("audio").volume * window.__callGains[0].gain.value)).toBe(expected);
            if (expected === 0) await expect.poll(playbackLevel).toBeLessThan(0.00001);
            else await expect.poll(async () => Math.abs(await playbackLevel() - 0.0707 * expected)).toBeLessThan(0.015);
        }
        await bob.evaluate(() => window.__playbackMeter.context.close());
        await alice.evaluate(async () => {
            const test = window.__testTone; await test.sender.replaceTrack(test.original);
            test.tone.stop(); test.output.stream.getTracks().forEach(track => track.stop()); await test.context.close();
        });
        if (["normal", "group-media"].includes(signalingMode)) {
            await alice.getByRole("button", { name: "Mute microphone", exact: true }).click();
            await alice.evaluate(async () => {
                window.__previousCallMic = window.__streams[0].getAudioTracks()[0];
                window.__noxa.state.settings.noise_suppression = false;
                await window.__callsModule.applyPrivateCallAudioSettings();
            });
            expect(await alice.evaluate(() => window.__captures)).toBe(2);
            expect(await alice.evaluate(() => window.__previousCallMic.readyState)).toBe("ended");
            expect(await alice.evaluate(() => window.__peers.every(peer => peer.connectionState === "connected" && peer.getSenders().some(sender => sender.track === window.__streams[1].getAudioTracks()[0] && !sender.track.enabled)))).toBe(true);
            await alice.getByRole("button", { name: "Unmute microphone", exact: true }).click();
            await expect.poll(() => alice.evaluate(() => window.__streams[1].getAudioTracks()[0].enabled)).toBe(true);
            await alice.getByRole("button", { name: "Mute call audio", exact: true }).click();
            await expect.poll(() => alice.evaluate(() => window.__streams[1].getAudioTracks()[0].enabled)).toBe(false);
            await alice.getByRole("button", { name: "Unmute call audio", exact: true }).click();
            await expect.poll(() => alice.evaluate(() => window.__streams[1].getAudioTracks()[0].enabled)).toBe(true);
            await alice.getByRole("button", { name: "Mute microphone", exact: true }).click();
            await alice.getByRole("button", { name: "Mute call audio", exact: true }).click();
            await alice.getByRole("button", { name: "Unmute call audio", exact: true }).click();
            expect(await alice.evaluate(() => window.__streams[1].getAudioTracks()[0].enabled)).toBe(false);
            await alice.getByRole("button", { name: "Unmute microphone", exact: true }).click();
            await alice.evaluate(() => { window.__noxa.state.deafened = true; });
            await expect.poll(() => alice.evaluate(() => window.__streams[1].getAudioTracks()[0].enabled)).toBe(false);
            await alice.evaluate(() => { window.__noxa.state.deafened = false; });
            await expect.poll(() => alice.evaluate(() => window.__streams[1].getAudioTracks()[0].enabled)).toBe(true);
        }
        if (["normal", "group-media"].includes(signalingMode)) {
            // Start camera from both negotiation roles after audio connected.
            for (const page of pages.values()) {
                await expect(page.getByRole("button", { name: "Start camera", exact: true })).toBeVisible();
                await page.getByRole("button", { name: "Start camera", exact: true }).click();
            }
            for (const page of pages.values()) {
                await expect(page.locator('.call-media-tile[data-local="false"][data-source="camera"] video')).toHaveCount(participantIDs.length - 1);
                await expect.poll(() => page.evaluate(() => [...document.querySelectorAll('.call-media-tile[data-local="false"] video')].every(video => video.videoWidth > 0))).toBe(true);
            }
            await alice.evaluate(() => {
                navigator.mediaDevices.getDisplayMedia = async options => {
                    window.__displayOptions = options;
                    const stream = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
                    window.__displayStream = stream;
                    return stream;
                };
            });
            await alice.getByRole("combobox", { name: "Share audio", exact: true }).selectOption("system");
            await alice.getByRole("button", { name: "Share screen", exact: true }).click();
            await expect(bob.locator('.call-media-tile[data-local="false"][data-source="screen"] video')).toBeVisible();
            await expect.poll(() => bob.evaluate(async () => [...(await window.__peers[0].getStats()).values()].filter(stat => stat.type === "inbound-rtp" && stat.kind === "video" && stat.bytesReceived > 0).length)).toBe(2);
            expect(await alice.evaluate(() => window.__displayOptions.audio)).toBe(true);
            expect(await alice.evaluate(() => window.__peers[0].getSenders().filter(sender => sender.track?.kind === "audio").length)).toBe(2);
            if (signalingMode === "normal") {
                const screen = bob.locator('.call-media-tile[data-local="false"][data-source="screen"]');
                await screen.getByRole("slider", { name: "Shared audio", exact: true }).fill("35");
                await expect.poll(() => bob.evaluate(() => window.__savedSettings?.user_share_volumes?.alice)).toBe(35);
                await bob.evaluate(() => { window.__noxa.state.settings.muted_users = ["alice"]; });
                await expect.poll(() => bob.evaluate(() => window.__callGains.slice(0, 2).map(gain => Number(gain.gain.value.toFixed(2))))).toEqual([0, 0.35]);
                // A real tone on the separate negotiated share-audio sender must
                // remain audible when the same member's microphone is muted.
                await alice.evaluate(async () => {
                    const context = new AudioContext(); await context.resume();
                    const tone = context.createOscillator(), gain = context.createGain(), output = context.createMediaStreamDestination();
                    gain.gain.value = 0.1; tone.connect(gain); gain.connect(output); tone.start();
                    const sender = window.__peers[0].getSenders().find(sender => sender.track === window.__displayStream.getAudioTracks()[0]);
                    window.__shareTone = { context, tone, output, sender, original: sender.track }; await sender.replaceTrack(output.stream.getAudioTracks()[0]);
                });
                await bob.evaluate(async () => {
                    const context = new AudioContext(); await context.resume();
                    const source = context.createMediaStreamSource(document.querySelector("audio").srcObject);
                    const analyser = context.createAnalyser(); source.connect(analyser); window.__playbackMeter = { context, source, analyser };
                });
                await expect.poll(async () => Math.abs(await playbackLevel() - 0.0707 * 0.35)).toBeLessThan(0.008);
                await screen.getByRole("button", { name: "Mute shared audio", exact: true }).click();
                await expect.poll(playbackLevel).toBeLessThan(0.00001);
                await screen.getByRole("button", { name: "Unmute shared audio", exact: true }).click();
                await expect.poll(playbackLevel).toBeGreaterThan(0.015);
                await bob.evaluate(() => { window.__noxa.state.settings.muted_users = []; return window.__playbackMeter.context.close(); });
                await alice.evaluate(async () => {
                    const tone = window.__shareTone; await tone.sender.replaceTrack(tone.original); tone.tone.stop(); tone.output.stream.getTracks().forEach(track => track.stop()); await tone.context.close();
                });
            }
            await alice.getByRole("button", { name: "Stop camera", exact: true }).click();
            await expect(bob.locator('.call-media-tile[data-local="false"][data-source="camera"]')).toHaveCount(participantIDs.length - 2);
            await expect(bob.locator('.call-media-tile[data-local="false"][data-source="screen"]')).toHaveCount(1);
            await alice.getByRole("button", { name: "Stop sharing", exact: true }).click();
            await expect(bob.locator('.call-media-tile[data-local="false"][data-source="screen"]')).toHaveCount(0);
            expect(await alice.evaluate(() => window.__displayStream.getTracks().every(track => track.readyState === "ended"))).toBe(true);
        }
        await alice.getByRole("button", { name: "Mute microphone", exact: true }).click();
        await expect(alice.getByRole("button", { name: "Unmute microphone", exact: true })).toHaveAttribute("aria-pressed", "true");
        await expect(alice.getByRole("button", { name: "Unmute microphone", exact: true })).toHaveClass(/voice-control/);
        await expect.poll(() => alice.evaluate(() => window.__streams.filter(stream => stream.getAudioTracks().length).every(stream => stream.getAudioTracks()[0].readyState === "ended" || !stream.getAudioTracks()[0].enabled))).toBe(true);
        await bob.evaluate(() => { window.__noxa.state.settings.activation_mode = "ptt"; });
        const talk = bob.getByRole("button", { name: "Hold to talk", exact: true });
        await expect(talk).toBeVisible();
        await expect.poll(() => bob.evaluate(() => window.__streams[0].getAudioTracks()[0].enabled)).toBe(false);
        await talk.focus(); await bob.keyboard.down("Space");
        await expect(talk).toHaveAttribute("aria-pressed", "true");
        await expect.poll(() => bob.evaluate(() => window.__streams[0].getAudioTracks()[0].enabled)).toBe(true);
        await bob.keyboard.up("Space");
        await expect.poll(() => bob.evaluate(() => window.__streams[0].getAudioTracks()[0].enabled)).toBe(false);
        await bob.evaluate(() => { window.__noxa.state.settings.activation_mode = "continuous"; });
        await expect(talk).toHaveCount(0);
        await expect.poll(() => bob.evaluate(() => window.__streams[0].getAudioTracks()[0].enabled)).toBe(true);
        await bob.evaluate(() => {
            window.__sinks = [];
            document.querySelector("audio").setSinkId = async id => { window.__sinks.push(id); };
            window.__noxa.state.settings.playback_device_id = "headset";
        });
        await expect.poll(() => bob.evaluate(() => window.__sinks)).toEqual(["headset"]);
        await bob.evaluate(() => { window.__noxa.state.settings.playback_device_id = ""; });
        await expect.poll(() => bob.evaluate(() => window.__sinks)).toEqual(["headset", ""]);
        // A delayed old device switch must settle before the new one runs.
        await bob.evaluate(() => {
            window.__sinks = [];
            document.querySelector("audio").setSinkId = async id => {
                window.__sinks.push(id);
                if (id === "slow") await new Promise(resolve => { window.__releaseSink = resolve; });
                window.__currentSink = id;
            };
            window.__noxa.state.settings.playback_device_id = "slow";
        });
        await expect.poll(() => bob.evaluate(() => window.__sinks)).toEqual(["slow"]);
        await bob.evaluate(() => { window.__noxa.state.settings.playback_device_id = "latest"; });
        await bob.evaluate(() => window.__releaseSink());
        await expect.poll(() => bob.evaluate(() => window.__currentSink)).toBe("latest");
        await bob.evaluate(() => {
            window.__failedSinkCalls = 0;
            document.querySelector("audio").setSinkId = async () => { window.__failedSinkCalls++; throw new Error("test output unavailable"); };
            window.__noxa.state.settings.playback_device_id = "unavailable";
        });
        await expect.poll(() => bob.evaluate(() => window.__warnings.filter(w => w.includes("test output unavailable")).length)).toBe(1);
        await bob.evaluate(() => { window.__noxa.state.settings.activation_mode = "ptt"; });
        await expect(talk).toBeVisible();
        expect(await bob.evaluate(() => window.__failedSinkCalls)).toBe(1);
        await bob.getByRole("button", { name: "Mute call audio", exact: true }).click();
        expect(await bob.evaluate(() => [...document.querySelectorAll("audio")].every(audio => audio.muted))).toBe(true);
        await alice.getByRole("button", { name: "End call", exact: true }).click();
        for (const page of pages.values()) {
            await expect(page.locator(".private-call-panel")).toHaveCount(0);
            expect(await page.evaluate(() => window.__streams.every(stream => stream.getTracks().every(track => track.readyState === "ended")))).toBe(true);
        }
    } catch (error) {
        for (const [uid, page] of pages) console.log(uid, JSON.stringify(await page.evaluate(async () => ({ warnings: window.__warnings, peers: await Promise.all(window.__peers.map(async peer => ({ state: peer.connectionState, transceivers: peer.getTransceivers().map(t => ({ current: t.currentDirection, desired: t.direction, sender: t.sender.track?.readyState, enabled: t.sender.track?.enabled, receiver: t.receiver.track?.readyState })), stats: [...(await peer.getStats()).values()].filter(s => s.type === "outbound-rtp" || s.type === "inbound-rtp").map(s => ({ type: s.type, kind: s.kind, sent: s.bytesSent, received: s.bytesReceived })) }))) }))));
        throw error;
    } finally { await Promise.all([...pages.values()].map(page => page.close())); }
});
}

for (const audioLabel of ["Application Audio", "System Audio"]) {
    test(`private-call application audio validates the captured source (${audioLabel})`, async ({ page }) => {
        await mountCallRaceFixture(page);
        await page.evaluate(async audioLabel => {
            await window.__callsModule.startPrivateCall("alice");
            navigator.mediaDevices.getDisplayMedia = async options => {
                window.__displayOptions = options;
                const stream = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
                const video = stream.getVideoTracks()[0], settings = video.getSettings();
                video.getSettings = () => ({ ...settings, displaySurface: "window" });
                Object.defineProperty(stream.getAudioTracks()[0], "label", { value: audioLabel });
                window.__displayStream = stream;
                return stream;
            };
        }, audioLabel);
        await page.getByRole("combobox", { name: "Share audio", exact: true }).selectOption("application");
        await page.getByRole("button", { name: "Share screen", exact: true }).click();
        await expect(page.locator('.call-media-tile[data-local="true"][data-source="screen"]')).toBeVisible();
        await expect(page.getByRole("combobox", { name: "Share audio", exact: true })).toBeDisabled();
        if (audioLabel !== "Application Audio") {
            await expect.poll(() => page.evaluate(() => window.__warnings.join(" "))).toContain("Application audio unavailable. Sharing without audio.");
            expect(await page.evaluate(() => window.__displayStream.getAudioTracks().length)).toBe(0);
        }
        await page.getByRole("button", { name: "Stop sharing", exact: true }).click();
        expect(await page.evaluate(() => window.__displayStream.getTracks().every(t => t.readyState === "ended"))).toBe(true);
        expect(await page.evaluate(() => window.__displayOptions)).toMatchObject({ video: { displaySurface: "window" }, windowAudio: "window", systemAudio: "exclude" });
        await expect(page.getByRole("combobox", { name: "Share audio", exact: true })).toHaveValue(audioLabel === "Application Audio" ? "application" : "none");
    });
}

for (const captureError of ["NotReadableError", "NotSupportedError", "NotAllowedError", "AbortError"]) {
    test(`private-call application audio handles ${captureError}`, async ({ page }) => {
        await mountCallRaceFixture(page);
        await page.evaluate(async captureError => {
            await window.__callsModule.startPrivateCall("alice");
            window.__displayAttempts = [];
            navigator.mediaDevices.getDisplayMedia = async options => {
                window.__displayAttempts.push(options);
                if (options.audio) throw new DOMException("audio unavailable", captureError);
                window.__displayStream = await navigator.mediaDevices.getUserMedia({ video: true });
                return window.__displayStream;
            };
        }, captureError);
        await page.getByRole("combobox", { name: "Share audio", exact: true }).selectOption("application");
        await page.getByRole("button", { name: "Share screen", exact: true }).click();
        const cancelled = ["NotAllowedError", "AbortError"].includes(captureError);
        if (cancelled) {
            await expect(page.getByRole("button", { name: "Share screen", exact: true })).toBeVisible();
            await expect(page.locator('.call-media-tile[data-source="screen"]')).toHaveCount(0);
            expect(await page.evaluate(() => window.__displayAttempts.length)).toBe(1);
            expect(await page.evaluate(() => window.__warnings.join(" "))).not.toContain("Sharing without audio");
        } else {
            await expect(page.locator('.call-media-tile[data-local="true"][data-source="screen"]')).toBeVisible();
            expect(await page.evaluate(() => window.__displayAttempts.length)).toBe(2);
            expect(await page.evaluate(() => window.__displayAttempts[1])).toMatchObject({ audio: false, windowAudio: "exclude", systemAudio: "exclude" });
            expect(await page.evaluate(() => window.__warnings)).toEqual(["Application audio unavailable. Sharing without audio."]);
            await expect(page.getByRole("combobox", { name: "Share audio", exact: true })).toHaveValue("none");
            await page.getByRole("button", { name: "Stop sharing", exact: true }).click();
            expect(await page.evaluate(() => window.__displayStream.getTracks().every(track => track.readyState === "ended"))).toBe(true);
        }
    });
}

for (const source of ["camera", "screen"]) {
    test(`late private-call ${source} capture after ending a call is released`, async ({ page }) => {
        await mountCallRaceFixture(page);
        await page.evaluate(async source => {
            await window.__callsModule.startPrivateCall("alice");
            window.__mediaPermissionRequested = false;
            const getMedia = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
            const delayed = async () => {
                window.__mediaPermissionRequested = true;
                await new Promise(resolve => { window.__allowMedia = resolve; });
                const stream = await getMedia({ video: true, audio: source === "screen" });
                window.__lateMedia = stream;
                return stream;
            };
            if (source === "camera") navigator.mediaDevices.getUserMedia = delayed;
            else navigator.mediaDevices.getDisplayMedia = delayed;
        }, source);
        await page.getByRole("button", { name: source === "camera" ? "Start camera" : "Share screen", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__mediaPermissionRequested)).toBe(true);
        await page.getByRole("button", { name: "End call", exact: true }).click();
        await page.evaluate(() => window.__allowMedia());
        await expect.poll(() => page.evaluate(() => window.__lateMedia?.getTracks().every(track => track.readyState === "ended"))).toBe(true);
        await expect(page.locator(".call-media-tile")).toHaveCount(0);
        await expect(page.locator(".private-call-panel")).toHaveCount(0);
    });
}

// These races use the real browser capture/peer APIs. Only the native control
// bridge is delayed, so failures exercise call ownership rather than a mock PC.
async function mountCallRaceFixture(page) {
    await page.route("**/__call_race_test__", route => route.fulfill({ contentType: "text/html", body: '<!doctype html><title>Call race test</title><link rel="stylesheet" href="/src/private-calls.css">' }));
    await page.goto("http://127.0.0.1:12364/__call_race_test__");
    await page.evaluate(async () => {
        window.__captures = 0; window.__streams = []; window.__allTracks = []; window.__peers = [];
        window.__warnings = []; window.__stops = []; window.__answers = []; window.__pendingGets = [];
        window.__holdGets = false; window.__failICE = false;
        const getMedia = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
        navigator.mediaDevices.getUserMedia = async constraints => {
            window.__captures++;
            const stream = await getMedia(constraints);
            window.__streams.push(stream); window.__allTracks.push(...stream.getTracks());
            return stream;
        };
        const cloneTrack = MediaStreamTrack.prototype.clone;
        MediaStreamTrack.prototype.clone = function () { const clone = cloneTrack.call(this); window.__allTracks.push(clone); return clone; };
        window.__OriginalPeer = window.RTCPeerConnection;
        window.RTCPeerConnection = class extends window.__OriginalPeer {
            constructor(...args) { super(...args); window.__peers.push(this); }
        };
        window.__makeCall = (id = "call-old", revision = 1, bothAccepted = false) => ({
            id, caller: "bob", revision, created_at: Math.floor(Date.now() / 1000), ring_until: Math.floor(Date.now() / 1000) + 30, ended_at: 0,
            participants: [{ unique_id: "bob", client_id: "b", state: "accepted" }, { unique_id: "alice", client_id: "a", state: bothAccepted ? "accepted" : "ringing" }],
        });
        window.__call = window.__makeCall();
        window.__noxa = {
            state: { activeTabID: "server-a", serverGeneration: 1, myUniqueID: "bob", myChannelID: 0, muted: false, deafened: false, settings: { activation_mode: "continuous", blocked_users: [] }, clients: [{ unique_id: "alice", nickname: "Alice" }, { unique_id: "bob", nickname: "Bob" }] },
            toast: message => window.__warnings.push(message), resetVoiceSession() {},
        };
        window.go = { main: { App: {
            PrivateCallForTab: async (_tab, request) => {
                const response = { action: request.action, call: structuredClone(window.__call) };
                if (request.action === "get" && window.__holdGets) return new Promise(resolve => window.__pendingGets.push({ resolve, response }));
                return response;
            },
            GetICEServersForTab: async () => { if (window.__failICE) throw new Error("ICE configuration unavailable"); return []; },
            OpenPrivateCallDescriptionForTab: async (_tab, signal) => signal.description,
            SendPrivateCallDescriptionForTab: async (_tab, id, target, type, sdp) => {
                window.__answers.push({ id, target, type, sdp });
                if (target === "alice" && type === "answer" && window.__remoteOfferPeer) await window.__remoteOfferPeer.setRemoteDescription({ type, sdp });
                if (target === "alice" && type === "candidates" && window.__remoteOfferPeer) {
                    for (const candidate of JSON.parse(sdp)) await window.__remoteOfferPeer.addIceCandidate(candidate);
                }
            },
            StopPrivateCallForTab: async (_tab, id) => { window.__stops.push(id); },
            JoinChannelForTab: async () => { throw new Error("private call must not join a channel"); },
        } } };
        window.__callsModule = await import("/src/private-calls.js");
    });
}

test("incoming private call remains answerable while already in a voice channel", async ({ page }) => {
    await mountCallRaceFixture(page);
    await page.clock.install();
    await page.evaluate(async () => {
        window.__noxa.state.myUniqueID = "alice";
        window.__noxa.state.myChannelID = 7;
        await window.__callsModule.privateCallChanged({ id: "call-old" });
    });
    await expect(page.getByRole("button", { name: "Accept", exact: true })).toBeVisible();
    await page.clock.runFor(4500);
    await expect(page.getByRole("button", { name: "Accept", exact: true })).toBeVisible();
    expect(await page.evaluate(() => ({ channel: window.__noxa.state.myChannelID, captures: window.__captures, stops: window.__stops }))).toEqual({ channel: 7, captures: 0, stops: [] });
    await page.getByRole("button", { name: "Decline", exact: true }).click();
    await expect(page.locator(".private-call-panel")).toHaveCount(0);
    expect(await page.evaluate(() => window.__stops)).toEqual(["call-old"]);
});

test("cancelled incoming call cannot leave voice through an already open confirmation", async ({ page }) => {
    await mountCallRaceFixture(page);
    await page.evaluate(async () => {
        window.__noxa.state.myUniqueID = "alice";
        window.__noxa.state.myChannelID = 7;
        window.__channelMoves = 0;
        window.go.main.App.JoinChannelForTab = async () => {
            window.__channelMoves++;
            window.__noxa.state.myChannelID = 0;
            return "";
        };
        await window.__callsModule.privateCallChanged({ id: "call-old" });
    });
    await page.getByRole("button", { name: "Accept", exact: true }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await page.evaluate(async () => {
        window.__call = { ...window.__call, revision: 2, ended_at: Math.floor(Date.now() / 1000) };
        await window.__callsModule.privateCallChanged({ id: "call-old" });
    });
    await expect(page.locator(".private-call-panel")).toHaveCount(0);
    await page.getByRole("button", { name: "Confirm", exact: true }).click();
    expect(await page.evaluate(() => ({ channel: window.__noxa.state.myChannelID, moves: window.__channelMoves, captures: window.__captures }))).toEqual({ channel: 7, moves: 0, captures: 0 });
});

for (const knownCall of [false, true]) {
    test(`offer preceding call-state refresh is replayed (${knownCall ? "sender still ringing" : "no local call yet"})`, async ({ page }) => {
        await mountCallRaceFixture(page);
        if (knownCall) await page.evaluate(() => window.__callsModule.startPrivateCall("alice"));
        await page.evaluate(async () => {
            // Alice has accepted on the server and produced an offer, while
            // Bob's local call-state get has not yet delivered that revision.
            window.__remoteOfferPeer = new window.__OriginalPeer({ iceServers: [] });
            const peer = window.__remoteOfferPeer;
            peer.addTransceiver("audio", { direction: "recvonly" });
            await peer.setLocalDescription(await peer.createOffer());
            if (peer.iceGatheringState !== "complete") await new Promise(resolve => peer.addEventListener("icegatheringstatechange", () => { if (peer.iceGatheringState === "complete") resolve(); }));
            window.__call = window.__makeCall("call-old", 2, true);
            window.__holdGets = true;
            window.__refresh = window.__callsModule.privateCallChanged({ id: "call-old" });
        });
        await expect.poll(() => page.evaluate(() => window.__pendingGets.length)).toBeGreaterThan(0);
        await page.evaluate(() => {
            window.__earlySignal = window.__callsModule.privateCallSignal({
                call_id: "call-old", from: "alice", to: "bob", body: btoa("x".repeat(64)),
                description: { call_id: "call-old", from: "alice", to: "bob", type: "offer", sdp: window.__remoteOfferPeer.localDescription.sdp },
            });
        });
        // Receiving a signal alone must not acquire the microphone before the
        // authorized accepted membership snapshot has reached this client.
        expect(await page.evaluate(() => window.__captures)).toBe(0);
        await page.evaluate(async () => {
            window.__holdGets = false;
            for (const pending of window.__pendingGets.splice(0)) pending.resolve(pending.response);
            await window.__refresh;
        });
        await expect.poll(() => page.evaluate(() => window.__answers.filter(answer => answer.type === "answer").length), { timeout: 5000 }).toBe(1);
        await expect.poll(() => page.evaluate(() => window.__peers.map(peer => peer.connectionState)), { timeout: 10000 }).toEqual(["connected"]);
        await page.evaluate(() => { window.__callsModule.stopPrivateCall(); window.__remoteOfferPeer.close(); });
    });
}

test("ICE configuration failure after capture releases microphone and closes the call", async ({ page }) => {
    await mountCallRaceFixture(page);
    await page.evaluate(async () => {
        window.__call = window.__makeCall("call-ice-failure", 2, true);
        window.__failICE = true;
        await window.__callsModule.startPrivateCall("alice");
    });
    expect(await page.evaluate(() => window.__captures)).toBe(1);
    await expect.poll(() => page.evaluate(() => window.__allTracks.length > 0 && window.__allTracks.every(track => track.readyState === "ended"))).toBe(true);
    await expect(page.locator(".private-call-panel")).toHaveCount(0);
    expect(await page.evaluate(() => window.__stops)).toContain("call-ice-failure");
});

test("private-call microphone switch failure preserves the live capture and allows retry", async ({ page }) => {
    await mountCallRaceFixture(page);
    await page.evaluate(async () => {
        window.__call = window.__makeCall("call-old", 2, true);
        await window.__callsModule.startPrivateCall("alice");
        window.__oldCallMic = window.__streams[0].getAudioTracks()[0];
        window.__noxa.state.settings.capture_device_id = "missing-audit-microphone";
        try { await window.__callsModule.applyPrivateCallAudioSettings(); } catch (error) { window.__captureError = error.name; }
    });
    expect(await page.evaluate(() => window.__captureError)).toBeTruthy();
    expect(await page.evaluate(() => window.__oldCallMic.readyState)).toBe("live");
    expect(await page.evaluate(() => window.__peers[0].getSenders().some(sender => sender.track === window.__oldCallMic))).toBe(true);
    await page.evaluate(async () => {
        window.__noxa.state.settings.capture_device_id = "";
        window.__noxa.state.settings.noise_suppression = false;
        await window.__callsModule.applyPrivateCallAudioSettings();
    });
    expect(await page.evaluate(() => window.__oldCallMic.readyState)).toBe("ended");
    await page.getByRole("button", { name: "End call", exact: true }).click();
    expect(await page.evaluate(() => window.__allTracks.every(track => track.readyState === "ended"))).toBe(true);
});

test("ending a private call releases a microphone switch still waiting for capture", async ({ page }) => {
    await mountCallRaceFixture(page);
    await page.evaluate(async () => {
        window.__call = window.__makeCall("call-old", 2, true);
        await window.__callsModule.startPrivateCall("alice");
        const capture = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
        navigator.mediaDevices.getUserMedia = async constraints => {
            await new Promise(resolve => { window.__allowCaptureSwitch = resolve; });
            window.__lateSwitch = await capture(constraints); return window.__lateSwitch;
        };
        window.__noxa.state.settings.noise_suppression = false;
        window.__captureSwitch = window.__callsModule.applyPrivateCallAudioSettings();
    });
    await expect.poll(() => page.evaluate(() => typeof window.__allowCaptureSwitch)).toBe("function");
    await page.getByRole("button", { name: "End call", exact: true }).click();
    await page.evaluate(async () => { window.__allowCaptureSwitch(); await window.__captureSwitch; });
    expect(await page.evaluate(() => window.__lateSwitch.getTracks().every(track => track.readyState === "ended"))).toBe(true);
    expect(await page.evaluate(() => window.__allTracks.every(track => track.readyState === "ended"))).toBe(true);
    await expect(page.locator(".private-call-panel")).toHaveCount(0);
});

test("an ended private-call microphone can recover without changing its capture profile", async ({ page }) => {
    await mountCallRaceFixture(page);
    await page.evaluate(async () => {
        window.__call = window.__makeCall("call-old", 2, true);
        await window.__callsModule.startPrivateCall("alice");
        window.__oldCallMic = window.__streams[0].getAudioTracks()[0]; window.__oldCallMic.stop();
        await window.__callsModule.applyPrivateCallAudioSettings();
    });
    expect(await page.evaluate(() => window.__captures)).toBe(2);
    expect(await page.evaluate(() => window.__peers[0].getSenders().some(sender => sender.track === window.__streams[1].getAudioTracks()[0] && sender.track.readyState === "live"))).toBe(true);
    await page.getByRole("button", { name: "End call", exact: true }).click();
    expect(await page.evaluate(() => window.__allTracks.every(track => track.readyState === "ended"))).toBe(true);
});

test("hardware loss in a private call waits for explicit retry and keeps the call connected", async ({ page }) => {
    await mountCallRaceFixture(page);
    await page.evaluate(async () => {
        window.__call = window.__makeCall("call-old", 2, true);
        await window.__callsModule.startPrivateCall("alice");
        window.__oldCallMic = window.__streams[0].getAudioTracks()[0];
        window.__oldPeer = window.__peers[0];
        window.__oldCallMic.dispatchEvent(new Event("ended"));
        await window.__callsModule.applyPrivateCallAudioSettings();
    });
    await expect(page.getByText(/Microphone disconnected:/)).toBeVisible();
    expect(await page.evaluate(() => window.__captures)).toBe(1);
    expect(await page.evaluate(() => window.__oldCallMic.readyState)).toBe("ended");
    await expect.poll(() => page.evaluate(() => window.__oldPeer.getSenders().every(sender => !sender.track))).toBe(true);
    await page.getByRole("button", { name: "Retry microphone access" }).click();
    await expect.poll(() => page.evaluate(() => window.__captures)).toBe(2);
    await expect(page.getByText(/Microphone disconnected:/)).toHaveCount(0);
    expect(await page.evaluate(() => window.__peers[0] === window.__oldPeer && window.__oldPeer.getSenders().some(sender => sender.track === window.__streams[1].getAudioTracks()[0]))).toBe(true);
    await page.getByRole("button", { name: "End call", exact: true }).click();
    expect(await page.evaluate(() => window.__allTracks.every(track => track.readyState === "ended"))).toBe(true);
});

test("partial group microphone swap failure rolls every active peer back before retry", async ({ page }) => {
    await mountCallRaceFixture(page);
    await page.evaluate(async () => {
        window.__call = window.__makeCall("call-old", 2, true);
        window.__call.participants.push({ unique_id: "charlie", client_id: "c", state: "accepted" });
        await window.__callsModule.startPrivateCall("alice");
        window.__oldCallMic = window.__streams[0].getAudioTracks()[0]; window.__initialTrackCount = window.__allTracks.length;
        window.__rejectingSender = window.__peers[1].getSenders().find(sender => sender.track === window.__oldCallMic);
        window.__replaceTrack = window.__rejectingSender.replaceTrack.bind(window.__rejectingSender);
        window.__rejectingSender.replaceTrack = async track => { if (track !== window.__oldCallMic) throw new Error("Device swap rejected"); return window.__replaceTrack(track); };
        window.__noxa.state.settings.noise_suppression = false;
        try { await window.__callsModule.applyPrivateCallAudioSettings(); } catch (error) { window.__captureError = error.message; }
    });
    expect(await page.evaluate(() => window.__captureError)).toBe("Device swap rejected");
    expect(await page.evaluate(() => window.__peers.every(peer => peer.getSenders().some(sender => sender.track === window.__oldCallMic && sender.track.readyState === "live")))).toBe(true);
    expect(await page.evaluate(() => window.__allTracks.slice(window.__initialTrackCount).every(track => track.readyState === "ended"))).toBe(true);
    await page.evaluate(async () => { window.__rejectingSender.replaceTrack = window.__replaceTrack; await window.__callsModule.applyPrivateCallAudioSettings(); });
    expect(await page.evaluate(() => window.__oldCallMic.readyState)).toBe("ended");
    expect(await page.evaluate(() => window.__peers.every(peer => peer.getSenders().some(sender => sender.track === window.__streams.at(-1).getAudioTracks()[0] && sender.track.readyState === "live")))).toBe(true);
    await page.getByRole("button", { name: "End call", exact: true }).click();
    expect(await page.evaluate(() => window.__allTracks.every(track => track.readyState === "ended"))).toBe(true);
});

test("delayed active call response cannot resurrect an ended call", async ({ page }) => {
    await mountCallRaceFixture(page);
    await page.evaluate(async () => {
        await window.__callsModule.startPrivateCall("alice");
        window.__holdGets = true;
        window.__lateRefresh = window.__callsModule.privateCallChanged({ id: "call-old" });
    });
    await expect.poll(() => page.evaluate(() => window.__pendingGets.length)).toBe(1);
    await page.evaluate(async () => {
        window.__holdGets = false;
        window.__call.ended_at = Math.floor(Date.now() / 1000);
        window.__call.revision = 2;
        await window.__callsModule.privateCallChanged({ id: "call-old" });
    });
    await expect(page.locator(".private-call-panel")).toHaveCount(0);
    await page.evaluate(async () => {
        const pending = window.__pendingGets.shift();
        pending.resolve(pending.response);
        await window.__lateRefresh;
    });
    // Assert immediately after delivery; a later periodic poll hiding the
    // resurrected panel would mask the stale-response ownership bug.
    expect(await page.locator(".private-call-panel").count()).toBe(0);
    expect(await page.evaluate(() => window.__captures)).toBe(0);
});

test("delayed ended response for an old call cannot close a newer call", async ({ page }) => {
    await mountCallRaceFixture(page);
    await page.evaluate(async () => {
        await window.__callsModule.startPrivateCall("alice");
        window.__call.ended_at = Math.floor(Date.now() / 1000);
        window.__call.revision = 2;
        window.__holdGets = true;
        window.__lateRefresh = window.__callsModule.privateCallChanged({ id: "call-old" });
    });
    await expect.poll(() => page.evaluate(() => window.__pendingGets.length)).toBe(1);
    await page.evaluate(async () => {
        window.__callsModule.stopPrivateCall();
        window.__holdGets = false;
        window.__call = window.__makeCall("call-new");
        await window.__callsModule.startPrivateCall("alice");
        const pending = window.__pendingGets.shift();
        pending.resolve(pending.response);
        await window.__lateRefresh;
    });
    await expect(page.locator(".private-call-panel")).toHaveCount(1);
    expect(await page.evaluate(() => window.__stops)).not.toContain("call-new");
    await page.getByRole("button", { name: "End call", exact: true }).click();
    expect(await page.evaluate(() => window.__stops)).toContain("call-new");
});

for (const failure of ["createOffer", "sendDescription"]) {
    test(`negotiation ${failure} failure releases original and monitor capture`, async ({ page }) => {
        await mountCallRaceFixture(page);
        await page.evaluate(async failure => {
            // Bob creates the offer to Charlie, exercising negotiation after
            // real microphone capture and monitor-track creation succeed.
            window.__call = window.__makeCall("call-negotiation-failure", 2, true);
            window.__call.participants[1].unique_id = "charlie";
            window.__noxa.state.clients.push({ unique_id: "charlie", nickname: "Charlie" });
            window.__negotiationFailures = 0;
            if (failure === "createOffer") {
                window.__OriginalPeer.prototype.createOffer = async function () {
                    window.__negotiationFailures++;
                    throw new Error("offer creation failed");
                };
            } else {
                window.go.main.App.SendPrivateCallDescriptionForTab = async () => {
                    window.__negotiationFailures++;
                    throw new Error("encrypted signaling send failed");
                };
            }
            await window.__callsModule.startPrivateCall("charlie");
        }, failure);
        await expect.poll(() => page.evaluate(() => window.__negotiationFailures)).toBe(1);
        expect(await page.evaluate(() => window.__captures)).toBe(1);
        expect(await page.evaluate(() => window.__allTracks.length)).toBeGreaterThanOrEqual(2);
        await expect.poll(() => page.evaluate(() => window.__allTracks.every(track => track.readyState === "ended"))).toBe(true);
        await expect(page.locator(".private-call-panel")).toHaveCount(0);
        expect(await page.evaluate(() => window.__peers.every(peer => peer.connectionState === "closed"))).toBe(true);
        expect(await page.evaluate(() => window.__stops)).toContain("call-negotiation-failure");
    });
}

test("member removed during ICE lookup is not recreated by stale call continuation", async ({ page }) => {
    await mountCallRaceFixture(page);
    await page.evaluate(() => {
        window.__call = window.__makeCall("call-group-race", 2, true);
        window.__call.conversation_id = "private-group";
        window.__call.participants.push({ unique_id: "charlie", client_id: "c", state: "accepted" });
        window.__noxa.state.clients.push({ unique_id: "charlie", nickname: "Charlie" });
        window.__iceWaits = 0; window.__createdOffers = 0;
        const createOffer = window.__OriginalPeer.prototype.createOffer;
        window.__OriginalPeer.prototype.createOffer = function (...args) { window.__createdOffers++; return createOffer.apply(this, args); };
        window.go.main.App.GetICEServersForTab = () => new Promise(resolve => { window.__iceWaits++; window.__releaseICE = resolve; });
        window.__startingGroup = window.__callsModule.startPrivateCall("", "private-group");
    });
    await expect.poll(() => page.evaluate(() => window.__iceWaits)).toBe(1);
    expect(await page.evaluate(() => window.__peers.length)).toBe(0);
    await page.evaluate(() => {
        window.__call.revision = 3;
        window.__call.participants.find(member => member.unique_id === "charlie").state = "left";
        window.__membershipRefresh = window.__callsModule.privateCallChanged({ id: "call-group-race" });
    });
    await expect(page.locator(".private-call-panel")).not.toContainText("Charlie");
    await page.evaluate(async () => {
        window.__releaseICE([]);
        await Promise.all([window.__startingGroup, window.__membershipRefresh]);
    });
    // Alice is still accepted, so exactly one real peer should exist. Charlie
    // must not be reintroduced from the pre-await accepted-member array.
    expect(await page.evaluate(() => window.__peers.length)).toBe(1);
    expect(await page.evaluate(() => window.__createdOffers)).toBe(0);
    expect(await page.evaluate(() => window.__answers.filter(message => message.target === "charlie"))).toEqual([]);
    await expect(page.locator(".private-call-panel")).toHaveCount(1);
    await page.evaluate(() => window.__callsModule.stopPrivateCall());
});

test("decrypted offer released after member removal never installs SDP", async ({ page }) => {
    await mountCallRaceFixture(page);
    await page.evaluate(async () => {
        window.__call = window.__makeCall("call-decrypt-race", 2, true);
        window.__call.conversation_id = "private-group";
        window.__call.participants.push({ unique_id: "charlie", client_id: "c", state: "accepted" });
        window.__noxa.state.clients.push({ unique_id: "charlie", nickname: "Charlie" });
        await window.__callsModule.startPrivateCall("", "private-group");
        window.__remoteSDPInstalls = 0;
        const setRemoteDescription = window.__OriginalPeer.prototype.setRemoteDescription;
        window.__OriginalPeer.prototype.setRemoteDescription = function (...args) {
            if (window.__peers.includes(this)) window.__remoteSDPInstalls++;
            return setRemoteDescription.apply(this, args);
        };
        window.__remoteOfferPeer = new window.__OriginalPeer({ iceServers: [] });
        const peer = window.__remoteOfferPeer;
        peer.addTransceiver("audio", { direction: "recvonly" });
        await peer.setLocalDescription(await peer.createOffer());
        if (peer.iceGatheringState !== "complete") await new Promise(resolve => peer.addEventListener("icegatheringstatechange", () => { if (peer.iceGatheringState === "complete") resolve(); }));
        window.__decryptWaits = 0;
        window.go.main.App.OpenPrivateCallDescriptionForTab = (_tab, signal) => new Promise(resolve => {
            window.__decryptWaits++;
            window.__releaseDescription = () => resolve(signal.description);
        });
        window.__pendingSignal = window.__callsModule.privateCallSignal({
            call_id: "call-decrypt-race", from: "alice", to: "bob", body: btoa("x".repeat(64)),
            description: { call_id: "call-decrypt-race", from: "alice", to: "bob", type: "offer", sdp: peer.localDescription.sdp },
        });
    });
    await expect.poll(() => page.evaluate(() => window.__decryptWaits)).toBe(1);
    await page.evaluate(async () => {
        window.__call.revision = 3;
        window.__call.participants.find(member => member.unique_id === "alice").state = "left";
        await window.__callsModule.privateCallChanged({ id: "call-decrypt-race" });
        window.__releaseDescription();
        await window.__pendingSignal;
    });
    expect(await page.evaluate(() => window.__remoteSDPInstalls)).toBe(0);
    expect(await page.evaluate(() => window.__answers.filter(message => message.target === "alice"))).toEqual([]);
    await expect(page.locator(".private-call-panel")).not.toContainText("Alice");
    await page.evaluate(() => { window.__callsModule.stopPrivateCall(); window.__remoteOfferPeer.close(); });
});
