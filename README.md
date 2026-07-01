# douyin-lib

一个**纯协议**的抖音网页版**私信（IM）** Go 库 —— 拉取会话列表、读取历史消息
（文本 / 图片 / 语音 / 表情贴纸 / 分享）、把语音转成文字、查看**火花**状态、
发送与撤回消息，并支持通过 WebSocket 接收实时消息与已读回执。

**基于 Cookie，不需要浏览器、不需要跑 JS 签名。** 它直接用你登录后的 Cookie
调用抖音网页版的 HTTP 接口，可在任何能跑 Go 的地方运行，包括无头的 Docker 容器。
可以把它理解成「抖音私信版的 discord selfbot」。

> ⚠️ 本库操作的是**你自己账号**、用**你自己的会话**。请遵守抖音用户协议与当地法律，
> 自行承担使用风险。

## 功能一览

| 功能 | 方法 |
|---|---|
| 拉取会话列表（含火花） | `Conversations` |
| 按昵称 / @号 / uid 查找会话 | `FindConversation` |
| 读取历史消息（可分段） | `GetMessages` |
| 语音转文字（抖音 ASR） | `GetMessages` 传 `TranscribeVoice: true` |
| 图片 / 贴纸 / 分享 / 引用回复 | 在 `GetMessages` 中自动归类 |
| 火花天数 + 今日状态 | `Conversation.Spark` |
| 拉取收藏 / 自定义表情 | `FavoriteStickers` |
| 标记消息已读 | `MarkReadMessage` / `MarkReadByIndex` / `MarkReadToEnd` |
| 发送文本消息 | `SendText` |
| 发送表情贴纸 | `SendSticker` / `SendFavoriteSticker` / `SendContentJSON` |
| 撤回消息 | `RecallMessage` / `RecallMessageByID` |
| 下载并解密图片 | `DownloadImage` / `DecryptImage` |
| 下载并解密视频（CENC） | `DownloadVideo` |
| 实时推送（新消息、已读回执、撤回） | `Realtime` |
| 已读状态（对方读到哪了） | `ReadIndex` / `PeerRead` / `WasRead` |

所有接口都走 Cookie 鉴权、纯 HTTP，落在 `imapi.douyin.com` 与 `www.douyin.com`。
实测**发送消息只需 Cookie** —— 不需要 `a_bogus` / `msToken` /
`identity_security_token`。

## 安装

```bash
go get github.com/yx-zero/douyin-lib
```

需要 Go 1.23+。

## 快速开始

```go
package main

import (
	"context"
	"fmt"

	dy "github.com/yx-zero/douyin-lib"
)

func main() {
	client, err := dy.NewFromFile("cookies.txt")
	if err != nil {
		panic(err)
	}
	ctx := context.Background()

	// 1. 列出会话及火花状态
	convs, _ := client.Conversations(ctx, false)
	for _, c := range convs {
		line := fmt.Sprintf("%s @%s (%d 条消息)", c.Nickname, c.UniqueID, c.MsgCount)
		if c.Spark != nil {
			line += fmt.Sprintf("  🔥%d %s", c.Spark.Days, c.Spark.State)
		}
		fmt.Println(line)
	}

	// 2. 读取某个会话最近 30 条消息，并把语音转成文字
	conv, _ := client.FindConversation(ctx, "某个昵称")
	msgs, _ := client.GetMessages(ctx, conv, dy.MessageOptions{
		Range: dy.RangeLast, Count: 30, TranscribeVoice: true,
	})
	for _, m := range msgs {
		who := conv.Nickname
		if m.IsMe {
			who = "我"
		}
		fmt.Printf("[%s] %s: %s\n", m.Timestamp.Format("15:04"), who, m.Text)
	}

	// 3. 发送一条消息
	res, err := client.SendText(ctx, conv, "你好")
	if err == nil {
		fmt.Println("已发送，server id：", res.ServerMessageID)
	}
}
```

## 火花（friendship spark）状态

火花数据直接内嵌在会话目录里 —— **不需要额外请求**。
每个 `Conversation.Spark`（没有火花时为 `nil`）包含：

