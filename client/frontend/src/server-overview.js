import { t } from "./i18n.js";
import { formatBytes, measured } from "./connection-stats.js";

const node = (tag, cls, text) => { const el = document.createElement(tag); el.className = cls; if (text !== undefined) el.textContent = text; return el; };
// Updates existing fields rather than replacing the section on each refresh,
// keeping focus and the scroll position stable.
export function createServerOverview() {
 const root = node("section", "server-overview");
 root.setAttribute("aria-label", t("overview.title"));
 const heading = node("h4", "", t("overview.title")), unavailable = node("p", "server-info-note", t("overview.unavailable"));
 const content = node("div", ""), quality = node("div", "server-info-quality");
 const fields = {};
 for (const key of ["latency","loss","jitter"]) { const item = node("div", ""), label = node("span", "", t("overview."+key)); fields[key] = node("strong", "", "—"); fields[key].dataset.overviewStat=key; item.append(label, fields[key]); quality.append(item); }
 const samples = node("p", "server-info-note");
 const table = node("table", "server-info-traffic"), caption = node("caption", "", t("overview.traffic"));
 const head = node("thead", ""), headRow = node("tr", "");
 for (const key of ["desktop.transferred", "desktop.in", "desktop.out"]) { const cell=node("th","",t(key));cell.scope="col";headRow.append(cell); }
 head.append(headRow);table.append(caption,head);
 const body = node("tbody", "");
 for (const key of ["start","days7","days30","all"]) { const row=node("tr",""),title=node("th","",t("overview."+key));row.dataset.overviewWindow=key;title.scope="row";const incoming=node("td","","—"),outgoing=node("td","","—");fields[key]=[incoming,outgoing];row.append(title,incoming,outgoing);body.append(row); }
 table.append(body);
 const storage = node("p", "server-info-note", t("overview.storage")), coverage = node("p", "server-info-note"), warning = node("p", "server-overview-warning", t("overview.delayed"));
 warning.setAttribute("role","status");
 const details = node("details","server-info-note"), summary = node("summary","",t("overview.method"));
 details.append(summary,node("p","",t("overview.help")),node("p","",t("overview.direction")));
 content.append(quality,samples,table,storage,coverage,warning,details);
 root.append(heading,unavailable,content);
 return { element:root, update(value) {
  unavailable.hidden=!!value;content.hidden=!value;if(!value)return;
  fields.latency.textContent=measured(value.latency_ms)?value.latency_ms.toFixed(0)+" ms":"—";
  fields.loss.textContent=measured(value.loss_percent)?value.loss_percent.toFixed(2)+" %":"—";
  fields.jitter.textContent=measured(value.jitter_ms)?value.jitter_ms.toFixed(1)+" ms":"—";
  samples.textContent=t("overview.samples",{latency:value.latency_samples||0,clients:value.clients||0,loss:value.loss_samples||0,jitter:value.jitter_samples||0});
  const traffic=value.traffic;
  const quantities={start:traffic?.since_start,days7:traffic?.days_7,days30:traffic?.days_30,all:traffic?.all_time};
  for(const [key,amount] of Object.entries(quantities)){fields[key][0].textContent=formatBytes(amount?.in_bytes);fields[key][1].textContent=formatBytes(amount?.out_bytes);}
  coverage.textContent=traffic?.recording_since?t("overview.coverage",{date:new Date(traffic.recording_since*1000).toLocaleString(),saved:traffic.persisted_at?new Date(traffic.persisted_at*1000).toLocaleString():t("overview.never")}):"";
  warning.hidden=!traffic||traffic.storage_ok===true;
 }};
}
