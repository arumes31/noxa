import { expect, test } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.route("**/__group_test__", route => route.fulfill({ contentType: "text/html", body: '<!doctype html><html><head><title>Private groups</title><link rel="stylesheet" href="/src/conversations.css"></head><body><aside id="sidebar"><div id="channel-tree"><button>Lobby</button></div></aside><nav id="test-group-list"></nav><main id="center"><section id="chat-pane"><p>Channel messages</p><textarea aria-label="Message Lobby">channel draft</textarea></section></main><button id="launch">Groups</button></body></html>' }));
    await page.goto("/__group_test__");
    await page.evaluate(async () => {
        window.__noxa = { state: { activeTabID: "server-a", serverGeneration: 1, myUniqueID: "local-device-key", myClientID: "a", clients: [{ client_id: "a", unique_id: "alice", nickname: "Alice" }, { client_id: "b", unique_id: "bob", nickname: "Bob" }], settings: { blocked_users: [] } } };
        window.__groups = [
            { id: "g1", name: "Raid <script>", owner: "alice", revision: 1, epoch: 1, members: [{ unique_id: "alice", pending: false, joined_epoch: 1 }, { unique_id: "bob", pending: false, joined_epoch: 1 }] },
            { id: "g2", name: "Friends", owner: "bob", revision: 1, epoch: 1, members: [{ unique_id: "alice", pending: true, joined_epoch: 0 }, { unique_id: "bob", pending: false, joined_epoch: 1 }] },
        ];
        window.__groupCalls = [];
        window.__notifications = [];
        window.__noxaNotify = { notify: (...args) => window.__notifications.push(args) };
        window.__groupMessages = [];
        window.__readPositions = {};
        window.go = { main: { App: {
            async SaveSettings(settings) { window.__noxa.state.settings = structuredClone(settings); return ""; },
            async GetSettings() { return structuredClone(window.__noxa.state.settings); },
            async ConversationForTab(tab, command) {
                window.__groupCalls.push([tab, structuredClone(command)]);
                if (window.__groupGate) await window.__groupGate;
                const group = window.__groups.find(group => group.id === command.id);
                if (command.action === "mark_read") {
                    if (window.__markGate) await window.__markGate;
                    window.__readPositions[command.id] = Math.max(window.__readPositions[command.id] || 0, command.read_message_id);
                    return { conversations: [], messages: [] };
                }
                if (["delete", "delete_message", "remove", "transfer"].includes(command.action)) {
                    if (group.owner !== "alice") throw new Error("owner required");
                    if (window.__mutationFailure) throw new Error("Mutation rejected");
                    if (command.action === "delete") window.__groups = window.__groups.filter(g => g.id !== group.id);
                    if (command.action === "delete_message") window.__groupMessages = window.__groupMessages.filter(m => m.conversation_id !== group.id || m.id !== command.message_id);
                    if (command.action === "remove") group.members = group.members.filter(m => m.unique_id !== command.target);
                    if (command.action === "transfer") group.owner = command.target;
                    group.revision++;
                }
                if (command.action === "accept") { group.members.find(member => member.unique_id === "alice").pending = false; group.revision++; group.epoch++; }
                if (command.action === "invite") {
                    if (window.__inviteFailure) throw new Error(window.__inviteFailure);
                    group.members.push({ unique_id: command.target, pending: true, joined_epoch: 0 });
                    group.revision++;
                }
                if (command.action === "create") {
                    const created = { id: "g3", name: command.name, owner: "alice", revision: 1, epoch: 1, members: [{ unique_id: "alice", pending: false, joined_epoch: 1 }] };
                    window.__groups.push(created); return { conversations: [structuredClone(created)], messages: [] };
                }
                const summaries = (command.action === "list" ? window.__groups : group ? [group] : []).map(group => ({ ...group,
                    read_message_id: window.__readPositions[group.id] || 0,
                    latest_message_id: Math.max(0, ...window.__groupMessages.filter(message => message.conversation_id === group.id).map(message => message.id)),
                    unread_count: window.__groupMessages.filter(message => message.conversation_id === group.id && message.from_unique_id !== "alice" && message.id > (window.__readPositions[group.id] || 0)).length,
                }));
                // The Go client marks every group message whose envelope it authenticated
                // as enc_verified; unverified bodies are refused by the frontend.
                return { conversations: structuredClone(summaries), messages: command.action === "history" ? structuredClone(window.__groupMessages.filter(message => message.conversation_id === command.id).map(message => ({ enc_verified: true, ...message }))) : [] };
            },
            async SendConversationForTab(tab, id, body, reference) {
                window.__groupCalls.push([tab, { action: "send", id, body, reference }]);
                window.__groupMessages.unshift({ id: window.__groupMessages.length + 1, conversation_id: id, from_unique_id: "alice", body, created_at: Math.floor(Date.now() / 1000) });
                return window.__groupMessages.length;
            },
        } } };
        const module = await import("/src/conversations.js");
        window.__conversationModule = module;
        document.getElementById("launch").onclick = module.openConversations;
        // A small navigation host for this component fixture. The full Messages
        // integration is exercised in app-shell.spec.js.
        window.__noxa.openDirectMessages = () => module.openConversationsAt("");
        window.addEventListener("noxa-private-groups-changed", () => {
            const nav = document.getElementById("test-group-list"); nav.replaceChildren();
            for (const group of module.listedPrivateGroups()?.groups || []) {
                const button = document.createElement("button"); button.className = "group-list-item";
                button.dataset.groupId = group.id; button.setAttribute("aria-label", group.name + (group.members.some(m => m.unique_id === "alice" && m.pending) ? " · Invitation" : "")); button.textContent = group.name;
                if (group.unread_count) { const badge = document.createElement("span"); badge.className = "group-unread"; badge.textContent = group.unread_count; button.append(badge); }
                button.onclick = () => module.openConversationsAt(group.id); nav.append(button);
            }
        });
        module.initConversations();
    });
    await page.getByRole("button", { name: "Groups", exact: true }).click();
    await expect(page.locator(".group-list-item")).toHaveCount(2);
});

