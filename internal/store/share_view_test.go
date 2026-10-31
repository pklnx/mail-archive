package store_test

import (
	"encoding/json"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
)

// shareView keeps only what the owner may see of another user's read
// (#389, revision 2 R1/R2, decision 17): the owner's own accounts, the
// terms of named first pages only, and of every other page only whether it
// searched.
func TestShareView(t *testing.T) {
	const a, c = 3, 7
	via := `"via_owners":[3,7],"via_accounts":[{"id":1,"owner":3,"key":"a/info","n":2},{"id":2,"owner":7,"key":"c/sales","n":1}]`
	for _, tc := range []struct {
		name, action, details string
		viewer                int64
		want                  string
	}{
		{"unnamed search", "message.page", `{"q":"secret","from":"x","folder":"INBOX","thread":"t","group":"1","gone":"1","cursor":"c","limit":3,` + via + `}`, a,
			`{"search":true,"via_accounts":[{"id":1,"owner":3,"key":"a/info","n":2}]}`},
		{"unnamed list, other owner", "message.list", `{"folder":"INBOX",` + via + `}`, c,
			`{"search":false,"via_accounts":[{"id":2,"owner":7,"key":"c/sales","n":1}]}`},
		{"named search", "message.search", `{"q":"secret","account":"a/info","thread":"t","limit":5,"via_owners":[3],"via_accounts":[{"id":1,"owner":3,"key":"a/info","n":1}]}`, a,
			`{"account":"a/info","q":"secret","thread":"t","via_accounts":[{"id":1,"owner":3,"key":"a/info","n":1}]}`},
		{"named cursor page", "message.page", `{"q":"secret","account":"a/info","folder":"INBOX","via_owners":[3],"via_accounts":[{"id":1,"owner":3,"key":"a/info","n":1}]}`, a,
			`{"search":true,"via_accounts":[{"id":1,"owner":3,"key":"a/info","n":1}]}`},
		{"open", "message.open", `{"images":true,` + via + `}`, c,
			`{"via_accounts":[{"id":2,"owner":7,"key":"c/sales","n":1}]}`},
		{"conversation", "message.conversation", `{"members":4,` + via + `}`, a,
			`{"via_accounts":[{"id":1,"owner":3,"key":"a/info","n":2}]}`},
		{"no accounts of the viewer", "message.open", `{` + via + `}`, 9, `{"via_accounts":[]}`},
	} {
		got, err := store.ShareView(tc.action, []byte(tc.details), tc.viewer)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var g, w any
		_ = json.Unmarshal(got, &g)
		_ = json.Unmarshal([]byte(tc.want), &w)
		gb, _ := json.Marshal(g)
		wb, _ := json.Marshal(w)
		if string(gb) != string(wb) {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, gb, wb)
		}
	}
}
