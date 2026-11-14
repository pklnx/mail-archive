package web

import (
	"net/http"

	"github.com/keepia/archive/internal/web/ui"
)

// access says who may call a route. The same table registers the routes,
// authorizes requests and is compared with openapi.yaml in a test, so the
// documented access cannot drift from the enforced one.
type access int

const (
	accessSession access = iota // a session only; the zero value, so a forgotten field is the strictest
	accessLogin                 // the login endpoints: no session needed, refused with a key
	accessPublic                // no login: health checks, UI code, the spec
	accessRead                  // a session or an API key with "read"
	accessSync                  // a session or an API key with "sync"
	// accessMetrics: an admin's session, or an API key with "metrics" (a
	// scope no other key has, alone in its key) whose owner is still an
	// admin and not locked (#397, part 3). Any other user gets 403.
	accessMetrics
)

type route struct {
	pattern string // "GET /api/messages/{sha}"
	handler http.Handler
	access  access
	ui      bool // the UI code: not an API route, not in the spec
	// catchAll answers paths under /api/ that no other route takes; it is
	// not in the spec.
	catchAll bool
	// auditor: an auditor (#393) may call the route. Deny by default: an
	// auditor's request to any other route that needs a login answers 403
	// and writes auditor.denied. openapi.yaml marks the same routes with
	// x-auditor.
	auditor bool
}

