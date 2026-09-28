import { expect, test } from './fixtures.js';

test.beforeEach(async ({ page }) => {
    await page.route('**/__voice_test__', route => route.fulfill({ contentType: 'text/html', body: '<!doctype html><title>Voice test</title><link rel="stylesheet" href="/src/style.css"><main id="fixture"></main>' }));
    await page.goto('/__voice_test__');
    await page.evaluate(async () => {
        window.__scope = 1; window.__sent = []; window.__uploads = []; window.__downloads = 0; window.__tracks = [];
        window.__noxa = { state: { settings: { language: 'en', volume: 50 } }, applyOutputSettings(audio) { audio.volume = window.__noxa.state.settings.volume / 100; audio.muted = !!window.__noxa.state.deafened; } };
        window.go = { main: { App: {
            UploadChatAttachmentForTab: async (...args) => { window.__uploads.push(args); window.__audio = args[3]; if (window.__holdUpload) await new Promise(resolve => { window.__releaseUpload = resolve; }); return `[file:secret-storage#secret-key#${args[2]}]`; },
            DownloadChatAttachmentForTab: async () => { window.__downloads++; return window.__audio || 'bad payload'; },
        } } };
        window.__context = new AudioContext();
        const oscillator = window.__context.createOscillator(); oscillator.start();
        navigator.mediaDevices.getUserMedia = async () => {
            const destination = window.__context.createMediaStreamDestination(); oscillator.connect(destination);
            window.__tracks.push(...destination.stream.getTracks());
            if (window.__holdCapture) await new Promise(resolve => { window.__releaseCapture = resolve; });
            return destination.stream;
        };
        window.__voiceModule = await import('/src/voice-messages.js');
        document.querySelector('#fixture').append(window.__voiceModule.voiceMessageButton(() => {
            const scope = window.__scope;
            return { tabID: 'server-a', channelID: 7, isCurrent: () => window.__scope === scope, send: async token => { window.__sent.push(token); return ''; } };
        }));
    });
});

