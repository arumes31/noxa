package turn

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"

	pion "github.com/pion/turn/v5"
	"golang.org/x/time/rate"
)

const CredentialLifetime = time.Hour
const AuthorizationLease = 15 * time.Second

var ErrDenied = errors.New("relay access unavailable")

// Lease contains only the opaque generation and its delivery metadata. Canonical
// account/session identity never appears in a TURN username.
type Lease struct {
	Username   string `json:"username"`
	Password   string `json:"credential"`
	Generation uint64 `json:"generation"`
	IssuedAt   int64  `json:"issued_at"`
	RefreshAt  int64  `json:"refresh_at"`
	ExpiresAt  int64  `json:"expires_at"`
}
type grant struct {
	lease   Lease
	key     []byte
	session string
	expires time.Time
	sockets map[*relaySocket]struct{}
}
type relaySession struct {
	account    string
	blocked    bool
	validUntil time.Time
	generation uint64
	current    string
	in, out    *rate.Limiter
}

// Manager lives with the control authority; restarting fails closed. Its lock
// serializes issuance, allocation registration and revocation. Packet I/O uses
// short bounded operations under this same barrier, never remote policy reads.
type Manager struct {
	mu               sync.Mutex
	endpoints        map[netip.AddrPort]*relaySocket
	realm            string
	sessions         map[string]*relaySession
	grants           map[string]*grant
	now              func() time.Time
	maxAllocations   int
	totalAllocations int
	bytesPerSecond   int
	closed           bool
}

func NewManager(realm string, maxAllocations, bytesPerSecond int) *Manager {
	return &Manager{endpoints: make(map[netip.AddrPort]*relaySocket), realm: realm, sessions: make(map[string]*relaySession), grants: make(map[string]*grant), now: time.Now, maxAllocations: maxAllocations, bytesPerSecond: bytesPerSecond}
}
func (m *Manager) Register(session, account string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed && m.sessions[session] == nil {
		burst := max(m.bytesPerSecond, 65536)
		m.sessions[session] = &relaySession{account: account, in: rate.NewLimiter(rate.Limit(m.bytesPerSecond), burst), out: rate.NewLimiter(rate.Limit(m.bytesPerSecond), burst)}
	}
}

// Authorize is called only under the authoritative policy lease. A blocked
// session cannot regain access through renewal or delayed reconciliation.
func (m *Manager) Authorize(session string, allowed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[session]
	if s == nil {
		return
	}
	if allowed && !s.blocked {
		s.validUntil = m.now().Add(AuthorizationLease)
		return
	}
	s.validUntil = time.Time{}
	m.closeSessionLocked(session)
}
func (m *Manager) Issue(session string) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	s := m.sessions[session]
	if m.closed || s == nil || s.blocked || !now.Before(s.validUntil) {
		return Lease{}, ErrDenied
	}
	if old := m.grants[s.current]; old != nil && now.UnixMilli() < old.lease.RefreshAt {
		return old.lease, nil
	}
	// Remove expired generations before minting; never more than two live ones.
	count := 0
	for id, g := range m.grants {
		if g.session == session {
			if !now.Before(g.expires) {
				m.closeGrantLocked(id, g)
			} else {
				count++
			}
		}
	}
	if count >= 2 {
		return Lease{}, ErrDenied
	}
	username, err := randomCredential()
	if err != nil {
		return Lease{}, err
	}
	password, err := randomCredential()
	if err != nil {
		return Lease{}, err
	}
	var jitter [1]byte
	if _, err = rand.Read(jitter[:]); err != nil {
		return Lease{}, err
	}
	s.generation++
	lease := Lease{Username: username, Password: password, Generation: s.generation, IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(CredentialLifetime).UnixMilli(), RefreshAt: now.Add(50*time.Minute + time.Duration(jitter[0])*time.Second).UnixMilli()}
	m.grants[username] = &grant{lease: lease, key: pion.GenerateAuthKey(username, m.realm, password), session: session, expires: now.Add(CredentialLifetime), sockets: make(map[*relaySocket]struct{})}
	s.current = username
	return lease, nil
}
func randomCredential() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func (m *Manager) validLocked(g *grant) bool {
	if g == nil || m.closed {
		return false
	}
	s := m.sessions[g.session]
	now := m.now()
	return s != nil && !s.blocked && now.Before(s.validUntil) && now.Before(g.expires)
}
func (m *Manager) Authenticate(a *pion.RequestAttributes) (string, []byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.grants[a.Username]
	if a.Realm != m.realm || !m.validLocked(g) {
		return "", nil, false
	}
	// Pion binds an allocation to this user ID. Keep it generation-specific:
	// returning an account ID here would permit cross-generation refresh.
	return a.Username, append([]byte(nil), g.key...), true
}
func (m *Manager) Revoke(session string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[session]; s != nil {
		s.blocked = true
		s.validUntil = time.Time{}
	}
	m.closeSessionLocked(session)
}
func (m *Manager) Remove(session string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeSessionLocked(session)
	delete(m.sessions, session)
}
func (m *Manager) closeSessionLocked(session string) {
	for id, g := range m.grants {
		if g.session == session {
			m.closeGrantLocked(id, g)
		}
	}
}
func (m *Manager) closeGrantLocked(id string, g *grant) {
	delete(m.grants, id)
	for socket := range g.sockets {
		socket.closeLocked()
	}
}

