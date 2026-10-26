//! High-level Olm boundary. Cryptography is delegated to pinned vodozemac.
//! No transport, trust-directory or durable storage decisions occur here.
use base64::{Engine as _, prelude::BASE64_STANDARD};
use serde::{Deserialize, Serialize};
use vodozemac::{
    Curve25519PublicKey,
    olm::{Account, AccountPickle, OlmMessage, Session, SessionConfig, SessionPickle},
};
use zeroize::{Zeroize, Zeroizing};

const VERSION: &str = "noxa-olm-v1";
const MAX_INPUT: usize = 1024 * 1024;
const MAX_OUTPUT: usize = 2 * 1024 * 1024;
const MAX_TEXT: usize = 65536;

#[link(wasm_import_module = "noxa")]
unsafe extern "C" {
    fn random_fill(ptr: *mut u8, len: usize) -> i32;
}

#[unsafe(no_mangle)]
unsafe extern "Rust" fn __getrandom_v03_custom(
    ptr: *mut u8,
    len: usize,
) -> std::result::Result<(), getrandom::Error> {
    if unsafe { random_fill(ptr, len) } == 0 {
        Ok(())
    } else {
        Err(getrandom::Error::UNSUPPORTED)
    }
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
struct Binding {
    server: String,
    conversation: String,
    sender: String,
    sender_device: String,
    recipient: String,
    recipient_device: String,
    message_id: String,
    purpose: String,
}
impl Binding {
    fn valid(&self) -> bool {
        [
            &self.server,
            &self.conversation,
            &self.sender,
            &self.sender_device,
            &self.recipient,
            &self.recipient_device,
            &self.message_id,
        ]
        .iter()
        .all(|s| !s.is_empty() && s.len() <= 256 && !s.chars().any(char::is_control))
            && matches!(self.purpose.as_str(), "message" | "edit" | "delete" | "media-key")
    }
}
#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct Envelope {
    version: String,
    session_id: String,
    binding: Binding,
    message: OlmMessage,
}
#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct PlainEnvelope {
    version: String,
    session_id: String,
    binding: Binding,
    plaintext: String,
}
impl Drop for PlainEnvelope {
    fn drop(&mut self) {
        self.plaintext.zeroize();
    }
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    op: String,
    key: String,
    #[serde(default)]
    account: String,
    #[serde(default)]
    session: String,
    #[serde(default)]
    peer_identity: String,
    #[serde(default)]
    one_time_key: String,
    #[serde(default)]
    count: usize,
    #[serde(default)]
    binding: Option<Binding>,
    #[serde(default)]
    plaintext: String,
    #[serde(default)]
    envelope: Option<Envelope>,
}
impl Drop for Request {
    fn drop(&mut self) {
        self.key.zeroize();
        self.plaintext.zeroize();
    }
}
#[derive(Serialize)]
struct PreKey {
    id: String,
    key: String,
}
#[derive(Serialize)]
struct Bundle {
    identity: String,
    signing: String,
    one_time_keys: Vec<PreKey>,
}
#[derive(Default, Serialize)]
struct Response {
    #[serde(skip_serializing_if = "String::is_empty")]
    error: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    account: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    session: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    session_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    bundle: Option<Bundle>,
    #[serde(skip_serializing_if = "Option::is_none")]
    envelope: Option<Envelope>,
    #[serde(skip_serializing_if = "Option::is_none")]
    plaintext: Option<String>,
}

