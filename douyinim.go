// Package douyinim is a pure-protocol Go client for Douyin (抖音) web direct
// messages (私信). It talks to Douyin's web HTTP APIs directly using your login
// cookies — no browser, no WebSocket, no JS signing. It can list conversations,
// read history (text/image/voice/sticker/share), transcribe voice to text,
// check 火花 (friendship-spark) status, and send text messages.
//
// All endpoints are cookie-authenticated. Cookies expire; refresh cookies.txt
// when calls start failing with non-200 / non-zero status.
package douyinim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	imapiHost   = "imapi.douyin.com"
	wwwHost     = "www.douyin.com"
	urlInit     = "https://imapi.douyin.com/v1/message/get_message_by_init"
	urlByConv   = "https://imapi.douyin.com/v1/message/get_by_conversation"
	urlSend     = "https://imapi.douyin.com/v1/message/send"
	urlUserInfo = "https://www.douyin.com/aweme/v1/web/im/user/info/"
	urlRecog    = "https://www.douyin.com/aweme/v1/web/im/message/audio/recognition/"

	defaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"
)

// Client is a Douyin web IM client. Construct with New or NewFromFile.
// It is safe for sequential use; the directory cache is not goroutine-safe.
type Client struct {
	cookies []Cookie
	http    *http.Client
	ua      string
	webid   string

	// identity (resolved lazily from im/user/info owner_sec_uid)
	myUID    string
	mySecUID string

	// directory cache
	dir    []Conversation
	dirAt  time.Time
	dirTTL time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets a custom *http.Client (e.g. for proxy / TLS settings).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.ua = ua } }

// WithDirectoryTTL sets how long the conversation directory is cached
// (default 5 minutes). Use 0 to disable caching.
func WithDirectoryTTL(d time.Duration) Option { return func(c *Client) { c.dirTTL = d } }

