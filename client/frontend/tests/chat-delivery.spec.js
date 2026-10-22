import { expect, test } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
 await page.addInitScript(() => {
  window.__events = {}; window.__calls = []; window.__online = true; window.__outbox = []; window.__sent = []; window.__corrupt = false;
  const emit = (name, value) => { for (const fn of window.__events[name] || []) fn(value); };
  window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
  window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
   window.__calls.push([method, ...args]);
   if (method === "GetSettings") return { language: "en", theme: "midnight", onboarding_done: true, alpha_dismissed: "test", effects_enabled: false, bookmarks: [], chat_max_lines: 200, chat_undo_seconds: 5 };
   if (["ListTabs", "GetPermissions", "ListStreamsForTab"].includes(method)) return [];
   if (["IdentityInfo", "GetAvatar", "GetMediaLimitsForTab"].includes(method)) return {};
   if (["Connected", "IsGuest", "GamingOverlayAvailable"].includes(method)) return false;
   if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
   if (method === "ChatDeliveryFeatures") return { outbox: true };
   if (method === "ChatOutboxForTab") {
    const [tab, r] = args;
    if (r.action === "reset") { window.__corrupt = false; window.__outbox = window.__outbox.filter(entry => entry.tab !== tab); }
    if (window.__corrupt) throw new Error("cannot decrypt this outbox");
    if (r.action === "stage") {
     if (!window.__online && !r.queue_offline) throw new Error("offline");
     window.__outbox.push({ ...r, tab, id: String(window.__calls.length), state: window.__online ? "held" : "queued", due_at: Date.now() + 5000 });
    }
    if (r.action === "cancel") window.__outbox = window.__outbox.filter(entry => entry.tab !== tab || entry.id !== r.id);
    if (r.action === "send" || r.action === "retry") {
     const entry = window.__outbox.find(entry => entry.id === r.id && entry.tab === tab);
     if (entry) { entry.state = "queued"; entry.due_at = 0; }
    }
    return { available: true, entries: window.__outbox.filter(entry => entry.tab === tab) };
   }
   if (method === "PumpChatOutbox") {
    if (window.__online) {
     const due = window.__outbox.filter(entry => ["held", "queued"].includes(entry.state) && entry.due_at <= Date.now());
     window.__sent.push(...due); window.__outbox = window.__outbox.filter(entry => !due.includes(entry));
    }
    return;
   }
   if (method === "ChatHistoryForTab") return { messages: [] };
   if (method === "SubscribeChannelsForTab") { emit("subscriptions", JSON.stringify({ channel_ids: args[1] })); return ""; }
   if (method === "ConversationForTab") return { conversations: [] };
   if (method === "VoiceQueueForTab") return { ...args[1] };
   return "";
  }; } }) } };
 });
 await page.goto("/");
 await page.waitForFunction(() => !!window.__noxaShell && !!window.__noxaChat);
 await page.evaluate(() => {
  const v = window.__noxa;
  Object.assign(v.state, { activeTabID: "server-a", myClientID: "self", myUniqueID: "u-self", myNickname: "Daniel", myChannelID: 2, ownAuthority: "owner" });
  v.showWorkspace(false);
  const snapshot = { supports_voice_queue: true, root_channels: [
   { ChannelID: 1, Name: "Lobby", clients: [] },
   { ChannelID: 2, Name: "Raid", access: { can_manage_voice_queue: true, can_connect: true }, clients: [
    { client_id: "self", unique_id: "u-self", nickname: "Daniel", channel_id: 2 },
    { client_id: "friend", unique_id: "u-friend", nickname: "Bene", channel_id: 2, hand_raised_order: 1 },
   ] },
   { ChannelID: 11, Name: "general", TextOnly: true, clients: [] },
  ] };
  window.__snapshot = snapshot;
  for (const callback of window.__events.snapshot) callback(JSON.stringify(snapshot));
 });
 await expect(page.locator("#chat-outbox-open")).toBeVisible();
});

