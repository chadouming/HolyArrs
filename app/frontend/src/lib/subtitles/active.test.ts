import { describe, expect, it } from 'vitest';
import { activeSubtitles, resetActiveSubtitles, summariseSubtitles, syncActiveSubtitles } from './active.svelte.ts';
import type { OverviewJob } from './overview.ts';

const job = (over: Partial<OverviewJob>): OverviewJob => ({ id: 1, kind: 'subtitle_transcribe', state: 'running', progress: 0, ...over });

describe('summariseSubtitles', () => {
	it('returns zeroes when nothing runs or fails', () => {
		expect(summariseSubtitles({ running: {}, queued: {}, jobs: [] })).toEqual({ count: 0, percent: 0, failed: 0 });
	});

	it('averages only running jobs and counts both running and queued', () => {
		const summary = summariseSubtitles({
			running: { subtitle_transcribe: 2 },
			queued: { subtitle_translate: 3 },
			jobs: [
				job({ id: 1, state: 'running', progress: 0.25 }),
				job({ id: 2, state: 'running', progress: 0.75 }),
				job({ id: 3, state: 'queued', progress: 0.9 })
			],
			failed_count: 2
		});
		expect(summary).toEqual({ count: 5, percent: 50, failed: 2 });
	});

	it('clamps out-of-range progress and reports failed count when idle', () => {
		expect(
			summariseSubtitles({
				running: { subtitle_search: 2 },
				queued: {},
				jobs: [job({ id: 1, state: 'running', progress: -0.5 }), job({ id: 2, state: 'running', progress: 1.8 })],
				failed_count: 4
			})
		).toEqual({ count: 2, percent: 50, failed: 4 });

		expect(summariseSubtitles({ running: {}, queued: {}, jobs: [], failed_count: 3 })).toEqual({ count: 0, percent: 0, failed: 3 });
	});

	it('syncs and resets the reactive activeSubtitles state', () => {
		syncActiveSubtitles({
			running: { subtitle_transcribe: 1 },
			queued: { subtitle_search: 2 },
			jobs: [job({ id: 1, state: 'running', progress: 0.65 })],
			failed_count: 1
		});
		expect(activeSubtitles).toEqual({ count: 3, percent: 65, failed: 1 });
		resetActiveSubtitles();
		expect(activeSubtitles).toEqual({ count: 0, percent: 0, failed: 0 });
	});
});
