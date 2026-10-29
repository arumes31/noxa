// studio-clock.worker.js — the Studio program's frame clock.
// Dedicated-worker timers are not subject to the page's background timer
// throttling, so a hidden or minimised window keeps receiving ticks at the
// program rate. The worker carries no media and no state beyond the period.
let timer = null;
self.onmessage = event => {
    clearInterval(timer);
    timer = null;
    const period = Number(event.data?.period);
    if (Number.isFinite(period) && period >= 10 && period <= 1000) timer = setInterval(() => self.postMessage(0), period);
};
