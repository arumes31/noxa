// Test-only WebCrypto barrier. seal() can capture the callee before an awaited
// wrapping-key argument, so restoring the property alone cannot release it.
export function pauseCrypto(subtle, method, onPaused = () => {}) {
    const original = subtle[method].bind(subtle);
    let open, released = false;
    const gate = new Promise(resolve => { open = resolve; });
    const held = async (...args) => {
        onPaused();
        await gate;
        return original(...args);
    };
    subtle[method] = held;
    return () => {
        if (released) return;
        released = true;
        if (subtle[method] === held) subtle[method] = original;
        open();
    };
}
