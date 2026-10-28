// In the base: a plain function and its caller in the same file.
export function save(): void {}

export function caller() {
	save(); // want no-floating-promises (once save is async)
}
