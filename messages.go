package douyinim

// get_by_conversation — paginated message history (protobuf), plus classification
// of each message into text/voice/image/sticker/share/system. Wire format ported
// from the in-page JS, verified live.

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"
)

const (
	newestTS = uint64(9999999999999999) // anchor: start from the newest message
	pageSize = 50
)

// buildByConvRequest builds a get_by_conversation request body.
//
//	convID: "0:1:{peer}:{me}"
//	cursor: conv_short_id (mandatory; 0 → empty)
//	ts:     paging anchor; newestTS for latest, then response.nextTs
func buildByConvRequest(convID string, cursor, ts uint64) []byte {
	inner := (&pbWriter{}).
		stringField(1, convID).
		varintField(2, 1).
		varintField(3, cursor).
		varintField(4, 1).
		varintField(5, ts).
		varintField(6, pageSize).
		finish()
	query := (&pbWriter{}).bytesField(301, inner).finish()

	return (&pbWriter{}).
		varintField(1, 301).
		varintField(2, 10027).
		stringField(3, "0.1.6").
		stringField(4, "").
		varintField(5, 3).
		varintField(6, 0).
		stringField(7, "fef1a80:p/lzg/store").
		bytesField(8, query).
		stringField(9, "0").
		stringField(11, "douyin_pc").
		stringField(14, "360000").
		varintField(18, 1).
		stringField(21, "douyin_pc").
		finish()
}

// rawMessage is the decoded-but-unclassified message.
type rawMessage struct {
	convID       string
	serverID     string
	createdAtUs  string // f4 — index_in_conversation (read-watermark-comparable)
	order        string // f5
	typeCode     int
	senderUID    string
	senderSecUID string
	contentJSON  string
	isRecalled   int
	indexV2      uint64 // f17 — per-conversation ordinal (compare to read index_v2)
	ref          *Reply
}

// parseByConvResponse decodes a get_by_conversation response: f6 → f301 → f1[].
func parseByConvResponse(data []byte) (msgs []rawMessage, nextTs uint64, hasMore bool) {
	f6 := fieldBytes(readFields(data), 6)
	if f6 == nil {
		return
	}
	f301 := fieldBytes(readFields(f6), 301)
	if f301 == nil {
		return
	}
	for _, f := range readFields(f301) {
		switch {
		case f.num == 2 && f.wire == 0:
			nextTs = f.val
		case f.num == 3 && f.wire == 0:
			hasMore = f.val == 1
		case f.num == 1 && f.wire == 2:
			msgs = append(msgs, parseMessage(f.bytes))
		}
	}
	return
}

func parseMessage(buf []byte) rawMessage {
	m := rawMessage{}
	for _, f := range readFields(buf) {
		if f.num == 0 || f.num > 500 {
			break
		}
		switch {
		case f.wire == 0:
			switch f.num {
			case 3:
				m.serverID = strconv.FormatUint(f.val, 10)
			case 4:
				m.createdAtUs = strconv.FormatUint(f.val, 10)
			case 5:
				m.order = strconv.FormatUint(f.val, 10)
			case 6:
				m.typeCode = int(f.val)
			case 7:
				m.senderUID = strconv.FormatUint(f.val, 10)
			case 11:
				m.isRecalled = int(f.val)
			case 17:
				m.indexV2 = f.val
			}
		case f.wire == 2:
			switch f.num {
			case 1:
				m.convID = string(f.bytes)
			case 8:
				m.contentJSON = string(f.bytes)
			case 14:
				m.senderSecUID = string(f.bytes)
			case 18:
				m.ref = parseRef(f.bytes)
			}
		}
	}
	return m
}

