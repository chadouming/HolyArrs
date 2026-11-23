import { describe, expect, it } from 'vitest';
import {
	cleanSkipTags,
	coveragePercent,
	createDefaultProfileDraft,
	DEFAULT_SKIP_TAGS,
	DEFAULT_WRITE_TAG,
	encodeGpuList,
	encoderChoicesForCodec,
	fileBaseName,
	folderAssignmentValue,
	formatBacklogEta,
	formatBytes,
	formatFps,
	formatRealtime,
	formatSavedDelta,
	gpuSummary,
	HW_PRESETS,
	itemsQuery,
	itemTitle,
	jobPercent,
	mergeRefreshedSettings,
	normalizeHwPreset,
	normalizeProfile,
	normalizeSettings,
	normalizeSettingsClone,
	parseFolderAssignmentValue,
	parseGpuList,
	parseTranscodeTab,
	phaseLabel,
	phaseTone,
	resolutionLabel,
	savingsPercent,
	skipReasonLabel,
	validateProfile,
	validateSettings,
	waitingQuery,
	workingCount
} from './helpers.ts';
import type { TranscodeSettings } from './types.ts';

const baseSettings: TranscodeSettings = {
	auto_transcode: false,
	concurrent_jobs: 2,
	preferred_encoder: 'auto',
	hw_preset: 'medium',
	gpu_devices: ['/dev/dri/renderD128'],
	selected_gpus: [],
	allow_cpu_fallback: true,
	keep_original_in_holding: false,
	copy_if_larger: false,
	skip_tags: [...DEFAULT_SKIP_TAGS],
	write_tag: DEFAULT_WRITE_TAG,
	normalize_1080p: false,
	pillarbox_4_3: true,
	hdr_mode: 'preserve',
	vmaf_enabled: true,
	vmaf_min_score: 93,
	vmaf_samples: 4,
	vmaf_sample_seconds: 20,
	vmaf_fps: 12,
	q_min: 15,
	q_max: 25,
	max_iterations: 5,
	svtav1_preset: 4,
	svtav1_crf: 24,
	max_bitrate_kbps: 10000,
	allowed_languages: ['eng', 'fra', 'jpn', 'und'],
	prefer_vfq: true,
	audio_bitrate_kbps: 192,
	audio_channels: 2,
	keep_anime_attachments: true,
	apply_edl_cuts: true,
	require_audio_stream: true,
	min_file_size_mb: 0,
	scratch_dir: ''
};

describe('transcode tab and title helpers', () => {
	it('parses the tab query parameter safely', () => {
		expect(parseTranscodeTab('overview')).toBe('overview');
		expect(parseTranscodeTab('waiting')).toBe('waiting');
		expect(parseTranscodeTab('history')).toBe('history');
		expect(parseTranscodeTab('failed')).toBe('failed');
		expect(parseTranscodeTab('settings')).toBe('settings');
		expect(parseTranscodeTab('unknown')).toBe('overview');
		expect(parseTranscodeTab(null)).toBe('overview');
	});

	it('formats movie and episode titles', () => {
		expect(itemTitle({ title: 'Quiet Harbour', season: 1, episode: 2 })).toBe('Quiet Harbour S01E02');
		expect(itemTitle({ title: 'The Lantern' })).toBe('The Lantern');
		expect(itemTitle({ season: 2, episode: 11 })).toBe('S02E11');
		expect(itemTitle({})).toBe('');
	});

	it('extracts base file names across slashes', () => {
		expect(fileBaseName('Season 1/Show.S01E01.mkv')).toBe('Show.S01E01.mkv');
		expect(fileBaseName('movie.mp4')).toBe('movie.mp4');
		expect(fileBaseName('')).toBe('');
	});
});

