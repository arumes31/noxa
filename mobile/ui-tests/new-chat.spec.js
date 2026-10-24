import {test,expect} from "@playwright/test";
test("Home starts a new DM by member name without exposing ID entry",async({page})=>{
 await page.goto("http://127.0.0.1:5175/app/ui-tests/layout.html?view=home");
 await expect(page.getByRole("link",{name:"New chat",exact:true})).toHaveAttribute("href","#/new-chat");
 await page.evaluate(async()=>{
  const {app}=await import("/app/src/app.js");
  app.session.clients=()=>[
   {unique_id:"self",nickname:"Daniel"},
   {unique_id:"peer",nickname:"Avery#M1",on_mobile:true},
   {unique_id:"peer",nickname:"Avery"},
   {unique_id:"guest:x",nickname:"Guest"},
   {unique_id:"key-guest",nickname:"Guest key",is_guest:true}];
  const {renderNewChat}=await import("/app/src/ui/new-chat.js");renderNewChat(document.getElementById("app"));
 });
 const search=page.getByRole("searchbox",{name:"Search members by name"});
 await expect(page.getByRole("link",{name:"Avery",exact:true})).toHaveCount(1);
 await expect(page.locator('a[href^="#/dm/"]')).toHaveCount(1);
 await search.fill("PEER");await expect(page.getByText("No members found.")).toBeVisible();
 await search.fill("aVe");await page.getByRole("link",{name:"Avery",exact:true}).click();
 await expect(page).toHaveURL(/#\/dm\/peer$/);
});