```go
type Spark struct {
	Days           int        // 连续聊天天数
	State          SparkState // SparkRenewed / SparkNotRenewed / SparkToRecover
	Level          string     // 颜色档位：normal / blue / gray / to_recover
	Text           string     // 服务端展示文案（如 "29" 或 "2 天后消失"）
	RenewedToday   bool        // 便捷字段，等价于 State == SparkRenewed
	ExpireTime     time.Time  // 若今天不续，火花何时熄灭
	CanRecoverDays int        // 熄灭后可重燃的宽限天数
}
```

`State` 回答的就是「**今天续上了吗**」（已用真实数据校验）：

| State | 含义 |
|---|---|
| `SparkRenewed` (1) | 今天已续上 —— 今天聊过，火花点亮 |
| `SparkNotRenewed` (2) | 今天还没续 —— 火花仍在但今天没续 |
| `SparkToRecover` (3) | 待重燃 / 即将消失 —— 处于可重燃窗口 |

## 读取消息

`GetMessages` 会自动给每条 `Message` 归类到 `Kind`：

- `KindText` —— 纯文本
- `KindVoice` —— 含 `Duration` 与 `VoiceURI`；开启转写后 `Text` 为识别结果，`MediaURL` 为可播放的签名地址
- `KindImage` —— `MediaURL` 是（加密的）图片，`ImageSKey` 用于解密，`InlineWebP` 是可即时展示的缩略图
- `KindVideo` —— 直接发的短视频；`VideoID`+`VideoSKey` 交给 `DownloadVideo` 取址下载并 CENC 解密，`MediaURL`/`InlineWebP` 是封面
- `KindSticker` —— `Text` 为贴纸展示名（若有），`Sticker` 保留完整原始负载，`MediaURL` 为图片地址
- `KindShare` —— 分享的视频 / 商品 / 链接，`Text` 为描述
- `KindSystem` —— 系统通知
- 引用 / 回复内容在 `Message.Reply` 里

`GetMessages` 通过 `MessageOptions` 控制拉取范围：

```go
type MessageOptions struct {
	Range           MessageRange // RangeLast（默认）/ RangeFirst / RangeAll
	Count           int          // last/first 取多少条（all 忽略），默认 50
	TranscribeVoice bool         // 是否调用抖音 ASR 把语音转文字
}
```

## 表情贴纸

抖音网页版的贴纸消息目前和文本走同一个 `/v1/message/send` 接口，只是**消息类型为 `5`**，
content JSON 也是贴纸结构（收藏 / 自定义表情的 `aweType` 常见为 `501`，系统 lite 表情为 `507`）。

网页版「收藏 / 自定义表情」面板背后是
`/aweme/v1/web/im/resource/list/aggregation/`（Cookie 鉴权的 GET）。本库把收藏列表拍平后暴露出来：

```go
stickers, _ := client.FavoriteStickers(ctx)
fmt.Println("收藏数量：", len(stickers))

// 直接把某个收藏表情发出去
_, err := client.SendFavoriteSticker(ctx, conv, stickers[0])
```

也可以把历史消息里的某个贴纸解析出来再原样发回去：

```go
sticker, _ := dy.StickerContentFromMessage(&msgs[len(msgs)-1])
_, err := client.SendSticker(ctx, conv, sticker)
```

或者直接发送任意已捕获的 content JSON：

```go
_, err := client.SendContentJSON(ctx, conv, 5, rawStickerJSON)
```

## 加密图片

抖音私信里的图片在 `tplv-x-get:*.image` 地址上是 **AES-256-GCM 加密**的，本库会替你处理好：

```go
data, format, err := client.DownloadImage(ctx, &msg) // 下载 + 解密
os.WriteFile("image"+format.Ext, data, 0644)         // .jpg/.png/.webp/.heic/.mp4
```

`DownloadImage` 返回解密后的字节和一个 `MediaFormat`（根据文件头魔数判断类型 ——
注意 aweType 2702/2703/2704 既可能是图片也可能是视频）。每条图片 `Message` 还带一个
`InlineWebP`（未加密的小 WebP 缩略图），可以不下载就即时预览。

