import test from "node:test";
import assert from "node:assert/strict";
import { sidebarWidth, joinFailure, unreadLabel } from "../src/client-usability-model.js";
test("sidebar widths are clamped to usable desktop space",()=>{assert.equal(sidebarWidth(900,1440),420);assert.equal(sidebarWidth(100,1000),220);assert.equal(sidebarWidth(420,721),321);});
test("join errors explain known restrictions and preserve unknown causes",()=>{for(const [error,text] of [["invalid channel password","password"],["channel is full","full"],["permission denied","permission"],["accept server rules","rules"],["channel not found","no longer"],["not connected","connection"],["custom reason","custom reason"]])assert.ok(joinFailure(error).includes(text));});
test("unread labels distinguish messages, mentions and replies",()=>{assert.equal(unreadLabel(3,1,2),"3 unread messages · 1 mentions · 2 replies to you");});
