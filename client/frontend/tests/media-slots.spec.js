import { expect, test } from "@playwright/test";

test.beforeEach(async ({ page }) => {
    await page.route("**/media-slots-fixture", (route) => route.fulfill({ contentType: "text/html", body: `<!doctype html>
        <button id="voice-screen">Share</button><button id="voice-video">Camera</button>
        <video id="local-video"></video><div id="mic-status"></div><button id="ptt-btn">Talk</button>` }));
    await page.goto("/media-slots-fixture");
    await page.evaluate(async () => {
        const capture = () => document.createElement("canvas").captureStream(1);
        const localStream = capture();
        const camera = localStream.getVideoTracks()[0];
        const transceivers = [];
        const pc = {
            signalingState: "stable",
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
        const cameraSender = pc.addTransceiver(camera, { direction: "sendrecv" }).sender;
        window.__media = { camera, cameraSender, offers: [], errors: [], displayTracks: [] };
        window.__noxa = { state: { pc, localStream, serverGeneration: 1, activeTabID: "media-tab", settings: {}, myChannelID: 1,
            myClientID: "self", clients: [{ client_id: "peer", channel_id: 1 }] },
            $: id => document.getElementById(id), sysMsg: message => window.__media.errors.push(message) };
        window.go = { main: { App: { WebRTCOfferForTab: async (tab, _sdp, slots) => {
            if (tab !== "media-tab") throw new Error("wrong server tab");
            if (window.__media.failOffer) throw new Error("offer rejected");
            window.__media.offers.push(slots);
            if (window.__media.delayOffer) await new Promise(resolve => { window.__media.finishOffer = resolve; });
            return "answer";
        },
            SetScreenShareForTab: async tab => tab === "media-tab" ? "" : "wrong tab",
            SetScreenShareQualityForTab: async (tab, active, height) => {
                if (tab !== "media-tab") throw new Error("wrong tab");
                window.__media.shareControl = [tab, active, height];
                if (window.__media.delayShareControl) return await new Promise(resolve => { window.__media.finishShareControl = resolve; });
                return "";
            },
            SetVideoQualityForTab: async (tab, quality) => {
                if (tab !== "media-tab") throw new Error("wrong tab");
                window.__media.qualityControl = [tab, quality];
                if (window.__media.delayQualityControl) return await new Promise(resolve => { window.__media.finishQualityControl = resolve; });
                return "";
            } } } };
        Object.defineProperty(navigator.mediaDevices, "getDisplayMedia", { configurable: true, value: async (options) => {
            window.__media.captureOptions = options;
            const stream = capture();
            window.__media.displayTracks.push(...stream.getTracks());
            if (window.__media.delayCapture) await new Promise(resolve => { window.__media.finishCapture = resolve; });
            return stream;
        } });
        window.CropTarget = { fromElement: async () => ({}) };
        MediaStreamTrack.prototype.cropTo = async () => {};
        window.__media.video = await import("/src/video.js");
        (await import("/src/modal.js")).initModalSystem();
        document.getElementById("voice-screen").onclick = () => window.__media.video.shareToggle();
    });
});

test("server limits constrain screen capture and divide the budget with the camera", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.mediaLimits = { video_max_width: 320, video_max_height: 180, video_max_bitrate: 1000000 };
    });
    await page.locator("#voice-screen").click();
    await expect(page.locator(".media-limit-hint")).toContainText("320 × 180");
    await expect(page.locator(".media-limit-hint")).toContainText("1000 kbit/s");
    await page.getByRole("button", { name: "Start sharing", exact: true }).click();
    await expect.poll(() => page.evaluate(() => !!window.__noxa.state.screenSharing)).toBe(true);
    const result = await page.evaluate(() => ({
        options: window.__media.captureOptions,
        budgets: window.__noxa.state.pc.getSenders().map(sender => sender.getParameters().encodings[0].maxBitrate),
    }));
    expect(result.options.video.width).toEqual({ ideal: 320, max: 320 });
    expect(result.options.video.height).toEqual({ ideal: 180, max: 180 });
    expect(result.budgets).toEqual([425000, 425000]);
    await page.locator("#voice-screen").click();
    await page.getByRole("dialog").getByRole("button", { name: "Stop sharing", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__media.cameraSender.getParameters().encodings[0].maxBitrate)).toBe(850000);
});

