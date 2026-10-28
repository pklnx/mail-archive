// The branch turns load async; use.ts, which it does not touch, now drops
// the promise.
export async function load(): Promise<void> {}
