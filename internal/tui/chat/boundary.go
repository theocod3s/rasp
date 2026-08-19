package chat

import "strings"

// codeIndent is the column at which a line becomes an indented code block, and
// equally the indent that keeps a line inside the list item above it.
const codeIndent = 4

// stableBoundary returns how many bytes of src can be rendered on their own and
// come out exactly as they would at the head of the finished message, or 0 when
// no such prefix can be proven.
//
// A boundary sits after a blank line with nothing open above it: no fence,
// list, table, quote, indented code or paragraph that a later line could still
// reach back into and re-read (internals §4.4). The scan is deliberately
// one-sided — it proves a prefix safe or says nothing — so every construct it
// does not understand ends up rendered whole, which is slow and never wrong.
func stableBoundary(src string) int {
	var (
		boundary int
		fence    string // the run of ` or ~ that opened the block being scanned
		// carry says the block last seen can still be rewritten by what follows:
		// a list turns loose when another item arrives, an indented code block
		// resumes across blank lines. Neither may sit at the end of a prefix.
		carry     bool
		prevBlank = true
	)

	for off := 0; off < len(src); {
		line := src[off:]
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line, off = line[:i], off+i+1
		} else {
			off = len(src)
		}

		if fence != "" {
			if closesFence(line, fence) {
				fence = ""
			}
			continue
		}

		body := strings.TrimLeft(line, " \t")
		if body == "" {
			if !carry {
				boundary = off
			}
			prevBlank = true
			continue
		}

		switch {
		case indentOf(line) >= codeIndent:
			// Four columns in after a blank line starts an indented code block or
			// continues the list item above; without one it continues whatever the
			// line above was, and changes nothing.
			carry = carry || prevBlank
		case unsplittable(body):
			return 0
		case isQuote(body), isListItem(body), isTableRule(body):
			carry = true
		default:
			// A block hard against the left margin after a blank line cannot be a
			// continuation of anything above it, so whatever was open is closed.
			// A fence three columns in is a different matter: it can be a list
			// item's own code, and the list is still open around it.
			if prevBlank && indentOf(line) == 0 {
				carry = false
			}
			fence = opensFence(body)
		}
		prevBlank = false
	}
	return boundary
}

// unsplittable reports a line that no boundary in the message can survive, so
// that the whole of it goes through one render.
//
// A link reference or footnote definition turns brackets *earlier* in the
// message into links, and a definition-list marker turns the paragraph above it
// into a term: both make a prefix render differently once they arrive, whatever
// the boundary said when it was taken. Raw HTML is a different failure — glamour
// draws it with none of the spacing it puts between blocks, so a seam in front
// of one is a blank line the whole document does not have.
//
// All three are rare in a reply, and the cost of meeting one is a render rather
// than a wrong frame.
func unsplittable(body string) bool {
	switch body[0] {
	case '<':
		return true
	case '[':
		return strings.Contains(body, "]:")
	case ':':
		return len(body) == 1 || body[1] == ' ' || body[1] == '\t'
	}
	return false
}

func isQuote(body string) bool { return body[0] == '>' }

func isListItem(body string) bool {
	if strings.IndexByte("-+*", body[0]) >= 0 {
		return len(body) == 1 || body[1] == ' ' || body[1] == '\t'
	}
	digits := 0
	for digits < len(body) && body[digits] >= '0' && body[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits > 9 || digits == len(body) {
		return false
	}
	if body[digits] != '.' && body[digits] != ')' {
		return false
	}
	rest := body[digits+1:]
	return rest == "" || rest[0] == ' ' || rest[0] == '\t'
}

// isTableRule reports the delimiter row under a table's header. A table cannot
// exist without one, so finding it is the cheapest way to know a run of lines
// with pipes in them is a table rather than prose about a shell pipeline.
func isTableRule(body string) bool {
	var pipe, dash bool
	for i := range len(body) {
		switch body[i] {
		case '|':
			pipe = true
		case '-':
			dash = true
		case ':', ' ', '\t':
		default:
			return false
		}
	}
	return pipe && dash
}

// opensFence returns the marker a fence opens with, or "" for any other line.
func opensFence(body string) string {
	if body[0] != '`' && body[0] != '~' {
		return ""
	}
	n := 0
	for n < len(body) && body[n] == body[0] {
		n++
	}
	if n < 3 {
		return ""
	}
	// A backtick anywhere in the info string means this is inline code split
	// across the line, not a fence.
	if body[0] == '`' && strings.ContainsRune(body[n:], '`') {
		return ""
	}
	return body[:n]
}

func closesFence(line, fence string) bool {
	if indentOf(line) >= codeIndent {
		return false
	}
	body := strings.TrimLeft(line, " \t")
	n := 0
	for n < len(body) && body[n] == fence[0] {
		n++
	}
	return n >= len(fence) && strings.TrimRight(body[n:], " \t") == ""
}

// indentOf counts the columns a line is indented by, tabs included, and stops
// counting at the one column that decides anything.
func indentOf(line string) int {
	n := 0
	for i := 0; i < len(line) && n < codeIndent; i++ {
		switch line[i] {
		case ' ':
			n++
		case '\t':
			n += codeIndent - n%codeIndent
		default:
			return n
		}
	}
	return n
}
