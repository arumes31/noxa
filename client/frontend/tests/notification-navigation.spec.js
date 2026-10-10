import { expect, test } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__noticeEvents = new Map(); window.__noticeCalls = []; window.__nativeTab = "b";
        window.__noticeTabs = ["a", "b"].map(id => ({ id, addr: `${id}.test`, nickname: id, connected: true, active: id === "b" }));
        window.__emitNotice = (name, value) => { for (const callback of window.__noticeEvents.get(name) || []) callback(value); };
        window.__activateNotice = tab => {
            window.__nativeTab = tab;
            window.__emitNotice("tab_reset", tab);
            window.__emitNotice("tab_identity", { tab_id: tab, identity_uid: `identity-${tab}` });
            Object.assign(window.__noxa.state, { myChannelID: tab === "a" ? 1 : 3, myClientID: `self-${tab}`, lastConnect: { addr: `${tab}.test` },
                channels: [{ ChannelID: 1, Name: "Voice A" }, { ChannelID: 2, Name: "Target Channel" }, { ChannelID: 3, Name: "Voice B" }],
                clients: [{ client_id: `self-${tab}`, unique_id: `account-${tab}`, nickname: "Me", channel_id: tab === "a" ? 1 : 3 }] });
            window.__noxaChat.onSubscriptions({ channel_ids: [1, 2, 3] });
            window.__emitNotice("tab_replay_done", tab);
        };
        window.runtime = { EventsOn(name, callback) { const set = window.__noticeEvents.get(name) || new Set(); set.add(callback); window.__noticeEvents.set(name, set); return () => set.delete(callback); }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            window.__noticeCalls.push([method, ...args]);
            if (method === "GetSettings") return { language: "en", onboarding_done: true, alpha_dismissed: "test", bookmarks: [], activation_mode: "ptt", chat_max_lines: 200, dnd_enabled: true };
            if (method === "ListTabs") return structuredClone(window.__noticeTabs);
            if (method === "SetActiveTab") { window.__activateNotice(args[0]); return ""; }
            if (method === "SessionInfoForTab") return { connected: true, client_id: `self-${args[0]}`, nickname: "Me", is_guest: false };
            if (method === "DMHistoryContextForTab") return { tab_id: args[0], identity_uid: `identity-${args[0]}`, activation: "0", identity_revision: "0" };
            if (method === "DMHistoryLoadForContext") return [{ seq: 12, client_msg_id: "dm-target", body: `Exact DM on ${args[0].tab_id}`, from_unique_id: "bob", from_nickname: "Bob", sent_at: 1700000000, enc_verified: true }];
            if (method === "ChatHistoryForTab") {
                if (window.__noticeHistoryGate && (!window.__noticeHistoryBefore || args[2] === window.__noticeHistoryBefore)) await window.__noticeHistoryGate;
                return { messages: args[2] && !window.__noticeRevoked ? [{ id: args[2] - 1, channel_id: args[1], body: `Exact channel message on ${args[0]}`, from_nickname: "Bob", sent_at: 1700000000, enc_verified: true }] : [] };
            }
            if (method === "ConversationForTab") {
                const group = { id: "g1", name: "Friends", owner: "bob", revision: 1, epoch: 1, members: [{ unique_id: `account-${args[0]}`, pending: !!window.__noticeInvitation }, { unique_id: "bob" }] };
                const messages = window.__noticeGroupMessages || [{ id: 61, conversation_id: "g1", from_unique_id: "bob", body: `Exact group message on ${args[0]}`, created_at: 1700000000 }];
                return { conversations: window.__noticeRevoked ? [] : [group], messages: args[1].action === "history" ? messages.filter(message => !args[1].before_id || message.id < args[1].before_id).sort((a, b) => b.id - a.id).slice(0, 25) : [] };
            }
            if (method === "DiscussionForTab") {
                const request = args[1];
                if (request.action === "get" && window.__noticeThreadGate) await window.__noticeThreadGate;
                return { action: request.action, channel_id: request.channel_id, thread_id: request.thread_id || 0, forum: false, tags: [], has_more: false,
                    threads: window.__noticeRevoked ? [] : [{ id: 8, title: "Target thread", tags: [], joined: true, subscribed: true, unread: true }],
                    messages: request.thread_id ? [{ id: 71, body: `Exact thread message on ${args[0]}`, from_nickname: "Bob", enc_verified: true, sent_at: 1700000000 }] : [] };
            }
            if (["GetPermissions", "SubscriptionsForTab", "ListIdentities"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest", "GamingOverlayAvailable"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            return "";
        }; } }) } };
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { enumerateDevices: async () => [], addEventListener() {} } });
    });
    await page.goto("/"); await page.waitForFunction(() => !!window.__noxaPolish && !!window.__noxaChat);
    await page.evaluate(() => { window.__noxa.showWorkspace(false); window.__noxa.syncOwnChannel = () => {}; window.__activateNotice("a"); });
});