test("group voice recording survives incoming messages and sends to the same group", async ({ page }) => {
    await page.evaluate(() => {
        window.__voiceContext = new AudioContext();
        const tone = window.__voiceContext.createOscillator(), output = window.__voiceContext.createMediaStreamDestination();
        tone.connect(output); tone.start(); window.__voiceStream = output.stream;
        navigator.mediaDevices.getUserMedia = async () => output.stream;
        window.__encodedVoiceChunks = 0;
        const Recorder = window.MediaRecorder;
        window.MediaRecorder = class extends Recorder {
            constructor(...args) {
                super(...args);
                this.addEventListener('dataavailable', event => { if (event.data.size) window.__encodedVoiceChunks++; });
            }
        };
        window.go.main.App.UploadConversationAttachmentForTab = async () => "[file:recording#key#voice.weba]";
    });
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await page.getByRole("button", { name: "Record voice message", exact: true }).click();
    await page.getByRole("button", { name: "Start recording", exact: true }).click();
    await expect(page.getByRole("button", { name: "Stop recording", exact: true })).toBeEnabled();
    await page.evaluate(() => {
        window.__groupMessages.push({ id: 101, conversation_id: "g1", from_unique_id: "bob", body: "Message while recording", created_at: 1 });
        return window.__conversationModule.conversationChanged({ id: "g1", message_id: 101 });
    });
    await expect(page.locator(".group-messages")).toContainText("Message while recording");
    // The next recording status tick must preserve the session after refresh.
    await expect(page.locator(".voice-message-dialog [role=status]:not(.voice-recording-warning)")).toContainText("Recording");
    expect(await page.evaluate(() => window.__voiceStream.getTracks()[0].readyState)).toBe("live");
    await expect.poll(() => page.evaluate(() => window.__encodedVoiceChunks)).toBeGreaterThan(0);
    await page.getByRole("button", { name: "Stop recording", exact: true }).click();
    await expect(page.getByRole("button", { name: "Send voice message", exact: true })).toBeEnabled();
    await page.evaluate(() => window.__conversationModule.conversationChanged({ id: "g1", message_id: 102 }));
    await page.getByRole("button", { name: "Send voice message", exact: true }).click();
    await expect(page.locator(".voice-message-dialog")).toHaveCount(0);
    expect(await page.evaluate(() => window.__groupCalls.filter(([, request]) => request.action === "send").map(([, request]) => request.id))).toEqual(["g1"]);
    expect(await page.evaluate(() => window.__voiceStream.getTracks()[0].readyState)).toBe("ended");
    await page.evaluate(() => window.__voiceContext.close());
});

