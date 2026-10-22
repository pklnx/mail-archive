package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

// addMessage stores a message in an account's folder, like a sync, under
// the given name. A folder without a UIDVALIDITY yet gets 1, as after a
// first sync, so the location is current (not superseded). A folder that
// already has one keeps it and its last UID. Use renumberFolder for
// superseded locations on purpose.
func (f *apiFixture) addMessage(account, folder, name, raw string, uid uint32) {
	f.t.Helper()
	ctx := context.Background()
	acc, err := accountByName(f.st, account)
	if err != nil {
		f.t.Fatal(err)
	}
	fo, err := f.st.GetOrCreateFolder(ctx, acc.ID, folder)
	if err != nil {
		f.t.Fatal(err)
	}
	if fo.UIDValidity == 0 {
		// Not for a folder that has one: ResetFolder also zeroes last_uid.
		if err := f.st.ResetFolder(ctx, fo.ID, 1); err != nil {
			f.t.Fatal(err)
		}
		fo.UIDValidity = 1
	}
	blob, _, err := f.blobs.Put(strings.NewReader(raw))
	if err != nil {
		f.t.Fatal(err)
	}
	meta, err := archive.BuildMeta(ctx, f.st, f.blobs, blob, true)
	if err != nil {
		f.t.Fatal(err)
	}
	loc := store.Location{FolderID: fo.ID, UIDValidity: fo.UIDValidity, UID: uid}
	if _, err := f.st.SaveBatch(ctx, fo.ID, uid, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
		f.t.Fatal(err)
	}
	f.ids[name] = blob.SHA256
}

// renumberFolder gives a folder a new UIDVALIDITY, as a server-side change
// does. Locations stored under the old value then read superseded. Like
// ResetFolder it also resets the folder's last_uid to 0.
func (f *apiFixture) renumberFolder(account, folder string, uidValidity uint32) {
	f.t.Helper()
	ctx := context.Background()
	acc, err := accountByName(f.st, account)
	if err != nil {
		f.t.Fatal(err)
	}
	fo, err := f.st.GetOrCreateFolder(ctx, acc.ID, folder)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.st.ResetFolder(ctx, fo.ID, uidValidity); err != nil {
		f.t.Fatal(err)
	}
}

