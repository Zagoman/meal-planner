package claude

import (
	"context"
	"errors"
	"sync"
)

// Fake is a scripted Client for tests. Each call pops the next reply and
// records the request.
type Fake struct {
	mu       sync.Mutex
	replies  []FakeReply
	Requests []Request
}

// FakeReply is either a JSON body or an error.
type FakeReply struct {
	JSON  string
	Err   error
	Usage Usage
}

// ErrNoReply is returned when the fake runs out of scripted replies.
var ErrNoReply = errors.New("claude.Fake: no scripted reply left")

func NewFake(replies ...FakeReply) *Fake { return &Fake{replies: replies} }

// Push appends more scripted replies.
func (f *Fake) Push(r ...FakeReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, r...)
}

func (f *Fake) Generate(ctx context.Context, req Request) (Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, req)
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if len(f.replies) == 0 {
		return Response{}, ErrNoReply
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	if r.Err != nil {
		return Response{}, r.Err
	}
	u := r.Usage
	if u == (Usage{}) {
		u = Usage{InputTokens: 1000, OutputTokens: 500}
	}
	return Response{JSON: []byte(r.JSON), Model: "fake", Usage: u}, nil
}

// Calls returns how many requests were made.
func (f *Fake) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Requests)
}
