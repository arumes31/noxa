import { t } from "./i18n.js";
import { closeDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";

const el = (tag,text="") => { const node=document.createElement(tag);node.textContent=text;return node; };
export function openWebhooks(channelID) {
    const state=window.__noxa.state,tabID=state.activeTabID,generation=state.serverGeneration;
    if (!window.go?.main?.App?.WebhookForTab) { window.__noxa.toast(t("webhook.unavailable"),"warn");return; }
    const overlay=el("div");overlay.className="dlg-overlay";
    const dialog=el("section");dialog.className="dlg discussion-dialog";overlay.append(dialog);
    const heading=el("h2",t("webhook.title")),description=el("p",t("webhook.description")),content=el("div"),secret=el("div"),status=el("p");status.setAttribute("role","status");
    const makeButton=(key,handler)=>{const button=el("button",t(key));button.type="button";button.onclick=handler;return button;};
    const close=makeButton("webhook.close",()=>closeDialog(overlay,"close"));
    const form=el("form"),label=el("label",t("webhook.name")),name=el("input");name.required=true;name.maxLength=60;label.append(name);
    const create=makeButton("webhook.create",null);create.type="submit";form.append(label,create);
    dialog.append(heading,description,content,secret,form,status,close);
    let busy=false;
    const current=()=>isCurrentServerDialog(overlay)&&state.activeTabID===tabID&&state.serverGeneration===generation;
    const request=async(action,extra={})=>{
        if(busy||!current())return;busy=true;status.textContent="";
        for(const control of dialog.querySelectorAll("button,input"))control.disabled=true;
        try{
            const result=await window.go.main.App.WebhookForTab(tabID,{action,channel_id:channelID,...extra});
            if(!current())return;
            if(result.action!==action||result.channel_id!==channelID)throw new Error(t("webhook.unavailable"));
            content.replaceChildren();
            for(const hook of result.hooks||[]){const row=el("div");row.className="discussion-toolbar";row.append(el("strong",hook.name),makeButton("webhook.revoke",()=>request("revoke",{id:hook.id})));content.append(row);}
            if(!result.hooks?.length)content.append(el("p",t("webhook.empty")));
            if(action==="create"){
                secret.replaceChildren(el("p",t("webhook.secret")));
                const endpoint=el("label",t("webhook.endpoint")),path=el("input");path.readOnly=true;path.value=`/hooks/${result.id}`;endpoint.append(path);
                const tokenLabel=el("label",t("webhook.token")),token=el("input");token.readOnly=true;token.value=result.token;token.autocomplete="off";token.spellcheck=false;tokenLabel.append(token);
                secret.append(endpoint,tokenLabel,makeButton("webhook.copy",async()=>{try{await navigator.clipboard.writeText(token.value);if(current())status.textContent=t("webhook.copied");}catch{token.focus();token.select();}}),el("p",t("webhook.help",{port:result.health_port})));name.value="";
            }else if(action==="revoke")secret.replaceChildren();
        }catch(error){if(current())status.textContent=t("webhook.error",{error:String(error)});}
        finally{busy=false;if(current())for(const control of dialog.querySelectorAll("button,input"))control.disabled=false;}
    };
    form.onsubmit=event=>{event.preventDefault();void request("create",{name:name.value.trim()});};
    mountServerDialog(overlay,{onClose:()=>secret.replaceChildren()});void request("list");
}
