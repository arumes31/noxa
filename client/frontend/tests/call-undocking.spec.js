import {test, expect} from './fixtures.js';

test.beforeEach(async ({page}) => {
    await page.route('**/__undock_test__', route => route.fulfill({contentType:'text/html',body:'<!doctype html><title>Call undocking</title><link rel="stylesheet" href="/src/private-calls.css">'}));
    await page.goto('/__undock_test__');
});

test('optional call undock moves live controls and media, then close docks without ending capture', async ({page}) => {
    await page.evaluate(async () => {
        const {callUndockButton} = await import('/src/call-undocking.js');
        const panel = document.createElement('aside'); panel.id = 'undock-test'; panel.className = 'private-call-panel';
        const camera = document.createElement('video'); camera.muted=true; camera.autoplay=true;
        const canvas = document.createElement('canvas'); canvas.width=160;canvas.height=90;canvas.getContext('2d').fillRect(0,0,160,90);
        window.__undockStream=canvas.captureStream(1);camera.srcObject=window.__undockStream;
        const mute = document.createElement('button');mute.textContent='Test mute';mute.onclick=()=>{window.__undockMuted=true;};
        panel.append(camera,mute,callUndockButton(panel));document.body.append(panel);window.__undockPanel=panel;
    });
    await page.locator('#undock-test').getByRole('button',{name:'Undock call',exact:true}).click();
    await expect.poll(()=>page.evaluate(()=>window.__undockPanel.ownerDocument !== document)).toBe(true);
    await page.evaluate(()=>window.__undockPanel.querySelector('button').click());
    expect(await page.evaluate(()=>window.__undockMuted)).toBe(true);
    expect(await page.evaluate(()=>window.__undockStream.getVideoTracks()[0].readyState)).toBe('live');
    await page.evaluate(()=>window.documentPictureInPicture.window.close());
    await expect(page.locator('#undock-test')).toBeVisible();
    await expect(page.locator('#undock-test').getByRole('button',{name:'Undock call',exact:true})).toBeVisible();
    await expect(page.locator('#undock-test').getByRole('button',{name:'Undock call',exact:true})).toBeFocused();
    expect(await page.evaluate(()=>window.__undockStream.getVideoTracks()[0].readyState)).toBe('live');
    await page.evaluate(()=>window.__undockStream.getTracks().forEach(track=>track.stop()));
});

test('docking restores focus to a recreated call control without changing background focus on teardown', async ({page}) => {
    await page.evaluate(async () => {
        const module = await import('/src/call-undocking.js');
        const panel = document.createElement('aside'); panel.id = 'undock-test';
        panel.append(module.callUndockButton(panel)); document.body.append(panel);
        const draft = document.createElement('input'); draft.id = 'other-draft'; document.body.append(draft);
        window.__panel = panel; window.__undockModule = module;
    });
    await page.getByRole('button', {name:'Undock call', exact:true}).press('Enter');
    await expect.poll(() => page.evaluate(() => window.__panel.ownerDocument !== document)).toBe(true);
    await page.evaluate(() => {
        const control = window.__undockModule.callUndockButton(window.__panel);
        window.__panel.replaceChildren(control);
        control.click();
    });
    await expect(page.getByRole('button', {name:'Undock call', exact:true})).toBeFocused();
    await page.getByRole('button', {name:'Undock call', exact:true}).click();
    await expect.poll(() => page.evaluate(() => window.__panel.ownerDocument !== document)).toBe(true);
    await page.locator('#other-draft').focus();
    await page.evaluate(() => { window.__undockModule.closeCallUndock(window.__panel); window.__panel.remove(); });
    await expect(page.locator('#other-draft')).toBeFocused();
});

test('channel docking restores focus to the closed voice options summary', async ({page}) => {
    await page.evaluate(async () => {
        document.body.innerHTML = '<div id="voice-bar"><div class="voice-buttons"><details id="voice-options"><summary>More voice options</summary><div class="voice-secondary-actions"></div></details></div></div>';
        window.__noxa = {state:{pc:{}, activeTabID:'one', serverGeneration:1, myChannelID:1, channels:[], clients:[], settings:{activation_mode:'continuous'}}};
        (await import('/src/call-undocking.js')).initChannelUndocking();
        const options = document.getElementById('voice-options');
        options.addEventListener('click', event => {
            if (!event.target.closest('[data-channel-undock]')) return;
            options.open = false;
            if (options.contains(document.activeElement)) options.querySelector('summary').focus();
        });
    });
    await page.getByText('More voice options', {exact:true}).click();
    await page.locator('[data-channel-undock]').click();
    await expect.poll(() => page.evaluate(() => !!window.documentPictureInPicture.window)).toBe(true);
    await page.evaluate(() => window.documentPictureInPicture.window.close());
    await expect(page.locator('#voice-options > summary')).toBeFocused();
});

