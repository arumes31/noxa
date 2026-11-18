import { expect, test, chooseShareOption } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.route("**/media-slots-fixture", (route) => route.fulfill({ contentType: "text/html", body: `<!doctype html>
        <button id="voice-screen">Share</button><button id="voice-video">Camera</button>
        <video id="local-video"></video><div id="mic-status"></div><button id="ptt-btn">Talk</button><section id="sharing-status" hidden></section>` }));
    await page.goto("/media-slots-fixture");
    if (test.info().title.includes("without frame processing")) {
        await page.evaluate(() => { window.MediaStreamTrackProcessor = undefined; });
    }
    await page.evaluate(async () => {
        const capture = () => document.createElement("canvas").captureStream(1);
        const localStream = capture();
        const camera = localStream.getVideoTracks()[0];
        const makePeer = () => {
          const transceivers = [];
          return {
            signalingState: "stable",
            close() { this.signalingState = "closed"; transceivers.forEach(t => t.stop()); },
            getSenders: () => transceivers.filter(t => !t.stopped).map(t => t.sender),
            getTransceivers: () => transceivers.filter(t => !t.stopped),
            createOffer: async () => ({ type: "offer", sdp: "test-offer" }),
            async setLocalDescription(description) {
                if (description.type === "rollback") { this.signalingState = "stable"; return; }
                if (this.signalingState !== "stable") throw new Error("overlapping local offer");
                this.signalingState = "have-local-offer";
            },
            async setRemoteDescription() {
                if (this.signalingState !== "have-local-offer") throw new Error("answer without local offer");
                this.signalingState = "stable";
            },
            addTransceiver(track, options) {
                if (window.__media && track.kind === "video") window.__media.initialVideoEncodings = structuredClone(options.sendEncodings);
                let parameters = { encodings: options.sendEncodings || [{}] };
                const sender = { track: typeof track === "string" ? null : track, replaceTrack: async (next) => { sender.track = next; },
                    getParameters: () => structuredClone(parameters), setParameters: async (next) => {
                        if (window.__media.failCaps) throw new Error("encoder cap rejected");
                        parameters = structuredClone(next);
                    } };
                const transceiver = { sender, receiver: { track: typeof track === "string" ? { kind: track } : track }, direction: options.direction,
                    stop() { this.stopped = true; } };
                transceivers.push(transceiver);
                return transceiver;
            },
          };
        };
        const pc = makePeer();
        const cameraSender = pc.addTransceiver(camera, { direction: "sendrecv" }).sender;
        window.__media = { makePeer, camera, cameraSender, offers: [], errors: [], notices: [], displayTracks: [] };
        window.__noxa = { state: { pc, localStream, serverGeneration: 1, activeTabID: "media-tab", settings: {}, myChannelID: 1,
            myClientID: "self", clients: [{ client_id: "peer", channel_id: 1 }] },
            $: id => document.getElementById(id), sysMsg: message => window.__media.errors.push(message),
            toast: message => window.__media.notices.push(message) };
        window.go = { main: { App: { ChannelOfferMockForTab: async (tab, _sdp, slots) => {
            if (tab !== "media-tab") throw new Error("wrong server tab");
            if (window.__media.failOffer) throw new Error("offer rejected");
            window.__media.offers.push(slots);
            if (window.__media.delayOffer) await new Promise(resolve => { window.__media.finishOffer = resolve; });
            return "answer";
        },
            VideoStreamControlForTab: async (tab, msg) => {
                if (tab !== "media-tab") throw new Error("wrong tab");
                if (msg.action === "publish" && msg.active && msg.slot === "screen") {
                    window.__media.shareControl = [tab, msg.active];
                    if (window.__media.delayShareControl) {
                        const error = await new Promise(resolve => { window.__media.finishShareControl = resolve; });
                        if (error) throw new Error(error);
                    }
                }
                return { ...msg, generation: msg.generation === "0" ? "1" : msg.generation, streams: [] };
            },
            SetVideoQualityForTab: async (tab, quality) => {
                if (tab !== "media-tab") throw new Error("wrong tab");
                window.__media.qualityControl = [tab, quality];
                if (window.__media.delayQualityControl) return await new Promise(resolve => { window.__media.finishQualityControl = resolve; });
                return "";
            } } } };
        window.__captureLeases = { acquired: [], released: [] };
        Object.assign(window.go.main.App, {
            AcquireDisplayCaptureLease: async () => { const id = `lease-${window.__captureLeases.acquired.length}`; window.__captureLeases.acquired.push(id); return id; },
            RefreshDisplayCaptureLease: async () => {},
            ReleaseDisplayCaptureLease: async id => { window.__captureLeases.released.push(id); },
        });
        Object.defineProperty(navigator.mediaDevices, "getDisplayMedia", { configurable: true, value: async (options) => {
            window.__media.captureOptions = options;
            const stream = capture();
            window.__media.displayTracks.push(...stream.getTracks());
            if (window.__media.delayCapture) await new Promise(resolve => { window.__media.finishCapture = resolve; });
            return stream;
        } });
        // Encrypted channel media (Task 2): these stand-in peers carry no
        // encoded transforms, so negotiation runs with the session double;
        // offers go through the secure binding.
        window.go.main.App.WebRTCOfferSecureForTab = (tab, sdp, slots) => window.go.main.App.ChannelOfferMockForTab(tab, sdp, slots);
        const channelMedia = await import("/src/channel-media.js");
        const standIn = () => ({ active: true, media: { authenticated: () => true, onAuthenticated() {} }, identity: { channelID: 1 }, async start() { return this; }, stop() { this.active = false; }, setRoster() {}, protectPeer: pc => pc,
            peerConfig: c => c, bindSenders: async () => {}, syncReceivers: async () => {}, rescope() {}, publicationsChanged() {}, receive: () => ({ ok: false }), status: () => ({ state: "encrypted" }) });
        channelMedia.useChannelMediaFactory(standIn);
        channelMedia.useChannelMediaSession(standIn());
        // Go's native_share events reach the webview through the Wails runtime.
        window.__events = {};
        window.runtime = { EventsOn: (name, callback) => { (window.__events[name] ||= []).push(callback); return () => {}; } };
        window.__media.video = await import("/src/video.js");
        (await import("/src/modal.js")).initModalSystem();
        document.getElementById("voice-screen").onclick = () => window.__media.video.shareToggle();
    });
});

