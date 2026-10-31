package attachtext

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"github.com/pklnx/mail-archive/internal/mime"
)

// plainText returns the text of a text or HTML attachment and its status.
// go-message has already decoded a declared charset to UTF-8; text without
// a charset that is not valid UTF-8 is read as Windows-1252, which is what
// German spreadsheet exports usually are.
func plainText(data []byte, charset string, html bool, maxText int) (string, Status) {
	s := string(data)
	if charset == "" && !utf8.ValidString(s) {
		if dec, err := charmap.Windows1252.NewDecoder().String(s); err == nil {
			s = dec
		}
	}
	s = cleanText(s)
	if html {
		s = mime.HTMLToText(s)
	}
	switch {
	case strings.TrimSpace(s) == "":
		return "", StatusNoText
	case len(s) > maxText:
		return truncateText(s, maxText), StatusTruncated
	}
	return s, StatusOK
}
