import { expect, test } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.route("**/__group_test__", route => route.fulfill({ contentType: "text/html", body: '<!doctype html><html><head><title>Private groups</title><link rel="stylesheet" href="/src/conversations.css"></head><body><aside id="sidebar"><div id="channel-tree"><button>Lobby</button></div></aside><main id="center"><section id="chat-pane"><p>Channel messages</p><textarea aria-label="Message Lobby">channel draft</textarea></section></main><button id="launch">Groups</button></body></html>' }));
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
                return { conversations: structuredClone(summaries), messages: command.action === "history" ? structuredClone(window.__groupMessages.filter(message => message.conversation_id === command.id)) : [] };
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
        module.initConversations();
    });
    await page.getByRole("button", { name: "Groups", exact: true }).click();
    await expect(page.locator(".group-list-item")).toHaveCount(2);
});

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
    const group = page.locator('#sidebar .group-list-item[data-group-id="g1"]');
    await page.evaluate(async () => {
        for (const id of [101, 102]) window.__groupMessages.push({ id, conversation_id: "g1", from_unique_id: "bob", body: "Unread", created_at: 1 });
        await window.__conversationModule.conversationChanged({ id: "g1", message_id: 101 });
        await window.__conversationModule.conversationChanged({ id: "g1", message_id: 101 });
        await window.__conversationModule.conversationChanged({ id: "g1", message_id: 102 });
    });
    await expect(group.locator(".group-unread")).toHaveText("2");
    await expect(page.locator('#sidebar [data-group-id="g2"] .group-unread')).toHaveCount(0);
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

test("durable unread state and invitation totals survive reset and remain visible when collapsed", async ({ page }) => {
    await page.getByRole("button", { name: "Back to channel", exact: true }).click();
    await page.evaluate(() => {
        window.__groupMessages.push({ id: 501, conversation_id: "g1", from_unique_id: "bob", body: "While offline", created_at: 1 });
        window.__conversationModule.resetConversations(); window.__conversationModule.initConversations();
    });
    const group = page.locator('[data-group-id="g1"].group-list-item');
    await expect(group.locator(".group-unread")).toHaveText("1");
    await page.locator(".group-collapse").click();
    await expect(page.locator(".group-summary-unread")).toHaveText("1");
    await expect(page.locator(".group-summary-invitations")).toHaveText("1");
    await expect(page.locator(".group-summary-unread")).toBeVisible();
    await page.locator(".group-collapse").click(); await group.click();
    await expect.poll(() => page.evaluate(() => window.__readPositions.g1)).toBe(501);
    await page.evaluate(() => { window.__conversationModule.resetConversations(); window.__conversationModule.initConversations(); });
    await expect(group.locator(".group-unread")).toHaveCount(0);
    await expect(page.locator(".group-summary-unread")).toBeHidden();
});

test("sidebar search filters groups and call activity appears in each matching row", async ({ page }) => {
    await page.evaluate(() => {
        window.__groups[0].active_call_count = 1; window.__groups[0].call_participant_count = 2;
        window.__conversationModule.conversationChanged();
    });
    await expect(page.locator('[data-group-id="g1"] .group-call-status')).toHaveText("Call · 2");
    await page.evaluate(() => { window.__noxa.state.treeFilter = "friends"; window.__conversationModule.filterConversations(); });
    await expect(page.locator('[data-group-id="g1"]')).toBeHidden();
    await expect(page.locator('[data-group-id="g2"]')).toBeVisible();
    await page.evaluate(() => { window.__noxa.state.treeFilter = "no match"; window.__conversationModule.filterConversations(); });
    await expect(page.locator(".group-search-empty")).toBeVisible();
    await page.evaluate(() => { window.__noxa.state.treeFilter = ""; window.__conversationModule.filterConversations(); });
    await expect(page.locator('[data-group-id="g1"]')).toBeVisible();
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
    await expect(page.locator(".group-message p").filter({ hasText: "Second" })).toBeVisible();
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
    await expect(page.locator(".group-message p").filter({ hasText: "New arrival" })).toHaveCount(1);
    expect(await page.evaluate(() => window.__readPositions.g1)).toBe(30);
    await page.locator(".group-messages").evaluate(node => { node.scrollTop = node.scrollHeight; });
    await expect.poll(() => page.evaluate(() => window.__readPositions.g1)).toBe(31);
});