test("post-update stream prompt waits for an explicit source choice and preserves quality", async ({ page }) => {
    await page.evaluate(async () => {
        const bar = document.createElement('div'); bar.id = 'voice-bar'; document.body.append(bar);
        const { offerUpdateShareResume } = await import('/src/update-share-resume.js');
        offerUpdateShareResume({ tab_id: 'media-tab', stream_channel_id: 1,
            stream: { surface: 'window', preset: 'hdMotion', custom: {}, audioMode: 'none', bitrateCeiling: 6000000 } });
    });
    expect(await page.evaluate(() => window.__media.displayTracks.length)).toBe(0);
    await page.getByRole('button', { name: 'Resume stream', exact: true }).click();
    await expect(page.getByRole('dialog')).toBeVisible();
    expect(await page.evaluate(() => window.__media.displayTracks.length)).toBe(0);
    await expect(page.locator('.sh-preset input:checked')).toHaveValue('hdMotion');
    await expect(page.locator('.sh-bitrate input:checked')).toHaveValue('6000000');
    await expect(page.locator('.sh-audio input:checked')).toHaveValue('none');
});

test("leaving the restored channel permanently removes the stream resume prompt", async ({ page }) => {
    await page.evaluate(async () => {
        const bar = document.createElement('div'); bar.id = 'voice-bar'; document.body.append(bar);
        const { offerUpdateShareResume } = await import('/src/update-share-resume.js');
        offerUpdateShareResume({ tab_id: 'media-tab', stream_channel_id: 1,
            stream: { surface: 'window', preset: 'hd', custom: {}, audioMode: 'none', bitrateCeiling: 6000000 } });
    });
    const resume = page.getByRole('button', { name: 'Resume stream', exact: true });
    await expect(resume).toBeVisible();
    await page.evaluate(() => { window.__noxa.state.myChannelID = 0; });
    await expect(resume).toHaveCount(0);
    await page.evaluate(() => { window.__noxa.state.myChannelID = 1; });
    await expect(resume).toHaveCount(0);
});

