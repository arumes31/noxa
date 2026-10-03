// connection-quality.js — connection quality sampling and freshness.
import { t } from "./i18n.js";


export function createConnectionQuality({ $, state }) {
    function qualityFromPing(pingMs, known) {
        if (!known) return "";
        if (pingMs < 100) return "good";
        if (pingMs < 250) return "fair";
        return "poor";
    }

    let qualityTimer = null;
    let qualityAgeTimer = null;
    let qualitySamplerEpoch = 0;
    let lastQualitySample = null;

    function qualitySampleAge(at, now = Date.now()) {
        const seconds = Math.max(0, Math.floor((now - at) / 1000));
        if (seconds < 5) return t("runtime.justNow");
        if (seconds < 60) return t(seconds === 1 ? "runtime.secondAgo" : "runtime.secondsAgo", { count: seconds });
        const minutes = Math.floor(seconds / 60);
        return t(minutes === 1 ? "runtime.minuteAgo" : "runtime.minutesAgo", { count: minutes });
    }

    function renderQualitySample() {
        if (!lastQualitySample) return;
        const pill = $("conn-pill");
        const latency = $("voice-latency");
        const stale = Date.now() - lastQualitySample.at >= 15000;
        const quality = stale ? "stale" : lastQualitySample.quality;
        const qualityLabel = t(`runtime.quality.${quality}`);
        const text = `${lastQualitySample.pingMs} ms${stale ? " · " + qualityLabel : ""}`;
        const label = t("runtime.latencyLabel", { ping: lastQualitySample.pingMs, quality: qualityLabel });
        // The existing age ticker runs each second; only change visible DOM when
        // the value or freshness actually changes. No layout reads or new timers.
        if (latency.textContent !== text) latency.textContent = text;
        if (latency.dataset.quality !== quality) latency.dataset.quality = quality;
        if (latency.getAttribute("aria-label") !== label) latency.setAttribute("aria-label", label);
        if (latency.hidden) latency.hidden = false;
        if (pill.dataset.quality !== quality) pill.dataset.quality = quality;
        pill.title = t("runtime.qualityTitle", { quality: t(`runtime.quality.${lastQualitySample.quality}`), ping: lastQualitySample.pingMs, age: qualitySampleAge(lastQualitySample.at) });
        const title = t("runtime.latencyTitle", { quality: qualityLabel, age: qualitySampleAge(lastQualitySample.at) });
        if (latency.title !== title) latency.title = title;
    }

    function startQualitySampler() {
        stopQualitySampler();
        const epoch = qualitySamplerEpoch;
        const generation = state.serverGeneration;
        const tabID = state.activeTabID;
        let inFlight = false;
        if (state.myClientID) {
            $("voice-latency").hidden = false;
            $("voice-latency").textContent = "— ms";
            $("voice-latency").setAttribute("aria-label", t("runtime.latencyWaiting"));
        }
        const sample = async () => {
            if (!state.myClientID || inFlight) return;
            inFlight = true;
            const clientID = state.myClientID;
            try {
                const info = await window.go.main.App.GetClientInfoForTab(tabID, clientID);
                if (epoch !== qualitySamplerEpoch || generation !== state.serverGeneration ||
                    clientID !== state.myClientID) return;
                const q = qualityFromPing(info.ping_ms, Number.isFinite(info.ping_ms) && info.ping_ms >= 0);
                if (q) {
                    lastQualitySample = { quality: q, pingMs: info.ping_ms, at: Date.now() };
                    renderQualitySample();
                }
            } catch {
                // Keep the last successful sample, but age it so stale telemetry
                // is never presented as current.
                if (epoch === qualitySamplerEpoch) renderQualitySample();
            } finally {
                inFlight = false;
            }
        };
        sample();
        qualityTimer = setInterval(sample, 5000);
        qualityAgeTimer = setInterval(renderQualitySample, 1000);
    }

    function stopQualitySampler() {
        qualitySamplerEpoch++;
        if (qualityTimer) {
            clearInterval(qualityTimer);
            qualityTimer = null;
        }
        if (qualityAgeTimer) {
            clearInterval(qualityAgeTimer);
            qualityAgeTimer = null;
        }
        lastQualitySample = null;
        const pill = $("conn-pill");
        delete pill.dataset.quality;
        pill.title = pill.classList.contains("up") ? "" : t("runtime.latencyOffline");
        const latency = $("voice-latency");
        latency.hidden = true;
        latency.textContent = "";
        latency.removeAttribute("data-quality");
        latency.removeAttribute("aria-label");
        latency.removeAttribute("title");
    }

    // ---------------------------------------------------------------------------

    return { startQualitySampler, stopQualitySampler };
}