test("live bitrate updates preserve captures and redistribute both sender budgets", async ({ page }) => {
    await page.locator("#voice-screen").click();
    await page.getByRole("button", { name: "Start sharing", exact: true }).click();
    await expect.poll(() => page.evaluate(() => !!window.__noxa.state.screenSharing)).toBe(true);
    const result = await page.evaluate(async () => {
        const { state } = window.__noxa;
        const offers = window.__media.offers.length;
        const changed = await window.__media.video.applyVideoLimits({ video_max_width: 0, video_max_height: 0, video_max_bitrate: 600000 });
        const budgets = state.pc.getSenders().map(sender => sender.getParameters().encodings[0].maxBitrate);
        const duplicate = await window.__media.video.applyVideoLimits({ ...state.mediaLimits });
        await window.__media.video.applyVideoLimits({ video_max_width: 0, video_max_height: 0, video_max_bitrate: 0 });
        return { changed, duplicate, budgets, restored: state.pc.getSenders().map(sender => sender.getParameters().encodings[0].maxBitrate),
            offers: window.__media.offers.length - offers, camera: window.__media.camera.readyState, screen: state.shareStream.getVideoTracks()[0].readyState };
    });
    expect(result).toEqual({ changed: { changed: true, dimensionsChanged: false }, duplicate: { changed: false, dimensionsChanged: false },
        budgets: [255000, 255000], restored: [undefined, 1500000], offers: 0, camera: "live", screen: "live" });
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

test("dimension relaxation retains a screen's original preset and disabled capture stays disabled", async ({ page }) => {
    await page.locator("#voice-screen").click();
    await page.getByRole("button", { name: "Start sharing", exact: true }).click();
    await expect.poll(() => page.evaluate(() => !!window.__noxa.state.screenSharing)).toBe(true);
    const result = await page.evaluate(async () => {
        const track = window.__noxa.state.shareStream.getVideoTracks()[0];
        const applied = [];
        track.enabled = false;
        let settings = track.getSettings();
        track.getSettings = () => settings;
        track.applyConstraints = async constraints => {
            applied.push(constraints);
            settings = { width: constraints.width.ideal, height: constraints.height.ideal };
        };
        await window.__media.video.applyVideoLimits({ video_max_width: 320, video_max_height: 180 });
        await window.__media.video.applyVideoLimits({ video_max_width: 0, video_max_height: 0 });
        return { applied, enabled: track.enabled, state: track.readyState };
    });
    expect(result.applied.map(value => value.width)).toEqual([{ ideal: 320, max: 320 }, { ideal: 1280 }]);
    expect(result.applied.map(value => value.height)).toEqual([{ ideal: 180, max: 180 }, { ideal: 720 }]);
    expect(result.enabled).toBe(false);
    expect(result.state).toBe("live");
});

for (const source of ["camera", "screen"]) {
    for (const waitAt of ["queue", "caps"]) {
        test(`${source} waiting for ${waitAt} rechecks dimensions before its offer`, async ({ page }) => {
            await page.evaluate(async ({ source, waitAt }) => {
                const state = window.__noxa.state;
                if (waitAt === "queue") {
                    window.__media.blocker = window.__media.video.queuePeerNegotiation(state.pc,
                        () => new Promise(resolve => { window.__media.finishWait = resolve; }));
                } else {
                    const sender = window.__media.cameraSender;
                    const set = sender.setParameters;
                    sender.setParameters = async parameters => {
                        await new Promise(resolve => { window.__media.finishWait = resolve; });
                        sender.setParameters = set;
                        return set(parameters);
                    };
                }
                if (source === "camera") {
                    state.localStream.removeTrack(window.__media.camera);
                    window.__media.camera.stop();
                    await window.__media.cameraSender.replaceTrack(null);
                    navigator.mediaDevices.getUserMedia = async () => {
                        const stream = document.createElement("canvas").captureStream(1);
                        window.__media.candidate = stream.getVideoTracks()[0];
                        return stream;
                    };
                    window.__media.cameraStart = window.__media.video.cameraToggle();
                }
            }, { source, waitAt });
            if (source === "screen") {
                await page.locator("#voice-screen").click();
                await page.getByRole("button", { name: "Start sharing", exact: true }).click();
            }
            await expect.poll(() => page.evaluate(() => typeof window.__media.finishWait)).toBe("function");
            await expect.poll(() => page.evaluate(source => source === "camera" ? !!window.__media.candidate : !!window.__media.displayTracks.length, source)).toBe(true);
            await page.evaluate(() => {
                window.__noxa.state.mediaLimits = { video_max_width: 100, video_max_height: 50 };
                window.__media.finishWait();
            });
            await expect.poll(() => page.evaluate(source => (source === "camera" ? window.__media.candidate : window.__media.displayTracks[0]).readyState, source)).toBe("ended");
            expect(await page.evaluate(() => window.__media.offers)).toEqual([]);
        });
    }
}

test("pending screen capture is checked against the latest limits", async ({ page }) => {
    await page.evaluate(() => { window.__media.delayCapture = true; });
    await page.locator("#voice-screen").click();
    await page.getByRole("button", { name: "Start sharing", exact: true }).click();
    await expect.poll(() => page.evaluate(() => typeof window.__media.finishCapture)).toBe("function");
    await page.evaluate(() => {
        window.__noxa.state.mediaLimits = { video_max_width: 100, video_max_height: 100 };
        window.__media.finishCapture();
    });
    await expect.poll(() => page.evaluate(() => window.__media.displayTracks[0].readyState)).toBe("ended");
    expect(await page.evaluate(() => window.__media.offers)).toEqual([]);
});

test("capture ignoring server dimensions is stopped before publication", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.mediaLimits = { video_max_width: 100, video_max_height: 100 };
    });
    await page.locator("#voice-screen").click();
    await page.getByRole("button", { name: "Start sharing", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__media.displayTracks.map(track => track.readyState))).toEqual(["ended"]);
    expect(await page.evaluate(() => window.__media.offers)).toEqual([]);
    expect(await page.evaluate(() => window.__media.errors.join(" "))).toContain("server's video size limit");
});

