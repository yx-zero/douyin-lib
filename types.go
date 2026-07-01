package douyinim

import "time"

// Conversation is one chat in the directory, with resolved peer info and spark.
type Conversation struct {
	ConvID    string // "0:1:{peer}:{me}"
	Cursor    string // conv_short_id — mandatory pagination cursor & send field
	Ticket    string // conversation ticket (meta.f4) — required to send messages
	ConvType  int
	MsgCount  int
	PeerUID   string
	PeerSecID string
	Nickname  string
	UniqueID  string // @handle
	Spark     *Spark // friendship-flame status, nil if none
}

// SparkState is today's flame status for a conversation.
type SparkState int

const (
	// SparkRenewed: chatted today, flame is lit (火花已续上).
	SparkRenewed SparkState = 1
	// SparkNotRenewed: flame active but not renewed today, will go gray (今天还没续).
	SparkNotRenewed SparkState = 2
	// SparkToRecover: about to expire, in the recover window (待重燃/即将消失).
	SparkToRecover SparkState = 3
)

func (s SparkState) String() string {
	switch s {
	case SparkRenewed:
		return "renewed"
	case SparkNotRenewed:
		return "not_renewed"
	case SparkToRecover:
		return "to_recover"
	default:
		return "unknown"
	}
}

// Spark is the friendship-flame (火花) status for a conversation.
type Spark struct {
	Days           int        // consecutive chat days
	State          SparkState // today's status
	Level          string     // colour tier before '|': normal / blue / gray / to_recover
	Text           string     // server display text (e.g. "29" or "2 天后消失")
	RenewedToday   bool       // convenience: State == SparkRenewed
	ExpireTime     time.Time  // when the flame expires if not renewed
	CanRecoverDays int        // grace days to relight after expiry
}

// MessageKind classifies a message's content type.
type MessageKind string

const (
	KindText    MessageKind = "text"
	KindVoice   MessageKind = "voice"
	KindSticker MessageKind = "sticker"
	KindImage   MessageKind = "image"
	KindShare   MessageKind = "share"
	KindSystem  MessageKind = "system"
	KindOther   MessageKind = "other"
)

// Message is a classified, presentable message.
type Message struct {
	ServerID    string // snowflake; >>32 = unix seconds
	ConvID      string
	TypeCode    int // raw IM message type (text=7, sticker=5, voice=17, image=27, ...)
	AweType     int // content JSON aweType when present
	SenderUID   string
	SenderSecID string
	IsMe        bool
	Timestamp   time.Time
	Order       string // monotonic ordering key (created_at_us)
	IndexInConv uint64 // per-conversation index (compare to a read watermark's Index)
	IndexV2     uint64 // per-conversation ordinal (compare to ReadIndex.IndexV2)
	Kind        MessageKind
	Text        string // display text; for voice filled by transcription

	// Voice
	VoiceURI string        // douyin-user-audio-file/... uri (for transcription)
	Duration time.Duration // voice length

	// Media (image / sticker / share / voice playback)
	MediaURL string // first usable signed URL

	// Sticker (Kind == KindSticker). Preserves the raw web payload so callers can
	// inspect, render, or replay a sticker through SendSticker / SendContentJSON.
	Sticker *StickerContent

	// Image (Kind == KindImage). Encrypted images need DecryptImage / the SKey.
	ImageSKey  string // AES-256-GCM key (hex) to decrypt MediaURL bytes; empty if unencrypted
	InlineWebP []byte // small unencrypted WebP thumbnail embedded in the message, if any

	// Quoted/replied message, if any
	Reply *Reply

	RawContent string // raw content JSON, for advanced callers
}

