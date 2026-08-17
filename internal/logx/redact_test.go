package logx_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/logx"
)

const secret = "sk-ant-api03-not-a-real-key"

// credentialShapes is how many credential-carrying attributes emit writes.
const credentialShapes = 6

// emit logs one record per way an attribute can carry a credential: directly,
// inside an inline group, through With, under WithGroup, and behind a LogValuer
// that resolves to a group.
//
// The fourth is the one that looks redundant and is not: a logger derived with
// With keeps redacting the attributes it was derived with even when the wrapper
// is dropped, because those were cleaned on the way in. Only a credential
// logged on the derived logger *afterwards* shows the loss.
func emit(logger *slog.Logger) {
	logger.Info("plain", "api_key", secret)
	logger.Info("inline group", slog.Group("provider", "authorization", secret))
	logger.With("x-api-key", secret).Info("with")
	logger.With("component", "llm").Info("derived", "password", secret)
	logger.WithGroup("mcp").With("token", secret).Info("with group")
	logger.Info("valuer", "credentials", lazy{})
}

// lazy resolves to a group, so redaction has to run after resolution.
type lazy struct{}

func (lazy) LogValue() slog.Value {
	return slog.GroupValue(slog.String("password", secret))
}

func TestCredentialAttributesAreRedacted(t *testing.T) {
	lg, path := logTo(t, nil)
	emit(lg.Logger)
	written := read(t, path)

	if found := strings.Count(written, secret); found != 0 {
		t.Errorf("the key reached the log %d times:\n%s", found, written)
	}
	if hidden := strings.Count(written, logx.Redacted); hidden != credentialShapes {
		t.Errorf("%d values were redacted, want %d:\n%s", hidden, credentialShapes, written)
	}
	if len(records(t, path)) != credentialShapes {
		t.Errorf("a record went missing:\n%s", written)
	}
}

// TestAnUnwrappedHandlerShowsTheKey is the negative control for the test above:
// it runs the same records through a plain JSON handler and insists the key is
// visible. Without it, a search that could never find the key — a mistyped
// constant, a logger writing nowhere — reads exactly like redaction working.
func TestAnUnwrappedHandlerShowsTheKey(t *testing.T) {
	var buf bytes.Buffer
	emit(slog.New(slog.NewJSONHandler(&buf, nil)))

	if found := strings.Count(buf.String(), secret); found != credentialShapes {
		t.Errorf("the key is visible %d times without redaction, want %d — the check "+
			"above cannot fail:\n%s", found, credentialShapes, buf.String())
	}
}

func TestKeySpellings(t *testing.T) {
	hidden := []string{"api_key", "apiKey", "API-KEY", "anthropic_api_key", "authorization",
		"Authorization", "x-api-key", "X-Api-Key", "token", "access_token", "password"}
	kept := []string{"input_tokens", "tokens", "user", "message", "api_key_path"}

	lg, path := logTo(t, nil)
	for _, key := range hidden {
		lg.Logger.Info("credential", key, secret)
	}
	for _, key := range kept {
		lg.Logger.Info("not a credential", key, "visible-"+key)
	}

	// Per key rather than "the secret is nowhere in the file": a spelling that
	// stopped producing a record at all would satisfy absence.
	got := records(t, path)
	if len(got) != len(hidden)+len(kept) {
		t.Fatalf("%d records for %d keys — one wrote nothing", len(got), len(hidden)+len(kept))
	}
	for i, key := range hidden {
		if got[i][key] != logx.Redacted {
			t.Errorf("%s is %v, want %s", key, got[i][key], logx.Redacted)
		}
	}
	for i, key := range kept {
		if got[len(hidden)+i][key] != "visible-"+key {
			t.Errorf("%s is %v, want it left alone — only a trailing match is a credential",
				key, got[len(hidden)+i][key])
		}
	}
}

func TestRedactionKeepsTheRestOfTheRecord(t *testing.T) {
	lg, path := logTo(t, nil)
	lg.Logger.Info("calling", "provider", "anthropic", "api_key", secret, "attempt", 2)

	rec := records(t, path)[0]
	if rec["msg"] != "calling" || rec["provider"] != "anthropic" || rec["attempt"] != float64(2) {
		t.Errorf("attributes around the credential were lost: %v", rec)
	}
	if rec["api_key"] != logx.Redacted {
		t.Errorf("api_key is %v, want %s", rec["api_key"], logx.Redacted)
	}
}
