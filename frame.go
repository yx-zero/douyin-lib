package douyinim

// Frontier WebSocket (pbbp2) connection params + frame decoding for real-time
// message push. Verified live 2026-06-29 via TLS-proxy capture.
//
// access_key is fully derivable (no SharedWorker secret):
//   access_key = MD5(fpId + appKey + deviceId + "f8a69f1719916z")
// with the IM-frontier constants below (same appKey ByteDance uses on TikTok).

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
)

const (
	wsHost           = "frontier-im.douyin.com"
	wsFpid           = "9"
	wsAppKey         = "e1bd35ec9db7b8d846de66ed140b1ad9"
	wsAppID          = "6383"
	wsDevicePlatform = "douyin_pc"
	wsVersionCode    = "360000"
	wsAccessSuffix   = "f8a69f1719916z"
)

// wsAccessKey derives the frontier access_key from the numeric uid (device_id).
func wsAccessKey(uid string) string {
	sum := md5.Sum([]byte(wsFpid + wsAppKey + uid + wsAccessSuffix))
	return hex.EncodeToString(sum[:])
}

// buildWSURL builds the frontier-im WebSocket URL for the given uid.
func buildWSURL(uid string) string {
	return "wss://" + wsHost + "/ws/v2" +
		"?aid=" + wsAppID +
		"&fpid=" + wsFpid +
		"&device_id=" + uid +
		"&access_key=" + wsAccessKey(uid) +
		"&device_platform=" + wsDevicePlatform +
		"&version_code=" + wsVersionCode
}

// pushFrame is a decoded pbbp2 Frame envelope.
//
//	f1=seqid, f2=logid, f3=service, f4=method, f5[]=headers(kv),
//	f6=payload_encoding, f7=payload_type("pb"), f8=payload
type pushFrame struct {
	headers     map[string]string
	payloadType string
	payload     []byte
}

// parseFrame decodes the pbbp2 Frame envelope.
func parseFrame(b []byte) pushFrame {
	fr := pushFrame{headers: map[string]string{}}
	for _, f := range readFields(b) {
		switch f.num {
		case 5: // repeated header {1:key, 2:val}
			if f.wire == 2 {
				kv := readFields(f.bytes)
				if k := fieldString(kv, 1); k != "" {
					fr.headers[k] = fieldString(kv, 2)
				}
			}
		case 7:
			fr.payloadType = string(f.bytes)
		case 8:
			fr.payload = f.bytes
		}
	}
	return fr
}

// pushPayload holds the fields we extract from a Frame's IM payload. The payload
// nests the message a few levels deep and exact field numbers vary by command,
// so we walk it leniently and pattern-match the distinctive leaves (all ASCII,
// so robust): conversation_id, the content JSON, and the sender sec_uid.
type pushPayload struct {
	convID       string
	contentJSON  string
	senderSecUID string
	serverID     string // best-effort snowflake
	ext          map[string]string
}

// extractPush walks a Frame payload and pulls out the message leaves.
func extractPush(payload []byte) pushPayload {
	pp := pushPayload{ext: map[string]string{}}
	var walk func(b []byte, depth int)
	walk = func(b []byte, depth int) {
		if depth > 10 {
			return
		}
		for _, f := range readFields(b) {
			switch f.wire {
			case 0:
				// candidate snowflake server id (recent: ~7.6e18)
				if pp.serverID == "" && f.val > 7_000_000_000_000_000_000 && f.val < 8_500_000_000_000_000_000 {
					pp.serverID = u64s(f.val)
				}
			case 2:
				s := f.bytes
				str := string(s)
				// ext kv: {1:"x:key", 2:"val"} where key contains ':'
				if isExtKV(s) {
					kv := readFields(s)
					k := fieldString(kv, 1)
					pp.ext[k] = fieldString(kv, 2)
					continue
				}
				if pp.convID == "" && looksConvID(str) {
					pp.convID = str
					continue
				}
				if pp.senderSecUID == "" && looksSecUID(str) {
					pp.senderSecUID = str
					continue
				}
				if pp.contentJSON == "" && len(str) > 1 && str[0] == '{' && json.Valid(s) {
					pp.contentJSON = str
					continue
				}
				// recurse into nested protobuf, but never into JSON/text leaves
				if maybeNested(s) {
					walk(s, depth+1)
				}
			}
		}
	}
	walk(payload, 0)
	return pp
}

func u64s(v uint64) string {
	const d = "0123456789"
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = d[v%10]
		v /= 10
	}
	return string(buf[i:])
}

// looksConvID matches a conversation id like "0:1:{peer}:{me}".
func looksConvID(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	colons := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ':' {
			colons++
		} else if c < '0' || c > '9' {
			return false
		}
	}
	return colons == 3
}

// looksSecUID matches a sec_uid like "MS4wLjABAAAA...".
func looksSecUID(s string) bool {
	if len(s) < 40 || len(s) > 100 || s[:4] != "MS4w" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// isExtKV reports whether bytes decode as a {1:string-with-colon, 2:string} pair.
func isExtKV(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	fs := readFields(b)
	if len(fs) < 1 || len(fs) > 2 {
		return false
	}
	k := fieldString(fs, 1)
	if k == "" || len(k) > 40 {
		return false
	}
	hasColon := false
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c == ':' {
			hasColon = true
		} else if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_') {
			return false
		}
	}
	return hasColon
}

// maybeNested reports whether bytes parse cleanly as a non-trivial protobuf
// message (used to decide whether to recurse). Conservative: requires the parse
// to consume the whole buffer with valid wire types.
func maybeNested(b []byte) bool {
	if len(b) < 2 {
		return false
	}
	r := newReader(b)
	fields := 0
	for !r.eof() {
		start := r.pos
		tag := r.uvarint()
		num := int(tag >> 3)
		wire := int(tag & 7)
		if num == 0 || num > 6000 || wire == 3 || wire == 4 || wire == 6 || wire == 7 {
			return false
		}
		switch wire {
		case 0:
			r.uvarint()
		case 2:
			n := int(r.uvarint())
			if r.pos+n > len(b) {
				return false
			}
			r.pos += n
		case 1:
			if r.pos+8 > len(b) {
				return false
			}
			r.pos += 8
		case 5:
			if r.pos+4 > len(b) {
				return false
			}
			r.pos += 4
		}
		if r.pos <= start {
			return false
		}
		fields++
	}
	return fields > 0
}
