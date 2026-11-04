import { App } from "./App";
import { AuthGate } from "./Auth";

/**
 * The app behind the login gate. A switch to another auditor grant (#393)
 * starts the app afresh (its key): nothing of the last case stays.
 */
export function Root() {
  return (
    <AuthGate>
      {(session, logout, logoutError, update) => (
        <App
          key={session.grant?.id ?? 0}
          user={session.user}
          auditor={session.grant && { grant: session.grant, grants: session.grants ?? [], switched: update }}
          logout={logout}
          logoutError={logoutError}
        />
      )}
    </AuthGate>
  );
}
