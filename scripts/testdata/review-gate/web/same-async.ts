// In the base: a plain function and its caller in the same file.
export async function save(): Promise<void> {}

export function caller() {
	save(); // want no-floating-promises (once save is async)
}
