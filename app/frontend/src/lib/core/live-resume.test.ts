import { afterEach, describe, expect, it, vi } from 'vitest';
import { createLive, type SourceFactory } from './live.svelte.ts';

function fakeDoc(hidden = false) {
	const listeners = new Set<() => void>();
	const doc = {
		hidden,
		addEventListener(type: string, fn: () => void) {
			if (type === 'visibilitychange') listeners.add(fn);
		},
		removeEventListener(type: string, fn: () => void) {
			if (type === 'visibilitychange') listeners.delete(fn);
		},
		setHidden(next: boolean) {
			doc.hidden = next;
			for (const fn of listeners) fn();
		}
	};
	vi.stubGlobal('document', doc);
	return doc;
}

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

function fakeSources() {
	const made: {
		url: string;
		onopen: null | ((e: Event) => void);
		onerror: null | ((e: Event) => void);
		onmessage: null | ((e: MessageEvent) => void);
		close: () => void;
	}[] = [];
	const factory = ((url: string) => {
		const s = { url, onopen: null, onerror: null, onmessage: null, close: vi.fn() };
		made.push(s);
		return s;
	}) as unknown as SourceFactory;
	return { made, factory };
}

const msg = (topic: string, id: string) => new MessageEvent('message', { data: JSON.stringify({ topic, data: {} }), lastEventId: id });

describe('live resume', () => {
	it('reopens with ?last= after an error and lets the server decide', () => {
		vi.useFakeTimers();
		const { made, factory } = fakeSources();
		const live = createLive(factory);
		const title = vi.fn();
		live.on('title', title);
		live.start();
		made[0].onopen?.(new Event('open'));
		made[0].onmessage?.(msg('title', 'b00t-7'));
		made[0].onerror?.(new Event('error'));
		vi.advanceTimersByTime(20_000);
		expect(made[1].url).toBe('/live?last=b00t-7');
		made[1].onopen?.(new Event('open'));
		expect(title).toHaveBeenCalledTimes(1);
		live.stop();
		vi.useRealTimers();
	});

	it('reloads every listener when the boot id changes (server restart)', () => {
		const { made, factory } = fakeSources();
		const live = createLive(factory);
		const job = vi.fn();
		const ask = vi.fn();
		live.on('job', job);
		live.on('ask', ask);
		live.start();
		made[0].onmessage?.(msg('job', 'aaaa-1'));
		made[0].onmessage?.(msg('job', 'aaaa-2'));
		expect(ask).not.toHaveBeenCalled();
		made[0].onmessage?.(msg('job', 'bbbb-1'));
		expect(ask).toHaveBeenCalledWith(null, 'resync');
		expect(ask).toHaveBeenCalledTimes(1);
		live.stop();
	});

	it('reloads after a reconnect when there was no id to resume from', () => {
		vi.useFakeTimers();
		const { made, factory } = fakeSources();
		const live = createLive(factory);
		const title = vi.fn();
		live.on('title', title);
		live.start();
		made[0].onopen?.(new Event('open'));
		made[0].onerror?.(new Event('error'));
		vi.advanceTimersByTime(20_000);
		expect(made[1].url).toBe('/live');
		made[1].onopen?.(new Event('open'));
		expect(title).toHaveBeenCalledWith(null, 'resync');
		live.stop();
		vi.useRealTimers();
	});

	it('pauses on error while hidden instead of retrying in the background, and reconnects immediately when visible', () => {
		vi.useFakeTimers();
		const doc = fakeDoc(false);
		const { made, factory } = fakeSources();
		const live = createLive(factory);
		live.start();
		made[0].onopen?.(new Event('open'));
		expect(live.status.state).toBe('live');

		doc.hidden = true;
		made[0].onerror?.(new Event('error'));
		expect(live.status.state).toBe('live');
		expect(made[0].close).toHaveBeenCalledTimes(1);

		vi.advanceTimersByTime(60_000);
		expect(made).toHaveLength(1);

		doc.setHidden(false);
		expect(made).toHaveLength(2);
		live.stop();
	});

	it('cancels a pending 15s backoff timer and reconnects immediately on returning to visible', () => {
		vi.useFakeTimers();
		const doc = fakeDoc(false);
		const { made, factory } = fakeSources();
		const live = createLive(factory);
		live.start();
		made[0].onopen?.(new Event('open'));

		// Escalate tries to the 15s cap: 0ms, 2s, 4s, 8s, then 15s pending.
		made[0].onerror?.(new Event('error'));
		vi.advanceTimersByTime(0);
		made[1].onerror?.(new Event('error'));
		vi.advanceTimersByTime(2_000);
		made[2].onerror?.(new Event('error'));
		vi.advanceTimersByTime(4_000);
		made[3].onerror?.(new Event('error'));
		vi.advanceTimersByTime(8_000);
		expect(made).toHaveLength(5);
		made[4].onerror?.(new Event('error'));
		expect(live.status.state).toBe('reconnecting');

		// Brief hide (< 10s pause delay) and return to visible must bypass the 15s wait and reset tries.
		doc.setHidden(true);
		vi.advanceTimersByTime(1_000);
		doc.setHidden(false);
		expect(made).toHaveLength(6);

		// Confirm tries was reset to 0: next error schedules a 1s retry, not 15s.
		made[5].onerror?.(new Event('error'));
		vi.advanceTimersByTime(999);
		expect(made).toHaveLength(6);
		vi.advanceTimersByTime(1);
		expect(made).toHaveLength(7);
		live.stop();
	});

	it('stops retrying and invokes onUnauthorized when probeAuth returns unauthorized', async () => {
		vi.useFakeTimers();
		fakeDoc(false);
		const { made, factory } = fakeSources();
		const probe = vi.fn(async () => 'unauthorized' as const);
		const unauth = vi.fn();
		const live = createLive(factory, undefined, probe);
		live.onUnauthorized(unauth);
		live.start();

		made[0].onerror?.(new Event('error'));
		expect(probe).not.toHaveBeenCalled();
		vi.advanceTimersByTime(1_000);
		expect(made).toHaveLength(2);

		made[1].onerror?.(new Event('error'));
		await Promise.resolve();
		await Promise.resolve();

		expect(probe).toHaveBeenCalledTimes(1);
		expect(live.status.state).toBe('off');
		expect(unauth).toHaveBeenCalledTimes(1);

		vi.advanceTimersByTime(30_000);
		expect(made).toHaveLength(2);
	});

	it('resets backoff and reopens immediately when probeAuth returns ok while reconnecting', async () => {
		vi.useFakeTimers();
		fakeDoc(false);
		const { made, factory } = fakeSources();
		const probe = vi.fn(async () => 'ok' as const);
		const live = createLive(factory, undefined, probe);
		live.start();

		made[0].onerror?.(new Event('error'));
		vi.advanceTimersByTime(1_000);
		expect(made).toHaveLength(2);

		made[1].onerror?.(new Event('error'));
		expect(live.status.state).toBe('reconnecting');
		await Promise.resolve();
		await Promise.resolve();

		expect(probe).toHaveBeenCalledTimes(1);
		expect(made).toHaveLength(3);

		// Verify backoff was reset to 0 by probeAuth returning ok.
		made[2].onerror?.(new Event('error'));
		vi.advanceTimersByTime(999);
		expect(made).toHaveLength(3);
		vi.advanceTimersByTime(1);
		expect(made).toHaveLength(4);
		live.stop();
	});
});
