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
    expect(await page.evaluate(()=>window.__undockStream.getVideoTracks()[0].readyState)).toBe('live');
    await page.evaluate(()=>window.__undockStream.getTracks().forEach(track=>track.stop()));
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
