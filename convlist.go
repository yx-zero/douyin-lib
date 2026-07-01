package douyinim

// get_message_by_init — the full conversation directory, plus 火花 (spark) data
// which is embedded per-conversation in core_info (meta.f50) ext map (f11).
// Wire format verified live 2026-06-28/29.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// initUA mirrors the UA the web client puts in the init meta block.
const initUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// buildInitRequest builds a get_message_by_init body (full conversation sync).
func buildInitRequest() []byte {
	inner := (&pbWriter{}).varintField(2, 0).finish() // f2=0 → full sync
	f8 := (&pbWriter{}).bytesField(2043, inner).finish()

	w := (&pbWriter{}).
		varintField(1, 2043).
		varintField(2, 10001).
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

// rawConversation is the pre-identity parse of one conversation.
type rawConversation struct {
	convID    string
	cursor    string
	ticket    string
	convType  int
	msgCount  int
	parts     []participant
	sparkChat string // a:consecutive_chat
	sparkData string // a:consecutive_chat_data (JSON)
}

type participant struct {
	uid   string
	secID string
}

// parseInitResponse decodes a get_message_by_init response into raw
// conversations (spark windows are resolved later, once the server `now` is
// known from im/user/info).
func parseInitResponse(data []byte) (convs []rawConversation) {
	top := readFields(data)
	f6 := fieldBytes(top, 6)
	if f6 == nil {
		return nil
	}
	container := readFields(f6)
	f2043 := fieldBytes(container, 2043)
	if f2043 == nil {
		return nil
	}
	init := readFields(f2043)

	for _, cf := range allFields(init, 1) {
		if cf.wire != 2 {
			continue
		}
		conv := readFields(cf.bytes)
		metaBytes := fieldBytes(conv, 1)
		if metaBytes == nil {
			continue
		}
		meta := readFields(metaBytes)
		convID := fieldString(meta, 1)
		cursor := ""
		if v, ok := fieldVarint(meta, 2); ok {
			cursor = strconv.FormatUint(v, 10)
		}
		if convID == "" || cursor == "" {
			continue
		}
		rc := rawConversation{
			convID: convID,
			cursor: cursor,
			ticket: fieldString(meta, 4),
		}
		if v, ok := fieldVarint(meta, 7); ok {
			rc.convType = int(v)
		}
		if v, ok := fieldVarint(meta, 10); ok {
			rc.msgCount = int(v)
		}
		// participants: meta.f6 → f1[] → {f1 uid, f5 secUid}
		if pc := fieldBytes(meta, 6); pc != nil {
			for _, pf := range allFields(readFields(pc), 1) {
				if pf.wire != 2 {
					continue
				}
				p := readFields(pf.bytes)
				uid := ""
				if v, ok := fieldVarint(p, 1); ok {
					uid = strconv.FormatUint(v, 10)
				}
				secID := fieldString(p, 5)
				if uid != "" || secID != "" {
					rc.parts = append(rc.parts, participant{uid: uid, secID: secID})
				}
			}
		}
		// spark: core_info meta.f50 → ext kv (f11) — keep raw, resolve later
		if core := fieldBytes(meta, 50); core != nil {
			rc.sparkChat, rc.sparkData = extractSparkRaw(readFields(core))
		}
		convs = append(convs, rc)
	}
	return convs
}

// sparkExt holds the JSON shape of a:consecutive_chat_data.
type sparkExtData struct {
	ExpireTime     int64 `json:"expire_time"`
	CanRecoverDays int   `json:"can_recover_days"`
	FlameInfos     []struct {
		Days  int    `json:"days"`
		Start int64  `json:"start"`
		End   int64  `json:"end"`
		Level string `json:"level"`
		Text  string `json:"text"`
		State int    `json:"state"`
	} `json:"flame_infos"`
	ConsecutiveCountInfo struct {
		ConsecutiveCount int   `json:"consecutive_count"`
		ExpireTime       int64 `json:"expire_time"`
	} `json:"consecutive_count_info"`
}

// extractSparkRaw pulls the two spark ext values from a conversation's core_info.
func extractSparkRaw(core []pbField) (chat, dataJSON string) {
	for _, ef := range allFields(core, 11) {
		if ef.wire != 2 {
			continue
		}
		kv := readFields(ef.bytes)
		switch fieldString(kv, 1) {
		case "a:consecutive_chat":
			chat = fieldString(kv, 2)
		case "a:consecutive_chat_data":
			dataJSON = fieldString(kv, 2)
		}
	}
	return chat, dataJSON
}

// buildSpark computes a Spark from the raw ext strings, picking the flame window
// that contains the server `now`. State is authoritative (1=renewed today,
// 2=not renewed today, 3=to-recover); level colour (before '|') is a hint.
func buildSpark(chat, dataJSON string, now time.Time) *Spark {
	if chat == "" && dataJSON == "" {
		return nil
	}
	sp := &Spark{}
	if dataJSON != "" {
		var d sparkExtData
		if err := json.Unmarshal([]byte(dataJSON), &d); err == nil {
			sp.Days = d.ConsecutiveCountInfo.ConsecutiveCount
			sp.CanRecoverDays = d.CanRecoverDays
			switch {
			case d.ConsecutiveCountInfo.ExpireTime > 0:
				sp.ExpireTime = time.Unix(d.ConsecutiveCountInfo.ExpireTime, 0)
			case d.ExpireTime > 0:
				sp.ExpireTime = time.Unix(d.ExpireTime, 0)
			}
			nowSec := now.Unix()
			for _, fi := range d.FlameInfos {
				if nowSec >= fi.Start && nowSec < fi.End {
					sp.State = SparkState(fi.State)
					sp.Level = strings.SplitN(fi.Level, "|", 2)[0]
					sp.Text = fi.Text
					if fi.Days > 0 {
						sp.Days = fi.Days
					}
					break
				}
			}
		}
	}
	// Fallback parse of "days:lastChat:expire:1:1".
	if sp.Days == 0 && chat != "" {
		parts := strings.Split(chat, ":")
		if len(parts) >= 1 {
			sp.Days, _ = strconv.Atoi(parts[0])
		}
		if len(parts) >= 3 && sp.ExpireTime.IsZero() {
			if exp, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
				sp.ExpireTime = time.Unix(exp, 0)
			}
		}
	}
	// If no flame window matched (now outside all windows), infer from level.
	if sp.State == 0 {
		switch sp.Level {
		case "gray":
			sp.State = SparkNotRenewed
		case "to_recover":
			sp.State = SparkToRecover
		default:
			sp.State = SparkNotRenewed
		}
	}
	sp.RenewedToday = sp.State == SparkRenewed
	return sp
}

