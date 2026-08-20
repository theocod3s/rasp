package diffview_test

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestProbeTruncate(t *testing.T) {
	s := " func (m *Middleware) claims(r *http.Request) (Claims, error) {"
	for _, n := range []int{27, 26, 10} {
		got := ansi.Truncate(s, n, "")
		t.Logf("n=%d len=%d width=%d %q", n, len(got), ansi.StringWidth(got), got)
	}
	t.Logf("width of elision = %d", ansi.StringWidth("…"))
	t.Logf("width of full = %d", ansi.StringWidth(s))
	t.Logf("wide: %q -> %q", "abc漢漢漢", ansi.Truncate("abc漢漢漢", 5, ""))
}
