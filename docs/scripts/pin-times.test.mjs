import assert from "node:assert/strict";
import { test } from "node:test";
import { bucket, describeUnpinned, minute, pinTimes } from "./pin-times.mjs";

const docsNow = Date.parse("2026-10-23T08:41:00Z");
const ready = Date.parse("2026-10-29T17:03:12Z");
const iso = (t) => new Date(t).toISOString();
const run = (value, path = "/api/sessions") => pinTimes(JSON.stringify(value), { path, docsNow, ready });

test("a time in the window moves next to docsNow", () => {
	const { body, unpinned } = run({ lastSync: iso(ready + 3 * minute) });
	assert.deepEqual(JSON.parse(body), { lastSync: iso(docsNow) });
	assert.deepEqual(unpinned, []);
});

test("the next sync keeps its distance from ready", () => {
	const { body } = run({ next: iso(ready + 6 * 60 * minute) });
	assert.equal(JSON.parse(body).next, iso(docsNow + 36 * bucket));
});

test("rounding: just under half a bucket goes down, half a bucket goes up", () => {
	assert.equal(JSON.parse(run({ t: iso(ready + 5 * minute - 1000) }).body).t, iso(docsNow));
	assert.equal(JSON.parse(run({ t: iso(ready + 5 * minute) }).body).t, iso(docsNow + bucket));
});

test("the window edges are pinned, just outside is not", () => {
	assert.deepEqual(run({ t: iso(ready - 60 * minute) }).unpinned, []);
	assert.deepEqual(run({ t: iso(ready + 2 * 24 * 60 * minute) }).unpinned, []);
	assert.equal(run({ t: iso(ready + 2 * 24 * 60 * minute + 1000) }).unpinned.length, 1);
});

test("a 30-day expiry is reported with its path, key and value", () => {
	const value = iso(ready + 30 * 24 * 60 * minute);
	const { body, unpinned } = run({ session: { expiresAt: value } });
	assert.deepEqual(unpinned, [{ path: "/api/sessions", key: "expiresAt", value }]);
	assert.equal(JSON.parse(body).session.expiresAt, value);
	assert.equal(describeUnpinned(unpinned), `/api/sessions expiresAt=${value}`);
});

test("a mail date at or before docsNow passes and stays", () => {
	for (const t of [docsNow, docsNow - 12 * 365 * 86400000]) {
		const { body, unpinned } = run({ date: iso(t) });
		assert.deepEqual(unpinned, []);
		assert.equal(JSON.parse(body).date, iso(t));
	}
});

test("retainUntil on /api/messages/{id} passes, at the top and in locations[]", () => {
	const retainUntil = "2034-12-31T23:00:00Z";
	const path = "/api/messages/3f9a0c";
	assert.deepEqual(run({ retainUntil }, path).unpinned, []);
	assert.deepEqual(run({ locations: [{ retainUntil }, { retainUntil }] }, path).unpinned, []);
	assert.deepEqual(run({ a: { b: [{ c: { retainUntil } }] } }, path).unpinned, []);
});

test("retainUntil on another path is reported", () => {
	const retainUntil = "2034-12-31T23:00:00Z";
	for (const path of ["/api/messages", "/api/messages/3f9a0c/raw", "/api/search"]) {
		assert.equal(run({ retainUntil }, path).unpinned.length, 1, path);
	}
});

test("another key on /api/messages/{id} is reported, also nested", () => {
	const value = "2034-12-31T23:00:00Z";
	const path = "/api/messages/3f9a0c";
	assert.deepEqual(run({ holdUntil: value }, path).unpinned, [{ path, key: "holdUntil", value }]);
	assert.equal(run({ locations: [{ holdUntil: value }] }, path).unpinned.length, 1);
});

test("a body that is not JSON passes through unchanged", () => {
	const text = "<html>not json 2099-01-01T00:00:00Z</html>";
	assert.deepEqual(pinTimes(text, { path: "/api/x", docsNow, ready }), { body: text, unpinned: [] });
});

test("large integers keep their precision", () => {
	const text = '{"id":9007199254740993,"t":"2020-01-01T00:00:00Z"}';
	assert.equal(pinTimes(text, { path: "/api/x", docsNow, ready }).body, text);
});