func parseRef(buf []byte) *Reply {
	fields := readFields(buf)
	jsonStr := fieldString(fields, 2)
	if jsonStr == "" {
		return nil
	}
	var j struct {
		Content       string `json:"content"`
		RefmsgContent string `json:"refmsg_content"`
		Nickname      string `json:"nickname"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &j); err != nil {
		return nil
	}
	content := j.Content
	if content == "" {
		content = j.RefmsgContent
	}
	if content == "" && j.Nickname == "" {
		return nil
	}
	return &Reply{Nickname: j.Nickname, Content: content}
}

// fetchPage fetches one page of a conversation's messages.
func (c *Client) fetchPage(ctx context.Context, convID string, cursor, ts uint64) (msgs []rawMessage, nextTs uint64, hasMore bool, status int, err error) {
	body, status, err := c.postProtobuf(ctx, urlByConv, buildByConvRequest(convID, cursor, ts))
	if err != nil {
		return nil, 0, false, status, err
	}
	if status != 200 {
		return nil, 0, false, status, nil
	}
	msgs, nextTs, hasMore = parseByConvResponse(body)
	return msgs, nextTs, hasMore, 200, nil
}

// fetchRaw pages backward collecting raw messages. max=0 → full history.
func (c *Client) fetchRaw(ctx context.Context, convID string, cursor uint64, max int) ([]rawMessage, error) {
	var all []rawMessage
	ts := newestTS
	consecErr := 0
	for {
		msgs, nextTs, hasMore, status, err := c.fetchPage(ctx, convID, cursor, ts)
		if err != nil || status != 200 {
			consecErr++
			if consecErr >= 3 {
				if err != nil {
					return nil, err
				}
				return nil, errStatus("get_by_conversation", status)
			}
			sleep(ctx, 1500*time.Millisecond)
			continue
		}
		consecErr = 0
		if len(msgs) == 0 {
			break
		}
		all = append(all, msgs...)
		if max > 0 && len(all) >= max {
			break
		}
		if !hasMore || nextTs == 0 {
			break
		}
		ts = nextTs
		sleep(ctx, 120*time.Millisecond)
	}
	return all, nil
}

// MessageRange selects which slice of history to return.
type MessageRange string

const (
	RangeLast  MessageRange = "last"  // newest N
	RangeFirst MessageRange = "first" // oldest N
	RangeAll   MessageRange = "all"   // everything
)

// MessageOptions controls GetMessages.
type MessageOptions struct {
	Range           MessageRange // default RangeLast
	Count           int          // N for last/first (ignored for all); default 50
	TranscribeVoice bool         // transcribe voice messages via Douyin ASR
}

// GetMessages fetches, classifies, windows, transcribes, and chronologically
// sorts a conversation's messages.
func (c *Client) GetMessages(ctx context.Context, conv *Conversation, opts MessageOptions) ([]Message, error) {
	if opts.Range == "" {
		opts.Range = RangeLast
	}
	if opts.Count == 0 {
		opts.Count = 50
	}
	cursor, err := strconv.ParseUint(conv.Cursor, 10, 64)
	if err != nil {
		return nil, err
	}
	max := 0
	if opts.Range == RangeLast && opts.Count > 0 {
		max = opts.Count * 2 // over-fetch; recalled/empty get filtered
	}

	raw, err := c.fetchRaw(ctx, conv.ConvID, cursor, max)
	if err != nil {
		return nil, err
	}

	msgs := make([]Message, 0, len(raw))
	for i := range raw {
		if m, ok := classify(&raw[i], c.myUID); ok {
			msgs = append(msgs, m)
		}
	}
	sort.SliceStable(msgs, func(i, j int) bool {
		return cmpBig(msgs[i].Order, msgs[j].Order) < 0 ||
			(cmpBig(msgs[i].Order, msgs[j].Order) == 0 && cmpBig(msgs[i].ServerID, msgs[j].ServerID) < 0)
	})

	switch opts.Range {
	case RangeLast:
		if opts.Count > 0 && len(msgs) > opts.Count {
			msgs = msgs[len(msgs)-opts.Count:]
		}
	case RangeFirst:
		if opts.Count > 0 && len(msgs) > opts.Count {
			msgs = msgs[:opts.Count]
		}
	}

	if opts.TranscribeVoice {
		if err := c.transcribeInPlace(ctx, msgs, conv.Cursor); err != nil {
			// non-fatal: return messages without transcription
			return msgs, nil
		}
	}
	return msgs, nil
}

func cmpBig(a, b string) int {
	x, _ := strconv.ParseUint(a, 10, 64)
	y, _ := strconv.ParseUint(b, 10, 64)
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	default:
		return 0
	}
}
