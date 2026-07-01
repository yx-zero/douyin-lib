package douyinim

// IM video (typeCode 30) download + decryption.
//
// Douyin IM videos are delivered as CENC-encrypted MP4s (scheme "cenc",
// AES-CTR). The flow, all cookie-authenticated and pure-protocol (no a_bogus):
//   1. From the message content, take video.vid + video.skey.
//   2. POST /aweme/v1/web/maya/story/batch_play_info/v1/ with tos_key="vid-"+vid
//      to get a playable CDN main_url.
//   3. GET main_url → the CENC-encrypted MP4 (clear container, encrypted samples).
//   4. Decrypt each track's samples with AES-CTR using skey (16-byte key) and the
//      per-sample IVs from the senc box, then rewrite the sample-entry fourcc
//      (encv→hvc1/avc1, enca→mp4a) so the result is a plain, playable MP4.
// Verified live 2026-07-01 (frame decoded correctly via ffmpeg).

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const urlBatchPlayInfo = "https://www.douyin.com/aweme/v1/web/maya/story/batch_play_info/v1/"

// DownloadVideo fetches an IM video message, decrypts it, and returns a plain,
// playable MP4. The message must be Kind == KindVideo (carrying VideoID +
// VideoSKey). Pure-protocol: cookie-only, no a_bogus.
func (c *Client) DownloadVideo(ctx context.Context, m *Message) ([]byte, error) {
	if m.VideoID == "" {
		return nil, fmt.Errorf("message has no video id")
	}
	playURL, err := c.videoPlayURL(ctx, m.VideoID)
	if err != nil {
		return nil, err
	}
	enc, err := c.downloadURL(ctx, playURL)
	if err != nil {
		return nil, err
	}
	if m.VideoSKey == "" {
		// Not encrypted (unexpected for IM, but handle gracefully).
		return enc, nil
	}
	key, err := hex.DecodeString(m.VideoSKey)
	if err != nil {
		return nil, fmt.Errorf("bad video skey: %w", err)
	}
	if err := decryptCENC(enc, key); err != nil {
		return nil, fmt.Errorf("cenc decrypt: %w", err)
	}
	return enc, nil
}