test("Direct Messages appears above Voice and voice-linked text entries do not join voice", async ({ page }) => {
 const positions = await page.evaluate(() => ({ direct: document.getElementById("direct-messages").getBoundingClientRect().top, voice: document.getElementById("voice-heading").getBoundingClientRect().top }));
 expect(positions.direct).toBeLessThan(positions.voice);
 const linked = page.locator('#text-channels [data-chid="2"]');
 await expect(linked).toHaveAccessibleName("Linked chat for voice channel Raid, You are in this voice channel");
 await expect(linked).toHaveClass(/voice-linked-current/);
 await page.locator('#text-channels [data-chid="1"]').click();
 await expect.poll(() => page.evaluate(() => window.__noxaChat.activeChannelID())).toBe(1);
 expect(await page.evaluate(() => window.__noxa.state.myChannelID)).toBe(2);
 expect(await page.evaluate(() => window.__calls.some(call => call[0] === "JoinChannelForTab"))).toBe(false);
 await expect(linked).toHaveClass(/voice-linked-current/);
});

test("undo keeps a newer draft and prevents transmission", async ({ page }) => {
 await page.locator("#chat-text").fill("First message");
 await page.locator("#chat-send").click();
 await expect(page.locator(".chat-delivery-strip")).toBeVisible();
 expect(await page.evaluate(() => window.__sent.length)).toBe(0);
 await page.locator("#chat-text").fill("New draft");
 await page.locator(".chat-delivery-strip").getByRole("button", { name: "Undo", exact: true }).click();
 await expect(page.locator("#chat-text")).toHaveValue("First message New draft");
 expect(await page.evaluate(() => window.__outbox.length)).toBe(0);
});

test("send now releases a held message exactly once", async ({ page }) => {
 await page.locator("#chat-text").fill("Send immediately");
 await page.locator("#chat-send").click();
 await expect(page.locator(".chat-delivery-strip")).toBeVisible();
 await page.locator(".chat-delivery-strip").getByRole("button", { name: "Send now", exact: true }).click();
 await expect.poll(() => page.evaluate(() => window.__sent.length)).toBe(1);
 await expect(page.locator(".chat-delivery-strip")).toBeHidden();
});

test("offline queuing is explicit and retains the channel destination across disconnection", async ({ page }) => {
 await page.evaluate(() => {
  window.__online = false;
  for (const callback of window.__events.disconnected) callback();
 });
 await page.locator("#chat-text").fill("After reconnect");
 await page.locator("#chat-send").click();
 await expect(page.getByRole("button", { name: "Queue when online", exact: true })).toBeVisible();
 expect(await page.evaluate(() => window.__outbox.length)).toBe(0);
 await page.getByRole("button", { name: "Queue when online", exact: true }).click();
 await expect(page.locator("#chat-text")).toHaveValue("");
 expect(await page.evaluate(() => window.__outbox.map(entry => [entry.tab, entry.scope, entry.target]))).toEqual([["server-a", "channel", "2"]]);
 expect(await page.evaluate(() => window.__sent.length)).toBe(0);
 await page.evaluate(() => { window.__online = true; window.__outbox[0].due_at = 0; });
 await expect.poll(() => page.evaluate(() => window.__sent.length)).toBe(1);
});

test("uncertain delivery stays visible and requires an explicit retry", async ({ page }) => {
 await page.evaluate(() => { window.__outbox = [{ tab: "server-a", id: "uncertain", scope: "channel", target: "2", text: "Check history", state: "uncertain", due_at: 0, error: "Connection lost" }]; });
 await page.locator("#chat-outbox-open").click();
 await expect(page.getByRole("dialog")).toContainText("Not confirmed");
 expect(await page.evaluate(() => window.__sent.length)).toBe(0);
 await page.getByRole("button", { name: "Retry — may duplicate", exact: true }).click();
 await expect.poll(() => page.evaluate(() => window.__sent.length)).toBe(1);
});

test("outbox hides another server's content immediately on tab reset", async ({ page }) => {
 await page.locator("#chat-text").fill("Private on A");
 await page.locator("#chat-send").click();
 await page.locator("#chat-outbox-open").click();
 await expect(page.locator(".outbox-text")).toHaveText("Private on A");
 await page.evaluate(() => { window.__noxa.state.activeTabID = "server-b"; window.__noxa.state.serverGeneration++; for (const fn of window.__events.tab_reset || []) fn("server-b"); });
 await expect(page.locator(".outbox-dialog")).toHaveCount(0);
 await expect(page.locator(".chat-delivery-strip")).toBeHidden();
});

