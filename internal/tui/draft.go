package tui

import (
	"strings"
	"unicode/utf8"
)

// draft is the message being composed: the text, and the byte offset the caret
// sits at. One type rather than two fields on the model because the two have to
// agree — an offset left over from text that has since changed slices a string
// mid-rune or out of range — and every method here moves both halves together.
//
// Columns are counted in runes rather than terminal cells. What up and down have
// to preserve is the character the caret was over, and a cell count would put it
// somewhere else on a line holding a double-width one.
type draft struct {
	text string
	at   int
}

func (d draft) empty() bool { return d.text == "" }

// insert puts s at the caret and leaves the caret after it — the whole of what
// a keystroke and a bracketed paste each do to the line.
func (d draft) insert(s string) draft {
	if s == "" {
		return d
	}
	d.text = d.text[:d.at] + s + d.text[d.at:]
	d.at += len(s)
	return d
}

func (d draft) backspace() draft {
	if d.at == 0 {
		return d
	}
	_, width := utf8.DecodeLastRuneInString(d.text[:d.at])
	d.text = d.text[:d.at-width] + d.text[d.at:]
	d.at -= width
	return d
}

// left and right step over a line break as they step over any other rune, so a
// caret run off the front of a line lands on the end of the one above it.
func (d draft) left() draft {
	if d.at == 0 {
		return d
	}
	_, width := utf8.DecodeLastRuneInString(d.text[:d.at])
	d.at -= width
	return d
}

func (d draft) right() draft {
	if d.at == len(d.text) {
		return d
	}
	_, width := utf8.DecodeRuneInString(d.text[d.at:])
	d.at += width
	return d
}

// up and down hold the column where the next line is long enough for it and
// stop at that line's end where it is not. Neither moves at the edge of the
// draft: on the first line, up leaves the caret where the reader put it rather
// than jumping it to the front.
func (d draft) up() draft {
	start, _ := d.line()
	if start == 0 {
		return d
	}
	column := utf8.RuneCountInString(d.text[start:d.at])
	above, aboveEnd := lineOf(d.text, start-1)
	d.at = offsetIn(d.text, above, aboveEnd, column)
	return d
}

func (d draft) down() draft {
	start, end := d.line()
	if end == len(d.text) {
		return d
	}
	column := utf8.RuneCountInString(d.text[start:d.at])
	below, belowEnd := lineOf(d.text, end+1)
	d.at = offsetIn(d.text, below, belowEnd, column)
	return d
}

func (d draft) home() draft {
	d.at, _ = d.line()
	return d
}

func (d draft) end() draft {
	_, d.at = d.line()
	return d
}

// prefix is the caret's own line up to it. Measured in terminal cells that is
// the column the cursor stands in, and the byte offset it is cut at is
// something only this type holds (cursor.go).
func (d draft) prefix() string {
	start, _ := d.line()
	return d.text[start:d.at]
}

// below is how many lines of the draft sit under the caret's own.
func (d draft) below() int { return strings.Count(d.text[d.at:], "\n") }

// line is the caret's own line: the offset of its first byte, and of the break
// that ends it.
func (d draft) line() (start, end int) { return lineOf(d.text, d.at) }

func lineOf(s string, at int) (start, end int) {
	start = strings.LastIndexByte(s[:at], '\n') + 1
	if next := strings.IndexByte(s[at:], '\n'); next >= 0 {
		return start, at + next
	}
	return start, len(s)
}

// offsetIn is the offset column runes into s[start:end], or end when that is
// fewer runes than column.
func offsetIn(s string, start, end, column int) int {
	for range column {
		if start >= end {
			break
		}
		_, width := utf8.DecodeRuneInString(s[start:end])
		start += width
	}
	return start
}
