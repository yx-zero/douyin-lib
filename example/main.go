// Command example demonstrates the douyinim library: list chats (with 火花),
// read a conversation (text/image/voice transcribed), check spark status, and
// send a text message.
//
// Usage:
//
//	go run . list
//	go run . spark
//	go run . read <name> [count]
//	go run . send <name> <text...>
//	go run . favorites [limit]
//
// Cookies come from ../../cookies.txt (or $DOUYIN_COOKIES).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	dy "github.com/yx-zero/douyin-lib"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cookiesPath := os.Getenv("DOUYIN_COOKIES")
	if cookiesPath == "" {
		cookiesPath = "../../cookies.txt"
	}
	client, err := dy.NewFromFile(cookiesPath, dy.WithDoH(""))
	must(err)

	// Long-running commands run until Ctrl-C; the rest use a 2-minute timeout.
	switch os.Args[1] {
	case "listen", "pong":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if os.Args[1] == "listen" {
			cmdListen(ctx, client)
		} else {
			cmdPong(ctx, client)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	switch os.Args[1] {
	case "list":
		cmdList(ctx, client)
	case "spark":
		cmdSpark(ctx, client)
	case "read":
		if len(os.Args) < 3 {
			usage()
		}
		count := 30
		if len(os.Args) >= 4 {
			count, _ = strconv.Atoi(os.Args[3])
		}
		cmdRead(ctx, client, os.Args[2], count)
	case "send":
		if len(os.Args) < 4 {
			usage()
		}
		cmdSend(ctx, client, os.Args[2], strings.Join(os.Args[3:], " "))
	case "recall":
		if len(os.Args) < 3 {
			usage()
		}
		var msgID string
		if len(os.Args) >= 4 {
			msgID = os.Args[3]
		}
		cmdRecall(ctx, client, os.Args[2], msgID)
	case "favorites":
		limit := 20
		if len(os.Args) >= 3 {
			limit, _ = strconv.Atoi(os.Args[2])
		}
		cmdFavorites(ctx, client, limit)
	case "download":
		if len(os.Args) < 3 {
			usage()
		}
		cmdDownload(ctx, client, os.Args[2])
	case "readstatus":
		if len(os.Args) < 3 {
			usage()
		}
		cmdReadStatus(ctx, client, os.Args[2])
	default:
		usage()
	}
}

func cmdList(ctx context.Context, c *dy.Client) {
	convs, err := c.Conversations(ctx, false)
	must(err)
	fmt.Printf("Your conversations (%d):\n", len(convs))
	for _, v := range convs {
		name := v.Nickname
		if name == "" {
			name = "(uid " + v.PeerUID + ")"
		}
		handle := ""
		if v.UniqueID != "" {
			handle = " @" + v.UniqueID
		}
		spark := ""
		if v.Spark != nil {
			spark = fmt.Sprintf("  🔥%d %s", v.Spark.Days, v.Spark.State)
		}
		fmt.Printf("- %s%s · %d msgs%s\n", name, handle, v.MsgCount, spark)
	}
}

func cmdSpark(ctx context.Context, c *dy.Client) {
	convs, err := c.Conversations(ctx, false)
	must(err)
	fmt.Println("火花 status:")
	any := false
	for _, v := range convs {
		if v.Spark == nil {
			continue
		}
		any = true
		s := v.Spark
		status := map[dy.SparkState]string{
			dy.SparkRenewed:    "✅ 今天已续上",
			dy.SparkNotRenewed: "⚠️  今天还没续",
			dy.SparkToRecover:  "🆘 待重燃/即将消失",
		}[s.State]
		name := v.Nickname
		if name == "" {
			name = v.PeerUID
		}
		extra := ""
		if s.State == dy.SparkToRecover && s.Text != "" {
			extra = " (" + s.Text + ")"
		}
		fmt.Printf("- %-20s 🔥%-4d %s%s\n", name, s.Days, status, extra)
	}
	if !any {
		fmt.Println("(no active sparks)")
	}
}

func cmdRead(ctx context.Context, c *dy.Client, name string, count int) {
	conv, err := c.FindConversation(ctx, name)
	must(err)
	msgs, err := c.GetMessages(ctx, conv, dy.MessageOptions{
		Range:           dy.RangeLast,
		Count:           count,
		TranscribeVoice: true,
	})
	must(err)

	label := conv.Nickname
	if label == "" {
		label = conv.PeerUID
	}
	fmt.Printf("Chat with %s — %d message(s)\n%s\n", label, len(msgs), strings.Repeat("=", 48))
	for _, m := range msgs {
		who := label
		if m.IsMe {
			who = "me"
		}
		t := "?"
		if !m.Timestamp.IsZero() {
			t = m.Timestamp.Format("2006-01-02 15:04:05")
		}
		fmt.Printf("[%s] %s: %s\n", t, who, render(m))
		if m.Reply != nil && m.Reply.Content != "" {
			fmt.Printf("        ↳ 引用 %s: %s\n", m.Reply.Nickname, m.Reply.Content)
		}
	}
}

func render(m dy.Message) string {
	switch m.Kind {
	case dy.KindVoice:
		dur := int(m.Duration.Seconds())
		if m.Text != "" {
			return fmt.Sprintf("[语音 %ds] %s", dur, m.Text)
		}
		return fmt.Sprintf("[语音消息 %ds]（无转写）", dur)
	case dy.KindSticker:
		if m.Sticker != nil && m.Sticker.DisplayName != "" {
			return "[贴纸] " + m.Sticker.DisplayName
		}
		if m.Text != "" && m.Text != "[表情]" {
			return "[贴纸] " + m.Text
		}
		return "[贴纸]"
	case dy.KindImage:
		if m.Text != "" && m.Text != "[图片]" {
			return "[图片] " + m.Text
		}
		return "[图片]"
	case dy.KindShare:
		if m.Text == "" || m.Text == "[分享]" {
			return "[分享]"
		}
		return "[分享] " + m.Text
	case dy.KindSystem:
		return "[系统] " + m.Text
	default:
		return m.Text
	}
}

func cmdSend(ctx context.Context, c *dy.Client, name, text string) {
	conv, err := c.FindConversation(ctx, name)
	must(err)
	res, err := c.SendText(ctx, conv, text)
	must(err)
	fmt.Printf("Sent to %s ✓  server_msg_id=%s client_msg_id=%s\n",
		conv.Nickname, res.ServerMessageID, res.ClientMessageID)
}

// cmdRecall recalls a message: by explicit server_message_id if given, else
// the most recent message I sent in the conversation.
func cmdRecall(ctx context.Context, c *dy.Client, name, msgID string) {
	conv, err := c.FindConversation(ctx, name)
	must(err)
	if msgID != "" {
		must(c.RecallMessageByID(ctx, conv, msgID))
		fmt.Printf("Recalled message %s in %s ✓\n", msgID, conv.Nickname)
		return
	}
	msgs, err := c.GetMessages(ctx, conv, dy.MessageOptions{Range: dy.RangeLast, Count: 20})
	must(err)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].IsMe {
			must(c.RecallMessage(ctx, conv, &msgs[i]))
			fmt.Printf("Recalled message %s (%q) in %s ✓\n", msgs[i].ServerID, msgs[i].Text, conv.Nickname)
			return
		}
	}
	fmt.Println("no message of mine found in last 20 messages")
}

