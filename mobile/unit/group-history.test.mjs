import assert from 'node:assert/strict';
import test from 'node:test';
import nacl from 'tweetnacl';
import { DirectMessages } from '../src/core/direct.js';
import { Groups } from '../src/core/groups.js';
import { approvalTranscript } from '../src/core/dm-trust.js';
import { generateIdentity, publicKeyPEM, sealDM, toBase64 } from '../src/core/crypto.js';

test('revoked group sender remains verifiable without entering the live directory', async () => {
 const root=generateIdentity(),sender=generateIdentity(),recipient=generateIdentity();
 const first={device_id:'desktop',first_device:true,ed25519_public_key:publicKeyPEM(root.edPublic),x25519_public_key:toBase64(root.xPublic)};
 const phone={device_id:'phone',ed25519_public_key:publicKeyPEM(sender.edPublic),x25519_public_key:toBase64(sender.xPublic),approved_by:'desktop'};
 phone.approval_signature=toBase64(nacl.sign.detached(approvalTranscript('alice','desktop',phone.ed25519_public_key,phone.x25519_public_key),root.edSecret));
 const entry={trust:'ok',signingKey:root.edPublic,encryptionKey:root.xPublic,devices:[]};
 // Exercise the real historical verifier; the active directory intentionally
 // contains no phone (including after idle expiry or explicit sign-out).
 const dm={devicesOf:async()=>entry,historicalSenderKey:DirectMessages.prototype.historicalSenderKey};
 const body=sealDM(JSON.stringify({conversation_id:'group',epoch:1,reference:'r1',from:'alice',to:'bob',text:'old message'}),recipient.xPublic,sender.xSecret);
 const message={id:1,epoch:1,reference:'r1',from_unique_id:'alice',sender_device_id:'phone',sender_device_key:phone.x25519_public_key,sender_device_chain:[phone,first],device_envelope:true,body};
 const session=new EventTarget();Object.assign(session,{uniqueID:'bob',identity:recipient,conn:{request:async()=>({messages:[message]})}});
 const groups=new Groups(session,dm);
 await groups.loadOlder('group');
 assert.equal(groups.messages.get('group')[0].text,'old message');
 assert.deepEqual(entry.devices,[],'historical verification must not reactivate a sender');
 phone.approval_signature=toBase64(new Uint8Array(64));
 await groups.loadOlder('group');
 assert.equal(groups.messages.get('group')[0].text,null,'forged approval must fail');
 entry.trust='changed';
 assert.equal(await dm.historicalSenderKey('alice','phone',phone.x25519_public_key,[phone,first]),null);
 groups.close();
});
