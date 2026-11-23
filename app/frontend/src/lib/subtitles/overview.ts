// The Subtitles page: what is missing and why, and the jobs that work on it. Pure, so the rules are testable without a browser.
import { duration } from '../core/format.ts';
import { t } from '../core/i18n/i18n.svelte.ts';
import { missingName, missingQuery, type MissingRow, type SiteView, type Transcriber } from './api.ts';

export type JobState = 'running' | 'queued' | 'failed' | 'cancelled';

export interface OverviewJob {
	id: number;
	kind: string;
	state: JobState;
	/** 0 to 1. */
	progress: number;
	progress_note?: string;
	phase?: string;
	speed?: number;
	speed_unit?: string;
	attempt?: number;
	max_attempts?: number;
	started_at?: string;
	ended_at?: string;
	error?: string;
	file_id?: number;
	title_id?: number;
	name?: string;
	media_kind?: string;
	season?: number | null;
	episode?: number | null;
	relative_path?: string;
	language?: string;
	forced?: boolean;
	model?: string;
}

export interface WaitingJob {
	id: number;
	kind: string;
	state: string;
	priority: 'now' | 'normal' | 'idle' | string;
	position: number;
	file_id?: number;
	title_id?: number;
	name?: string;
	media_kind?: string;
	season?: number | null;
	episode?: number | null;
	relative_path?: string;
	language?: string;
	forced?: boolean;
	created_at: string;
	run_after: string;
}

export interface HistoryItem {
	id: number;
	file_id: number;
	title_id: number;
	title?: string;
	name?: string;
	media_kind?: string;
	season?: number | null;
	episode?: number | null;
	relative_path?: string;
	language: string;
	forced?: boolean;
	hearing_impaired?: boolean;
	state: 'done' | 'skipped';
	how: 'downloaded' | 'translated' | 'speech' | 'skipped';
	source?: string;
	at: string;
}

export type HistoryStateFilter = 'done' | 'skipped' | 'all';

export interface LibraryProgress {
	wanted: number;
	have: number;
	missing: number;
	percent: number;
	files_total: number;
	files_done: number;
	files_missing: number;
	unlooked_files: number;
}

export interface SpeedStat {
	live_speed?: number;
	avg_speed?: number;
	unit: string;
	samples: number;
	total_lines?: number;
	total_audio_seconds?: number;
}

export interface SubtitleStats {
	speech?: SpeedStat;
	translate?: SpeedStat;
}

export interface IntegritySummary {
	ok: number;
	bad: number;
	accepted: number;
	unchecked: number;
	last_checked_at?: string;
}

export interface IntegrityRow {
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
	relative_path: string;
	issue: string;
	note: string;
	coverage?: number;
	last_ms?: number;
	video_seconds?: number;
	checked_at: string;
}

export type SubtitlesTab = 'overview' | 'waiting' | 'history' | 'failed' | 'integrity' | 'settings';
const VALID_TABS = new Set<SubtitlesTab>(['overview', 'waiting', 'history', 'failed', 'integrity', 'settings']);

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
	/** Searches, translations and speech to text wait in the queue. */
	paused: boolean;
	done: DoneRow[];
	done_counts: Record<How, number>;
	jobs: OverviewJob[];
	running: Record<string, number>;
	queued: Record<string, number>;
	library?: LibraryProgress;
	stats?: SubtitleStats;
	failed_count?: number;
	cancelled_count?: number;
	skipped_count?: number;
	integrity?: IntegritySummary;
	eta_seconds?: number;
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
	subtitle_model_download: 'model',
	subtitle_verify: 'verify'
};

/** A job kind in plain words. */
export function jobWord(kind: string): string {
	const k = jobKeys[kind];
	return k ? t(`subtitles.page.job.${k}`) : kind.replaceAll('_', ' ');
}

const phaseKeys = new Set(['waiting_slot', 'extracting', 'detecting', 'transcribing', 'preparing', 'translating', 'verifying']);

/** Maps a job phase token to localized words, passing through plain text notes unchanged. */
export function jobPhaseWord(note?: string): string {
	if (!note) return '';
	return phaseKeys.has(note) ? t(`subtitles.page.phase.${note}`) : note;
}

/** Maps a job phase token to a visual pill tone. */
export function jobPhaseTone(phase?: string | null): 'ok' | 'wait' | 'idle' {
	switch ((phase ?? '').trim().toLowerCase()) {
		case 'transcribing':
		case 'translating':
		case 'verifying':
			return 'ok';
		case 'waiting_slot':
		case 'extracting':
		case 'detecting':
		case 'preparing':
			return 'wait';
		default:
			return 'idle';
	}
}

