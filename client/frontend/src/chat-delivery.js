import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { closeDialog, confirmDialog, mountServerDialog } from "./modal.js";
import "./chat-delivery.css";

const node = (tag, className, text) => { const element = document.createElement(tag); element.className = className; if (text !== undefined) element.textContent = text; return element; };
const button = (key, action) => { const element = node("button", "", t(key)); element.type = "button"; element.onclick = action; return element; };

// UI owns no durable message payload. Go seals and scopes the queue and changes
// "sending" durably before network I/O. Timers only request native pumping.
export function createChatDelivery({ app, state, readScope, restore }) {
 let enabled = false, entries = [], owner = "", busy = false, dialog = null, signature = "", latestID = "", loadError = "";
 const ready = Promise.resolve().then(() => app().ChatDeliveryFeatures()).then(features => { enabled = features?.outbox === true; return enabled; }).catch(() => false);
 const launch = node("button", "icon-btn"); launch.id = "chat-outbox-open"; launch.type = "button"; launch.hidden = true;
 launch.innerHTML = icon("outbox"); launch.onclick = () => open();
 document.getElementById("chat-head-actions").append(launch);
 const strip = node("div", "chat-delivery-strip"); strip.hidden = true; strip.setAttribute("role", "status");
 const countdown = node("span", "delivery-countdown");
 const undo = button("delivery.undo", () => { const entry = entries.find(item => item.id === latestID); if (entry) void act("cancel", entry, true); });
 const now = button("delivery.now", () => { const entry = entries.find(item => item.id === latestID); if (entry) void act("send", entry); });
 strip.append(countdown, undo, now); document.getElementById("chat-input-row").before(strip);
 const scopeKey = () => state().activeTabID + "|" + state().serverGeneration + "|" + (state().myUniqueID || "");
 const destination = entry => entry.scope === "global" ? t("delivery.global") : entry.scope === "channel"
  ? state().channels.find(channel => String(channel.ChannelID) === entry.target)?.Name || entry.target
  : state().clients.find(client => client.unique_id === entry.target)?.nickname || entry.target;
 const currentEntry = entry => { const scope = readScope(); return entry.scope === scope.sendScope && entry.target === scope.target; };
 function reset() { entries = []; owner = ""; signature = ""; loadError = ""; strip.hidden = true; launch.hidden = true; if (dialog) closeDialog(dialog.overlay, "navigate"); dialog = null; }
 function renderDialog() {
  if (!dialog?.overlay.isConnected) return;
  const focused = dialog.list.contains(document.activeElement) ? document.activeElement.dataset.entryAction : null;
  dialog.list.replaceChildren();
  if (loadError) {
   dialog.list.append(node("p", "outbox-error", loadError), button("delivery.recover", async () => {
    const scope = scopeKey();
    try {
     await app().ChatOutboxForTab(state().activeTabID, { action: "reset" });
     if (scope !== scopeKey()) return;
     loadError = ""; signature = ""; window.__noxa.toast(t("delivery.recovered"), "info"); await refresh();
    } catch (error) { if (scope === scopeKey()) { loadError = String(error); renderDialog(); } }
   }));
  }
  if (!entries.length) dialog.list.append(node("p", "", t("delivery.empty")));
  for (const entry of entries) {
   const row = node("article", "outbox-entry"); row.dataset.entryId = entry.id;
   row.append(node("strong", "", destination(entry)), node("p", "outbox-text", entry.text),
    node("p", "outbox-state", t("delivery." + entry.state)));
   if (entry.error) row.append(node("p", "outbox-error", entry.error));
   const actions = node("div", "outbox-actions");
   const add = (key, action) => { const control = button(key, action); control.dataset.entryAction = entry.id + key; actions.append(control); return control; };
   if (entry.state !== "sending") {
    add("delivery.cancel", () => void act("cancel", entry));
    add(entry.state === "uncertain" ? "delivery.retry" : "delivery.now", () => void act(entry.state === "uncertain" ? "retry" : "send", entry));
    if (currentEntry(entry)) add("delivery.undo", () => void act("cancel", entry, true));
   }
   row.append(actions); dialog.list.append(row);
  }
  if (focused) [...dialog.list.querySelectorAll("button")].find(control => control.dataset.entryAction === focused)?.focus({ preventScroll: true });
 }
 function render() {
  launch.hidden = !enabled || !state().activeTabID;
  launch.setAttribute("aria-label", t("delivery.count", { count: entries.length })); launch.title = launch.getAttribute("aria-label");
  launch.dataset.count = String(entries.length);
  const pending = entries.filter(entry => entry.state === "held" && currentEntry(entry)).at(-1);
  latestID = pending?.id || ""; strip.hidden = !pending;
  if (pending) countdown.textContent = t("delivery.pending", { seconds: Math.max(0, Math.ceil((pending.due_at - Date.now()) / 1000)) });
  undo.textContent = t("delivery.undo"); now.textContent = t("delivery.now");
  const next = loadError + JSON.stringify(entries);
  if (signature !== next) { signature = next; renderDialog(); }
 }
 async function refresh() {
  if (!await ready || !state().activeTabID) { reset(); return; }
  const scope = scopeKey(), tabID = state().activeTabID;
  if (scope !== owner) { reset(); owner = scope; }
  try {
   const result = await app().ChatOutboxForTab(tabID, { action: "list" });
   if (scope !== scopeKey()) return;
   loadError = ""; entries = Array.isArray(result?.entries) ? result.entries : []; render();
  } catch (error) { if (scope === scopeKey()) { loadError = String(error); render(); } }
 }
 async function act(action, entry, editing = false, acknowledge = false) {
  const scope = scopeKey(), tabID = state().activeTabID;
  if (owner !== scope || !entries.some(item => item.id === entry.id)) return;
  try {
   const result = await app().ChatOutboxForTab(tabID, { action, id: entry.id, ...(acknowledge ? { acknowledge_undelivered: true } : {}) });
   if (scope !== scopeKey()) return;
   entries = result.entries || []; render();
   if (editing && currentEntry(entry)) restore(entry);
   if (action === "send" || action === "retry") void pump();
  } catch (error) {
   if (scope !== scopeKey()) return;
   // An attempted secure message may still be delivered: removing it needs an
   // explicit acknowledgement (never a silent drop, never an undo + resend).
   if (action === "cancel" && !acknowledge && /may still (be delivered|arrive)/.test(String(error))) {
    const ok = await confirmDialog({ title: t("delivery.removeAnywayTitle"), message: t("delivery.removeAnyway"), danger: true,
     confirmLabel: t("delivery.removeAnywayConfirm"), cancelLabel: t("delivery.close"), serverScoped: true });
    if (ok && scope === scopeKey()) void act(action, entry, false, true);
    return;
   }
   window.__noxa.toast(t("delivery.failed", { error: String(error) }), "warn");
  }
 }
 async function pump() {
  if (busy || !await ready) return;
  busy = true;
  try { await app().PumpChatOutbox(); } catch { /* Native failures remain in the encrypted queue. */ }
  finally { busy = false; await refresh(); }
 }
 async function stage(scope, text, replyToID, queueOffline = false) {
  if (!await ready) return false;
  const result = await app().ChatOutboxForTab(scope.tabID, { action: "stage", scope: scope.sendScope, target: scope.target,
   text, reply_to_id: replyToID, queue_offline: queueOffline });
  if (!result?.available || !Array.isArray(result.entries)) throw new Error("Outbox response unavailable");
  if (scope.tabID === state().activeTabID && scope.generation === state().serverGeneration) { owner = scopeKey(); entries = result.entries; signature = ""; render(); }
  return true;
 }
 function open() {
  if (dialog?.overlay.isConnected) return;
  const overlay = node("div", "dlg-overlay"), box = node("section", "dlg outbox-dialog");
  const list = node("div", "outbox-list");
  const close = button("delivery.close", () => { closeDialog(overlay); dialog = null; });
  box.append(node("h2", "", t("delivery.title")), node("p", "outbox-help", t("delivery.help")), list, close); overlay.append(box);
  dialog = { overlay, list }; mountServerDialog(overlay, { initialFocus: close }); renderDialog();
 }
 window.runtime.EventsOn("tab_reset", reset);
 window.addEventListener("noxa-language-changed", () => { signature = ""; render(); renderDialog(); });
 const timer = setInterval(() => {
  if (owner && owner !== scopeKey()) reset();
  render(); void pump();
 }, 1000);
 window.addEventListener("beforeunload", () => clearInterval(timer), { once: true });
 void ready.then(refresh);
 return { ready, stage, refresh, enabled: () => enabled, open };
}