test("group voice rerecord uses a new send reference while retries retain the original", async ({ page }) => {
    await page.evaluate(() => {
        window.__voiceContext = new AudioContext();
        const tone = window.__voiceContext.createOscillator(); tone.start();
        navigator.mediaDevices.getUserMedia = async () => {
            const output = window.__voiceContext.createMediaStreamDestination(); tone.connect(output); return output.stream;
        };
        window.__voiceUploads = 0; window.__voiceSends = []; window.__storedVoice = new Map();
        window.__encodedVoiceChunks = 0;
        const Recorder = window.MediaRecorder;
        window.MediaRecorder = class extends Recorder {
            constructor(...args) {
                super(...args);
                this.addEventListener('dataavailable', event => { if (event.data.size) window.__encodedVoiceChunks++; });
            }
        };
        window.go.main.App.UploadConversationAttachmentForTab = async () => `[file:recording-${++window.__voiceUploads}#key#voice.weba]`;
        window.go.main.App.SendConversationForTab = async (_tab, _group, body, reference) => {
            window.__voiceSends.push({ body, reference });
            // Simulate the server's reference deduplication and a lost response.
            if (!window.__storedVoice.has(reference)) window.__storedVoice.set(reference, body);
            if (window.__voiceSends.length <= 2) throw new Error("send response lost");
            return 1;
        };
    });
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await page.getByRole("button", { name: "Record voice message", exact: true }).click();
    const record = async () => {
        const previousChunks = await page.evaluate(() => window.__encodedVoiceChunks);
        await page.getByRole("button", { name: "Start recording", exact: true }).click();
        await expect(page.getByRole("button", { name: "Stop recording", exact: true })).toBeEnabled();
        await expect(page.locator(".voice-message-dialog [role=status]:not(.voice-recording-warning)")).toContainText("Recording");
        await expect.poll(() => page.evaluate(() => window.__encodedVoiceChunks)).toBeGreaterThan(previousChunks);
        await page.getByRole("button", { name: "Stop recording", exact: true }).click();
        await expect(page.getByRole("button", { name: "Send voice message", exact: true })).toBeEnabled();
    };
    await record();
    for (let attempt = 1; attempt <= 2; attempt++) {
        await page.getByRole("button", { name: "Send voice message", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__voiceSends.length)).toBe(attempt);
        await expect(page.locator(".voice-message-dialog [role=status]:not(.voice-recording-warning)")).toContainText("send response lost");
    }
    await record();
    await page.getByRole("button", { name: "Send voice message", exact: true }).click();
    await expect(page.locator(".voice-message-dialog")).toHaveCount(0);
    const sends = await page.evaluate(() => window.__voiceSends);
    expect(sends[0].reference).toBe(sends[1].reference);
    expect(sends[0].body).toBe(sends[1].body);
    expect(sends[2].body).not.toBe(sends[0].body);
    expect(sends[2].reference).not.toBe(sends[0].reference);
    expect(await page.evaluate(() => [...window.__storedVoice.values()])).toEqual([sends[0].body, sends[2].body]);
    expect(await page.evaluate(() => window.__voiceUploads)).toBe(2);
    await page.evaluate(() => window.__voiceContext.close());
});

for (const change of ["navigation", "membership"]) {
    test(`group voice recording stops after ${change} changes`, async ({ page }) => {
        await page.evaluate(() => {
            window.__voiceContext = new AudioContext();
            const output = window.__voiceContext.createMediaStreamDestination(); window.__voiceStream = output.stream;
            navigator.mediaDevices.getUserMedia = async () => output.stream;
        });
        await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
        await page.getByRole("button", { name: "Record voice message", exact: true }).click();
        await page.getByRole("button", { name: "Start recording", exact: true }).click();
        await expect(page.getByRole("button", { name: "Stop recording", exact: true })).toBeEnabled();
        await page.evaluate(async change => {
            if (change === "navigation") {
                // A quick away-and-back transition must still cancel capture.
                document.querySelector('.group-list-item[data-group-id="g2"]').click();
                document.querySelector('.group-list-item[data-group-id="g1"]').click();
            } else {
                window.__groups[0].members = window.__groups[0].members.filter(member => member.unique_id !== "alice");
                await window.__conversationModule.conversationChanged({ id: "g1" });
            }
        }, change);
        await expect(page.locator(".voice-message-dialog")).toHaveCount(0);
        expect(await page.evaluate(() => window.__voiceStream.getTracks()[0].readyState)).toBe("ended");
        expect(await page.evaluate(() => window.__groupCalls.filter(([, request]) => request.action === "send"))).toEqual([]);
        await page.evaluate(() => window.__voiceContext.close());
    });
}

test("group refresh preserves typing focus and selection; notifications honor mute and current membership", async ({ page }) => {
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    const composer = page.getByRole("textbox", { name: "Message this group" });
    await composer.fill("working draft");
    await composer.evaluate(input => input.setSelectionRange(3, 7));
    await page.evaluate(() => window.__conversationModule.conversationChanged({ id: "g1", message_id: 1 }));
    await expect(composer).toBeFocused();
    await expect.poll(() => composer.evaluate(input => [input.selectionStart, input.selectionEnd])).toEqual([3, 7]);
    await page.getByRole("checkbox", { name: "Mute group notifications" }).check();
    await page.getByRole("button", { name: "Back to channel", exact: true }).click();
    await page.evaluate(async () => {
        await window.__conversationModule.conversationChanged({ id: "g1", message_id: 2 });
        await window.__conversationModule.conversationChanged({ id: "g2", invitation: true });
        await window.__conversationModule.conversationChanged({ id: "g2", invitation: true });
        await window.__conversationModule.conversationChanged({ id: "removed", message_id: 3 });
    });
    expect(await page.evaluate(() => window.__notifications.map(args => args[1]))).toEqual(["Invitation to Friends"]);
    await page.evaluate(async () => {
        window.__noxa.state.settings.muted_conversations = [];
        await window.__conversationModule.conversationChanged({ id: "g1", message_id: 4 });
        await window.__conversationModule.conversationChanged({ id: "g1", message_id: 4 });
    });
    expect(await page.evaluate(() => window.__notifications.map(args => args[1]))).toEqual(["Invitation to Friends", "New message in Raid <script>"]);
});

