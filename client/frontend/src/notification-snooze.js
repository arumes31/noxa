import { t, currentLanguage } from "./i18n.js";

// Keep this separate from permanent DND and quiet hours. Expiry is checked
// against wall time by the dispatcher, including after sleep or restart.
export function mountSnoozeControls(container) {
    const panel = document.createElement("div");
    panel.className = "notification-snooze";
    const status = document.createElement("p");
    status.setAttribute("role", "status");
    const error = document.createElement("p");
    error.setAttribute("role", "alert");
    error.hidden = true;
    const buttons = [30, 60, 0].map(minutes => {
        const button = document.createElement("button");
        button.type = "button";
        button.dataset.minutes = String(minutes);
        button.onclick = async () => {
            buttons.forEach(item => { item.disabled = true; });
            error.hidden = true;
            try {
                const until = await window.go.main.App.SetNotificationSnooze(minutes);
                window.__noxa.state.settings.notification_snooze_until = until;
                window.__noxa.renderTree();
            } catch {
                error.textContent = t("wins.snoozeFailed");
                error.hidden = false;
            } finally {
                buttons.forEach(item => { item.disabled = false; });
                render();
            }
        };
        return button;
    });
    const render = () => {
        const until = window.__noxa.state.settings?.notification_snooze_until;
        const active = Number.isFinite(until) && until > Date.now();
        const text = active ? t("wins.snoozeUntil", { time: new Date(until).toLocaleTimeString(currentLanguage(), { hour: "2-digit", minute: "2-digit" }) }) : t("wins.snoozeOff");
        if (status.textContent !== text) status.textContent = text;
        buttons[0].textContent = t("wins.snooze30");
        buttons[1].textContent = t("wins.snooze60");
        buttons[2].textContent = t("wins.snoozeCancel");
        buttons[2].hidden = !active;
    };
    panel.append(status, ...buttons, error);
    container.append(panel);
    render();
    const timer = setInterval(render, 1000);
    window.addEventListener("noxa-language-changed", render);
    return () => { clearInterval(timer); window.removeEventListener("noxa-language-changed", render); };
}