// StickerContent is the raw content JSON shape used by Douyin web sticker
// messages (observed live for aweType 501/507). A few fields are polymorphic in
// the wild (number or string), so they stay `any` to preserve replayability.
type StickerContent struct {
	ActivityDesc            string      `json:"activity_desc"`
	ActivitySchema          string      `json:"activity_schema"`
	AuthorID                string      `json:"author_id"`
	AweType                 int         `json:"aweType"`
	DanmakuID               string      `json:"danmaku_id"`
	DanmakuText             string      `json:"danmaku_text"`
	DisplayName             string      `json:"display_name"`
	EmojiFrom               string      `json:"emoji_from"`
	EmojiSource             string      `json:"emoji_source"`
	EmojiType               string      `json:"emoji_type"`
	EnterMethod             string      `json:"enter_method"`
	FromEmojiID             string      `json:"from_emoji_id"`
	Height                  int         `json:"height"`
	HintContent             string      `json:"hint_content"`
	ID                      any         `json:"id"`
	ImageID                 any         `json:"image_id"`
	ImageType               string      `json:"image_type"`
	LightIcon               string      `json:"light_icon"`
	LightID                 string      `json:"light_id"`
	LightURL                string      `json:"light_url"`
	MatchUID                string      `json:"match_uid"`
	MessageReplyDisplayType int         `json:"message_reply_display_type"`
	PackageID               any         `json:"package_id"`
	ReplaceNickUserID       string      `json:"replace_nick_user_id"`
	ResourceType            int         `json:"resource_type"`
	ShowNotice              bool        `json:"show_notice"`
	SourceDesc              string      `json:"source_desc"`
	SourceSchema            string      `json:"source_schema"`
	StickerID               string      `json:"sticker_id"`
	StickerType             int         `json:"sticker_type"`
	TopDescReceiverV2       string      `json:"top_desc_receiver_v2"`
	TopDescReceiverV3       string      `json:"top_desc_receiver_v3"`
	TopDescSceneType        int         `json:"top_desc_scene_type"`
	TopDescSenderV2         string      `json:"top_desc_sender_v2"`
	TopDescSenderV3         string      `json:"top_desc_sender_v3"`
	TopDescUIDMatch         string      `json:"top_desc_uid_match"`
	URL                     *StickerURL `json:"url"`
	Version                 int64       `json:"version"`
	Width                   int         `json:"width"`
}

type StickerURL struct {
	URI     string   `json:"uri"`
	URLList []string `json:"url_list"`
}

// FavoriteSticker is one sticker from the web "favorite/custom stickers"
// aggregation endpoint (`custom_sticker_page_list.resources[].stickers[]`).
// PackageID is the sender-side favorites package id needed to reconstruct a
// sendable sticker content payload.
type FavoriteSticker struct {
	PackageID         int64                 `json:"package_id"`
	AnimateType       string                `json:"animate_type"`
	AnimateURL        *FavoriteStickerAsset `json:"animate_url"`
	DisplayName       string                `json:"display_name"`
	Hash              string                `json:"hash"`
	Height            int                   `json:"height"`
	ID                int64                 `json:"id"`
	IDStr             string                `json:"id_str"`
	OriginPackageID   int64                 `json:"origin_package_id"`
	StaticType        string                `json:"static_type"`
	StaticURL         *FavoriteStickerAsset `json:"static_url"`
	StickerInfoSource string                `json:"sticker_info_source"`
	StickerType       int                   `json:"sticker_type"`
	VideoID           string                `json:"video_id"`
	Width             int                   `json:"width"`
}

// FavoriteStickerAsset is the URL block used by favorite/custom sticker items.
type FavoriteStickerAsset struct {
	Height  int      `json:"height"`
	URI     string   `json:"uri"`
	URLList []string `json:"url_list"`
	Width   int      `json:"width"`
}

// Reply is a quoted message.
type Reply struct {
	Nickname string
	Content  string
}

// SendResult is returned by SendText / SendSticker / SendContentJSON.
type SendResult struct {
	ServerMessageID string // server-assigned snowflake id
	ClientMessageID string // the UUID we generated (echoed by server)
}

// EventType classifies a realtime Event.
type EventType string

const (
	// EventNewMessage: a new chat message arrived (Event.Message set).
	EventNewMessage EventType = "new_message"
	// EventReadReceipt: a conversation read watermark moved (Event.ReadReceipt set).
	EventReadReceipt EventType = "read_receipt"
	// EventConnected: the WebSocket (re)connected.
	EventConnected EventType = "connected"
	// EventDisconnected: the WebSocket dropped (Event.Err may say why; auto-reconnect follows).
	EventDisconnected EventType = "disconnected"
)

// Event is a realtime push event from the frontier WebSocket.
type Event struct {
	Type        EventType
	Message     *Message     // set for EventNewMessage
	ReadReceipt *ReadReceipt // set for EventReadReceipt
	Err         error        // set for EventDisconnected
}

// ReadReceipt is a conversation read-watermark update. A sent message is "read"
// once a participant's ReadIndex reaches/passes that message's index.
type ReadReceipt struct {
	ConvID      string
	ReaderSecID string // sec_uid of who read (compare to your own to tell self vs peer)
	IsMe        bool   // the reader is you (your own read syncing across devices)
	ReadIndex   uint64
	BadgeCount  int
}

// ParticipantRead is one participant's read watermark in a conversation, from
// ReadIndex. A message is "read" by this participant when Index >= the message's
// IndexInConv (equivalently IndexV2 >= the message's IndexV2).
type ParticipantRead struct {
	UID      string
	SecID    string
	Index    uint64 // read watermark (compare to Message.IndexInConv)
	IndexV2  uint64 // read ordinal (compare to Message.IndexV2)
	IndexMin uint64
	IsMe     bool
}