async function clickNotice(page, text, { confirm = true } = {}) {
    await page.evaluate(() => window.__noxaPolish.openNotifCenter());
    await expect(page.locator(".nc-row").filter({ hasText: text })).toBeVisible();
    await page.locator(".nc-close").click();
    await page.evaluate(() => { window.__activateNotice("b"); window.__noxaPolish.openNotifCenter(); });
    const row = page.locator(".nc-row").filter({ hasText: text });
    await row.focus(); await page.keyboard.press("Enter");
    if (confirm) await page.getByRole("button", { name: "Switch server and open", exact: true }).click();
}
async function unchangedVoice(page) {
    expect(await page.evaluate(() => window.__noticeCalls.filter(([name]) => /JoinChannel|LeaveChannel/.test(name)))).toEqual([]);
    expect(await page.evaluate(() => [window.__noxa.state.activeTabID, window.__noxa.state.myChannelID])).toEqual(["a", 1]);
}

test("channel message alert switches server and opens exact authorized message without joining voice", async ({ page }) => {
    await page.evaluate(() => window.__noxaChat.addChat({ id: 42, channel_id: 2, from_unique_id: "bob", from: "Bob", text: "Channel notification", enc_verified: true }));
    await clickNotice(page, "Channel notification");
    await expect(page.locator(".message-reference-flash")).toContainText("Exact channel message on a");
    expect(await page.evaluate(() => window.__noticeCalls.filter(([name]) => name === "ChatHistoryForTab").at(-1))).toEqual(["ChatHistoryForTab", "a", 2, 43, 20]);
    await page.getByRole("button", { name: "Open conversation", exact: true }).click();
    await expect(page.getByRole("tab", { name: "# Target Channel", exact: true })).toHaveAttribute("aria-selected", "true");
    await unchangedVoice(page);
});

test("DM alert opens its exact local-history message under the originating identity", async ({ page }) => {
    await page.evaluate(() => window.__noxaChat.addChat({ direct: true, from_unique_id: "bob", to_unique_id: "account-a", from: "Bob", text: "DM notification", client_msg_id: "dm-target", enc_verified: true }));
    await clickNotice(page, "DM notification");
    await expect(page.locator(".message-reference-flash")).toContainText("Exact DM on a");
    expect(await page.evaluate(() => window.__noticeCalls.filter(([name]) => name === "DMHistoryLoadForContext").at(-1)[1].identity_uid)).toBe("identity-a");
    await page.getByRole("button", { name: "Open conversation", exact: true }).click();
    await expect(page.locator("#chat-log")).toContainText("Exact DM on a");
    await unchangedVoice(page);
});

test("group notification retains group and exact message destination", async ({ page }) => {
    await page.evaluate(() => window.__emitNotice("event", JSON.stringify({ type: "conversation_changed", data: { id: "g1", message_id: 61 } })));
    await clickNotice(page, "Friends");
    await expect(page.locator(".group-messages .message-reference-flash")).toContainText("Exact group message on a");
    await unchangedVoice(page);
});

test("group invitations open pending group without accepting it", async ({ page }) => {
    await page.evaluate(() => { window.__noticeInvitation = true; window.__emitNotice("event", JSON.stringify({ type: "conversation_changed", data: { id: "g1", invitation: true } })); });
    await clickNotice(page, "Friends");
    await expect(page.locator("#private-groups")).toBeVisible();
    await expect(page.getByRole("button", { name: "Accept invitation", exact: true })).toBeVisible();
    expect(await page.evaluate(() => window.__noticeCalls.some(([name, , request]) => name === "ConversationForTab" && request.action === "accept"))).toBe(false);
    await unchangedVoice(page);
});

