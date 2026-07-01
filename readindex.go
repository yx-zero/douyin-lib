package douyinim

// Read receipts ("已读"): how far each participant has read a conversation.
// Pure protocol via imapi v3/conversation/get_read_index (cmd 2000). The peer's
// watermark vs. a message's index tells you whether they've read it.
// Verified live 2026-06-29 (works for all conversations, unlike the flaky
// batch_get_conversation_participants_readindex / cmd 2038).

import (
	"context"
	"strconv"
)

const urlReadIndex = "https://imapi.douyin.com/v3/conversation/get_read_index"

// buildReadIndexRequest builds a get_read_index body for one conversation.
// Envelope mirrors get_by_conversation; inner body (field 2000) =
// { f1=conversation_short_id, f2=1, f3=conversation_id }.
func buildReadIndexRequest(convID string, cursor uint64) []byte {
	inner := (&pbWriter{}).
		varintField(1, cursor).
		varintField(2, 1).
		stringField(3, convID).
		finish()
	f8 := (&pbWriter{}).bytesField(2000, inner).finish()

	w := (&pbWriter{}).
		varintField(1, 2000).
		varintField(2, 10027).
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
		{"priority_region", "cn"}, {"user_agent", initUA}, {"cookie_enabled", "true"},
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

// parseReadIndexResponse decodes the cmd-2000 response: f6 → field 2000 →
// repeated ParticipantReadIndex { f1=user_id, f2=sec_uid, f3=index, f4=index_v2, f5=index_min }.
func parseReadIndexResponse(data []byte, myUID string) []ParticipantRead {
	f6 := fieldBytes(readFields(data), 6)
	if f6 == nil {
		return nil
	}
	body := fieldBytes(readFields(f6), 2000)
	if body == nil {
		return nil
	}
	var out []ParticipantRead
	// Each ParticipantReadIndex is a length-delimited sub-message (repeated f1).
	for _, f := range readFields(body) {
		if f.wire != 2 {
			continue
		}
		sub := readFields(f.bytes)
		uid, ok := fieldVarint(sub, 1)
		if !ok {
			continue
		}
		idx, ok := fieldVarint(sub, 3)
		if !ok {
			continue
		}
		v2, _ := fieldVarint(sub, 4)
		mn, _ := fieldVarint(sub, 5)
		uidStr := strconv.FormatUint(uid, 10)
		out = append(out, ParticipantRead{
			UID:      uidStr,
			SecID:    fieldString(sub, 2),
			Index:    idx,
			IndexV2:  v2,
			IndexMin: mn,
			IsMe:     uidStr == myUID,
		})
	}
	return out
}

// ReadIndex returns each participant's read watermark for a conversation
// (including your own). Use it to tell whether the peer has read your messages.
func (c *Client) ReadIndex(ctx context.Context, conv *Conversation) ([]ParticipantRead, error) {
	cursor, err := strconv.ParseUint(conv.Cursor, 10, 64)
	if err != nil {
		return nil, err
	}
	// ensure identity so IsMe is meaningful
	if c.myUID == "" {
		if _, e := c.Conversations(ctx, false); e != nil {
			return nil, e
		}
	}
	body, status, err := c.postProtobuf(ctx, urlReadIndex, buildReadIndexRequest(conv.ConvID, cursor))
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, errStatus("get_read_index", status)
	}
	return parseReadIndexResponse(body, c.myUID), nil
}

// PeerRead returns the peer participant's read watermark for a conversation
// (the one that isn't you). Returns nil if not found.
func (c *Client) PeerRead(ctx context.Context, conv *Conversation) (*ParticipantRead, error) {
	reads, err := c.ReadIndex(ctx, conv)
	if err != nil {
		return nil, err
	}
	for i := range reads {
		if !reads[i].IsMe {
			return &reads[i], nil
		}
	}
	return nil, nil
}

// WasRead reports whether the peer has read a given message (peer's read
// watermark has reached/passed the message's index). ok=false means the peer's
// read state couldn't be determined (no peer watermark returned).
func (c *Client) WasRead(ctx context.Context, conv *Conversation, msg *Message) (read bool, ok bool, err error) {
	peer, err := c.PeerRead(ctx, conv)
	if err != nil {
		return false, false, err
	}
	if peer == nil {
		return false, false, nil
	}
	// Prefer index_v2 (compact ordinal); fall back to the big index.
	if msg.IndexV2 > 0 && peer.IndexV2 > 0 {
		return peer.IndexV2 >= msg.IndexV2, true, nil
	}
	if msg.IndexInConv > 0 && peer.Index > 0 {
		return peer.Index >= msg.IndexInConv, true, nil
	}
	return false, false, nil
}
