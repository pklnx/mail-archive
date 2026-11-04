package csvsafe

import "testing"

func TestCell(t *testing.T) {
	cases := map[string]string{
		"":           "",
		"plain":      "plain",
		"=SUM(A1)":   "'=SUM(A1)",
		"+49 30":     "'+49 30",
		"-50 %":      "'-50 %",
		"@cmd":       "'@cmd",
		"\tx":        "'\tx",
		"\rx":        "'\rx",
		"a=b":        "a=b",
		"'quoted":    "'quoted",
		"ünïcode":    "ünïcode",
		" =leading":  " =leading",
		"\n=newline": "\n=newline",
	}
	for in, want := range cases {
		if got := Cell(in); got != want {
			t.Errorf("Cell(%q) = %q, want %q", in, got, want)
		}
	}
}
