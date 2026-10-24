import {test,expect} from "@playwright/test";
const base="http://127.0.0.1:5175/app/ui-tests/layout.html";
for(const [label,route] of [["New message","new-chat"],["Create group chat","new-group"]]) test(`Home plus opens ${label}`,async({page})=>{
 await page.goto(base+"?view=home");
 await page.getByRole("button",{name:"New message or group",exact:true}).click();
 await page.getByRole("dialog").getByRole("button",{name:label,exact:true}).click();
 await expect(page).toHaveURL(new RegExp("#/"+route+"$"));
});
for(const fail of [false,true])test(`Server voice join handles failure=${fail}`,async({page})=>{
 await page.goto(base+"?view=servers#/servers");
 await page.evaluate(async fail=>{
  const {app}=await import("/app/src/app.js");
  window.joined=[];app.voice.join=async id=>{window.joined.push(id);if(fail)throw new Error("Microphone denied");app.voice.active=true;app.voice.channelID=id};
 },fail);
 await expect(page.getByRole("button",{name:"Join Lobby",exact:true})).toHaveCount(0);
 await page.getByRole("button",{name:"Join Public1",exact:true}).click();
 expect(await page.evaluate(()=>window.joined)).toEqual([1]);
 await expect(page).toHaveURL(fail?/#\/servers$/:/#\/voice$/);
 if(fail){await expect(page.locator("#toasts")).toContainText("Microphone denied");await expect(page.getByRole("button",{name:"Join Public1",exact:true})).toBeEnabled();}
});
test("Pending join cannot join twice or hijack another tab",async({page})=>{
 await page.goto(base+"?view=servers#/servers");
 await page.evaluate(async()=>{const {app}=await import("/app/src/app.js");window.calls=0;app.voice.join=()=>{window.calls++;return new Promise(r=>{window.finish=r})};});
 await page.getByRole("button",{name:"Join Public1",exact:true}).click();
 await page.getByRole("button",{name:/Join A very/}).click();
 expect(await page.evaluate(()=>window.calls)).toBe(1);
 await page.evaluate(()=>{location.hash="#/home";window.finish()});
 await expect(page).toHaveURL(/#\/home$/);
});
test("Create group invites selected member and retry does not create duplicate group",async({page})=>{
 await page.goto(base+"?view=new-group#/new-group");
 await page.evaluate(async()=>{const {app}=await import("/app/src/app.js");window.created=[];window.invited=[];app.groups.create=async name=>{window.created.push(name);return{id:"new-group"}};app.groups.invite=async(id,uid)=>{window.invited.push([id,uid]);if(window.invited.length===1)throw new Error("Try again")};});
 await expect(page.getByRole("link",{name:"Open group",exact:true})).toBeHidden();
 await page.getByRole("textbox",{name:"Group name",exact:true}).fill("Raid team");
 await page.getByRole("checkbox",{name:"Avery"}).check();
 await page.getByRole("button",{name:"Create group chat",exact:true}).click();
 await expect(page.getByRole("alert")).toContainText("Group created.");
 await expect(page.getByRole("link",{name:"Open group",exact:true})).toHaveAttribute("href","#/group/new-group");
 await page.getByRole("button",{name:"Retry invitations",exact:true}).click();
 expect(await page.evaluate(()=>window.created)).toEqual(["Raid team"]);
 expect(await page.evaluate(()=>window.invited)).toEqual([["new-group","peer"],["new-group","peer"]]);
 await expect(page).toHaveURL(/#\/group\/new-group$/);
});

test("Group member selection keeps keyboard focus and enforces 15 invitations",async({page})=>{
 await page.goto(base+"?view=home");
 await page.evaluate(async()=>{
  const {app}=await import("/app/src/app.js");
  app.session.clients=()=>Array.from({length:16},(_,i)=>({unique_id:`peer-${i}`,nickname:`Member ${String(i).padStart(2,"0")}`}));
  const {renderNewGroup}=await import("/app/src/ui/new-group.js");renderNewGroup(document.getElementById("app"));
 });
 const first=page.getByRole("checkbox",{name:"Member 00",exact:true});
 await first.focus();await page.keyboard.press("Space");await expect(first).toBeFocused();await expect(first).toBeChecked();
 for(let i=1;i<15;i++) await page.getByRole("checkbox",{name:`Member ${String(i).padStart(2,"0")}`,exact:true}).check();
 await expect(page.getByRole("checkbox",{name:"Member 15",exact:true})).toBeDisabled();
 await expect(page.getByRole("status")).toHaveText("15 / 15 selected");
 await first.uncheck();await expect(page.getByRole("checkbox",{name:"Member 15",exact:true})).toBeEnabled();
});
