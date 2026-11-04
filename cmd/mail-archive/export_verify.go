package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/gobd"
)

func newExportVerifyCmd() *cobra.Command {
	var publicKeys []string
	var attestations, expectGroup string
	cmd := &cobra.Command{
		Use:   "verify PATH [--public-key FILE ...] [--attestations FILE] [--expect-group NAME]",
		Short: "Check a GoBD export, a directory or a ZIP, offline",
		Long: `Check a GoBD export (format v1), a directory or a ZIP, without a database,
configuration or key: every file against SHA256SUMS, SHA256SUMS against the
signed STATEMENT.txt, the tables against each other and the statement, the
attestation it names, and the customer group and owners.

A ZIP is checked for entries an unpacker could misread: duplicate names (also
names that differ only in case or Unicode normalization), names outside the
format, "..", absolute paths, backslashes, directory or symbolic link entries
and entries larger than the tables say.

--public-key trusts only the keys in the given PEM files (repeatable): pass
the copy of signing.pub kept off the archive's host, never the one in the
export's attest/ directory, which anyone who changed the export could have
replaced. Without it, the keys in attest/ are trusted, which shows only that
the export is consistent.

--attestations compares the attestation in the export with your off-host copy
of attestations.jsonl (with a full chain: that it is a prefix of the copy).
Leaves files in a leaves/ directory next to the copy are used to check every
exported message when the export has none.

--expect-group checks that the statement names this customer group (case is
ignored; "none" for an export of accounts without an owner).

Exit codes: 0 nothing found, 1 findings, 2 the export cannot be opened or a
usage error.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError(cmd, fmt.Errorf("give the export's path (a directory or a .zip), got %d argument(s)", len(args)))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			trusted, err := loadPublicKeys(publicKeys)
			if err != nil {
				return usageError(cmd, err)
			}
			opts := gobd.VerifyOptions{Trusted: trusted, Attestations: attestations}
			if cmd.Flags().Changed("expect-group") {
				opts.ExpectGroup = &expectGroup
			}
			code, err := runExportVerify(args[0], opts, cmd.OutOrStdout())
			if err != nil {
				return usageError(cmd, err)
			}
			if code != 0 {
				return &exitError{msg: "export verify found problems", code: code}
			}
			return nil
		},
	}
	cmd.SetFlagErrorFunc(usageError)
	cmd.Flags().StringArrayVar(&publicKeys, "public-key", nil, "trust only the public keys in this PEM file (repeatable)")
	cmd.Flags().StringVar(&attestations, "attestations", "", "your off-host copy of attestations.jsonl")
	cmd.Flags().StringVar(&expectGroup, "expect-group", "", `the customer group the export must name ("none" for none)`)
	return cmd
}

// terminalSafe returns s as it is when every character is printable, and
// else quoted with escapes: names in an export are untrusted, and a
// control character (ESC) in one could rewrite what the terminal shows.
func terminalSafe(s string) string {
	if utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return r != ' ' && !unicode.IsGraphic(r) }) {
		return s
	}
	return strconv.QuoteToGraphic(s)
}

// usageError prints err and ends with exit code 2.
func usageError(c *cobra.Command, err error) error {
	fmt.Fprintln(c.ErrOrStderr(), "error:", err)
	return &exitError{msg: err.Error(), code: exitIncomplete}
}

func runExportVerify(path string, opts gobd.VerifyOptions, out io.Writer) (int, error) {
	opts.OnFinding = func(f gobd.Finding) {
		line := fmt.Sprintf("%-12s %s", f.Kind, terminalSafe(f.Path))
		if f.Detail != "" {
			line += " (" + terminalSafe(f.Detail) + ")"
		}
		fmt.Fprintln(out, line)
	}
	res, err := gobd.Verify(path, opts)
	if err != nil {
		return 0, err
	}
	for _, n := range res.Notes {
		fmt.Fprintln(out, "note:", terminalSafe(n))
	}
	if st := res.Statement; st != nil {
		fmt.Fprintf(out, "export %s of %s, created %s\n", st.ID, gobd.GroupLabel(st), st.Created.Format("2006-01-02 15:04:05 UTC"))
		owners := "none"
		if len(st.Owners) > 0 {
			owners = terminalSafe(strings.Join(st.Owners, ", "))
		}
		fmt.Fprintf(out, "owners: %s\n", owners)
	}
	parts := []string{}
	for _, k := range gobd.FindingKinds {
		if n := res.Findings[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
		}
	}
	summary := "no problems"
	if len(parts) > 0 {
		summary = strings.Join(parts, ", ")
	}
	fmt.Fprintf(out, "checked %d message file(s): %s\n", res.Messages, summary)
	if res.Problems() > 0 {
		return exitFindings, nil
	}
	return 0, nil
}
