import { load } from "./lib";

export function use() {
	load(); // want no-floating-promises (once lib.ts makes load async)
}
