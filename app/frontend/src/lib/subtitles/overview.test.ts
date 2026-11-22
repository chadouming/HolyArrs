import { describe, expect, it } from 'vitest';
import { doneParts, howWord, jobName, jobShare, jobWord, missingNeedQuery, nextStep, siteUse, speechState, waitingMore, workingCount, type Overview, type OverviewJob } from './overview.ts';

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
		expect(jobName(job({ kind: 'subtitle_model_download', model: 'Whisper' }))).toBe('Whisper');
		expect(jobName(job({ kind: 'subtitle_missing' }))).toBe('');
	});
	it('has a word for every kind it follows and a plain one for any other', () => {
		for (const k of ['subtitle_transcribe', 'subtitle_translate', 'subtitle_search', 'subtitle_missing', 'subtitle_better', 'subtitle_look', 'subtitle_model_download'])
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
