import { test, expect } from "@playwright/test";

test("a microphone failure stays visible until a successful retry", async ({ page }) => {
    await page.setViewportSize({width:320,height:844});
    await page.goto("http://127.0.0.1:5175/app/ui-tests/layout.html?view=voice-active");
    await expect(page.locator("html")).toHaveAttribute("data-ready","true");
    const warning=page.locator(".vc-mic-unavailable");await expect(warning).toBeHidden();
    await page.evaluate(async()=>{
        const {app}=await import("/app/src/app.js"), voice=app.voice;
        voice.microphoneAvailable=false;voice.microphoneError=new Error("Microphone busy");
        voice.retryMicrophone=()=>new Promise(resolve=>{window.__finishMic=()=>{voice.microphoneAvailable=true;voice.microphoneError=null;voice.dispatchEvent(new Event("state"));resolve(true);};});
        voice.dispatchEvent(new Event("state"));
    });
    await expect(warning).toContainText("Others cannot hear you");await expect(warning).toContainText("Microphone busy");
    const retry=warning.getByRole("button",{name:"Reconnect microphone",exact:true});
    await retry.click();await expect(retry).toBeDisabled();
    await page.evaluate(()=>window.__finishMic());await expect(warning).toBeHidden();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
});