test("application audio requires a window and never switches silently to system audio", async ({ page }) => {
    await page.locator("#voice-screen").click();
    await expect(page.locator(".sh-audio input:checked")).toHaveValue("none");
    await expect(page.locator('.sh-audio input[value="application"]')).toBeDisabled();
    await page.getByLabel("Window", { exact: true }).check();
    await expect(page.locator(".sh-audio input:checked")).toHaveValue("application");
    await page.getByLabel("Screen", { exact: true }).check();
    await expect(page.locator(".sh-audio input:checked")).toHaveValue("none");
    await expect(page.locator('.sh-audio input[value="application"]')).toBeDisabled();
    await page.getByLabel("Region", { exact: true }).check();
    await expect(page.locator(".sh-audio input:checked")).toHaveValue("none");
    await expect(page.locator('.sh-audio input[value="application"]')).toBeDisabled();
    await page.getByLabel("Window", { exact: true }).check();
    await expect(page.locator(".sh-audio input:checked")).toHaveValue("application");
});

test("slot declarations preserve negotiated MSID after replacing a real browser sender", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const pc = new RTCPeerConnection();
        const capture = () => document.createElement("canvas").captureStream(1);
        const first = capture(), next = capture();
        const transceiver = pc.addTransceiver(first.getVideoTracks()[0], { direction: "sendonly", streams: [first] });
        await pc.setLocalDescription(await pc.createOffer());
        await transceiver.sender.replaceTrack(next.getVideoTracks()[0]);
        window.__noxa.state.pc = pc;
        window.__noxa.state.shareVideoTransceiver = transceiver;
        const sdp = (await pc.createOffer()).sdp;
        const slots = window.__media.video.trackSlots(sdp);
        const original = first.getVideoTracks()[0].id;
        const replacement = next.getVideoTracks()[0].id;
        pc.close(); first.getTracks().forEach(t => t.stop()); next.getTracks().forEach(t => t.stop());
        return { slots, original, replacement };
    });
    expect(result.slots).toContainEqual({ track_id: result.original, slot: "screen" });
    expect(result.slots.some(s => s.track_id === result.replacement)).toBe(false);
});

test("offer collision answers the pending server offer before retrying the same capture", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const video = window.__media.video, pc = window.__noxa.state.pc, calls = [];
        const identity = "a=ice-ufrag:existing\r\na=ice-pwd:existing-password\r\n";
        pc.remoteDescription = { sdp: identity };
        pc.createAnswer = async () => ({ type: "answer", sdp: "local-answer" });
        pc.setLocalDescription = async description => { calls.push(description.type === "offer" ? "local-offer" : description.type); };
        pc.setRemoteDescription = async description => { calls.push(`remote-${description.type}`); };
        let attempt = 0;
        window.go.main.App.ChannelOfferMockForTab = async () => {
            calls.push("offer-write");
            if (++attempt === 1) {
                window.__media.pendingAnswer = video.answerRemoteOffer(pc, 1, identity);
                throw new Error("webrtc negotiation collision");
            }
            return "remote-answer";
        };
        window.go.main.App.WebRTCAnswerForTab = async () => { calls.push("answer-write"); };
        let error = null;
        try { await video.renegotiate(pc); } catch (e) { error = String(e); }
        await window.__media.pendingAnswer;
        return { calls, error, capture: window.__media.camera.readyState };
    });
    expect(result.error).toBeNull();
    expect(result.capture).toBe("live");
    expect(result.calls).toEqual(["local-offer", "offer-write", "rollback", "remote-offer", "answer", "answer-write", "local-offer", "offer-write", "remote-answer"]);
});