test("a damaged outbox has a backup and reset action", async ({ page }) => {
 await page.evaluate(() => { window.__corrupt = true; });
 await page.locator("#chat-outbox-open").click();
 await expect(page.getByRole("button", { name: "Back up and reset outbox" })).toBeVisible();
 await page.getByRole("button", { name: "Back up and reset outbox" }).click();
 await expect(page.getByRole("dialog")).toContainText("Your outbox is empty");
});

test("hand raising, FIFO display and moderator controls use scoped requests", async ({ page }) => {
 await page.getByRole("button", { name: "Raise hand", exact: true }).click();
 expect(await page.evaluate(() => window.__calls.filter(call => call[0] === "VoiceQueueForTab").map(call => call.slice(1))))
  .toEqual([["server-a", { action: "raise", channel_id: 2, client_id: "" }]]);
 await page.locator("#details-toggle").click();
 await expect(page.locator(".voice-queue-list")).toHaveText(/Bene/);
 await page.locator(".voice-queue").getByRole("button", { name: "Call next", exact: true }).click();
 expect(await page.evaluate(() => window.__calls.filter(call => call[0] === "VoiceQueueForTab").at(-1)[2].action)).toBe("next");
 await page.evaluate(() => {
  const self = window.__snapshot.root_channels[1].clients[0]; self.hand_raised_order = 2;
  for (const callback of window.__events.snapshot) callback(JSON.stringify(window.__snapshot));
 });
 await expect(page.locator("#voice-raise-hand")).toHaveAttribute("aria-pressed", "true");
 await expect(page.locator("#voice-raise-hand")).toHaveAttribute("title", /Your position: 2/);
});

test("moderator actions are absent without permission and old servers hide hand raising", async ({ page }) => {
 await page.evaluate(() => {
  window.__snapshot.root_channels[1].access.can_manage_voice_queue = false;
  for (const callback of window.__events.snapshot) callback(JSON.stringify(window.__snapshot));
 });
 await page.locator("#details-toggle").click();
 await expect(page.locator(".voice-queue").getByRole("button", { name: "Call next" })).toHaveCount(0);
 await page.evaluate(() => {
  window.__snapshot.supports_voice_queue = false;
  for (const callback of window.__events.snapshot) callback(JSON.stringify(window.__snapshot));
 });
 await expect(page.locator("#voice-raise-hand")).toBeHidden();
 await expect(page.locator(".voice-queue")).toHaveCount(0);
});

test("compact workspace keeps linked chats and the speaking queue readable", async ({ page }, testInfo) => {
 await page.setViewportSize({ width: 1280, height: 800 });
 await page.locator("#details-toggle").click();
 await page.evaluate(() => {
  window.__noxaChat.addChat({ enc_verified: true, channel_id: 2, id: 101, from_unique_id: "u-friend", from: "Bene", text: "Ready for the next round? Raise your hand to take a turn." });
  window.__noxa.state.settings.chat_undo_seconds = 30;
 });
 await page.locator("#chat-text").fill("I will join after this round.");
 await page.locator("#chat-send").click();
 await expect(page.locator(".chat-delivery-strip")).toBeVisible();
 await page.screenshot({ path: testInfo.outputPath("workspace-preview.png") });
 await page.setViewportSize({ width: 1000, height: 760 });
 const overlap = await page.evaluate(() => document.getElementById("chat-head-actions").getBoundingClientRect().left < document.querySelector(".channel-heading").getBoundingClientRect().right);
 expect(overlap).toBe(false);
 await page.screenshot({ path: testInfo.outputPath("compact-preview.png") });
});

test("scrollbars use six-pixel rails without native arrow buttons", async ({ page }) => {
 const styles = await page.locator("#chat-log").evaluate(log => ({
  width: getComputedStyle(log, "::-webkit-scrollbar").width,
  buttons: getComputedStyle(log, "::-webkit-scrollbar-button").display,
  standard: getComputedStyle(log).scrollbarWidth,
 }));
 expect(styles).toEqual({ width: "6px", buttons: "none", standard: "auto" });
});
