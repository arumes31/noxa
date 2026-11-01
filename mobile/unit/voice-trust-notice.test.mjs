import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import vm from "node:vm";
import { mediaErrorReason, MediaSessionError } from "../src/core/media-e2ee/media-session.js";

test("voice trust guidance stays in one dismissible sheet; ordinary errors remain toasts", async () => {
 const strings = {}, notices = [], toasts = []; let dismiss;
 const sources = {
  "../i18n.js": { t: (key, args) => args?.error || strings.en?.[key] || key, addStrings: data => Object.assign(strings,data) },
  "../core/media-e2ee/media-session.js": { mediaErrorReason },
  "./dom.js": { sheet: (...args) => {notices.push(args);return new Promise(resolve => {dismiss=resolve;});}, toast:(...args)=>toasts.push(args) },
 };
 const context = vm.createContext({});
 const module = new vm.SourceTextModule(await fs.readFile(new URL("../src/ui/media-e2ee-text.js",import.meta.url),"utf8"), {context});
 await module.link(name => new vm.SyntheticModule(Object.keys(sources[name]),function(){for(const [key,value]of Object.entries(sources[name]))this.setExport(key,value);},{context}));
 await module.evaluate();
 const {showVoiceJoinError,mediaErrorText}=module.namespace;
 const pending=showVoiceJoinError(new MediaSessionError("link_required"));
 assert.equal(showVoiceJoinError(new MediaSessionError("link_required")),pending);
 assert.equal(notices.length,1);assert.equal(toasts.length,0);
 assert.match(notices[0][1],/QR code.*own desktop account/);
 assert.equal(notices[0][2][0].primary,true);
 dismiss(true);await pending;
 const changed=showVoiceJoinError(new MediaSessionError("binding_changed"));
 assert.equal(notices.length,2);assert.match(notices[1][1],/identity does not match/);
 dismiss(true);await changed;
 await showVoiceJoinError(new Error("wrong password"));
 assert.equal(notices.length,2);assert.equal(toasts.length,1);assert.match(toasts[0][0],/wrong password/);
 for(const locale of ["en","de"])for(const reason of ["link_required","binding_changed"])assert.ok(strings[locale][`e2ee.reason.${reason}`]);
 assert.match(mediaErrorText(new MediaSessionError("link_required")),/QR code/);
});