test("live bitrate changes apply to a negotiated browser sender", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const pc = new RTCPeerConnection();
        const receiver = new RTCPeerConnection();
        const state = window.__noxa.state;
        state.pc = pc;
        const sender = pc.addTransceiver(window.__media.camera, { direction: "sendonly", streams: [state.localStream] }).sender;
        try {
            await pc.setLocalDescription(await pc.createOffer());
            await receiver.setRemoteDescription(pc.localDescription);
            await receiver.setLocalDescription(await receiver.createAnswer());
            await pc.setRemoteDescription(receiver.localDescription);
            await window.__media.video.applyVideoLimits({ video_max_bitrate: 600000 });
            const bounded = sender.getParameters().encodings[0].maxBitrate;
            await window.__media.video.applyVideoLimits({ video_max_bitrate: 0 });
            return { bounded, unlimited: sender.getParameters().encodings[0].maxBitrate,
                state: window.__media.camera.readyState, signaling: pc.signalingState };
        } finally { pc.close(); receiver.close(); }
    });
    expect(result).toEqual({ bounded: 510000, unlimited: undefined, state: "live", signaling: "stable" });
});

test("live dimension updates pause capture and validate actual settings before resuming", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const track = window.__media.camera;
        let settings = { width: 300, height: 150 };
        track.getSettings = () => settings;
        track.applyConstraints = async constraints => {
            window.__media.paused = !track.enabled;
            window.__media.applied = constraints;
            settings = { width: 160, height: 90 };
        };
        const update = await window.__media.video.applyVideoLimits({ video_max_width: 160, video_max_height: 90, video_max_bitrate: 1000000 });
        return { update, paused: window.__media.paused, enabled: track.enabled, constraints: window.__media.applied, state: track.readyState };
    });
    expect(result.update).toEqual({ changed: true, dimensionsChanged: true });
    expect(result.paused).toBe(true);
    expect(result.enabled).toBe(true);
    expect(result.constraints.width.max).toBe(160);
    expect(result.constraints.height.max).toBe(90);
    expect(result.state).toBe("live");
});

test("a superseding limit update owns completion and keeps video paused until it finishes", async ({ page }) => {
    await page.evaluate(() => {
        const track = window.__media.camera;
        let settings = { width: 300, height: 150 };
        track.getSettings = () => settings;
        track.applyConstraints = async constraints => {
            await new Promise(resolve => { window.__media.finishConstraints = () => { settings = { width: constraints.width.max, height: constraints.height.max }; resolve(); }; });
        };
        window.__media.firstUpdate = window.__media.video.applyVideoLimits({ video_max_width: 200, video_max_height: 100, video_max_bitrate: 1000000 });
    });
    await expect.poll(() => page.evaluate(() => typeof window.__media.finishConstraints)).toBe("function");
    await page.evaluate(() => {
        window.__media.secondUpdate = window.__media.video.applyVideoLimits({ video_max_width: 100, video_max_height: 50, video_max_bitrate: 500000 });
        window.__media.finishConstraints();
        window.__media.finishConstraints = null;
    });
    await expect.poll(() => page.evaluate(() => typeof window.__media.finishConstraints)).toBe("function");
    expect(await page.evaluate(() => window.__media.camera.enabled)).toBe(false);
    const result = await page.evaluate(async () => {
        window.__media.finishConstraints();
        return { results: await Promise.all([window.__media.firstUpdate, window.__media.secondUpdate]),
            enabled: window.__media.camera.enabled, settings: window.__media.camera.getSettings(),
            budget: window.__media.cameraSender.getParameters().encodings[0].maxBitrate };
    });
    expect(result).toEqual({ results: [null, { changed: true, dimensionsChanged: true }], enabled: true,
        settings: { width: 100, height: 50 }, budget: 425000 });
});

test("failed live limits stop owned video and screen audio without stopping the microphone", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const context = new AudioContext();
        const mic = context.createMediaStreamDestination().stream.getAudioTracks()[0];
        const screenAudio = context.createMediaStreamDestination().stream.getAudioTracks()[0];
        window.__noxa.state.localStream.addTrack(mic);
        window.__noxa.state.shareStream = new MediaStream([screenAudio]);
        window.__media.failCaps = true;
        let failed = false;
        try { await window.__media.video.applyVideoLimits({ video_max_bitrate: 500000 }); }
        catch { failed = true; }
        const states = [window.__media.camera, mic, screenAudio].map(track => track.readyState);
        mic.stop();
        await context.close();
        return { failed, states };
    });
    expect(result).toEqual({ failed: true, states: ["ended", "live", "ended"] });
});

