declare function load(): Promise<boolean>;
declare function useCount(initial: number): number;

export async function awaited() {
	await load();
	void load();
	load().catch(() => undefined);
	if (await load()) {
		return 1;
	}
	return 0;
}

export async function callbacks(xs: number[]) {
	await Promise.all(xs.map(async (x) => {
		await load();
		return x;
	}));
}

export function Panel(open: boolean) {
	const n = useCount(0);
	return open ? n : null;
}
