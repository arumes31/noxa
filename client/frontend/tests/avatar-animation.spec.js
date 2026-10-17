import { test, expect } from "./fixtures.js";
import fs from "node:fs";
const fixture = ext => fs.readFileSync(new URL(`../../../internal/server/testdata/avatar-animated.${ext}`, import.meta.url));
const source = ext => `data:image/${ext};base64,${fixture(ext).toString("base64")}`;
test.beforeEach(async ({ page }) => {
 await page.route("**/__avatar_animation__", route => route.fulfill({contentType:"text/html",body:'<!doctype html><div id="avatars"></div><div id="other"></div>'}));
 await page.goto("/__avatar_animation__");
 await page.evaluate(() => {window.__focused=true;window.__hidden=false;Object.defineProperty(document,"hasFocus",{value:()=>window.__focused});Object.defineProperty(document,"hidden",{get:()=>window.__hidden});window.__noxa={state:{serverGeneration:1},toast:text=>{window.__toast=text;}};});
});
async function install(page,ext="gif") {await page.evaluate(async src=>{
 const {setSafeImage}=await import("/src/safe-media.js"),{manageAvatarPlayback}=await import("/src/avatar-playback.js");
 window.__avatarSource=src;window.__setAvatar=(id,value=src)=>{let el=document.getElementById(id);if(!el){el=document.createElement("div");el.id=id;document.getElementById("avatars").append(el);}return setSafeImage(el,value,{avatar:true});};
 await window.__setAvatar("first").decode();setSafeImage(document.getElementById("other"),src);window.__dispose=manageAvatarPlayback(document.body);
},source(ext));}
async function focused(page,value){await page.evaluate(value=>{window.__focused=value;window.dispatchEvent(new Event(value?"focus":"blur"));},value);}
for(const ext of ["gif","webp"])test(`${ext} preserves upload bytes and actually animates`,async({page})=>{
 await page.evaluate(async bytes=>{const {prepareAvatar}=await import("/src/image-tools.js");window.__prepared=prepareAvatar(new File([Uint8Array.from(atob(bytes),c=>c.charCodeAt(0))],"avatar.bin",{type:""}),{serverGeneration:1});},fixture(ext).toString("base64"));
 await expect(page.locator(".img-preview img[data-avatar-image]")).toHaveAttribute("src",source(ext));await expect(page.locator(".img-zoom")).toHaveCount(0);await page.locator(".dlg-ok").click();
 expect(await page.evaluate(()=>window.__prepared)).toEqual({dataBase64:fixture(ext).toString("base64"),contentType:`image/${ext}`});await install(page,ext);
 const frames = new Set();
 for (let i = 0; i < 8; i++) { frames.add((await page.locator("#first img").screenshot({ animations: "allow" })).toString("base64")); await page.waitForTimeout(80); }
 expect(frames.size).toBeGreaterThan(1);
});
test("continuous60s blur freezes only avatars; focus restores original",async({page})=>{
 await install(page);await page.clock.install();await focused(page,false);await page.clock.runFor(59000);await expect(page.locator("#first img")).toHaveAttribute("src",source("gif"));await page.clock.runFor(1000);await expect(page.locator("#first img")).toHaveAttribute("src",/^data:image\/png;/);await expect(page.locator("#other img")).toHaveAttribute("src",source("gif"));expect(await page.evaluate(()=>window.__avatarSource)).toBe(source("gif"));await focused(page,true);await expect(page.locator("#first img")).toHaveAttribute("src",source("gif"));
});
test("early focus resets delay; hidden is equivalent",async({page})=>{
 await install(page);await page.clock.install();await focused(page,false);await page.clock.runFor(59000);await focused(page,true);await focused(page,false);await page.clock.runFor(59000);await expect(page.locator("#first img")).toHaveAttribute("src",source("gif"));await focused(page,true);
 await page.evaluate(()=>{window.__hidden=true;document.dispatchEvent(new Event("visibilitychange"));});await page.clock.runFor(60000);await expect(page.locator("#first img")).toHaveAttribute("src",/^data:image\/png;/);await focused(page,true);await expect(page.locator("#first img")).toHaveAttribute("src",/^data:image\/png;/);await page.evaluate(()=>{window.__hidden=false;document.dispatchEvent(new Event("visibilitychange"));});await expect(page.locator("#first img")).toHaveAttribute("src",source("gif"));
});
test("new/changed paused avatars freeze, remove/dispose cannot restore stale source",async({page})=>{
 await install(page);await page.clock.install();await focused(page,false);await page.clock.runFor(60000);await page.evaluate(src=>{window.__setAvatar("second",src);document.querySelector("#first img").src=src;},source("webp"));
 for(const id of ["first","second"])await expect(page.locator(`#${id} img`)).toHaveAttribute("src",/^data:image\/png;/);await focused(page,true);for(const id of ["first","second"])await expect(page.locator(`#${id} img`)).toHaveAttribute("src",source("webp"));
 await page.evaluate(()=>document.getElementById("second").remove());await focused(page,false);await page.clock.runFor(60000);await page.evaluate(()=>{document.querySelector("#first img").src="data:image/png;base64,Y2hhbmdlZA==";window.__dispose();});await expect(page.locator("#first img")).toHaveAttribute("src","data:image/png;base64,Y2hhbmdlZA==");await page.evaluate(()=>window.__setAvatar("third"));await page.clock.runFor(60000);await expect(page.locator("#third img")).toHaveAttribute("src",source("gif"));
});
test("APNG/SVG reject without flatten",async({page})=>{
 const result=await page.evaluate(async()=>{const{prepareAvatar}=await import("/src/image-tools.js");const png=new Uint8Array([137,80,78,71,13,10,26,10,0,0,0,0,97,99,84,76,0,0,0,0]);const value=await prepareAvatar(new File([png],"a.png",{type:"image/png"}));const message=window.__toast;const svg=await prepareAvatar(new File(['<svg xmlns="http://www.w3.org/2000/svg"/>'],"a.gif",{type:"image/gif"}));return{value,message,svg};});expect(result.value).toBeNull();expect(result.message).toContain("Animated PNG");expect(result.svg).toBeNull();await expect(page.locator(".dlg-overlay")).toHaveCount(0);
});

