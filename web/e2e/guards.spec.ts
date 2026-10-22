// The guards' exceptions for the sandboxed mail frame stay narrow. No
// browser needed: these check the matcher on the messages Chromium logs.
import { test as base, expect } from "@playwright/test";
import { origin } from "./demo";
import { fromMailFrame } from "./fixtures";

const frame = `${origin}/api/messages/${"ab".repeat(32)}/html`;
const pixel = "https://tracker.example/pixel.gif";
const policy = `"img-src 'self' ${origin} https://localhost:18181 data:"`;
// Chromium 156 (Playwright's headless shell) and Chromium 141.
const blocked = `Loading the image '${pixel}' violates the following Content Security Policy directive: ${policy}. The action has been blocked.`;
const refused = `Refused to load the image '${pixel}' because it violates the following Content Security Policy directive: ${policy}.\n`;

base("a remote image blocked in the mail frame is expected, in both wordings", () => {
  expect(fromMailFrame(frame, blocked)).toBe(true);
  expect(fromMailFrame(frame, refused)).toBe(true);
  expect(fromMailFrame(`${frame}?images=1`, blocked)).toBe(true);
});

base("other CSP violations still fail", () => {
  // Outside the mail frame: the app itself.
  expect(fromMailFrame(`${origin}/`, blocked)).toBe(false);
  expect(fromMailFrame(`${origin}/api/messages/${"ab".repeat(32)}/parts/1`, blocked)).toBe(false);
  // Another directive, or a style instead of an image.
  expect(fromMailFrame(frame, blocked.replace("img-src", "style-src"))).toBe(false);
  expect(fromMailFrame(frame, blocked.replace("the image", "the stylesheet"))).toBe(false);
  // An image from the demo server itself is not "remote".
  expect(fromMailFrame(frame, blocked.replace(pixel, `${origin}/logo.png`))).toBe(false);
  expect(fromMailFrame(frame, blocked.replace(pixel, "http://127.0.0.1:18181/logo.png"))).toBe(false);
  // Inline script, fonts, connections.
  expect(fromMailFrame(frame, `Refused to execute inline script because it violates the following Content Security Policy directive: "script-src 'none'".`)).toBe(false);
  expect(fromMailFrame(frame, `Loading the font 'https://fonts.example/x.woff2' violates the following Content Security Policy directive: "font-src 'none'". The action has been blocked.`)).toBe(false);
});
