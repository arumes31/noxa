const windowMS = 300000;
const gapMS = 10000;
const fields = [
    { key: "buffer_ms", label: "historyBuffer", unit: "ms", limit: 60000 },
    { key: "concealment_percent", label: "historyConcealment", unit: "%", limit: 100 },
    { key: "loss_percent", label: "historyLoss", unit: "%", limit: 100 },
];
const validTime = value => Number.isSafeInteger(value) && value > 0 && value <= 8640000000000000;
const measured = (value, limit) => Number.isFinite(value) && value >= 0 && value <= limit ? value : null;
const scale = value => {
    const magnitude = 10 ** Math.floor(Math.log10(value));
    return [1, 2, 5, 10].find(step => step * magnitude >= value) * magnitude;
};

// Keep server observation time separate from the viewer's local clock. Missing
// values and gaps longer than two normal telemetry intervals break the line.
export function voiceHistoryModel(history, observedAt) {
    const ordered = (Array.isArray(history) ? history : []).filter(point => validTime(point?.observed_at))
        .slice().sort((a, b) => a.observed_at - b.observed_at);
    const latest = ordered.at(-1)?.observed_at;
    const ageMS = latest && validTime(observedAt) && observedAt >= latest ? observedAt - latest : null;
    const end = ageMS !== null ? observedAt : latest || 0;
    const points = [...new Map(ordered.filter(point => point.observed_at >= end - windowMS)
        .map(point => [point.observed_at, point])).values()].slice(-60).map(point => ({
        ...point, buffer_ms: measured(point.buffer_ms, 60000), loss_percent: measured(point.loss_percent, 100),
        concealment_percent: measured(point.concealment_percent, 100), discard_percent: measured(point.discard_percent, 100),
    }));
    const start = Math.min(points[0]?.observed_at ?? end, end - 30000);
    const percentMax = scale(Math.max(1, ...points.flatMap(point => [point.loss_percent || 0, point.concealment_percent || 0])));
    const series = fields.map(field => {
        const maximum = field.unit === "%" ? percentMax : scale(Math.max(100, ...points.map(point => point[field.key] || 0)));
        const segments = [];
        let segment = null, previousTime = null;
        for (const point of points) {
            const value = point[field.key];
            if (value === null) { segment = null; previousTime = null; continue; }
            if (!segment || point.observed_at - previousTime > gapMS) { segment = []; segments.push(segment); }
            segment.push({ at: point.observed_at, value }); previousTime = point.observed_at;
        }
        return { ...field, maximum, segments };
    });
    return { points, start, end, ageMS, stale: ageMS !== null && ageMS > 15000, series };
}
