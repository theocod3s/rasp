package anthropic

import (
	"context"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/theocod3s/rasp/internal/llm"
)

// ProviderID is what this adapter answers to in config and in a session file.
const ProviderID = "anthropic"

// Config is everything the adapter needs to reach the API. Strings rather than
// SDK options, so nothing above this package imports the SDK to build a provider.
type Config struct {
	APIKey string

	// BaseURL overrides Anthropic's own. Empty is the normal case; tests point it
	// at a server replaying recorded frames.
	BaseURL string
}

// Client is the Anthropic provider.
type Client struct {
	api sdk.Client
}

var _ llm.Provider = (*Client)(nil)

func New(cfg Config) *Client {
	// Retries are llm/retry's (design §12): its sleep is interruptible by the turn's
	// context and it refuses a Retry-After above its cap instead of sleeping through
	// it, where the SDK's timers ignore cancellation and honour whatever delay a
	// provider names. Left on they would also multiply with tier 1's attempts.
	opts := []option.RequestOption{option.WithMaxRetries(0)}
	if cfg.APIKey != "" {
		// Applied unconditionally, an empty key would send an empty X-Api-Key and
		// suppress the SDK's credential chain, so "nothing configured" would reach
		// the user as an opaque 401 with an ambient token left unconsulted.
		//
		// The header delete is the other half. NewClient reads ANTHROPIC_AUTH_TOKEN
		// into an Authorization header of its own, and WithAPIKey only adds X-Api-Key
		// beside it; the server rejects a request carrying both. Anyone using a
		// gateway exports that variable, so without this a configured key fails every
		// turn with an error naming neither credential.
		opts = append(opts, option.WithAPIKey(cfg.APIKey), option.WithHeaderDel("Authorization"))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Client{api: sdk.NewClient(opts...)}
}

func (c *Client) ID() string { return ProviderID }

// Stream runs one model call. A request this adapter refuses to build, a non-2xx
// response and a connection dropped mid-message all leave through the terminal
// EventError, which is why there is no error return to write to.
func (c *Client) Stream(ctx context.Context, req llm.Request) llm.StreamResponse {
	return func(yield func(llm.Event) bool) {
		// Allocated before anything can yield: Partial is this pointer on every
		// event. Moving it inside the loop still satisfies the contract's letter
		// and costs an allocation per token (design §3.1).
		msg := &llm.Message{Role: llm.RoleAssistant, Model: req.Model, Provider: ProviderID}

		params, err := buildParams(req)
		if err != nil {
			yield(errorEvent(msg, err))
			return
		}

		stream := c.api.Messages.NewStreaming(ctx, params)
		defer stream.Close()

		var acc sdk.Message
		for stream.Next() {
			event := stream.Current()
			if err := acc.Accumulate(event); err != nil {
				yield(errorEvent(msg, fmt.Errorf("anthropic: %w", err)))
				return
			}
			if err := project(msg, &acc); err != nil {
				yield(errorEvent(msg, err))
				return
			}

			ev, ok := neutralEvent(event)
			if !ok {
				// A wire event with no neutral counterpart: a block opening or
				// closing, a signature fragment, the stop reason. It moved Partial,
				// which is all the contract asks of it.
				continue
			}
			ev.Partial = msg
			if !yield(ev) {
				return
			}
		}

		// NewStreaming runs the request before it returns and hands the result — a
		// response body or an error — to the stream it builds, which is why a 401, a
		// 429 and a half-delivered message all surface in this one place rather than
		// splitting across a return value and a loop.
		if err := stream.Err(); err != nil {
			yield(errorEvent(msg, fmt.Errorf("anthropic: %w", err)))
			return
		}
		yield(terminalEvent(msg, acc.StopReason))
	}
}
