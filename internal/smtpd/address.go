package smtpd

import "strings"

// Decode splits an envelope recipient into the base address an smtp
// account receives for and the original recipient. Postfix's
// recipient_bcc_maps can encode the original recipient in the extension:
//
//	archive+alice=example.com@archive.local
//
// has the base archive@archive.local and the recipient alice@example.com.
// The extension starts at the first "+" and is split at its last "=", so
// local parts with "+" or "=" survive. Without such an extension the
// recipient is the address as received.
func Decode(rcpt string) (base, original string) {
	at := strings.LastIndexByte(rcpt, '@')
	if at < 0 {
		return rcpt, rcpt
	}
	local, domain := rcpt[:at], rcpt[at+1:]
	plus := strings.IndexByte(local, '+')
	if plus < 0 {
		return rcpt, rcpt
	}
	base = local[:plus] + "@" + domain
	ext := local[plus+1:]
	eq := strings.LastIndexByte(ext, '=')
	if eq <= 0 || eq == len(ext)-1 {
		return base, rcpt
	}
	return base, ext[:eq] + "@" + ext[eq+1:]
}
