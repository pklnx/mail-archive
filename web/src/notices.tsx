import { InlineNotification } from "@carbon/react";
import { t } from "./i18n";

/** An error under a form, announced when it appears. */
export function FormError({ text }: { text: string }) {
  return text ? (
    <p role="alert" className="app-text-error">
      {text}
    </p>
  ) : null;
}

/** A success message that stays until the next action. */
export function Success({ text }: { text: string }) {
  return (
    <InlineNotification
      className="notification--full"
      kind="success"
      lowContrast
      hideCloseButton
      role="status"
      statusIconDescription={t.carbon.status.success}
      subtitle={text}
    />
  );
}
