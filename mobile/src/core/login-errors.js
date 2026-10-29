// login-errors.js — sorts sign-in failures into the classes the desktop
// explains (client/frontend/src/login-errors.js). Only known server reasons
// are matched; anything else stays "unknown" and shows the server's text.

/** Returns {kind, ...details} for an error or reason string. */
export function classifySignIn(error) {
    const text = String(error?.message ?? error ?? "").trim();
    const reason = text.toLowerCase();
    const minimum = /^client version rejected: installed (.{1,100}); required >=([0-9A-Za-z.+-]{1,100}); update noxa to connect$/i.exec(text);
    if (minimum) return { kind: "minimumVersion", installed: minimum[1], required: minimum[2] };
    if (reason === "invalid server password") return { kind: "serverPassword" };
    if (reason === "banned" || reason.startsWith("banned: ")) return { kind: "banned", detail: text.slice(8) };
    if (reason === "too many failed logins, try again later") return { kind: "rateLimit" };
    if (/signed out|link it again/.test(reason)) return { kind: "revoked" };
    if (reason === "server is full") return { kind: "full" };
    if (reason === "guest login is disabled on this server") return { kind: "guestDisabled" };
    if (reason.startsWith("unsupported authorization model") || reason.startsWith("unsupported server authorization model")) return { kind: "version" };
    if (reason.startsWith("this server needs an update: it does not support secure sign-in")) return { kind: "serverOutdated" };
    if (reason === "could not reach the server" || reason === "connection closed" || reason === "the server did not answer in time" || reason === "not connected") return { kind: "network" };
    if (reason === "invalid credentials" || reason === "invalid public key" || reason === "no pending challenge") return { kind: "authentication" };
    return { kind: "unknown" };
}

/** An Error carrying the server's reason and its class. */
export function signInError(reason) {
    const error = new Error(reason || "sign-in failed");
    error.kind = classifySignIn(error).kind;
    return error;
}
