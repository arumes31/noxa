import { expect,test } from "./fixtures.js";

async function open(page){
    await page.route("**/__webhook_test__",route=>route.fulfill({contentType:"text/html",body:'<!doctype html><title>Hooks</title><link rel="stylesheet" href="/src/style.css">'}));
    await page.goto("/__webhook_test__");
    await page.evaluate(async()=>{
        window.__requests=[];window.__hooks=[];
        window.__noxa={state:{activeTabID:"a",serverGeneration:1},toast(){}};
        window.go={main:{App:{WebhookForTab:async(tabID,r)=>{
            window.__requests.push({tabID,...r});
            if(window.__hold)return new Promise(resolve=>{window.__resolve=resolve;});
            if(window.__failure)throw Error(window.__failure);
            if(r.action==="create")window.__hooks.push({id:21,name:r.name,channel_id:r.channel_id});
            if(r.action==="revoke")window.__hooks=window.__hooks.filter(h=>h.id!==r.id);
            return {action:r.action,channel_id:r.channel_id,hooks:window.__hooks,id:21,token:r.action==="create"?"one-time-secret":"",health_port:12337};
        }}}};
        window.__webhooks=await import("/src/webhooks.js");window.__webhooks.openWebhooks(7);
    });
    await expect(page.getByText("No webhooks in this channel.")).toBeVisible();
}

test("webhook management shows secret once and revokes only the selected channel hook",async({page})=>{
    await open(page);
    await page.getByLabel("Integration name").fill("Build results");
    await page.getByRole("button",{name:"Create webhook",exact:true}).click();
    await expect(page.getByLabel("Bearer token")).toHaveValue("one-time-secret");
    await expect(page.getByLabel("Endpoint path")).toHaveValue("/hooks/21");
    await expect(page.getByText(/internal HTTP listener uses port 12337/)).toBeVisible();
    await page.getByRole("button",{name:"Close",exact:true}).click();
    await page.evaluate(()=>window.__webhooks.openWebhooks(7));
    await expect(page.getByText("Build results",{exact:true})).toBeVisible();
    await expect(page.getByLabel("Bearer token")).toHaveCount(0);
    await page.getByRole("button",{name:"Revoke",exact:true}).click();
    await expect(page.getByText("No webhooks in this channel.")).toBeVisible();
    expect(await page.evaluate(()=>window.__requests.every(r=>r.tabID==="a"&&r.channel_id===7))).toBe(true);
});

test("webhook creation ignores secret returned after changing servers",async({page})=>{
    await open(page);await page.getByLabel("Integration name").fill("Late result");
    await page.evaluate(()=>{window.__hold=true;});
    await page.getByRole("button",{name:"Create webhook",exact:true}).click();
    await page.evaluate(()=>{window.__noxa.state.activeTabID="b";window.__noxa.state.serverGeneration++;window.__resolve({action:"create",channel_id:7,hooks:[],id:21,token:"old-server-secret"});});
    await expect(page.getByLabel("Bearer token")).toHaveCount(0);
    await expect(page.getByText("old-server-secret")).toHaveCount(0);
});

test("webhook permission failure keeps the draft and fits a narrow window",async({page})=>{
    await page.setViewportSize({width:430,height:760});await open(page);
    await page.getByLabel("Integration name").fill("Preserved draft");
    await page.evaluate(()=>{window.__failure="Permission denied";});
    await page.getByRole("button",{name:"Create webhook",exact:true}).click();
    await expect(page.getByRole("status")).toContainText("Permission denied");
    await expect(page.getByLabel("Integration name")).toHaveValue("Preserved draft");
    expect(await page.locator(".dlg").evaluate(node=>node.scrollWidth<=node.clientWidth)).toBe(true);
});
