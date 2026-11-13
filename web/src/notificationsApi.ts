// Profile → Notifications (#397, part 5): an admin's address for alert
// mail. Only Profile imports this module, so it is part of the lazy
// Profile chunk and not of the initial bundle.
import { getJSON, send } from "./api";

/** The own address for alert mail. available is false without a mail relay on the server. */
export interface Notifications {
  available: boolean;
  email: string | null;
  /** Only a confirmed address gets alert mail. */
  confirmed: boolean;
  confirmedAt: string | null;
  /** A relay refused the address; it gets no mail until confirmed again. */
  bounced: boolean;
  /** Set while a code waits to be entered. */
  codeExpiresAt: string | null;
}

const path = "/api/profile/notifications";

export const notificationsApi = {
  get: () => getJSON<Notifications>(path),
  /** Sets the address and mails a code to it; language is the UI's (de or en). */
  set: (email: string, language: string) => send<Notifications>("PUT", path, { email, language }),
  confirm: (code: string) => send<Notifications>("POST", `${path}/confirm`, { code }),
  remove: () => send("DELETE", path),
};
