// Package claude is the boundary to the Claude API. The planner depends only
// on the Client interface; tests always use Fake, never the real API.
//
// The real implementation (anthropic.go, package B) uses the official
// Anthropic Go SDK, reads ANTHROPIC_API_KEY and CLAUDE_MODEL from the
// environment via config, and enforces Request.Schema with structured output
// or a forced tool call.
package claude

import (
	"context"
	"encoding/json"
)

// Message is one conversation turn. Role is "user" or "assistant".
type Message struct {
	Role    string
	Content string
	// Images go before the text (user messages only), e.g. a photo of a
	// cookbook page.
	Images []Image
}

// Image is a JPEG, PNG, GIF or WebP image sent with a message.
type Image struct {
	MediaType string // "image/jpeg"
	Data      []byte
}

// Request asks for one JSON document matching Schema.
type Request struct {
	System    string
	Messages  []Message
	Schema    json.RawMessage
	MaxTokens int
}

// Usage is the token count of one call, logged per household.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Response is the raw JSON Claude produced plus usage.
type Response struct {
	JSON  json.RawMessage
	Model string
	Usage Usage
}

type Client interface {
	Generate(ctx context.Context, req Request) (Response, error)
}