func TestAddMessageLocations(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	detail := func(name string) messageJSON {
		t.Helper()
		var m messageJSON
		f.getJSON("/api/messages/"+f.ids[name], 200, &m)
		if len(m.Locations) != 1 {
			t.Fatalf("%s locations = %+v", name, m.Locations)
		}
		return m
	}

	// A new folder gets a UIDVALIDITY, so its location is current.
	f.addMessage("bob", "Drafts", "draft", reply("draft@x", "meeting@x", "Re: draft", "Fri, 9 Oct 2026 10:00:00 +0000"), 1)
	if m := detail("draft"); m.Locations[0].Superseded {
		t.Errorf("new folder: location superseded: %+v", m.Locations[0])
	}

	// An existing folder is not rewound.
	alice, err := accountByName(f.st, "alice")
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.st.GetOrCreateFolder(ctx, alice.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	f.addMessage("alice", "INBOX", "later", reply("later@x", "meeting@x", "Re: later", "Sat, 10 Oct 2026 10:00:00 +0000"), 10)
	after, err := f.st.GetOrCreateFolder(ctx, alice.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if after.UIDValidity != 1 || after.LastUID < before.LastUID {
		t.Errorf("alice/INBOX before %+v, after %+v", before, after)
	}
	if m := detail("later"); m.Locations[0].Superseded {
		t.Errorf("existing folder: location superseded: %+v", m.Locations[0])
	}

	// renumberFolder makes superseded locations on purpose.
	f.renumberFolder("bob", "Drafts", 2)
	if m := detail("draft"); !m.Locations[0].Superseded {
		t.Errorf("after renumber: location not superseded: %+v", m.Locations[0])
	}
}

func reply(id, to, subject, date string) string {
	return crlf("Message-ID: <" + id + ">\nFrom: Alice <alice@example.com>\nIn-Reply-To: <" + to + ">\n" +
		"Subject: " + subject + "\nDate: " + date + "\nContent-Type: text/plain\n\nOk.\n")
}

type conversationResp struct {
	Messages  []conversationEntryJSON `json:"messages"`
	Total     int64                   `json:"total"`
	Truncated bool                    `json:"truncated"`
}

func (f *apiFixture) conversation(name string) []string {
	f.t.Helper()
	var r conversationResp
	f.getJSON("/api/messages/"+f.ids[name]+"/conversation", 200, &r)
	out := []string{}
	for _, m := range r.Messages {
		out = append(out, f.name(m.ID)+":"+m.Relation)
	}
	if r.Total != int64(len(out)) || r.Truncated {
		f.t.Errorf("total %d, truncated %v for %d entries", r.Total, r.Truncated, len(out))
	}
	return out
}

func (f *apiFixture) grouped(query string) []string {
	f.t.Helper()
	var r listResp
	f.getJSON("/api/messages?group=1&"+query, 200, &r)
	out := []string{}
	for _, m := range r.Messages {
		out = append(out, fmt.Sprintf("%s:%s:%d", f.name(m.ID), m.Thread, m.Count))
	}
	return out
}

func TestConversationAPI(t *testing.T) {
	f := newAPIFixture(t)
	// A reply with In-Reply-To only: its thread ID is the meeting's
	// Message-ID.
	f.addMessage("alice", "INBOX", "meetingReply", reply("meeting-reply@x", "meeting@x", "Re: Team sync", "Fri, 9 Oct 2026 10:00:00 +0000"), 10)

	if got := f.conversation("meeting"); !equal(got, []string{"meeting:self", "meetingReply:reply"}) {
		t.Errorf("meeting: %v", got)
	}
	if got := f.conversation("meetingReply"); !equal(got, []string{"meeting:parent", "meetingReply:self"}) {
		t.Errorf("reply: %v", got)
	}
	if got := f.conversation("rich"); !equal(got, []string{"rich:self"}) {
		t.Errorf("rich: %v", got)
	}

	var d struct {
		InReplyTo string `json:"inReplyTo"`
		Thread    string `json:"thread"`
	}
	f.getJSON("/api/messages/"+f.ids["meetingReply"], 200, &d)
	if d.InReplyTo != "meeting@x" || d.Thread != "meeting@x" {
		t.Errorf("detail: %+v", d)
	}

	// One row per thread, the newest message with the count.
	if got := f.grouped(""); !equal(got, []string{"meetingReply:meeting@x:2", "rich:rich@x:1", "newsletter:news@x:1", "invoice:invoice@x:1"}) {
		t.Errorf("grouped: %v", got)
	}
	if got := f.grouped("q=roadmap"); !equal(got, []string{"meeting:meeting@x:1"}) {
		t.Errorf("grouped search: %v", got)
	}
	if got := f.list("thread=meeting%40x"); !equal(got, []string{"meetingReply", "meeting"}) {
		t.Errorf("thread: %v", got)
	}
	if got := f.list("group=0"); len(got) != 5 {
		t.Errorf("group=0 lists %v", got)
	}
	var plain listResp
	f.getJSON("/api/messages", 200, &plain)
	if plain.Messages[0].Thread != "" || plain.Messages[0].Count != 0 {
		t.Errorf("plain listing has thread fields: %+v", plain.Messages[0])
	}

	for path, want := range map[string]int{
		"/api/messages?group=2": 400,
		"/api/messages?thread=" + strings.Repeat("a", maxThreadLen+1): 400,
		"/api/messages?thread=" + strings.Repeat("a", maxThreadLen):   200,
		"/api/messages/not-a-sha/conversation":                        400,
		"/api/messages/" + strings.Repeat("0", 64) + "/conversation":  404,
	} {
		if resp := f.get(path); resp.StatusCode != want {
			t.Errorf("GET %s: %d, want %d", path, resp.StatusCode, want)
		}
	}
}

// Another user's reply to a shared message is never listed: not in the
// conversation, the grouped list, its count or the thread listing.
func TestConversationsPerUser(t *testing.T) {
	f := newAPIFixture(t)
	other, token := sessionFor(t, f.st, "other")
	bob, err := accountByName(f.st, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetAccountOwner(context.Background(), bob.Ref(), other.ID); err != nil {
		t.Fatal(err)
	}
	g := *f
	g.cookie = token
	// The invoice is in both users' accounts; each user answered it.
	f.addMessage("alice", "INBOX", "replyA", reply("reply-a@x", "invoice@x", "Re: Rechnung A", "Fri, 9 Oct 2026 10:00:00 +0000"), 10)
	f.addMessage("bob", "INBOX", "replyB", reply("reply-b@x", "invoice@x", "Re: Rechnung B", "Sat, 10 Oct 2026 10:00:00 +0000"), 10)

	if got := f.conversation("invoice"); !equal(got, []string{"invoice:self", "replyA:reply"}) {
		t.Errorf("first user's conversation: %v", got)
	}
	if got := g.conversation("invoice"); !equal(got, []string{"invoice:self", "replyB:reply"}) {
		t.Errorf("second user's conversation: %v", got)
	}
	if got := f.grouped("q=Rechnung"); !equal(got, []string{"replyA:invoice@x:2"}) {
		t.Errorf("first user's grouped list: %v", got)
	}
	if got := g.grouped("q=Rechnung"); !equal(got, []string{"replyB:invoice@x:2"}) {
		t.Errorf("second user's grouped list: %v", got)
	}
	if got := f.list("thread=invoice%40x"); !equal(got, []string{"replyA", "invoice"}) {
		t.Errorf("first user's thread: %v", got)
	}
	if got := g.list("thread=invoice%40x"); !equal(got, []string{"replyB", "invoice"}) {
		t.Errorf("second user's thread: %v", got)
	}
	// Guessing another user's thread key lists nothing.
	if got := f.list("thread=news%40x"); len(got) != 0 {
		t.Errorf("guessed thread: %v", got)
	}
	for _, c := range []struct {
		f    *apiFixture
		name string
	}{{f, "newsletter"}, {f, "replyB"}, {&g, "rich"}, {&g, "replyA"}} {
		if resp := c.f.get("/api/messages/" + f.ids[c.name] + "/conversation"); resp.StatusCode != http.StatusNotFound {
			t.Errorf("conversation of %s for its non-owner: %d", c.name, resp.StatusCode)
		}
	}
}