// routes lists every route of the server.
func (s *Server) routes() []route {
	h := func(pattern string, f http.HandlerFunc, a access) route {
		return route{pattern: pattern, handler: f, access: a}
	}
	// au is h for a route an auditor may call (the allowlist).
	au := func(pattern string, f http.HandlerFunc, a access) route {
		rt := h(pattern, f, a)
		rt.auditor = true
		return rt
	}
	app := ui.Handler()
	routes := []route{
		h("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok\n"))
		}, accessPublic),
		h("GET /healthz/sync", s.handleSyncHealth, accessPublic),
		h("GET /healthz/jobs", s.handleJobsHealth, accessPublic),
		h("GET "+openAPIPath, s.handleOpenAPI, accessPublic),

		au("GET /api/session", s.handleGetSession, accessLogin),
		au("POST /api/session", s.handleLogin, accessLogin),
		au("POST /api/session/2fa", s.handleTwoFactorLogin, accessLogin),
		au("POST "+passkeyLoginBeginPath, s.handleBeginPasskeyLogin, accessLogin),
		au("POST "+passkeyLoginFinishPath, s.handleFinishPasskeyLogin, accessLogin),
		au("DELETE /api/session", s.handleLogout, accessLogin),
		// The first admin from the operator's setup link (#396, part 3).
		au("POST "+setupPath, s.handleSetup, accessLogin),
		// An auditor picks the grant the session reads through (#393).
		au("PUT /api/session/grant", s.handleSelectGrant, accessSession),

		au("PUT "+profilePasswordPath, s.handleChangeOwnPassword, accessSession),
		au("PATCH /api/profile", s.handleUpdateProfile, accessSession),
		au("GET /api/profile/2fa", s.handleGetTwoFactor, accessSession),
		au("POST /api/profile/2fa/setup", s.handleBeginTwoFactor, accessSession),
		au("POST /api/profile/2fa/confirm", s.handleConfirmTwoFactor, accessSession),
		h("DELETE /api/profile/2fa", s.handleDisableTwoFactor, accessSession),
		h("POST /api/profile/2fa/recovery-codes", s.handleRegenerateRecoveryCodes, accessSession),
		au("GET /api/profile/passkeys", s.handleListPasskeys, accessSession),
		au("POST /api/profile/passkeys/begin", s.handleBeginPasskeyRegistration, accessSession),
		au("POST /api/profile/passkeys/finish", s.handleFinishPasskeyRegistration, accessSession),
		au("DELETE /api/profile/passkeys/{id}", s.handleRemovePasskey, accessSession),
		h("GET /api/profile/api-keys", s.handleListAPIKeys, accessSession),
		h("POST /api/profile/api-keys", s.handleCreateAPIKey, accessSession),
		h("DELETE /api/profile/api-keys/{id}", s.handleRevokeOwnAPIKey, accessSession),
		// The admin's address for alert mail (#397, part 5): admins only,
		// 403 for anyone else before any lookup.
		h("GET /api/profile/notifications", s.handleGetNotifications, accessSession),
		h("PUT /api/profile/notifications", s.handleSetNotifications, accessSession),
		h("POST /api/profile/notifications/confirm", s.handleConfirmNotifications, accessSession),
		h("DELETE /api/profile/notifications", s.handleRemoveNotifications, accessSession),

		h("GET /api/users", s.handleListUsers, accessSession),
		h("POST /api/users", s.handleCreateUser, accessSession),
		h("PATCH /api/users/{name}", s.handleUpdateUser, accessSession),
		h("DELETE /api/users/{name}", s.handleDeleteUser, accessSession),
		h("POST /api/users/{name}/password", s.handleResetUserPassword, accessSession),
		h("POST /api/users/{name}/2fa/reset", s.handleResetUserTwoFactor, accessSession),
		h("DELETE /api/users/{name}/passkeys", s.handleRemoveUserPasskeys, accessSession),
		// Customer groups (#470): admins only, with a session.
		h("GET /api/groups", s.handleListGroups, accessSession),
		h("POST /api/groups", s.handleCreateGroup, accessSession),
		h("PATCH /api/groups/{id}", s.handleUpdateGroup, accessSession),
		h("DELETE /api/groups/{id}", s.handleDeleteGroup, accessSession),
		h("GET /api/api-keys", s.handleListAllAPIKeys, accessSession),
		h("DELETE /api/api-keys/{id}", s.handleRevokeAnyAPIKey, accessSession),

		h("GET /api/retention/rules", s.handleListRetentionRules, accessSession),
		h("POST /api/retention/rules", s.handleCreateRetentionRule, accessSession),
		h("DELETE /api/retention/rules/{id}", s.handleDeleteRetentionRule, accessSession),
		h("GET /api/legal-holds", s.handleListLegalHolds, accessSession),
		h("POST /api/legal-holds", s.handleCreateLegalHold, accessSession),
		h("POST /api/legal-holds/{id}/release", s.handleReleaseLegalHold, accessSession),
		h("GET /api/retention/runs", s.handleListDeletionRuns, accessSession),
		h("POST /api/retention/runs", s.handleCreateDeletionRun, accessSession),
		h("GET /api/retention/runs/{id}", s.handleGetDeletionRun, accessSession),
		h("POST /api/retention/runs/{id}/confirm", s.handleConfirmDeletionRun, accessSession),
		h("POST /api/retention/runs/{id}/cancel", s.handleCancelDeletionRun, accessSession),

		// Auditor grants (#393): owners manage their own with a session, no
		// API key, as for shares; auditors list theirs; admins revoke any.
		au("GET /api/auditor-grants", s.handleListGrants, accessSession),
		h("POST /api/auditor-grants", s.handleCreateGrant, accessSession),
		h("GET /api/auditor-grants/{id}", s.handleGetGrant, accessSession),
		h("PATCH /api/auditor-grants/{id}", s.handleExtendGrant, accessSession),
		h("DELETE /api/auditor-grants/{id}", s.handleRevokeGrant, accessSession),

		// Users see their own entries, admins everyone's.
		au("GET /api/audit", s.handleAudit, accessSession),

		au("GET /api/status", s.handleStatus, accessRead),
		// Storage usage (#397, part 4): admins see everything, members the
		// size of their own accounts and whether new mail is archived.
		h("GET /api/usage", s.handleUsage, accessSession),
		h("POST /api/usage/refresh", s.handleRefreshUsage, accessSession),
		// Instance metrics in Prometheus text: IDs and counts, no names.
		h("GET /api/metrics", s.handleMetrics, accessMetrics),
		au("GET /api/accounts", s.handleAccounts, accessRead),
		h("POST /api/accounts", s.handleCreateAccount, accessSession),
		h("PATCH /api/accounts/{name}", s.handleUpdateAccount, accessSession),
		h("DELETE /api/accounts/{name}", s.handleDeleteAccount, accessSession),
		h("GET /api/accounts/{name}/purge", s.handlePurgePreview, accessSession),
		h("POST /api/accounts/{name}/purge", s.handleConfirmPurge, accessSession),
		// It logs in to the IMAP server: account setup, not reading.
		h("GET /api/accounts/{name}/server-folders", s.handleServerFolders, accessSession),
		h("POST /api/accounts/{name}/sync", s.handleSyncAccount, accessSync),
		h("POST /api/accounts/{name}/fills", s.handleStartFill, accessSession),
		h("POST /api/accounts/{name}/fills/folders", s.handleFillFolders, accessSession),
		h("PATCH /api/accounts/{name}/fills/{id}", s.handleChangeFill, accessSession),
		h("POST /api/accounts/{name}/fills/{id}/resume", s.handleResumeFill, accessSession),
		h("POST /api/accounts/{name}/fills/{id}/cancel", s.handleCancelFill, accessSession),
		// SMTP credentials of smtp accounts (#395): the owner, who is an
		// admin, with a session; any admin lists and revokes them all.
		h("GET /api/accounts/{name}/smtp-credentials", s.handleListSMTPCredentials, accessSession),
		h("POST /api/accounts/{name}/smtp-credentials", s.handleCreateSMTPCredential, accessSession),
		h("DELETE /api/accounts/{name}/smtp-credentials/{id}", s.handleRevokeSMTPCredential, accessSession),
		h("GET /api/admin/smtp-credentials", s.handleListAllSMTPCredentials, accessSession),
		h("DELETE /api/admin/smtp-credentials/{id}", s.handleRevokeAnySMTPCredential, accessSession),
		// Only the owner shares, within their group, with a session (#389, #470).
		h("PUT /api/accounts/{name}/shares/{user}", s.handleShareAccount, accessSession),
		h("DELETE /api/accounts/{name}/shares/{user}", s.handleUnshareAccount, accessSession),
		h("POST /api/sync", s.handleSyncAll, accessSync),

		au("GET /api/messages", s.handleListMessages, accessRead),
		au("GET /api/messages/{sha}", s.handleMessage, accessRead),
		au("GET /api/messages/{sha}/conversation", s.handleConversation, accessRead),
		au("GET /api/messages/{sha}/html", s.handleMessageHTML, accessRead),
		au("GET /api/messages/{sha}/raw", s.handleMessageRaw, accessRead),
		au("GET /api/messages/{sha}/parts/{n}", s.handleMessagePart, accessRead),

		// Unknown paths and methods under /api/ answer JSON 404 (after the
		// login), not the mux's text.
		{pattern: "/api/", handler: http.HandlerFunc(s.handleUnknownAPI), access: accessSession, catchAll: true, auditor: true},

		{pattern: "GET /{$}", handler: app, access: accessPublic, ui: true},
		{pattern: "GET /assets/", handler: app, access: accessPublic, ui: true},
	}
	// The web app manifest and its icons, at fixed names in the root.
	for _, path := range ui.RootPaths() {
		routes = append(routes, route{pattern: "GET " + path, handler: app, access: accessPublic, ui: true})
	}
	return routes
}