test("sidebar group unread badges deduplicate events and clear on opening the group", async ({ page }) => {
    await page.getByRole("button", { name: "Back to channel", exact: true }).click();
    const group = page.locator('#test-group-list .group-list-item[data-group-id="g1"]');
    await page.evaluate(async () => {
        for (const id of [101, 102]) window.__groupMessages.push({ id, conversation_id: "g1", from_unique_id: "bob", body: "Unread", created_at: 1 });
        await window.__conversationModule.conversationChanged({ id: "g1", message_id: 101 });
        await window.__conversationModule.conversationChanged({ id: "g1", message_id: 101 });
        await window.__conversationModule.conversationChanged({ id: "g1", message_id: 102 });
    });
    await expect(group.locator(".group-unread")).toHaveText("2");
    await expect(page.locator('#test-group-list [data-group-id="g2"] .group-unread')).toHaveCount(0);
    await group.click();
    await expect(page.locator("#private-groups .group-content h3")).toHaveText("Raid <script>");
    await expect(group.locator(".group-unread")).toHaveCount(0);
    await page.getByRole("textbox", { name: "Message this group" }).focus();
    await page.evaluate(() => { window.__groupMessages.push({ id: 103, conversation_id: "g1", from_unique_id: "bob", body: "Viewed", created_at: 1 }); return window.__conversationModule.conversationChanged({ id: "g1", message_id: 103 }); });
    await expect(group.locator(".group-unread")).toHaveCount(0);
    await page.getByRole("button", { name: "Back to channel", exact: true }).click();
    await page.evaluate(async () => {
        window.__groupMessages.push({ id: 104, conversation_id: "g1", from_unique_id: "bob", body: "Unread again", created_at: 1 });
        await window.__conversationModule.conversationChanged({ id: "g1", message_id: 103 });
        await window.__conversationModule.conversationChanged({ id: "g1", message_id: 104 });
    });
    await expect(group.locator(".group-unread")).toHaveText("1");
});

test("durable unread state and invitations survive reset", async ({ page }) => {
    await page.getByRole("button", { name: "Back to channel", exact: true }).click();
    await page.evaluate(() => {
        window.__groupMessages.push({ id: 501, conversation_id: "g1", from_unique_id: "bob", body: "While offline", created_at: 1 });
        window.__conversationModule.resetConversations(); window.__conversationModule.initConversations();
    });
    const group = page.locator('[data-group-id="g1"].group-list-item');
    await expect(group.locator(".group-unread")).toHaveText("1");
    expect(await page.evaluate(() => window.__conversationModule.listedPrivateGroups().groups.filter(g => g.members.some(m => m.unique_id === "alice" && m.pending)).length)).toBe(1);
    await group.click();
    await expect.poll(() => page.evaluate(() => window.__readPositions.g1)).toBe(501);
    await page.evaluate(() => { window.__conversationModule.resetConversations(); window.__conversationModule.initConversations(); });
    await expect(group.locator(".group-unread")).toHaveCount(0);

});

test("Messages snapshot retains group call activity across refresh", async ({ page }) => {
    await page.evaluate(async () => {
        window.__groups[0].active_call_count = 1; window.__groups[0].call_participant_count = 2;
        await window.__conversationModule.conversationChanged();
    });
    expect(await page.evaluate(() => window.__conversationModule.listedPrivateGroups().groups[0])).toMatchObject({ active_call_count: 1, call_participant_count: 2 });
});

test("new visible messages queue behind a delayed read acknowledgment", async ({ page }) => {
    await page.evaluate(() => {
        window.__markGate = new Promise(resolve => { window.__releaseMark = resolve; });
        window.__groupMessages.push({ id: 701, conversation_id: "g1", from_unique_id: "bob", body: "First", created_at: 1 });
    });
    await page.locator('[data-group-id="g1"]').click();
    await expect.poll(() => page.evaluate(() => window.__groupCalls.some(([, request]) => request.action === "mark_read"))).toBe(true);
    await page.evaluate(() => {
        window.__groupMessages.push({ id: 702, conversation_id: "g1", from_unique_id: "bob", body: "Second", created_at: 1 });
        window.__conversationModule.conversationChanged({ id: "g1", message_id: 702 });
    });
    await expect(page.locator(".group-message-body").filter({ hasText: "Second" })).toBeVisible();
    await page.evaluate(() => { window.__markGate = null; window.__releaseMark(); });
    await expect.poll(() => page.evaluate(() => window.__readPositions.g1)).toBe(702);
});