test("thread alert opens the precise thread message instead of the author DM", async ({ page }) => {
    await page.evaluate(() => window.__emitNotice("event", JSON.stringify({ type: "discussion_changed", data: { channel_id: 2, thread_id: 8, message_id: 71, new_message: true, author: "bob" } })));
    await clickNotice(page, "Target thread");
    await expect(page.locator(".discussion-target")).toContainText("Exact thread message on a");
    expect(await page.evaluate(() => window.__noticeCalls.filter(([name]) => name === "DiscussionForTab").some(([, tab, request]) => tab === "a" && request.thread_id === 8))).toBe(true);
    await unchangedVoice(page);
});

test("revoked channel message reports unavailable without voice changes", async ({ page }) => {
    await page.evaluate(() => { window.__noxaPolish.recordNotification("mention", "Revoked notice", { reference: { kind: "channel", channel_id: 2, message_id: 42 } }); window.__noticeRevoked = true; });
    await clickNotice(page, "Revoked notice");
    await expect(page.locator("#toasts")).toContainText("This message is unavailable or you no longer have access.");
    await expect(page.locator(".message-reference-flash")).toHaveCount(0);
    await unchangedVoice(page);
});

test("late message history cannot appear after another server switch", async ({ page }) => {
    await page.evaluate(() => { window.__noxaPolish.recordNotification("mention", "Delayed notice", { reference: { kind: "channel", channel_id: 2, message_id: 42 } }); window.__noticeHistoryGate = new Promise(resolve => { window.__releaseNoticeHistory = resolve; }); });
    await clickNotice(page, "Delayed notice");
    await expect.poll(() => page.evaluate(() => window.__noticeCalls.some(([name, tab, , before]) => name === "ChatHistoryForTab" && tab === "a" && before === 43))).toBe(true);
    await page.evaluate(() => { window.__activateNotice("b"); window.__releaseNoticeHistory(); });
    await expect(page.locator(".message-reference-flash")).toHaveCount(0);
    expect(await page.evaluate(() => window.__noxa.state.activeTabID)).toBe("b");
    expect(await page.evaluate(() => window.__noticeCalls.filter(([name]) => /JoinChannel|LeaveChannel/.test(name)))).toEqual([]);
});

test("cross-server notification keeps voice active until the explicit switch", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxaPolish.recordNotification("mention", "Voice notice", { reference: { kind: "channel", channel_id: 2, message_id: 42 } });
        window.__noticeResets = []; window.__noticeStops = [];
        const reset = window.__noxa.resetVoiceSession, stop = window.__noxa.stopPrivateCall;
        window.__noxa.resetVoiceSession = (...args) => { window.__noticeResets.push(window.__nativeTab); return reset(...args); };
        window.__noxa.stopPrivateCall = (...args) => { window.__noticeStops.push(window.__nativeTab); return stop(...args); };
    });
    await clickNotice(page, "Voice notice", { confirm: false });
    await expect(page.getByRole("button", { name: "Cancel", exact: true })).toBeFocused();
    await expect(page.getByRole("dialog")).toContainText("a.test");
    expect(await page.evaluate(() => [window.__noxa.state.activeTabID, window.__noticeResets, window.__noticeStops])).toEqual(["b", ["b"], ["b"]]);
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await page.evaluate(() => window.__noxa.state.activeTabID)).toBe("b");
    await page.evaluate(() => window.__noxaPolish.openNotifCenter());
    await page.locator(".nc-row").filter({ hasText: "Voice notice" }).click();
    await page.getByRole("button", { name: "Switch server and open", exact: true }).click();
    await expect(page.locator(".message-reference-flash")).toContainText("Exact channel message on a");
    expect(await page.evaluate(() => [window.__noticeResets, window.__noticeStops])).toEqual([["b", "a"], ["b", "a"]]);
});

