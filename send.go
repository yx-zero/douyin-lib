package douyinim

// Send a text direct message via imapi /v1/message/send. Pure protocol,
// cookie-only — verified 2026-06-28 that NO a_bogus / msToken /
// identity_security_token is required; the server returns f3=0 f4="OK" plus a
// server_message_id and echoes our client_message_id.
//
// The envelope mirrors get_by_conversation; the send body (f8 → f100) carries
// the conversation id, content JSON, the conversation ticket (read from the
// directory), a fresh client_message_id, and an stime.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	sendMessageTypeSticker = 5
	sendMessageTypeText    = 7
)

// ParseStickerContent decodes a raw sticker content JSON blob captured from
// history / realtime / the web client. It preserves polymorphic numeric fields
// as json.Number so the content can be replayed without losing precision.
func ParseStickerContent(raw string) (*StickerContent, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var sticker StickerContent
	if err := dec.Decode(&sticker); err != nil {
		return nil, err
	}
	return &sticker, nil
}

// StickerContentFromMessage extracts a replayable sticker payload from a
// classified Message.
func StickerContentFromMessage(msg *Message) (*StickerContent, error) {
	if msg == nil {
		return nil, fmt.Errorf("nil message")
	}
	if msg.Kind != KindSticker {
		return nil, fmt.Errorf("message %s is not a sticker", msg.ServerID)
	}
	if msg.RawContent == "" {
		return nil, fmt.Errorf("message %s has empty raw content", msg.ServerID)
	}
	return ParseStickerContent(msg.RawContent)
}

// SendText sends a plain text message to a conversation.
func (c *Client) SendText(ctx context.Context, conv *Conversation, text string) (*SendResult, error) {
	content, err := json.Marshal(map[string]any{
		"aweType":       700,
		"type":          0,
		"richTextInfos": []any{},
		"text":          text,
	})
	if err != nil {
		return nil, err
	}
	return c.SendContentJSON(ctx, conv, sendMessageTypeText, string(content))
}

// SendSticker sends a sticker payload through the same /v1/message/send path the
// web client uses. The easiest way to get a valid StickerContent is to parse it
// from an existing sticker Message via StickerContentFromMessage.
func (c *Client) SendSticker(ctx context.Context, conv *Conversation, sticker *StickerContent) (*SendResult, error) {
	if sticker == nil {
		return nil, fmt.Errorf("nil sticker")
	}
	content, err := json.Marshal(sticker)
	if err != nil {
		return nil, err
	}
	return c.SendContentJSON(ctx, conv, sendMessageTypeSticker, string(content))
}

// SendFavoriteSticker sends a sticker fetched from FavoriteStickers.
func (c *Client) SendFavoriteSticker(ctx context.Context, conv *Conversation, sticker FavoriteSticker) (*SendResult, error) {
	return c.SendSticker(ctx, conv, sticker.StickerContent())
}

// SendContentJSON sends an arbitrary JSON content body with an explicit IM
// message type. This is the generic primitive under SendText / SendSticker.
func (c *Client) SendContentJSON(ctx context.Context, conv *Conversation, messageType int, contentJSON string) (*SendResult, error) {
	if conv.Cursor == "" || conv.Ticket == "" {
		return nil, fmt.Errorf("conversation missing cursor/ticket; fetch it via Conversations() or FindConversation()")
	}
	if messageType <= 0 {
		return nil, fmt.Errorf("invalid message type %d", messageType)
	}
	cursor, err := strconv.ParseUint(conv.Cursor, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("bad cursor %q: %w", conv.Cursor, err)
	}
	contentJSON, err = compactJSON(contentJSON)
	if err != nil {
		return nil, fmt.Errorf("invalid content JSON: %w", err)
	}

	cmid := uuid4()
	stime := strconv.FormatFloat(float64(time.Now().UnixMilli())+0.5, 'f', 4, 64)

	body := buildSendRequest(sendParams{
		convID:    conv.ConvID,
		cursor:    cursor,
		msgType:   uint64(messageType),
		content:   contentJSON,
		ticket:    conv.Ticket,
		clientMID: cmid,
		stime:     stime,
		ua:        c.ua,
	})

	resp, status, err := c.postProtobuf(ctx, urlSend, body)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, errStatus("message/send", status)
	}
	return parseSendResponse(resp, cmid)
}

type sendParams struct {
	convID    string
	cursor    uint64
	msgType   uint64
	content   string
	ticket    string
	clientMID string
	stime     string
	ua        string
}

func buildSendRequest(p sendParams) []byte {
	ext := func(k, v string) []byte {
		return (&pbWriter{}).stringField(1, k).stringField(2, v).finish()
	}
	sendBody := (&pbWriter{}).
		stringField(1, p.convID).
		varintField(2, 1).
		varintField(3, p.cursor).
		stringField(4, p.content).
		bytesField(5, ext("s:mentioned_users", "")).
		bytesField(5, ext("s:client_message_id", p.clientMID)).
		bytesField(5, ext("s:stime", p.stime)).
		varintField(6, p.msgType).
		stringField(7, p.ticket).
		stringField(8, p.clientMID).
		finish()
	f8 := (&pbWriter{}).bytesField(100, sendBody).finish()

	w := (&pbWriter{}).
		varintField(1, 100).
		varintField(2, 10037).
		stringField(3, "0.1.6").
		stringField(4, "").
		varintField(5, 3).
		varintField(6, 0).
		stringField(7, "fef1a80:p/lzg/store").
		bytesField(8, f8).
		stringField(9, "0").
		stringField(11, "douyin_pc").
		stringField(14, "360000")

	meta := [][2]string{
		{"session_aid", "6383"}, {"session_did", "0"}, {"app_name", "douyin_pc"},
		{"priority_region", "cn"}, {"user_agent", p.ua}, {"cookie_enabled", "true"},
		{"browser_language", "en-US"}, {"browser_platform", "Win32"}, {"browser_name", "Mozilla"},
		{"browser_version", "5.0 (Windows NT 10.0; Win64; x64)"}, {"browser_online", "true"},
		{"screen_width", "1920"}, {"screen_height", "1080"}, {"referer", ""},
		{"timezone_name", "Asia/Shanghai"}, {"deviceId", "0"}, {"is-retry", "0"},
	}
	for _, kv := range meta {
		w.bytesField(15, (&pbWriter{}).stringField(1, kv[0]).stringField(2, kv[1]).finish())
	}
	w.varintField(18, 1).stringField(21, "douyin_web").stringField(22, "web_sdk")
	return w.finish()
}

// parseSendResponse decodes the send response: f3=status, f4=msg, f6→f100 body.
func parseSendResponse(data []byte, sentCMID string) (*SendResult, error) {
	top := readFields(data)
	if status, ok := fieldVarint(top, 3); ok && status != 0 {
		msg := fieldString(top, 4)
		return nil, fmt.Errorf("send failed: status=%d msg=%q", status, msg)
	}
	res := &SendResult{ClientMessageID: sentCMID}
	if rb := fieldBytes(top, 6); rb != nil {
		if sb := fieldBytes(readFields(rb), 100); sb != nil {
			sf := readFields(sb)
			if v, ok := fieldVarint(sf, 1); ok {
				res.ServerMessageID = strconv.FormatUint(v, 10)
			}
			if echo := fieldString(sf, 4); echo != "" {
				res.ClientMessageID = echo
			}
		}
	}
	return res, nil
}

func compactJSON(raw string) (string, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(raw)); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// uuid4 generates a random RFC-4122 v4 UUID string.
func uuid4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
