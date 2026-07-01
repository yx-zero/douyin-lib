package douyinim

// Encrypted IM image (and image-typed video) handling.
//
// Douyin DM images at tplv-x-get:*.image URLs are AES-256-GCM encrypted:
//   key = hex_decode(resource_url.skey)   (32 bytes, AES-256)
//   iv  = ciphertext[:12]                 (12-byte GCM nonce, prepended)
//   ct  = ciphertext[12:]                 (ciphertext + 16-byte GCM tag)
// The decrypted bytes are a normal image (JPEG/PNG/WebP/HEIC) or, for some
// aweType 2702/2703/2704 messages, a video — detect by magic bytes.
// Verified live 2026-06-29. No salt, no header, no special request header;
// decryption is purely client-side from skey.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
)

// DecryptImage decrypts AES-256-GCM image/video bytes using the message's
// ImageSKey (hex). Returns the plaintext (a real image or video file).
func DecryptImage(encrypted []byte, skeyHex string) ([]byte, error) {
	if skeyHex == "" {
		return nil, fmt.Errorf("empty skey: image is not encrypted, use the bytes as-is")
	}
	key, err := hex.DecodeString(skeyHex)
	if err != nil {
		return nil, fmt.Errorf("bad skey hex: %w", err)
	}
	if len(encrypted) < 12+16 {
		return nil, fmt.Errorf("ciphertext too short (%d bytes)", len(encrypted))
	}
	block, err := aes.NewCipher(key) // AES-256 (32-byte key)
	if err != nil {
		return nil, fmt.Errorf("aes init: %w", err)
	}
	gcm, err := cipher.NewGCM(block) // 12-byte nonce, 16-byte tag
	if err != nil {
		return nil, fmt.Errorf("gcm init: %w", err)
	}
	nonce := encrypted[:12]
	body := encrypted[12:]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("gcm decrypt (wrong skey or corrupt data): %w", err)
	}
	return plain, nil
}

// MediaFormat is the detected file type of decrypted media.
type MediaFormat struct {
	Ext     string // ".jpg" / ".png" / ".webp" / ".gif" / ".heic" / ".mp4" / ".bin"
	IsVideo bool
}

// DetectMediaFormat inspects magic bytes of decrypted media.
func DetectMediaFormat(b []byte) MediaFormat {
	switch {
	case len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff:
		return MediaFormat{Ext: ".jpg"}
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return MediaFormat{Ext: ".png"}
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return MediaFormat{Ext: ".webp"}
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return MediaFormat{Ext: ".gif"}
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		brand := string(b[8:12])
		switch brand {
		case "heic", "heix", "hevc", "mif1", "msf1":
			return MediaFormat{Ext: ".heic"}
		default:
			return MediaFormat{Ext: ".mp4", IsVideo: true}
		}
	default:
		return MediaFormat{Ext: ".bin"}
	}
}

// DownloadImage fetches an image message's encrypted bytes and decrypts them,
// returning the plaintext media plus its detected format. If the message has no
// SKey (unencrypted), the downloaded bytes are returned as-is.
func (c *Client) DownloadImage(ctx context.Context, m *Message) ([]byte, MediaFormat, error) {
	if m.MediaURL == "" {
		// Fall back to the embedded thumbnail if there's no remote URL.
		if len(m.InlineWebP) > 0 {
			return m.InlineWebP, DetectMediaFormat(m.InlineWebP), nil
		}
		return nil, MediaFormat{}, fmt.Errorf("message has no image URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.MediaURL, nil)
	if err != nil {
		return nil, MediaFormat{}, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Referer", "https://www.douyin.com/")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, MediaFormat{}, err
	}
	defer resp.Body.Close()
	enc, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, MediaFormat{}, err
	}
	if resp.StatusCode != 200 {
		return nil, MediaFormat{}, fmt.Errorf("image download HTTP %d", resp.StatusCode)
	}

	if m.ImageSKey == "" {
		// Not encrypted (rare): return as-is.
		return enc, DetectMediaFormat(enc), nil
	}
	plain, err := DecryptImage(enc, m.ImageSKey)
	if err != nil {
		return nil, MediaFormat{}, err
	}
	return plain, DetectMediaFormat(plain), nil
}
