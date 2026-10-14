package server

import (
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"noxa/internal/webrtc"
)

type streamTimingBackend interface {
	StreamTimingDiagnostics(webrtc.StreamTimingRequest, string) (*webrtc.StreamTimingDiagnostics, error)
}

func streamTimingQuery(raw string) (webrtc.StreamTimingRequest, bool) {
	var out webrtc.StreamTimingRequest
	if len(raw) > 768 {
		return out, false
	}
	q, err := url.ParseQuery(raw)
	if err != nil {
		return out, false
	}
	peer, stamp := q["peer_id"], q["ingress_started_at"]
	if len(peer) != 1 || len(stamp) != 1 || len(peer[0]) == 0 || len(peer[0]) > 80 || strings.ContainsAny(peer[0], " \t\r\n\x00") {
		return out, false
	}
	at, err := strconv.ParseInt(stamp[0], 10, 64)
	if err != nil || at <= 0 || strconv.FormatInt(at, 10) != stamp[0] {
		return out, false
	}
	out.PeerID, out.IngressStartedAt = peer[0], at
	q.Del("peer_id")
	q.Del("ingress_started_at")
	scope, ok := streamRouteQuery(q.Encode())
	out.StreamRouteRequest = scope
	return out, ok && scope.RouteToken != ""
}

// Own loopback guard is mandatory because this endpoint also accepts explicit
// POST arm and DELETE stop. A non-simple header and absent Origin prevent web
// pages from using simple cross-origin requests to arm local collection.
func (s *TCPServer) StreamTimingDiagnosticsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		address, parseErr := netip.ParseAddr(host)
		if err != nil || parseErr != nil || !address.IsLoopback() {
			http.Error(w, "local operator only", http.StatusForbidden)
			return
		}
		action := "read"
		switch r.Method {
		case http.MethodGet:
		case http.MethodPost:
			action = "arm"
		case http.MethodDelete:
			action = "stop"
		default:
			w.Header().Set("Allow", "GET, POST, DELETE")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Origin") != "" || r.ContentLength != 0 || (action != "read" && r.Header.Get("X-NoXa-Diagnostic") != "upstream-timing") {
			http.Error(w, "explicit local operation required", http.StatusBadRequest)
			return
		}
		request, ok := streamTimingQuery(r.URL.RawQuery)
		if !ok {
			http.Error(w, "invalid timing scope", http.StatusBadRequest)
			return
		}
		if s.deps == nil || s.deps.State == nil {
			http.Error(w, "timing unavailable", http.StatusServiceUnavailable)
			return
		}
		backend, ok := s.deps.Voice.(streamTimingBackend)
		if !ok {
			http.Error(w, "timing unavailable", http.StatusServiceUnavailable)
			return
		}
		// Arming requires the currently authenticated publisher. Retained interrupted
		// metadata remains readable only locally with all exact original scope pins.
		var admittedClient *Client
		var channel int64
		var epoch uint64
		if action == "arm" {
			s.mu.RLock()
			client := s.clients[request.PublisherID]
			s.mu.RUnlock()
			member, present := s.deps.State.GetClient(request.PublisherID)
			if client == nil || !client.isAuthed() || !present || member.ChannelID <= 0 {
				http.Error(w, "timing scope changed", http.StatusConflict)
				return
			}

			admittedClient = client
			channel = member.ChannelID
			epoch = member.ChannelEpoch
		}
		result, err := backend.StreamTimingDiagnostics(request, action)
		if err != nil || result == nil {
			http.Error(w, "timing unavailable or changed", http.StatusConflict)
			return
		}
		if action == "arm" {
			s.mu.RLock()
			same := s.clients[request.PublisherID] == admittedClient
			s.mu.RUnlock()
			after, present := s.deps.State.GetClient(request.PublisherID)
			if !same || !admittedClient.isAuthed() || !present || after.ChannelID != channel || after.ChannelEpoch != epoch {
				_, _ = backend.StreamTimingDiagnostics(request, "stop")
				http.Error(w, "timing scope changed", http.StatusConflict)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}
