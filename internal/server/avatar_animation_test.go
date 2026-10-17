package server

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image/gif"
	"os"
	"testing"
)

func avatarFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/avatar-animated." + name)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestAvatarAnimationRoundTrip(t *testing.T) {
	for _, ext := range []string{"gif", "webp"} {
		t.Run(ext, func(t *testing.T) {
			b := avatarFixture(t, ext)
			raw, suffix, e := decodeAvatarImage(base64.StdEncoding.EncodeToString(b))
			if e != nil {
				t.Fatal(e)
			}
			if suffix != "."+ext || !bytes.Equal(raw, b) {
				t.Fatal("changed upload bytes")
			}
			store := assetStorage{rootDir: t.TempDir()}
			if _, e = store.writeAvatar("user/id", suffix, raw); e != nil {
				t.Fatal(e)
			}
			got, meta, e := store.readAvatar("user/id")
			if e != nil || !bytes.Equal(got, b) || meta.contentType != "image/"+ext {
				t.Fatal("roundtrip", meta, e)
			}
			if _, _, e = decodeImage(base64.StdEncoding.EncodeToString(b)); e == nil {
				t.Fatal("non-avatar static contract broadened")
			}
			if e = validateImageForDirectory(b, ".png", "avatars"); e == nil {
				t.Fatal("MIME mismatch accepted")
			}
			if ext == "gif" {
				g, e := gif.DecodeAll(bytes.NewReader(got))
				if e != nil || len(g.Image) != 2 || g.Image[0].ColorIndexAt(0, 0) == g.Image[1].ColorIndexAt(0, 0) {
					t.Fatal("two distinct frames missing", e)
				}
			} else {
				fs, e := preflightAvatarWebP(got)
				if e != nil || len(fs) != 2 || bytes.Equal(fs[0].data, fs[1].data) {
					t.Fatal("two WebP frames missing", e)
				}
			}
		})
	}
}
func TestAvatarAnimationRejectMalformed(t *testing.T) {
	for _, ext := range []string{"gif", "webp"} {
		b := avatarFixture(t, ext)
		for _, name := range []string{"truncated", "trailing", "frame-bounds", "bad-last-frame"} {
			t.Run(ext+"/"+name, func(t *testing.T) {
				x := append([]byte(nil), b...)
				switch name {
				case "truncated":
					x = x[:len(x)-1]
				case "trailing":
					x = append(x, 0)
				case "frame-bounds":
					if ext == "gif" {
						i := bytes.IndexByte(x, 0x2c)
						x[i+1] = 1
					} else {
						i := bytes.Index(x, []byte("ANMF"))
						x[i+8] = 255
					}
				case "bad-last-frame":
					if ext == "gif" {
						i := bytes.LastIndex(x, []byte{0x2c})
						x[i+10] = 0xff
					} else {
						i := bytes.LastIndex(x, []byte("VP8 "))
						x[i+8] = 0xff
					}
				}
				if _, e := inspectAvatarImage(x); e == nil {
					t.Fatal("invalid animation accepted")
				}
			})
		}
	}
	if _, e := inspectAvatarImage([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)); e == nil {
		t.Fatal("SVG accepted")
	}
	if _, e := inspectAvatarImage(testPNGWithChunk(t, "acTL", nil)); e == nil {
		t.Fatal("APNG accepted")
	}
}
func TestAvatarAnimationPreflightBounds(t *testing.T) {
	b := avatarFixture(t, "gif")
	i := bytes.IndexByte(b, 0x2c)
	frame := b[i : len(b)-1]
	prefix := b[:i]
	tooMany := append([]byte(nil), prefix...)
	for range maxAvatarFrames + 1 {
		tooMany = append(tooMany, frame...)
	}
	tooMany = append(tooMany, 0x3b)
	if e := preflightAvatarGIF(tooMany); e == nil {
		t.Fatal("frame cap missing")
	}
	x := append([]byte(nil), b...)
	binary.LittleEndian.PutUint16(x[6:8], 4096)
	binary.LittleEndian.PutUint16(x[8:10], 4096)
	if e := preflightAvatarGIF(x); e == nil {
		t.Fatal("canvas cap missing")
	}
	x = append([]byte(nil), tooMany...)
	binary.LittleEndian.PutUint16(x[6:8], 256)
	binary.LittleEndian.PutUint16(x[8:10], 256)
	if e := preflightAvatarGIF(x); e == nil {
		t.Fatal("total work cap missing")
	}
	b = avatarFixture(t, "webp")
	i = bytes.Index(b, []byte("ANMF"))
	_, _, end, e := webPChunk(b, i)
	if e != nil {
		t.Fatal(e)
	}
	frame = b[i:end]
	x = append([]byte(nil), b[:i]...)
	for range maxAvatarFrames + 1 {
		x = append(x, frame...)
	}
	binary.LittleEndian.PutUint32(x[4:8], uint32(len(x)-8))
	if _, e = preflightAvatarWebP(x); e == nil {
		t.Fatal("WebP frame cap missing")
	}
}

