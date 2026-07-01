package douyinim

// Real-time message push over the frontier WebSocket. Connect, derive the
// access_key, keep alive with "hi" heartbeats, decode pbbp2 frames into Events,
// and auto-reconnect with backoff. Pure protocol — no browser, no SharedWorker.

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Realtime is a live event stream. Create with Client.Realtime, consume
// Events(), and Close() when done. Events() is closed when the stream ends;
// check Err() afterwards for the terminal error (nil on clean Close).
type Realtime struct {
	client *Client
	events chan Event

	cancel    context.CancelFunc
	closeOnce sync.Once

	mu   sync.Mutex
	err  error
	seen map[string]bool // dedup by frontier msg id
}

// RealtimeOption configures a Realtime stream.
type RealtimeOption func(*realtimeConfig)

type realtimeConfig struct {
	reconnect bool
}

// WithReconnect toggles auto-reconnect on drop (default true).
func WithReconnect(on bool) RealtimeOption {
	return func(c *realtimeConfig) { c.reconnect = on }
}

// Realtime opens a live frontier WebSocket and streams push Events. It resolves
// your identity first (one HTTP call if not already cached), derives the
// access_key, and runs read + heartbeat loops in the background.
func (c *Client) Realtime(ctx context.Context, opts ...RealtimeOption) (*Realtime, error) {
	cfg := realtimeConfig{reconnect: true}
	for _, o := range opts {
		o(&cfg)
	}
	// Ensure identity (uid for device_id/access_key, sec_uid for self-filtering).
	if c.myUID == "" || c.mySecUID == "" {
		if _, err := c.Conversations(ctx, false); err != nil {
			return nil, err
		}
	}
	if c.myUID == "" {
		return nil, errStatus("realtime: could not resolve uid", 0)
	}

	rctx, cancel := context.WithCancel(ctx)
	rt := &Realtime{
		client: c,
		events: make(chan Event, 64),
		cancel: cancel,
		seen:   map[string]bool{},
	}
	go rt.run(rctx, cfg)
	return rt, nil
}

// Events returns the event channel. It is closed when the stream terminates.
func (rt *Realtime) Events() <-chan Event { return rt.events }

// Close stops the stream and closes the connection.
func (rt *Realtime) Close() error {
	rt.closeOnce.Do(func() { rt.cancel() })
	return nil
}

// Err returns the terminal error after Events() is closed (nil if Close()d).
func (rt *Realtime) Err() error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.err
}

func (rt *Realtime) setErr(err error) {
	rt.mu.Lock()
	rt.err = err
	rt.mu.Unlock()
}

func (rt *Realtime) emit(ctx context.Context, ev Event) {
	select {
	case rt.events <- ev:
	case <-ctx.Done():
	}
}

func (rt *Realtime) run(ctx context.Context, cfg realtimeConfig) {
	defer close(rt.events)
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := rt.connectOnce(ctx)
		if ctx.Err() != nil {
			return // clean Close()
		}
		rt.setErr(err)
		rt.emit(ctx, Event{Type: EventDisconnected, Err: err})
		if !cfg.reconnect {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (rt *Realtime) connectOnce(ctx context.Context) error {
	// Reuse the client's transport (so WithDoH resolution applies), but with no
	// timeout — the handshake client must not kill the long-lived connection.
	wsHTTP := &http.Client{}
	if rt.client.http != nil && rt.client.http.Transport != nil {
		wsHTTP.Transport = rt.client.http.Transport
	}
	conn, _, err := websocket.Dial(ctx, buildWSURL(rt.client.myUID), &websocket.DialOptions{
		HTTPClient: wsHTTP,
		HTTPHeader: http.Header{
			"Origin":     {"https://www.douyin.com"},
			"Cookie":     {cookieHeader(rt.client.cookies, wsHost)},
			"User-Agent": {rt.client.ua},
		},
		Subprotocols: []string{"binary", "base64", "pbbp2"},
	})
	if err != nil {
		return err
	}
	conn.SetReadLimit(32 << 20)
	defer conn.Close(websocket.StatusNormalClosure, "")

	rt.emit(ctx, Event{Type: EventConnected})

	// Heartbeat: the server advertises ping-interval=30; send "hi" every 15s.
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				if err := conn.Write(hbCtx, websocket.MessageText, []byte("hi")); err != nil {
					return
				}
			}
		}
	}()

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if typ == websocket.MessageText {
			continue // "hi" heartbeat echo
		}
		rt.handleFrame(ctx, data)
	}
}

