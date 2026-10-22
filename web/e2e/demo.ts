// The demo server the end-to-end tests run against (tools/demo), and its
// made-up logins. The values match tools/demo/main.go.
import { createHmac } from "node:crypto";

export const port = 18181;
export const imapPort = 18143;
// localhost, not 127.0.0.1: the server checks the Host header against its
// public URL, and passkeys need a host name.
export const origin = `http://localhost:${port}`;

/** The demo admin. Admins need 2FA; globalSetup logs it in once. */
export const admin = { name: "demo", password: "demo-password", totpSecret: "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP" };
/** A user with 2FA for the login test only, so its time steps are its own. */
export const twoFactorUser = { name: "kim", password: "kim-password", totpSecret: "KRSXG5BAKRSXG5BAKRSXG5BAKRSXG5BA" };
/** A locked user. */
export const lockedUser = { name: "lee", password: "lee-password" };
/** A login on the demo IMAP server that is not an account yet. */
export const spareLogin = { username: "robin@example.net", password: "demo" };

/** The current RFC 6238 time step (30 seconds). */
export function timeStep(now = Date.now()): number {
  return Math.floor(now / 30000);
}

/** The six-digit TOTP code of a base32 secret for a time step. */
export function totp(secret: string, step: number): string {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const c of secret) bits += alphabet.indexOf(c).toString(2).padStart(5, "0");
  const key = Buffer.from((bits.match(/.{8}/g) ?? []).map((b) => parseInt(b, 2)));
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(step));
  const mac = createHmac("sha1", key).update(msg).digest();
  const off = mac[mac.length - 1]! & 0xf;
  return String((mac.readUInt32BE(off) & 0x7fffffff) % 1000000).padStart(6, "0");
}

/** A name no earlier run has used, for things a test creates. */
export function unique(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}${Math.floor(Math.random() * 1296).toString(36)}`;
}