// videoPlayURL resolves a vid to a playable CDN URL via batch_play_info.
func (c *Client) videoPlayURL(ctx context.Context, vid string) (string, error) {
	tosKey := vid
	if !strings.HasPrefix(tosKey, "vid-") {
		tosKey = "vid-" + vid
	}
	body, _ := json.Marshal(map[string]any{
		"req_infos":    []map[string]any{{"item_id": 0, "tos_key": tosKey, "type": 2}},
		"with_caption": true,
	})
	// batch_play_info requires app_name=douyin_web; without it the server returns
	// err_no=5 "参数不合法" (verified 2026-07-01).
	q := c.webParams()
	q.Set("app_name", "douyin_web")
	endpoint := urlBatchPlayInfo + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Origin", "https://www.douyin.com")
	req.Header.Set("Referer", "https://www.douyin.com/chat")
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("x-secsdk-csrf-token", "DOWNGRADE")
	req.Header.Set("Cookie", cookieHeader(c.cookies, wwwHost))

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", errStatus("batch_play_info", resp.StatusCode)
	}
	var parsed struct {
		ErrNo int `json:"err_no"`
		Data  struct {
			PlayInfos []struct {
				EncryptedURL struct {
					MainURL   string `json:"main_url"`
					BackupURL string `json:"backup_url"`
				} `json:"encrypted_url"`
			} `json:"play_infos"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	if parsed.ErrNo != 0 || len(parsed.Data.PlayInfos) == 0 {
		return "", fmt.Errorf("batch_play_info: err_no=%d, no play info", parsed.ErrNo)
	}
	u := parsed.Data.PlayInfos[0].EncryptedURL.MainURL
	if u == "" {
		u = parsed.Data.PlayInfos[0].EncryptedURL.BackupURL
	}
	if u == "" {
		return "", fmt.Errorf("batch_play_info: no url")
	}
	return u, nil
}

// downloadURL GETs a media URL with the standard headers.
func (c *Client) downloadURL(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Referer", "https://www.douyin.com/")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("video download HTTP %d", resp.StatusCode)
	}
	return data, nil
}

// --- CENC (scheme "cenc", AES-CTR) MP4 decryption ---

type mp4Box struct {
	typ   string
	start int
	hdr   int
	end   int
}

func mp4Parse(data []byte, off, end int) []mp4Box {
	var out []mp4Box
	for off+8 <= end {
		size := int(binary.BigEndian.Uint32(data[off : off+4]))
		typ := string(data[off+4 : off+8])
		hdr := 8
		bsize := size
		if size == 1 {
			if off+16 > end {
				break
			}
			bsize = int(binary.BigEndian.Uint64(data[off+8 : off+16]))
			hdr = 16
		} else if size == 0 {
			bsize = end - off
		}
		if bsize < hdr || off+bsize > end {
			break
		}
		out = append(out, mp4Box{typ: typ, start: off, hdr: hdr, end: off + bsize})
		off += bsize
	}
	return out
}

// mp4Find finds the first box matching path, descending through containers.
func mp4Find(data []byte, off, end int, path ...string) *mp4Box {
	for _, b := range mp4Parse(data, off, end) {
		if b.typ == path[0] {
			if len(path) == 1 {
				bb := b
				return &bb
			}
			cs := b.start + b.hdr
			switch b.typ {
			case "stsd":
				cs += 8
			case "encv":
				cs += 78
			case "enca":
				cs += 28
			}
			return mp4Find(data, cs, b.end, path[1:]...)
		}
	}
	return nil
}

type cencSample struct {
	iv         []byte
	subsamples [][2]int // {clearBytes, protectedBytes}
}

func parseSencBox(p []byte, ivSize int) []cencSample {
	if len(p) < 8 {
		return nil
	}
	flags := binary.BigEndian.Uint32(p[0:4]) & 0xffffff
	count := int(binary.BigEndian.Uint32(p[4:8]))
	i := 8
	var out []cencSample
	for s := 0; s < count && i+ivSize <= len(p); s++ {
		var se cencSample
		se.iv = append([]byte{}, p[i:i+ivSize]...)
		i += ivSize
		if flags&0x2 != 0 {
			if i+2 > len(p) {
				break
			}
			sub := int(binary.BigEndian.Uint16(p[i : i+2]))
			i += 2
			for k := 0; k < sub && i+6 <= len(p); k++ {
				clear := int(binary.BigEndian.Uint16(p[i : i+2]))
				prot := int(binary.BigEndian.Uint32(p[i+2 : i+6]))
				se.subsamples = append(se.subsamples, [2]int{clear, prot})
				i += 6
			}
		}
		out = append(out, se)
	}
	return out
}

func parseStszBox(p []byte) []int {
	if len(p) < 12 {
		return nil
	}
	sampleSize := binary.BigEndian.Uint32(p[4:8])
	count := int(binary.BigEndian.Uint32(p[8:12]))
	out := make([]int, count)
	if sampleSize != 0 {
		for i := range out {
			out[i] = int(sampleSize)
		}
		return out
	}
	for i := 0; i < count && 16+i*4 <= len(p); i++ {
		out[i] = int(binary.BigEndian.Uint32(p[12+i*4 : 16+i*4]))
	}
	return out
}

func parseStcoBox(p []byte, is64 bool) []int {
	if len(p) < 8 {
		return nil
	}
	count := int(binary.BigEndian.Uint32(p[4:8]))
	out := make([]int, count)
	if is64 {
		for i := 0; i < count && 16+i*8 <= len(p); i++ {
			out[i] = int(binary.BigEndian.Uint64(p[8+i*8 : 16+i*8]))
		}
	} else {
		for i := 0; i < count && 12+i*4 <= len(p); i++ {
			out[i] = int(binary.BigEndian.Uint32(p[8+i*4 : 12+i*4]))
		}
	}
	return out
}

func parseStscBox(p []byte) [][2]int {
	if len(p) < 8 {
		return nil
	}
	count := int(binary.BigEndian.Uint32(p[4:8]))
	out := make([][2]int, 0, count)
	for i := 0; i < count && 16+i*12 <= len(p); i++ {
		fc := int(binary.BigEndian.Uint32(p[8+i*12 : 12+i*12]))
		spc := int(binary.BigEndian.Uint32(p[12+i*12 : 16+i*12]))
		out = append(out, [2]int{fc, spc})
	}
	return out
}

// cencSampleOffsets expands stsc/stco/stsz into per-sample absolute offsets.
func cencSampleOffsets(sizes, chunkOffsets []int, stsc [][2]int) []int {
	numChunks := len(chunkOffsets)
	spc := make([]int, numChunks)
	for i := 0; i < len(stsc); i++ {
		start := stsc[i][0] - 1
		n := stsc[i][1]
		endChunk := numChunks
		if i+1 < len(stsc) {
			endChunk = stsc[i+1][0] - 1
		}
		for cc := start; cc < endChunk && cc < numChunks; cc++ {
			spc[cc] = n
		}
	}
	var offsets []int
	idx := 0
	for cc := 0; cc < numChunks; cc++ {
		off := chunkOffsets[cc]
		for s := 0; s < spc[cc] && idx < len(sizes); s++ {
			offsets = append(offsets, off)
			off += sizes[idx]
			idx++
		}
	}
	return offsets
}

// decryptCENC decrypts a scheme-cenc MP4 in place and rewrites sample-entry
// fourccs so the result plays as clear media.
func decryptCENC(data, key []byte) error {
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	moov := mp4Find(data, 0, len(data), "moov")
	if moov == nil {
		return fmt.Errorf("no moov box")
	}
	patched := false
	for _, b := range mp4Parse(data, moov.start+moov.hdr, moov.end) {
		if b.typ != "trak" {
			continue
		}
		mdia := mp4Find(data, b.start+b.hdr, b.end, "mdia")
		if mdia == nil {
			continue
		}
		if decryptTrak(data, block, mdia.start+mdia.hdr, mdia.end) {
			patched = true
		}
	}
	if !patched {
		return fmt.Errorf("no encrypted track found")
	}
	// Rewrite sample-entry fourcc: encv→orig video codec, enca→mp4a.
	patchSampleEntry(data, moov)
	return nil
}

func decryptTrak(data []byte, block cipher.Block, mdiaStart, mdiaEnd int) bool {
	stbl := mp4Find(data, mdiaStart, mdiaEnd, "minf", "stbl")
	if stbl == nil {
		return false
	}
	bs, be := stbl.start+stbl.hdr, stbl.end
	sencB := mp4Find(data, bs, be, "senc")
	stszB := mp4Find(data, bs, be, "stsz")
	stscB := mp4Find(data, bs, be, "stsc")
	if sencB == nil || stszB == nil || stscB == nil {
		return false
	}
	stcoB := mp4Find(data, bs, be, "stco")
	is64 := false
	if stcoB == nil {
		stcoB = mp4Find(data, bs, be, "co64")
		is64 = true
	}
	if stcoB == nil {
		return false
	}
	sizes := parseStszBox(data[stszB.start+stszB.hdr : stszB.end])
	chunks := parseStcoBox(data[stcoB.start+stcoB.hdr:stcoB.end], is64)
	stsc := parseStscBox(data[stscB.start+stscB.hdr : stscB.end])
	sencs := parseSencBox(data[sencB.start+sencB.hdr:sencB.end], 8)
	offsets := cencSampleOffsets(sizes, chunks, stsc)

	n := len(sencs)
	if len(offsets) < n {
		n = len(offsets)
	}
	for s := 0; s < n; s++ {
		se := sencs[s]
		iv := make([]byte, 16)
		copy(iv, se.iv) // 8-byte per-sample IV + 8 zero bytes = AES-CTR counter
		ctr := cipher.NewCTR(block, iv)
		off, size := offsets[s], sizes[s]
		if off+size > len(data) {
			continue
		}
		if len(se.subsamples) == 0 {
			ctr.XORKeyStream(data[off:off+size], data[off:off+size])
			continue
		}
		pos := off
		for _, ss := range se.subsamples {
			pos += ss[0] // clear bytes untouched
			if ss[1] > 0 && pos+ss[1] <= len(data) {
				ctr.XORKeyStream(data[pos:pos+ss[1]], data[pos:pos+ss[1]])
			}
			pos += ss[1]
		}
	}
	return true
}

// patchSampleEntry restores encrypted sample-entry fourccs to their original
// format (from the frma box), so players treat the samples as clear.
func patchSampleEntry(data []byte, moov *mp4Box) {
	for _, b := range mp4Parse(data, moov.start+moov.hdr, moov.end) {
		if b.typ != "trak" {
			continue
		}
		stsd := mp4Find(data, b.start+b.hdr, b.end, "mdia", "minf", "stbl", "stsd")
		if stsd == nil {
			continue
		}
		for _, entry := range mp4Parse(data, stsd.start+stsd.hdr+8, stsd.end) {
			if entry.typ != "encv" && entry.typ != "enca" {
				continue
			}
			frmaStart := entry.start + entry.hdr
			if entry.typ == "encv" {
				frmaStart += 78
			} else {
				frmaStart += 28
			}
			frma := mp4Find(data, frmaStart, entry.end, "sinf", "frma")
			if frma == nil || frma.end-(frma.start+frma.hdr) < 4 {
				continue
			}
			orig := data[frma.start+frma.hdr : frma.start+frma.hdr+4]
			copy(data[entry.start+4:entry.start+8], orig)
		}
	}
}
