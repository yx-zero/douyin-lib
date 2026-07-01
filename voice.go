package douyinim

// Voice → text via Douyin's own ASR (aweme im/message/audio/recognition).
// The endpoint silently returns NO text for large batches (verified: 104 → 0;
// chunks of 5 → 100%), so we chunk and pace. conv_short_id is REQUIRED.

import (
	"context"
	"encoding/json"
	"time"
)

const transcribeChunk = 5

type recogReqItem struct {
	URI         string `json:"uri"`
	SecUID      string `json:"sec_uid"`
	MessageID   string `json:"message_id"`
	MessageType int    `json:"message_type"`
	ConvShortID string `json:"conv_short_id"`
}

// transcribeInPlace fills voice messages' Text with transcriptions.
func (c *Client) transcribeInPlace(ctx context.Context, msgs []Message, convShortID string) error {
	// collect voices needing transcription
	idx := map[string]int{} // serverID → position in msgs
	var items []recogReqItem
	for i := range msgs {
		m := &msgs[i]
		if m.Kind == KindVoice && m.VoiceURI != "" && m.SenderSecID != "" {
			items = append(items, recogReqItem{
				URI:         m.VoiceURI,
				SecUID:      m.SenderSecID,
				MessageID:   m.ServerID,
				MessageType: 17,
				ConvShortID: convShortID,
			})
			idx[m.ServerID] = i
		}
	}
	if len(items) == 0 {
		return nil
	}

	endpoint := urlRecog + "?" + c.webParams().Encode()
	for i := 0; i < len(items); i += transcribeChunk {
		end := i + transcribeChunk
		if end > len(items) {
			end = len(items)
		}
		chunk := items[i:end]
		results, err := c.transcribeOne(ctx, endpoint, chunk)
		if err != nil {
			// skip failed chunk, keep going
			continue
		}
		for id, text := range results {
			if pos, ok := idx[id]; ok && text != "" {
				msgs[pos].Text = text
			}
		}
		if end < len(items) {
			sleep(ctx, 150*time.Millisecond)
		}
	}
	return nil
}

func (c *Client) transcribeOne(ctx context.Context, endpoint string, chunk []recogReqItem) (map[string]string, error) {
	reqBody, _ := json.Marshal(map[string]any{"req_list": chunk})
	body, status, err := c.postJSON(ctx, endpoint, reqBody)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, errStatus("audio/recognition", status)
	}
	var parsed struct {
		RecognitionResults []struct {
			MessageID  string `json:"message_id"`
			URI        string `json:"uri"`
			TextResult string `json:"text_result"`
		} `json:"recognition_results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range parsed.RecognitionResults {
		key := r.MessageID
		if key == "" {
			// match by uri
			for _, it := range chunk {
				if it.URI == r.URI {
					key = it.MessageID
					break
				}
			}
		}
		if key != "" && r.TextResult != "" {
			out[key] = r.TextResult
		}
	}
	return out, nil
}