// Sweep closes sockets even when neither side sends traffic. Packet wrappers
// check exact deadlines independently, including while the sweeper is delayed.
func (m *Manager) Sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, g := range m.grants {
		if !m.validLocked(g) {
			m.closeGrantLocked(id, g)
		}
	}
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for id, g := range m.grants {
		m.closeGrantLocked(id, g)
	}
	clear(m.sessions)
}

// Generator restricts relay sockets to an explicit UDP range and destinations.
// TCP/TLS client transports still use UDP relays; RFC6062 relays are disabled.
type Generator struct {
	Manager     *Manager
	Base        *pion.RelayAddressGeneratorPortRange
	AllowedPeer func(net.Addr) bool
}

func (g *Generator) Validate() error {
	if g.Manager == nil || g.AllowedPeer == nil || g.Base == nil || g.Base.MinPort > g.Base.MaxPort || g.Base.MaxPort == 65535 {
		return ErrDenied
	}
	return g.Base.Validate()
}
func (g *Generator) AllocatePacketConn(c pion.AllocateListenerConfig) (net.PacketConn, net.Addr, error) {
	m := g.Manager
	m.mu.Lock()
	defer m.mu.Unlock()
	grant := m.grants[c.UserID]
	if !m.validLocked(grant) || c.Realm != m.realm || (c.RequestedPort != 0 && (c.RequestedPort < int(g.Base.MinPort) || c.RequestedPort > int(g.Base.MaxPort))) {
		return nil, nil, ErrDenied
	}
	count, accountCount := 0, 0
	for _, item := range m.grants {
		if owner := m.sessions[item.session]; owner != nil && owner.account == m.sessions[grant.session].account {
			accountCount += len(item.sockets)
		}
		if item.session == grant.session {
			count += len(item.sockets)
		}
	}
	if count >= 16 || accountCount >= 64 || m.totalAllocations >= m.maxAllocations {
		return nil, nil, ErrDenied
	}
	conn, addr, err := g.Base.AllocatePacketConn(c)
	if err != nil {
		return nil, nil, err
	}
	session := m.sessions[grant.session]
	socket := &relaySocket{PacketConn: conn, manager: m, grant: grant, allowed: g.AllowedPeer, in: session.in, out: session.out}
	endpoint := addr.(*net.UDPAddr).AddrPort()
	socket.endpoint = netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
	m.endpoints[socket.endpoint] = socket
	grant.sockets[socket] = struct{}{}
	m.totalAllocations++
	return socket, addr, nil
}
func (*Generator) AllocateListener(pion.AllocateListenerConfig) (net.Listener, net.Addr, error) {
	return nil, nil, ErrDenied
}
func (*Generator) AllocateConn(pion.AllocateConnConfig) (net.Conn, error) { return nil, ErrDenied }

type relaySocket struct {
	endpoint netip.AddrPort
	net.PacketConn
	manager *Manager
	grant   *grant
	allowed func(net.Addr) bool
	closed  bool
	in, out *rate.Limiter
}

func (s *relaySocket) closeLocked() {
	if s.closed {
		return
	}
	s.closed = true
	_ = s.PacketConn.Close()
	delete(s.grant.sockets, s)
	if s.manager.endpoints[s.endpoint] == s {
		delete(s.manager.endpoints, s.endpoint)
	}
	s.manager.totalAllocations--
}
func (s *relaySocket) Close() error {
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	s.closeLocked()
	return nil
}
func (s *relaySocket) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, addr, err := s.PacketConn.ReadFrom(p)
		if err != nil {
			return n, addr, err
		}
		s.manager.mu.Lock()
		valid := !s.closed && s.manager.validLocked(s.grant)
		allowed := valid && s.allowedLocked(addr) && s.in.AllowN(s.manager.now(), n)
		s.manager.mu.Unlock()
		if !valid {
			return 0, nil, net.ErrClosed
		}
		if allowed {
			return n, addr, nil
		}
	}
}
func (s *relaySocket) WriteTo(p []byte, addr net.Addr) (int, error) {
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	if s.closed || !s.manager.validLocked(s.grant) {
		return 0, net.ErrClosed
	}
	if !s.allowedLocked(addr) || !s.out.AllowN(s.manager.now(), len(p)) {
		return 0, ErrDenied
	}
	// No unbounded network wait under the revocation barrier.
	if err := s.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		return 0, err
	}
	return s.PacketConn.WriteTo(p, addr)
}

// An exact active allocation can be a peer for private calls on this relay.
// The listener and unrelated services on the public IP remain unreachable.
// Called with the registry lock held; a revoked destination is denied too.
func (s *relaySocket) allowedLocked(addr net.Addr) bool {
	if s.allowed(addr) {
		return true
	}
	udp, ok := addr.(*net.UDPAddr)
	if !ok {
		return false
	}
	endpoint := udp.AddrPort()
	peer := s.manager.endpoints[netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())]
	return peer != nil && peer != s && !peer.closed && s.manager.validLocked(peer.grant)
}
