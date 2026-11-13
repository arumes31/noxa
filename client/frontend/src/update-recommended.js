// When the server recommends a newer client, update in the background and
// restart into it as soon as it is verified, even during a call: the restart
// restores the session. Respects the saved auto-install opt-out.
export function createRecommendedUpdate({ app, settings, busy, notify, download, restart, unstage }) {
    let running = false;
    return async function recommendedUpdateCheck(tabID) {
        const saved = settings();
        if (!tabID || running || busy() || !saved || saved.updates_auto_install === false) return false;
        running = true;
        try {
            // Empty unless this build is an older release (never for dev builds).
            const session = await app().SessionInfoForTab(tabID);
            if (!session?.connected || !session.update_recommended) return false;
            const info = await app().CheckForUpdate();
            if (!info?.available || busy() || settings()?.updates_auto_install === false) return false;
            notify("updater.softUpdating", { version: info.version });
            const error = await download(info);
            if (error) {
                notify("updater.softUpdateFailed", { error });
                return false;
            }
            // Turned off during the download: keep the staged update for a manual restart.
            if (settings()?.updates_auto_install === false) {
                notify("updater.softUpdateReady", { version: info.version });
                return false;
            }
            try {
                return await restart();
            } catch (error) {
                notify("updater.restartFailed", { error });
                if (await app().GetUpdatePhase?.() === "restored") unstage();
                return false;
            }
        } catch {
            return false; // offline or no update source: try again on the next connect
        } finally {
            running = false;
        }
    };
}
