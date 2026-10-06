export const benchmarkMessages = {
    en: {
        title: "Connection benchmark", help: "Send 20 seconds of silent test packets through this server’s private Echo Test, then wait 2 seconds for returns. A temporary guest appears during the test. Your voice channel stays connected; your microphone and speakers are not used.",
        limit: "Measures delivery timing, not microphone, headset or listening quality. No VPN or network settings are changed.",
        start: "Start 20-second connection test", cancel: "Cancel test", copy: "Copy test summary", unavailable: "Connect to a server to run this test.",
        connecting: "Connecting the private test…", running: "Sending test packets…", draining: "Waiting for final returns…", complete: "Connection test complete", cancelled: "Connection test cancelled", failed: "Test unavailable. The server must allow temporary guests and private Echo Test, and its trusted media connection must be reachable.",
        sent: "Packets sent / returned", tail: "Not returned within the test window", tailHelp: "Includes packets arriving after the 2-second drain; this is not a definitive loss rate.",
        rtt: "Round trip · median / p95 / maximum", gap: "Return interval · median / p95 / maximum", late: "Sender scheduling lateness · median / p95 / maximum", route: "Transport / server candidate type", noReturns: "No test packets returned. Delivery timing is unavailable.", disclaimer: "Socket-write-to-return timing only. It does not measure one-way delay or certify audible voice quality.",
    },
    de: {
        title: "Verbindungstest", help: "Sendet 20 Sekunden lautlose Testpakete durch den privaten Echo-Test dieses Servers und wartet danach 2 Sekunden auf Rückläufe. Währenddessen erscheint ein temporärer Gast. Dein Voice-Channel bleibt verbunden; Mikrofon und Lautsprecher werden nicht verwendet.",
        limit: "Misst die Paketübertragung, nicht Mikrofon, Headset oder hörbare Sprachqualität. VPN- und Netzwerkeinstellungen bleiben unverändert.",
        start: "20-Sekunden-Verbindungstest starten", cancel: "Test abbrechen", copy: "Testergebnis kopieren", unavailable: "Verbinde dich mit einem Server, um den Test zu starten.",
        connecting: "Privater Test wird verbunden…", running: "Testpakete werden gesendet…", draining: "Warte auf die letzten Rückläufe…", complete: "Verbindungstest abgeschlossen", cancelled: "Verbindungstest abgebrochen", failed: "Test nicht verfügbar. Der Server muss temporäre Gäste und den privaten Echo-Test erlauben; seine vertraute Medienverbindung muss erreichbar sein.",
        sent: "Pakete gesendet / zurückgekehrt", tail: "Nicht innerhalb des Testfensters zurückgekehrt", tailHelp: "Enthält Pakete, die erst nach der Wartezeit von 2 Sekunden ankommen; dies ist keine definitive Verlustrate.",
        rtt: "Hin- und Rückweg · Median / p95 / Maximum", gap: "Rücklaufabstand · Median / p95 / Maximum", late: "Sendeverzögerung · Median / p95 / Maximum", route: "Transport / Server-Kandidatentyp", noReturns: "Keine Testpakete zurückgekehrt. Übertragungszeiten sind nicht verfügbar.", disclaimer: "Gemessen wird vom Schreiben des Pakets bis zum Rücklauf. Dies misst weder eine Einwegverzögerung noch bestätigt es hörbare Sprachqualität.",
    },
};

export function benchmarkResultRows(result, language = "en") {
    const labels = benchmarkMessages[language] || benchmarkMessages.en;
    const count = value => Number.isSafeInteger(value) && value >= 0 ? String(value) : "—";
    const timing = value => value && [value.p50_ms, value.p95_ms, value.max_ms].every(n => Number.isFinite(n) && n >= 0)
        ? `${value.p50_ms.toFixed(1)} / ${value.p95_ms.toFixed(1)} / ${value.max_ms.toFixed(1)} ms` : "—";
    return [
        [labels.sent, `${count(result?.sent)} / ${count(result?.returned)}`],
        [labels.tail, count(result?.unreturned)],
        [labels.rtt, timing(result?.round_trip)],
        [labels.gap, timing(result?.arrival_gap)],
        [labels.late, timing(result?.send_lateness)],
        [labels.route, `${["udp", "tcp"].includes(result?.protocol) ? result.protocol.toUpperCase() : "—"} / ${["host", "srflx", "prflx", "relay"].includes(result?.candidate_type) ? result.candidate_type : "—"}`],
    ];
}

export function benchmarkSummary(result) {
    return { schema: "noxa-connection-benchmark-v1", measured_at: new Date().toISOString(),
        method: "Synthetic silent Opus RTP; client socket-write to private server echo return. No microphone capture or audio playback.",
        interpretation: "Unreturned includes packets later than the 2-second drain. This is not a definitive network-loss, one-way latency or listening-quality measurement.", ...result };
}
