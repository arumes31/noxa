import { test, expect } from "@playwright/test";

test("static speech decodes and frequent contact cues survive mandatory repetition", async ({ page }) => {
    test.setTimeout(120000);
    await page.goto("/");
    const result=await page.evaluate(async()=>{
        const {SoundEngine}=await import("/src/sound-engine.js");
        const {SOUND_DEFINITIONS}=await import("/src/sound-catalog.js");
        const {SPEECH_ASSETS}=await import("/src/speech-catalog.js");
        const engine=new SoundEngine({getState:()=>({settings:{sound_volume:100}}),isDND:()=>false,
            createContext:()=>new AudioContext({latencyHint:"interactive"}),load:async url=>(await fetch(url)).arrayBuffer()});
        await engine.preload();await engine.resume();
        let speech=0;
        for(const clips of Object.values(SPEECH_ASSETS))for(const clip of Object.values(clips)){
            await engine.ctx.decodeAudioData(await (await fetch(clip.url)).arrayBuffer());speech++;
        }
        const counts={};
        for(const [events,repetitions] of [[["ptt_on","ptt_off"],100],[["user_join"],50],[["user_leave"],50],[["channel_message"],50],[["mic_on","mic_off"],50],[["own_channel_switch"],30]]){
            for(let i=0;i<repetitions;i++)for(const event of events){
                if(!engine.play(event,{force:true}))throw Error(event+" failed");
                counts[event]=(counts[event]||0)+1;
                await new Promise(resolve=>setTimeout(resolve,SOUND_DEFINITIONS[event].duration*1000+30));
            }
        }
        const active=engine.active.size,retiring=engine.retiring.size;
        await engine.dispose();return {counts,active,retiring,speech};
    });
    expect(result.speech).toBe(18);expect(result.active).toBe(0);expect(result.retiring).toBe(0);
    expect(result.counts).toEqual({ptt_on:100,ptt_off:100,user_join:50,user_leave:50,channel_message:50,mic_on:50,mic_off:50,own_channel_switch:30});
});

test("original sound set decodes, completes Test All, and releases all source nodes", async ({ page }) => {
    await page.goto("/");
    const result = await page.evaluate(async () => {
        window.__voicx = { state: { settings: { play_sounds: false, sound_volume: 100 } } };
        window.__voicxPolish = { dndActive: () => false };
        const { SoundEngine } = await import("/src/sound-engine.js");
        const { SOUND_EVENTS } = await import("/src/sound-catalog.js");
        const engine = new SoundEngine({
            getState: () => window.__voicx.state, isDND: () => false,
            createContext: () => new AudioContext({ latencyHint: "interactive" }),
            load: async url => (await fetch(url)).arrayBuffer(),
        });
        await engine.preload(); await engine.resume();
        const count = engine.buffers.size;
        const durations = [...engine.buffers.values()].map(b => b.duration);
        const ctx = engine.ctx;
        const source = ctx.createBufferSource.bind(ctx);
        let created = 0, ended = 0;
        ctx.createBufferSource = () => {
            const node = source(); created++;
            node.addEventListener("ended", () => ended++);
            return node;
        };
        for (const name of SOUND_EVENTS) {
            if (!engine.play(name, { force: true })) throw Error("Preview failed: " + name);
            await new Promise(r => setTimeout(r, 500));
        }
        const active = engine.active.size;
        await engine.dispose();
        return { count, durations, active, created, ended, closed: ctx.state };
    });
    expect(result.count).toBe(32);
    expect(result.created).toBe(32);
    expect(result.ended).toBe(32);
    expect(result.active).toBe(0);
    expect(result.closed).toBe("closed");
    expect(Math.max(...result.durations)).toBeLessThan(.501);
});

test("four simultaneous cues at maximum gain retain headroom in real WebAudio", async ({ page }) => {
    await page.goto("/");
    const result = await page.evaluate(async () => {
        const { SOUND_URLS } = await import("/src/sound-catalog.js");
        const peaks = [];
        for (const volume of [0, 1, 2]) {
            const ctx = new OfflineAudioContext(1, 48000, 48000);
            const gain = ctx.createGain(); gain.gain.value = volume; gain.connect(ctx.destination);
            const bytes = await (await fetch(SOUND_URLS.connection_lost)).arrayBuffer();
            const buffer = await ctx.decodeAudioData(bytes);
            for (let i = 0; i < 4; i++) {
                const source = ctx.createBufferSource(); source.buffer = buffer;
                source.connect(gain); source.start();
            }
            const rendered = await ctx.startRendering();
            peaks.push(rendered.getChannelData(0).reduce((max, sample) => Math.max(max, Math.abs(sample)), 0));
        }
        return peaks;
    });
    expect(result[0]).toBe(0);
    expect(result[1]).toBeGreaterThan(.1);
    expect(result[2]).toBeLessThanOrEqual(.921);
    expect(result[2]).toBeCloseTo(result[1] * 2, 4);
});
