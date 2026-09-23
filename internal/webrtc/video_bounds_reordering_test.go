package webrtc

import "testing"

func TestVideoBoundsLatePacketsCannotInvalidateRecoveredKeyframe(t *testing.T) {
	for _, sequence := range []uint16{2, 4} {
		inspector := vp8BoundsInspector{bounds: VideoBounds{1280, 720}}
		if !inspector.accept(boundsKeyPacket(1, 1, 640, 360)) {
			t.Fatal("initial keyframe rejected")
		}
		if inspector.accept(boundsDeltaPacket(3, 3)) {
			t.Fatal("missing reference accepted")
		}
		if !inspector.accept(boundsKeyPacket(4, 4, 640, 360)) {
			t.Fatal("recovery keyframe rejected")
		}
		// A late or duplicate packet may even contain oversized dimensions;
		// it must neither reach the viewer nor poison the new frame state.
		if inspector.accept(boundsKeyPacket(sequence, 2, 1920, 1080)) {
			t.Fatal("stale oversized keyframe accepted")
		}
		if !inspector.accept(boundsDeltaPacket(5, 5)) {
			t.Fatal("late packet invalidated a newer recovered keyframe")
		}
	}
}

func TestVideoBoundsSequenceWrapStillAcceptsNewFrames(t *testing.T) {
	inspector := vp8BoundsInspector{bounds: VideoBounds{1280, 720}}
	if !inspector.accept(boundsKeyPacket(65535, 1, 640, 360)) || !inspector.accept(boundsDeltaPacket(0, 2)) {
		t.Fatal("sequence wrap blocked contiguous media")
	}
}
