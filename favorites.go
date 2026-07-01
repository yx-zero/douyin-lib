package douyinim

// Favorite/custom sticker aggregation via
// /aweme/v1/web/im/resource/list/aggregation/. This is the same web endpoint
// the Douyin chat page uses to populate the favorites panel.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

const urlResourceAggregation = "https://www.douyin.com/aweme/v1/web/im/resource/list/aggregation/"
const defaultCustomStickerPageSize = 50

type favoriteStickerResponse struct {
	StatusCode            int    `json:"status_code"`
	StatusMsg             string `json:"status_msg"`
	CustomStickerPageList struct {
		IsCompleted bool  `json:"is_completed"`
		NextCursor  int64 `json:"next_cursor"`
		TotalCounts int64 `json:"total_counts"`
		Resources   []struct {
			ID       int64             `json:"id"`
			Stickers []FavoriteSticker `json:"stickers"`
		} `json:"resources"`
	} `json:"custom_sticker_page_list"`
}

// FavoriteStickers fetches the stickers shown in the web chat favorites/custom
// sticker panel. The result is flattened across packages; each item carries its
// sender-side favorites PackageID so it can be replayed via SendSticker.
func (c *Client) FavoriteStickers(ctx context.Context) ([]FavoriteSticker, error) {
	var (
		cursor   int64
		stickers []FavoriteSticker
		seen     = map[string]bool{}
	)
	for {
		page, err := c.favoriteStickersPage(ctx, cursor, defaultCustomStickerPageSize)
		if err != nil {
			return nil, err
		}
		for _, r := range page.CustomStickerPageList.Resources {
			for _, st := range r.Stickers {
				st.PackageID = r.ID
				key := st.IDString()
				if key != "" && seen[key] {
					continue
				}
				if key != "" {
					seen[key] = true
				}
				stickers = append(stickers, st)
			}
		}
		next := page.CustomStickerPageList.NextCursor
		total := page.CustomStickerPageList.TotalCounts
		if next <= cursor || next == 0 {
			break
		}
		if total > 0 && int64(len(stickers)) >= total {
			break
		}
		cursor = next
	}
	return stickers, nil
}

func (c *Client) favoriteStickersPage(ctx context.Context, cursor int64, limit int) (*favoriteStickerResponse, error) {
	q := c.webParams()
	q.Set("custom_cursor", strconv.FormatInt(cursor, 10))
	q.Set("custom_limit", strconv.Itoa(limit))

	u := urlResourceAggregation + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Origin", "https://www.douyin.com")
	req.Header.Set("Referer", "https://www.douyin.com/chat")
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("x-secsdk-csrf-token", "DOWNGRADE")
	req.Header.Set("Cookie", cookieHeader(c.cookies, wwwHost))

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errStatus("resource/list/aggregation", resp.StatusCode)
	}

	var out favoriteStickerResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.StatusCode != 0 {
		return nil, fmt.Errorf("resource/list/aggregation failed: status=%d msg=%q", out.StatusCode, out.StatusMsg)
	}
	return &out, nil
}

// StickerContent converts a favorite/custom sticker entry into the JSON shape
// used by sticker messages on the send path.
func (s FavoriteSticker) StickerContent() *StickerContent {
	url := s.AnimateURL
	if url == nil || (url.URI == "" && len(url.URLList) == 0) {
		url = s.StaticURL
	}
	imageType := s.StaticType
	if imageType == "" {
		imageType = s.AnimateType
	}
	if imageType == "" {
		imageType = "webp"
	}
	emojiSource := s.StickerInfoSource
	if emojiSource == "" {
		emojiSource = "personal_upload"
	}
	return &StickerContent{
		ActivityDesc:            "",
		ActivitySchema:          "",
		AuthorID:                "",
		AweType:                 501,
		DanmakuID:               "",
		DanmakuText:             "",
		DisplayName:             s.DisplayName,
		EmojiFrom:               "favorite_emoji",
		EmojiSource:             emojiSource,
		EmojiType:               "favorite_emoji",
		EnterMethod:             "click_favorite_emoji_panel",
		FromEmojiID:             "",
		Height:                  s.Height,
		HintContent:             "",
		ID:                      s.ID,
		ImageID:                 s.ID,
		ImageType:               imageType,
		LightIcon:               "",
		LightID:                 "",
		LightURL:                "",
		MatchUID:                "",
		MessageReplyDisplayType: 0,
		PackageID:               s.PackageID,
		ReplaceNickUserID:       "",
		ResourceType:            0,
		ShowNotice:              false,
		SourceDesc:              "",
		SourceSchema:            "",
		StickerID:               "",
		StickerType:             s.StickerType,
		TopDescReceiverV2:       "",
		TopDescReceiverV3:       "",
		TopDescSceneType:        0,
		TopDescSenderV2:         "",
		TopDescSenderV3:         "",
		TopDescUIDMatch:         "",
		URL:                     favoriteAssetToStickerURL(url),
		Version:                 0,
		Width:                   s.Width,
	}
}

func favoriteAssetToStickerURL(a *FavoriteStickerAsset) *StickerURL {
	if a == nil {
		return &StickerURL{}
	}
	return &StickerURL{URI: a.URI, URLList: a.URLList}
}

// IDString is a convenience identifier for logging / dedupe.
func (s FavoriteSticker) IDString() string {
	if s.IDStr != "" {
		return s.IDStr
	}
	return strconv.FormatInt(s.ID, 10)
}