// New creates a Client from already-parsed cookies.
func New(cookies []Cookie, opts ...Option) *Client {
	c := &Client{
		cookies: cookies,
		http:    &http.Client{Timeout: 30 * time.Second},
		ua:      defaultUA,
		dirTTL:  5 * time.Minute,
	}
	c.webid = getCookie(cookies, "douyin.com", "webid")
	if c.webid == "" {
		c.webid = "0"
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// NewFromFile creates a Client from a Netscape cookies.txt file.
func NewFromFile(cookiesPath string, opts ...Option) (*Client, error) {
	cookies, err := LoadCookies(cookiesPath)
	if err != nil {
		return nil, err
	}
	return New(cookies, opts...), nil
}

// webParams returns the standard douyin web query params shared by aweme/* endpoints.
func (c *Client) webParams() url.Values {
	return url.Values{
		"device_platform": {"webapp"},
		"aid":             {"6383"},
		"channel":         {"channel_pc_web"},
		"pc_client_type":  {"1"},
		"version_code":    {"170400"},
		"version_name":    {"17.4.0"},
		"cookie_enabled":  {"true"},
		"platform":        {"PC"},
		"webid":           {c.webid},
	}
}

// postProtobuf POSTs a protobuf body to an imapi endpoint and returns the raw response.
func (c *Client) postProtobuf(ctx context.Context, endpoint string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Accept", "application/x-protobuf")
	req.Header.Set("Origin", "https://www.douyin.com")
	req.Header.Set("Referer", "https://www.douyin.com/")
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Cookie", cookieHeader(c.cookies, imapiHost))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// sleep is a context-aware delay used for pacing.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// postForm POSTs a urlencoded form to a www.douyin.com aweme endpoint.
func (c *Client) postForm(ctx context.Context, endpoint, form string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(form)))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Origin", "https://www.douyin.com")
	req.Header.Set("Referer", "https://www.douyin.com/chat")
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Cookie", cookieHeader(c.cookies, wwwHost))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// postJSON POSTs a JSON body to a www.douyin.com aweme endpoint (voice recognition).
func (c *Client) postJSON(ctx context.Context, endpoint string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Origin", "https://www.douyin.com")
	req.Header.Set("Referer", "https://www.douyin.com/chat")
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("x-secsdk-csrf-token", "DOWNGRADE")
	req.Header.Set("Cookie", cookieHeader(c.cookies, wwwHost))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

func anyToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case json.Number:
		return x.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}

// MyUID returns the caller's own numeric uid (resolved after the first
// Conversations / directory call). Empty until then.
func (c *Client) MyUID() string { return c.myUID }

// Conversations returns the live conversation directory with resolved peer
// nicknames and 火花 (spark) status. Results are cached for the configured TTL
// (default 5m); pass refresh=true to force a fresh fetch.
func (c *Client) Conversations(ctx context.Context, refresh bool) ([]Conversation, error) {
	if !refresh && c.dir != nil && (c.dirTTL == 0 || time.Since(c.dirAt) < c.dirTTL) {
		return c.dir, nil
	}

	body, status, err := c.postProtobuf(ctx, urlInit, buildInitRequest())
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, errStatus("get_message_by_init", status)
	}
	raws := parseInitResponse(body)

	// Resolve every participant's sec_uid → profile (identifies us via
	// owner_sec_uid and labels the peer). This also returns the server time
	// used to pick the active 火花 flame window.
	secSet := map[string]struct{}{}
	for _, rc := range raws {
		for _, p := range rc.parts {
			if p.secID != "" {
				secSet[p.secID] = struct{}{}
			}
		}
	}
	secUIDs := make([]string, 0, len(secSet))
	for s := range secSet {
		secUIDs = append(secUIDs, s)
	}

	users, ownerSecUID, now, err := c.resolveUsers(ctx, secUIDs)
	if err == nil {
		if prof, ok := users[ownerSecUID]; ok && prof.uid != "" {
			c.myUID = prof.uid
		}
		c.mySecUID = ownerSecUID
	}
	if now.IsZero() {
		now = time.Now()
	}

	out := make([]Conversation, 0, len(raws))
	for _, rc := range raws {
		peer := c.pickPeer(rc.parts)
		prof := users[peer.secID]
		out = append(out, Conversation{
			ConvID:    rc.convID,
			Cursor:    rc.cursor,
			Ticket:    rc.ticket,
			ConvType:  rc.convType,
			MsgCount:  rc.msgCount,
			PeerUID:   peer.uid,
			PeerSecID: peer.secID,
			Nickname:  prof.nickname,
			UniqueID:  prof.uniqueID,
			Spark:     buildSpark(rc.sparkChat, rc.sparkData, now),
		})
	}
	c.dir = out
	c.dirAt = time.Now()
	return out, nil
}

// pickPeer selects the participant that is neither my uid nor my sec_uid.
func (c *Client) pickPeer(parts []participant) participant {
	for _, p := range parts {
		if p.uid != c.myUID && p.secID != c.mySecUID {
			return p
		}
	}
	for _, p := range parts {
		if p.secID != c.mySecUID {
			return p
		}
	}
	if len(parts) > 0 {
		return parts[0]
	}
	return participant{}
}

// ConversationByID returns the directory entry for a conv_id (e.g. from a
// realtime Event), so callers have the Cursor/Ticket needed to reply.
func (c *Client) ConversationByID(ctx context.Context, convID string) (*Conversation, error) {
	dir, err := c.Conversations(ctx, false)
	if err != nil {
		return nil, err
	}
	for i := range dir {
		if dir[i].ConvID == convID {
			return &dir[i], nil
		}
	}
	// Not in cache (e.g. a brand-new conversation) — force one refresh.
	dir, err = c.Conversations(ctx, true)
	if err != nil {
		return nil, err
	}
	for i := range dir {
		if dir[i].ConvID == convID {
			return &dir[i], nil
		}
	}
	return nil, fmt.Errorf("no conversation with id %q", convID)
}

// FindConversation resolves a name (nickname / @unique_id / peer uid / conv_id)
// to a conversation. Substring match on nickname is allowed as a last resort.
func (c *Client) FindConversation(ctx context.Context, name string) (*Conversation, error) {
	dir, err := c.Conversations(ctx, false)
	if err != nil {
		return nil, err
	}
	lower := strings.ToLower(name)
	handle := strings.TrimPrefix(lower, "@")
	var substr *Conversation
	for i := range dir {
		d := &dir[i]
		if strings.ToLower(d.Nickname) == lower ||
			strings.ToLower(d.UniqueID) == handle ||
			d.PeerUID == name ||
			d.ConvID == name {
			return d, nil
		}
		if substr == nil && d.Nickname != "" && strings.Contains(strings.ToLower(d.Nickname), lower) {
			substr = d
		}
	}
	if substr != nil {
		return substr, nil
	}
	return nil, fmt.Errorf("no conversation matched %q", name)
}

// errStatus formats a non-200 HTTP error.
func errStatus(name string, status int) error {
	return fmt.Errorf("%s: HTTP %d (cookies expired?)", name, status)
}
