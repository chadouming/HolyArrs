import { describe, expect, it } from 'vitest';
import {
	doneParts, fileBaseName, formatBacklogEta, formatSpeed, formatTimestamp, historyQuery, howWord, integrityIssueWord, jobName,
	jobPhaseTone, jobPhaseWord, jobShare, jobWord, libraryPercent, missingNeedQuery, nextStep, parseSubtitlesTab,
	siteUse, speechState, waitingMore, waitingQuery, workingCount, type Overview, type OverviewJob
} from './overview.ts';

const base: Overview = {
	missing: 3,
	files: 3,
	need_speech: 2,
	can_translate: 1,
	speech_held: 0,
	transcriber: { available: true, ready: true, model: 'Whisper' },
	transcribe_missing: true,
	translator_ready: true,
	translator_name: 'Built in',
	paused: false,
	done: [],
	done_counts: { downloaded: 0, translated: 0, speech: 0 },
	jobs: [],
	running: {},
	queued: {}
};
const job = (over: Partial<OverviewJob>): OverviewJob => ({ id: 1, kind: 'subtitle_transcribe', state: 'running', progress: 0.4, ...over });

describe('speechState', () => {
	it('says why speech to text will not run by itself', () => {
		expect(speechState(base)).toBe('auto');
		expect(speechState({ ...base, transcribe_missing: false })).toBe('byHand');
		expect(speechState({ ...base, transcriber: { available: true, ready: false, model: '' } })).toBe('noModel');
		expect(speechState({ ...base, transcriber: { available: false, ready: false, model: '' } })).toBe('notHere');
	});
});

describe('nextStep', () => {
	const row = { has_text: false, speech: undefined as string | undefined };
	it('a file with nothing to read needs speech to text', () => {
		expect(nextStep(row, base)).toBe('speechAuto');
		expect(nextStep(row, { ...base, transcribe_missing: false })).toBe('speechByHand');
		expect(nextStep(row, { ...base, transcriber: { available: false, ready: false, model: '' } })).toBe('speechOff');
	});
	it('a transcript that ended for good or failed is said', () => {
		expect(nextStep({ ...row, speech: 'final' }, base)).toBe('speechFinal');
		expect(nextStep({ ...row, speech: 'failed' }, base)).toBe('speechFailed');
	});
	it('a file with another language can be translated when a translator is set', () => {
		expect(nextStep({ ...row, has_text: true }, base)).toBe('translate');
		expect(nextStep({ ...row, has_text: true }, { ...base, translator_ready: false })).toBe('download');
	});
});

describe('jobs', () => {
	it('names a job by its title and episode, or its model', () => {
		expect(jobName(job({ name: 'Quiet Harbour', season: 1, episode: 2 }))).toBe('Quiet Harbour S01E02');
		expect(jobName(job({ name: 'The Lantern' }))).toBe('The Lantern');
		expect(jobName(job({ name: 'The Lantern', season: null, episode: null }))).toBe('The Lantern');
		expect(jobName(job({ kind: 'subtitle_model_download', model: 'Whisper' }))).toBe('Whisper');
		expect(jobName(job({ kind: 'subtitle_missing' }))).toBe('');
		expect(fileBaseName('Show/Season 1/Show.S01E01.mkv')).toBe('Show.S01E01.mkv');
		expect(fileBaseName('')).toBe('');
	});
	it('has a word for every kind it follows and a plain one for any other', () => {
		for (const k of ['subtitle_transcribe', 'subtitle_translate', 'subtitle_search', 'subtitle_missing', 'subtitle_better', 'subtitle_look', 'subtitle_model_download', 'subtitle_verify'])
			expect(jobWord(k)).not.toBe(k);
		expect(jobWord('other_kind')).toBe('other kind');
	});
	it('shows a share only for a running job', () => {
		expect(jobShare(job({ progress: 0.4 }))).toBe(40);
		expect(jobShare(job({ state: 'queued', progress: 0.4 }))).toBeNull();
		expect(jobShare(job({ progress: 7 }))).toBe(100);
	});
	it('counts what runs and waits, and what the list leaves out', () => {
		const o = { ...base, jobs: [job({}), job({ id: 2, state: 'queued' })], running: { subtitle_transcribe: 1 }, queued: { subtitle_transcribe: 1, subtitle_search: 40 } };
		expect(workingCount(o)).toEqual({ running: 1, queued: 41 });
		expect(waitingMore(o)).toBe(40);
	});
	it('maps known job phase tokens and passes through plain notes', () => {
		for (const p of ['waiting_slot', 'extracting', 'detecting', 'transcribing', 'preparing', 'translating', 'verifying']) {
			const word = jobPhaseWord(p);
			expect(word).not.toBe('');
			expect(word).not.toBe(p);
		}
		expect(jobPhaseWord('Custom step')).toBe('Custom step');
		expect(jobPhaseWord(undefined)).toBe('');
		expect(jobPhaseTone('transcribing')).toBe('ok');
		expect(jobPhaseTone('translating')).toBe('ok');
		expect(jobPhaseTone('waiting_slot')).toBe('wait');
		expect(jobPhaseTone('other')).toBe('idle');
	});
	it('formats speeds cleanly and ignores zero or negative values', () => {
		expect(formatSpeed(14.2, 'x')).toBe('14.2x');
		expect(formatSpeed(18.5, 'lines/s')).toBe('18.5 lines/s');
		expect(formatSpeed(18, 'lines/s')).toBe('18 lines/s');
		expect(formatSpeed(124.4, 'x')).toBe('124x');
		expect(formatSpeed(0, 'x')).toBe('');
		expect(formatSpeed(-1, 'x')).toBe('');
		expect(formatSpeed(undefined, 'x')).toBe('');
	});

	it('formats backlog ETA cleanly and ignores non-positive or non-finite values', () => {
		expect(formatBacklogEta(0)).toBe('');
		expect(formatBacklogEta(-12)).toBe('');
		expect(formatBacklogEta(null)).toBe('');
		expect(formatBacklogEta(undefined)).toBe('');
		expect(formatBacklogEta(Number.NaN)).toBe('');
		expect(formatBacklogEta(45)).toBe('~45s left');
		expect(formatBacklogEta(600)).toBe('~10m left');
		expect(formatBacklogEta(3 * 3600 + 120)).toBe('~3h 2m left');
		expect(formatBacklogEta(3 * 86400)).toBe('~3d 0h left');
	});
});