test("reading older group messages does not mark new arrivals read until scrolling down", async ({ page }) => {
    await page.addStyleTag({ content: '.group-messages { height: 100px; max-height: 100px; flex: none; }' });
    await page.evaluate(() => { window.__groupMessages = Array.from({ length: 30 }, (_, i) => ({ id: i + 1, conversation_id: "g1", from_unique_id: "bob", body: `Message ${i}`, created_at: 1 })); });
    await page.locator('[data-group-id="g1"]').click();
    await expect.poll(() => page.evaluate(() => window.__readPositions.g1)).toBe(30);
    await page.locator(".group-messages").evaluate(node => { node.scrollTop = 0; });
    await page.evaluate(() => {
        window.__groupMessages.push({ id: 31, conversation_id: "g1", from_unique_id: "bob", body: "New arrival", created_at: 1 });
        window.__conversationModule.conversationChanged({ id: "g1", message_id: 31 });
    });
    await expect(page.locator('.group-list-item[data-group-id="g1"] .group-unread')).toHaveText("1");
    await expect(page.locator(".group-message-body").filter({ hasText: "New arrival" })).toHaveCount(1);
    expect(await page.evaluate(() => window.__readPositions.g1)).toBe(30);
    await page.locator(".group-messages").evaluate(node => { node.scrollTop = node.scrollHeight; });
    await expect.poll(() => page.evaluate(() => window.__readPositions.g1)).toBe(31);
});


for (const delayedMethod of ["SaveSettings", "GetSettings"]) test(`group mute ${delayedMethod} completion after reset cannot overwrite the replacement server`, async ({ page }) => {
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await page.evaluate(delayedMethod => {
        window.__muteGetCalls = 0;
        window.__oldMuteSettings = { ...structuredClone(window.__noxa.state.settings), muted_conversations: ["g1"] };
        window.go.main.App.SaveSettings = async () => {
            if (delayedMethod === "SaveSettings") await new Promise(resolve => { window.__releaseMute = resolve; });
            return "";
        };
        window.go.main.App.GetSettings = async () => {
            window.__muteGetCalls++;
            if (delayedMethod === "GetSettings") await new Promise(resolve => { window.__releaseMute = resolve; });
            return structuredClone(window.__oldMuteSettings);
        };
        const mute = document.querySelector(".group-mute input");
        const change = mute.onchange;
        mute.onchange = event => { window.__muteComplete = change(event); };
    }, delayedMethod);
    await page.getByRole("checkbox", { name: "Mute group notifications" }).check();
    await expect.poll(() => page.evaluate(() => typeof window.__releaseMute)).toBe("function");
    await page.evaluate(async () => {
        window.__conversationModule.resetConversations();
        window.__noxa.state.activeTabID = "server-b";
        window.__noxa.state.serverGeneration++;
        window.__noxa.state.settings = { blocked_users: [], muted_conversations: ["server-b-group"], language: "en" };
        window.__releaseMute();
        await window.__muteComplete;
    });
    expect(await page.evaluate(() => window.__noxa.state.settings)).toEqual({ blocked_users: [], muted_conversations: ["server-b-group"], language: "en" });
    expect(await page.evaluate(() => window.__muteGetCalls)).toBe(delayedMethod === "GetSettings" ? 1 : 0);
    await expect(page.locator("#private-groups")).toHaveCount(0);
});

test("same-group refresh preserves expanded members and invitation draft focus and selection", async ({ page }) => {
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await page.locator(".group-members > summary").click();
    const invite = page.getByLabel("Member identity", { exact: true });
    await invite.fill("charlie-device-identity");
    await invite.evaluate(input => input.setSelectionRange(3, 12, "backward"));
    await page.evaluate(() => {
        window.__groups[0].revision++;
        window.__groupMessages.push({ id: 71, conversation_id: "g1", from_unique_id: "bob", body: "Refresh completed marker", created_at: Math.floor(Date.now() / 1000) });
        window.__conversationModule.conversationChanged({ id: "g1" });
    });
    await expect(page.locator(".group-message-body")).toHaveText("Refresh completed marker");
    expect(await invite.evaluate(input => ({
        expanded: input.closest("details").open,
        value: input.value,
        focused: document.activeElement === input,
        selection: [input.selectionStart, input.selectionEnd, input.selectionDirection],
    }))).toEqual({ expanded: true, value: "charlie-device-identity", focused: true, selection: [3, 12, "backward"] });
});

