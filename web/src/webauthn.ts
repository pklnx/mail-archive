// Passkeys in the browser: navigator.credentials with the JSON the server
// speaks. Binary values travel as base64url; no library needed.

export function toBase64url(buf: ArrayBuffer | ArrayBufferView): string {
  const bytes = buf instanceof ArrayBuffer ? new Uint8Array(buf) : new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength);
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function fromBase64url(s: string): ArrayBuffer {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(s.length / 4) * 4, "=");
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}

/** Whether this browser can use passkeys at all. */
export function passkeysSupported(): boolean {
  return typeof window !== "undefined" && typeof window.PublicKeyCredential === "function" && !!navigator.credentials;
}

/** Whether the browser can offer passkeys in the user name's autofill. */
export async function autofillSupported(): Promise<boolean> {
  if (!passkeysSupported()) return false;
  const check = (PublicKeyCredential as unknown as { isConditionalMediationAvailable?: () => Promise<boolean> }).isConditionalMediationAvailable;
  try {
    return typeof check === "function" && (await check.call(PublicKeyCredential));
  } catch {
    return false;
  }
}

type JSONDescriptor = { id: string; type: string; transports?: string[] };

const descriptors = (list?: JSONDescriptor[]): PublicKeyCredentialDescriptor[] | undefined =>
  list?.map((d) => ({ type: "public-key", id: fromBase64url(d.id), transports: d.transports as AuthenticatorTransport[] | undefined }));

/** Registration options from the server, ready for navigator.credentials.create. */
export function creationOptions(json: Record<string, unknown>): PublicKeyCredentialCreationOptions {
  const user = json.user as { id: string; name: string; displayName: string };
  return {
    ...(json as unknown as PublicKeyCredentialCreationOptions),
    challenge: fromBase64url(json.challenge as string),
    user: { ...user, id: fromBase64url(user.id) },
    excludeCredentials: descriptors(json.excludeCredentials as JSONDescriptor[] | undefined),
  };
}

/** Login options from the server, ready for navigator.credentials.get. */
export function requestOptions(json: Record<string, unknown>): PublicKeyCredentialRequestOptions {
  return {
    ...(json as unknown as PublicKeyCredentialRequestOptions),
    challenge: fromBase64url(json.challenge as string),
    allowCredentials: descriptors(json.allowCredentials as JSONDescriptor[] | undefined),
  };
}

/** The browser's answer as JSON for the server. */
export function credentialJSON(cred: PublicKeyCredential): Record<string, unknown> {
  const r = cred.response;
  const response: Record<string, unknown> = { clientDataJSON: toBase64url(r.clientDataJSON) };
  if ("attestationObject" in r) {
    const att = r as AuthenticatorAttestationResponse;
    response.attestationObject = toBase64url(att.attestationObject);
    if (typeof att.getTransports === "function") response.transports = att.getTransports();
  } else {
    const a = r as AuthenticatorAssertionResponse;
    response.authenticatorData = toBase64url(a.authenticatorData);
    response.signature = toBase64url(a.signature);
    if (a.userHandle) response.userHandle = toBase64url(a.userHandle);
  }
  return {
    id: cred.id,
    rawId: toBase64url(cred.rawId),
    type: cred.type,
    response,
    clientExtensionResults: cred.getClientExtensionResults?.() ?? {},
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
  };
}

/**
 * Tells the browser that a passkey no longer exists, so that it leaves the
 * browser's list. Browsers without this API ignore it.
 */
export async function forgetPasskey(rpId: string, credentialId: string): Promise<void> {
  const signal = (PublicKeyCredential as unknown as { signalUnknownCredential?: (o: { rpId: string; credentialId: string }) => Promise<void> })
    .signalUnknownCredential;
  if (!passkeysSupported() || typeof signal !== "function") return;
  try {
    await signal.call(PublicKeyCredential, { rpId, credentialId });
  } catch {
    // Only a hint for the browser.
  }
}

/** The DOMException name of a failed browser call, or "" for other errors. */
export function browserError(err: unknown): string {
  return err instanceof DOMException ? err.name : "";
}
