import assert from 'node:assert/strict';
import { test } from 'node:test';
import { encryptionDetails, compactMediaEncryptionStatus } from '../src/encryption-details.js';
import { setLanguage } from '../src/i18n.js';

test('offline and unavailable diagnostics never claim an encrypted connection',()=>{
 setLanguage('en');const text=encryptionDetails({session:{connected:false}});
 assert.match(text,/not connected/);assert.match(text,/status unknown/);assert.match(text,/not active/);
 assert.match(text,/Channel chat & private calls: no E2EE/);
 assert.doesNotMatch(encryptionDetails(),/not connected/,'a failed lookup is unknown, not a confirmed disconnect');
});
test('everyday hovers omit protocol internals and identities but keep delivery warnings',()=>{
 setLanguage('en');const principal='untruncated-account-identity-'.repeat(20);
 const text=encryptionDetails({session:{connected:true,security:'TLS (fingerprint AA:BB)'},diagnostics:{active:true,protocol:'noxa-olm-v1',cipher:'Olm'},media:{state:'encrypted',recipients:1,keyHolders:[{nickname:'<b>peer</b>',principal}],waiting:1}});
 assert.doesNotMatch(text,/untruncated|<b>|AA:BB|noxa-olm|Olm|AES|fingerprint/);assert.match(text,/cannot hear\/see you yet/);
 assert.match(text,/User ↔ user: E2EE/);assert.match(text,/only the participating devices/);assert.ok(text.length<500);
 assert.match(encryptionDetails({media:{state:'paused'},voiceOnly:true}),/paused/);
 assert.match(encryptionDetails({media:{state:'partial',failed:['one']},voiceOnly:true}),/unavailable for some participants/);
 assert.match(compactMediaEncryptionStatus({state:'encrypted',recipients:1,keyHolders:[{}],membershipMismatch:['peer']}),/Warning: participant lists/);
 assert.match(compactMediaEncryptionStatus({state:'encrypted',recipients:1}),/setting up/);
});
test('German status and blocked secure messaging remain explicit',()=>{
 setLanguage('de');const text=encryptionDetails({diagnostics:{active:false,unavailable:'recovery_required'},capability:{reason:'keyless_guest'}});
 assert.match(text,/Senden gesperrt/);assert.doesNotMatch(text,/recovery_required/);assert.match(text,/Gastsitzung/);assert.match(text,/Kanalchat/);
 setLanguage('en');
});

test('transport shows encryption only for an observed TLS connection',()=>{
 setLanguage('en');
 assert.match(encryptionDetails({session:{connected:true,security:'TLS (fingerprint xyz)'}}),/You ↔ server: encrypted/);
 assert.match(encryptionDetails({session:{connected:true,security:'PLAINTEXT — traffic is NOT encrypted'}}),/You ↔ server: not encrypted/);
 assert.match(encryptionDetails({session:{connected:true,security:'unexpected'}}),/You ↔ server: status unknown/);
});
