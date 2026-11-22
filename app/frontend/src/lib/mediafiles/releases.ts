// Plain-words labels, warnings and paging for the Releases list and search status lines.
import type { Reason, ReleaseView } from '../core/types.ts';
import { t } from '../core/i18n/i18n.svelte.ts';

export interface Pager {
	from: number;
	to: number;
	prev: number | null;
	next: number | null;
}

export interface ReleaseScope {
	// Heading shown for what is being searched.
	label: string;
	// Search text sent to the sources.
	query: string;
	// Set when the search is for one slot only.
	slotId?: number;
	slotState?: string;
}

const outcomeKeys = ['chosen', 'held', 'passed_over', 'refused', 'turned_down'];
const outcomeWord = (k: string): string | undefined => (outcomeKeys.includes(k) ? t(`mediafiles.release.${k}`) : undefined);

// A real state beats the judged outcome; an outcome alone is only what "would" happen.
export function verdict(r: Pick<ReleaseView, 'state' | 'outcome'>): string {
	if (r.state === 'turned_down') return t('mediafiles.release.turned_down');
	if (r.state === 'chosen') return t('mediafiles.release.chosen');
	if (r.state === 'held') return t('mediafiles.release.held');
	if (r.outcome === 'chosen') return t('mediafiles.release.wouldChoose');
	if (r.outcome === 'held') return t('mediafiles.release.wouldHold');
	return outcomeWord(r.outcome) ?? outcomeWord(r.state) ?? t('mediafiles.release.judged');
}

export function verdictTone(r: Pick<ReleaseView, 'state' | 'outcome'>): 'red' | 'ok' | 'wait' | 'fail' | 'idle' {
	if (r.state === 'chosen') return 'red';
	if (r.state === 'turned_down' || r.outcome === 'refused') return 'fail';
	if (r.outcome === 'chosen') return 'ok';
	if (r.outcome === 'held' || r.state === 'held') return 'wait';
	return 'idle';
}

const TOO_FEW_POINTS = 'points.below_min';

// Only reasons that changed the result are worth showing up front. "Too few
// points" alone names no rule, so the rules that took points follow it,
// the costliest first.
export function keyReasons(reasons: Reason[]): Reason[] {
	const key = reasons.filter((x) => x.effect === 'fail' || x.effect === 'rank' || x.effect === 'hold' || x.effect === 'warn');
	const at = key.findIndex((x) => x.code === TOO_FEW_POINTS);
	if (at < 0) return key;
	const took = reasons.filter((x) => x.effect === 'subtract').sort((x, y) => (x.points ?? 0) - (y.points ?? 0));
	return [...key.slice(0, at + 1), ...took, ...key.slice(at + 1)];
}

export function facts(r: ReleaseView, size: (n: number) => string): string {
	return [
		r.source_name,
		size(r.size),
		r.seeders != null ? t('mediafiles.release.seeders', { n: r.seeders }) : '',
		r.protocol,
		r.passed ? t('mediafiles.release.points', { n: r.points }) : '',
		r.passed && r.rank ? t('mediafiles.release.ranked', { n: r.rank }) : ''
	]
		.filter(Boolean)
		.join(' · ');
}

// Tells the user what choosing this release overrides, worded for a movie, show or album.
export function chooseWarnings(r: ReleaseView, slotState?: string, show = false, music = false): string[] {
	const out: string[] = [];
	if (r.needs_force) {
		const fails = r.reasons.filter((x) => x.effect === 'fail').map((x) => x.text);
		out.push(t('mediafiles.release.didNotPass', { why: fails.length ? fails.join('; ') : t('mediafiles.release.wasRefused') }));
	}
	if (music) {
		if (r.gate_skipped || slotState === 'not_out_yet') out.push(t('mediafiles.release.albumNotOut'));
		const n = r.slot_ids?.length ?? 0;
		if (n > 1) out.push(r.album ? t('mediafiles.release.fillsTracksOf', { n, album: r.album }) : t('mediafiles.release.fillsTracks', { n }));
		return out;
	}
	if (show) {
		if (r.gate_skipped || slotState === 'not_out_yet') out.push(t('mediafiles.release.episodeNotAired'));
		if (slotState === 'in_library') out.push(t('mediafiles.release.episodeHasCopy'));
		if ((r.slot_ids?.length ?? 0) > 1 && r.episode) out.push(t('mediafiles.release.fillsEpisode', { episode: r.episode }));
		return out;
	}
	if (r.gate_skipped || slotState === 'not_out_yet') out.push(t('mediafiles.release.gateSkipped'));
	if (slotState === 'in_library') out.push(t('mediafiles.release.versionHasCopy'));
	return out;
}

export function listedName(r: Pick<ReleaseView, 'title_name' | 'episode' | 'turned_down_for' | 'state' | 'album'>): string {
	if (r.album) return [r.title_name, r.album].filter(Boolean).join(' – ');
	const ep = r.state === 'turned_down' && r.turned_down_for ? r.turned_down_for : r.episode;
	return [r.title_name, ep].filter(Boolean).join(' ');
}

// Shows "from to total" bounds, clamping an offset that ran past the end.
export function pager(total: number, limit: number, offset: number): Pager {
	if (total <= 0) return { from: 0, to: 0, prev: null, next: null };
	const from = Math.min(offset, total - 1) + 1;
	const to = Math.min(offset + limit, total);
	return { from, to, prev: offset > 0 ? Math.max(0, offset - limit) : null, next: to < total ? offset + limit : null };
}

// 0 turns feed checks off; otherwise 10 to 120 minutes.
export function feedMinutesOk(n: number): boolean {
	return Number.isInteger(n) && (n === 0 || (n >= 10 && n <= 120));
}
