package server

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"noxa/internal/webrtc"
)

type streamRouteBackend interface {
	StreamRouteDiagnostics(webrtc.StreamRouteRequest) (*webrtc.StreamRouteDiagnostics, error)
}

func streamRouteQuery(raw string) (webrtc.StreamRouteRequest, bool) {
	var out webrtc.StreamRouteRequest
	if len(raw) > 512 {
		return out, false
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return out, false
	}
	for key, items := range values {
		if len(items) != 1 || (key != "publisher_id" && key != "slot" && key != "generation" && key != "ssrc" && key != "route_token") {
			return out, false
		}
	}
	out.PublisherID, out.Slot, out.RouteToken = values.Get("publisher_id"), values.Get("slot"), values.Get("route_token")
	if len(out.PublisherID) < 1 || len(out.PublisherID) > 80 || strings.ContainsAny(out.PublisherID, " \t\r\n\x00") || (out.Slot != "screen" && out.Slot != "cam") {
		return out, false
	}
	generation, e1 := strconv.ParseUint(values.Get("generation"), 10, 64)
	ssrc, e2 := strconv.ParseUint(values.Get("ssrc"), 10, 32)
	if e1 != nil || e2 != nil || generation == 0 || ssrc == 0 || strconv.FormatUint(generation, 10) != values.Get("generation") || strconv.FormatUint(ssrc, 10) != values.Get("ssrc") {
		return out, false
	}
	out.Generation, out.SSRC = generation, uint32(ssrc)
	if _, present := values["route_token"]; present {
		decoded, decodeErr := hex.DecodeString(out.RouteToken)
		if decodeErr != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != out.RouteToken {
			return out, false
		}
	}
	return out, true
}

// Register exclusively with health.HandleLocalGET. Loopback is the operator
// boundary (typically authenticated SSH); forwarded headers grant no access.
// This endpoint never adds addresses to ordinary client diagnostics/telemetry.
func (s *TCPServer) StreamRouteDiagnosticsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		request, valid := streamRouteQuery(r.URL.RawQuery)
		if !valid {
			http.Error(w, "invalid route scope", http.StatusBadRequest)
			return
		}
		if s.deps == nil || s.deps.State == nil {
			http.Error(w, "route unavailable", http.StatusServiceUnavailable)
			return
		}
		backend, ok := s.deps.Voice.(streamRouteBackend)
		if !ok {
			http.Error(w, "route unavailable", http.StatusServiceUnavailable)
			return
		}
		s.mu.RLock()
		client := s.clients[request.PublisherID]
		s.mu.RUnlock()
		member, present := s.deps.State.GetClient(request.PublisherID)
		if client == nil || !client.isAuthed() || !present || member.ChannelID <= 0 {
			http.Error(w, "route unavailable or changed", http.StatusConflict)
			return
		}
		channel, epoch := member.ChannelID, member.ChannelEpoch
		result, err := backend.StreamRouteDiagnostics(request)
		s.mu.RLock()
		same := s.clients[request.PublisherID] == client
		s.mu.RUnlock()
		after, present := s.deps.State.GetClient(request.PublisherID)
		if err != nil || result == nil || !same || !client.isAuthed() || !present || after.ChannelID != channel || after.ChannelEpoch != epoch || result.ChannelID != channel || result.PublisherID != request.PublisherID || result.Slot != request.Slot || result.Generation != request.Generation || result.SSRC != request.SSRC {
			http.Error(w, "route unavailable or changed", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}