test('a delayed undock request cannot reopen a removed call', async ({page}) => {
    await page.evaluate(async () => {
        const module = await import('/src/call-undocking.js');
        const request = window.documentPictureInPicture.requestWindow.bind(window.documentPictureInPicture);
        window.documentPictureInPicture.requestWindow = async options => {
            const target = await request(options);
            return new Promise(resolve => { window.__finishUndock = () => resolve(target); });
        };
        const panel = document.createElement('aside'); panel.id = 'undock-test';
        panel.append(module.callUndockButton(panel)); document.body.append(panel);
        window.__panel = panel; window.__undockModule = module;
    });
    await page.getByRole('button', {name:'Undock call', exact:true}).click();
    await page.waitForFunction(() => !!window.__finishUndock);
    await page.evaluate(() => { window.__undockModule.closeCallUndock(window.__panel); window.__panel.remove(); window.__finishUndock(); });
    await expect.poll(() => page.evaluate(() => window.documentPictureInPicture.window === null)).toBe(true);
    await expect(page.locator('#undock-test')).toHaveCount(0);
});

test('ending a call closes its detached window and a late request cannot resurrect it', async ({page}) => {
    await page.evaluate(async () => {
        const module = await import('/src/call-undocking.js');
        window.__undockModule=module;const panel=document.createElement('aside');panel.id='undock-test';panel.append(module.callUndockButton(panel));document.body.append(panel);
        window.__panel=panel;
    });
    await page.locator('#undock-test').getByRole('button',{name:'Undock call',exact:true}).click();
    await expect.poll(()=>page.evaluate(()=>!!window.documentPictureInPicture.window)).toBe(true);
    await page.evaluate(()=>{window.__undockModule.closeCallUndock(window.__panel);window.__panel.remove();});
    await expect.poll(()=>page.evaluate(()=>window.documentPictureInPicture.window === null)).toBe(true);
    await expect(page.locator('#undock-test')).toHaveCount(0);
});

test('detached channel push-to-talk releases on keyup, docking and channel changes', async ({page}) => {
    await page.evaluate(async () => {
        document.body.innerHTML = '<div id="voice-bar"><div class="voice-buttons"><button id="ptt-btn">Hold to talk</button><button id="voice-mute">Mute</button></div></div><div id="video-grid"></div>';
        window.__pttEvents = [];
        window.__noxa = { state: { pc: {}, activeTabID: 'one', serverGeneration: 1, myChannelID: 1, channels: [], clients: [], settings: {activation_mode:'ptt'} }, setPTT(active) {window.__pttEvents.push(active);window.__noxa.state.pttActive=active;} };
        const module = await import('/src/call-undocking.js'); module.initChannelUndocking();
    });
    await page.locator('[data-channel-undock]').click();
    await expect.poll(() => page.evaluate(() => window.documentPictureInPicture.window?.document.querySelector('[data-undock-ptt]')?.hidden)).toBe(false);
    await page.evaluate(() => { const hold=window.documentPictureInPicture.window.document.querySelector('[data-undock-ptt]');hold.dispatchEvent(new KeyboardEvent('keydown',{key:' '})); });
    expect(await page.evaluate(() => window.__noxa.state.pttActive)).toBe(true);
    await page.evaluate(() => { const hold=window.documentPictureInPicture.window.document.querySelector('[data-undock-ptt]');hold.dispatchEvent(new KeyboardEvent('keyup',{key:' '})); });
    expect(await page.evaluate(() => window.__noxa.state.pttActive)).toBe(false);
    await page.evaluate(() => { const hold=window.documentPictureInPicture.window.document.querySelector('[data-undock-ptt]');hold.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter'}));window.documentPictureInPicture.window.close(); });
    await expect.poll(() => page.evaluate(() => window.__noxa.state.pttActive)).toBe(false);
    await page.locator('[data-channel-undock]').click();
    await expect.poll(() => page.evaluate(() => window.documentPictureInPicture.window?.document.querySelector('[data-undock-ptt]')?.hidden)).toBe(false);
    await page.evaluate(() => { const hold=window.documentPictureInPicture.window.document.querySelector('[data-undock-ptt]');hold.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter'}));window.__noxa.state.myChannelID=2; });
    await expect.poll(() => page.evaluate(() => window.documentPictureInPicture.window === null)).toBe(true);
    expect(await page.evaluate(() => window.__noxa.state.pttActive)).toBe(false);
    expect(await page.evaluate(() => window.__pttEvents)).toEqual([true,false,true,false,true,false]);
});
