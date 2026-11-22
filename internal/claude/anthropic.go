package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// Anthropic is the real Client, backed by the official Anthropic Go SDK. It
// is constructed only in cmd/server; tests use Fake.
//
// Request.Schema is enforced with structured outputs (output_config.format),
// which works on every current model, including the ones that reject a
// forced tool_choice. The API supports a subset of JSON Schema, so the
// schema is reduced first (see APISchema); the caller still validates the
// answer against the full schema.
type Anthropic struct {
	client anthropic.Client
	model  string
}

// NewAnthropic returns a client for model. The key is passed explicitly so
// the SDK never falls back to other credentials in the environment.
func NewAnthropic(apiKey, model string) *Anthropic {
	return &Anthropic{
		client: anthropic.NewClient(
			option.WithAPIKey(apiKey),
			// One quick retry on 429/5xx; the caller's context (55 s) bounds the total.
			option.WithMaxRetries(1),
			option.WithRequestTimeout(50*time.Second),
		),
		model: model,
	}
}

// ErrIncomplete is returned when Claude stopped before finishing the JSON
// (max_tokens) or declined (refusal). Usage is still reported.
var ErrIncomplete = errors.New("claude: incomplete answer")

func (a *Anthropic) Generate(ctx context.Context, req Request) (Response, error) {
	schema, err := APISchema(req.Schema)
	if err != nil {
		return Response{}, err
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16000
	}
	msgs := make([]anthropic.MessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		block := anthropic.NewTextBlock(m.Content)
		switch m.Role {
		case "user":
			msgs = append(msgs, anthropic.NewUserMessage(block))
		case "assistant":
			msgs = append(msgs, anthropic.NewAssistantMessage(block))
		default:
			return Response{}, fmt.Errorf("claude: unknown message role %q", m.Role)
		}
	}
	params := anthropic.MessageNewParams{
		Model:        a.model,
		MaxTokens:    int64(maxTokens),
		Messages:     msgs,
		OutputConfig: anthropic.OutputConfigParam{Format: anthropic.JSONOutputFormatParam{Schema: schema}},
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}

	msg, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("claude: messages.new: %w", err)
	}
	out := Response{
		Model: msg.Model,
		Usage: Usage{InputTokens: int(msg.Usage.InputTokens), OutputTokens: int(msg.Usage.OutputTokens)},
	}
	switch msg.StopReason {
	case anthropic.StopReasonMaxTokens:
		return out, fmt.Errorf("%w: hit max_tokens (%d)", ErrIncomplete, maxTokens)
	case anthropic.StopReasonRefusal:
		return out, fmt.Errorf("%w: refused (%s)", ErrIncomplete, msg.StopDetails.Category)
	}
	var text strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	out.JSON = json.RawMessage(strings.TrimSpace(text.String()))
	if len(out.JSON) == 0 {
		return out, fmt.Errorf("%w: no text in the answer (stop reason %s)", ErrIncomplete, msg.StopReason)
	}
	return out, nil
}

// unsupportedKeywords are JSON Schema keywords the structured-output API
// rejects or ignores; they are checked by the caller's full validation.
var unsupportedKeywords = []string{
	"$schema", "$id", "title",
	"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
	"minLength", "maxLength", "pattern", "format",
	"maxItems", "uniqueItems", "minProperties", "maxProperties",
}

// APISchema converts a JSON schema to the subset structured outputs accept:
// it drops numeric/string/array constraints (minItems is kept only as 0 or
// 1), and rewrites "type": ["x", "null"] as anyOf.
func APISchema(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, errors.New("claude: request has no schema")
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("claude: schema: %w", err)
	}
	return reduceSchema(root), nil
}

func reduceSchema(s map[string]any) map[string]any {
	out := make(map[string]any, len(s))
	for k, v := range s {
		out[k] = v
	}
	for _, k := range unsupportedKeywords {
		delete(out, k)
	}
	if n, ok := out["minItems"].(float64); ok && n > 1 {
		delete(out, "minItems")
	}
	if types, ok := out["type"].([]any); ok {
		delete(out, "type")
		var anyOf []any
		for _, t := range types {
			anyOf = append(anyOf, map[string]any{"type": t})
		}
		out["anyOf"] = anyOf
	}
	for _, key := range []string{"items", "additionalProperties", "not"} {
		if sub, ok := out[key].(map[string]any); ok {
			out[key] = reduceSchema(sub)
		}
	}
	for _, key := range []string{"properties", "$defs", "definitions"} {
		if props, ok := out[key].(map[string]any); ok {
			np := make(map[string]any, len(props))
			for name, p := range props {
				if pm, ok := p.(map[string]any); ok {
					np[name] = reduceSchema(pm)
				} else {
					np[name] = p
				}
			}
			out[key] = np
		}
	}
	for _, key := range []string{"anyOf", "allOf", "oneOf"} {
		if list, ok := out[key].([]any); ok {
			nl := make([]any, len(list))
			for i, p := range list {
				if pm, ok := p.(map[string]any); ok {
					nl[i] = reduceSchema(pm)
				} else {
					nl[i] = p
				}
			}
			out[key] = nl
		}
	}
	return out
}