如需离线使用，`DecryptImage(encryptedBytes, skeyHex)` 只做解密这一步：
`key = hex(skey)`（32 字节 AES-256），`iv = bytes[:12]`，其余部分带 GCM tag。

## 加密视频

抖音私信里直接发的短视频（消息 `Kind == KindVideo`，`typeCode 30`）以 **CENC
（scheme `cenc`，AES-CTR）加密的 MP4** 传输：明文容器 + 加密画面。本库一步到位：

```go
data, err := client.DownloadVideo(ctx, &msg) // 取址 + 下载 + CENC 解密
os.WriteFile("video.mp4", data, 0644)        // 明文可播放 MP4
```

`DownloadVideo` 内部：用消息里的 `VideoID`（vid）经 `batch_play_info` 换取可播放
CDN 地址（cookie-only，无需 a_bogus），下载后用 `VideoSKey`（CENC key）逐 sample
AES-CTR 解密，并把 sample entry 的 fourcc 从 `encv`/`enca` 还原为原始编码
（`hvc1`/`mp4a` 等），得到明文 MP4。视频消息的 `MediaURL` / `InlineWebP` 指向封面图。

> 视频取址用的 CDN 地址有时效，建议收到消息即时下载（这也是防撤回场景的必要做法）。

## 撤回消息

```go
// 撤回历史消息里的某条
err := client.RecallMessage(ctx, conv, &msg)

// 或者直接按 server_message_id 撤回（比如刚发完拿到的 id）
res, _ := client.SendText(ctx, conv, "手滑了")
err := client.RecallMessageByID(ctx, conv, res.ServerMessageID)
```

> 注意：你**自己撤回**的消息，原文内容在服务端仍保留（只是打上撤回标记）；
> 但**对方撤回**的消息，服务端会在推给你之前就把内容替换成占位符，原文无法找回。

## 实时推送（WebSocket）

`Realtime` 会连上抖音的 frontier WebSocket，实时推送事件 ——
新的收到消息、以及已读回执更新，并自带断线重连。
这一部分同样是纯协议：连接用的 `access_key` 在本地推导（`MD5(...)`），不需要浏览器或任何令牌服务。

```go
rt, err := client.Realtime(ctx)
if err != nil { panic(err) }
defer rt.Close()

for ev := range rt.Events() {
	switch ev.Type {
	case dy.EventNewMessage:
		m := ev.Message
		if !m.IsMe && m.Kind == dy.KindText && m.Text == "ping" {
			conv, _ := client.ConversationByID(ctx, m.ConvID)
			client.SendText(ctx, conv, "pong") // ping→pong 自动回复机器人
		}
	case dy.EventReadReceipt:
		// ev.ReadReceipt.ReaderSecID 已读到 ev.ReadReceipt.ReadIndex
	case dy.EventRecall:
		// 有消息被撤回：ev.Recall.TargetServerMessageID 是被撤回消息的 ServerID
		// ev.Recall.IsMe=false 表示是对方撤回的
	case dy.EventConnected, dy.EventDisconnected:
		// 连接生命周期
	}
}
```

- **新消息**以 `EventNewMessage` 到达（`ev.Message`，和历史消息同一个 `Message` 类型），
  并带 `IsMe`，机器人不会回复自己发的消息。
- **已读回执**以 `EventReadReceipt` 到达 —— 是按会话的已读**水位线**（`ReadIndex`）；
  当某个参与者的 `ReadIndex` 追上一条消息，就说明这条被读了。`IsMe` 用于区分是你自己的多端同步还是对方已读。
- **撤回**以 `EventRecall` 到达（`ev.Recall`）—— 带被撤回消息的 `TargetServerMessageID`（对应先前某条 `Message.ServerID`），
  `IsMe` 区分是你还是对方撤回的。撤回本身不携带原文，所以要「反撤回」需在收到消息的当下就缓存内容（图片更要即时 `DownloadImage`，撤回后 CDN 资源可能失效）。
- 事件流自带退避重连；`Events()` 只有在你 `Close()` 时（或 `WithReconnect(false)` 且连接断开时）才会关闭。心跳在内部处理。

