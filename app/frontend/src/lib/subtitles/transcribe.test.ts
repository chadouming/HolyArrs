import { describe, expect, it } from 'vitest';
import { canTranscribe, madeLabel, madeOf, termsBody, translatorCanTranslate, type SubtitleRow, type Transcriber } from './api.ts';

const row = (over: Partial<SubtitleRow>): SubtitleRow => ({ ID: 1, file_id: 1, language: 'en', forced: false, hearing_impaired: false, place: 'beside', score_parts: [], origin: 'found', state: 'present', tries: 0, sync: {}, ...over });
const ready: Transcriber = { available: true, ready: true, model: 'Whisper large-v3-turbo' };

describe('subtitles from the audio', () => {
	it('is offered only for a file with no text subtitle, when the transcriber is ready', () => {
		expect(canTranscribe([], ready)).toBe(true);
		expect(canTranscribe([row({ place: 'wish', state: 'wanted' })], ready)).toBe(true);
		// A picture track is not text: nothing can be read from it.
		expect(canTranscribe([row({ place: 'built_in', picture: true, codec: 'S_HDMV/PGS' })], ready)).toBe(true);
		expect(canTranscribe([row({ place: 'built_in', codec: 'S_TEXT/UTF8' })], ready)).toBe(false);
		expect(canTranscribe([row({ relative_path: 'a.en.srt' })], ready)).toBe(false);
		// A subtitle that was replaced or is gone no longer counts.
		expect(canTranscribe([row({ relative_path: 'a.en.srt', state: 'replaced' })], ready)).toBe(true);
		expect(canTranscribe([], { ...ready, ready: false })).toBe(false);
		expect(canTranscribe([], null)).toBe(false);
	});

	it('labels a transcript as transcribed, whatever else the row says made it', () => {
		expect(madeOf(row({ made: 'ai_translated', transcribed: true }))).toBe('transcribed');
		expect(madeOf(row({ made: 'ai_translated' }))).toBe('ai_translated');
		expect(madeOf(row({}))).toBeUndefined();
		expect(madeLabel('transcribed')).not.toBe('');
		expect(madeLabel('transcribed')).not.toBe(madeLabel('ai_translated'));
	});

	it('turns the terms box into a clean list', () => {
		expect(termsBody('en', '  Wind Blade Technique \n\nAoi Kisaragi\nwind blade technique\n', ['Mirelle'])).toEqual({ language: 'en', own: ['Wind Blade Technique', 'Aoi Kisaragi'], removed: ['Mirelle'] });
		const many = Array.from({ length: 300 }, (_, i) => `Term ${i}`).join('\n');
		expect(termsBody('ja', many, []).own).toHaveLength(200);
	});

	it('knows the built-in translator can translate without an address', () => {
		expect(translatorCanTranslate({ kind: 'builtin', url: '', model: '', has_key: false, built_in_auto: false, builtin_available: true, builtin_model: 'hy-mt2-1.8b-q8' })).toBe(true);
		expect(translatorCanTranslate({ kind: 'builtin', url: '', model: '', has_key: false, built_in_auto: false, builtin_available: false, builtin_model: 'x' })).toBe(false);
		expect(translatorCanTranslate({ kind: 'openai', url: 'http://x/v1', model: 'm', has_key: false, built_in_auto: false })).toBe(true);
		expect(translatorCanTranslate({ kind: 'openai', url: '', model: 'm', has_key: false, built_in_auto: false })).toBe(false);
		expect(translatorCanTranslate({ kind: '', url: 'http://x', model: '', has_key: false, built_in_auto: false })).toBe(false);
	});
});
