import { test, expect } from "./fixtures.js";

test("ready members have no icon, tooltip or gap; failures and rekeys update the focused row", async ({page}) => {
    await page.route("**/member-media-test", route => route.fulfill({contentType:"text/html",body:'<!doctype html><html><head><link rel="stylesheet" href="/src/style.css"><style>body{display:block;padding:16px}#media-member-test{display:flex;gap:8px;height:30px}</style></head><body></body></html>'}));
    await page.goto("/member-media-test");
    await page.evaluate(async () => {
        const {createMemberMediaIndicator,updateMemberMediaIndicators}=await import("/src/member-media-indicator.js");
        const {useChannelMediaSession}=await import("/src/channel-media.js");
        const state={myChannelID:42,myClientID:"self",pc:{connectionState:"connected"}};
        const ready={state:"encrypted",participants:[{clientID:"peer",sent:true,received:true}]};
        useChannelMediaSession({active:true,status:()=>ready,stop(){}});
        const row=document.createElement("div");row.id="media-member-test";row.setAttribute("role","treeitem");row.setAttribute("aria-label","Peer, Member");
        row.append(createMemberMediaIndicator({client_id:"peer",channel_id:42},state));document.body.append(row);
        window.__mediaMember={state,ready,update:updateMemberMediaIndicators};
        updateMemberMediaIndicators(state,ready);
    });
    const row=page.locator("#media-member-test"), indicator=row.locator(".member-media-indicator");
    await expect(indicator).toBeHidden(); await expect(indicator).not.toHaveAttribute("title");
    expect(await indicator.evaluate(e=>e.getBoundingClientRect().width)).toBe(0);
    await expect(row).toHaveAttribute("aria-label","Peer, Member");
    await page.evaluate(()=>{const f=window.__mediaMember;f.state.pc.connectionState="failed";f.update(f.state,f.ready);});
    await expect(indicator).toBeVisible();await expect(indicator).toHaveAttribute("data-state","unavailable");
    await expect(row).toHaveAttribute("aria-label",/Voice connection unavailable/);
    await page.evaluate(()=>{const f=window.__mediaMember;f.state.pc.connectionState="connected";f.update(f.state,{state:"keying"});});
    await expect(indicator).toHaveAttribute("data-state","keying");
    await expect(row).toHaveAttribute("aria-label",/Exchanging encryption keys/);
    await page.evaluate(()=>{const f=window.__mediaMember;f.update(f.state,f.ready);});
    await expect(indicator).toBeHidden();await expect(row).toHaveAttribute("aria-label","Peer, Member");
});