type Result<T> = std::result::Result<T, &'static str>;
fn account(r: &Request, key: &[u8; 32]) -> Result<Account> {
    AccountPickle::from_encrypted(&r.account, key)
        .map(Account::from_pickle)
        .map_err(|_| "invalid protected account")
}
fn session(r: &Request, key: &[u8; 32]) -> Result<Session> {
    SessionPickle::from_encrypted(&r.session, key)
        .map(Session::from_pickle)
        .map_err(|_| "invalid protected session")
}
fn peer(r: &Request) -> Result<Curve25519PublicKey> {
    Curve25519PublicKey::from_base64(&r.peer_identity).map_err(|_| "invalid peer identity")
}
fn binding(r: &Request) -> Result<&Binding> {
    r.binding
        .as_ref()
        .filter(|b| b.valid())
        .ok_or("invalid message binding")
}
fn encrypted(r: &Request, s: &mut Session) -> Result<Envelope> {
    if r.plaintext.len() > MAX_TEXT {
        return Err("plaintext too large");
    }
    let b = binding(r)?.clone();
    let id = s.session_id();
    let plain = PlainEnvelope {
        version: VERSION.into(),
        session_id: id.clone(),
        binding: b.clone(),
        plaintext: r.plaintext.clone(),
    };
    let bytes = Zeroizing::new(serde_json::to_vec(&plain).map_err(|_| "encoding failed")?);
    let message = s.encrypt(&bytes).map_err(|_| "encryption failed")?;
    Ok(Envelope {
        version: VERSION.into(),
        session_id: id,
        binding: b,
        message,
    })
}
fn checked_envelope(r: &Request) -> Result<&Envelope> {
    let b = binding(r)?;
    let e = r.envelope.as_ref().ok_or("missing envelope")?;
    if e.version != VERSION || e.binding != *b || e.session_id.len() > 128 {
        return Err("envelope binding mismatch");
    }
    Ok(e)
}
fn decrypted(bytes: Vec<u8>, e: &Envelope, id: &str) -> Result<String> {
    let bytes = Zeroizing::new(bytes);
    let p: PlainEnvelope =
        serde_json::from_slice(&bytes).map_err(|_| "invalid authenticated payload")?;
    if p.version != VERSION
        || p.session_id != id
        || e.session_id != id
        || p.binding != e.binding
        || p.plaintext.len() > MAX_TEXT
    {
        return Err("authenticated binding mismatch");
    }
    Ok(p.plaintext.clone())
}
fn process(r: &Request) -> Result<Response> {
    let raw = Zeroizing::new(
        BASE64_STANDARD
            .decode(&r.key)
            .map_err(|_| "invalid storage key")?,
    );
    let key =
        Zeroizing::new(<[u8; 32]>::try_from(raw.as_slice()).map_err(|_| "invalid storage key")?);
    match r.op.as_str() {
        "new_account" | "prekeys" => {
            if !(1..=100).contains(&r.count) {
                return Err("invalid prekey count");
            }
            let mut a = if r.op == "new_account" {
                Account::new()
            } else {
                account(r, &key)?
            };
            // Never evict a still-needed private key to replenish the public pool.
            if a.stored_one_time_key_count() + r.count > 100 {
                return Err("prekey capacity reached");
            }
            a.generate_one_time_keys(r.count);
            let mut keys: Vec<_> = a
                .one_time_keys()
                .into_iter()
                .map(|(id, k)| PreKey {
                    id: id.to_base64(),
                    key: k.to_base64(),
                })
                .collect();
            keys.sort_by(|a, b| a.id.cmp(&b.id));
            let bundle = Bundle {
                identity: a.curve25519_key().to_base64(),
                signing: a.ed25519_key().to_base64(),
                one_time_keys: keys,
            };
            // Caller must persist this returned state atomically with the publication
            // outbox; retry that exact bundle rather than generating another one.
            a.mark_keys_as_published();
            Ok(Response {
                account: a.pickle().encrypt(&key),
                bundle: Some(bundle),
                ..Default::default()
            })
        }
        "retire_prekey" => {
            let mut a = account(r, &key)?;
            let otk = Curve25519PublicKey::from_base64(&r.one_time_key)
                .map_err(|_| "invalid one-time key")?;
            if a.remove_one_time_key(otk).is_none() {
                return Err("one-time key not found");
            }
            Ok(Response { account: a.pickle().encrypt(&key), ..Default::default() })
        }
        "outbound" => {
            let a = account(r, &key)?;
            let otk = Curve25519PublicKey::from_base64(&r.one_time_key)
                .map_err(|_| "invalid one-time key")?;
            let mut s = a
                .create_outbound_session(SessionConfig::version_1(), peer(r)?, otk)
                .map_err(|_| "session creation rejected")?;
            let e = encrypted(r, &mut s)?;
            Ok(Response {
                session_id: s.session_id(),
                session: s.pickle().encrypt(&key),
                envelope: Some(e),
                ..Default::default()
            })
        }
        "inbound" => {
            let e = checked_envelope(r)?;
            let OlmMessage::PreKey(m) = &e.message else {
                return Err("expected prekey message");
            };
            let mut a = account(r, &key)?;
            let result = a
                .create_inbound_session(SessionConfig::version_1(), peer(r)?, m)
                .map_err(|_| "session creation rejected")?;
            let id = result.session.session_id();
            let plain = decrypted(result.plaintext, e, &id)?;
            Ok(Response {
                account: a.pickle().encrypt(&key),
                session: result.session.pickle().encrypt(&key),
                session_id: id,
                plaintext: Some(plain),
                ..Default::default()
            })
        }
        "encrypt" => {
            let mut s = session(r, &key)?;
            let e = encrypted(r, &mut s)?;
            Ok(Response {
                session_id: s.session_id(),
                session: s.pickle().encrypt(&key),
                envelope: Some(e),
                ..Default::default()
            })
        }
        "decrypt" => {
            let e = checked_envelope(r)?;
            let mut s = session(r, &key)?;
            if e.session_id != s.session_id() {
                return Err("session mismatch");
            }
            let bytes = s.decrypt(&e.message).map_err(|_| "decryption rejected")?;
            let plain = decrypted(bytes, e, &s.session_id())?;
            Ok(Response {
                session_id: s.session_id(),
                session: s.pickle().encrypt(&key),
                plaintext: Some(plain),
                ..Default::default()
            })
        }
        _ => Err("unsupported operation"),
    }
}

// The host owns these allocations, calls free exactly once, and closes the
// instance after every operation. JSON bytes cannot supply pointers to this ABI.
#[unsafe(no_mangle)]
pub extern "C" fn alloc(len: usize) -> *mut u8 {
    if len > MAX_INPUT {
        return std::ptr::null_mut();
    }
    Box::into_raw(vec![0u8; len].into_boxed_slice()) as *mut u8
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn free(ptr: *mut u8, len: usize) {
    let mut b = unsafe { Box::from_raw(std::ptr::slice_from_raw_parts_mut(ptr, len)) };
    b.zeroize();
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn run(ptr: *const u8, len: usize) -> u64 {
    if len > MAX_INPUT {
        return 0;
    }
    let bytes = unsafe { std::slice::from_raw_parts(ptr, len) };
    let r = serde_json::from_slice::<Request>(bytes)
        .map_err(|_| "invalid request")
        .and_then(|r| process(&r));
    let mut result = match r {
        Ok(r) => r,
        Err(e) => Response {
            error: e.into(),
            ..Default::default()
        },
    };
    let output = serde_json::to_vec(&result)
        .expect("serializable response")
        .into_boxed_slice();
    if let Some(p) = &mut result.plaintext {
        p.zeroize();
    }
    if output.len() > MAX_OUTPUT {
        return 0;
    }
    let len = output.len();
    let ptr = Box::into_raw(output) as *mut u8;
    ((len as u64) << 32) | (ptr as u64)
}
