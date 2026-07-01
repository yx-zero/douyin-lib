package douyinim

// Classify a raw IM message into a typed, presentable form. aweType mapping
// ported from the reference tool + verified live (voice = typeCode 17 with
// resource_url.uri + duration; sender_sec_uid in message field 14).

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

var (
	emojiTypes   = map[int]bool{500: true, 501: true, 507: true, 508: true, 510: true, 514: true, 516: true}
	imageTypes   = map[int]bool{2702: true, 2703: true, 2704: true}
	shareVideo   = map[int]bool{11054: true, 11055: true, 11063: true, 11066: true, 11067: true, 11069: true, 11070: true}
	shareProduct = map[int]bool{11029: true, 10500: true, 10401: true}
	shareMisc    = map[int]bool{800: true, 801: true, 803: true}
)

// contentJSON is the loosely-typed content of message field 8.
type contentJSON struct {
	AweType     *int         `json:"aweType"`
	Text        string       `json:"text"`
	Description string       `json:"description"`
	Duration    float64      `json:"duration"`
	AiAudioText string       `json:"ai_audio_text"`
	DisplayName string       `json:"display_name"`
	PushDetail  string       `json:"push_detail"`
	AwemeTitle  string       `json:"aweme_title"`
	Comment     string       `json:"comment"`
	InlinePic   string       `json:"inline_pic"` // base64 WebP thumbnail (unencrypted)
	ResourceURL *resourceURL `json:"resource_url"`
	URL         *urlList     `json:"url"`
	CoverURL    *urlList     `json:"cover_url"`
}

type resourceURL struct {
	URI           string   `json:"uri"`
	SKey          string   `json:"skey"` // AES-256-GCM key (hex) for encrypted image/video bytes
	MD5           string   `json:"md5"`
	URLList       []string `json:"url_list"`
	LargeURLList  []string `json:"large_url_list"`
	MediumURLList []string `json:"medium_url_list"`
	OriginURLList []string `json:"origin_url_list"`
	ThumbURLList  []string `json:"thumb_url_list"`
}

type urlList struct {
	URLList []string `json:"url_list"`
}

func tsFromServerID(serverID string) time.Time {
	id, err := strconv.ParseUint(serverID, 10, 64)
	if err != nil || id == 0 {
		return time.Time{}
	}
	return time.Unix(int64(id>>32), 0)
}

func firstURL(lists ...[]string) string {
	for _, l := range lists {
		if len(l) > 0 && l[0] != "" {
			return l[0]
		}
	}
	return ""
}

// stripWhitespace removes \r and \n that Douyin inserts into base64 inline_pic.
func stripWhitespace(s string) string {
	return strings.NewReplacer("\r", "", "\n", "", " ", "").Replace(s)
}

