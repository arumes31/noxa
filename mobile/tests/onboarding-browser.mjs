import assert from "node:assert/strict";
import { test } from "node:test";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromium } from "@playwright/test";

const root = path.resolve(fileURLToPath(new URL("../", import.meta.url)));
// Execute the real app and UI entry modules, with controlled network/auth/UI
// dependencies. Identity-store, db, settings and IndexedDB are not mocked.
const fixture = `
export const connections = [];
export function generateIdentity(marker=7) { return Object.fromEntries([['edPublic',32],['edSecret',64],['xPublic',32],['xSecret',32]].map(([k,n])=>[k,new Uint8Array(n).fill(marker)])); }
export class Connection extends EventTarget {
 static async open() { const c=new Connection();c.open=true;connections.push(c);return c; }
 close() {this.open=false;} async probe() {}
}
class Empty extends EventTarget { async refresh() {} async clear() {} close() {} }
export class ChatSession extends Empty {
 constructor(conn) {super();this.conn=conn;this.subscriptions=new Set();}
 activateIncoming() {} channels() {return [];} close() {this.conn.close();}
}
export class DirectMessages extends Empty {ready=Promise.resolve();async importHistory() {}}
export {Empty as Calls,Empty as Groups,Empty as Voice,Empty as SavedMessages};
export const LinkState={approved:'approved',claimed:'claimed',denied:'denied'};
const gate=async kind=>{if(globalThis.mode===kind){globalThis.entered=true;await globalThis.blocked;}};
export async function signIn(){await gate('guest');await gate('signin');if(globalThis.revoked)throw new Error('signed out, link it again');return {device_id:'phone',unique_id:'alice',nickname:'Alice'};}
export async function linkWithPasskey(){await gate('passkey');return {state:'approved'};}
export async function claimLink(){return {state:'claimed',sas:'42',linkID:'link',approverFingerprint:'desktop'};}
export async function waitForDecision(){await gate('link');return 'approved';}
export const passkeysSupported=()=>true;
export async function scanPairingQR(){await gate('scan');return {code:'12345678'};}
export class BackupError extends Error {}
export async function importBackup(){await gate('backup');return {identity:generateIdentity(),meta:{via:'backup'}};}
export const classifySignIn=()=>({kind:'unknown'});
export const pendingInvite=()=>null;
export const isForThisServer=()=>true;
export const parseInviteLink=()=>null;
export const resolveInvite=async()=>null;
export const setPendingInvite=()=>{};
export const webInviteURL=()=>'';
export const get=()=>[];
export const set=()=>{};
export const t=(key,vars)=>vars?.error || key;
export function h(tag,attrs={},...children){const n=document.createElement(tag);for(const[k,v]of Object.entries(attrs)){if(v===null||v===undefined)continue;if(k.startsWith('on')||k==='value'||k==='disabled'||k==='checked')n[k]=v;else n.setAttribute(k,v);}for(const c of children.flat(Infinity))if(c!==null&&c!==undefined)n.append(c);return n;}
export const icon=()=>h('span');
export const toast=message=>{globalThis.message=message;};
export const sheet=async()=>true;
export const notifier={install(){},decide(){return {count:true}},read(){}};
export const activityShared=()=>false,isMuted=()=>false,syncMuted=()=>{},watchCallLog=()=>{},resetCallLog=async()=>{};
export const enqueue=()=>{},flush=async()=>{},showPending=async()=>{},waitingCount=async()=>0;
export const announceRead=()=>{},closeNotifications=()=>{},reportVisibility=()=>{},setBadge=()=>{},syncPush=async()=>({});
export const addServer=()=>{};
// noxa-olm-v1 consumer imports of app.js (this fixture server advertises no capability, so none opens).
export const Msg={},signBundle=()=>({}),signReset=()=>({}),connectionTransport=()=>({}),verifyLinkBinding=()=>'',announcedBinding=()=>'',linkBindingFromStatus=()=>null,signKeyRecord=()=>'';
export class RatchetConsumer {}
`;