test("failed group invitation preserves expanded member form and its draft", async ({ page }) => {
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await page.locator(".group-members > summary").click();
    const invite = page.getByLabel("Member identity", { exact: true });
    await invite.fill("charlie-device-identity");
    await invite.evaluate(input => input.setSelectionRange(2, 9));
    const historyBefore = await page.evaluate(() => window.__groupCalls.filter(([, request]) => request.action === "history").length);
    await page.evaluate(() => { window.__inviteFailure = "Invitation unavailable"; });
    await invite.press("Enter");
    await expect(page.locator(".group-status")).toContainText("Invitation unavailable");
    await expect.poll(() => page.evaluate(() => window.__groupCalls.filter(([, request]) => request.action === "history").length)).toBeGreaterThan(historyBefore);
    expect(await invite.evaluate(input => ({
        expanded: input.closest("details").open,
        value: input.value,
        focused: document.activeElement === input,
        selection: [input.selectionStart, input.selectionEnd],
    }))).toEqual({ expanded: true, value: "charlie-device-identity", focused: true, selection: [2, 9] });
    expect(await page.evaluate(() => window.__groups[0].members.some(member => member.unique_id === "charlie-device-identity"))).toBe(false);
});

test("successful group invitation clears only the submitted invite draft and keeps members expanded", async ({ page }) => {
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await page.getByRole("textbox", { name: "Message this group" }).fill("Unsent group message");
    await page.locator(".group-members > summary").click();
    const invite = page.getByLabel("Member identity", { exact: true });
    await invite.fill("charlie-device-identity");
    await invite.press("Enter");
    await expect(page.locator(".group-member")).toHaveCount(3);
    expect(await invite.evaluate(input => ({ expanded: input.closest("details").open, value: input.value }))).toEqual({ expanded: true, value: "" });
    await expect(page.getByRole("textbox", { name: "Message this group" })).toHaveValue("Unsent group message");
    expect(await page.evaluate(() => window.__groupCalls.filter(([, command]) => command.action === "invite").map(([, command]) => command.target))).toEqual(["charlie-device-identity"]);
});

test("invitation stays disabled until the new membership revision has arrived", async ({ page }) => {
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await page.locator(".group-members summary").click();
    await page.evaluate(() => {
        const original = window.go.main.App.ConversationForTab;
        let holdList = false;
        window.go.main.App.ConversationForTab = async (tab, request) => {
            if (request.action === "list" && holdList) await new Promise(resolve => { window.__releaseInviteRefresh = resolve; });
            const result = await original(tab, request);
            if (request.action === "invite") holdList = true;
            return result;
        };
    });
    await page.getByLabel("Member identity", { exact: true }).fill("charlie");
    const invite = page.getByRole("button", { name: "Invite", exact: true });
    await invite.click();
    await expect.poll(() => page.evaluate(() => typeof window.__releaseInviteRefresh)).toBe("function");
    await expect(invite).toBeDisabled();
    await page.evaluate(() => window.__releaseInviteRefresh());
    await expect(invite).toBeEnabled();
});

test("retrying an uncertain send after refresh keeps its deduplication reference", async ({ page }) => {
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await page.getByRole("textbox", { name: "Message this group" }).fill("one message");
    await page.evaluate(() => {
        const original = window.go.main.App.SendConversationForTab;
        window.go.main.App.SendConversationForTab = async (...args) => { await original(...args); throw new Error("reply lost"); };
    });
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await expect(page.locator(".group-status")).toContainText("reply lost");
    await expect(page.locator(".group-message")).toHaveCount(1);
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__groupCalls.filter(([, command]) => command.action === "send").length)).toBe(2);
    const references = await page.evaluate(() => window.__groupCalls.filter(([, command]) => command.action === "send").map(([, command]) => command.reference));
    expect(references[0]).toBe(references[1]);
});

test("older group history remains retryable after a transient page failure", async ({ page }, testInfo) => {
    await page.evaluate(() => {
        const original = window.go.main.App.ConversationForTab;
        window.__historyRequests = [];
        window.go.main.App.ConversationForTab = async (tab, command) => {
            if (command.action !== "history") return original(tab, command);
            window.__historyRequests.push(command.before_id);
            if (window.__historyRequests.length === 2) throw new Error("History temporarily unavailable");
            const newest = command.before_id ? command.before_id - 1 : 50;
            return { conversations: [], messages: Array.from({ length: 25 }, (_, index) => ({
                id: newest - index, conversation_id: "g1", from_unique_id: "bob",
                body: `Historical message ${newest - index}`, created_at: 1,
            })) };
        };
    });
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await expect(page.locator(".group-message")).toHaveCount(25);
    const older = page.getByRole("button", { name: "Load older messages", exact: true });
    await older.click();
    await expect(page.locator(".group-status")).toContainText("History temporarily unavailable");
    await page.screenshot({ path: testInfo.outputPath("history-retry.png") });
    await expect(older).toBeEnabled();
    await older.click();
    await expect(page.locator(".group-message")).toHaveCount(50);
    await expect(page.locator(".group-status")).toHaveText("");
    expect(await page.evaluate(() => window.__historyRequests)).toEqual([0, 26, 26]);
    expect(await page.locator(".group-message").evaluateAll(rows => new Set(rows.map(row => row.dataset.messageId)).size)).toBe(50);
});

