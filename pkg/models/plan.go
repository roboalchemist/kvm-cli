package models

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/redact"
)

const (
	// systemPrompt is the /cua harness system message verbatim.
	systemPrompt = "You are an expert UI element locator. Given a numbered list of GUI " +
		"elements and a user's target description, reply with ONLY the integer id of the " +
		"element to act on. No prose, no punctuation."
	// defaultPlanMaxTokens caps the planner reply.
	defaultPlanMaxTokens = 128
)

// PlanOptions tunes a Plan call.
type PlanOptions struct {
	// Model overrides the client's PlannerModel.
	Model string
	// MaxTokens caps the reply. Non-positive uses 128.
	MaxTokens int
	// Temperature is forwarded as-is (0 is deterministic).
	Temperature float64
	// Count is the element total reported by the grounding response
	// (GroundResult.Count). The /cua harness prints g.count in the prompt;
	// when positive this value is used verbatim, otherwise len(elements). The
	// two agree in practice; element indexing is always bounded by
	// len(elements), which is the slice we actually resolve against.
	Count int
}

// PlanResult is the outcome of Plan.
type PlanResult struct {
	ElementID int     `json:"element_id"`
	Element   Element `json:"element"`
	Reply     string  `json:"reply"`
	LatencyMS float64 `json:"latency_ms"`
}

var (
	// explicitIDRe prefers a number introduced by "id"/"element"/"answer"/"click".
	explicitIDRe = regexp.MustCompile(`(?i)(?:id|element|answer|click)\D{0,6}(\d+)`)
	// bareIDRe is the fallback for a reply that is just a number.
	bareIDRe = regexp.MustCompile(`\b(\d+)\b`)
)

// Plan asks a chat model to choose the element matching instruction and resolves
// the chosen element. N is derived from len(elements).
func (c *Client) Plan(ctx context.Context, instruction string, elements []Element, opts PlanOptions) (*PlanResult, error) {
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = c.PlannerModel
	}
	if model == "" {
		return nil, fmt.Errorf("models: no planner model configured")
	}
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultPlanMaxTokens
	}

	body, err := marshalNoEscape(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": buildPlannerUser(instruction, elements, opts.Count)},
		},
		"temperature": opts.Temperature,
		"max_tokens":  maxTokens,
	})
	if err != nil {
		return nil, redact.Error(fmt.Errorf("models: encode plan request: %w", err))
	}

	path := "/model/" + url.PathEscape(model) + "/v1/chat/completions"
	start := time.Now()
	var wire chatResponse
	if err := c.do(ctx, http.MethodPost, path, "application/json", body, &wire); err != nil {
		return nil, err
	}
	latency := float64(time.Since(start).Microseconds()) / 1000.0

	reply := parseReply(wire)
	id, err := chooseElementID(reply, len(elements))
	if err != nil {
		return nil, err
	}
	return &PlanResult{
		ElementID: id,
		Element:   elements[id],
		Reply:     reply,
		LatencyMS: latency,
	}, nil
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
	} `json:"choices"`
}

// parseReply returns the assistant text, preferring content and falling back to
// reasoning_content for reasoning planners that leave content empty.
func parseReply(resp chatResponse) string {
	if len(resp.Choices) == 0 {
		return ""
	}
	msg := resp.Choices[0].Message
	if s := strings.TrimSpace(msg.Content); s != "" {
		return s
	}
	return strings.TrimSpace(msg.ReasoningContent)
}

// marshalNoEscape is json.Marshal without HTML escaping, matching the /cua
// harness's JS JSON.stringify: '&', '<' and '>' stay literal instead of becoming
// \u0026 /\u003c /\u003e. This matters for OCR labels such as "Terms & Conditions".
// json.Encoder appends a newline, which is trimmed so the result equals
// JSON.stringify(v) byte-for-byte for ordinary and special-character strings.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// buildPlannerUser mirrors the /cua harness prompt exactly, including the
// JSON-quoted element content (produced without HTML escaping). declared is the
// element total reported by the grounding response; a non-positive value falls
// back to len(elements), which is what the reference's g.count equals in
// practice.
func buildPlannerUser(instruction string, elements []Element, declared int) string {
	if declared <= 0 {
		declared = len(elements)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Instruction: %s\n\n", instruction)
	fmt.Fprintf(&b, "There are %d interactable elements on screen (0-indexed):\n", declared)
	for i, e := range elements {
		content, _ := marshalNoEscape(e.Content)
		fmt.Fprintf(&b, "%d: %s %s\n", i, e.Type, content)
	}
	b.WriteString("\nWhich element id should be acted on to satisfy the instruction? Reply with the id only.")
	return b.String()
}

// chooseElementID extracts the planner's choice. It prefers an explicit
// "id/element/answer/click N" mention (last one wins), falls back to the last
// bare integer, and rejects anything outside [0, n).
func chooseElementID(reply string, n int) (int, error) {
	if strings.TrimSpace(reply) == "" {
		return 0, fmt.Errorf("models: planner returned an empty reply")
	}
	pick := ""
	if m := explicitIDRe.FindAllStringSubmatch(reply, -1); len(m) > 0 {
		pick = m[len(m)-1][1]
	} else if m := bareIDRe.FindAllStringSubmatch(reply, -1); len(m) > 0 {
		pick = m[len(m)-1][1]
	}
	if pick == "" {
		return 0, fmt.Errorf("models: planner reply contained no element id: %q", reply)
	}
	id, err := strconv.Atoi(pick)
	if err != nil {
		return 0, fmt.Errorf("models: planner element id %q is not an integer: %w", pick, err)
	}
	if id < 0 || id >= n {
		return 0, fmt.Errorf("models: planner chose element id %d, out of range [0,%d): %q", id, n, reply)
	}
	return id, nil
}
