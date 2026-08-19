package openaicompat

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/theocod3s/rasp/internal/llm"
)

// Config is one endpoint. Strings rather than SDK options, so nothing above this
// package imports the SDK to build a provider.
type Config struct {
	// ProviderID is the name this endpoint answers to in config and in a session
	// file — "openrouter", "ollama". Configuration rather than a constant: one
	// adapter serves many endpoints, and a session has to say which one it recorded.
	ProviderID string

	APIKey string

	// BaseURL is required, unlike the Anthropic adapter's: left empty the SDK falls
	// back to OPENAI_BASE_URL and then to api.openai.com, so a missing one sends the
	// conversation to OpenAI under a provider named "ollama" rather than failing.
	BaseURL string
}

// Client is one OpenAI-compatible endpoint.
type Client struct {
	cfg Config
	api sdk.Client
}

var _ llm.Provider = (*Client)(nil)

func New(cfg Config) *Client {
	// Retries are llm/retry's (design §12): its sleep is interruptible by the turn's
	// context and it refuses a Retry-After above its cap instead of sleeping through
	// it, where the SDK's timers ignore cancellation and honour whatever delay a
	// provider names. Left on they would also multiply with tier 1's attempts.
	opts := []option.RequestOption{option.WithMaxRetries(0)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	} else {
		// The SDK reads OPENAI_API_KEY into an Authorization header of its own, and
		// this adapter points at somebody else's server by definition — so leaving
		// that chain alone, as the Anthropic adapter does, hands an OpenAI credential
		// to OpenRouter or to a LAN address. The delete also marks the request as
		// carrying its own auth, which is what stops the SDK putting the ambient key
		// back after every option has been applied.
		opts = append(opts, option.WithHeaderDel("Authorization"))
	}
	return &Client{cfg: cfg, api: sdk.NewClient(opts...)}
}

func (c *Client) ID() string { return c.cfg.ProviderID }

// Stream runs one model call. A request this adapter refuses to build, a non-2xx
// response and a connection dropped mid-message all leave through the terminal
// EventError, which is why there is no error return to write to.
func (c *Client) Stream(ctx context.Context, req llm.Request) llm.StreamResponse {
	return func(yield func(llm.Event) bool) {
		// Allocated before anything can yield: Partial is this pointer on every
		// event. Moving it inside the loop still satisfies the contract's letter
		// and costs an allocation per token (design §3.1).
		msg := &llm.Message{Role: llm.RoleAssistant, Model: req.Model, Provider: c.cfg.ProviderID}

		if err := c.check(); err != nil {
			yield(c.fail(msg, err))
			return
		}
		params, err := buildParams(req)
		if err != nil {
			yield(c.fail(msg, err))
			return
		}

		stream := c.api.Chat.Completions.NewStreaming(ctx, params)
		defer stream.Close()

		var (
			acc       sdk.ChatCompletionAccumulator
			shape     = newShape()
			pending   calls
			truncated bool
		)
		for stream.Next() {
			chunk := stream.Current()
			if !acc.AddChunk(chunk) {
				// AddChunk refuses exactly one thing: a chunk whose id names a different
				// response, which reassembled would splice two completions into one
				// message.
				yield(c.fail(msg, fmt.Errorf("chunk %q does not belong to response %q", chunk.ID, acc.ID)))
				return
			}
			if finishReason(chunk) == wireLength {
				truncated = true
			}

			// Read before this chunk is projected: the call it reports finished is an
			// earlier one, whose fragments all arrived in earlier chunks.
			if fin, ok := acc.JustFinishedToolCall(); ok && !truncated {
				if at, open := shape.tools[fin.Index]; open {
					pending.finish(at)
				}
			}
			for _, call := range pending.ready(msg) {
				if !yield(llm.Event{Type: llm.EventToolCall, ToolCall: call, Partial: msg}) {
					return
				}
			}

			done, err := shape.project(msg, &acc, chunk, yield)
			if err != nil {
				yield(c.fail(msg, err))
				return
			}
			if done {
				return
			}
		}

		// NewStreaming runs the request before it returns and hands the result — a
		// response body or an error — to the stream it builds, which is why a 401, a
		// 429 and a half-delivered message all surface in this one place.
		if err := stream.Err(); err != nil {
			yield(c.fail(msg, err))
			return
		}

		// The sweep the state machine cannot replace. JustFinishedToolCall fires on a
		// transition, so a dialect whose last chunk carries the final fragment AND the
		// finish reason never transitions — and a chunk carrying content beside
		// tool_calls is classified as content, losing the transition the same way.
		// Either leaves a model asking for a tool that nothing runs. Truncation stays
		// unannounced (design §4 invariant 2): those arguments can parse and still mean
		// something else.
		if !truncated {
			pending.finishAll()
			for _, call := range pending.ready(msg) {
				if !yield(llm.Event{Type: llm.EventToolCall, ToolCall: call, Partial: msg}) {
					return
				}
			}
		}

		ev, err := terminalEvent(msg, &acc, shape)
		if err != nil {
			yield(c.fail(msg, err))
			return
		}
		yield(ev)
	}
}

// check holds what New cannot report, New having no error to return. Neither field
// has a safe default, so a half-configured provider fails before the network.
func (c *Client) check() error {
	switch {
	case c.cfg.ProviderID == "":
		return errors.New("this OpenAI-compatible provider has no id; it is what a session file " +
			"records the turn against, and one adapter serves many endpoints")
	case c.cfg.BaseURL == "":
		return fmt.Errorf("no endpoint: set providers.%s.base_url. Without one the request would go "+
			"to api.openai.com under this provider's name, and read as working", c.cfg.ProviderID)
	}
	return nil
}

// fail names the endpoint in every error this adapter produces. One adapter serves
// many, so "429" with no name does not say which one is rate-limiting the turn.
func (c *Client) fail(msg *llm.Message, err error) llm.Event {
	if c.cfg.ProviderID != "" {
		err = fmt.Errorf("%s: %w", c.cfg.ProviderID, err)
	}
	return errorEvent(msg, err)
}
