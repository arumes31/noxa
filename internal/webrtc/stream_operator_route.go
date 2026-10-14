package webrtc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"time"

	pion "github.com/pion/webrtc/v4"
)

// These types are exclusively for the loopback operator endpoint. They are not
// embedded in netproto diagnostics or any authenticated client's wire messages.
type StreamRouteRequest struct {
	PublisherID string
	Slot        string
	Generation  uint64
	SSRC        uint32
	RouteToken  string
}
type StreamRouteCandidate struct {
	Component uint16 `json:"component"`
	Address   string `json:"address"`
	Port      uint16 `json:"port"`
	Protocol  string `json:"protocol"`
	Type      string `json:"type"`
}
type StreamRouteDiagnostics struct {
	SampledAt        int64                `json:"sampled_at"`
	PublisherID      string               `json:"publisher_id"`
	Slot             string               `json:"slot"`
	Generation       uint64               `json:"generation,string"`
	ChannelID        int64                `json:"channel_id"`
	SSRC             uint32               `json:"ssrc"`
	RID              string               `json:"rid"`
	IngressStartedAt int64                `json:"ingress_started_at"`
	PeerID           string               `json:"peer_id"`
	RouteToken       string               `json:"route_token"`
	Local            StreamRouteCandidate `json:"local"`
	Remote           StreamRouteCandidate `json:"remote"`
}

var ErrStreamRouteUnavailable = errors.New("stream route unavailable or changed")

type selectedStreamRoute struct {
	peerID, localUfrag, remoteUfrag string
	local, remote                   StreamRouteCandidate
}

func streamRouteCandidate(c *pion.ICECandidate) (StreamRouteCandidate, bool) {
	if c == nil || (c.Component != 1 && c.Component != 2) || c.Port == 0 {
		return StreamRouteCandidate{}, false
	}
	address, err := netip.ParseAddr(c.Address)
	if err != nil || address.IsUnspecified() || address.IsMulticast() {
		return StreamRouteCandidate{}, false
	}
	protocol, kind := c.Protocol.String(), c.Typ.String()
	if protocol != "udp" && protocol != "tcp" {
		return StreamRouteCandidate{}, false
	}
	if kind != "host" && kind != "srflx" && kind != "prflx" && kind != "relay" {
		return StreamRouteCandidate{}, false
	}
	return StreamRouteCandidate{Component: c.Component, Address: address.String(), Port: c.Port, Protocol: protocol, Type: kind}, true
}

func readSelectedStreamRoute(peer *PeerConnectionWrapper) (selectedStreamRoute, error) {
	if peer == nil || peer.pc == nil || peer.pc.ConnectionState() != pion.PeerConnectionStateConnected || peer.pc.SCTP() == nil || peer.pc.SCTP().Transport() == nil {
		return selectedStreamRoute{}, ErrStreamRouteUnavailable
	}
	ice := peer.pc.SCTP().Transport().ICETransport()
	if ice == nil || (ice.State() != pion.ICETransportStateConnected && ice.State() != pion.ICETransportStateCompleted) {
		return selectedStreamRoute{}, ErrStreamRouteUnavailable
	}
	pair, err := ice.GetSelectedCandidatePair()
	if err != nil || pair == nil {
		return selectedStreamRoute{}, ErrStreamRouteUnavailable
	}
	local, ok := streamRouteCandidate(pair.Local)
	if !ok {
		return selectedStreamRoute{}, ErrStreamRouteUnavailable
	}
	remote, ok := streamRouteCandidate(pair.Remote)
	if !ok || local.Protocol != remote.Protocol {
		return selectedStreamRoute{}, ErrStreamRouteUnavailable
	}
	localParams, err := ice.GetLocalParameters()
	if err != nil {
		return selectedStreamRoute{}, ErrStreamRouteUnavailable
	}
	remoteParams, err := ice.GetRemoteParameters()
	if err != nil || localParams.UsernameFragment == "" || remoteParams.UsernameFragment == "" {
		return selectedStreamRoute{}, ErrStreamRouteUnavailable
	}
	return selectedStreamRoute{peerID: peer.pc.ID(), localUfrag: localParams.UsernameFragment, remoteUfrag: remoteParams.UsernameFragment, local: local, remote: remote}, nil
}

func (r *Router) streamRoute(request StreamRouteRequest, read func(*PeerConnectionWrapper) (selectedStreamRoute, error)) (*StreamRouteDiagnostics, error) {
	if request.PublisherID == "" || (request.Slot != SlotCam && request.Slot != SlotScreen) || request.Generation == 0 || request.SSRC == 0 {
		return nil, ErrStreamRouteUnavailable
	}
	// Read Pion outside router locks; verify the exact publication, ingress and
	// peer again afterwards. No route monitor, packet read or ICE probe is started.
	r.mu.RLock()
	r.watchMu.RLock()
	key := publicationKey{request.PublisherID, request.Slot}
	peer, channel := r.pubPeers[request.PublisherID], r.clientChan[request.PublisherID]
	var ingress *videoIngressDiagnostic
	var rid string
	for _, candidateRID := range []string{"", "f", "h", "q"} {
		input := r.videoIngress[videoIngressKey{request.PublisherID, request.Slot, candidateRID}]
		if input != nil && input.ssrc == request.SSRC && r.videoSources[request.PublisherID][request.Slot][candidateRID] == request.SSRC {
			if ingress != nil {
				r.watchMu.RUnlock()
				r.mu.RUnlock()
				return nil, ErrStreamRouteUnavailable
			}
			ingress, rid = input, candidateRID
		}
	}
	valid := peer != nil && channel > 0 && ingress != nil && r.publications[key] == request.Generation
	r.watchMu.RUnlock()
	r.mu.RUnlock()
	if !valid {
		return nil, ErrStreamRouteUnavailable
	}
	first, err := read(peer)
	if err != nil {
		return nil, ErrStreamRouteUnavailable
	}
	second, err := read(peer)
	if err != nil || first != second || first.peerID == "" {
		return nil, ErrStreamRouteUnavailable
	}
	// Credentials never leave this function; only username fragments contribute
	// to the opaque epoch hash, never passwords, SDP or ICE foundations.
	identity, _ := json.Marshal([]any{request.PublisherID, request.Slot, request.Generation, request.SSRC, first.peerID, first.localUfrag, first.remoteUfrag, first.local, first.remote})
	sum := sha256.Sum256(identity)
	token := hex.EncodeToString(sum[:])
	if request.RouteToken != "" && request.RouteToken != token {
		return nil, ErrStreamRouteUnavailable
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	r.watchMu.RLock()
	defer r.watchMu.RUnlock()
	if r.pubPeers[request.PublisherID] != peer || r.clientChan[request.PublisherID] != channel || r.publications[key] != request.Generation || r.videoIngress[videoIngressKey{request.PublisherID, request.Slot, rid}] != ingress || r.videoSources[request.PublisherID][request.Slot][rid] != request.SSRC {
		return nil, ErrStreamRouteUnavailable
	}
	return &StreamRouteDiagnostics{SampledAt: time.Now().UnixMilli(), PublisherID: request.PublisherID, Slot: request.Slot, Generation: request.Generation, ChannelID: channel, SSRC: request.SSRC, RID: rid, IngressStartedAt: ingress.started.UnixMilli(), PeerID: first.peerID, RouteToken: token, Local: first.local, Remote: first.remote}, nil
}

func (v *Voice) StreamRouteDiagnostics(request StreamRouteRequest) (*StreamRouteDiagnostics, error) {
	return v.router.streamRoute(request, readSelectedStreamRoute)
}
