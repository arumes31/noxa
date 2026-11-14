// update-policy.js — when an installed app update is applied.
// Outside voice it reloads at once; in voice it waits until the user leaves,
// so a call is never cut off by an optional update.

/**
 * @param {{ inVoice: () => boolean, reload: () => void }} hooks
 */
export function updatePolicy({ inVoice, reload }) {
    let waiting = false;
    const check = () => {
        if (!waiting || inVoice()) return false;
        waiting = false;
        reload();
        return true;
    };
    return {
        /** A new version is installed; true when it is applied right away. */
        ready() { waiting = true; return check(); },
        /** Voice or call state changed; applies a waiting update once free. */
        check,
    };
}
