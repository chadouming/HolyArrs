// Active subtitle jobs and failures for the sidebar, fed by the same overview endpoint as the Subtitles page.

import { api } from '../core/api.ts';
import { workingCount, type Overview } from './overview.ts';

export const activeSubtitles = $state<{ count: number; percent: number; failed: number }>({ count: 0, percent: 0, failed: 0 });

/** Summarises running and queued subtitle jobs, their average running percent, and failed count. */
export function summariseSubtitles(o: Pick<Overview, 'running' | 'queued' | 'jobs' | 'failed_count'>): { count: number; percent: number; failed: number } {
	const counts = workingCount(o);
	const count = counts.running + counts.queued;
	const failed = o.failed_count ?? 0;
	const running = (o.jobs ?? []).filter((j) => j.state === 'running');
	if (running.length === 0) return { count, percent: 0, failed };
	const sum = running.reduce((a, j) => a + Math.min(Math.max(Number.isFinite(j.progress) ? j.progress : 0, 0), 1), 0);
	return { count, percent: Math.floor((sum / running.length) * 100), failed };
}

/** Syncs the sidebar state directly from an already-fetched overview payload. */
export function syncActiveSubtitles(o: Pick<Overview, 'running' | 'queued' | 'jobs' | 'failed_count'>): void {
	const s = summariseSubtitles(o);
	activeSubtitles.count = s.count;
	activeSubtitles.percent = s.percent;
	activeSubtitles.failed = s.failed;
}

/** Fetches the subtitles overview for the sidebar; errors leave the last answer. */
export async function refreshActiveSubtitles(): Promise<void> {
	try {
		const o = await api.get<Overview>('/subtitles/overview');
		syncActiveSubtitles(o);
	} catch {
		// Keep the last answer.
	}
}

/** Clears the sidebar subtitle state when signed out or not allowed. */
export function resetActiveSubtitles(): void {
	activeSubtitles.count = 0;
	activeSubtitles.percent = 0;
	activeSubtitles.failed = 0;
}
