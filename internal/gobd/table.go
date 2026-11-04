package gobd

import (
	"bufio"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pklnx/mail-archive/internal/csvsafe"
)

// scanTable reads a CSV table of format v1: the exact header line, then
// one record per CRLF-terminated line. Every record that has the columns
// and forms of cols goes to fn with its values (text without quotes);
// every other line goes to bad. It holds one line (at most maxLine bytes)
// in memory. The error is a read error only.
func scanTable(r io.Reader, cols []column, fn func(line int, vals []string), bad func(line int, why string)) error {
	br := bufio.NewReaderSize(r, maxLine)
	want := header(cols)
	vals := make([]string, len(cols))
	for n := 1; ; n++ {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			bad(n, "line too long")
			if err := skipLine(br); errors.Is(err, io.EOF) {
				return nil
			} else if err != nil {
				return err
			}
			continue
		}
		if errors.Is(err, io.EOF) {
			switch {
			case len(line) > 0:
				bad(n, "incomplete line (no CRLF)")
			case n == 1:
				bad(n, "empty file (no header)")
			}
			return nil
		}
		if err != nil {
			return err
		}
		s, ok := strings.CutSuffix(string(line), "\r\n")
		switch {
		case n == 1 && string(line) != want:
			bad(n, "header differs from format v1")
		case n == 1:
		case !ok:
			bad(n, "line does not end with CRLF")
		default:
			if why := splitRecord(s, cols, vals); why != "" {
				bad(n, why)
				continue
			}
			fn(n, vals)
		}
	}
}

func skipLine(br *bufio.Reader) error {
	for {
		_, err := br.ReadSlice('\n')
		if !errors.Is(err, bufio.ErrBufferFull) {
			return err
		}
	}
}

// splitRecord splits a record into vals and checks each value; it returns
// why the record is malformed, or "".
func splitRecord(s string, cols []column, vals []string) string {
	for i, c := range cols {
		if i > 0 {
			if s == "" || s[0] != ';' {
				return "too few values"
			}
			s = s[1:]
		}
		var v string
		if c.kind == text {
			if s == "" || s[0] != '"' {
				return c.name + ": text value without quotes"
			}
			end := strings.IndexByte(s[1:], '"')
			if end < 0 {
				return c.name + ": unterminated quote"
			}
			v, s = s[1:1+end], s[2+end:]
			if !validText(v) {
				return c.name + ": value not written by format v1 (control character, unquoted formula or too long)"
			}
		} else {
			end := strings.IndexByte(s, ';')
			if end < 0 {
				end = len(s)
			}
			v, s = s[:end], s[end:]
			if !validPlain(v, c) {
				return c.name + ": not a valid " + kindName(c.kind)
			}
		}
		if !validForm(v, c.form) {
			return c.name + ": malformed value"
		}
		vals[i] = v
	}
	if s != "" {
		return "too many values"
	}
	return ""
}

func kindName(k kind) string {
	if k == date {
		return "date (DD.MM.YYYY)"
	}
	return "integer"
}

// validText: what textValue can produce.
func validText(v string) bool {
	if len(v) > MaxValueBytes+1 || !utf8.ValidString(v) || csvsafe.NeedsQuote(v) {
		return false
	}
	return !strings.ContainsFunc(v, func(r rune) bool {
		return unicode.IsControl(r) || r == ' ' || r == ' '
	})
}

func validPlain(v string, c column) bool {
	switch {
	case v == "":
		return c.optional
	case c.kind == date:
		t, err := time.Parse("02.01.2006", v)
		return err == nil && t.Format("02.01.2006") == v
	}
	if len(v) > 19 || !allDigits(v) || (len(v) > 1 && v[0] == '0') {
		return false
	}
	_, err := strconv.ParseInt(v, 10, 64)
	return err == nil
}

func validForm(v string, f form) bool {
	switch f {
	case formSHA:
		return isSHA(v)
	case formClock:
		t, err := time.Parse("15:04:05", v)
		return err == nil && t.Format("15:04:05") == v
	case formUTC:
		return validUTC(v)
	case formUTCOpt:
		return v == "" || validUTC(v)
	case formBool:
		return v == "0" || v == "1"
	case formIDs:
		if v == "" {
			return true
		}
		for _, id := range strings.Split(v, " ") {
			if !allDigits(id) || id[0] == '0' || len(id) > 18 {
				return false
			}
		}
		return true
	case formNonEmpty:
		return v != ""
	}
	return true
}

func validUTC(v string) bool {
	t, err := time.Parse("2006-01-02T15:04:05Z", v)
	return err == nil && formatUTC(t) == v
}
