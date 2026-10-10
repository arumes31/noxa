package webrtc

import (
	"sort"
	"time"

	"noxa/internal/netproto"
)

// VideoOperatorDiagnostics exposes watcher identity only to the server's
// operator handler. It does not replace or broaden StreamDiagnostics access.
func (r *Router) VideoOperatorDiagnostics() *netproto.VideoOperatorDiagnostics {
	r.mu.RLock()
	defer r.mu.RUnlock()
	r.watchMu.RLock()
	defer r.watchMu.RUnlock()
	now := time.Now()
	result := &netproto.VideoOperatorDiagnostics{SampledAt: now.UnixMilli(), Publications: []netproto.VideoOperatorPublication{}}
	keys := make([]publicationKey, 0, len(r.publications))
	for key := range r.publications {
		if r.clientChan[key.publisher] > 0 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].publisher != keys[j].publisher {
			return keys[i].publisher < keys[j].publisher
		}
		return keys[i].slot < keys[j].slot
	})
	if len(keys) > netproto.MaxVideoOperatorPublications {
		result.Truncated = true
		keys = keys[:netproto.MaxVideoOperatorPublications]
	}
	for _, key := range keys {
		publication := netproto.VideoOperatorPublication{
			PublisherID: key.publisher, Slot: key.slot, Generation: r.publications[key], ChannelID: r.clientChan[key.publisher],
			Layers: []netproto.VideoStreamLayerDiagnostics{}, Viewers: []netproto.VideoOperatorViewer{},
		}
		for _, rid := range []string{"", "f", "h", "q"} {
			input := r.videoIngress[videoIngressKey{key.publisher, key.slot, rid}]
			if input == nil || input.ssrc != r.videoSources[key.publisher][key.slot][rid] {
				continue
			}
			input.mu.Lock()
			publication.Layers = append(publication.Layers, netproto.VideoStreamLayerDiagnostics{
				RID: rid, SSRC: input.ssrc, StartedAt: input.started.UnixMilli(), Ingress: input.stage.snapshot(now),
			})
			input.mu.Unlock()
		}
		viewers := make([]string, 0, len(r.members[publication.ChannelID]))
		for client := range r.members[publication.ChannelID] {
			if client != key.publisher && r.publisherAllowedLocked(client, key.publisher) {
				viewers = append(viewers, client)
			}
		}
		sort.Strings(viewers)
		if len(viewers) > netproto.MaxVideoOperatorViewers {
			publication.ViewersTruncated = true
			viewers = viewers[:netproto.MaxVideoOperatorViewers]
		}
		for _, client := range viewers {
			publication.Viewers = append(publication.Viewers, r.videoOperatorViewerLocked(client, key, now))
		}
		result.Publications = append(result.Publications, publication)
	}
	return result
}

// Router.mu and watchMu are held; snapshots never retain mutable media state.
func (r *Router) videoOperatorViewerLocked(client string, key publicationKey, now time.Time) netproto.VideoOperatorViewer {
	viewer := netproto.VideoOperatorViewer{ClientID: client, Session: r.watchSessions[client], ConnectionState: "missing"}
	if peer := r.pubPeers[client]; peer != nil {
		viewer.ConnectionState = peer.pc.ConnectionState().String()
	}
	watch := r.watches[watchKey{client, key.publisher, key.slot}]
	if watch.publication == r.publications[key] && watch.session == viewer.Session {
		viewer.WatchRevision, viewer.WatchEpoch, viewer.Watching = watch.revision, watch.epoch, watch.active
	}
	tracks := r.pubTracks[client][key.publisher]
	if tracks == nil || tracks.video[key.slot] == nil {
		return viewer
	}
	viewer.OutputExists = true
	output := tracks.video[key.slot].egress
	if output == nil {
		return viewer
	}
	output.mu.RLock()
	viewer.OutputActive, viewer.OutputSSRC = output.active, output.outputSSRC.Load()
	pacer := output.pacer
	output.mu.RUnlock()
	if pacer != nil {
		viewer.Pacer = pacer.diagnosticSnapshot(now)
	}
	d := &output.videoDiagnostic
	d.mu.Lock()
	if viewer.Watching && d.publication == watch.publication && d.watchEpoch == watch.epoch && !d.stage.last.IsZero() {
		viewer.Forwarding = &netproto.VideoStreamForwardDiagnostics{SourceSSRC: d.sourceSSRC, RID: d.rid,
			OutputSSRC: viewer.OutputSSRC, Stage: d.stage.snapshot(now)}
	}
	d.mu.Unlock()
	return viewer
}

func (v *Voice) VideoOperatorDiagnostics() *netproto.VideoOperatorDiagnostics {
	return v.router.VideoOperatorDiagnostics()
}