test("sidebar refresh preserves the focused group button for keyboard navigation", async ({ page }) => {
    const group = page.locator('#sidebar .group-list-item[data-group-id="g1"]');
    await group.focus();
    await page.evaluate(() => {
        window.__groups[0].name = "Refreshed raid team";
        window.__conversationModule.conversationChanged({ id: "g1" });
    });
    await expect(group).toHaveText("Refreshed raid team");
    await expect(group).toBeFocused();
    await group.press("Enter");
    await expect(page.locator("#private-groups .group-content h3")).toHaveText("Refreshed raid team");
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
    await expect(page.locator(".group-message p")).toHaveText("Refresh completed marker");
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
    await expect(page.locator(".group-message p")).toHaveText("raid-only draft");
    await expect(page.getByRole("textbox", { name: "Message this group" })).toHaveValue("");
    const sends = await page.evaluate(() => window.__groupCalls.filter(([, command]) => command.action === "send"));
    expect(sends).toHaveLength(1);
    expect(sends[0][0]).toBe("server-a");
    expect(sends[0][1].id).toBe("g1");
    await expect(page.locator(".group-content script")).toHaveCount(0);
});

test("creation and pending responses stay bound to their original server", async ({ page }) => {
    await page.locator(".group-create").getByRole("textbox", { name: "Group name", exact: true }).fill("New group");
    await page.locator(".group-create").getByRole("button", { name: "Create group", exact: true }).click();
    await expect(page.locator(".group-content h3")).toHaveText("New group");
    await page.evaluate(() => {
        window.__groupGate = new Promise(resolve => { window.__releaseGroup = resolve; });
        window.__conversationModule.conversationChanged();
    });
    await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.__noxa.state.activeTabID = "server-b"; window.__releaseGroup(); });
    await expect(page.locator(".group-content h3")).toHaveText("New group");
    expect(await page.evaluate(() => window.__groupCalls.every(([tab]) => tab === "server-a"))).toBe(true);
});

test("private groups live below channels and open in the main workspace without a dialog", async ({ page }) => {
    await expect(page.locator("#sidebar .group-list-item")).toHaveCount(2);
    expect(await page.locator("#sidebar .group-list-item").first().evaluate(item => {
        const tree = document.getElementById("channel-tree");
        return !tree.contains(item) && !!(tree.compareDocumentPosition(item) & Node.DOCUMENT_POSITION_FOLLOWING);
    })).toBe(true);
    await expect(page.locator(".dlg-overlay, [role=dialog]")).toHaveCount(0);
    await page.locator("#sidebar").getByRole("button", { name: "Raid <script>", exact: true }).click();
    await expect(page.locator("#center #private-groups")).toBeVisible();
    await expect(page.locator("#center #private-groups .group-content h3")).toHaveText("Raid <script>");
    await expect(page.locator("#chat-pane")).toBeHidden();
    await expect(page.locator(".dlg-overlay, [role=dialog]")).toHaveCount(0);
    await page.evaluate(() => window.__conversationModule.initConversations());
    await expect(page.locator("#sidebar .group-list-item")).toHaveCount(2);
    await expect(page.locator("#private-groups")).toHaveCount(1);
});

async function prepareSplitLayout(page) {
    await page.evaluate(() => {
        window.__conversationModule.resetConversations();
        const tree = document.getElementById("channel-tree");
        const split = document.createElement("div"); split.id = "sidebar-scroll";
        tree.before(split); split.append(tree);
        document.getElementById("sidebar").style.cssText = "display:flex;flex-direction:column;height:600px;width:280px";
        window.__conversationModule.initConversations();
    });
}

test("empty groups stay at the bottom by default and both lists scroll independently", async ({ page }) => {
    await page.evaluate(() => { window.__groups = []; });
    await prepareSplitLayout(page);
    await expect(page.locator(".group-sidebar-status")).toHaveText("No private groups yet.");
    const split = await page.locator("#sidebar-scroll").boundingBox();
    const divider = await page.getByRole("separator").boundingBox();
    expect(divider.y).toBeGreaterThan(split.y + split.height * 0.8);
    await page.evaluate(() => {
        const tree = document.getElementById("channel-tree");
        for (let i = 0; i < 50; i++) { const row = document.createElement("p"); row.textContent = `Channel ${i}`; tree.append(row); }
        window.__groups = Array.from({ length: 30 }, (_, i) => ({ id: `g${i}`, name: `Private group ${i}`, owner: "alice", members: [{ unique_id: "alice", pending: false }] }));
        window.__conversationModule.conversationChanged();
    });
    await expect(page.locator(".group-list-item")).toHaveCount(30);
    for (const selector of ["#channel-tree", ".group-sidebar-body"]) {
        expect(await page.locator(selector).evaluate(node => { node.scrollTop = 100; return node.scrollTop; })).toBeGreaterThan(0);
    }
    expect(await page.locator("#sidebar-scroll").evaluate(node => node.scrollHeight <= node.clientHeight)).toBe(true);
    await page.setViewportSize({ width: 320, height: 640 });
    await page.locator("#sidebar").evaluate(node => { node.style.height = "320px"; });
    await expect(page.getByRole("button", { name: "Private groups", exact: true })).toBeVisible();
    expect(await page.locator("#sidebar-scroll").evaluate(node => node.scrollHeight <= node.clientHeight)).toBe(true);
});