func cmdFavorites(ctx context.Context, c *dy.Client, limit int) {
	stickers, err := c.FavoriteStickers(ctx)
	must(err)
	if limit <= 0 || limit > len(stickers) {
		limit = len(stickers)
	}
	fmt.Printf("Favorite stickers (%d total, showing %d):\n", len(stickers), limit)
	for i := 0; i < limit; i++ {
		s := stickers[i]
		name := s.DisplayName
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Printf("%2d. id=%s pkg=%d type=%d %dx%d src=%s name=%s\n",
			i+1, s.IDString(), s.PackageID, s.StickerType, s.Width, s.Height, s.StickerInfoSource, name)
	}
}

// cmdDownload finds the most recent image in a chat, decrypts it, and saves it.
func cmdDownload(ctx context.Context, c *dy.Client, name string) {
	conv, err := c.FindConversation(ctx, name)
	must(err)
	msgs, err := c.GetMessages(ctx, conv, dy.MessageOptions{Range: dy.RangeLast, Count: 30})
	must(err)
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Kind != dy.KindImage {
			continue
		}
		data, format, err := c.DownloadImage(ctx, &m)
		must(err)
		out := "downloaded_" + m.ServerID + format.Ext
		must(os.WriteFile(out, data, 0644))
		kind := "image"
		if format.IsVideo {
			kind = "video"
		}
		fmt.Printf("Saved %s (%d bytes, %s) → %s\n", kind, len(data), format.Ext, out)
		return
	}
	fmt.Println("no image found in last 30 messages")
}

