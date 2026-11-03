// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-FileCopyrightText: 2026 noxa contributors
// SPDX-License-Identifier: MIT

package nackgen

import "sync"

const uint16SizeHalf = 1 << 15

type receiveLog struct {
	packets         []uint64
	size            uint16
	end             uint16
	started         bool
	lastConsecutive uint16
	m               sync.RWMutex
}

func newReceiveLog(size uint16) *receiveLog {
	return &receiveLog{packets: make([]uint64, size/64), size: size}
}

func (s *receiveLog) add(seq uint16) {
	s.m.Lock()
	defer s.m.Unlock()
	if !s.started {
		s.setReceived(seq)
		s.end = seq
		s.started = true
		s.lastConsecutive = seq
		return
	}
	diff := seq - s.end
	switch {
	case diff == 0:
		return
	case diff < uint16SizeHalf:
		// Clear the slots between end and seq. Past size slots the ring wraps,
		// so a larger jump clears the whole ring once instead of slot by slot.
		if diff > s.size {
			clear(s.packets)
		} else {
			for i := s.end + 1; i != seq; i++ {
				s.delReceived(i)
			}
		}
		s.end = seq
		if s.lastConsecutive+1 == seq {
			s.lastConsecutive = seq
		} else if seq-s.lastConsecutive > s.size {
			s.lastConsecutive = seq - s.size
			s.fixLastConsecutive()
		}
	case s.lastConsecutive+1 == seq:
		s.lastConsecutive = seq
		s.fixLastConsecutive()
	}
	s.setReceived(seq)
}

func (s *receiveLog) missingSeqNumbers(buf []uint16) []uint16 {
	s.m.RLock()
	defer s.m.RUnlock()
	until := s.end
	if until-s.lastConsecutive >= uint16SizeHalf {
		return nil
	}
	c := 0
	for i := s.lastConsecutive + 1; i != until+1; i++ {
		if !s.getReceived(i) {
			buf[c] = i
			c++
		}
	}
	return buf[:c]
}

func (s *receiveLog) setReceived(seq uint16) {
	pos := seq % s.size
	s.packets[pos/64] |= 1 << (pos % 64)
}

func (s *receiveLog) delReceived(seq uint16) {
	pos := seq % s.size
	s.packets[pos/64] &^= 1 << (pos % 64)
}

func (s *receiveLog) getReceived(seq uint16) bool {
	pos := seq % s.size
	return s.packets[pos/64]&(1<<(pos%64)) != 0
}

func (s *receiveLog) fixLastConsecutive() {
	i := s.lastConsecutive + 1
	for i != s.end+1 && s.getReceived(i) {
		i++
	}
	s.lastConsecutive = i - 1
}
