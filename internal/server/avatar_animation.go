package server

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image/gif"

	"golang.org/x/image/webp"
)

const maxAvatarFrames = 300
const maxAvatarAnimationPixels int64 = 16 * 1024 * 1024

func avatarDimensions(w, h int) bool {
	return w > 0 && h > 0 && w <= maxImageDimension && h <= maxImageDimension && int64(w)*int64(h) <= maxImagePixels
}

// Avatar-only opt-in: icons, emoji and banners retain their static-image contract.
// Container preflight bounds all allocations before full decoding every frame.
// The original container is stored, never the reconstructed validation frames.
func inspectAvatarImage(raw []byte) (assetImageFormat, error) {
	if len(raw) == 0 || len(raw) > maxImageBytes {
		return assetImageFormat{}, errors.New("avatar must be at most 256 KiB")
	}
	if bytes.HasPrefix(raw, []byte("GIF87a")) || bytes.HasPrefix(raw, []byte("GIF89a")) {
		if err := preflightAvatarGIF(raw); err != nil {
			return assetImageFormat{}, err
		}
		return withAssetImageDecodeSlot(assetImageDecodeSlots, func() (assetImageFormat, error) {
			_, err := gif.DecodeAll(bytes.NewReader(raw))
			if err != nil {
				return assetImageFormat{}, fmt.Errorf("invalid GIF frame: %w", err)
			}
			f, _ := assetFormatForDecoder("gif")
			return f, nil
		})
	}
	if len(raw) >= 30 && string(raw[:4]) == "RIFF" && string(raw[8:16]) == "WEBPVP8X" && raw[20]&2 != 0 {
		frames, err := preflightAvatarWebP(raw)
		if err != nil {
			return assetImageFormat{}, err
		}
		return withAssetImageDecodeSlot(assetImageDecodeSlots, func() (assetImageFormat, error) {
			for _, f := range frames {
				body := make([]byte, 30+len(f.data))
				copy(body, "RIFF")
				binary.LittleEndian.PutUint32(body[4:8], uint32(len(body)-8))
				copy(body[8:], "WEBPVP8X")
				binary.LittleEndian.PutUint32(body[16:20], 10)
				if f.alpha {
					body[20] = 16
				}
				put24(body[24:27], f.width-1)
				put24(body[27:30], f.height-1)
				copy(body[30:], f.data)
				cfg, e := webp.DecodeConfig(bytes.NewReader(body))
				if e != nil || cfg.Width != f.width || cfg.Height != f.height {
					return assetImageFormat{}, errors.New("WebP frame bitstream dimensions disagree")
				}
				img, e := webp.Decode(bytes.NewReader(body))
				if e != nil || img.Bounds().Dx() != f.width || img.Bounds().Dy() != f.height {
					return assetImageFormat{}, errors.New("invalid WebP frame bitstream")
				}
			}
			f, _ := assetFormatForDecoder("webp")
			return f, nil
		})
	}
	return inspectAssetImage(raw)
}
func decodeAvatarImage(encoded string) ([]byte, string, error) {
	if len(encoded) > base64.StdEncoding.EncodedLen(maxImageBytes) {
		return nil, "", errors.New("avatar too large (max 256 KiB)")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", errors.New("invalid base64 avatar")
	}
	f, err := inspectAvatarImage(raw)
	if err != nil {
		return nil, "", fmt.Errorf("invalid avatar: %w", err)
	}
	return raw, f.extension, nil
}
func validateImageForDirectory(raw []byte, extension, dir string) error {
	if dir != "avatars" {
		return validateAssetImage(raw, extension)
	}
	f, err := inspectAvatarImage(raw)
	if err != nil {
		return err
	}
	if f.extension != extension {
		return errors.New("avatar content does not match extension")
	}
	return nil
}
func preflightAvatarGIF(raw []byte) error {
	bad := errors.New("invalid or excessive GIF animation (max 300 frames / 16 megapixels)")
	if len(raw) < 13 {
		return bad
	}
	w, h := int(binary.LittleEndian.Uint16(raw[6:8])), int(binary.LittleEndian.Uint16(raw[8:10]))
	if !avatarDimensions(w, h) {
		return bad
	}
	pos := 13
	if raw[10]&128 != 0 {
		pos += 3 * (1 << ((raw[10] & 7) + 1))
	}
	frames := 0
	for pos < len(raw) {
		switch raw[pos] {
		case 0x3b:
			if pos != len(raw)-1 || frames == 0 {
				return bad
			}
			return nil
		case 0x21:
			if len(raw)-pos < 2 {
				return bad
			}
			if raw[pos+1] == 0xf9 {
				if len(raw)-pos < 8 || raw[pos+2] != 4 || raw[pos+7] != 0 || raw[pos+3]&0xe0 != 0 || (raw[pos+3]>>2)&7 > 3 {
					return bad
				}
			}
			var err error
			pos, err = skipGIFSubBlocks(raw, pos+2)
			if err != nil {
				return bad
			}
		case 0x2c:
			if len(raw)-pos < 10 {
				return bad
			}
			frames++
			if frames > maxAvatarFrames || int64(w)*int64(h)*int64(frames) > maxAvatarAnimationPixels {
				return bad
			}
			x, y := int(binary.LittleEndian.Uint16(raw[pos+1:])), int(binary.LittleEndian.Uint16(raw[pos+3:]))
			fw, fh := int(binary.LittleEndian.Uint16(raw[pos+5:])), int(binary.LittleEndian.Uint16(raw[pos+7:]))
			if !avatarDimensions(fw, fh) || x+fw > w || y+fh > h || raw[pos+9]&0x18 != 0 {
				return bad
			}
			packed := raw[pos+9]
			pos += 10
			if packed&128 != 0 {
				pos += 3 * (1 << ((packed & 7) + 1))
			}
			if pos >= len(raw) {
				return bad
			}
			pos++
			var err error
			pos, err = skipGIFSubBlocks(raw, pos)
			if err != nil {
				return bad
			}
		default:
			return bad
		}
	}
	return bad
}