for (const failure of [false, true]) {
    test(`obsolete dimension completion cannot affect a replacement session (${failure ? "failure" : "success"})`, async ({ page }) => {
        await page.evaluate(() => {
            window.__media.camera.applyConstraints = () => new Promise((resolve, reject) => {
                window.__media.finishConstraints = failure => failure ? reject(new Error("old capture error")) : resolve();
            });
            window.__media.update = window.__media.video.applyVideoLimits({ video_max_width: 320, video_max_height: 180 });
        });
        await expect.poll(() => page.evaluate(() => typeof window.__media.finishConstraints)).toBe("function");
        const result = await page.evaluate(async failure => {
            const state = window.__noxa.state;
            state.serverGeneration++;
            state.activeTabID = "replacement";
            state.localStream = document.createElement("canvas").captureStream(1);
            state.shareStream = document.createElement("canvas").captureStream(1);
            state.mediaLimits = { video_max_bitrate: 12345 };
            state.pc = { getSenders: () => [] };
            window.__media.finishConstraints(failure);
            return { result: await window.__media.update, limits: state.mediaLimits,
                states: [...state.localStream.getTracks(), ...state.shareStream.getTracks()].map(track => [track.readyState, track.enabled]),
                errors: window.__media.errors };
        }, failure);
        expect(result).toEqual({ result: null, limits: { video_max_bitrate: 12345 }, states: [["live", true], ["live", true]], errors: [] });
    });
}

for (const obsoleteBy of ["new update", "relevance", "session"]) {
    test(`obsolete cap failure stays silent after ${obsoleteBy}`, async ({ page }) => {
        await page.evaluate(() => {
            const sender = window.__media.cameraSender;
            const set = sender.setParameters;
            sender.setParameters = async () => {
                sender.setParameters = set;
                await new Promise((_resolve, reject) => { window.__media.rejectCaps = () => reject(new Error("old cap failure")); });
            };
            window.__media.relevant = true;
            window.__media.firstUpdate = window.__media.video.applyVideoLimits({ video_max_bitrate: 1000000 }, () => window.__media.relevant);
        });
        await expect.poll(() => page.evaluate(() => typeof window.__media.rejectCaps)).toBe("function");
        const result = await page.evaluate(async obsoleteBy => {
            if (obsoleteBy === "new update") window.__media.secondUpdate = window.__media.video.applyVideoLimits({ video_max_bitrate: 500000 });
            if (obsoleteBy === "relevance") window.__media.relevant = false;
            if (obsoleteBy === "session") window.__noxa.state.sessionGeneration = 2;
            window.__media.rejectCaps();
            const first = await window.__media.firstUpdate;
            await window.__media.secondUpdate;
            return { first, errors: window.__media.errors, state: window.__media.camera.readyState };
        }, obsoleteBy);
        expect(result).toEqual({ first: null, errors: [], state: "live" });
    });
}

test("a channel move on the same voice peer does not abandon paused video", async ({ page }) => {
    await page.evaluate(() => {
        window.__media.camera.applyConstraints = () => new Promise(resolve => { window.__media.finishConstraints = resolve; });
        window.__media.update = window.__media.video.applyVideoLimits({ video_max_width: 320, video_max_height: 180 });
    });
    await expect.poll(() => page.evaluate(() => typeof window.__media.finishConstraints)).toBe("function");
    const result = await page.evaluate(async () => {
        window.__noxa.state.myChannelID = 2;
        window.__media.finishConstraints();
        return { result: await window.__media.update, enabled: window.__media.camera.enabled, state: window.__media.camera.readyState };
    });
    expect(result).toEqual({ result: { changed: true, dimensionsChanged: true }, enabled: true, state: "live" });
});

test("capture that ignores live constraints stops instead of resuming out of bounds", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const track = window.__media.camera;
        track.applyConstraints = async () => {};
        try { await window.__media.video.applyVideoLimits({ video_max_width: 100, video_max_height: 50 }); }
        catch (error) { return { error: error.message, state: track.readyState, enabled: track.enabled }; }
        return null;
    });
    expect(result.error).toContain("server's video size limit");
    expect(result.state).toBe("ended");
    expect(result.enabled).toBe(false);
});