func TestAvatarWebPAlphaAndLossless(t *testing.T) {
	for _, name := range []string{"lossy", "lossless"} {
		t.Run(name, func(t *testing.T) {
			raw, e := os.ReadFile("testdata/avatar-alpha-" + name + ".webp")
			if e != nil {
				t.Fatal(e)
			}
			frames, e := preflightAvatarWebP(raw)
			if e != nil || len(frames) != 2 {
				t.Fatal(e)
			}
			if _, e = inspectAvatarImage(raw); e != nil {
				t.Fatal(e)
			}
			if name == "lossy" && !frames[0].alpha {
				t.Fatal("fixture must exercise ALPH+VP8")
			}
			if name == "lossless" && !bytes.Contains(frames[0].data, []byte("VP8L")) {
				t.Fatal("fixture must exercise VP8L")
			}
		})
	}
}
func TestAvatarGIFExactFrameAndWorkLimits(t *testing.T) {
	b := avatarFixture(t, "gif")
	first := bytes.IndexByte(b, 0x2c)
	second := bytes.IndexByte(b[first+1:], 0x2c) + first + 1
	// Keep the first image only (without the next graphics control extension).
	frame := b[first : second-8]
	build := func(count, w, h int) []byte {
		x := append([]byte(nil), b[:first]...)
		binary.LittleEndian.PutUint16(x[6:8], uint16(w))
		binary.LittleEndian.PutUint16(x[8:10], uint16(h))
		for range count {
			x = append(x, frame...)
		}
		return append(x, 0x3b)
	}
	for _, c := range []struct {
		count, w, h int
		ok          bool
	}{{300, 1, 1, true}, {301, 1, 1, false}, {256, 256, 256, true}, {257, 256, 256, false}} {
		e := preflightAvatarGIF(build(c.count, c.w, c.h))
		if (e == nil) != c.ok {
			t.Fatalf("%+v: %v", c, e)
		}
	}
	x := append([]byte(nil), b...)
	i := bytes.Index(x, []byte{0x21, 0xf9})
	x[i+3] = 4 << 2
	if e := preflightAvatarGIF(x); e == nil {
		t.Fatal("reserved disposal accepted")
	}
}
func TestAvatarWebPRejectContainerAmbiguityAndWork(t *testing.T) {
	b := avatarFixture(t, "webp")
	frameAt := bytes.Index(b, []byte("ANMF"))
	_, _, end, e := webPChunk(b, frameAt)
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"reserved-frame", "duplicate-header", "missing-anim", "bitstream-size", "duplicate-frame-bitstream", "work"} {
		t.Run(name, func(t *testing.T) {
			x := append([]byte(nil), b...)
			switch name {
			case "reserved-frame":
				x[frameAt+8+15] = 4
			case "duplicate-header":
				x = append(x, b[12:30]...)
			case "missing-anim":
				i := bytes.Index(x, []byte("ANIM"))
				copy(x[i:], "JUNK")
			case "bitstream-size":
				i := bytes.Index(x, []byte("VP8 "))
				x[i+8+6] = 255
			case "duplicate-frame-bitstream":
				frame := b[frameAt:end]
				sub := frame[24:]
				x = append(append(append([]byte(nil), b[:end]...), sub...), b[end:]...)
				binary.LittleEndian.PutUint32(x[frameAt+4:], uint32(len(frame)-8+len(sub)))
			case "work":
				x = append([]byte(nil), b[:frameAt]...)
				put24(x[24:27], 255)
				put24(x[27:30], 255)
				for range 257 {
					x = append(x, b[frameAt:end]...)
				}
			}
			binary.LittleEndian.PutUint32(x[4:8], uint32(len(x)-8))
			if _, e := inspectAvatarImage(x); e == nil {
				t.Fatal("malformed animation accepted")
			}
		})
	}
}
