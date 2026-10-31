package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"testing"
)

// Shares at the HTTP level (#389, part 1): with the fixture's account
// "alice" shared with reader through the store, reader reads its mail with a
// session and with a read key; outsider, who has no share, gets nothing; the
// next request after the unshare gets nothing either. A plain name in
// account= stays the viewer's own (owner/name: TestShareNameCollision),
// and the shared account is in "shared", not in "accounts".
func TestSharedAccountReads(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	reader, readerToken := sessionFor(t, f.st, "reader")
	outsider, outsiderToken := sessionFor(t, f.st, "outsider")
	readers := []credential{{name: "reader", cookie: readerToken}, {name: "reader's key", key: readKey(t, f.st, reader)}}
	outsiders := []credential{{name: "outsider", cookie: outsiderToken}, {name: "outsider's key", key: readKey(t, f.st, outsider)}}
	alice, err := accountByName(f.st, "alice")
	if err != nil {
		t.Fatal(err)
	}
	sorted := func(l []string) []string { sort.Strings(l); return l }
	var rich messageJSON
	f.getJSON("/api/messages/"+f.ids["rich"], 200, &rich)
	routes := []string{"", "/html", "/raw", "/parts/1", "/conversation"}

	// reads checks what c gets of alice's mail; shared says whether it may.
	reads := func(stage string, c credential, shared bool) {
		t.Helper()
		want := []string{}
		if shared {
			want = []string{"invoice", "meeting", "rich"}
		}
		for _, grouped := range []bool{false, true} {
			if got := sorted(f.listAs(c, "", grouped)); !slices.Equal(got, want) {
				t.Errorf("%s: %s lists (grouped %v) %v, want %v", stage, c.name, grouped, got, want)
			}
			for _, q := range []string{"q=roadmap", "to=alice@example.com", "attachment=Vertrag", "has=attachment", "thread=" + url.QueryEscape(rich.Thread)} {
				got := f.listAs(c, q, grouped)
				if shared != (len(got) > 0) {
					t.Errorf("%s: %s lists %s (grouped %v): %v", stage, c.name, q, grouped, got)
				}
			}
		}
		wantStatus := http.StatusNotFound
		if shared {
			wantStatus = http.StatusOK
		}
		for _, p := range routes {
			if status, _ := f.fetch(c, "/api/messages/"+f.ids["rich"]+p); status != wantStatus {
				t.Errorf("%s: GET rich%s as %s: %d, want %d", stage, p, c.name, status, wantStatus)
			}
		}
		// The newsletter is only in tester's bob, which is not shared.
		for _, p := range routes {
			if status, _ := f.fetch(c, "/api/messages/"+f.ids["newsletter"]+p); status != http.StatusNotFound {
				t.Errorf("%s: GET newsletter%s as %s: %d", stage, p, c.name, status)
			}
		}
	}
	for _, c := range append(readers, outsiders...) {
		reads("before sharing", c, false)
	}

	if _, err := f.st.ShareAccount(ctx, alice.Ref(), reader.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range readers {
		reads("shared", c, true)
		// The invoice is also in bob: reader sees only alice's location.
		var d messageJSON
		f.fetchJSON(c, "/api/messages/"+f.ids["invoice"], &d)
		if len(d.Locations) != 1 || d.Locations[0].Account != "alice" {
			t.Errorf("%s: locations of the invoice %+v", c.name, d.Locations)
		}
		// A plain account= names own accounts only.
		if got := f.listAs(c, "account=alice", false); len(got) != 0 {
			t.Errorf("%s: account=alice lists %v", c.name, got)
		}
	}
	var accounts accountsResponse
	f.fetchJSON(readers[0], "/api/accounts", &accounts)
	if len(accounts.Accounts) != 0 {
		t.Errorf("reader's accounts %+v", accounts.Accounts)
	}
	for _, c := range outsiders {
		reads("shared with reader", c, false)
	}

	// The access ends with the unshare, for the session and the key alike.
	alice, err = accountByName(f.st, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.UnshareAccount(ctx, alice.Ref(), reader.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range readers {
		reads("unshared", c, false)
	}
}
