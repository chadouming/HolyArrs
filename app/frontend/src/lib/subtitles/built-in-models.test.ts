import { describe, expect, it } from 'vitest';
import { modelAction, modelBadges, sizeWords, translatorBody, translatorReady, type ModelView, type TranslatorForm } from './api.ts';

const view = (over: Partial<ModelView>): ModelView => ({ id: 'm', name: 'M', size: 1908528192, need: 3 * 2 ** 30, license: 'Apache-2.0', own: false, state: 'missing', progress: 0, fits: true, chosen: false, ...over });
const form = (over: Partial<TranslatorForm>): TranslatorForm => ({ kind: 'builtin', url: 'http://x/v1', model: 'm', has_key: true, built_in_auto: false, builtin_model: 'hy-mt2-1.8b-q8', threads: 0, gpu: '', builtin_available: true, key: '', clear_key: false, ...over });

describe('built-in translator models', () => {
	it('offers one action per state', () => {
		expect(modelAction(view({ state: 'missing' }))).toBe('download');
		expect(modelAction(view({ state: 'downloading', progress: 0.4 }))).toBe('cancel');
		expect(modelAction(view({ state: 'present' }))).toBe('use');
		expect(modelAction(view({ state: 'present', chosen: true }))).toBe('inUse');
		// A chosen model whose file is gone is offered for download again, not shown as in use.
		expect(modelAction(view({ state: 'missing', chosen: true }))).toBe('download');
	});

	it('says sizes in GB with one decimal, and small ones in MB', () => {
		expect(sizeWords(1908528192)).toBe('1.8 GB');
		expect(sizeWords(3 * 2 ** 30)).toBe('3.0 GB');
		expect(sizeWords(300 * 2 ** 20)).toBe('300 MB');
		expect(sizeWords(0)).toBe('0 MB');
	});

	it('sends only the built-in fields for the built-in kind', () => {
		expect(translatorBody(form({ threads: 8, gpu: 'Vulkan0' }))).toEqual({ kind: 'builtin', built_in_auto: false, builtin_model: 'hy-mt2-1.8b-q8', threads: 8, gpu: 'Vulkan0' });
		// The key box of another kind never travels with the built-in kind.
		expect(translatorBody(form({ key: 'secret', clear_key: true }))).not.toHaveProperty('key');
		expect(translatorBody(form({ key: 'secret', clear_key: true }))).not.toHaveProperty('clear_key');
		// An odd thread count is sent as the default.
		expect(translatorBody(form({ threads: Number.NaN })).threads).toBe(0);
	});

	it('keeps the other kinds as they were', () => {
		expect(translatorBody(form({ kind: 'openai' }))).toEqual({ kind: 'openai', url: 'http://x/v1', model: 'm', built_in_auto: false });
	});

	it('is ready with an address, or for the built-in kind with a model that is there', () => {
		const present = [view({ id: 'hy-mt2-1.8b-q8', state: 'present' })];
		expect(translatorReady(form({}), present)).toBe(true);
		expect(translatorReady(form({}), [view({ id: 'hy-mt2-1.8b-q8', state: 'missing' })])).toBe(false);
		expect(translatorReady(form({ builtin_available: false }), present)).toBe(false);
		expect(translatorReady(form({ kind: 'openai' }), [])).toBe(true);
		expect(translatorReady(form({ kind: 'openai', url: '' }), [])).toBe(false);
		expect(translatorReady(form({ kind: '' }), present)).toBe(false);
	});

	it('returns non-empty runtime badges in backend, quantization order', () => {
		expect(modelBadges(view({ backend: 'OpenVINO', quantization: 'INT8' }))).toEqual(['OpenVINO', 'INT8']);
		expect(modelBadges(view({ backend: 'OpenVINO', quantization: 'INT4' }))).toEqual(['OpenVINO', 'INT4']);
		expect(modelBadges(view({ backend: 'OpenVINO', quantization: 'FP16' }))).toEqual(['OpenVINO', 'FP16']);
		expect(modelBadges(view({ backend: 'GGUF', quantization: 'Q8_0' }))).toEqual(['GGUF', 'Q8_0']);
		expect(modelBadges(view({ backend: 'GGUF', quantization: 'Q4_0' }))).toEqual(['GGUF', 'Q4_0']);
		expect(modelBadges(view({ backend: 'GGML', quantization: 'Q5_0' }))).toEqual(['GGML', 'Q5_0']);
		expect(modelBadges(view({ backend: 'OpenVINO' }))).toEqual(['OpenVINO']);
		expect(modelBadges(view({ quantization: 'ONNX' }))).toEqual(['ONNX']);
		expect(modelBadges(view({ backend: '', quantization: '  ' }))).toEqual([]);
		expect(modelBadges(view({}))).toEqual([]);
	});
});
