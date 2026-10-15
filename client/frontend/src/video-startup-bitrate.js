const primaryCodecs = new Set(["h264", "h265", "vp8"]);
const directionPattern = /^a=(sendrecv|sendonly|recvonly|inactive)$/;
const tokenPattern = /^[A-Za-z0-9!#$%&'*+.^_`{|}~-]+$/;

// Chromium reads this remote codec parameter in kbit/s. This helper does not
// authorize a reset: the caller owns first-demand, fresh-BWE and peer guards.
// An explicit payload type prevents guessing between negotiated H264 formats.
export function inspectScreenStartupCodec(answer, options = {}, ownedBitrateKbps, local = false) {
    const sdp = answer?.sdp;
    const unchanged = () => null;
    const { mid, payloadType, bitrateKbps } = options || {};
    if (!["answer", "offer"].includes(answer?.type) || typeof sdp !== "string" || sdp.length > 1024 * 1024 ||
        typeof mid !== "string" || !tokenPattern.test(mid) ||
        !Number.isInteger(payloadType) || payloadType < 0 || payloadType > 127 ||
        !Number.isInteger(bitrateKbps) || bitrateKbps < 1 || bitrateKbps > 2000 ||
        /[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/.test(sdp) || sdp.replace(/\r\n/g, "").includes("\r")) return unchanged();

    const lines = [];
    for (const match of sdp.matchAll(/([^\r\n]*)(\r\n|\n|$)/g)) {
        if (!match[0]) continue;
        if (!/^[a-z]=/.test(match[1])) return unchanged();
        lines.push({ text: match[1], eol: match[2], offset: match.index });
    }
    if (lines[0]?.text !== "v=0") return unchanged();

    const session = [], sections = [];
    let section = null;
    for (const line of lines) {
        if (line.text.startsWith("m=")) {
            const match = /^m=([^ ]+) ([0-9]+) ([^ ]+) ([A-Za-z0-9-]+(?: [A-Za-z0-9-]+)*)$/.exec(line.text);
            if (!match || !Number.isSafeInteger(Number(match[2])) || Number(match[2]) > 65535) return unchanged();
            if ((match[1] === "audio" || match[1] === "video") && !/^[0-9]+(?: [0-9]+)*$/.test(match[4])) return unchanged();
            const formats = match[4].split(" ").map(Number);
            if ((match[1] === "audio" || match[1] === "video") &&
                (formats.some(pt => pt > 127) || new Set(formats).size !== formats.length)) return unchanged();
            section = { lines: [], kind: match[1], port: Number(match[2]), protocol: match[3], formats };
            sections.push(section);
        }
        (section?.lines || session).push(line);
    }

    const mids = new Set();
    let target;
    for (const candidate of sections) {
        const declarations = candidate.lines.filter(line => line.text.startsWith("a=mid:"));
        if (declarations.length !== 1) return unchanged();
        const value = declarations[0].text.slice(6);
        if (!tokenPattern.test(value) || mids.has(value)) return unchanged();
        mids.add(value);
        if (value === mid) target = candidate;
    }
    if (!target || target.kind !== "video" || target.port === 0 ||
        target.protocol !== "UDP/TLS/RTP/SAVPF" || !target.formats.includes(payloadType)) return unchanged();

    const direction = group => {
        const declarations = group.filter(line => /^a=(?:sendrecv|sendonly|recvonly|inactive)\b/.test(line.text));
        if (declarations.length > 1) return null;
        return declarations.length ? directionPattern.exec(declarations[0].text)?.[1] ?? null : undefined;
    };
    const sessionDirection = direction(session), mediaDirection = direction(target.lines);
    if (sessionDirection === null || mediaDirection === null) return unchanged();
    const effectiveDirection = mediaDirection ?? sessionDirection ?? "sendrecv";
    if (!local && effectiveDirection !== "recvonly" && effectiveDirection !== "sendrecv") return unchanged();

    const mappings = new Map(), parameters = new Map();
    for (const line of target.lines) {
        if (line.text.startsWith("a=rtpmap")) {
            const match = /^a=rtpmap:([0-9]+) ([A-Za-z0-9-]+)\/([0-9]+)(?:\/([0-9]+))?$/.exec(line.text);
            if (!match) return unchanged();
            const pt = Number(match[1]);
            if (!target.formats.includes(pt) || mappings.has(pt)) return unchanged();
            mappings.set(pt, { codec: match[2].toLowerCase(), clock: Number(match[3]), channels: match[4], line });
        } else if (line.text.startsWith("a=fmtp")) {
            const match = /^a=fmtp:([0-9]+) (\S.*)$/.exec(line.text);
            if (!match) return unchanged();
            const pt = Number(match[1]);
            if (!target.formats.includes(pt) || parameters.has(pt)) return unchanged();
            // Only an exact selected-payload hint from the ownership ledger may pass.
            if (/(?:^|;)\s*x-google-start-bitrate\s*(?:=|;|$)/i.test(match[2]) &&
                (!(local && pt !== payloadType) && (pt !== payloadType || ownedBitrateKbps !== bitrateKbps))) return unchanged();
            parameters.set(pt, { value: match[2], line });
        }
    }
    if (target.formats.some(pt => !mappings.has(pt))) return unchanged();
    const selected = mappings.get(payloadType);
    if (!primaryCodecs.has(selected.codec) || selected.clock !== 90000 || selected.channels !== undefined) return unchanged();

    for (const [pt, parameter] of parameters) {
        if (mappings.get(pt).codec === "red" && /^[0-9]+(?:\/[0-9]+)*$/.test(parameter.value)) continue;
        const keys = new Set();
        for (const item of parameter.value.split(";")) {
            const match = /^\s*([^=\s]+)\s*=\s*(\S.*?)\s*$/.exec(item);
            if (!match || !tokenPattern.test(match[1]) || keys.has(match[1].toLowerCase())) return unchanged();
            keys.add(match[1].toLowerCase());
        }
    }
    const existing = parameters.get(payloadType);
    const selectedParams = new Map();
    let hint = null;
    if (existing) {
        const segments = [...existing.value.matchAll(/(?:^|;)([^;]*)/g)];
        for (const segment of segments) {
            const match = /^\s*([^=\s]+)\s*=\s*(\S.*?)\s*$/.exec(segment[1]);
            const key = match[1].toLowerCase();
            if (key === "x-google-start-bitrate") {
                if (match[2] !== String(ownedBitrateKbps)) return unchanged();
                const valueOffset = existing.line.offset + existing.line.text.indexOf(existing.value);
                let start = valueOffset + segment.index, end = start + segment[0].length;
                if (segments.length === 1) { start = existing.line.offset; end = start + existing.line.text.length + existing.line.eol.length; }
                else if (segment.index === 0) end++; // Consume the following separator.
                hint = { start, end };
            } else selectedParams.set(key, match[2]);
        }
    }
    const effective = name => {
        const own = target.lines.filter(line => line.text.startsWith(`a=${name}:`));
        const values = own.length ? own : session.filter(line => line.text.startsWith(`a=${name}:`));
        return values.length === 1 ? values[0].text.slice(name.length + 3) : null;
    };
    const ufrag = effective("ice-ufrag"), password = effective("ice-pwd"), fingerprint = effective("fingerprint"), setup = effective("setup");
    const secure = /^[A-Za-z0-9+/]{4,256}$/.test(ufrag || "") && /^[A-Za-z0-9+/]{22,256}$/.test(password || "") &&
        /^sha-256 (?:[0-9a-f]{2}:){31}[0-9a-f]{2}$/i.test(fingerprint || "") &&
        (answer.type === "offer" ? ["actpass", "active", "passive"] : ["active", "passive"]).includes(setup);
    return { identity: JSON.stringify([mid, payloadType, selected.codec, selected.clock,
        [...selectedParams].sort(([a], [b]) => a.localeCompare(b))]),
        security: secure ? JSON.stringify([ufrag, password, fingerprint.toLowerCase()]) : null,
        primaryPayloadType: target.formats.find(pt => primaryCodecs.has(mappings.get(pt)?.codec)),
        hint, existing, selected, target, sdp };
}

export function withScreenStartupBitrateHint(answer, options = {}) {
    const sdp = answer?.sdp;
    const unchanged = () => ({ sdp, changed: false });
    if (answer?.type !== "answer") return unchanged();
    const parsed = inspectScreenStartupCodec(answer, options);
    if (!parsed) return unchanged();
    const { existing, selected, target } = parsed;
    const { payloadType, bitrateKbps } = options;
    if (existing) {
        const at = existing.line.offset + existing.line.text.length;
        return { sdp: sdp.slice(0, at) + `;x-google-start-bitrate=${bitrateKbps}` + sdp.slice(at), changed: true };
    }

    // Insert beside the selected mapping without normalizing any original byte.
    const line = selected.line, at = line.offset + line.text.length + line.eol.length;
    const attribute = `a=fmtp:${payloadType} x-google-start-bitrate=${bitrateKbps}`;
    const insertion = line.eol ? attribute + line.eol : (target.lines[0].eol || "\r\n") + attribute;
    return { sdp: sdp.slice(0, at) + insertion + sdp.slice(at), changed: true };
}

// Preserve byte-for-byte SDP outside the exact, validated owned parameter.
export function withoutOwnedScreenStartupHint(description, owner, browserEcho = false) {
    const parsed = inspectScreenStartupCodec(description, owner, owner.bitrateKbps, true);
    if (!parsed || !parsed.hint || (browserEcho ? parsed.selected.codec !== owner.codec :
        ![owner.identity, owner.localIdentity].includes(parsed.identity))) return description;
    return { ...description, sdp: description.sdp.slice(0, parsed.hint.start) + description.sdp.slice(parsed.hint.end) };
}

export function carryScreenStartupHint(description, owner) {
    const parsed = inspectScreenStartupCodec(description, owner, owner.bitrateKbps);
    if (!parsed || parsed.primaryPayloadType !== owner.payloadType || parsed.identity !== owner.identity || parsed.security !== owner.security) return null;
    if (parsed.hint) return description;
    const hinted = withScreenStartupBitrateHint({ ...description, type: "answer" }, owner);
    return hinted.changed ? { ...description, sdp: hinted.sdp } : null;
}
