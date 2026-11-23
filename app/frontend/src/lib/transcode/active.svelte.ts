// Active transcode jobs and failures for the sidebar, fed by the Transcode overview endpoint.

import { api } from '#lib/core/api.ts';
import { workingCount } from './helpers.ts';
import type { Overview } from './types.ts';

export const activeTranscode = $state<{ count: number; percent: number; failed: number }>({
	count: 0,
	percent: 0,
	failed: 0
});

/** Summarises running and queued transcode jobs, their average running percent, and failed count. */
export function summariseTranscode(
	o: Pick<Overview, 'running' | 'queued' | 'jobs' | 'failed_count'>
): { count: number; percent: number; failed: number } {
	const counts = workingCount(o);
	const count = counts.running + counts.queued;
	const failed = o.failed_count ?? 0;
	const running = (o.jobs ?? []).filter((j) => j.state === 'running');
	if (running.length === 0) return { count, percent: 0, failed };
	const sum = running.reduce(
		(acc, j) => acc + Math.min(Math.max(Number.isFinite(j.progress) ? j.progress : 0, 0), 1),
		0
	);
	return { count, percent: Math.floor((sum / running.length) * 100), failed };
}

/** Syncs the sidebar state directly from an already-fetched overview payload. */
export function syncActiveTranscode(o: Pick<Overview, 'running' | 'queued' | 'jobs' | 'failed_count'>): void {
	const s = summariseTranscode(o);
	activeTranscode.count = s.count;
	activeTranscode.percent = s.percent;
	activeTranscode.failed = s.failed;
}

/** Fetches the transcode overview for the sidebar; errors leave the last answer. */
export async function refreshActiveTranscode(): Promise<void> {
	try {
		const o = await api.get<Overview>('/transcode/overview');
		syncActiveTranscode(o);
	} catch {
		// Keep the last answer.
	}
}

/** Clears the sidebar transcode state when signed out or not allowed. */
export function resetActiveTranscode(): void {
	activeTranscode.count = 0;
	activeTranscode.percent = 0;
	activeTranscode.failed = 0;
}