describe('stats and integrity helpers', () => {
	it('clamps and rounds library percent to 1 decimal', () => {
		expect(libraryPercent(undefined)).toBe(0);
		expect(libraryPercent({ wanted: 100, have: 67, missing: 33, percent: 67.34, files_total: 50, files_done: 33, files_missing: 17, unlooked_files: 0 })).toBe(67.3);
		expect(libraryPercent({ wanted: 10, have: 10, missing: 0, percent: 150, files_total: 10, files_done: 10, files_missing: 0, unlooked_files: 0 })).toBe(100);
		expect(libraryPercent({ wanted: 10, have: 0, missing: 10, percent: -5, files_total: 10, files_done: 0, files_missing: 10, unlooked_files: 0 })).toBe(0);
	});

	it('maps integrity issue codes and falls back cleanly', () => {
		for (const code of ['truncated', 'empty', 'unreadable', 'broken_timing', 'too_few_cues', 'longer_than_video']) {
			const label = integrityIssueWord(code);
			expect(label).not.toBe('');
			expect(label).not.toBe(code);
		}
		expect(integrityIssueWord('custom_issue')).toBe('custom issue');
	});

	it('formats millisecond timestamps as M:SS or H:MM:SS', () => {
		expect(formatTimestamp(undefined)).toBe('');
		expect(formatTimestamp(-10)).toBe('');
		expect(formatTimestamp(0)).toBe('0:00');
		expect(formatTimestamp(65_000)).toBe('1:05');
		expect(formatTimestamp(3_661_000)).toBe('1:01:01');
	});

	it('parses the subtitles tab query parameter', () => {
		expect(parseSubtitlesTab('waiting')).toBe('waiting');
		expect(parseSubtitlesTab('history')).toBe('history');
		expect(parseSubtitlesTab('failed')).toBe('failed');
		expect(parseSubtitlesTab('integrity')).toBe('integrity');
		expect(parseSubtitlesTab('settings')).toBe('settings');
		expect(parseSubtitlesTab('overview')).toBe('overview');
		expect(parseSubtitlesTab('other')).toBe('overview');
		expect(parseSubtitlesTab(null)).toBe('overview');
	});

	it('builds waiting and history queries cleanly', () => {
		expect(waitingQuery({ q: 'atlas', limit: 50, offset: 0 })).toBe('limit=50&q=atlas');
		expect(waitingQuery({ kind: 'subtitle_search', limit: 50, offset: 100 })).toBe('limit=50&offset=100&kind=subtitle_search');
		expect(historyQuery({ state: 'done', q: 'clock', limit: 50, offset: 0 })).toBe('state=done&q=clock&limit=50');
		expect(historyQuery({ state: 'skipped', limit: 50, offset: 50 })).toBe('state=skipped&limit=50&offset=50');
	});
});

describe('siteUse', () => {
	it('gives downloads of today against the cap, and none when there is no cap', () => {
		expect(siteUse({ downloads_today: 12, limits: { downloads_per_day: 100, gap_ms: 0, per_window: 0, window_seconds: 0 } })).toEqual({ used: 12, cap: 100, percent: 12 });
		expect(siteUse({ downloads_today: 150, limits: { downloads_per_day: 100, gap_ms: 0, per_window: 0, window_seconds: 0 } })?.percent).toBe(100);
		expect(siteUse({ downloads_today: 5, limits: { downloads_per_day: 0, gap_ms: 0, per_window: 0, window_seconds: 0 } })).toEqual({ used: 5, cap: 0, percent: 0 });
	});
});

describe('missingNeedQuery', () => {
	it('adds the need to the list query', () => {
		expect(missingNeedQuery({ need: 'speech', language: 'fr' }, 100, 0)).toBe('language=fr&limit=100&need=speech');
		expect(missingNeedQuery({ need: '' }, 50, 50)).toBe('limit=50&offset=50');
	});
});

describe('done', () => {
	it('has a word for each way a subtitle was made, and reads an unknown way as downloaded', () => {
		expect(new Set([howWord('downloaded'), howWord('translated'), howWord('speech')]).size).toBe(3);
		expect(howWord('something_new')).toBe(howWord('downloaded'));
	});
	it('leaves out the ways that made none', () => {
		expect(doneParts({ downloaded: 4, translated: 0, speech: 2 })).toEqual([{ how: 'downloaded', n: 4 }, { how: 'speech', n: 2 }]);
		expect(doneParts(undefined)).toEqual([]);
	});
});
