import {test,expect} from "@playwright/test";
async function prepare(page, platform, standalone=false) {
    await page.addInitScript(({platform,standalone})=>{
        Object.defineProperty(navigator,'userAgent',{get:()=>platform==='ios'?'iPhone Safari':'Android Chrome'});
        Object.defineProperty(navigator,'standalone',{get:()=>standalone});
    },{platform,standalone});
    await page.setViewportSize({width:320,height:844});
    await page.goto('http://127.0.0.1:5175/app/ui-tests/layout.html?view=chat');
    await expect(page.locator('html')).toHaveAttribute('data-ready','true');
    await page.evaluate(async()=>{
        const module=await import('/app/src/ui/install-prompt.js');module.initInstallPrompt();window.__install=module;
        module.renderInstallPrompt(document.getElementById('app'));
    });
}
for(const platform of ['ios','android']) test(`${platform} browser installation help and dismissal`,async({page})=>{
    await prepare(page,platform);
    const banner=page.locator('#install-banner');await expect(banner).toBeVisible();
    await expect(page.getByRole('button',{name:'Send',exact:true})).toBeInViewport();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
    await banner.getByRole('button',{name:'How to install'}).click();
    await expect(page.getByRole('dialog',{name:'Install noXa'})).toContainText(platform==='ios'?'Add to Home Screen':'Install and create shortcut');
    await page.getByRole('button',{name:'Got it'}).click();await banner.getByRole('button',{name:'Not now'}).click();
    await page.evaluate(()=>window.__install.renderInstallPrompt(document.getElementById('app')));
    await expect(banner).toHaveCount(0);
});
test('installed iOS app hides banner',async({page})=>{await prepare(page,'ios',true);await expect(page.locator('#install-banner')).toHaveCount(0);});
test('Android native install requires a click',async({page})=>{
    await prepare(page,'android');
    await page.evaluate(()=>{window.__prompted=0;const e=new Event('beforeinstallprompt',{cancelable:true});e.prompt=async()=>{window.__prompted++};e.userChoice=Promise.resolve({outcome:'accepted'});window.dispatchEvent(e);});
    expect(await page.evaluate(()=>window.__prompted)).toBe(0);
    await page.locator('#install-banner').getByRole('button',{name:'Install',exact:true}).click();
    expect(await page.evaluate(()=>window.__prompted)).toBe(1);await expect(page.locator('#install-banner')).toHaveCount(0);
});
