package models

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlanBuildsPromptAndResolvesElement(t *testing.T) {
	var (
		gotPath string
		req     struct {
			Model       string `json:"model"`
			Messages    []struct{ Role, Content string }
			Temperature float64 `json:"temperature"`
			MaxTokens   int     `json:"max_tokens"`
		}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"The answer is id 2"}}]}`))
	}))
	defer srv.Close()

	elements := []Element{
		{Type: "icon", Content: "Username", Center: [2]float64{10, 20}},
		{Type: "text", Content: "Password", Center: [2]float64{30, 40}},
		{Type: "icon", Content: "Sign in", Center: [2]float64{50, 60}},
	}

	c := NewClient(Options{BaseURL: srv.URL, HTTPClient: srv.Client(), PlannerModel: "qwen3.8-27b"})
	res, err := c.Plan(context.Background(), "click the Sign in button", elements, PlanOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if gotPath != "/model/qwen3.8-27b/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if req.Model != "qwen3.8-27b" {
		t.Errorf("model = %q", req.Model)
	}
	if req.Temperature != 0 {
		t.Errorf("temperature = %v", req.Temperature)
	}
	if req.MaxTokens != defaultPlanMaxTokens {
		t.Errorf("max_tokens = %d", req.MaxTokens)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(req.Messages))
	}
	if req.Messages[0].Role != "system" || req.Messages[0].Content != systemPrompt {
		t.Errorf("system prompt = %q", req.Messages[0].Content)
	}
	user := req.Messages[1].Content
	for _, want := range []string{
		"Instruction: click the Sign in button",
		"There are 3 interactable elements on screen (0-indexed):",
		`0: icon "Username"`,
		`1: text "Password"`,
		`2: icon "Sign in"`,
		"Which element id should be acted on to satisfy the instruction? Reply with the id only.",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing %q:\n%s", want, user)
		}
	}

	if res.ElementID != 2 || res.Element.Content != "Sign in" || res.Element.Center != [2]float64{50, 60} {
		t.Errorf("result = %+v", res)
	}
	if res.Reply != "The answer is id 2" {
		t.Errorf("reply = %q", res.Reply)
	}
}

func TestPlanOptionsOverride(t *testing.T) {
	var req struct {
		Model     string  `json:"model"`
		MaxTokens int     `json:"max_tokens"`
		Temp      float64 `json:"temperature"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&req)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"0"}}]}`))
	}))
	defer srv.Close()

	c := NewClient(Options{BaseURL: srv.URL, HTTPClient: srv.Client(), PlannerModel: "default"})
	if _, err := c.Plan(context.Background(), "x", []Element{{Type: "icon"}}, PlanOptions{Model: "hemmingway", MaxTokens: 64, Temperature: 0.7}); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if req.Model != "hemmingway" {
		t.Errorf("model = %q, want hemmingway", req.Model)
	}
	if req.MaxTokens != 64 {
		t.Errorf("max_tokens = %d, want 64", req.MaxTokens)
	}
	if req.Temp != 0.7 {
		t.Errorf("temperature = %v, want 0.7", req.Temp)
	}
}

func TestPlanReasoningContentFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","reasoning_content":"blah blah id 1"}}]}`))
	}))
	defer srv.Close()

	c := NewClient(Options{BaseURL: srv.URL, HTTPClient: srv.Client(), PlannerModel: "m"})
	res, err := c.Plan(context.Background(), "x", []Element{{Type: "icon"}, {Type: "icon"}}, PlanOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.ElementID != 1 || res.Reply != "blah blah id 1" {
		t.Errorf("result = %+v", res)
	}
}

func TestPlanNoModel(t *testing.T) {
	c := NewClient(Options{BaseURL: "http://base.example"})
	if _, err := c.Plan(context.Background(), "x", []Element{{Type: "icon"}}, PlanOptions{}); err == nil {
		t.Fatal("expected error when no planner model is configured")
	}
}

func TestPlanServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	c := NewClient(Options{BaseURL: srv.URL, HTTPClient: srv.Client(), PlannerModel: "m"})
	if _, err := c.Plan(context.Background(), "x", []Element{{Type: "icon"}}, PlanOptions{}); err == nil {
		t.Fatal("expected server error")
	}
}

