// The Subtitles page: what is missing and why, and the jobs that work on it. Pure, so the rules are testable without a browser.
import { t } from '../core/i18n/i18n.svelte.ts';
import { missingName, missingQuery, type MissingRow, type SiteView, type Transcriber } from './api.ts';

export interface OverviewJob {
	id: number;
	kind: string;
	state: 'running' | 'queued';
	/** 0 to 1. */
	progress: number;
	started_at?: string;
	file_id?: number;
	title_id?: number;
	name?: string;
	season?: number;
	episode?: number;
	language?: string;
	model?: string;
}

export type How = 'downloaded' | 'translated' | 'speech';

/** One subtitle HolyArrs saved beside a video. */
export interface DoneRow {
	id: number;
	file_id: number;
	title_id: number;
	title: string;
	kind: 'movie' | 'episode';
	season?: number;
	episode?: number;
	language: string;
	forced: boolean;
	hearing_impaired: boolean;
	how: How;
	/** The site, the translator or the speech model. */
	by?: string;
	saved_at: string;
}

const howKeys: Record<How, string> = { downloaded: 'subtitles.page.done.downloaded', translated: 'subtitles.made.translated', speech: 'subtitles.page.job.transcribe' };

/** How a saved subtitle was made, in plain words; an unknown way reads as downloaded. */
export function howWord(how: string): string {
	return t(howKeys[how as How] ?? howKeys.downloaded);
}

/** The counts of saved subtitles by how they were made, the ways with none left out. */
export function doneParts(c: Record<How, number> | undefined): { how: How; n: number }[] {
	return (['downloaded', 'translated', 'speech'] as How[]).map((how) => ({ how, n: c?.[how] ?? 0 })).filter((x) => x.n > 0);
}

export interface Overview {
	missing: number;
	files: number;
	need_speech: number;
	can_translate: number;
	speech_held: number;
	transcriber: Transcriber;
	transcribe_missing: boolean;
	translator_ready: boolean;
	translator_name: string;
	done: DoneRow[];
	done_counts: Record<How, number>;
	jobs: OverviewJob[];
	running: Record<string, number>;
	queued: Record<string, number>;
}

export type Need = '' | 'speech' | 'text';

/** A missing row as the page reads it: the list's row with what the File has. */
export type NeedRow = MissingRow & { has_text: boolean; speech?: string; speech_reason?: string };

export type SpeechState = 'auto' | 'byHand' | 'noModel' | 'notHere';

/** Whether a subtitle is made from the audio by itself, and if not, why. */
export function speechState(o: Pick<Overview, 'transcriber' | 'transcribe_missing'>): SpeechState {
	if (!o.transcriber.available) return 'notHere';
	if (!o.transcriber.ready) return 'noModel';
	return o.transcribe_missing ? 'auto' : 'byHand';
}

export type Step = 'download' | 'translate' | 'speechAuto' | 'speechByHand' | 'speechOff' | 'speechFinal' | 'speechFailed';

/** What happens next for one missing subtitle. */
export function nextStep(r: Pick<NeedRow, 'has_text' | 'speech'>, o: Pick<Overview, 'transcriber' | 'transcribe_missing' | 'translator_ready'>): Step {
	if (r.has_text) return o.translator_ready ? 'translate' : 'download';
	if (r.speech === 'final') return 'speechFinal';
	if (r.speech === 'failed') return 'speechFailed';
	const s = speechState(o);
	return s === 'auto' ? 'speechAuto' : s === 'byHand' ? 'speechByHand' : 'speechOff';
}

export function stepWords(step: Step, reason = ''): string {
	return t(`subtitles.page.step.${step}`, { reason });
}

const jobKeys: Record<string, string> = {
	subtitle_transcribe: 'transcribe',
	subtitle_translate: 'translate',
	subtitle_search: 'search',
	subtitle_missing: 'missing',
	subtitle_better: 'better',
	subtitle_look: 'look',
	subtitle_model_download: 'model'
};

/** A job kind in plain words. */
export function jobWord(kind: string): string {
	const k = jobKeys[kind];
	return k ? t(`subtitles.page.job.${k}`) : kind.replaceAll('_', ' ');
}

/** What a job works on: the movie, the episode, or the model. */
export function jobName(j: Pick<OverviewJob, 'name' | 'season' | 'episode' | 'model'>): string {
	if (j.name) return missingName({ title: j.name, kind: j.season !== undefined && j.episode !== undefined ? 'episode' : 'movie', season: j.season, episode: j.episode });
	return j.model ?? '';
}

/** A running job's percent; null for one that waits. */
export function jobShare(j: Pick<OverviewJob, 'state' | 'progress'>): number | null {
	if (j.state !== 'running') return null;
	return Math.round(Math.max(0, Math.min(1, Number.isFinite(j.progress) ? j.progress : 0)) * 100);
}

const sum = (m: Record<string, number>) => Object.values(m).reduce((a, b) => a + b, 0);

export function workingCount(o: Pick<Overview, 'running' | 'queued'>): { running: number; queued: number } {
	return { running: sum(o.running), queued: sum(o.queued) };
}

/** Jobs that run or wait but are not in the list. */
export function waitingMore(o: Pick<Overview, 'running' | 'queued' | 'jobs'>): number {
	const c = workingCount(o);
	return Math.max(0, c.running + c.queued - o.jobs.length);
}

/** A site's downloads of today against its cap; cap 0 means it has none. */
export function siteUse(s: Pick<SiteView, 'downloads_today' | 'limits'>): { used: number; cap: number; percent: number } {
	const cap = s.limits?.downloads_per_day ?? 0;
	return { used: s.downloads_today, cap, percent: cap > 0 ? Math.min(100, Math.round((s.downloads_today / cap) * 100)) : 0 };
}

export function siteUseWords(s: Pick<SiteView, 'downloads_today' | 'limits'>): string {
	const u = siteUse(s);
	return u.cap > 0 ? t('subtitles.page.sites.today', { used: u.used, cap: u.cap }) : t('subtitles.page.sites.todayNoCap', { used: u.used });
}

/** The missing list's query with the need filter. */
export function missingNeedQuery(f: { need: Need; language?: string; kind?: string; title?: number }, limit: number, offset: number): string {
	const q = new URLSearchParams(missingQuery(f, limit, offset));
	if (f.need) q.set('need', f.need);
	q.sort();
	return q.toString();
}
