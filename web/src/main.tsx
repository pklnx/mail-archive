import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { initTheme } from "./theme";
import { App } from "./App";
import { AuthGate } from "./Auth";

initTheme();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AuthGate>{(user, logout, logoutError) => <App user={user} logout={logout} logoutError={logoutError} />}</AuthGate>
  </StrictMode>,
);