test("an unavailable saved group reference does not constrain the next group history", async ({ page }) => {
    await page.evaluate(async () => {
        window.__groupMessages.push({ id: 200, conversation_id: "g1", from_unique_id: "bob", body: "Latest accessible message", created_at: 1 });
        const original = window.go.main.App.ConversationForTab;
        window.go.main.App.ConversationForTab = async (tab, command) => {
            const result = await original(tab, command);
            if (command.action === "history" && command.before_id) result.messages = result.messages.filter(message => message.id < command.before_id);
            return result;
        };
        await window.__conversationModule.openConversationsAt("removed-group", 123);
    });
    await expect(page.locator(".group-status")).toContainText("no longer have access");
    await page.getByRole("button", { name: /Raid <script>/ }).click();
    await expect(page.locator(".group-messages")).toContainText("Latest accessible message");
    await expect(page.locator(".group-status")).toHaveText("");
    expect(await page.evaluate(() => window.__groupCalls.filter(([, request]) => request.action === "history").at(-1)[1].before_id)).toBe(0);
});

test("a saved group message jump retries a failed history request before resolving its target", async ({ page }) => {
    await page.evaluate(async () => {
        window.__groupMessages.push({ id: 123, conversation_id: "g1", from_unique_id: "bob", body: "Saved group message", created_at: 1 });
        const original = window.go.main.App.ConversationForTab;
        let failOnce = true;
        window.go.main.App.ConversationForTab = async (tab, command) => {
            if (failOnce && command.action === "history") { failOnce = false; throw new Error("History retry required"); }
            return original(tab, command);
        };
        await window.__conversationModule.openConversationsAt("g1", 123);
    });
    await expect(page.locator(".group-status")).toContainText("History retry required");
    await page.getByRole("button", { name: "Load older messages", exact: true }).click();
    await expect(page.locator('.group-message[data-message-id="123"]')).toHaveClass(/message-reference-flash/);
    await expect(page.locator(".group-status")).toHaveText("");
});

test("invitations require acceptance; drafts never move into another private group", async ({ page }) => {
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await page.getByRole("textbox", { name: "Message this group" }).fill("raid-only draft");
    await page.getByRole("button", { name: "Friends · Invitation", exact: true }).click();
    await expect(page.locator(".group-composer")).toHaveCount(0);
    await page.getByRole("button", { name: "Accept invitation", exact: true }).click();
    await expect(page.getByRole("textbox", { name: "Message this group" })).toHaveValue("");
    await page.getByRole("textbox", { name: "Message this group" }).fill("friends-only draft");
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await expect(page.getByRole("textbox", { name: "Message this group" })).toHaveValue("raid-only draft");
    await page.evaluate(() => window.__conversationModule.conversationChanged());
    await expect(page.getByRole("textbox", { name: "Message this group" })).toHaveValue("raid-only draft");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await expect(page.locator(".group-message-body")).toHaveText("raid-only draft");
    await expect(page.getByRole("textbox", { name: "Message this group" })).toHaveValue("");
    const sends = await page.evaluate(() => window.__groupCalls.filter(([, command]) => command.action === "send"));
    expect(sends).toHaveLength(1);
    expect(sends[0][0]).toBe("server-a");
    expect(sends[0][1].id).toBe("g1");
    await expect(page.locator(".group-content script")).toHaveCount(0);
});

test("creation and pending responses stay bound to their original server", async ({ page }) => {
    await page.evaluate(() => window.__conversationModule.createPrivateGroup("New group"));
    await expect(page.locator(".group-content h3")).toHaveText("New group");
    await page.evaluate(() => {
        window.__groupGate = new Promise(resolve => { window.__releaseGroup = resolve; });
        window.__conversationModule.conversationChanged();
    });
    await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.__noxa.state.activeTabID = "server-b"; window.__releaseGroup(); });
    await expect(page.locator(".group-content h3")).toHaveText("New group");
    expect(await page.evaluate(() => window.__groupCalls.every(([tab]) => tab === "server-a"))).toBe(true);
});

test("private group workspace has no duplicate sidebar list or creation form", async ({ page }) => {
    await expect(page.locator("#sidebar .group-list-item, .group-create, .group-sidebar")).toHaveCount(0);
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await expect(page.locator("#center #private-groups")).toBeVisible();
    await expect(page.locator(".group-content h3")).toHaveText("Raid <script>");
    await expect(page.locator(".dlg-overlay")).toHaveCount(0);
});