async function record(page, milliseconds = 350) {
    await page.getByRole('button', { name: 'Record voice message', exact: true }).click();
    await page.getByRole('button', { name: 'Start recording', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Stop recording', exact: true })).toBeEnabled();
    await expect.poll(() => page.evaluate(() => window.__tracks.length)).toBe(1);
    // Allow the real MediaRecorder to emit a nonempty Opus segment.
    await page.waitForTimeout(milliseconds);
    await page.getByRole('button', { name: 'Stop recording', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Send voice message', exact: true })).toBeEnabled();
}

test('record, preview, encrypted attachment send and lazy inline playback', async ({ page }) => {
    await record(page);
    await expect(page.getByLabel('Voice message playback')).toBeVisible();
    expect(await page.evaluate(() => window.__tracks.every(track => track.readyState === 'ended'))).toBe(true);
    await page.getByRole('button', { name: 'Send voice message', exact: true }).click();
    await expect(page.locator('.voice-message-dialog')).toHaveCount(0);
    expect(await page.evaluate(() => window.__uploads[0].slice(0, 2))).toEqual(['server-a', 7]);
    await page.evaluate(() => window.__voiceModule.renderVoiceMessage(document.querySelector('#fixture'), window.__sent[0], { tabID: 'server-a', channelID: 7 }));
    expect(await page.evaluate(() => window.__downloads)).toBe(0);
    await expect(page.locator('#fixture')).not.toContainText('secret-key');
    await page.getByRole('button', { name: 'Load voice message', exact: true }).click();
    const audio = page.getByLabel('Voice message playback');
    await expect(audio).toBeVisible();
    await audio.evaluate(element => { window.__player = element; return element.play(); });
    expect(await audio.evaluate(element => element.volume)).toBe(.5);
    await page.evaluate(() => { window.__noxa.state.deafened = true; window.__noxa.state.settings.volume = 20; window.__voiceModule.refreshVoicePlayback(); });
    expect(await audio.evaluate(element => ({ muted: element.muted, volume: element.volume }))).toEqual({ muted: true, volume: .2 });
    await page.evaluate(() => document.querySelector('.voice-message').remove());
    await expect.poll(() => page.evaluate(() => window.__player.paused && !window.__player.hasAttribute('src'))).toBe(true);
});

test('discard during permission releases the late microphone without sending', async ({ page }) => {
    await page.evaluate(() => { window.__holdCapture = true; });
    await page.getByRole('button', { name: 'Record voice message', exact: true }).click();
    await page.getByRole('button', { name: 'Start recording', exact: true }).click();
    await expect.poll(() => page.evaluate(() => typeof window.__releaseCapture)).toBe('function');
    await page.getByRole('button', { name: 'Discard', exact: true }).click();
    await page.evaluate(() => window.__releaseCapture());
    await expect.poll(() => page.evaluate(() => window.__tracks[0].readyState)).toBe('ended');
    expect(await page.evaluate(() => window.__sent.length)).toBe(0);
});

test('scope change during upload cannot send the recording to another conversation', async ({ page }) => {
    await record(page);
    await page.evaluate(() => { window.__holdUpload = true; });
    await page.getByRole('button', { name: 'Send voice message', exact: true }).click();
    await expect.poll(() => page.evaluate(() => typeof window.__releaseUpload)).toBe('function');
    await page.evaluate(() => { window.__scope++; window.__releaseUpload(); });
    await expect(page.locator('.voice-message-dialog')).toHaveCount(0);
    expect(await page.evaluate(() => window.__sent.length)).toBe(0);
});

test('invalid voice payload remains retryable and never creates an unsafe player', async ({ page }) => {
    await page.evaluate(() => window.__voiceModule.renderVoiceMessage(document.querySelector('#fixture'), '[file:store#key#voice.weba]', { tabID: 'server-a', channelID: 7 }));
    await page.getByRole('button', { name: 'Load voice message', exact: true }).click();
    await expect(page.getByText('Voice message unavailable', { exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Load voice message', exact: true })).toBeEnabled();
    await expect(page.locator('audio')).toHaveCount(0);
});

test('voice waveform, seeking, speed and private local resume survive reopening', async ({ page }) => {
    await record(page, 2200);
    await expect(page.getByRole('img', {name:'Voice message waveform'})).toBeVisible();
    await page.getByRole('button', {name:'Send voice message', exact:true}).click();
    await expect(page.locator('.voice-message-dialog')).toHaveCount(0);
    await page.evaluate(() => { window.__remoteDecodes = 0; AudioContext.prototype.decodeAudioData = () => { window.__remoteDecodes++; throw new Error('Remote media must not expand into a complete PCM buffer'); }; });
    const mount = () => page.evaluate(() => window.__voiceModule.renderVoiceMessage(document.querySelector('#fixture'), window.__sent[0], {tabID:'server-a',channelID:7}));
    await mount(); await page.getByRole('button', {name:'Load voice message',exact:true}).click();
    const seek = page.getByRole('slider', {name:'Seek voice message'});
    await expect(seek).toBeEnabled();
    await expect.poll(() => seek.getAttribute('max').then(Number)).toBeGreaterThan(1);
    await page.getByLabel('Playback speed').selectOption('1.5');
    await seek.fill('0.4');
    await expect.poll(() => page.getByLabel('Voice message playback').evaluate(audio => audio.currentTime)).toBeCloseTo(.4,1);
    await expect.poll(() => page.evaluate(() => JSON.parse(localStorage.getItem('noxa:voice-playback:v1') || '[]')[0]?.position)).toBeCloseTo(.4,1);
    expect(await page.evaluate(() => localStorage.getItem('noxa:voice-playback:v1'))).not.toContain('secret-key');
    await page.getByLabel('Voice message playback').evaluate(audio => audio.play());
    await expect.poll(() => page.getByRole('img', {name:'Voice message waveform'}).evaluate(canvas => {
        const pixels = canvas.getContext('2d').getImageData(0, 0, canvas.width, 15).data;
        return pixels.some((value, index) => index % 4 === 3 && value > 0);
    })).toBe(true);
    await expect.poll(() => page.getByLabel('Voice message playback').evaluate(audio => audio.currentTime)).toBeGreaterThan(.65);
    const removedAt = await page.evaluate(() => { const position = document.querySelector('.voice-message audio').currentTime; document.querySelector('.voice-message').remove(); return position; });
    await expect.poll(() => page.evaluate(() => JSON.parse(localStorage.getItem('noxa:voice-playback:v1'))[0].position)).toBeCloseTo(removedAt, 1);
    await mount(); await page.getByRole('button', {name:'Load voice message',exact:true}).click();
    await expect(page.getByLabel('Playback speed')).toHaveValue('1.5');
    await expect.poll(() => page.getByLabel('Voice message playback').evaluate(audio => audio.currentTime)).toBeCloseTo(removedAt,1);
    expect(await page.evaluate(() => window.__remoteDecodes)).toBe(0);
});
