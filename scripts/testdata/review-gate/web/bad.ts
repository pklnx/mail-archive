declare function load(): Promise<boolean>;
declare function useCount(initial: number): number;

export async function floating() {
	load(); // want no-floating-promises
}

export function condition() {
	if (load()) { // want promise in a condition
		return 1;
	}
	return 0;
}

export function callback(xs: number[]) {
	xs.forEach(async (x) => { // want promise-returning callback
		await load();
		return x;
	});
}

export function Panel(open: boolean) {
	if (open) {
		useCount(0); // want rules-of-hooks
	}
	return null;
}
