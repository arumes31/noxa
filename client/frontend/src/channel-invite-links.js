const tokenPattern = /^[A-Za-z0-9_-]{43}$/;
export function parseChannelInvite(value) {
    try {
        const url = new URL(value);
        const match = url.pathname.match(/^\/invite\/([1-9][0-9]*)$/);
        const channelID = Number(match?.[1]);
        if (url.protocol !== "noxa:" || !url.hostname || url.username || url.password || url.search || !match || !Number.isSafeInteger(channelID) || !tokenPattern.test(url.hash.slice(1))) return null;
        return { address: url.host, channelID, token: url.hash.slice(1) };
    } catch { return null; }
}
export function buildChannelInvite(address, channelID, token) {
    try {
        const url = new URL(address.includes("://") ? address : "noxa://" + address);
        if (!["noxa:", "tls:", "tcp:"].includes(url.protocol) || !url.hostname || url.username || url.password || url.search || url.hash || (url.pathname && url.pathname !== "/") || !Number.isSafeInteger(channelID) || channelID < 1 || !tokenPattern.test(token)) return null;
        return "noxa://" + url.host + "/invite/" + channelID + "#" + token;
    } catch { return null; }
}
