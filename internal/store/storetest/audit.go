package storetest

import (
	"fmt"

	"github.com/pklnx/mail-archive/internal/store"
)

// Entry starts an audit entry of action in the first class the store
// allows for it (store.AuditActionClasses), for tests whose action varies.
// Code never picks a class this way: each site chooses its constructor.
func Entry(actor, action string) *store.AuditEntry {
	classes := store.AuditActionClasses()[action]
	if len(classes) == 0 {
		panic(fmt.Sprintf("storetest.Entry: action %q has no class", action))
	}
	switch classes[0] {
	case store.AuditSelf:
		return store.SelfEntry(actor, action)
	case store.AuditInstance:
		return store.InstanceEntry(actor, action)
	}
	return store.TargetedEntry(actor, action)
}
