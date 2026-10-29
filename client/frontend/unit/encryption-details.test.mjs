import assert from 'node:assert/strict';
import { test } from 'node:test';
import { encryptionDetails } from '../src/encryption-details.js';
import { setLanguage } from '../src/i18n.js';

test('offline and unavailable diagnostics never claim an encrypted connection',()=>{
 setLanguage('en');const text=encryptionDetails({session:{connected:false}});
 assert.match(text,/Not connected/);assert.match(text,/could not be verified/);assert.match(text,/No active encrypted channel-media/);
 assert.match(text,/Channel and global messages and private calls are not end-to-end encrypted/);
 assert.doesNotMatch(encryptionDetails(),/Not connected/,'a failed status lookup is unknown, not a confirmed disconnect');
});
test('live details preserve complete key-holder identities and partial/paused warnings',()=>{
 setLanguage('en');const principal='untruncated-account-identity-'.repeat(20);
 const text=encryptionDetails({session:{connected:true,security:'TLS (fingerprint AA:BB)'},diagnostics:{active:true,protocol:'noxa-olm-v1',cipher:'Olm'},media:{state:'encrypted',recipients:1,keyHolders:[{nickname:'<b>peer</b>',principal}],waiting:1}});
 assert.ok(text.includes(principal));assert.ok(text.includes('<b>peer</b>'));assert.match(text,/AA:BB/);assert.match(text,/still receiving/);assert.match(text,/not which member sent/);
 assert.match(encryptionDetails({media:{state:'paused'},voiceOnly:true}),/paused/);
 assert.match(encryptionDetails({media:{state:'partial',failed:['one']},voiceOnly:true}),/1 device\(s\) could not receive/);
});
test('German status and blocked secure messaging remain explicit',()=>{
 setLanguage('de');const text=encryptionDetails({diagnostics:{active:false,unavailable:'recovery_required'},capability:{reason:'keyless_guest'}});
 assert.match(text,/nicht aktiv/);assert.match(text,/recovery_required/);assert.match(text,/Gastsitzungen/);assert.match(text,/Kanalchat|Kanal- und/);
 setLanguage('en');
});