type avatarWebPFrame struct {
	data          []byte
	width, height int
	alpha         bool
}

func uint24(b []byte) int   { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 }
func put24(b []byte, n int) { b[0] = byte(n); b[1] = byte(n >> 8); b[2] = byte(n >> 16) }
func webPChunk(raw []byte, pos int) (string, []byte, int, error) {
	if pos > len(raw)-8 {
		return "", nil, 0, errors.New("truncated WebP chunk")
	}
	n := uint64(binary.LittleEndian.Uint32(raw[pos+4:]))
	end := uint64(pos) + 8 + n
	next := end + n%2
	if next > uint64(len(raw)) || (n%2 != 0 && raw[int(end)] != 0) {
		return "", nil, 0, errors.New("invalid WebP chunk length/padding")
	}
	return string(raw[pos : pos+4]), raw[pos+8 : int(end)], int(next), nil
}

// WebP RIFF/ANMF layout: https://developers.google.com/speed/webp/docs/riff_container
func preflightAvatarWebP(raw []byte) ([]avatarWebPFrame, error) {
	bad := errors.New("invalid or excessive WebP animation (max 300 frames / 16 megapixels)")
	if len(raw) < 30 || uint64(binary.LittleEndian.Uint32(raw[4:8]))+8 != uint64(len(raw)) {
		return nil, bad
	}
	name, header, pos, err := webPChunk(raw, 12)
	if err != nil || name != "VP8X" || len(header) != 10 || header[0]&2 == 0 || header[0]&0xc1 != 0 || header[1] != 0 || header[2] != 0 || header[3] != 0 {
		return nil, bad
	}
	w, h := uint24(header[4:])+1, uint24(header[7:])+1
	if !avatarDimensions(w, h) {
		return nil, bad
	}
	var frames []avatarWebPFrame
	anim := false
	icc := false
	for pos < len(raw) {
		name, data, next, e := webPChunk(raw, pos)
		if e != nil {
			return nil, e
		}
		pos = next
		switch name {
		case "VP8X", "VP8 ", "VP8L", "ALPH":
			return nil, bad
		case "ICCP":
			if icc || anim || len(frames) > 0 {
				return nil, bad
			}
			icc = true
		case "ANIM":
			if anim || len(frames) > 0 || len(data) != 6 {
				return nil, bad
			}
			anim = true
		case "ANMF":
			if !anim || len(data) < 16 || data[15]&0xfc != 0 || len(frames) >= maxAvatarFrames {
				return nil, bad
			}
			x, y, fw, fh := 2*uint24(data), 2*uint24(data[3:]), uint24(data[6:])+1, uint24(data[9:])+1
			if !avatarDimensions(fw, fh) || x+fw > w || y+fh > h || int64(w)*int64(h)*int64(len(frames)+1) > maxAvatarAnimationPixels {
				return nil, bad
			}
			sub := data[16:]
			at := 0
			alpha, imageSeen := false, false
			for at < len(sub) {
				typ, _, after, e := webPChunk(sub, at)
				if e != nil {
					return nil, e
				}
				at = after
				switch typ {
				case "ALPH":
					if alpha || imageSeen {
						return nil, bad
					}
					alpha = true
				case "VP8 ":
					if imageSeen {
						return nil, bad
					}
					imageSeen = true
				case "VP8L":
					if imageSeen || alpha {
						return nil, bad
					}
					imageSeen = true
				default:
					return nil, bad
				}
			}
			if !imageSeen {
				return nil, bad
			}
			frames = append(frames, avatarWebPFrame{sub, fw, fh, alpha})
		}
	}
	if !anim || len(frames) == 0 {
		return nil, bad
	}
	return frames, nil
}