// handleFrame decodes one pbbp2 binary frame and emits the resulting Event(s).
func (rt *Realtime) handleFrame(ctx context.Context, data []byte) {
	fr := parseFrame(data)
	if id := fr.headers["x_frontier_msg_id"]; id != "" {
		if rt.seen[id] {
			return
		}
		rt.seen[id] = true
	}
	if fr.payload == nil {
		return
	}
	pp := extractPush(fr.payload)

	// Recall (撤回): signalled purely via the ext map (content JSON is "{}"), so
	// check it before the content-based branches below. The server pushes several
	// frames per recall; only the one carrying the target message id is useful.
	if pp.ext["s:is_recalled"] == "true" {
		target := pp.ext["s:target_server_message_id"]
		if target == "" {
			return
		}
		recallUID := pp.ext["s:recall_uid"]
		rt.emit(ctx, Event{Type: EventRecall, Recall: &Recall{
			ConvID:                pp.convID,
			TargetServerMessageID: target,
			TargetClientMessageID: pp.ext["s:target_client_message_id"],
			RecallUID:             recallUID,
			IsMe:                  recallUID != "" && recallUID == rt.client.myUID,
		}})
		return
	}

	if pp.contentJSON == "" {
		return
	}

	// Parse content to decide message vs. read-receipt/state update.
	var cj struct {
		Text           string `json:"text"`
		CommandType    *int   `json:"command_type"`
		ConversationID string `json:"conversation_id"`
		ReadIndex      uint64 `json:"read_index"`
		ReadBadgeCount int    `json:"read_badge_count"`
	}
	_ = json.Unmarshal([]byte(pp.contentJSON), &cj)

	// Read watermark update (read receipt / badge).
	if cj.ReadIndex != 0 || cj.CommandType != nil {
		convID := pp.convID
		if convID == "" {
			convID = cj.ConversationID
		}
		rt.emit(ctx, Event{Type: EventReadReceipt, ReadReceipt: &ReadReceipt{
			ConvID:      convID,
			ReaderSecID: pp.senderSecUID,
			IsMe:        pp.senderSecUID != "" && pp.senderSecUID == rt.client.mySecUID,
			ReadIndex:   cj.ReadIndex,
			BadgeCount:  cj.ReadBadgeCount,
		}})
		return
	}

	// New chat message: reuse classify on the content.
	rm := &rawMessage{
		convID:       pp.convID,
		serverID:     pp.serverID,
		senderSecUID: pp.senderSecUID,
		contentJSON:  pp.contentJSON,
	}
	m, ok := classify(rm, rt.client.myUID)
	if !ok {
		return
	}
	// IsMe via sec_uid (numeric uid isn't in the push payload).
	if pp.senderSecUID != "" {
		m.IsMe = pp.senderSecUID == rt.client.mySecUID
	}
	if m.Timestamp.IsZero() {
		if t := pp.ext["s:server_message_create_time"]; t != "" {
			if ms := atoiSafe(t); ms > 0 {
				m.Timestamp = time.UnixMilli(ms)
			}
		}
		if m.Timestamp.IsZero() {
			m.Timestamp = time.Now()
		}
	}
	rt.emit(ctx, Event{Type: EventNewMessage, Message: &m})
}

func atoiSafe(s string) int64 {
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
