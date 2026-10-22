import { t } from "./i18n.js";

export function createChatUploadQueue({ app, currentTab, container }) {
    let serial = 0;
    const jobs = new Map();
    const render = job => {
        if (!job.row.isConnected) return;
        job.row.querySelector(".upload-state").textContent = t("usability.upload" + job.state);
        const progress = job.row.querySelector("progress");
        progress.hidden = !["Queued", "Active", "Sending"].includes(job.state);
        if (job.total > 0) { progress.max = job.total; progress.value = job.transferred || 0; } else progress.removeAttribute("value");
        const button = job.row.querySelector("button");
        const retry = ["Failed", "Canceled"].includes(job.state);
        button.textContent = retry ? "↻" : "×";
        button.title = t("usability.upload" + (retry ? "Retry" : job.state === "Done" ? "Dismiss" : "Cancel"), { name: job.file.name });
        button.setAttribute("aria-label", button.title);
        button.disabled = job.state === "Sending";
        button.onclick = () => {
            if (job.state === "Done") { job.row.remove(); jobs.delete(job.id); container.hidden = !jobs.size; return; }
            if (retry) { job.retry?.(); return; }
            job.canceled = true; job.state = "Canceled"; render(job);
            if (job.started && typeof app().CancelTransferForTab === "function") void Promise.resolve(app().CancelTransferForTab(job.tabID, job.id)).catch(() => {});
        };
    };
    return {
        add(files, tabID, retry) {
            return files.map(file => {
                const id = "chat-" + Date.now().toString(36) + "-" + ++serial;
                const row = document.createElement("div"); row.className = "chat-upload-item";
                row.innerHTML = '<span class="upload-name"></span><span class="upload-state"></span><button type="button"></button><progress></progress>';
                row.querySelector(".upload-name").textContent = file.name; row.querySelector(".upload-name").title = file.name;
                row.querySelector("progress").setAttribute("aria-label", file.name);
                if (jobs.size >= 50) { const previous = [...jobs.values()].find(entry => ["Done", "Failed", "Canceled"].includes(entry.state)); if (previous) { previous.row.remove(); jobs.delete(previous.id); } }
                container.append(row); container.hidden = false;
                const job = { id, row, file, tabID, retry: () => retry(file, job), state: "Queued", canceled: false };
                jobs.set(id, job); render(job); return job;
            });
        },
        async upload(job, channelID, isCurrent) {
            if (!isCurrent()) return null;
            job.canceled = false; job.cancelAcknowledged = false;
            if (job.token) { job.state = "Sending"; render(job); return job.token; }
            job.state = "Active"; job.started = true; render(job);
            try {
                const method = app().UploadChatAttachmentProgressForTab;
                const token = typeof method === "function"
                    ? await method(job.tabID, job.id, channelID, job.file.name, job.file.dataBase64)
                    : await app().UploadChatAttachmentForTab(job.tabID, channelID, job.file.name, job.file.dataBase64);
                if (job.canceled || !isCurrent()) return null;
                if (typeof token !== "string" || !token.startsWith("[file:")) throw new Error("Invalid attachment response");
                job.token = token; job.state = "Sending"; render(job); return token;
            } catch (error) { if (!job.canceled && isCurrent()) { job.state = "Failed"; render(job); } throw error; }
        },
        finish(job, error) { if (!job.canceled) { job.state = error ? "Failed" : "Done"; if (!error) { job.file = { name: job.file.name }; job.token = ""; job.retry = null; } render(job); } },
        remove(job) { job.row.remove(); jobs.delete(job.id); container.hidden = !jobs.size; },
        reset() { for (const job of jobs.values()) { job.canceled = true; if (job.started && ["Active", "Queued"].includes(job.state)) void Promise.resolve(app().CancelTransferForTab(job.tabID, job.id)).catch(() => {}); } jobs.clear(); container.replaceChildren(); container.hidden = true; },
        progress(event) {
            const job = jobs.get(event?.id);
            if (!job || job.tabID !== currentTab() || (event.tab_id && event.tab_id !== job.tabID)) return;
            if (job.canceled) { if (!job.cancelAcknowledged && event.status === "active") { job.cancelAcknowledged = true; void Promise.resolve(app().CancelTransferForTab(job.tabID, job.id)).catch(() => {}); } return; }
            job.total = Number(event.total) || 0; job.transferred = Number(event.transferred) || 0; render(job);
        },
    };
}
