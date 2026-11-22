import { beforeEach, describe, expect, it, vi } from 'vitest';

const get = vi.fn();
vi.mock('../core/api.ts', () => ({ api: { get: (...a: unknown[]) => get(...a) } }));

import { decisions, refreshDecisions } from './decisions.svelte.ts';

/** A read the test finishes by hand. */
function pending() {
	let done!: (v: { items: never[]; count: number }) => void;
	const p = new Promise<{ items: never[]; count: number }>((r) => (done = r));
	return { p, done };
}

describe('refreshDecisions', () => {
	beforeEach(() => {
		get.mockReset();
	});

	it('runs one read at a time: a burst of news while one is out becomes one more read', async () => {
		const first = pending();
		const second = pending();
		get.mockReturnValueOnce(first.p).mockReturnValueOnce(second.p);
		const calls = [refreshDecisions(), refreshDecisions(), refreshDecisions(), refreshDecisions()];
		expect(get).toHaveBeenCalledTimes(1);
		first.done({ items: [], count: 1 });
		await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(2));
		second.done({ items: [], count: 2 });
		await Promise.all(calls);
		expect(get).toHaveBeenCalledTimes(2);
		expect(decisions.count).toBe(2);
	});

	it('a call made while a read is out resolves only after a read that started later', async () => {
		const first = pending();
		const second = pending();
		get.mockReturnValueOnce(first.p).mockReturnValueOnce(second.p);
		void refreshDecisions();
		let settled = false;
		const late = refreshDecisions().then(() => (settled = true));
		first.done({ items: [], count: 1 });
		await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(2));
		expect(settled).toBe(false);
		second.done({ items: [], count: 5 });
		await late;
		expect(decisions.count).toBe(5);
	});

	it('reads again after the last one ended', async () => {
		get.mockResolvedValue({ items: [], count: 3 });
		await refreshDecisions();
		await refreshDecisions();
		expect(get).toHaveBeenCalledTimes(2);
	});

	it('keeps the last list when a read fails', async () => {
		get.mockResolvedValueOnce({ items: [], count: 4 }).mockRejectedValueOnce(new Error('down'));
		await refreshDecisions();
		await refreshDecisions();
		expect(decisions.count).toBe(4);
	});
});