test("camera capture uses bounded constraints and checks returned dimensions", async ({ page }) => {
    await page.evaluate(async () => {
        const state = window.__noxa.state;
        state.localStream.removeTrack(window.__media.camera);
        window.__media.camera.stop();
        await window.__media.cameraSender.replaceTrack(null);
        state.mediaLimits = { video_max_width: 320, video_max_height: 180, video_max_bitrate: 1000000 };
        window.__media.video.resetCameraState();
        navigator.mediaDevices.getUserMedia = async options => {
            window.__media.cameraOptions = options;
            const canvas = document.createElement("canvas");
            canvas.width = 320;
            canvas.height = 180;
            return canvas.captureStream(1);
        };
        await window.__media.video.cameraToggle();
    });
    expect(await page.evaluate(() => window.__media.cameraOptions.video.width)).toEqual({ ideal: 320, max: 320 });
    expect(await page.evaluate(() => window.__media.cameraSender.getParameters().encodings[0].maxBitrate)).toBe(850000);
    await expect(page.locator("#voice-video")).toHaveAttribute("aria-pressed", "true");
    await expect(page.locator("#voice-video")).toHaveAttribute("title", /320 × 180/);
    await page.evaluate(() => window.__media.video.cameraToggle());
    await page.getByRole("dialog").getByRole("button", { name: "Turn off", exact: true }).click();
    await expect(page.locator("#voice-video")).toHaveAttribute("aria-pressed", "false");
    await page.evaluate(async () => {
        window.__noxa.state.mediaLimits.video_max_width = 100;
        await window.__media.video.cameraToggle();
    });
    await expect(page.locator("#voice-video")).toHaveAttribute("aria-pressed", "false");
    expect(await page.evaluate(() => window.__media.offers.length)).toBe(1);
});

test("overlapping camera and screen negotiations wait for the previous answer", async ({ page }) => {
    await page.evaluate(() => {
        window.__media.delayOffer = true;
        window.__media.first = window.__media.video.renegotiate().catch(error => error.message);
    });
    await expect.poll(() => page.evaluate(() => window.__media.offers.length)).toBe(1);
    await page.evaluate(() => {
        window.__media.second = window.__media.video.renegotiate().catch(error => error.message);
    });
    const results = await page.evaluate(async () => {
        window.__media.delayOffer = false;
        window.__media.finishOffer();
        return Promise.all([window.__media.first, window.__media.second]);
    });
    expect(results).toEqual([undefined, undefined]);
    expect(await page.evaluate(() => window.__media.offers.length)).toBe(2);
});

test("queued offers from a replaced server peer are discarded", async ({ page }) => {
    const accepted = await page.evaluate(async () => {
        const pc = window.__noxa.state.pc;
        const oldSDP = "a=ice-ufrag:old\r\na=ice-pwd:old-password\r\n";
        const newSDP = "a=ice-ufrag:new\r\na=ice-pwd:new-password\r\n";
        pc.remoteDescription = { sdp: oldSDP };
        let finish;
        const waiting = new Promise(resolve => { finish = resolve; });
        const localRound = window.__media.video.queuePeerNegotiation(pc, async () => {
            await waiting;
            pc.remoteDescription = { sdp: newSDP };
        });
        await Promise.resolve();
        const offers = [];
        pc.setRemoteDescription = async description => { offers.push(description.sdp); };
        pc.createAnswer = async () => ({ type: "answer", sdp: "answer" });
        pc.setLocalDescription = async () => {};
        window.go.main.App.WebRTCAnswerForTab = async () => {};
        const stale = window.__media.video.answerRemoteOffer(pc, 1, oldSDP);
        finish();
        await Promise.all([localRound, stale]);
        await window.__media.video.answerRemoteOffer(pc, 1, newSDP);
        return offers;
    });
    expect(accepted).toEqual(["a=ice-ufrag:new\r\na=ice-pwd:new-password\r\n"]);
});

