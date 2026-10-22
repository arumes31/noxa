import { t } from "./i18n.js";
import { icon } from "./icons.js";

const V = () => window.__noxa;
let calledOwner = "";
const node = (tag, cls, text) => { const element = document.createElement(tag); element.className = cls; if (text !== undefined) element.textContent = text; return element; };
export function speakingQueue(clients, channelID) {
 return clients.filter(client => client.channel_id === channelID && client.hand_raised_order > 0)
  .sort((a, b) => a.hand_raised_order - b.hand_raised_order);
}
async function act(action, channelID, clientID = "") {
 const { state } = V(), tabID = state.activeTabID, generation = state.serverGeneration, session = state.sessionGeneration;
 if (!state.myClientID || state.myChannelID !== channelID) return;
 try {
  await window.go.main.App.VoiceQueueForTab(tabID, { action, channel_id: channelID, client_id: clientID });
 } catch (error) {
  if (tabID === state.activeTabID && generation === state.serverGeneration && session === state.sessionGeneration)
   V().toast(t("queue.error", { error: String(error) }), "warn");
 }
}
export function renderVoiceQueueControl() {
 const state = V().state;
 let control = document.getElementById("voice-raise-hand");
 if (!control) {
  control = node("button", "icon-btn"); control.id = "voice-raise-hand"; control.type = "button"; control.innerHTML = icon("hand");
  document.getElementById("chat-head-actions").prepend(control);
 }
 const me = state.clients.find(client => client.client_id === state.myClientID);
 control.hidden = !state.supportsVoiceQueue || !state.myClientID || !state.myChannelID;
 const call = me?.hand_called ? state.activeTabID + "|" + state.myClientID + "|" + state.myChannelID : "";
 if (call && calledOwner !== call && !state.replayingTabID) V().toast(t("queue.called"), "info");
 calledOwner = call;
 const raised = !!me?.hand_raised_order;
 control.setAttribute("aria-pressed", String(raised)); control.setAttribute("aria-label", t(raised ? "queue.lower" : "queue.raise"));
 const members = speakingQueue(state.clients, state.myChannelID), position = members.findIndex(client => client.client_id === state.myClientID);
 control.title = control.getAttribute("aria-label") + (position >= 0 ? " · " + t("queue.position", { position: position + 1 }) : "");
 const channelID = state.myChannelID;
 control.onclick = () => void act(raised ? "lower" : "raise", channelID);
}
export function voiceQueueSection(channelID) {
 const state = V().state;
 if (!state.supportsVoiceQueue || !channelID || state.myChannelID !== channelID) return null;
 const section = node("section", "voice-queue"); section.setAttribute("aria-label", t("queue.title"));
 section.append(node("h4", "", t("queue.title")));
 const queue = speakingQueue(state.clients, channelID), moderator = state.channels.find(channel => channel.ChannelID === channelID)?.Access?.can_manage_voice_queue === true;
 const button = (key, action, clientID) => { const control = node("button", "", t(key)); control.type = "button"; control.onclick = () => void act(action, channelID, clientID); return control; };
 const self = state.clients.find(client => client.client_id === state.myClientID);
 const called = state.clients.find(client => client.channel_id === channelID && client.hand_called);
 if (called && called.client_id !== state.myClientID) section.append(node("p", "voice-queue-called", t("queue.current", { name: called.nickname || called.unique_id })));
 if (self?.hand_called) { const notice = node("p", "voice-queue-called", t("queue.called")); notice.setAttribute("role", "status"); section.append(notice); }
 if (!queue.length) section.append(node("p", "voice-queue-help", t("queue.empty")));
 else {
  const list = node("ol", "voice-queue-list");
  for (const client of queue) {
   const row = node("li", "", client.nickname || client.unique_id || "?");
   if (moderator || client.client_id === state.myClientID) row.append(button("queue.lower", "lower", client.client_id));
   list.append(row);
  }
  section.append(list);
 }
 const actions = node("div", "voice-queue-actions");
 actions.append(button(self?.hand_raised_order ? "queue.lower" : "queue.raise", self?.hand_raised_order ? "lower" : "raise"));
 if (moderator && queue.length) actions.append(button("queue.next", "next"), button("queue.clear", "clear"));
 section.append(actions, node("p", "voice-queue-help", t("queue.help")));
 return section;
}