test("group divider supports dragging, keyboard limits and saved sizing", async ({ page }) => {
    await prepareSplitLayout(page);
    const divider = page.getByRole("separator", { name: "Resize channels and private groups" });
    await expect(divider).toBeVisible();
    const tree = page.locator("#channel-tree");
    const before = (await tree.boundingBox()).height;
    const bar = await divider.boundingBox();
    await page.mouse.move(bar.x + bar.width / 2, bar.y + bar.height / 2);
    await page.mouse.down(); await page.mouse.move(bar.x + bar.width / 2, bar.y - 100, { steps: 5 }); await page.mouse.up();
    expect((await tree.boundingBox()).height).toBeLessThan(before - 80);
    await divider.focus(); await divider.press("End");
    await expect(divider).toHaveAttribute("aria-valuenow", "80");
    await divider.press("ArrowUp");
    await expect(divider).toHaveAttribute("aria-valuenow", "75");
    const savedHeight = (await tree.boundingBox()).height;
    await page.evaluate(() => { window.__conversationModule.resetConversations(); window.__conversationModule.initConversations(); });
    await expect(divider).toHaveAttribute("aria-valuenow", "75");
    expect((await tree.boundingBox()).height).toBeCloseTo(savedHeight, 0);
    await divider.press("Home"); await divider.press("ArrowUp");
    await expect(divider).toHaveAttribute("aria-valuenow", "20");
});

test("collapsing groups preserves active chat, persists and makes room for channels", async ({ page }) => {
    await prepareSplitLayout(page);
    await page.getByRole("button", { name: "Raid <script>", exact: true }).click();
    const composer = page.getByRole("textbox", { name: "Message this group" });
    await composer.fill("Keep this draft");
    const toggle = page.getByRole("button", { name: "Private groups", exact: true });
    const height = (await page.locator("#channel-tree").boundingBox()).height;
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(page.locator(".group-list")).toBeHidden();
    await expect(page.getByRole("separator")).toBeHidden();
    await expect(composer).toHaveValue("Keep this draft");
    expect((await page.locator("#channel-tree").boundingBox()).height).toBeGreaterThan(height + 100);
    await page.evaluate(() => { window.__conversationModule.resetConversations(); window.__conversationModule.initConversations(); });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await toggle.focus(); await toggle.press("Enter");
    await expect(page.locator(".group-list")).toBeVisible();
    await toggle.press("Space");
    await page.getByRole("button", { name: "Create group", exact: true }).click();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    await expect(page.getByRole("textbox", { name: "Group name" })).toBeFocused();
});

test("back to channel restores channel chat and retains the private group draft", async ({ page }) => {
    await page.locator("#sidebar").getByRole("button", { name: "Raid <script>", exact: true }).click();
    await page.getByRole("textbox", { name: "Message this group" }).fill("Unsent private draft");
    await page.getByRole("button", { name: "Back to channel", exact: true }).click();
    await expect(page.locator("#chat-pane")).toBeVisible();
    await expect(page.getByRole("textbox", { name: "Message Lobby", exact: true })).toHaveValue("channel draft");
    await expect(page.locator("#private-groups")).toBeHidden();
    await expect(page.locator("#sidebar .group-list-item")).toHaveCount(2);
    await page.locator("#sidebar").getByRole("button", { name: "Raid <script>", exact: true }).click();
    await expect(page.locator("#center #private-groups")).toBeVisible();
    await expect(page.getByRole("textbox", { name: "Message this group" })).toHaveValue("Unsent private draft");
    await page.evaluate(() => window.__conversationModule.closeConversations());
    await expect(page.locator("#chat-pane")).toBeVisible();
    await expect(page.locator("#private-groups")).toBeHidden();
});

test("reset disposes server groups and ignores delayed old-server responses", async ({ page }) => {
    await page.locator("#sidebar").getByRole("button", { name: "Raid <script>", exact: true }).click();
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
    await expect(page.locator("#sidebar .group-list-item")).toHaveCount(0);
    await expect(page.locator("#private-groups")).toHaveCount(0);
    await expect(page.locator("#chat-pane")).toBeVisible();
    await page.evaluate(() => window.__conversationModule.initConversations());
    await expect(page.locator("#sidebar .group-list-item")).toHaveText(["Server B team"]);
    await page.evaluate(async () => {
        for (const release of window.__oldServerReplies.splice(0)) release();
        await new Promise(requestAnimationFrame);
    });
    await expect(page.locator("#sidebar .group-list-item")).toHaveText(["Server B team"]);
    await expect(page.locator("#private-groups")).toBeHidden();
    await expect(page.locator("#chat-pane")).toBeVisible();
    await page.locator("#sidebar").getByRole("button", { name: "Server B team", exact: true }).click();
    await expect(page.getByRole("textbox", { name: "Message this group" })).toHaveValue("");
    await expect(page.locator("#center #private-groups .group-content h3")).toHaveText("Server B team");
});
