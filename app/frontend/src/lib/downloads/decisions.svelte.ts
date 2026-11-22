// The Needs a decision badge on Downloads: how many movies wait for a person. Read again on page changes, on Slot news and every minute.

import { api } from '../core/api.ts';
import type { Decision } from '../core/types.ts';

export const decisions = $state<{ count: number; items: Decision[] }>({ count: 0, items: [] });

/** Errors leave the last list. */
async function read(): Promise<void> {
	try {
		const r = await api.get<{ items: Decision[]; count: number }>('/needs-decision');
		decisions.items = r.items;
		decisions.count = r.count;
	} catch {
		// Keep the last list.
	}
}

let running: Promise<void> | null = null;
let again = false;

/**
 * One read at a time. Slot news comes in bursts (a search over many titles
 * moves hundreds of Slots), and a read per piece of news piled up on the
 * server. A call made while a read is out asks for one more read after it and
 * ends with that one, so what it sees is never older than the call.
 */
export function refreshDecisions(): Promise<void> {
	if (running) {
		again = true;
		return running;
	}
	running = (async () => {
		do {
			again = false;
			await read();
		} while (again);
	})().finally(() => {
		running = null;
	});
	return running;
}
