// The fixed-clock time handling of screenshots.mjs, as a pure module (no I/O,
// no browser, no environment), so node --test can cover it.
//
// The times the server records during a run (syncs and the next one, logins,
// received times, the log) follow the real clock. In API answers, each
// RFC 3339 time from an hour before the demo was ready to two days after
// moves to docsNow plus its distance from that moment, rounded to 10
// minutes. That keeps their order and hides how long the run took. The
// mail's own dates (at or before docsNow) and dates far ahead, such as
// retention dates, are outside that window and stay as they are.
//
// The check: a time after docsNow that the rewrite did not move can only come
// from the real clock (or be derived from a mail date), and would change from
// day to day. It is reported, unless its API path and JSON key are in
// STABLE_TIMES.

export const minute = 60000;
export const bucket = 10 * minute;

const timestamp = /"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))"/g;
const exactTimestamp = new RegExp(`^${timestamp.source.slice(1, -1)}$`);

// Times after docsNow that are stable because they derive from a mail date,
// as { path, key }. The path is the API path with ids as {id}; the key matches
// at any depth of the answer (retainUntil is also in each locations[] entry).
export const STABLE_TIMES = [{ path: "/api/messages/{id}", key: "retainUntil" }];

/** The API path with its ids as {id}, as STABLE_TIMES names it. */
export function normalizePath(path) {
	return path.replace(/^(\/api\/messages)\/[^/]+$/, "$1/{id}");
}

function inWindow(t, ready) {
	return t >= ready - 60 * minute && t <= ready + 2 * 24 * 60 * minute;
}

// Walks parsed JSON, calling visit(key, string) for every string value; key is
// the nearest object key (array elements keep the key of their array).
function walk(value, key, visit) {
	if (typeof value === "string") visit(key, value);
	else if (Array.isArray(value)) for (const v of value) walk(v, key, visit);
	else if (value && typeof value === "object") for (const [k, v] of Object.entries(value)) walk(v, k, visit);
}

/**
 * pinTimes rewrites the times in a JSON answer (on the raw text, so large
 * integers keep their precision) and lists those after docsNow it left alone.
 * Returns { body, unpinned: [{ path, key, value }] }. A body that is not JSON
 * is rewritten the same way but not checked.
 */
export function pinTimes(body, { path, docsNow, ready }) {
	const pinned = body.replace(timestamp, (whole, value) => {
		const t = Date.parse(value);
		if (Number.isNaN(t) || !inWindow(t, ready)) return whole;
		return JSON.stringify(new Date(docsNow + Math.round((t - ready) / bucket) * bucket).toISOString());
	});

	let parsed;
	try {
		parsed = JSON.parse(body);
	} catch {
		return { body: pinned, unpinned: [] };
	}
	const normalized = normalizePath(path);
	const unpinned = [];
	walk(parsed, "", (key, value) => {
		if (!exactTimestamp.test(value)) return;
		const t = Date.parse(value);
		if (Number.isNaN(t) || t <= docsNow || inWindow(t, ready)) return;
		if (STABLE_TIMES.some((s) => s.path === normalized && s.key === key)) return;
		unpinned.push({ path, key, value });
	});
	return { body: pinned, unpinned };
}

/** One line per unpinned time, for the error message. */
export function describeUnpinned(unpinned) {
	return unpinned.map((u) => `${u.path} ${u.key}=${u.value}`).join("\n");
}
