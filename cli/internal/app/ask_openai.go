package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

// The same agent loop as askClaude, on OpenAI's Chat Completions API (plain HTTP, no SDK). A tool message carries
// text only, so the screen after an action follows it as a user message with the image.

type oaiMsg = map[string]any

type oaiResp struct {
	Choices []struct {
		Message      json.RawMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		Prompt     int64 `json:"prompt_tokens"`
		Completion int64 `json:"completion_tokens"`
		Details    struct {
			Cached int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type oaiMessage struct {
	Content   string `json:"content"`
	Refusal   string `json:"refusal"`
	ToolCalls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// oaiTools: askTools in OpenAI's function format.
func oaiTools() []oaiMsg {
	var out []oaiMsg
	for _, t := range askTools() {
		p := t.OfTool
		params := map[string]any{"type": "object", "properties": p.InputSchema.Properties}
		if len(p.InputSchema.Required) > 0 {
			params["required"] = p.InputSchema.Required
		}
		out = append(out, oaiMsg{"type": "function", "function": oaiMsg{
			"name": p.Name, "description": p.Description.Value, "parameters": params}})
	}
	return out
}

// oaiParts: tool result blocks as text, plus the images as a user message (nil when there is none).
func oaiParts(c []anthropic.ToolResultBlockParamContentUnion) (string, oaiMsg) {
	var text []string
	var imgs []oaiMsg
	for _, b := range c {
		if b.OfText != nil {
			text = append(text, b.OfText.Text)
		}
		if b.OfImage != nil && b.OfImage.Source.OfBase64 != nil {
			s := b.OfImage.Source.OfBase64
			imgs = append(imgs, oaiMsg{"type": "image_url", "image_url": oaiMsg{
				"url": "data:" + string(s.MediaType) + ";base64," + s.Data}})
		}
	}
	if len(text) == 0 {
		text = []string{"ok"}
	}
	if len(imgs) == 0 {
		return strings.Join(text, "\n"), nil
	}
	parts := append([]oaiMsg{{"type": "text", "text": "The phone's screen now:"}}, imgs...)
	return strings.Join(text, "\n"), oaiMsg{"role": "user", "content": parts}
}

func oaiCall(ctx context.Context, body oaiMsg) (*oaiResp, error) {
	b, _ := json.Marshal(body)
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/chat/completions", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+os.Getenv("OPENAI_API_KEY"))
	req.Header.Set("Content-Type", "application/json")
	c := http.Client{Timeout: 120 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var r oaiResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, raw)
	}
	if r.Error != nil {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, r.Error.Message)
	}
	if len(r.Choices) == 0 {
		return nil, fmt.Errorf("HTTP %d: no choices", resp.StatusCode)
	}
	return &r, nil
}

func (a *App) askOpenAI(task string, shot []anthropic.ToolResultBlockParamContentUnion, model string, price [4]float64,
	maxTurns int, res *askResult) {
	_, first := oaiParts(shot)
	if first == nil {
		first = oaiMsg{"role": "user", "content": []oaiMsg{}}
	}
	first["content"] = append([]oaiMsg{{"type": "text", "text": "Task: " + task}}, first["content"].([]oaiMsg)...)
	msgs := []any{oaiMsg{"role": "system", "content": askSystem}, first}
	tl := oaiTools()
	ctx := context.Background()
	for turn := 1; ; turn++ {
		if turn > maxTurns {
			res.Stopped = fmt.Sprintf("no answer after %d turns", maxTurns)
			return
		}
		t0 := time.Now()
		r, err := oaiCall(ctx, oaiMsg{"model": model, "messages": msgs, "tools": tl, "parallel_tool_calls": false,
			"max_completion_tokens": 4096, "reasoning_effort": "none"}) // Chat Completions takes tools only without reasoning
		if err != nil {
			res.Stopped = "API: " + err.Error()
			return
		}
		res.Turns = turn
		u := r.Usage
		fresh := u.Prompt - u.Details.Cached
		cost := (float64(fresh)*price[0] + float64(u.Completion)*price[1] + float64(u.Details.Cached)*price[2]) / 1e6
		step := askStep{N: turn, In: fresh, Out: u.Completion, CacheRead: u.Details.Cached,
			Ms: time.Since(t0).Milliseconds(), Cost: cost}
		res.In += u.Prompt
		res.Out += u.Completion
		res.Cost += cost
		ch := r.Choices[0]
		msgs = append(msgs, ch.Message) // as it came, for the next turn
		var m oaiMessage
		json.Unmarshal(ch.Message, &m)
		if step.Say = strings.TrimSpace(m.Content); step.Say != "" {
			fmt.Fprintf(a.Err, "%2d  %s\n", turn, step.Say)
		}
		if m.Refusal != "" {
			res.Steps = append(res.Steps, step)
			res.Stopped = "the model declined: " + m.Refusal
			return
		}
		if len(m.ToolCalls) == 0 {
			res.Steps = append(res.Steps, step)
			res.Answer = step.Say
			res.Stopped = "ended without done (" + ch.FinishReason + ")"
			return
		}
		var screens []any
		for _, tc := range m.ToolCalls {
			name, in := tc.Function.Name, tc.Function.Arguments
			step.Tool, step.Input = name, in
			fmt.Fprintf(a.Err, "    → %s %s\n", name, in)
			var args map[string]any
			if err := json.Unmarshal([]byte(in), &args); err != nil {
				msgs = append(msgs, oaiMsg{"role": "tool", "tool_call_id": tc.ID, "content": "bad input: " + err.Error()})
				continue
			}
			if name == "done" {
				res.Success, _ = args["success"].(bool)
				res.Answer, _ = args["answer"].(string)
				res.Steps = append(res.Steps, step)
				return
			}
			content, isErr := mcpContent(a.callTool(name, args))
			text, screen := oaiParts(content)
			if isErr {
				text = "error: " + text
			}
			msgs = append(msgs, oaiMsg{"role": "tool", "tool_call_id": tc.ID, "content": text})
			if screen != nil {
				screens = append(screens, screen)
			}
		}
		res.Steps = append(res.Steps, step)
		if len(screens) > 0 { // only the latest screen matters
			msgs = append(msgs, screens[len(screens)-1])
		}
	}
}