test("near-limit animated avatar encodes without argument-stack overflow", async ({page}) => {
 const original=fixture("gif");const parts=[original.subarray(0,original.length-1),Buffer.from([0x21,0xfe])];
 for(let i=0;i<800;i++)parts.push(Buffer.from([255]),Buffer.alloc(255,65));parts.push(Buffer.from([0,0x3b]));const bytes=Buffer.concat(parts);
 await page.evaluate(async base64=>{const {prepareAvatar}=await import("/src/image-tools.js");window.__prepared=prepareAvatar(new File([Uint8Array.from(atob(base64),c=>c.charCodeAt(0))],"large.gif",{type:"image/gif"}));},bytes.toString("base64"));
 await page.locator(".dlg-ok").click();expect((await page.evaluate(()=>window.__prepared)).dataBase64).toBe(bytes.toString("base64"));
});
test("paused original remains available to lightbox, disposal restores only owned poster", async ({page}) => {
 await install(page,"webp");await page.clock.install();await focused(page,false);await page.clock.runFor(60000);
 expect(await page.evaluate(async()=>{const {originalAvatarSource}=await import("/src/avatar-playback.js");return originalAvatarSource(document.querySelector("#first img"));})).toBe(source("webp"));
 await page.evaluate(()=>window.__dispose());await expect(page.locator("#first img")).toHaveAttribute("src",source("webp"));
 await page.clock.runFor(120000);await expect(page.locator("#first img")).toHaveAttribute("src",source("webp"));
});

test("unloaded paused avatar gets a visible still and resumes after remove/reinsert",async({page})=>{
 await install(page);await page.clock.install();await focused(page,false);await page.clock.runFor(60000);
 await page.evaluate(src=>window.__setAvatar("late",src),source("webp"));
 await expect.poll(()=>page.locator("#late img").evaluate(async img=>{if(!img.complete)return false;const c=document.createElement("canvas");c.width=c.height=1;const ctx=c.getContext("2d");ctx.drawImage(img,0,0,1,1);const p=ctx.getImageData(0,0,1,1).data;return img.src.startsWith("data:image/png;")&&p[3]>0&&(p[0]>20||p[2]>20);})).toBe(true);
 await page.evaluate(()=>{window.__removed=document.getElementById("late");window.__removed.remove();});
 await page.evaluate(()=>document.getElementById("avatars").append(window.__removed));
 await expect(page.locator("#late img")).toHaveAttribute("src",/^data:image\/png;/);
 await focused(page,true);await expect(page.locator("#late img")).toHaveAttribute("src",source("webp"));
});