// resolveUsers resolves sec_uids to profiles via aweme im/user/info (cookie-only).
// Returns a map sec_uid→profile, the caller's own owner_sec_uid, and the server
// time (extra.now) used to pick the active 火花 flame window.
func (c *Client) resolveUsers(ctx context.Context, secUIDs []string) (map[string]userProfile, string, time.Time, error) {
	users := map[string]userProfile{}
	if len(secUIDs) == 0 {
		return users, "", time.Time{}, nil
	}
	endpoint := urlUserInfo + "?" + c.webParams().Encode()
	ids, _ := json.Marshal(secUIDs)
	form := "sec_user_ids=" + urlQueryEscape(string(ids))

	body, status, err := c.postForm(ctx, endpoint, form)
	if err != nil {
		return users, "", time.Time{}, err
	}
	if status != 200 {
		return users, "", time.Time{}, errStatus("im/user/info", status)
	}
	var parsed struct {
		Data []struct {
			SecUID   string `json:"sec_uid"`
			UID      any    `json:"uid"`
			Nickname string `json:"nickname"`
			UniqueID string `json:"unique_id"`
			ShortID  string `json:"short_id"`
		} `json:"data"`
		OwnerSecUID string `json:"owner_sec_uid"`
		Extra       struct {
			Now int64 `json:"now"`
		} `json:"extra"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return users, "", time.Time{}, err
	}
	for _, u := range parsed.Data {
		if u.SecUID == "" {
			continue
		}
		uniq := u.UniqueID
		if uniq == "" {
			uniq = u.ShortID
		}
		users[u.SecUID] = userProfile{
			uid:      anyToString(u.UID),
			nickname: u.Nickname,
			uniqueID: uniq,
		}
	}
	var now time.Time
	if parsed.Extra.Now > 0 {
		now = time.UnixMilli(parsed.Extra.Now)
	}
	return users, parsed.OwnerSecUID, now, nil
}

type userProfile struct {
	uid      string
	nickname string
	uniqueID string
}
