// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-FileCopyrightText: 2026 noxa contributors
// SPDX-License-Identifier: MIT

// Package nackgen is a bounded copy of the pion/interceptor NACK generator
// (pkg/nack, v0.1.51). The upstream generator prunes its per-SSRC retry
// counters with slices.Contains against the full missing list, which is
// quadratic in the configured history (16384 for noxa video), and its receive
// log clears every slot of an arbitrarily large forward sequence jump. A
// publisher controls both through RTP sequence numbers. This copy rebuilds the
// retry counters in linear time and bounds gap clearing to the log size; the
// wire behaviour is otherwise unchanged.
package nackgen

import (
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
)

var errInvalidSize = errors.New("nackgen: size must be a power of two between 64 and 32768")

// Option configures a Generator.
type Option func(*Generator) error

// Size sets the receive history in packets (a power of two, 64..32768).
func Size(size uint16) Option {
	return func(g *Generator) error { g.size = size; return nil }
}

// MaxNacksPerPacket bounds how often one missing packet is requested.
// Zero means unlimited.
func MaxNacksPerPacket(n uint16) Option {
	return func(g *Generator) error { g.maxNacksPerPacket = n; return nil }
}

// Interval sets the NACK send interval.
func Interval(d time.Duration) Option {
	return func(g *Generator) error { g.interval = d; return nil }
}

// Factory builds Generators for each PeerConnection.
type Factory struct{ opts []Option }

// NewFactory returns a Factory applying opts to every Generator.
func NewFactory(opts ...Option) *Factory { return &Factory{opts: opts} }

// NewInterceptor implements interceptor.Factory.
func (f *Factory) NewInterceptor(string) (interceptor.Interceptor, error) {
	g := &Generator{
		size:          512,
		interval:      100 * time.Millisecond,
		receiveLogs:   map[uint32]*receiveLog{},
		nackCountLogs: map[uint32]map[uint16]uint16{},
		close:         make(chan struct{}),
		log:           logging.NewDefaultLoggerFactory().NewLogger("nack_generator"),
	}
	for _, opt := range f.opts {
		if err := opt(g); err != nil {
			return nil, err
		}
	}
	if !validSize(g.size) {
		return nil, errInvalidSize
	}
	return g, nil
}

func validSize(size uint16) bool {
	return size >= 64 && size&(size-1) == 0
}

func streamSupportNack(info *interceptor.StreamInfo) bool {
	for _, fb := range info.RTCPFeedback {
		if fb.Type == "nack" && fb.Parameter == "" {
			return true
		}
	}
	return false
}

// Generator sends transport-layer NACKs for missing RTP sequence numbers.
type Generator struct {
	interceptor.NoOp
	size              uint16
	maxNacksPerPacket uint16
	interval          time.Duration
	m                 sync.Mutex
	wg                sync.WaitGroup
	close             chan struct{}
	log               logging.LeveledLogger

	receiveLogsMu sync.Mutex
	receiveLogs   map[uint32]*receiveLog
	nackCountLogs map[uint32]map[uint16]uint16
}

// BindRTCPWriter starts the NACK loop once per PeerConnection.
func (g *Generator) BindRTCPWriter(writer interceptor.RTCPWriter) interceptor.RTCPWriter {
	g.m.Lock()
	defer g.m.Unlock()
	if g.isClosed() {
		return writer
	}
	g.wg.Add(1)
	go g.loop(writer)
	return writer
}

// BindRemoteStream records received sequence numbers of NACK-capable streams.
func (g *Generator) BindRemoteStream(info *interceptor.StreamInfo, reader interceptor.RTPReader) interceptor.RTPReader {
	if !streamSupportNack(info) {
		return reader
	}
	log := newReceiveLog(g.size)
	g.receiveLogsMu.Lock()
	g.receiveLogs[info.SSRC] = log
	g.receiveLogsMu.Unlock()
	return interceptor.RTPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
		i, attr, err := reader.Read(b, a)
		if err != nil {
			return 0, nil, err
		}
		if attr == nil {
			attr = make(interceptor.Attributes)
		}
		header, err := attr.GetRTPHeader(b[:i])
		if err != nil {
			return 0, nil, err
		}
		log.add(header.SequenceNumber)
		return i, attr, nil
	})
}

// UnbindRemoteStream drops the stream's receive and retry state.
func (g *Generator) UnbindRemoteStream(info *interceptor.StreamInfo) {
	g.receiveLogsMu.Lock()
	delete(g.receiveLogs, info.SSRC)
	delete(g.nackCountLogs, info.SSRC)
	g.receiveLogsMu.Unlock()
}

// Close stops the NACK loop.
func (g *Generator) Close() error {
	defer g.wg.Wait()
	g.m.Lock()
	defer g.m.Unlock()
	if !g.isClosed() {
		close(g.close)
	}
	return nil
}

func (g *Generator) isClosed() bool {
	select {
	case <-g.close:
		return true
	default:
		return false
	}
}

func (g *Generator) loop(writer interceptor.RTCPWriter) {
	defer g.wg.Done()
	senderSSRC := rand.Uint32() // #nosec G404 -- RTCP sender SSRC, not a secret
	missingBuf := make([]uint16, g.size)
	filteredBuf := make([]uint16, g.size)
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			g.receiveLogsMu.Lock()
			toSend := g.collect(senderSSRC, missingBuf, filteredBuf)
			g.receiveLogsMu.Unlock()
			for _, pkt := range toSend {
				if _, err := writer.Write([]rtcp.Packet{pkt}, interceptor.Attributes{}); err != nil {
					g.log.Warnf("failed sending nack: %+v", err)
				}
			}
		case <-g.close:
			return
		}
	}
}

// collect builds one NACK per SSRC with missing packets. Requires
// receiveLogsMu. Work is linear in each stream's missing packets.
func (g *Generator) collect(senderSSRC uint32, missingBuf, filteredBuf []uint16) []rtcp.Packet {
	var toSend []rtcp.Packet
	for ssrc, log := range g.receiveLogs {
		missing := log.missingSeqNumbers(missingBuf)
		if len(missing) == 0 {
			delete(g.nackCountLogs, ssrc)
			continue
		}
		requested := missing
		if g.maxNacksPerPacket > 0 {
			// Rebuild the retry counters from the current missing set: entries
			// for packets that arrived or left the window drop out without a
			// per-entry search of the missing list.
			previous := g.nackCountLogs[ssrc]
			counts := make(map[uint16]uint16, len(missing))
			count := 0
			for _, seq := range missing {
				n := previous[seq]
				if n < g.maxNacksPerPacket {
					filteredBuf[count] = seq
					count++
				}
				counts[seq] = n + 1
			}
			g.nackCountLogs[ssrc] = counts
			if count == 0 {
				continue
			}
			requested = filteredBuf[:count]
		}
		toSend = append(toSend, &rtcp.TransportLayerNack{
			SenderSSRC: senderSSRC,
			MediaSSRC:  ssrc,
			Nacks:      rtcp.NackPairsFromSequenceNumbers(requested),
		})
	}
	return toSend
}
