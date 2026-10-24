// connection.js — one control connection over WebSocket. It sends frames,
// matches replies to requests by message type (the server answers requests
// of one type in order), and dispatches unsolicited frames to listeners.
import { encodeFrame, FrameReader, Msg } from "./frame.js";

export class ProtocolError extends Error {
    constructor(error) {
        super(error?.message || "request failed");
        this.code = error?.code;
        this.retryAfterMS = error?.retry_after_ms;
        this.devicesStale = !!error?.recipient_devices_stale;
    }
}

export class Connection extends EventTarget {
    #socket = null;
    #reader = new FrameReader();
    #pending = [];
    #closed = false;
    #probe = null;
    /** Control bytes received and sent on this connection (server info). */
    bytesIn = 0;
    bytesOut = 0;

    /** Opens ws(s)://<host>/ws for the page's own origin unless url is given. */
    static async open(url = defaultURL(), WebSocketImpl = globalThis.WebSocket) {
        const connection = new Connection();
        await connection.#connect(url, WebSocketImpl);
        return connection;
    }

    #connect(url, WebSocketImpl) {
        return new Promise((resolve, reject) => {
            const socket = new WebSocketImpl(url);
            socket.binaryType = "arraybuffer";
            this.#socket = socket;
            socket.onopen = () => resolve();
            socket.onerror = () => reject(new Error("could not reach the server"));
            socket.onclose = () => this.#shutdown(new Error("connection closed"));
            socket.onmessage = event => this.#receive(event.data);
        });
    }

    get open() {
        return !this.#closed && this.#socket?.readyState === 1;
    }

    send(type, message) {
        if (!this.open) throw new Error("not connected");
        const frame = encodeFrame(type, message);
        this.bytesOut += frame.length;
        this.#socket.send(frame);
    }

    /**
     * Sends a request and resolves with the next frame of replyType. An Error
     * frame whose origin_type is the request type rejects it instead.
     */
    request(type, message, replyType, { timeout = 10000, accept = () => true } = {}) {
        return new Promise((resolve, reject) => {
            const pending = { type, replyType, accept, resolve, reject };
            pending.timer = setTimeout(() => {
                this.#pending = this.#pending.filter(p => p !== pending);
                reject(new Error("the server did not answer in time"));
            }, timeout);
            this.#pending.push(pending);
            try {
                this.send(type, message);
            } catch (error) {
                clearTimeout(pending.timer);
                this.#pending = this.#pending.filter(p => p !== pending);
                reject(error);
            }
        });
    }

    /** Check a possibly stale socket after the browser resumes from suspension. */
    probe() {
        if (!this.#probe) {
            this.#probe = this.request(Msg.Ping, {}, Msg.Pong, { timeout: 5000 })
                .catch(error => { this.close(); throw error; })
                .finally(() => { this.#probe = null; });
        }
        return this.#probe;
    }

    close() {
        this.#shutdown(new Error("connection closed"));
        this.#socket?.close();
    }

    #receive(data) {
        let frames;
        this.bytesIn += data?.byteLength || 0;
        try {
            frames = this.#reader.push(new Uint8Array(data));
        } catch (error) {
            this.close();
            return;
        }
        for (const frame of frames) this.#dispatch(frame);
    }

    #dispatch({ type, payload }) {
        if (type === Msg.Ping) {
            if (this.open) this.send(Msg.Pong, {});
            return;
        }
        if (type === Msg.Error) {
            const index = this.#pending.findIndex(p => p.type === payload.origin_type);
            if (index >= 0) {
                const [pending] = this.#pending.splice(index, 1);
                clearTimeout(pending.timer);
                pending.reject(new ProtocolError(payload));
                return;
            }
        }
        const index = this.#pending.findIndex(p => (Array.isArray(p.replyType) ? p.replyType.includes(type) : p.replyType === type) && p.accept(payload));
        if (index >= 0) {
            const [pending] = this.#pending.splice(index, 1);
            clearTimeout(pending.timer);
            pending.resolve(payload);
            return;
        }
        this.dispatchEvent(new CustomEvent(String(type), { detail: payload }));
    }

    #shutdown(error) {
        const wasOpen = !this.#closed || this.#pending.length;
        this.#closed = true;
        for (const pending of this.#pending.splice(0)) {
            clearTimeout(pending.timer);
            pending.reject(error);
        }
        if (wasOpen) this.dispatchEvent(new CustomEvent("close"));
    }

    /** Subscribes to unsolicited frames of one type; returns an unsubscribe function. */
    on(type, listener) {
        const handler = event => listener(event.detail);
        this.addEventListener(String(type), handler);
        return () => this.removeEventListener(String(type), handler);
    }
}

export function defaultURL() {
    const { protocol, host } = globalThis.location;
    return `${protocol === "https:" ? "wss:" : "ws:"}//${host}/ws`;
}