test("back to channel restores channel chat and retains the private group draft", async ({ page }) => {
    await page.locator("#test-group-list").getByRole("button", { name: "Raid <script>", exact: true }).click();
    await page.getByRole("textbox", { name: "Message this group" }).fill("Unsent private draft");
    await page.getByRole("button", { name: "Back to channel", exact: true }).click();
    await expect(page.locator("#chat-pane")).toBeVisible();
    await expect(page.getByRole("textbox", { name: "Message Lobby", exact: true })).toHaveValue("channel draft");
    await expect(page.locator("#private-groups")).toBeHidden();
    await expect(page.locator("#test-group-list .group-list-item")).toHaveCount(2);
    await page.locator("#test-group-list").getByRole("button", { name: "Raid <script>", exact: true }).click();
    await expect(page.locator("#center #private-groups")).toBeVisible();
    await expect(page.getByRole("textbox", { name: "Message this group" })).toHaveValue("Unsent private draft");
    await page.evaluate(() => window.__conversationModule.closeConversations());
    await expect(page.locator("#chat-pane")).toBeVisible();
    await expect(page.locator("#private-groups")).toBeHidden();
});

test("reset disposes server groups and ignores delayed old-server responses", async ({ page }) => {
    await page.locator("#test-group-list").getByRole("button", { name: "Raid <script>", exact: true }).click();
    await page.getByRole("textbox", { name: "Message this group" }).fill("Server A private draft");
    await page.evaluate(() => {
        window.__oldServerReplies = [];
        const original = window.go.main.App.ConversationForTab;
        window.go.main.App.ConversationForTab = async (tab, command) => {
            const response = await original(tab, command);
            if (tab === "server-a" && command.action === "list") {
                return new Promise(resolve => window.__oldServerReplies.push(() => resolve(response)));
            }
            return response;
        };
        window.__conversationModule.conversationChanged();
    });
    await expect.poll(() => page.evaluate(() => window.__oldServerReplies.length)).toBeGreaterThan(0);
    await page.evaluate(() => {
        window.__conversationModule.resetConversations();
        window.__noxa.state.activeTabID = "server-b";
        window.__noxa.state.serverGeneration++;
        // Same group ID on another server must not recover the old draft.
        window.__groups = [{ id: "g1", name: "Server B team", owner: "alice", revision: 1, epoch: 1, members: [{ unique_id: "alice", pending: false, joined_epoch: 1 }] }];
        window.__groupMessages = [];
    });
    await expect(page.locator("#test-group-list .group-list-item")).toHaveCount(0);
    await expect(page.locator("#private-groups")).toHaveCount(0);
    await expect(page.locator("#chat-pane")).toBeVisible();
    await page.evaluate(() => window.__conversationModule.initConversations());
    await expect(page.locator("#test-group-list .group-list-item")).toHaveText(["Server B team"]);
    await page.evaluate(async () => {
        for (const release of window.__oldServerReplies.splice(0)) release();
        await new Promise(requestAnimationFrame);
    });
    await expect(page.locator("#test-group-list .group-list-item")).toHaveText(["Server B team"]);
    await expect(page.locator("#private-groups")).toBeHidden();
    await expect(page.locator("#chat-pane")).toBeVisible();
    await page.locator("#test-group-list").getByRole("button", { name: "Server B team", exact: true }).click();
    await expect(page.getByRole("textbox", { name: "Message this group" })).toHaveValue("");
    await expect(page.locator("#center #private-groups .group-content h3")).toHaveText("Server B team");
});

test("owner can delete another member's message only after confirmation", async ({ page }) => {
    await page.evaluate(() => { window.__groupMessages.push({ id: 7, conversation_id: "g1", from_unique_id: "bob", body: "Moderate this", created_at: 1 }); });
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await page.getByRole("button", { name: "Delete message", exact: true }).click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(page.locator(".group-messages")).toContainText("Moderate this");
    await page.getByRole("button", { name: "Delete message", exact: true }).click();
    await page.getByRole("button", { name: "Confirm", exact: true }).click();
    await expect(page.locator(".group-message")).toHaveCount(0);
    expect(await page.evaluate(() => window.__groupCalls.find(([,c]) => c.action === "delete_message"))).toEqual(["server-a", { action: "delete_message", id: "g1", revision: 1, target: "", message_id: 7 }]);
});
for (const action of ["Remove", "Make owner", "Delete group"]) test(`owner management: ${action}`, async ({ page }) => {
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await page.locator(".group-members > summary").click();
    await page.getByRole("button", { name: action, exact: true }).click();
    await page.getByRole("button", { name: "Confirm", exact: true }).click();
    if (action === "Delete group") await expect(page.locator('#test-group-list [data-group-id="g1"]')).toHaveCount(0);
    else if (action === "Make owner") await expect(page.getByRole("button", { name: "Delete group", exact: true })).toHaveCount(0);
    else await expect.poll(() => page.evaluate(() => window.__groups[0].members.length)).toBe(1);
});
test("non-owner never sees owner deletion controls", async ({ page }) => {
    await page.evaluate(() => { window.__groups[0].owner = "bob"; window.__groupMessages.push({id:7,conversation_id:"g1",from_unique_id:"bob",body:"Private",created_at:1}); });
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    await expect(page.getByRole("button", { name: "Delete group", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Delete message", exact: true })).toHaveCount(0);
});