test("remote negotiation retries transient failures and resends the exact committed answer", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const pc = window.__noxa.state.pc;
        const sdp = "a=ice-ufrag:retry\r\na=ice-pwd:retry-password\r\n";
        pc.remoteDescription = { type: "answer", sdp };
        let descriptions = 0, answers = 0, writes = 0;
        const sent = [];
        pc.setRemoteDescription = async description => { descriptions++; pc.remoteDescription = description; pc.signalingState = "have-remote-offer"; };
        pc.createAnswer = async () => { if (++answers === 1) throw new Error("temporary browser failure"); return { type: "answer", sdp: "exact-answer" }; };
        pc.setLocalDescription = async description => { pc.localDescription = description; pc.signalingState = "stable"; };
        window.go.main.App.WebRTCAnswerForTab = async (_tab, answer) => { sent.push(answer); if (++writes === 1) throw new Error("temporary bridge failure"); };
        const accepted = await window.__media.video.answerRemoteOffer(pc, 1, sdp);
        return { accepted, descriptions, answers, sent };
    });
    expect(result).toEqual({ accepted: true, descriptions: 1, answers: 2, sent: ["exact-answer", "exact-answer"] });
});

for (const errorShape of ["string", "Error"]) test(`remote answer discards native tab changes without retries (${errorShape})`, async ({ page }) => {
    const result = await page.evaluate(async errorShape => {
        const pc = window.__noxa.state.pc;
        const sdp = "a=ice-ufrag:original\r\na=ice-pwd:original-password\r\n";
        pc.remoteDescription = { type: "answer", sdp };
        pc.setRemoteDescription = async description => { pc.remoteDescription = description; };
        pc.createAnswer = async () => ({ type: "answer", sdp: "old-server-answer" });
        pc.setLocalDescription = async description => { pc.localDescription = description; };
        const calls = [];
        window.go.main.App.WebRTCAnswerForTab = async (tab, answer) => {
            calls.push([tab, answer]);
            // Native activation precedes its frontend tab_reset event.
            const message = "server tab changed; refresh the editor";
            throw errorShape === "string" ? message : new Error(message);
        };
        const accepted = await window.__media.video.answerRemoteOffer(pc, 1, sdp);
        return { accepted, calls, peerUnchanged: window.__noxa.state.pc === pc };
    }, errorShape);
    expect(result).toEqual({ accepted: false, calls: [["media-tab", "old-server-answer"]], peerUnchanged: true });
});

test("saved camera mirroring refreshes the local preview without replacing its outgoing track", async ({ page }) => {
    await page.evaluate(() => {
        const grid = document.createElement("div"); grid.id = "video-grid"; document.body.append(grid);
        window.__media.video.initVideo();
        window.__noxa.state.settings.camera_mirror_preview = false;
        window.dispatchEvent(new Event("noxa-camera-preferences-changed"));
    });
    await expect(page.locator("#local-video")).toHaveCSS("transform", "none");
    await page.evaluate(() => {
        window.__noxa.state.settings.camera_mirror_preview = true;
        window.dispatchEvent(new Event("noxa-camera-preferences-changed"));
    });
    await expect(page.locator("#local-video")).toHaveCSS("transform", "matrix(-1, 0, 0, 1, 0, 0)");
    expect(await page.evaluate(() => window.__media.cameraSender.track === window.__media.camera)).toBe(true);
});

test("quality failure from an old peer cannot report against the replacement session", async ({ page }) => {
    await page.evaluate(() => {
        window.__media.delayQualityControl = true;
        window.__noxa.initials = name => (name || "P").slice(0, 1);
        const grid = document.createElement("div"); grid.id = "video-grid"; document.body.append(grid);
        const stream = document.createElement("canvas").captureStream(1);
        window.__media.video.videoTrackAdded("peer|screen", stream, { client_id: "peer" });
        void window.__media.video.setLowBandwidth(true, false);
    });
    await expect.poll(() => page.evaluate(() => typeof window.__media.finishQualityControl)).toBe("function");
    expect(await page.evaluate(() => window.__media.qualityControl)).toEqual(["media-tab", "low"]);
    await page.evaluate(() => {
        window.__noxa.state.serverGeneration++;
        window.__media.finishQualityControl("old denial");
    });
    expect(await page.evaluate(() => window.__media.errors)).toEqual([]);
});

