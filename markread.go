package douyinim

// Mark conversation read via imapi v3/conversation/mark_read (cmd 2002).
// Verified from Burp-captured Douyin web traffic and the in-page JS bundle.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const urlMarkRead = "https://imapi.douyin.com/v3/conversation/mark_read"

// MarkReadMessage advances your read watermark to the given message.
func (c *Client) MarkReadMessage(ctx context.Context, conv *Conversation, msg *Message) error {
	if conv == nil || msg == nil {
		return fmt.Errorf("nil conversation or message")
	}
	if msg.ConvID != "" && msg.ConvID != conv.ConvID {
		return fmt.Errorf("message %s belongs to %s, not %s", msg.ServerID, msg.ConvID, conv.ConvID)
	}
	if msg.IndexInConv == 0 || msg.IndexV2 == 0 {
		return fmt.Errorf("message %s missing conversation indices", msg.ServerID)
	}
	return c.MarkReadByIndex(ctx, conv, msg.IndexInConv, msg.IndexV2)
}

// MarkReadByIndex advances your read watermark to the supplied conversation
// indices. `readIndex` is the big per-conversation index; `readIndexV2` is the
// compact ordinal used by read receipts.
func (c *Client) MarkReadByIndex(ctx context.Context, conv *Conversation, readIndex, readIndexV2 uint64) error {
	if conv == nil {
		return fmt.Errorf("nil conversation")
	}
	cursor, err := strconv.ParseUint(conv.Cursor, 10, 64)
	if err != nil {
		return fmt.Errorf("bad cursor %q: %w", conv.Cursor, err)
	}
	body := buildMarkReadRequest(conv.ConvID, cursor, markReadConversationType(conv.ConvID), readIndex, readIndexV2)
	resp, status, err := c.postProtobuf(ctx, urlMarkRead, body)
	if err != nil {
		return err
	}
	if status != 200 {
		return errStatus("mark_read", status)
	}
	return parseStatusResponse("mark_read", resp)
}

// MarkReadToEnd advances your read watermark to the latest visible message in
// the conversation.
func (c *Client) MarkReadToEnd(ctx context.Context, conv *Conversation) error {
	if conv == nil {
		return fmt.Errorf("nil conversation")
	}
	msgs, err := c.GetMessages(ctx, conv, MessageOptions{Range: RangeLast, Count: 1})
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return nil
	}
	return c.MarkReadMessage(ctx, conv, &msgs[len(msgs)-1])
}

func buildMarkReadRequest(convID string, cursor uint64, convType uint64, readIndex uint64, readIndexV2 uint64) []byte {
	// Web captures always include one zeroed mute badge entry for message_type 50.
	mute := (&pbWriter{}).varintField(1, 50).varintField(2, 0).finish()
	inner := (&pbWriter{}).
		stringField(1, convID).
		varintField(2, cursor).
		varintField(3, convType).
		varintField(4, readIndex).
		varintField(5, 0). // conv_unread_count
		varintField(6, 0). // total_unread_count
		varintField(7, readIndexV2).
		varintField(8, 0). // read_badge_count
		bytesField(11, mute).
		finish()
	f8 := (&pbWriter{}).bytesField(604, inner).finish()

	w := (&pbWriter{}).
		varintField(1, 2002).
		varintField(2, 10007).
		stringField(3, "0.1.6").
		stringField(4, "").
		varintField(5, 3).
		varintField(6, 1).
		stringField(7, "fef1a80:p/lzg/store").
		bytesField(8, f8).
		stringField(9, "0").
		stringField(11, "douyin_pc").
		stringField(14, "360000")

	meta := [][2]string{
		{"session_aid", "6383"}, {"session_did", "0"}, {"app_name", "douyin_pc"},
		{"priority_region", "cn"}, {"user_agent", initUA}, {"cookie_enabled", "true"},
		{"browser_language", "en-US"}, {"browser_platform", "Win32"}, {"browser_name", "Mozilla"},
		{"browser_version", "5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"},
		{"browser_online", "true"}, {"screen_width", "1920"}, {"screen_height", "1080"},
		{"referer", ""}, {"timezone_name", "Asia/Shanghai"}, {"deviceId", "0"}, {"is-retry", "0"},
	}
	for _, kv := range meta {
		w.bytesField(15, (&pbWriter{}).stringField(1, kv[0]).stringField(2, kv[1]).finish())
	}
	w.varintField(18, 1).stringField(21, "douyin_web").stringField(22, "web_sdk")
	return w.finish()
}

func markReadConversationType(convID string) uint64 {
	parts := strings.Split(convID, ":")
	if len(parts) >= 2 {
		if n, err := strconv.ParseUint(parts[1], 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

func parseStatusResponse(op string, data []byte) error {
	top := readFields(data)
	if status, ok := fieldVarint(top, 3); ok && status != 0 {
		msg := fieldString(top, 4)
		return fmt.Errorf("%s failed: status=%d msg=%q", op, status, msg)
	}
	return nil
}