test("failed encoder setup stops capture before a screen offer", async ({ page }) => {
    await page.evaluate(() => { window.__media.failCaps = true; });
    await page.locator("#voice-screen").click();
    await page.getByRole("button", { name: "Start sharing", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__media.displayTracks.map(track => track.readyState))).toEqual(["ended"]);
    expect(await page.evaluate(() => window.__media.offers)).toEqual([]);
    expect(await page.evaluate(() => window.__media.errors.join(" "))).toContain("Could not apply video quality limits");
});

test("competing camera and screen offers cannot publish a track before its caps succeed", async ({ page }) => {
    await page.evaluate(async () => {
        const state = window.__noxa.state;
        state.localStream.removeTrack(window.__media.camera);
        window.__media.camera.stop();
        await window.__media.cameraSender.replaceTrack(null);
        state.mediaLimits = { video_max_bitrate: 1000000 };
        navigator.mediaDevices.getUserMedia = async () => document.createElement("canvas").captureStream(1);
        const sender = window.__media.cameraSender;
        const set = sender.setParameters;
        let count = 0;
        sender.setParameters = async parameters => {
            count++;
            if (count === 1) await new Promise(resolve => { window.__media.finishCameraCaps = resolve; });
            if (count === 2) await new Promise((_resolve, reject) => { window.__media.failShareCaps = () => reject(new Error("share cap failure")); });
            return set(parameters);
        };
        window.__media.cameraStart = window.__media.video.cameraToggle();
    });
    await expect.poll(() => page.evaluate(() => typeof window.__media.finishCameraCaps)).toBe("function");
    await page.locator("#voice-screen").click();
    await page.getByRole("dialog").getByRole("button", { name: "Start sharing", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__media.displayTracks.length)).toBe(1);
    await page.evaluate(async () => { window.__media.finishCameraCaps(); await window.__media.cameraStart; });
    await expect.poll(() => page.evaluate(() => typeof window.__media.failShareCaps)).toBe("function");
    expect(await page.evaluate(() => window.__media.offers.flatMap(slots => slots.map(slot => slot.slot)))).toEqual(["cam"]);
    await page.evaluate(() => window.__media.failShareCaps());
    await expect.poll(() => page.evaluate(() => window.__media.displayTracks[0].readyState)).toBe("ended");
    expect(await page.evaluate(() => window.__media.offers.flatMap(slots => slots.map(slot => slot.slot)))).toEqual(["cam"]);
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

test("ending capture closes a pending region picker and releases the share button", async ({ page }) => {
    await page.locator("#voice-screen").click();
    await page.getByRole("radio", { name: "Region of this app", exact: true }).check();
    await page.getByRole("dialog").getByRole("button", { name: "Start sharing", exact: true }).click();
    await expect(page.locator(".region-box")).toBeVisible();
    await page.evaluate(() => {
        const track = window.__media.displayTracks[0];
        track.stop();
        track.onended();
    });
    await expect(page.locator(".region-box")).toHaveCount(0);
    await expect(page.locator("#voice-screen")).toBeEnabled();
    expect(await page.evaluate(() => window.__media.offers)).toEqual([]);
});

test("screen sharing has its own declared track and never replaces the camera", async ({ page }) => {
    for (let attempt = 0; attempt < 2; attempt++) {
        await page.locator("#voice-screen").click();
        await page.getByRole("dialog").getByRole("button", { name: "Start sharing", exact: true }).click();
        await expect.poll(() => page.evaluate(() => !!window.__noxa.state.screenSharing)).toBe(true);
        expect(await page.evaluate(() => window.__media.video.trackSlots().map(t => t.slot).sort())).toEqual(["cam", "screen"]);
        expect(await page.evaluate(() => window.__media.cameraSender.track === window.__media.camera)).toBe(true);
        expect(await page.evaluate(() => window.__media.offers.at(-1).map(t => t.slot).sort())).toEqual(["cam", "screen"]);
        await page.locator("#voice-screen").click();
        await page.getByRole("dialog").getByRole("button", { name: "Stop sharing", exact: true }).click();
        await expect.poll(() => page.evaluate(() => !!window.__noxa.state.screenSharing)).toBe(false);
        expect(await page.evaluate(() => window.__media.cameraSender.track === window.__media.camera)).toBe(true);
        expect(await page.evaluate(() => window.__media.video.trackSlots().map(t => t.slot))).toEqual(["cam"]);
    }
    expect(await page.evaluate(() => window.__media.errors)).toEqual([]);
});

test("failed screen publication releases capture and preserves camera", async ({ page }) => {
    await page.evaluate(() => { window.__media.failOffer = true; });
    await page.locator("#voice-screen").click();
    await page.getByRole("dialog").getByRole("button", { name: "Start sharing", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__media.displayTracks.map(t => t.readyState))).toEqual(["ended"]);
    expect(await page.evaluate(() => window.__media.cameraSender.track === window.__media.camera)).toBe(true);
    expect(await page.evaluate(() => window.__media.video.trackSlots().map(t => t.slot))).toEqual(["cam"]);
    await expect(page.locator("#voice-screen")).toBeEnabled();
});

test("denied screen confirmation releases capture and preserves camera", async ({ page }) => {
    await page.evaluate(() => { window.__media.delayShareControl = true; });
    await page.locator("#voice-screen").click();
    await page.getByRole("dialog").getByRole("button", { name: "Start sharing", exact: true }).click();
    await expect.poll(() => page.evaluate(() => typeof window.__media.finishShareControl)).toBe("function");
    expect(await page.evaluate(() => window.__media.shareControl.slice(0, 2))).toEqual(["media-tab", true]);
    await expect(page.locator("#voice-screen")).toBeDisabled();
    await page.evaluate(() => window.__media.finishShareControl("permission denied"));
    await expect.poll(() => page.evaluate(() => window.__media.displayTracks.map(t => t.readyState))).toEqual(["ended"]);
    expect(await page.evaluate(() => window.__media.camera.readyState)).toBe("live");
    await expect(page.locator("#voice-screen")).toBeEnabled();
    expect(await page.evaluate(() => window.__media.errors.join(" "))).toContain("permission denied");
});

test("quality failure from an old peer cannot report against the replacement session", async ({ page }) => {
    await page.evaluate(() => {
        window.__media.delayQualityControl = true;
        window.__media.video.setIdleQualityOverride(true);
    });
    await expect.poll(() => page.evaluate(() => typeof window.__media.finishQualityControl)).toBe("function");
    expect(await page.evaluate(() => window.__media.qualityControl)).toEqual(["media-tab", "low"]);
    await page.evaluate(() => {
        window.__noxa.state.serverGeneration++;
        window.__media.finishQualityControl("old denial");
    });
    expect(await page.evaluate(() => window.__media.errors)).toEqual([]);
});

test("capture finishing after a server switch cannot publish into the new session", async ({ page }) => {
    await page.evaluate(() => { window.__media.delayCapture = true; });
    await page.locator("#voice-screen").click();
    await page.getByRole("dialog").getByRole("button", { name: "Start sharing", exact: true }).click();
    await expect(page.locator("#voice-screen")).toBeDisabled();
    await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.__media.finishCapture(); });
    await expect.poll(() => page.evaluate(() => window.__media.displayTracks.map(t => t.readyState))).toEqual(["ended"]);
    expect(await page.evaluate(() => window.__media.offers)).toEqual([]);
    expect(await page.evaluate(() => window.__media.video.trackSlots().map(t => t.slot))).toEqual(["cam"]);
});