describe('formatting and metrics helpers', () => {
	it('formats byte values and signed savings deltas', () => {
		expect(formatBytes(0)).toBe('0 B');
		expect(formatBytes(1024)).toBe('1.0 KB');
		expect(formatBytes(-2048)).toBe('-2.0 KB');
		expect(formatSavedDelta(1000, 400)).toEqual({
			savedText: '600 B',
			percentText: '60%',
			positive: true,
			negative: false
		});
		expect(formatSavedDelta(1000, 1200, -200)).toEqual({
			savedText: '200 B',
			percentText: '20%',
			positive: false,
			negative: true
		});
		expect(formatSavedDelta(1000, 1000)).toEqual({
			savedText: '0 B',
			percentText: '0%',
			positive: false,
			negative: false
		});
	});

	it('maps phases to labels and tones', () => {
		for (const p of ['probing', 'edl', 'vmaf', 'encoding', 'replacing', 'scanning']) {
			const label = phaseLabel(p);
			expect(label).not.toBe('');
			expect(label).not.toBe(p);
		}
		expect(phaseLabel('custom_step')).toBe('custom step');
		expect(phaseTone('encoding')).toBe('ok');
		expect(phaseTone('vmaf')).toBe('wait');
		expect(phaseTone('other')).toBe('idle');
	});

	it('formats FPS, realtime speed, coverage, savings, and resolution', () => {
		expect(formatFps(124.4)).toBe('124 fps');
		expect(formatFps(42.36)).toBe('42.4 fps');
		expect(formatFps(0)).toBe('');
		expect(formatRealtime(2.345)).toBe('2.35x');
		expect(formatRealtime(-1)).toBe('');

		expect(coveragePercent({ coverage: { total: 10, completed: 5, percent: 50.26 } })).toBe(50.3);
		expect(coveragePercent({ coverage: { total: 10, completed: 10, percent: 140 } })).toBe(100);
		expect(
			savingsPercent({
				savings: { input_bytes: 10, output_bytes: 11, saved_bytes: -1, percent: -4, files_done: 1 }
			})
		).toBe(-4);
		expect(
			savingsPercent({
				savings: { input_bytes: 100, output_bytes: 112, saved_bytes: -12, percent: -12.36, files_done: 1 }
			})
		).toBe(-12.4);
		expect(
			savingsPercent({
				savings: { input_bytes: 10, output_bytes: 1, saved_bytes: 9, percent: 150, files_done: 1 }
			})
		).toBe(100);
		expect(
			savingsPercent({
				savings: { input_bytes: 10, output_bytes: 10, saved_bytes: 0, percent: -0.04, files_done: 1 }
			})
		).toBe(0);

		expect(resolutionLabel(3840, 2160)).toBe('2160p');
		expect(resolutionLabel(1920, 1080)).toBe('1080p');
		expect(resolutionLabel(1280, 720)).toBe('720p');
		expect(resolutionLabel(0, 0)).toBe('');
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

	it('counts active jobs, progress share, and GPU summary', () => {
		expect(
			workingCount({ running: { transcode_file: 2 }, queued: { transcode_file: 3, transcode_scan: 1 } })
		).toEqual({
			running: 2,
			queued: 4
		});
		expect(jobPercent({ state: 'running', progress: 0.426 })).toBe(43);
		expect(jobPercent({ state: 'queued', progress: 0.5 })).toBeNull();
		expect(
			gpuSummary([
				{
					path: '/dev/dri/renderD128',
					vendor: 'intel',
					name: 'Arc A380',
					pci_slot: '00:02.0',
					discrete: true,
					busy_jobs: 1,
					selected: true
				},
				{
					path: '/dev/dri/renderD129',
					vendor: 'intel',
					name: 'UHD 770',
					pci_slot: '00:02.1',
					discrete: false,
					busy_jobs: 0,
					selected: false
				}
			])
		).toEqual({ total: 1, busy: 1, names: 'Arc A380' });
		expect(
			gpuSummary([
				{
					path: '/dev/dri/renderD128',
					vendor: 'intel',
					name: 'Arc A380',
					pci_slot: '00:02.0',
					discrete: true,
					busy_jobs: 1,
					selected: false
				},
				{
					path: '/dev/dri/renderD129',
					vendor: 'intel',
					name: 'UHD 770',
					pci_slot: '00:02.1',
					discrete: false,
					busy_jobs: 0
				}
			])
		).toEqual({ total: 2, busy: 1, names: 'Arc A380, UHD 770' });
	});

	it('builds items and waiting query parameters cleanly', () => {
		expect(itemsQuery({ state: 'pending', q: 'dune', limit: 50, offset: 0 })).toBe(
			'state=pending&q=dune&limit=50'
		);
		expect(itemsQuery({ state: 'done', codec: 'hevc', limit: 50, offset: 50 })).toBe(
			'state=done&codec=hevc&limit=50&offset=50'
		);
		expect(waitingQuery({ q: 'blade', limit: 50, offset: 0 })).toBe('limit=50&q=blade');
		expect(waitingQuery({ limit: 50, offset: 100 })).toBe('limit=50&offset=100');
	});

	it('maps known skip reason tokens to localized labels', () => {
		for (const reason of [
			'user_skipped',
			'fileflows_tagged',
			'tag_matched',
			'already_av1',
			'already_hevc',
			'already_h264',
			'below_min_size',
			'library_disabled',
			'no_video'
		]) {
			const label = skipReasonLabel(reason);
			expect(label).not.toBe('');
			expect(label).not.toContain('_');
		}
		expect(skipReasonLabel('custom_skip_token')).toBe('custom skip token');
		expect(skipReasonLabel('')).toBe('');
		expect(skipReasonLabel(null)).toBe('');
	});
});

describe('settings and profile helpers', () => {
	it('parses and encodes gpu_devices preserving array or string shape', () => {
		expect(parseGpuList(['/dev/dri/renderD128', ' /dev/dri/renderD129 '])).toEqual([
			'/dev/dri/renderD128',
			'/dev/dri/renderD129'
		]);
		expect(parseGpuList('/dev/dri/renderD128, /dev/dri/renderD129')).toEqual([
			'/dev/dri/renderD128',
			'/dev/dri/renderD129'
		]);
		expect(encodeGpuList(['/dev/dri/renderD128'], true)).toEqual(['/dev/dri/renderD128']);
		expect(encodeGpuList(['/dev/dri/renderD128', '/dev/dri/renderD129'], false)).toBe(
			'/dev/dri/renderD128,/dev/dri/renderD129'
		);
	});

	it('validates and normalizes TranscodeSettings', () => {
		expect(validateSettings(baseSettings)).toBe('');
		expect(validateSettings({ ...baseSettings, concurrent_jobs: 0 })).not.toBe('');
		expect(validateSettings({ ...baseSettings, q_min: 0 })).not.toBe('');
		expect(validateSettings({ ...baseSettings, q_max: 64 })).not.toBe('');
		expect(validateSettings({ ...baseSettings, q_min: 40, q_max: 20 })).not.toBe('');
		expect(validateSettings({ ...baseSettings, vmaf_min_score: 49 })).not.toBe('');
		expect(validateSettings({ ...baseSettings, vmaf_min_score: 105 })).not.toBe('');
		expect(validateSettings({ ...baseSettings, svtav1_preset: 14 })).not.toBe('');
		expect(validateSettings({ ...baseSettings, svtav1_crf: 64 })).not.toBe('');
		expect(validateSettings({ ...baseSettings, max_bitrate_kbps: 99 })).not.toBe('');
		expect(validateSettings({ ...baseSettings, max_bitrate_kbps: 100001 })).not.toBe('');
		expect(validateSettings({ ...baseSettings, audio_bitrate_kbps: 31 })).not.toBe('');
		expect(validateSettings({ ...baseSettings, audio_bitrate_kbps: 1537 })).not.toBe('');
		expect(validateSettings({ ...baseSettings, audio_channels: 0 })).not.toBe('');
		expect(validateSettings({ ...baseSettings, audio_channels: 9 })).not.toBe('');

		const normalized = normalizeSettings({
			...baseSettings,
			hw_preset: '  Fast ',
			concurrent_jobs: '3' as unknown as number,
			selected_gpus: [' /dev/dri/renderD128 ', ''],
			scratch_dir: '  /tmp/tc  ',
			allowed_languages: [' eng ', ''],
			skip_tags: [' HolyArrs ', 'holyarrs', ' Custom Tag ', ''],
			write_tag: '  MyTag  '
		});
		expect(normalized.hw_preset).toBe('fast');
		expect(normalizeSettings({ ...baseSettings, hw_preset: 'invalid' }).hw_preset).toBe('medium');
		expect(normalizeSettings({ ...baseSettings, hw_preset: '' }).hw_preset).toBe('medium');
		expect(normalized.concurrent_jobs).toBe(3);
		expect(normalized.selected_gpus).toEqual(['/dev/dri/renderD128']);
		expect(normalized.scratch_dir).toBe('/tmp/tc');
		expect(normalized.allowed_languages).toEqual(['eng']);
		expect(normalized.skip_tags).toEqual(['HolyArrs', 'Custom Tag']);
		expect(normalized.write_tag).toBe('MyTag');
		expect(normalizeSettings({ ...baseSettings, skip_tags: [], write_tag: '' })).toMatchObject({
			skip_tags: [],
			write_tag: ''
		});
		expect(
			normalizeSettings({
				...baseSettings,
				skip_tags: undefined as unknown as string[],
				write_tag: undefined as unknown as string
			})
		).toMatchObject({
			skip_tags: [...DEFAULT_SKIP_TAGS],
			write_tag: DEFAULT_WRITE_TAG
		});
	});

	it('creates, normalizes, and validates TranscodeProfile drafts', () => {
		const draft = createDefaultProfileDraft(undefined, 'Anime 10-bit');
		expect(draft.name).toBe('Anime 10-bit');
		expect(draft.is_default).toBe(false);
		expect(draft.hw_preset).toBe('medium');
		expect(draft).toMatchObject({
			pillarbox_4_3: true,
			vmaf_min_score: 93,
			vmaf_samples: 4,
			vmaf_sample_seconds: 20,
			vmaf_fps: 12,
			q_min: 15,
			q_max: 25,
			max_iterations: 5,
			cpu_preset: 4,
			cpu_crf: 24,
			max_bitrate_kbps: 10000,
			copy_if_larger: false,
			skip_tags: [...DEFAULT_SKIP_TAGS],
			write_tag: DEFAULT_WRITE_TAG,
			keep_original_in_holding: false,
			allowed_languages: ['eng', 'fra', 'jpn', 'und'],
			prefer_vfq: true,
			audio_codec: 'aac',
			audio_bitrate_kbps: 192,
			audio_channels: 2,
			keep_anime_attachments: true,
			apply_edl_cuts: true,
			require_audio_stream: true,
			min_file_size_mb: 0
		});
		expect(validateProfile(draft)).toBe('');

		expect(validateProfile({ ...draft, name: '   ' })).not.toBe('');
		expect(validateProfile({ ...draft, q_min: 0 })).not.toBe('');
		expect(validateProfile({ ...draft, q_max: 64 })).not.toBe('');
		expect(validateProfile({ ...draft, q_min: 40, q_max: 20 })).not.toBe('');
		expect(validateProfile({ ...draft, vmaf_min_score: 45 })).not.toBe('');
		expect(validateProfile({ ...draft, vmaf_min_score: 120 })).not.toBe('');
		expect(validateProfile({ ...draft, cpu_preset: 20 })).not.toBe('');
		expect(validateProfile({ ...draft, cpu_crf: 70 })).not.toBe('');
		expect(validateProfile({ ...draft, max_bitrate_kbps: 50 })).not.toBe('');
		expect(validateProfile({ ...draft, max_bitrate_kbps: 100001 })).not.toBe('');
		expect(validateProfile({ ...draft, audio_bitrate_kbps: 16 })).not.toBe('');
		expect(validateProfile({ ...draft, audio_bitrate_kbps: 2000 })).not.toBe('');
		expect(validateProfile({ ...draft, audio_channels: 0 })).not.toBe('');
		expect(validateProfile({ ...draft, audio_channels: 9 })).not.toBe('');

		const norm = normalizeProfile({
			...draft,
			name: '  4K Archive  ',
			hw_preset: 'SLOWER',
			max_height: 2160,
			normalize_1080p: true,
			allowed_languages: [' jpn ', '']
		});
		expect(norm.name).toBe('4K Archive');
		expect(norm.hw_preset).toBe('slower');
		expect(norm.normalize_1080p).toBe(false);
		expect(norm.allowed_languages).toEqual(['jpn']);
		expect(normalizeProfile({ ...draft, hw_preset: 'unknown' }).hw_preset).toBe('medium');
		expect(normalizeProfile({ ...draft, skip_tags: [], write_tag: '' })).toMatchObject({
			skip_tags: [],
			write_tag: ''
		});
		expect(createDefaultProfileDraft({ hw_preset: 'slow' }).hw_preset).toBe('slow');
		expect(createDefaultProfileDraft({ hw_preset: '' }).hw_preset).toBe('medium');
		expect(createDefaultProfileDraft({ skip_tags: [], write_tag: '' })).toMatchObject({
			skip_tags: [],
			write_tag: ''
		});
		expect(cleanSkipTags([' HolyArrs ', 'holyarrs', '', 'Created by FileFlows', 'created by fileflows'])).toEqual([
			'HolyArrs',
			'Created by FileFlows'
		]);
		expect(HW_PRESETS).toEqual(['veryfast', 'faster', 'fast', 'medium', 'slow', 'slower', 'veryslow']);
		expect(normalizeHwPreset(undefined)).toBe('medium');
		expect(normalizeHwPreset(' VERYFAST ')).toBe('veryfast');
	});

	it('lists encoder choices per codec including vulkan and detected options', () => {
		const av1 = encoderChoicesForCodec('av1', ['av1_qsv', 'hevc_vaapi']);
		expect(av1).toContain('auto');
		expect(av1).toContain('av1_vulkan');
		expect(av1).toContain('libsvtav1');
		expect(av1).not.toContain('hevc_vaapi');

		const hevc = encoderChoicesForCodec('hevc', ['hevc_nvenc', 'av1_qsv']);
		expect(hevc).toContain('hevc_vulkan');
		expect(hevc).toContain('libx265');
		expect(hevc).not.toContain('av1_qsv');
	});

	it('encodes and parses library folder assignment select values', () => {
		expect(folderAssignmentValue({ enabled: false, profile_id: null })).toBe('disabled');
		expect(folderAssignmentValue({ enabled: true, profile_id: null })).toBe('default');
		expect(folderAssignmentValue({ enabled: true, profile_id: 7 })).toBe('7');

		expect(parseFolderAssignmentValue('disabled')).toEqual({ enabled: false, profile_id: null });
		expect(parseFolderAssignmentValue('default')).toEqual({ enabled: true, profile_id: null });
		expect(parseFolderAssignmentValue('7')).toEqual({ enabled: true, profile_id: 7 });
	});

	it('merges refreshed server settings onto dirty local edits without overwriting them', () => {
		const { clone: prevSaved } = normalizeSettingsClone(baseSettings);
		const current: TranscodeSettings = {
			...prevSaved,
			concurrent_jobs: 4,
			skip_tags: [...prevSaved.skip_tags],
			selected_gpus: [...(prevSaved.selected_gpus ?? [])],
			allowed_languages: [...prevSaved.allowed_languages],
			gpu_devices: [...(prevSaved.gpu_devices as string[])]
		};
		const freshFromServer: TranscodeSettings = {
			...baseSettings,
			vmaf_min_score: 95,
			keep_original_in_holding: true,
			hw_preset: 'FAST'
		};

		const merged = mergeRefreshedSettings(
			current,
			prevSaved,
			freshFromServer,
			'/dev/dri/renderD128'
		);

		expect(merged.settings.concurrent_jobs).toBe(4);
		expect(merged.settings.vmaf_min_score).toBe(95);
		expect(merged.settings.keep_original_in_holding).toBe(true);
		expect(merged.settings.hw_preset).toBe('fast');
		expect(JSON.stringify(merged.settings)).not.toBe(merged.savedJson);

		const savedObj = JSON.parse(merged.savedJson) as TranscodeSettings;
		expect(savedObj.concurrent_jobs).toBe(2);
		expect(savedObj.vmaf_min_score).toBe(95);
		expect(savedObj.hw_preset).toBe('fast');
	});
});
