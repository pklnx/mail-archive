import type { ReactNode } from "react";
import { InlineNotification } from "@carbon/react";
import { ThemeToggle } from "./ThemeToggle";
import { t } from "./i18n";
import { ProductMark } from "./ProductMark";

// The frame of the screens before the app: login, the forced steps after
// it, and the setup of the first admin (#396).

export function Centered({ children }: { children: ReactNode }) {
  return (
    <div className="auth-page">
      <div className="auth-page__theme">
        <ThemeToggle />
      </div>
      <main className="auth-page__column">{children}</main>
    </div>
  );
}

export function Brand() {
  return (
    <div className="auth-page__brand">
      <ProductMark size={40} />
      <h1 className="type-heading-04">{t.appName}</h1>
    </div>
  );
}

export function Heading({ title, text }: { title: string; text?: string }) {
  return (
    <div>
      <h2 className="type-heading-03">{title}</h2>
      {text && <p className="auth-page__text">{text}</p>}
    </div>
  );
}

/** An error on the login screens: announced, and not closable (the next try clears it). */
export function ErrorNotice({ text, alert = false }: { text: string; alert?: boolean }) {
  return (
    <InlineNotification
      className="auth-page__wide"
      kind="error"
      lowContrast
      hideCloseButton
      role={alert ? "alert" : "status"}
      statusIconDescription={t.carbon.status.error}
      subtitle={text}
    />
  );
}
