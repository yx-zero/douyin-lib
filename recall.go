package douyinim

// Recall ("撤回") a message you sent, via imapi /v1/message/recall.
// Wire schema reverse-engineered from social.js (im_proto.RecallMessageRequestBody
// + IMCMD.RECALL_MESSAGE=702): cmd=702, body wraps RecallMessageRequestBody at
// RequestBody field 702: conversation_id(1) string, conversation_short_id(2)
// int64 (=cursor), conversation_type(3) int32, server_message_id(4) int64.
// Response toast lives in ResponseBody field 702 (recall_message_body.toast);
// success is status(f3)==0.

import (
	"context"
	"fmt"
	"strconv"
)

const urlRecall = "https://imapi.douyin.com/v1/message/recall"

// RecallMessage recalls ("撤回") a message you sent. Douyin only allows
// recalling your own recent messages; the server enforces the time window and
// returns a non-zero status (surfaced as an error) if it's no longer allowed.
func (c *Client) RecallMessage(ctx context.Context, conv *Conversation, msg *Message) error {
	if conv == nil || msg == nil {
		return fmt.Errorf("nil conversation or message")
	}
	if msg.ConvID != "" && msg.ConvID != conv.ConvID {
		return fmt.Errorf("message %s belongs to %s, not %s", msg.ServerID, msg.ConvID, conv.ConvID)
	}
	return c.RecallMessageByID(ctx, conv, msg.ServerID)
}

// RecallMessageByID recalls a message by its server_message_id (e.g. from
// SendResult.ServerMessageID or Message.ServerID).
func (c *Client) RecallMessageByID(ctx context.Context, conv *Conversation, serverMessageID string) error {
	if conv == nil {
		return fmt.Errorf("nil conversation")
	}
	cursor, err := strconv.ParseUint(conv.Cursor, 10, 64)
	if err != nil {
		return fmt.Errorf("bad cursor %q: %w", conv.Cursor, err)
	}
	sid, err := strconv.ParseUint(serverMessageID, 10, 64)
	if err != nil {
		return fmt.Errorf("bad server_message_id %q: %w", serverMessageID, err)
	}
	body := buildRecallRequest(conv.ConvID, cursor, markReadConversationType(conv.ConvID), sid, c.ua)
	resp, status, err := c.postProtobuf(ctx, urlRecall, body)
	if err != nil {
		return err
	}
	if status != 200 {
		return errStatus("message/recall", status)
	}
	return parseStatusResponse("message/recall", resp)
}

func buildRecallRequest(convID string, cursor, convType, serverMessageID uint64, ua string) []byte {
	recallBody := (&pbWriter{}).
		stringField(1, convID).
		varintField(2, cursor).
		varintField(3, convType).
		varintField(4, serverMessageID).
		finish()
	f8 := (&pbWriter{}).bytesField(702, recallBody).finish()

	w := (&pbWriter{}).
		varintField(1, 702).
		varintField(2, 10024).
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
		{"priority_region", "cn"}, {"user_agent", ua}, {"cookie_enabled", "true"},
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