/** Formats a job or stats speed value with its unit, or '' when absent. */
export function formatSpeed(speed: number | undefined, unit: string | undefined): string {
	if (!speed || !Number.isFinite(speed) || speed <= 0) return '';
	const n = speed >= 100 ? Math.round(speed) : Number(speed.toFixed(1));
	if (unit === 'x') return `${n}x`;
	if (unit === 'lines/s') return `${n} ${t('subtitles.page.stats.linesPerSec')}`;
	return unit ? `${n} ${unit}` : String(n);
}

/** Clamped library subtitle coverage percent rounded to one decimal place. */
export function libraryPercent(lib: LibraryProgress | undefined): number {
	if (!lib || !Number.isFinite(lib.percent)) return 0;
	return Math.round(Math.max(0, Math.min(100, lib.percent)) * 10) / 10;
}

/** Formats an overall backlog ETA in seconds, or returns '' when absent or non-positive. */
export function formatBacklogEta(seconds: number | null | undefined): string {
	if (!seconds || !Number.isFinite(seconds) || seconds <= 0) return '';
	const d = duration(Math.round(seconds));
	if (!d) return '';
	return t('downloads.list.left', { time: `~${d}` });
}

const integrityIssues = new Set(['truncated', 'empty', 'unreadable', 'broken_timing', 'too_few_cues', 'longer_than_video']);

/** Localized label for an integrity issue code. */
export function integrityIssueWord(issue: string): string {
	return integrityIssues.has(issue) ? t(`subtitles.integrity.issue.${issue}`) : issue.replaceAll('_', ' ');
}

/** Formats milliseconds as M:SS or H:MM:SS. */
export function formatTimestamp(ms: number | undefined): string {
	if (ms === undefined || !Number.isFinite(ms) || ms < 0) return '';
	const totalSec = Math.floor(ms / 1000);
	const hours = Math.floor(totalSec / 3600);
	const mins = Math.floor((totalSec % 3600) / 60);
	const secs = totalSec % 60;
	const pad = (v: number) => String(v).padStart(2, '0');
	return hours > 0 ? `${hours}:${pad(mins)}:${pad(secs)}` : `${mins}:${pad(secs)}`;
}

/** Normalizes the ?tab= query parameter to one of the Subtitles page tabs. */
export function parseSubtitlesTab(raw: string | null): SubtitlesTab {
	if (raw && VALID_TABS.has(raw as SubtitlesTab)) return raw as SubtitlesTab;
	return 'overview';
}

/** Extracts the file basename from a relative path. */
export function fileBaseName(path: string | undefined | null): string {
	if (!path) return '';
	const parts = path.split(/[\\/]/);
	return parts[parts.length - 1] || path;
}

/** What a job works on: the movie, the episode, or the model. */
export function jobName(j: {
	name?: string | null;
	title?: string | null;
	season?: number | null;
	episode?: number | null;
	model?: string | null;
}): string {
	const raw = j.name ?? j.title;
	if (raw) {
		const hasEp = j.season != null && j.episode != null;
		return missingName({
			title: raw,
			kind: hasEp ? 'episode' : 'movie',
			season: hasEp ? (j.season ?? undefined) : undefined,
			episode: hasEp ? (j.episode ?? undefined) : undefined
		});
	}
	return j.model ?? '';
}

/** Builds the query string for GET /subtitles/waiting. */
export function waitingQuery(params: { q?: string; kind?: string; limit: number; offset: number }): string {
	const sp = new URLSearchParams();
	sp.set('limit', String(params.limit));
	if (params.offset > 0) sp.set('offset', String(params.offset));
	if (params.q?.trim()) sp.set('q', params.q.trim());
	if (params.kind?.trim()) sp.set('kind', params.kind.trim());
	return sp.toString();
}

/** Builds the query string for GET /subtitles/history. */
export function historyQuery(params: { state: HistoryStateFilter; q?: string; limit: number; offset: number }): string {
	const sp = new URLSearchParams();
	sp.set('state', params.state || 'done');
	if (params.q?.trim()) sp.set('q', params.q.trim());
	sp.set('limit', String(params.limit));
	if (params.offset > 0) sp.set('offset', String(params.offset));
	return sp.toString();
}

/** A running job's percent; null for one that waits. */
export function jobShare(j: Pick<OverviewJob, 'state' | 'progress'>): number | null {
	if (j.state !== 'running') return null;
	return Math.round(Math.max(0, Math.min(1, Number.isFinite(j.progress) ? j.progress : 0)) * 100);
}

const sum = (m: Record<string, number> | undefined) => (m ? Object.values(m).reduce((a, b) => a + b, 0) : 0);

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
