import { expect, test } from "@playwright/test";

test.use({ launchOptions: { args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream", "--autoplay-policy=no-user-gesture-required"] }, permissions: ["microphone"] });

for (const signalingMode of ["normal", "gathering", "candidates-first"]) {
test(`real peer call captures only after acceptance and tears down without joining a channel${signalingMode === "normal" ? "" : ` (${signalingMode})`}`, async ({ browser }) => {
    const pages = new Map();
    const heldDescriptions = new Map();
    const candidateBatches = [];
    let call = null;
    let revision = 0;
    const notify = () => { for (const page of pages.values()) void page.evaluate(id => window.__callsModule.privateCallChanged({ id }), call.id).catch(() => {}); };
    for (const uid of ["alice", "bob"]) {
        const page = await browser.newPage({ permissions: ["microphone"] }); pages.set(uid, page);
        await page.route("**/__call_test__", route => route.fulfill({ contentType: "text/html", body: '<!doctype html><title>Call test</title><link rel="stylesheet" href="/src/private-calls.css">' }));
        await page.exposeFunction("requestCall", async request => {
            if (request.action === "start") call = { id: "call-1", caller: uid, revision: ++revision, created_at: Math.floor(Date.now()/1000), ring_until: Math.floor(Date.now()/1000)+30, ended_at: 0, participants: [{ unique_id: "alice", client_id: "a", state: "accepted" }, { unique_id: "bob", client_id: "b", state: "ringing" }] };
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
        await page.evaluate(async ({ uid, signalingMode }) => {
            window.__captures = 0; window.__streams = []; window.__peers = []; window.__channelMoves = 0; window.__warnings = []; window.__hostCandidates = 0;
            const getMedia = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
            navigator.mediaDevices.getUserMedia = async constraints => { window.__captures++; const stream = await getMedia(constraints); window.__streams.push(stream); return stream; };
            const OriginalPeer = window.RTCPeerConnection;
            window.RTCPeerConnection = class extends OriginalPeer {
                constructor(...args) {
                    super(...args); window.__peers.push(this);
                    this.addEventListener("icecandidate", event => { if (event.candidate) window.__hostCandidates++; });
                    if (signalingMode === "gathering") Object.defineProperty(this, "iceGatheringState", { get: () => "gathering" });
                }
            };
            window.__noxa = { state: { activeTabID: "server-a", serverGeneration: 1, myUniqueID: "local-device-key", myClientID: uid, myChannelID: 0, muted: false, deafened: false, settings: { activation_mode: "continuous", blocked_users: [] }, clients: [{ client_id: "alice", unique_id: "alice", nickname: "Alice" }, { client_id: "bob", unique_id: "bob", nickname: "Bob" }] }, toast: message => window.__warnings.push(message), resetVoiceSession() {} };
            window.go = { main: { App: {
                PrivateCallForTab: (_tab, request) => window.requestCall(request),
                GetICEServersForTab: async () => [],
                SendPrivateCallDescriptionForTab: (_tab, id, target, type, sdp) => window.relayDescription(id, target, type, sdp),
                OpenPrivateCallDescriptionForTab: async (_tab, signal) => signal.description,
                StopPrivateCallForTab: () => window.stopCall(),
                JoinChannelForTab: async () => { window.__channelMoves++; return ""; },
            } } };
            window.__callsModule = await import("/src/private-calls.js");
        }, { uid, signalingMode });
    }
    const alice = pages.get("alice"), bob = pages.get("bob");
    try {
        await alice.evaluate(() => window.__callsModule.startPrivateCall("bob"));
        await expect(bob.getByRole("button", { name: "Accept", exact: true })).toBeVisible();
        expect(await alice.evaluate(() => window.__captures)).toBe(0);
        expect(await bob.evaluate(() => window.__captures)).toBe(0);
        await bob.getByRole("button", { name: "Accept", exact: true }).click();
        for (const page of pages.values()) {
            await expect.poll(() => page.evaluate(() => window.__peers.map(peer => peer.connectionState)), { timeout: 15000 }).toEqual(["connected"]);
            expect(await page.evaluate(() => window.__captures)).toBe(1);
            expect(await page.evaluate(() => window.__channelMoves)).toBe(0);
            if (signalingMode !== "normal") {
                expect(await page.evaluate(() => window.__hostCandidates)).toBeGreaterThan(0);
                if (signalingMode === "gathering") expect(await page.evaluate(() => window.__peers[0].iceGatheringState)).toBe("gathering");
            }
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
        await alice.getByRole("button", { name: "Mute microphone", exact: true }).click();
        await expect.poll(() => alice.evaluate(() => window.__streams[0].getAudioTracks()[0].enabled)).toBe(false);
        await bob.getByRole("button", { name: "Mute call audio", exact: true }).click();
        expect(await bob.evaluate(() => [...document.querySelectorAll("audio")].every(audio => audio.muted))).toBe(true);
        await alice.getByRole("button", { name: "End call", exact: true }).click();
        for (const page of pages.values()) {
            await expect(page.locator(".private-call-panel")).toHaveCount(0);
            expect(await page.evaluate(() => window.__streams[0].getTracks().every(track => track.readyState === "ended"))).toBe(true);
        }
    } catch (error) {
        for (const [uid, page] of pages) console.log(uid, JSON.stringify(await page.evaluate(async () => ({ warnings: window.__warnings, peers: await Promise.all(window.__peers.map(async peer => ({ state: peer.connectionState, transceivers: peer.getTransceivers().map(t => ({ current: t.currentDirection, desired: t.direction, sender: t.sender.track?.readyState, enabled: t.sender.track?.enabled, receiver: t.receiver.track?.readyState })), stats: [...(await peer.getStats()).values()].filter(s => s.type === "outbound-rtp" || s.type === "inbound-rtp").map(s => ({ type: s.type, kind: s.kind, sent: s.bytesSent, received: s.bytesReceived })) }))) }))));
        throw error;
    } finally { await Promise.all([...pages.values()].map(page => page.close())); }
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
