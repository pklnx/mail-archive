import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import { passkeyError } from "./Auth";
import { registrationError } from "./Passkeys";
import { t } from "./i18n";
import { creationOptions, credentialJSON, fromBase64url, requestOptions, toBase64url } from "./webauthn";

const bytes = (b: ArrayBuffer | BufferSource | undefined) => Array.from(new Uint8Array(b as ArrayBuffer));

describe("base64url", () => {
  it("round-trips all byte values without padding", () => {
    const all = new Uint8Array(256).map((_, i) => i);
    const s = toBase64url(all);
    expect(s).not.toMatch(/[+/=]/);
    expect(bytes(fromBase64url(s))).toEqual(Array.from(all));
    expect(toBase64url(new Uint8Array([0xfb, 0xff]))).toBe("-_8");
  });
});

describe("options", () => {
  it("decodes the binary fields of a registration", () => {
    const o = creationOptions({
      challenge: "AQID",
      rp: { id: "archive.example.test", name: "Mail Archive" },
      user: { id: "BAUG", name: "alice", displayName: "alice" },
      excludeCredentials: [{ type: "public-key", id: "Bwg", transports: ["internal"] }],
      pubKeyCredParams: [{ type: "public-key", alg: -7 }],
    });
    expect(bytes(o.challenge)).toEqual([1, 2, 3]);
    expect(bytes(o.user.id)).toEqual([4, 5, 6]);
    expect(o.user.name).toBe("alice");
    expect(o.rp.id).toBe("archive.example.test");
    expect(bytes(o.excludeCredentials?.[0]?.id)).toEqual([7, 8]);
    expect(o.excludeCredentials?.[0]?.transports).toEqual(["internal"]);
  });
  it("decodes the challenge of a login", () => {
    const o = requestOptions({ challenge: "CQo", rpId: "archive.example.test", userVerification: "required" });
    expect(bytes(o.challenge)).toEqual([9, 10]);
    expect(o.userVerification).toBe("required");
    expect(o.allowCredentials).toBeUndefined();
  });
});

describe("credentialJSON", () => {
  const buf = (...b: number[]) => new Uint8Array(b).buffer;
  it("encodes a login answer", () => {
    const cred = {
      id: "AQ",
      rawId: buf(1),
      type: "public-key",
      authenticatorAttachment: "platform",
      getClientExtensionResults: () => ({}),
      response: { clientDataJSON: buf(2), authenticatorData: buf(3), signature: buf(4), userHandle: buf(5) },
    } as unknown as PublicKeyCredential;
    expect(credentialJSON(cred)).toEqual({
      id: "AQ",
      rawId: "AQ",
      type: "public-key",
      authenticatorAttachment: "platform",
      clientExtensionResults: {},
      response: { clientDataJSON: "Ag", authenticatorData: "Aw", signature: "BA", userHandle: "BQ" },
    });
  });
  it("encodes a registration answer with transports", () => {
    const cred = {
      id: "AQ",
      rawId: buf(1),
      type: "public-key",
      authenticatorAttachment: null,
      getClientExtensionResults: () => ({}),
      response: { clientDataJSON: buf(2), attestationObject: buf(3), getTransports: () => ["usb"] },
    } as unknown as PublicKeyCredential;
    expect(credentialJSON(cred).response).toEqual({ clientDataJSON: "Ag", attestationObject: "Aw", transports: ["usb"] });
  });
});

describe("passkey errors", () => {
  it("explains a failed login", () => {
    expect(passkeyError(new DOMException("x", "NotAllowedError"))).toBe(t.passkeyCancelled);
    expect(passkeyError(new ApiError(401, "this passkey is not registered", { unknownCredential: true, rpId: "a", credentialId: "b" }))).toBe(t.passkeyUnknown);
    expect(passkeyError(new ApiError(401, "the passkey login has expired; try again"))).toBe(t.passkeyExpired);
    expect(passkeyError(new ApiError(401, "the passkey was not accepted"))).toBe(t.passkeyFailed);
    expect(passkeyError(new ApiError(403, "this user is locked"))).toBe(t.userLocked);
    expect(passkeyError(new ApiError(429, "x", { retryAfter: 30 }))).toBe(t.tooManyAttempts(30));
  });
  it("explains a failed registration", () => {
    expect(registrationError(new DOMException("x", "InvalidStateError"), 10)).toBe(t.passkeyOnDevice);
    expect(registrationError(new DOMException("x", "NotAllowedError"), 10)).toBe(t.passkeyCancelled);
    expect(registrationError(new ApiError(401, "the current password is wrong"), 10)).toBe(t.wrongCurrentPassword);
    expect(registrationError(new ApiError(401, "invalid two-factor code"), 10)).toBe(t.invalidTwoFactor);
    expect(registrationError(new ApiError(409, "you already have a passkey with this name"), 10)).toBe(t.passkeyNameTaken);
    expect(registrationError(new ApiError(409, "this passkey is already registered"), 10)).toBe(t.passkeyOnDevice);
    expect(registrationError(new ApiError(409, "you already have 10 passkeys; remove one first"), 10)).toBe(t.passkeyLimit(10));
  });
});
