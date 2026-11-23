import { describe, expect, it } from 'vitest';
import {
	activeTranscode,
	resetActiveTranscode,
	summariseTranscode,
	syncActiveTranscode
} from './active.svelte.ts';
import type { OverviewJob } from './types.ts';

const job = (over: Partial<OverviewJob>): OverviewJob => ({
	id: 1,
	kind: 'transcode_file',
	state: 'running',
	progress: 0,
	phase: 'encoding',
	note: '',
	fps: 0,
	speed: 0,
	encoder: 'av1_qsv',
	gpu_device: '/dev/dri/renderD128',
	input_bytes: 1_000_000_000,
	created_at: '2026-01-01T00:00:00Z',
	...over
});

describe('summariseTranscode', () => {
	it('returns zeroes when nothing runs or fails', () => {
		expect(
			summariseTranscode({ running: {}, queued: {}, jobs: [], failed_count: 0 })
		).toEqual({ count: 0, percent: 0, failed: 0 });
	});

	it('averages only running jobs and counts both running and queued', () => {
		const summary = summariseTranscode({
			running: { transcode_file: 2 },
			queued: { transcode_file: 4 },
			jobs: [
				job({ id: 1, state: 'running', progress: 0.25 }),
				job({ id: 2, state: 'running', progress: 0.75 }),
				job({ id: 3, state: 'queued', progress: 0.9 })
			],
			failed_count: 2
		});
		expect(summary).toEqual({ count: 6, percent: 50, failed: 2 });
	});

	it('clamps out-of-range progress and reports failed count when idle', () => {
		expect(
			summariseTranscode({
				running: { transcode_file: 2 },
				queued: {},
				jobs: [
					job({ id: 1, state: 'running', progress: -0.5 }),
					job({ id: 2, state: 'running', progress: 1.8 })
				],
				failed_count: 5
			})
		).toEqual({ count: 2, percent: 50, failed: 5 });

		expect(
			summariseTranscode({ running: {}, queued: {}, jobs: [], failed_count: 3 })
		).toEqual({ count: 0, percent: 0, failed: 3 });
	});

	it('syncs and resets the reactive activeTranscode state', () => {
		syncActiveTranscode({
			running: { transcode_file: 1 },
			queued: { transcode_file: 3 },
			jobs: [job({ id: 1, state: 'running', progress: 0.65 })],
			failed_count: 1
		});
		expect(activeTranscode).toEqual({ count: 4, percent: 65, failed: 1 });
		resetActiveTranscode();
		expect(activeTranscode).toEqual({ count: 0, percent: 0, failed: 0 });
	});
});