// Screen sharing is native only: noXa's picker, capture and encoding in Go.
async function installNative(page, status = { available: true }) {
    await page.evaluate(status => {
        Object.assign(window.__media, { nativeStarts: [], nativeUpdates: [], nativeStops: [] });
        Object.assign(window.go.main.App, {
            NativeScreenShareStatusForTab: async () => ({ monitors: [{ index: 0, width: 1920, height: 1080, primary: true }],
                windows: [{ handle: "77", title: "Editor", exe: "editor.exe" }], foreground: "0", call_available: status.available, ...status }),
            NativeScreenThumbnails: async () => ({}),
            NativeScreenPreview: async () => "",
            StartNativeScreenShareForTab: async (tab, request) => { window.__media.nativeStarts.push([tab, request]); },
            UpdateNativeScreenShareForTab: async (tab, update) => { window.__media.nativeUpdates.push([tab, update]); },
            StopNativeScreenShareForTab: async (tab, id) => { window.__media.nativeStops.push([tab, id]); },
        });
    }, status);
}

async function startNative(page) {
    await page.locator("#voice-screen").click();
    const dialog = page.getByRole("dialog", { name: "Share screen" });
    await expect(dialog.locator(".sp-native-tile")).toHaveCount(1);
    await dialog.getByRole("button", { name: "Start sharing", exact: true }).click();
    await expect(page.locator("#voice-screen")).toHaveAttribute("aria-pressed", "true");
    return page.evaluate(() => window.__media.nativeStarts.at(-1));
}

test("a share starts natively from noXa's picker and stops in Go; the browser picker is never used", async ({ page }) => {
    await installNative(page);
    const [tab, request] = await startNative(page);
    expect(tab).toBe("media-tab");
    expect(request.source).toEqual({ monitor: 0, window: "0" });
    await expect.poll(() => page.evaluate(() => window.__media.nativeUpdates.some(([, update]) => update.upload_active !== undefined))).toBe(true);
    expect(await page.evaluate(() => window.__media.displayTracks.length)).toBe(0);
    // The post-update prompt can offer the running share's settings again.
    expect(await page.evaluate(() => window.__media.video.updateShareSettings())).toMatchObject({ surface: "monitor", audioMode: "none" });
    await page.locator("#voice-screen").click();
    await page.locator(".dlg-ok", { hasText: "Stop sharing" }).click();
    await expect.poll(() => page.evaluate(() => window.__media.nativeStops.map(([, id]) => id))).toEqual([request.id]);
    await expect(page.locator("#voice-screen")).toHaveAttribute("aria-pressed", "false");
});

test("a native share Go ends after a failure stops with its reason instead of falling back", async ({ page }) => {
    await installNative(page);
    const [, request] = await startNative(page);
    await page.evaluate(id => { for (const callback of window.__events.native_share || []) callback({ tab: "media-tab", share: id, state: "failed", reason: "stalled" }); }, request.id);
    await expect(page.locator("#voice-screen")).toHaveAttribute("aria-pressed", "false");
    expect(await page.evaluate(() => window.__media.notices)).toContain("Screen sharing stopped: no video could be sent for 10 seconds.");
    expect(await page.evaluate(() => ({ display: window.__media.displayTracks.length, starts: window.__media.nativeStarts.length, sharing: window.__noxa.state.screenSharing })))
        .toEqual({ display: 0, starts: 1, sharing: false });
});

test("without native capture the share button stays and the dialog explains why", async ({ page }) => {
    await installNative(page, { available: false, reason: "unsupported_windows" });
    await expect(page.locator("#voice-screen")).toBeEnabled();
    await page.locator("#voice-screen").click();
    const dialog = page.getByRole("dialog", { name: "Share screen" });
    await expect(dialog.locator(".sp-native-note")).toContainText("Windows 10 version 1903 or later");
    await expect(dialog.getByRole("button", { name: "Start sharing", exact: true })).toBeDisabled();
    expect(await page.evaluate(() => window.__media.nativeStarts.length + window.__media.displayTracks.length)).toBe(0);
});