// classify turns a rawMessage into a Message. Returns ok=false for messages
// that should be skipped (recalled, or empty/no-content).
func classify(m *rawMessage, myUID string) (Message, bool) {
	if m.isRecalled != 0 {
		return Message{}, false
	}

	order := m.createdAtUs
	if order == "" {
		order = m.order
	}
	var indexInConv uint64
	if m.createdAtUs != "" {
		indexInConv, _ = strconv.ParseUint(m.createdAtUs, 10, 64)
	}
	base := Message{
		ServerID:    m.serverID,
		ConvID:      m.convID,
		TypeCode:    m.typeCode,
		SenderUID:   m.senderUID,
		SenderSecID: m.senderSecUID,
		IsMe:        m.senderUID != "" && m.senderUID == myUID,
		Timestamp:   tsFromServerID(m.serverID),
		Order:       order,
		IndexInConv: indexInConv,
		IndexV2:     m.indexV2,
		Reply:       m.ref,
		RawContent:  m.contentJSON,
	}

	var cj contentJSON
	if err := json.Unmarshal([]byte(m.contentJSON), &cj); err != nil {
		// Non-JSON content: treat as plain text if present.
		t := strings.TrimSpace(m.contentJSON)
		if t == "" {
			return Message{}, false
		}
		base.Kind = KindText
		base.Text = t
		return base, true
	}

	aweType := -1
	if cj.AweType != nil {
		aweType = *cj.AweType
	}
	base.AweType = aweType
	text := cj.Text
	if text == "" {
		text = cj.Description
	}

	// Voice: typeCode 17, OR content has resource_url + duration.
	hasVoice := m.typeCode == 17 || (cj.ResourceURL != nil && cj.Duration > 0)
	if hasVoice && cj.ResourceURL != nil {
		base.Kind = KindVoice
		base.Text = strings.TrimSpace(cj.AiAudioText) // may be overridden by live transcription
		base.VoiceURI = cj.ResourceURL.URI
		base.Duration = time.Duration(cj.Duration) * time.Millisecond
		base.MediaURL = firstURL(cj.ResourceURL.URLList)
		return base, true
	}

	if emojiTypes[aweType] {
		if sticker, err := ParseStickerContent(m.contentJSON); err == nil {
			base.Sticker = sticker
			if text == "" {
				text = sticker.DisplayName
			}
			if base.MediaURL == "" && sticker.URL != nil {
				base.MediaURL = firstURL(sticker.URL.URLList)
			}
		}
		if text == "" {
			text = cj.DisplayName
		}
		if text == "" {
			text = "[表情]"
		}
		base.Kind = KindSticker
		base.Text = text
		if base.MediaURL == "" && cj.URL != nil {
			base.MediaURL = firstURL(cj.URL.URLList)
		}
		return base, true
	}

	if imageTypes[aweType] {
		if text == "" {
			text = "[图片]"
		}
		base.Kind = KindImage
		base.Text = text
		if cj.ResourceURL != nil {
			base.MediaURL = firstURL(cj.ResourceURL.LargeURLList, cj.ResourceURL.MediumURLList,
				cj.ResourceURL.OriginURLList, cj.ResourceURL.ThumbURLList)
			base.ImageSKey = cj.ResourceURL.SKey
		}
		if cj.InlinePic != "" {
			if raw, err := base64.StdEncoding.DecodeString(stripWhitespace(cj.InlinePic)); err == nil {
				base.InlineWebP = raw
			}
		}
		return base, true
	}

	if shareVideo[aweType] {
		if text == "" {
			text = cj.PushDetail
		}
		if text == "" {
			text = "[分享]"
		}
		base.Kind = KindShare
		base.Text = text
		if cj.CoverURL != nil {
			base.MediaURL = firstURL(cj.CoverURL.URLList)
		}
		return base, true
	}

	if shareProduct[aweType] {
		switch {
		case cj.Comment != "":
			text = cj.Comment
		case text == "" && cj.PushDetail != "":
			text = cj.PushDetail
		case text == "":
			text = cj.AwemeTitle
		}
		if text == "" {
			text = "[分享]"
		}
		base.Kind = KindShare
		base.Text = text
		return base, true
	}

	if shareMisc[aweType] {
		if text == "" {
			text = "[分享]"
		}
		base.Kind = KindShare
		base.Text = text
		return base, true
	}

	if aweType >= 100000 {
		if text == "" {
			text = cj.PushDetail
		}
		if text == "" {
			text = "[系统消息]"
		}
		base.Kind = KindSystem
		base.Text = text
		return base, true
	}

	// Plain text family (700/701/703/0) or anything with text.
	if text != "" {
		base.Kind = KindText
		base.Text = text
		return base, true
	}
	if cj.PushDetail != "" {
		base.Kind = KindOther
		base.Text = cj.PushDetail
		return base, true
	}
	if cj.DisplayName != "" {
		base.Kind = KindOther
		base.Text = cj.DisplayName
		return base, true
	}
	return Message{}, false
}