test("newer same-server notification wins over delayed history", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxaPolish.recordNotification("mention", "First notice", { reference: { kind: "channel", channel_id: 2, message_id: 42 } });
        window.__noxaPolish.recordNotification("mention", "Second notice", { reference: { kind: "channel", channel_id: 2, message_id: 43 } });
        window.__noticeHistoryBefore = 43;
        window.__noticeHistoryGate = new Promise(resolve => { window.__releaseNoticeHistory = resolve; });
        window.__noxaPolish.openNotifCenter();
    });
    await page.locator(".nc-row").filter({ hasText: "First notice" }).click();
    await expect.poll(() => page.evaluate(() => window.__noticeCalls.some(([name, , , before]) => name === "ChatHistoryForTab" && before === 43))).toBe(true);
    await page.evaluate(() => window.__noxaPolish.openNotifCenter());
    await page.locator(".nc-row").filter({ hasText: "Second notice" }).click();
    await expect(page.locator('.message-reference-flash[data-message-id="43"]')).toBeVisible();
    await page.evaluate(() => window.__releaseNoticeHistory());
    await expect(page.locator(".message-reference-flash")).toHaveCount(1);
    await expect(page.locator('.message-reference-flash[data-message-id="42"]')).toHaveCount(0);
});

test("a channel alert opens visible context while a group hides the retained chat log", async ({ page }) => {
    await page.evaluate(() => window.__noxaChat.addChat({ id: 42, channel_id: 1, from_unique_id: "bob", from: "Bob", text: "Hidden channel notice", enc_verified: true }));
    await page.getByRole("button", { name: "Friends", exact: true }).click();
    await expect(page.locator("#private-groups")).toBeVisible();
    await page.evaluate(() => window.__noxaPolish.openNotifCenter());
    await page.locator(".nc-row").filter({ hasText: "Hidden channel notice" }).click();
    await expect(page.locator(".message-reference-flash")).toBeVisible();
    await expect(page.locator(".message-reference-flash")).toContainText("Exact channel message on a");
});

test("opening another notification does not disable later group history paging", async ({ page }) => {
    await page.evaluate(() => {
        window.__noticeGroupMessages = Array.from({ length: 61 }, (_, index) => ({ id: index + 1, conversation_id: "g1", from_unique_id: "bob", body: `Group message ${index + 1}`, created_at: 1700000000 }));
        window.__noxaPolish.recordNotification("dm", "Group paging notice", { reference: { kind: "group", group_id: "g1", message_id: 61 } });
        window.__noxaPolish.recordNotification("mention", "Another notice", { reference: { kind: "channel", channel_id: 2, message_id: 42 } });
        window.__noxaPolish.openNotifCenter();
    });
    await page.locator(".nc-row").filter({ hasText: "Group paging notice" }).click();
    await expect(page.locator('.group-message[data-message-id="61"]')).toBeVisible();
    await page.evaluate(() => window.__noxaPolish.openNotifCenter());
    await page.locator(".nc-row").filter({ hasText: "Another notice" }).click();
    await expect(page.locator(".message-tools-dialog")).toBeVisible();
    await page.locator(".message-tools-dialog").getByRole("button", { name: "Close", exact: true }).click();
    await page.getByRole("button", { name: "Load older messages", exact: true }).click();
    await expect(page.locator('.group-message[data-message-id="36"]')).toHaveText(/Group message 36/);
});

test("a superseded thread failure cannot leave a disabled dialog behind the newer message", async ({ page }) => {
    await page.evaluate(() => {
        window.__noticeThreadGate = new Promise((resolve, reject) => { window.__rejectNoticeThread = reject; });
        window.__noxaPolish.recordNotification("channel_message", "Delayed thread", { reference: { kind: "thread", channel_id: 2, thread_id: 8, message_id: 71 } });
        window.__noxaPolish.recordNotification("mention", "Newer channel", { reference: { kind: "channel", channel_id: 2, message_id: 42 } });
        window.__noxaPolish.openNotifCenter();
    });
    await page.locator(".nc-row").filter({ hasText: "Delayed thread" }).click();
    await expect.poll(() => page.evaluate(() => window.__noticeCalls.some(([name, , request]) => name === "DiscussionForTab" && request.action === "get"))).toBe(true);
    await page.evaluate(() => window.__noxaPolish.openNotifCenter());
    await page.locator(".nc-row").filter({ hasText: "Newer channel" }).click();
    await expect(page.locator(".message-reference-flash")).toBeVisible();
    await page.evaluate(() => window.__rejectNoticeThread(new Error("late refusal")));
    await expect(page.locator(".discussion-dialog")).toHaveCount(0);
    await expect(page.locator(".message-reference-flash")).toBeVisible();
});