示例 CLI 里的 `listen`（打印实时事件）和 `pong`（ping→pong 机器人）演示了这个能力。

## 已读状态（已读）

用 `ReadIndex` 查看对方读到哪了 —— 每个参与者都有一个已读**水位线**，
当对方水位线追上某条消息，这条就算「已读」。

```go
peer, _ := client.PeerRead(ctx, conv)          // 对方的已读水位线
read, ok, _ := client.WasRead(ctx, conv, &msg) // 这条消息对方读了没？
if ok && read { fmt.Println("已读") }
```

- `ReadIndex(conv)` → `[]ParticipantRead{UID, SecID, Index, IndexV2, IndexMin, IsMe}`，包含每个参与者。
- `WasRead(conv, msg)` → 把对方水位线和消息的 `IndexV2` / `IndexInConv` 比较。`ok=false` 表示拿不到对方已读状态。
- 底层是 imapi `v3/conversation/get_read_index`（按单会话、Cookie 鉴权），和官方「已读」小标的来源一致。

示例 CLI 里的 `readstatus <name>` 会打印对方水位线并逐条标注已读/未读。

## Cookie

提供一个 Netscape 格式的 `cookies.txt`（从已登录的浏览器导出）。
Cookie 会过期 —— 一旦调用开始报错，就刷新这个文件。本库只需要抖音标准的会话 Cookie
（`sessionid`、`sid_tt`、`ttwid`、`sid_guard` 等）。

> 🔒 **`cookies.txt` 等于你的账号凭证，切勿提交进 git、切勿分享给他人。**
> 本仓库的 `.gitignore` 已默认忽略它。

## DNS-over-HTTPS（可选）

如果宿主机的系统 DNS 解析不了抖音 CDN 域名（比如只代理流量、不代理 DNS 的 VPN 环境），
可以用 `WithDoH` 让客户端改走 DNS-over-HTTPS 解析（走 HTTPS:443，绕开被封的 UDP:53）：

```go
// 用默认的 AliDNS（223.5.5.5）DoH 端点
client, _ := dy.NewFromFile("cookies.txt", dy.WithDoH(""))

// 或指定自定义 DoH 端点
client, _ := dy.NewFromFile("cookies.txt", dy.WithDoH("https://1.1.1.1/dns-query"))
```

## 其他可选项

- `WithHTTPClient(h)` —— 自定义 `*http.Client`（如代理 / TLS 设置）
- `WithUserAgent(ua)` —— 覆盖 User-Agent
- `WithDirectoryTTL(d)` —— 会话目录缓存时长（默认 5 分钟，传 0 关闭缓存）

## 示例 CLI

`example/main.go` 是一个可直接运行的 demo：

```bash
cd example
go run . list                  # 列出会话（含火花）
go run . spark                 # 所有会话的火花状态
go run . read <name> [count]   # 读取最近 N 条消息（语音自动转写）
go run . send <name> <text>    # 发送一条文本消息
go run . recall <name> [msgID] # 撤回我最近一条消息（或指定 server_message_id）
go run . favorites [limit]     # 列出收藏 / 自定义表情
go run . download <name>       # 解密并保存某会话里最新的一张图片
go run . readstatus <name>     # 对方已读水位线 + 每条消息已读/未读
go run . listen                # 实时打印新消息 + 已读回执（Ctrl-C 退出）
go run . pong                  # ping→pong 自动回复机器人（Ctrl-C 退出）
```

Cookie 从 `../../cookies.txt` 读取（或用环境变量 `DOUYIN_COOKIES` 指定路径）。

## 说明与限制

- **默认只读。** 读取消息不会触发已读回执。
- 节流：翻历史每页间隔约 120ms；语音转写按 5 条/请求分批（ASR 接口对大批量会静默丢弃）。
- 会话目录默认缓存 5 分钟（可用 `WithDirectoryTTL` 配置）；给 `Conversations` 传 `refresh=true` 可强制刷新。
- 发送消息需要一个从 `Conversations` / `FindConversation` 得到的 `Conversation`（它携带发送所需的会话 `Ticket`）。
- 本库均基于逆向抖音网页版接口，接口随时可能变动，不保证长期可用。
