package app

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

// The agent gets the phone tools without the board selector, and done; the API's schema shape.
func TestAskTools(t *testing.T) {
	b, err := json.Marshal(askTools())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"name":"tap"`, `"name":"done"`, `"name":"face"`, `"type":"object"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, bad := range []string{`"device"`, `"screenshot"`, `"phone_audio"`, `inputSchema`} {
		if strings.Contains(s, bad) {
			t.Errorf("unexpected %s", bad)
		}
	}
}

func TestMCPContentToToolResult(t *testing.T) {
	c, isErr := mcpContent(map[string]any{"isError": false, "content": []map[string]any{
		{"type": "text", "text": "tapped"}, {"type": "image", "data": "AAAA", "mimeType": "image/png"}}})
	if isErr || len(c) != 2 || c[0].OfText == nil || c[1].OfImage == nil {
		t.Fatalf("%+v %v", c, isErr)
	}
	b, _ := json.Marshal(c[1])
	if !strings.Contains(string(b), `"media_type":"image/png"`) {
		t.Fatal(string(b))
	}
}

func TestAskOpenAIDone(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"Home Screen.",
"tool_calls":[{"id":"c1","type":"function","function":{"name":"done","arguments":"{\"success\":true,\"answer\":\"18.2\"}"}}]}}],
"usage":{"prompt_tokens":1000,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":400}}}`)
	}))
	defer srv.Close()
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "test")
	a := &App{Out: io.Discard, Err: io.Discard}
	shot := []anthropic.ToolResultBlockParamContentUnion{{OfImage: &anthropic.ImageBlockParam{Source: anthropic.ImageBlockParamSourceUnion{
		OfBase64: &anthropic.Base64ImageSourceParam{Data: "AAAA", MediaType: "image/jpeg"}}}}}
	var res askResult
	a.askOpenAI("iOS version?", shot, "gpt-6-luna", askPrices["gpt-6-luna"], 5, &res)
	if !res.Success || res.Answer != "18.2" || res.Turns != 1 || res.In != 1000 {
		t.Fatalf("result %+v", res)
	}
	if want := (600*0.10 + 20*0.50 + 400*0.01) / 1e6; math.Abs(res.Cost-want) > 1e-12 {
		t.Fatalf("cost %v, want %v", res.Cost, want)
	}
	msgs := got["messages"].([]any)
	user := msgs[1].(map[string]any)["content"].([]any)
	if len(msgs) != 2 || user[0].(map[string]any)["text"] != "Task: iOS version?" || user[2].(map[string]any)["type"] != "image_url" {
		t.Fatalf("request messages %v", msgs)
	}
	if len(got["tools"].([]any)) != len(askTools()) {
		t.Fatalf("tools %d", len(got["tools"].([]any)))
	}
}
