import { expect, test } from './fixtures.js';

test.beforeEach(async ({page})=>{
 await page.setViewportSize({width:1024,height:768});
 await page.addInitScript(()=>{
  window.__events={};
  window.runtime={EventsOn:(name,fn)=>{(window.__events[name]||=[]).push(fn);return()=>{};},EventsEmit(){},WindowIsFullscreen:async()=>false};
  window.go={main:{App:new Proxy({},{get(_target,method){return async()=>{
   if(method==='GetSettings')return {language:'en',activation_mode:'ptt',onboarding_done:true,chat_max_lines:200,window_opacity:100,updates_auto_check:false};
   if(['ListTabs','GetPermissions','SubscriptionsForTab','DMHistoryLoadForContext'].includes(method))return [];
   if(['IdentityInfo','GetAvatar'].includes(method))return {};
   if(method==='Connected'||method==='IsGuest')return false;
   if(method==='ClientVersion'||method==='ClientVersionShort')return '0.6.34';
   return '';
  };}})}};
 });
 await page.goto('/');
 await page.waitForFunction(()=>!!window.__noxa?.openSettings);
 await page.evaluate(()=>{
  const v=window.__noxa;Object.assign(v.state,{activeTabID:'tooltip-test',myClientID:'self',myNickname:'Audit',myChannelID:15});v.showWorkspace(false);
  const original=window.go.main.App;
  window.go.main.App=new Proxy(original,{get(target,method){
   if(method==='SessionInfoForTab')return async()=>({connected:true,security:'TLS (fingerprint AA:BB:CC)'});
   if(method==='E2EEDiagnosticsForTab')return async()=>({active:true,protocol:'noxa-olm-v1',cipher:'Olm <img src=x onerror="window.__injected=true">'});
   if(method==='MediaE2EEStatusForTab')return async()=>({supported:true});
   return target[method];
  }});
  document.querySelector('#conn-lock').classList.remove('hidden');
 });
});

test('top lock opens details on focus and hover, fits viewport and escapes markup',async({page})=>{
 const lock=page.locator('#conn-lock'),tip=page.locator('#conn-lock-details');
 await lock.focus();await expect(tip).toBeVisible();await expect(tip).toContainText('You ↔ server: encrypted');
 await expect(tip).toContainText('User ↔ user: E2EE');
 await expect(tip).not.toContainText('AA:BB:CC');await expect(tip).not.toContainText('noxa-olm-v1');
 await expect(tip).toContainText('Channel chat & private calls: no E2EE');
 await expect(tip.locator('.encryption-tooltip-footer')).toContainText('only the participating devices');
 await expect(tip.locator('img')).toHaveCount(0);
 expect(await tip.evaluate(e=>{const r=e.getBoundingClientRect();return r.left>=0&&r.right<=innerWidth&&r.top>=0&&r.bottom<=innerHeight;})).toBe(true);
 expect(await tip.evaluate(e=>e.scrollHeight<=e.clientHeight)).toBe(true);
 expect(await tip.evaluate(e=>e.getBoundingClientRect().height)).toBeLessThan(230);
 await page.screenshot({path:'../../.tmp/audit-logs/compact-encryption-desktop.png'});
 await page.keyboard.press('Escape');await expect(tip).toBeHidden();
 await lock.hover();await expect(tip).toBeVisible();await tip.hover();await expect(tip).toBeVisible();
 await page.evaluate(()=>{for(const cb of window.__events.tab_reset||[])cb('');});await expect(tip).toBeHidden();
});

test('long encryption status remains a small lock inside narrow voice card',async({page})=>{
 await page.evaluate(async()=>{
  const {renderVoiceEncryptionStatus}=await import('/src/main.js');
  renderVoiceEncryptionStatus({state:'encrypted',recipients:1,keyHolders:[{principal:'A very long account identity '.repeat(100)}]});
  document.querySelector('#sidebar').style.width='220px';
 });
 const lock=page.locator('#voice-e2ee');await expect(lock.locator('svg')).toBeVisible();
 await expect(lock).toHaveAttribute('data-state','encrypted');
 await expect(lock).toHaveAttribute('aria-label','E2EE');
 expect(await lock.evaluate(e=>e.getBoundingClientRect().width)).toBeLessThanOrEqual(24);
 expect(await lock.evaluate(e=>{const r=e.getBoundingClientRect(),c=document.querySelector('#voice-bar').getBoundingClientRect();return r.left>=c.left&&r.right<=c.right;})).toBe(true);
 await lock.focus();await expect(page.locator('#voice-e2ee-details .encryption-tooltip-footer')).toContainText('only the participating devices');
 await expect(page.locator('#voice-e2ee-details')).not.toContainText('A very long account identity');
 await page.evaluate(async()=>{(await import('/src/main.js')).renderVoiceEncryptionStatus({state:'off'});});
 await expect(lock).toBeHidden();
});