func TestChooseElementID(t *testing.T) {
	cases := []struct {
		name    string
		reply   string
		n       int
		want    int
		wantErr bool
	}{
		{"bare number", "2", 3, 2, false},
		{"worded id", "The answer is id 1", 3, 1, false},
		{"click element", "click element 0 please", 3, 0, false},
		{"multiple bare numbers takes last", "I would choose 1, then 2, then 0", 3, 0, false},
		{"multiple explicit takes last", "id 1 and id 2", 3, 2, false},
		{"explicit beats bare", "maybe 5 but id 0", 3, 0, false},
		{"out of range", "id 5", 3, 0, true},
		{"zero elements", "id 0", 0, 0, true},
		{"no number", "no numbers here", 3, 0, true},
		{"empty", "", 3, 0, true},
		{"whitespace", "   ", 3, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chooseElementID(tc.reply, tc.n)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got id %d", got)
				}
				if strings.Contains(err.Error(), "empty reply") && tc.reply == "" {
					return
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("id = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestChooseElementIDErrorMentionsReply(t *testing.T) {
	_, err := chooseElementID("id 99", 2)
	if err == nil || !strings.Contains(err.Error(), "id 99") || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseReply(t *testing.T) {
	var resp chatResponse
	resp.Choices = append(resp.Choices, struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
	}{})
	resp.Choices[0].Message.Content = " 3 "
	resp.Choices[0].Message.ReasoningContent = "reasoning"
	if got := parseReply(resp); got != "3" {
		t.Errorf("parseReply content = %q", got)
	}

	resp.Choices[0].Message.Content = ""
	if got := parseReply(resp); got != "reasoning" {
		t.Errorf("parseReply reasoning = %q", got)
	}

	if got := parseReply(chatResponse{}); got != "" {
		t.Errorf("parseReply empty = %q", got)
	}
}

func TestPlanResultJSON(t *testing.T) {
	out, err := json.Marshal(PlanResult{ElementID: 1, Element: Element{Type: "icon"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"element_id":1`) {
		t.Errorf("json = %s", out)
	}
}

func TestMarshalNoEscapeMatchesJSONStringify(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain", `"plain"`},
		{"A & B", `"A & B"`},
		{"Terms & Conditions", `"Terms & Conditions"`},
		{"<b>Q&A</b>", `"<b>Q&A</b>"`},
		{`he said "hi"`, `"he said \"hi\""`},
		{"line\nbreak\ttab", `"line\nbreak\ttab"`},
		{"back\\slash", `"back\\slash"`},
		{"unicode ✓", `"unicode ✓"`},
	}
	for _, tc := range cases {
		got, err := marshalNoEscape(tc.in)
		if err != nil {
			t.Fatalf("marshalNoEscape(%q): %v", tc.in, err)
		}
		if string(got) != tc.want {
			t.Errorf("marshalNoEscape(%q) = %s, want %s", tc.in, got, tc.want)
		}
		if strings.Contains(string(got), `\u00`) {
			t.Errorf("marshalNoEscape(%q) HTML-escaped: %s", tc.in, got)
		}
	}
}

func TestBuildPlannerUserNoHTMLEscaping(t *testing.T) {
	elements := []Element{
		{Type: "text", Content: "A & B"},
		{Type: "text", Content: "Terms & Conditions"},
		{Type: "icon", Content: "<Back>"},
	}
	got := buildPlannerUser("click Q&A", elements, 0)

	for _, want := range []string{
		`0: text "A & B"`,
		`1: text "Terms & Conditions"`,
		`2: icon "<Back>"`,
		"Instruction: click Q&A",
		"There are 3 interactable elements on screen (0-indexed):",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{`\u0026`, `\u003c`, `\u003e`} {
		if strings.Contains(got, bad) {
			t.Errorf("prompt contains HTML escape %q:\n%s", bad, got)
		}
	}
}

func TestBuildPlannerUserDeclaredCount(t *testing.T) {
	elements := []Element{{Type: "icon", Content: "x"}}
	got := buildPlannerUser("go", elements, 5)
	if !strings.Contains(got, "There are 5 interactable elements on screen (0-indexed):") {
		t.Errorf("declared count not used:\n%s", got)
	}
	// Non-positive declared falls back to len(elements).
	got = buildPlannerUser("go", elements, 0)
	if !strings.Contains(got, "There are 1 interactable elements on screen (0-indexed):") {
		t.Errorf("fallback count not used:\n%s", got)
	}
}

func TestPlanPromptAndWireNoHTMLEscaping(t *testing.T) {
	var (
		rawBody []byte
		req     struct {
			Messages []struct{ Role, Content string }
		}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(rawBody, &req)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"1"}}]}`))
	}))
	defer srv.Close()

	elements := []Element{
		{Type: "text", Content: "A & B"},
		{Type: "link", Content: "<Sign in>"},
	}
	c := NewClient(Options{BaseURL: srv.URL, HTTPClient: srv.Client(), PlannerModel: "m"})
	if _, err := c.Plan(context.Background(), `press "Terms & Conditions"`, elements, PlanOptions{Count: 2}); err != nil {
		t.Fatalf("Plan: %v", err)
	}

	user := req.Messages[1].Content
	for _, want := range []string{
		`0: text "A & B"`,
		`1: link "<Sign in>"`,
		`Instruction: press "Terms & Conditions"`,
		"There are 2 interactable elements on screen (0-indexed):",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("decoded prompt missing %q:\n%s", want, user)
		}
	}

	// The wire body must also carry the literal characters (matching JS
	// JSON.stringify), not the \u00xx HTML escapes.
	for _, bad := range []string{`\u0026`, `\u003c`, `\u003e`} {
		if strings.Contains(string(rawBody), bad) {
			t.Errorf("wire body contains HTML escape %q:\n%s", bad, rawBody)
		}
	}
}
