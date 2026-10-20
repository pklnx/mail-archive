package web

import (
	"net/http"

	"github.com/pklnx/mail-archive/internal/web/ui"
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
)

type route struct {
	pattern string // "GET /api/messages/{sha}"
	handler http.Handler
	access  access
	ui      bool // the UI code: not an API route, not in the spec
}

// routes lists every route of the server.
func (s *Server) routes() []route {
	h := func(pattern string, f http.HandlerFunc, a access) route {
		return route{pattern: pattern, handler: f, access: a}
	}
	app := ui.Handler()
	routes := []route{
		h("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok\n"))
		}, accessPublic),
		h("GET /healthz/sync", s.handleSyncHealth, accessPublic),
		h("GET "+openAPIPath, s.handleOpenAPI, accessPublic),

		h("GET /api/session", s.handleGetSession, accessLogin),
		h("POST /api/session", s.handleLogin, accessLogin),
		h("POST /api/session/2fa", s.handleTwoFactorLogin, accessLogin),
		h("POST "+passkeyLoginBeginPath, s.handleBeginPasskeyLogin, accessLogin),
		h("POST "+passkeyLoginFinishPath, s.handleFinishPasskeyLogin, accessLogin),
		h("DELETE /api/session", s.handleLogout, accessLogin),

		h("PUT "+profilePasswordPath, s.handleChangeOwnPassword, accessSession),
		h("PATCH /api/profile", s.handleUpdateProfile, accessSession),
		h("GET /api/profile/2fa", s.handleGetTwoFactor, accessSession),
		h("POST /api/profile/2fa/setup", s.handleBeginTwoFactor, accessSession),
		h("POST /api/profile/2fa/confirm", s.handleConfirmTwoFactor, accessSession),
		h("DELETE /api/profile/2fa", s.handleDisableTwoFactor, accessSession),
		h("POST /api/profile/2fa/recovery-codes", s.handleRegenerateRecoveryCodes, accessSession),
		h("GET /api/profile/passkeys", s.handleListPasskeys, accessSession),
		h("POST /api/profile/passkeys/begin", s.handleBeginPasskeyRegistration, accessSession),
		h("POST /api/profile/passkeys/finish", s.handleFinishPasskeyRegistration, accessSession),
		h("DELETE /api/profile/passkeys/{id}", s.handleRemovePasskey, accessSession),
		h("GET /api/profile/api-keys", s.handleListAPIKeys, accessSession),
		h("POST /api/profile/api-keys", s.handleCreateAPIKey, accessSession),
		h("DELETE /api/profile/api-keys/{id}", s.handleRevokeOwnAPIKey, accessSession),

		h("GET /api/users", s.handleListUsers, accessSession),
		h("POST /api/users", s.handleCreateUser, accessSession),
		h("PATCH /api/users/{name}", s.handleUpdateUser, accessSession),
		h("DELETE /api/users/{name}", s.handleDeleteUser, accessSession),
		h("POST /api/users/{name}/password", s.handleResetUserPassword, accessSession),
		h("POST /api/users/{name}/2fa/reset", s.handleResetUserTwoFactor, accessSession),
		h("DELETE /api/users/{name}/passkeys", s.handleRemoveUserPasskeys, accessSession),
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

		// Users see their own entries, admins everyone's.
		h("GET /api/audit", s.handleAudit, accessSession),

		h("GET /api/status", s.handleStatus, accessRead),
		h("GET /api/accounts", s.handleAccounts, accessRead),
		h("POST /api/accounts", s.handleCreateAccount, accessSession),
		h("PATCH /api/accounts/{name}", s.handleUpdateAccount, accessSession),
		h("DELETE /api/accounts/{name}", s.handleDeleteAccount, accessSession),
		// It logs in to the IMAP server: account setup, not reading.
		h("GET /api/accounts/{name}/server-folders", s.handleServerFolders, accessSession),
		h("POST /api/accounts/{name}/sync", s.handleSyncAccount, accessSync),
		h("POST /api/accounts/{name}/fills", s.handleStartFill, accessSession),
		h("POST /api/accounts/{name}/fills/folders", s.handleFillFolders, accessSession),
		h("PATCH /api/accounts/{name}/fills/{id}", s.handleChangeFill, accessSession),
		h("POST /api/accounts/{name}/fills/{id}/resume", s.handleResumeFill, accessSession),
		h("POST /api/accounts/{name}/fills/{id}/cancel", s.handleCancelFill, accessSession),
		h("POST /api/sync", s.handleSyncAll, accessSync),

		h("GET /api/messages", s.handleListMessages, accessRead),
		h("GET /api/messages/{sha}", s.handleMessage, accessRead),
		h("GET /api/messages/{sha}/conversation", s.handleConversation, accessRead),
		h("GET /api/messages/{sha}/html", s.handleMessageHTML, accessRead),
		h("GET /api/messages/{sha}/raw", s.handleMessageRaw, accessRead),
		h("GET /api/messages/{sha}/parts/{n}", s.handleMessagePart, accessRead),

		{pattern: "GET /{$}", handler: app, access: accessPublic, ui: true},
		{pattern: "GET /assets/", handler: app, access: accessPublic, ui: true},
	}
	// The web app manifest and its icons, at fixed names in the root.
	for _, path := range ui.RootPaths() {
		routes = append(routes, route{pattern: "GET " + path, handler: app, access: accessPublic, ui: true})
	}
	return routes
}