function dependencies(source, file) {
  return source.replace(/^import.*;$/gm, line => {
    const spec = line.match(/(?:from\s+)?["']([^"']+)["'];$/)?.[1];
    if (!spec) throw new Error(`unrecognized import: ${line}`);
    if (spec.endsWith('.css') || spec.endsWith('account-strings.js')) return '';
    if (spec.endsWith('identity-store.js')) return line.replace(spec, '/src/core/identity-store.js');
    if (spec === '../app.js') return line.replace(spec, '/src/app.js');
    if (file.endsWith('/connect.js') && spec === './account.js') return line.replace(spec, '/src/ui/account.js');
    return line.replace(spec, '/fixture.js');
  });
}

test('actual onboarding entries retain flow-start generation across other-tab logout/replacement', {timeout:60000}, async () => {
  const server = createServer(async (req,res) => {
    try {
      const pathname = new URL(req.url,'http://localhost').pathname;
      if (pathname==='/') return res.setHeader('Content-Type','text/html').end('<!doctype html><div id="root"></div>');
      res.setHeader('Content-Type','application/javascript');
      if (pathname==='/fixture.js') return res.end(fixture);
      const filename = path.resolve(root,'.'+pathname);
      if (!filename.startsWith(root+path.sep)) return res.writeHead(404).end();
      let source=await readFile(filename,'utf8');
      if (['/src/app.js','/src/ui/connect.js','/src/ui/account.js'].includes(pathname)) source=dependencies(source,pathname);
      res.end(source);
    } catch(error) {res.writeHead(500).end(String(error));}
  });
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  let browser;
  try {
    browser=await chromium.launch();
    for (const kind of ['passkey','link','scan','backup','guest','guest-save','guest-reuse','signin','revoked']) {
      for (const change of ['logout','replacement']) {
        const ctx=await browser.newContext();
        try {
          const page=await ctx.newPage(), other=await ctx.newPage();
          await Promise.all([page,other].map(p=>p.goto(`http://127.0.0.1:${server.address().port}`)));
          await page.evaluate(async()=>{
            globalThis.ids=await import('/src/core/identity-store.js');
            globalThis.db=await import('/src/core/db.js');
            globalThis.f=await import('/fixture.js');
            await ids.saveIdentity(f.generateIdentity(),{owner:'original',linked:true},{onboarding:true,fence:await ids.beginIdentity()});
            globalThis.api=await import('/src/app.js');
            globalThis.ui=await import('/src/ui/connect.js');
            await api.loadDevice();
            api.app.info={guest_login:true};
            globalThis.original=api.app.identity;
            globalThis.signedIn=0;
            globalThis.start=kind=>{
              globalThis.mode=kind==='revoked'?'signin':kind==='guest-reuse'?'guest':kind;
              globalThis.revoked=kind==='revoked';
              globalThis.entered=false;
              if(kind==='guest-save') {
                const encrypt=crypto.subtle.encrypt.bind(crypto.subtle);
                crypto.subtle.encrypt=async(...args)=>{
                  crypto.subtle.encrypt=encrypt;
                  globalThis.entered=true;
                  await globalThis.blocked;
                  return encrypt(...args);
                };
              }
              globalThis.blocked=new Promise(resolve=>{globalThis.release=resolve;});
              const root=document.querySelector('#root');
              const onSignedIn=()=>{signedIn++;};
              ui.renderConnect(root,{onSignedIn});
              let pending;
              if(kind==='link') pending=ui.runLink(root,{code:'12345678'},{onSignedIn});
              if(kind==='passkey') pending=root.querySelector('.connect-passkey').onclick();
              if(kind==='scan') pending=root.querySelector('.connect-link button[type=button]').onclick();
              if(kind==='backup') {
                Object.defineProperty(root.querySelector('#restore-file'),'files',{value:[{text:async()=>'{"backup":true}'}]});
                pending=root.querySelector('.acct-restore form').onsubmit({preventDefault(){}});
              }
              if(kind==='guest'||kind==='guest-save'||kind==='guest-reuse') pending=root.querySelector('.connect-guest:not(.acct-restore) form').onsubmit({preventDefault(){}});
              if(kind==='signin'||kind==='revoked') pending=api.connect();
              globalThis.flow=Promise.resolve(pending).catch(e=>{globalThis.failure=e.code||e.message;});
            };
          });
          if(kind==='guest-reuse') await page.evaluate(async()=>{
            await ids.updateMeta({guest:true,linked:false});
            await api.loadDevice();
            globalThis.original=api.app.identity;
          });
          await page.evaluate(kind=>start(kind),kind);
          await page.waitForFunction(()=>globalThis.entered);
          await other.evaluate(async change=>{
            const ids=await import('/src/core/identity-store.js');
            const db=await import('/src/core/db.js');
            if(change==='logout') await ids.clearIdentity();
            else {
              const {generateIdentity}=await import('/fixture.js');
              await ids.saveIdentity(generateIdentity(9),{owner:'newer'},{onboarding:true,fence:await ids.beginIdentity()});
              await db.putSealed(db.Stores.dm,'sentinel',{body:'newer account message'});
            }
          },change);
          await page.evaluate(async()=>{release();await flow;});
          // The scanner starts runLink after it returns; let that actual flow finish.
          if(kind==='scan') await page.waitForFunction(()=>globalThis.message);
          const result=await page.evaluate(async()=>({
            stored:await ids.loadIdentity().then(record=>record?(record.meta.owner||'onboarding'):null),
            marker:await ids.loadIdentity().then(record=>record?.identity.edPublic[0]??null),
            sentinel:await db.getSealed(db.Stores.dm,'sentinel')??null,
            sameIdentity:api.app.identity===original,session:api.app.session!==null,
            open:f.connections.filter(c=>c.open).length,signedIn,
          }));
          assert.equal(result.stored,change==='logout'?null:'newer',`${kind}/${change}: persisted identity`);
          assert.equal(result.marker,change==='logout'?null:9,`${kind}/${change}: persisted identity key`);
          assert.deepEqual(result.sentinel,change==='logout'?null:{body:'newer account message'},`${kind}/${change}: account data`);
          assert.equal(result.sameIdentity,true,`${kind}/${change}: app identity`);
          assert.equal(result.session,false,`${kind}/${change}: no session publication`);
          assert.equal(result.open,0,`${kind}/${change}: pending connections closed`);
          assert.equal(result.signedIn,0,`${kind}/${change}: no success callback`);
          if(change==='replacement' && kind==='guest') {
            await page.evaluate(()=>start('guest'));
            await page.waitForFunction(()=>globalThis.entered);
            await page.evaluate(async()=>{release();await flow;});
            const replacement=await page.evaluate(async()=>({
              guest:(await ids.loadIdentity()).meta.guest,
              sentinel:await db.getSealed(db.Stores.dm,'sentinel')??null,
              signedIn,
            }));
            assert.deepEqual(replacement,{guest:true,sentinel:null,signedIn:1},'fresh successful guest atomically replaces old account');
          }
          // A deliberate fresh flow after logout still works, for every entry.
          if(change==='logout' && !['signin','revoked'].includes(kind)) {
            await page.evaluate(kind=>{globalThis.message='';start(kind);},kind);
            await page.waitForFunction(()=>globalThis.entered);
            await page.evaluate(async()=>{release();await flow;});
            await page.waitForFunction(()=>signedIn===1);
            assert.equal(await page.evaluate(async()=>!!(await ids.loadIdentity())),true,`${kind}: fresh onboarding allowed`);
          }
        } finally {await ctx.close();}
      }
    }
  } finally {await browser?.close();await new Promise(resolve=>server.close(resolve));}
});