// cmdListen streams live events (new messages + read receipts) until Ctrl-C.
func cmdListen(ctx context.Context, c *dy.Client) {
	rt, err := c.Realtime(ctx)
	must(err)
	defer rt.Close()
	fmt.Println("listening for live events (Ctrl-C to stop)…")
	for ev := range rt.Events() {
		switch ev.Type {
		case dy.EventConnected:
			fmt.Println("● connected")
		case dy.EventDisconnected:
			fmt.Printf("○ disconnected (%v)\n", ev.Err)
		case dy.EventNewMessage:
			m := ev.Message
			who := "peer"
			if m.IsMe {
				who = "me"
			}
			fmt.Printf("[msg] conv=%s %s: %s\n", short(m.ConvID), who, render(*m))
		case dy.EventReadReceipt:
			r := ev.ReadReceipt
			who := "peer"
			if r.IsMe {
				who = "me"
			}
			fmt.Printf("[read] conv=%s by=%s read_index=%d unread=%d\n", short(r.ConvID), who, r.ReadIndex, r.BadgeCount)
		case dy.EventRecall:
			r := ev.Recall
			who := "peer"
			if r.IsMe {
				who = "me"
			}
			fmt.Printf("[recall] conv=%s by=%s target_msg=%s\n", short(r.ConvID), who, r.TargetServerMessageID)
		}
	}
	if err := rt.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "stream ended:", err)
	}
}

// cmdPong is a ping→pong auto-reply bot: replies "pong" to any peer "ping".
func cmdPong(ctx context.Context, c *dy.Client) {
	rt, err := c.Realtime(ctx)
	must(err)
	defer rt.Close()
	fmt.Println("ping→pong bot running (Ctrl-C to stop)…")
	for ev := range rt.Events() {
		if ev.Type != dy.EventNewMessage {
			continue
		}
		m := ev.Message
		if m.IsMe || m.Kind != dy.KindText || strings.TrimSpace(m.Text) != "ping" {
			continue
		}
		conv, err := c.ConversationByID(ctx, m.ConvID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "lookup conv:", err)
			continue
		}
		if _, err := c.SendText(ctx, conv, "pong"); err != nil {
			fmt.Fprintln(os.Stderr, "send pong:", err)
			continue
		}
		fmt.Printf("ping from %s → ponged\n", conv.Nickname)
	}
}

// cmdReadStatus shows the peer's read watermark and marks recent messages read/unread.
func cmdReadStatus(ctx context.Context, c *dy.Client, name string) {
	conv, err := c.FindConversation(ctx, name)
	must(err)
	reads, err := c.ReadIndex(ctx, conv)
	must(err)
	fmt.Printf("Read watermarks for %s:\n", conv.Nickname)
	var peerV2 uint64
	for _, r := range reads {
		who := "peer"
		if r.IsMe {
			who = "me"
		}
		fmt.Printf("  %-4s uid=%s index_v2=%d\n", who, r.UID, r.IndexV2)
		if !r.IsMe {
			peerV2 = r.IndexV2
		}
	}
	if peerV2 == 0 {
		fmt.Println("(no peer read watermark)")
		return
	}
	msgs, err := c.GetMessages(ctx, conv, dy.MessageOptions{Range: dy.RangeLast, Count: 10})
	must(err)
	fmt.Printf("\nLast %d messages (✓=read by peer, ✗=unread):\n", len(msgs))
	for _, m := range msgs {
		mark := "✓"
		if m.IndexV2 > peerV2 {
			mark = "✗"
		}
		who := conv.Nickname
		if m.IsMe {
			who = "me"
		}
		fmt.Printf("  %s [v2=%d] %s: %s\n", mark, m.IndexV2, who, render(m))
	}
}

func short(convID string) string {
	if i := strings.LastIndex(convID, ":"); i > 0 && i < len(convID)-1 {
		return "…:" + convID[i+1:]
	}
	return convID
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  go run . list                  list conversations (with 火花)
  go run . spark                 show 火花 status for all chats
  go run . read <name> [count]   read last [count] messages (voice transcribed)
  go run . send <name> <text>    send a text message
  go run . recall <name> [msgID] recall my last message (or a specific server_message_id)
  go run . favorites [limit]     list favorite/custom stickers from the web panel
  go run . download <name>       decrypt + save the latest image in a chat
  go run . readstatus <name>     peer read watermark + per-message read/unread
  go run . listen                stream live messages + read receipts (Ctrl-C)
  go run . pong                  ping→pong auto-reply bot (Ctrl-C)`)
	os.Exit(2)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
