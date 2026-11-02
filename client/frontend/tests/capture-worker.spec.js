import { test, expect } from '@playwright/test';
async function fixture(page) {
    await page.route('**/capture-worker-fixture', route => route.fulfill({contentType:'text/html',body:'<!doctype html><title>Capture test</title>'}));
    await page.goto('/capture-worker-fixture');
}
test('capture alignment delivers frames while the owning UI thread is busy', async ({ page }) => {
    await fixture(page);
    const result = await page.evaluate(async () => {
        const { cropDisplayStream } = await import('/src/share-region.js');
        const source = new MediaStreamTrackGenerator({ kind: 'video' });
        source.getSettings = () => ({ width: 322, height: 182 }); source.applyConstraints = async () => {};
        const { stream } = cropDisplayStream(new MediaStream([source]), { x: 0, y: 0, width: 1, height: 1 });
        const track = stream.getVideoTracks()[0];
        await track.applyConstraints({ width: { max: 320 }, height: { max: 180 }, frameRate: { max: 30 } });
        const urls = [], workers = [];
        const make = fn => { const url = URL.createObjectURL(new Blob([`(${fn.toString()})()`], {type:'text/javascript'})); urls.push(url); const worker = new Worker(url); workers.push(worker); return worker; };
        const input = make(() => { self.onmessage = async ({ data }) => {
            const writer = data.writable.getWriter(), bytes = new Uint8Array(322 * 182 * 3 / 2); bytes.fill(128);
            while (true) { const frame = new VideoFrame(bytes, { format:'I420', codedWidth:322, codedHeight:182, timestamp: Math.round((performance.timeOrigin + performance.now()) * 1000) }); try { await writer.write(frame); } finally { frame.close(); } await new Promise(r => setTimeout(r, 30)); }
        }; });
        const output = make(() => { self.onmessage = async ({ data }) => {
            const reader = data.readable.getReader(), rows = []; self.onmessage = () => self.postMessage(rows);
            while (true) { const { value, done } = await reader.read(); if (done) break; rows.push({time:Date.now(),width:value.displayWidth,height:value.displayHeight}); value.close(); }
        }; });
        const processor = new MediaStreamTrackProcessor({track, maxBufferSize:1});
        output.postMessage({readable:processor.readable}, [processor.readable]); input.postMessage({writable:source.writable}, [source.writable]);
        try {
            await new Promise(r => setTimeout(r, 400)); const start = Date.now();
            while (Date.now() - start < 1200) { /* bounded UI load */ }
            const end = Date.now(), rows = await new Promise(resolve => { output.onmessage = event => resolve(event.data); output.postMessage({snapshot:true}); });
            await track.applyConstraints({ width: { max: 160 }, height: { max: 90 }, frameRate: { max: 30 } });
            const deadline = performance.now() + 3000;
            let resized = [];
            while (!resized.length && performance.now() < deadline) {
                await new Promise(resolve => setTimeout(resolve, 30));
                const updated = await new Promise(resolve => { output.onmessage = event => resolve(event.data); output.postMessage({snapshot:true}); });
                resized = updated.filter(row => row.width <= 160 && row.height <= 90);
            }
            return { during: rows.filter(row => row.time > start + 100 && row.time < end - 100), resized };
        } finally { track.stop(); for (const worker of workers) worker.terminate(); for (const url of urls) URL.revokeObjectURL(url); }
    });
    expect(result.resized.length).toBeGreaterThan(0);
    expect(result.resized.every(row => row.width % 2 === 0 && row.height % 2 === 0)).toBe(true);
    expect(result.during.length).toBeGreaterThanOrEqual(15);
    expect(result.during.every(row => row.width % 2 === 0 && row.height % 2 === 0 && row.width <= 320 && row.height <= 180)).toBe(true);
});
test('capture worker failure ends publication and releases the selected source', async ({ page }) => {
    await fixture(page);
    const result = await page.evaluate(async () => {
        const { cropDisplayStream } = await import('/src/share-region.js');
        const OriginalWorker = window.Worker; let captureWorker;
        window.Worker = class extends OriginalWorker { constructor(...args) { super(...args); captureWorker = this; } };
        const source = new MediaStreamTrackGenerator({kind:'video'}); source.getSettings = () => ({width:320,height:180}); source.applyConstraints = async () => {};
        let sourceStops = 0; const stop = source.stop.bind(source); source.stop = () => { sourceStops++; stop(); };
        try {
            const {stream} = cropDisplayStream(new MediaStream([source]), {x:0,y:0,width:1,height:1}); const track = stream.getVideoTracks()[0]; let ended = 0; track.addEventListener('ended',()=>ended++);
            await track.applyConstraints({width:320,height:180}); captureWorker.dispatchEvent(new ErrorEvent('error')); await new Promise(r=>setTimeout(r,50)); track.stop();
            return {sourceStops,ended,source:source.readyState,output:track.readyState};
        } finally {window.Worker=OriginalWorker;source.stop();}
    });
    expect(result).toEqual({sourceStops:1,ended:1,source:'ended',output:'ended'});
});
for (const mode of ['construction', 'transfer']) test(`capture ${mode} failure releases the original source and generated track`, async ({ page }) => {
    await fixture(page);
    const result = await page.evaluate(async mode => {
        const { cropDisplayStream } = await import('/src/share-region.js');
        const OriginalWorker = window.Worker, OriginalGenerator = window.MediaStreamTrackGenerator;
        const source = new OriginalGenerator({kind:'video'});
        source.getSettings = () => ({width:320,height:180}); source.applyConstraints = async () => {};
        let sourceStops = 0, output;
        const stop = source.stop.bind(source); source.stop = () => { sourceStops++; stop(); };
        window.MediaStreamTrackGenerator = new Proxy(OriginalGenerator, {construct(target,args) { output=Reflect.construct(target,args,target); return output; }});
        window.Worker = mode === 'construction' ? class {constructor() { throw new Error('capture worker denied'); }} : class extends OriginalWorker {
            postMessage(message,...args) { if (message.op==='init') throw new DOMException('capture transfer denied','DataCloneError'); return super.postMessage(message,...args); }
        };
        try {
            let error;
            try { cropDisplayStream(new MediaStream([source]),{x:0,y:0,width:1,height:1}); }
            catch (failure) { error=failure.message; }
            return {sourceStops,source:source.readyState,output:output?.readyState,error};
        } finally {window.Worker=OriginalWorker;window.MediaStreamTrackGenerator=OriginalGenerator;source.stop();output?.stop();}
    },mode);
    expect(result).toEqual({sourceStops:1,source:'ended',output:'ended',error:mode==='construction'?'capture worker denied':'capture transfer denied'});
});
test('an applied output acknowledgement survives a UI task longer than its watchdog window', async ({ page }) => {
    await fixture(page);
    const result = await page.evaluate(async () => {
        const { cropDisplayStream } = await import('/src/share-region.js');
        const OriginalWorker = window.Worker;
        let outputSent; const sent = new Promise(resolve => {outputSent=resolve;});
        window.Worker = class extends OriginalWorker {
            postMessage(message,...args) {super.postMessage(message,...args);if(message.op==='output')outputSent();}
        };
        const source = new MediaStreamTrackGenerator({kind:'video'});
        source.getSettings=()=>({width:322,height:182});source.applyConstraints=async()=>{};
        let stops=0;const stop=source.stop.bind(source);source.stop=()=>{stops++;stop();};
        let track;
        try {
            track=cropDisplayStream(new MediaStream([source]),{x:0,y:0,width:1,height:1}).stream.getVideoTracks()[0];
            const pending=track.applyConstraints({width:{max:320},height:{max:180}});
            await sent;
            const start=performance.now();while(performance.now()-start<6500){/* bounded UI stall */}
            await pending;
            return {source:source.readyState,output:track.readyState,stops,constraints:track.getConstraints()};
        } finally {window.Worker=OriginalWorker;track?.stop();source.stop();}
    });
    expect(result).toEqual({source:'live',output:'live',stops:0,constraints:{width:{max:320},height:{max:180}}});
});
