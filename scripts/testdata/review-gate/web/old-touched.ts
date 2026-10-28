// A promise that is already dropped in the base: the gate must not report it
// unless the branch touches this line.
declare function save(): Promise<void>;

export function oldCaller() {
	save(); // old hit, in the base
}

export const unrelated = 1; // an unrelated line the branch adds
