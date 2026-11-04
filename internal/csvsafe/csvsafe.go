// Package csvsafe keeps spreadsheet programs from reading a CSV cell as a
// formula. It is shared by the audit CSV, the deletion preview and the GoBD
// export.
package csvsafe

import "strings"

// formulaStart are the first characters that make Excel, LibreOffice and
// IDEA read a cell as a formula (or, for TAB and CR, hide such a start).
const formulaStart = "=+-@\t\r"

// Cell returns c with a leading ' when it starts with one of = + - @ TAB
// CR, so that a spreadsheet shows it as text. Any other value is returned
// unchanged.
func Cell(c string) string {
	if NeedsQuote(c) {
		return "'" + c
	}
	return c
}

// NeedsQuote reports whether Cell would add the leading '.
func NeedsQuote(c string) bool {
	return c != "" && strings.IndexByte(formulaStart, c[0]) >= 0
}
