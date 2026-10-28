// A promise that is already dropped in the base: the gate must not report it
// unless the branch touches this line.
declare function save(): Promise<void>;

export function oldCaller() {
	save(); // old hit, in the base
	save(); // want no-floating-promises (a second one, added by the branch)
}
