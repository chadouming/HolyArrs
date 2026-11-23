// Pure formatting, query, status, profile, and validation helpers for Transcode.
import { episodeCode } from '#lib/core/episodes.ts';
import { bytes, duration } from '#lib/core/format.ts';
import { t } from '#lib/core/i18n/i18n.svelte.ts';
import type {
	GPUDevice,
	LibraryAssignment,
	Overview,
	OverviewJob,
	TranscodeProfile,
	TranscodeSettings,
	TranscodeTab
} from './types.ts';

const VALID_TABS = new Set<TranscodeTab>(['overview', 'waiting', 'history', 'failed', 'settings']);
const KNOWN_PHASES = new Set(['probing', 'edl', 'vmaf', 'encoding', 'replacing', 'scanning']);
const KNOWN_SKIP_REASONS = new Set([
	'user_skipped',
	'fileflows_tagged',
	'tag_matched',
	'already_av1',
	'already_hevc',
	'already_h264',
	'below_min_size',
	'library_disabled',
	'no_video'
]);

const CODEC_ENCODERS: Record<TranscodeProfile['target_codec'], string[]> = {
	av1: ['av1_qsv', 'av1_vaapi', 'av1_nvenc', 'av1_vulkan', 'libsvtav1'],
	hevc: ['hevc_qsv', 'hevc_vaapi', 'hevc_nvenc', 'hevc_vulkan', 'libx265'],
	h264: ['h264_qsv', 'h264_vaapi', 'h264_nvenc', 'h264_vulkan', 'libx264']
};

export const HW_PRESETS = ['veryfast', 'faster', 'fast', 'medium', 'slow', 'slower', 'veryslow'] as const;
const HW_PRESET_SET = new Set<string>(HW_PRESETS);

export const DEFAULT_SKIP_TAGS = ['HolyArrs', 'Created by FileFlows'] as const;
export const DEFAULT_WRITE_TAG = 'HolyArrs';

/** Trims skip tags, drops empty items, and deduplicates case-insensitively while preserving first-seen case. */
export function cleanSkipTags(tags: readonly string[]): string[] {
	const seen = new Set<string>();
	const out: string[] = [];
	for (const raw of tags) {
		const clean = raw.trim();
		if (!clean) continue;
		const lower = clean.toLowerCase();
		if (seen.has(lower)) continue;
		seen.add(lower);
		out.push(clean);
	}
	return out;
}

/** Normalizes a hardware encoder preset string to a supported value (default 'medium'). */
export function normalizeHwPreset(raw: string | undefined | null): (typeof HW_PRESETS)[number] {
	const clean = (raw ?? '').trim().toLowerCase();
	return HW_PRESET_SET.has(clean) ? (clean as (typeof HW_PRESETS)[number]) : 'medium';
}

/** Normalizes the ?tab= query parameter to a valid Transcode page tab. */
export function parseTranscodeTab(raw: string | null): TranscodeTab {
	if (raw && VALID_TABS.has(raw as TranscodeTab)) return raw as TranscodeTab;
	return 'overview';
}

/** Formats byte counts into human-readable units, handling signed deltas cleanly. */
export function formatBytes(n: number | null | undefined): string {
	if (n === null || n === undefined || !Number.isFinite(n) || n === 0) return '0 B';
	if (n < 0) return `-${bytes(-n)}`;
	return bytes(n);
}

/** Summarises before and after sizes along with percent saved or lost. */
export function formatSavedDelta(
	inputBytes: number,
	outputBytes: number,
	savedBytes?: number
): { savedText: string; percentText: string; positive: boolean; negative: boolean } {
	const saved = savedBytes !== undefined ? savedBytes : inputBytes - outputBytes;
	const pct = inputBytes > 0 ? Math.round((saved / inputBytes) * 1000) / 10 : 0;
	const positive = saved > 0;
	const negative = saved < 0;
	const savedText = positive ? formatBytes(saved) : negative ? formatBytes(-saved) : '0 B';
	const percentText = `${Math.abs(pct)}%`;
	return { savedText, percentText, positive, negative };
}

/** Formats a title with optional episode code (e.g. S01E02). */
export function itemTitle(item: {
	title?: string | null;
	media_kind?: string | null;
	season?: number | null;
	episode?: number | null;
}): string {
	const name = item.title?.trim() ?? '';
	if (item.season != null && item.episode != null) {
		const code = episodeCode(item.season, item.episode);
		return name ? `${name} ${code}` : code;
	}
	return name;
}

/** Extracts the file basename from a relative path. */
export function fileBaseName(path: string | undefined | null): string {
	if (!path) return '';
	const parts = path.split(/[\\/]/);
	return parts[parts.length - 1] || path;
}

/** Maps a backend phase token to a localized label. */
export function phaseLabel(phase: string | undefined | null): string {
	if (!phase) return '';
	const clean = phase.trim().toLowerCase();
	if (KNOWN_PHASES.has(clean)) return t(`transcode.phase.${clean}`);
	return phase.replaceAll('_', ' ');
}

/** Maps a backend skip reason token to a localized label. */
export function skipReasonLabel(reason: string | undefined | null): string {
	if (!reason) return '';
	const clean = reason.trim().toLowerCase();
	if (!clean) return '';
	if (KNOWN_SKIP_REASONS.has(clean)) return t(`transcode.skipReason.${clean}`);
	return reason.replaceAll('_', ' ');
}

/** Maps a backend job phase to a visual pill tone. */
export function phaseTone(phase: string | undefined | null): 'ok' | 'wait' | 'idle' {
	switch ((phase ?? '').trim().toLowerCase()) {
		case 'encoding':
		case 'replacing':
			return 'ok';
		case 'probing':
		case 'edl':
		case 'vmaf':
		case 'scanning':
			return 'wait';
		default:
			return 'idle';
	}
}

const sumMap = (m: Record<string, number> | undefined) =>
	m ? Object.values(m).reduce((acc, n) => acc + (Number.isFinite(n) ? n : 0), 0) : 0;

/** Total running and queued transcode jobs from the overview maps. */
export function workingCount(o: Pick<Overview, 'running' | 'queued'>): { running: number; queued: number } {
	return { running: sumMap(o.running), queued: sumMap(o.queued) };
}

/** Job progress percentage (0..100), or null when queued. */
export function jobPercent(j: Pick<OverviewJob, 'state' | 'progress'>): number | null {
	if (j.state !== 'running') return null;
	const p = Number.isFinite(j.progress) ? j.progress : 0;
	return Math.round(Math.max(0, Math.min(1, p)) * 100);
}

/** Formats an FPS metric cleanly or returns an empty string when inactive. */
export function formatFps(fps: number | undefined | null): string {
	if (!fps || !Number.isFinite(fps) || fps <= 0) return '';
	return `${fps >= 100 ? Math.round(fps) : Number(fps.toFixed(1))} fps`;
}

/** Formats a realtime speed multiplier cleanly (e.g. 2.4x). */
export function formatRealtime(speed: number | undefined | null): string {
	if (!speed || !Number.isFinite(speed) || speed <= 0) return '';
	return `${speed >= 100 ? Math.round(speed) : Number(speed.toFixed(2))}x`;
}

/** Clamped library transcode coverage percentage rounded to one decimal place. */
export function coveragePercent(o: Pick<Overview, 'coverage'>): number {
	const raw = o.coverage?.percent;
	if (!Number.isFinite(raw)) return 0;
	return Math.round(Math.max(0, Math.min(100, raw)) * 10) / 10;
}

/** Formats an overall backlog ETA in seconds, or returns '' when absent or non-positive. */
export function formatBacklogEta(seconds: number | null | undefined): string {
	if (!seconds || !Number.isFinite(seconds) || seconds <= 0) return '';
	const d = duration(Math.round(seconds));
	if (!d) return '';
	return t('downloads.list.left', { time: `~${d}` });
}

/** Space savings percentage capped at 100 and rounded to one decimal place. */
export function savingsPercent(o: Pick<Overview, 'savings'>): number {
	const raw = o.savings?.percent;
	if (!Number.isFinite(raw)) return 0;
	const rounded = Math.round(Math.min(100, raw) * 10) / 10;
	return rounded === 0 ? 0 : rounded;
}

/** Formats resolution dimensions into standard labels like 2160p or 1080p. */
export function resolutionLabel(width: number | undefined, height: number | undefined): string {
	const w = width ?? 0;
	const h = height ?? 0;
	if (!w && !h) return '';
	if (h >= 2160 || w >= 3840) return '2160p';
	if (h >= 1080 || w >= 1920) return '1080p';
	if (h >= 720 || w >= 1280) return '720p';
	if (h >= 480 || w >= 720) return '480p';
	return h > 0 ? `${h}p` : `${w}x${h}`;
}

/** Summarises detected GPUs and busy job counts, preferring selected GPUs when marked. */
export function gpuSummary(gpus: GPUDevice[] | undefined): { total: number; busy: number; names: string } {
	const all = gpus ?? [];
	const selected = all.filter((g) => g.selected === true);
	const list = selected.length > 0 ? selected : all;
	const busy = list.reduce((acc, g) => acc + (g.busy_jobs > 0 ? g.busy_jobs : 0), 0);
	const names = list.map((g) => g.name || g.path).filter(Boolean).join(', ');
	return { total: list.length, busy, names };
}

/** Builds the query string for GET /transcode/items. */
export function itemsQuery(params: {
	state: string;
	q?: string;
	codec?: string;
	limit: number;
	offset: number;
}): string {
	const sp = new URLSearchParams();
	sp.set('state', params.state || 'pending');
	if (params.q?.trim()) sp.set('q', params.q.trim());
	if (params.codec?.trim()) sp.set('codec', params.codec.trim());
	sp.set('limit', String(params.limit));
	if (params.offset > 0) sp.set('offset', String(params.offset));
	return sp.toString();
}

/** Builds the query string for GET /transcode/waiting. */
export function waitingQuery(params: { q?: string; limit: number; offset: number }): string {
	const sp = new URLSearchParams();
	sp.set('limit', String(params.limit));
	if (params.offset > 0) sp.set('offset', String(params.offset));
	if (params.q?.trim()) sp.set('q', params.q.trim());
	return sp.toString();
}

/** Parses gpu_devices into a list of device paths regardless of array or string storage. */
export function parseGpuList(raw: string[] | string | undefined | null): string[] {
	if (Array.isArray(raw)) return raw.map((s) => s.trim()).filter(Boolean);
	if (typeof raw === 'string') return raw.split(',').map((s) => s.trim()).filter(Boolean);
	return [];
}

/** Encodes a GPU device list back to the original shape expected by the backend. */
export function encodeGpuList(list: string[], wasArray: boolean): string[] | string {
	const clean = list.map((s) => s.trim()).filter(Boolean);
	return wasArray ? clean : clean.join(',');
}

/** Normalizes numeric fields on TranscodeSettings before PUT. */
export function normalizeSettings(s: TranscodeSettings): TranscodeSettings {
	return {
		...s,
		hw_preset: normalizeHwPreset(s.hw_preset),
		selected_gpus: (s.selected_gpus ?? []).map((x) => x.trim()).filter(Boolean),
		concurrent_jobs: Math.max(1, Math.round(Number(s.concurrent_jobs) || 1)),
		vmaf_min_score: Number(s.vmaf_min_score) || 0,
		vmaf_samples: Math.max(1, Math.round(Number(s.vmaf_samples) || 1)),
		vmaf_sample_seconds: Math.max(1, Number(s.vmaf_sample_seconds) || 1),
		vmaf_fps: Math.max(1, Number(s.vmaf_fps) || 1),
		q_min: Math.round(Number(s.q_min) || 0),
		q_max: Math.round(Number(s.q_max) || 0),
		max_iterations: Math.max(1, Math.round(Number(s.max_iterations) || 1)),
		svtav1_preset: Math.round(Number(s.svtav1_preset) || 0),
		svtav1_crf: Math.round(Number(s.svtav1_crf) || 0),
		max_bitrate_kbps: Math.max(0, Math.round(Number(s.max_bitrate_kbps) || 0)),
		audio_bitrate_kbps: Math.max(0, Math.round(Number(s.audio_bitrate_kbps) || 0)),
		audio_channels: Math.max(0, Math.round(Number(s.audio_channels) || 0)),
		min_file_size_mb: Math.max(0, Number(s.min_file_size_mb) || 0),
		allowed_languages: (s.allowed_languages ?? []).map((x) => x.trim()).filter(Boolean),
		skip_tags: Array.isArray(s.skip_tags) ? cleanSkipTags(s.skip_tags) : [...DEFAULT_SKIP_TAGS],
		write_tag: typeof s.write_tag === 'string' ? s.write_tag.trim() : DEFAULT_WRITE_TAG,
		scratch_dir: (s.scratch_dir ?? '').trim()
	};
}

function validateQualityAndAudio(fields: {
	q_min: number;
	q_max: number;
	vmaf_min_score: number;
	preset: number;
	crf: number;
	max_bitrate_kbps: number;
	audio_bitrate_kbps: number;
	audio_channels: number;
}): string {
	const qMin = Number(fields.q_min);
	const qMax = Number(fields.q_max);
	if (!Number.isFinite(qMin) || !Number.isFinite(qMax) || qMin < 1 || qMax > 63 || qMin > qMax) {
		return t('transcode.settings.errQRange');
	}
	const vmaf = Number(fields.vmaf_min_score);
	if (!Number.isFinite(vmaf) || vmaf < 50 || vmaf > 100) return t('transcode.settings.errVmaf');
	const preset = Number(fields.preset);
	if (!Number.isFinite(preset) || preset < 0 || preset > 13) return t('transcode.settings.errPreset');
	const crf = Number(fields.crf);
	if (!Number.isFinite(crf) || crf < 0 || crf > 63) return t('transcode.settings.errCrf');
	const maxBr = Number(fields.max_bitrate_kbps);
	if (!Number.isFinite(maxBr) || maxBr < 100 || maxBr > 100000) {
		return 'Max bitrate must be between 100 and 100000 kbps.';
	}
	const audioBr = Number(fields.audio_bitrate_kbps);
	if (!Number.isFinite(audioBr) || audioBr < 32 || audioBr > 1536) {
		return 'Audio bitrate must be between 32 and 1536 kbps.';
	}
	const audioCh = Number(fields.audio_channels);
	if (!Number.isFinite(audioCh) || audioCh < 1 || audioCh > 8) {
		return 'Audio channels must be between 1 and 8.';
	}
	return '';
}

/** Validates TranscodeSettings and returns a localized error message or empty string. */
export function validateSettings(s: TranscodeSettings): string {
	const jobs = Number(s.concurrent_jobs);
	if (!Number.isFinite(jobs) || jobs < 1) return t('transcode.settings.errConcurrent');
	return validateQualityAndAudio({
		q_min: s.q_min,
		q_max: s.q_max,
		vmaf_min_score: s.vmaf_min_score,
		preset: s.svtav1_preset,
		crf: s.svtav1_crf,
		max_bitrate_kbps: s.max_bitrate_kbps,
		audio_bitrate_kbps: s.audio_bitrate_kbps,
		audio_channels: s.audio_channels
	});
}

/** Builds a clean profile draft from defaults or an existing profile. */
export function createDefaultProfileDraft(
	source?: Partial<TranscodeProfile>,
	nameOverride?: string
): Omit<TranscodeProfile, 'id'> {
	return {
		name: nameOverride ?? source?.name ?? '',
		description: source?.description ?? '',
		is_default: false,
		target_codec: source?.target_codec ?? 'av1',
		preferred_encoder: source?.preferred_encoder ?? 'auto',
		hw_preset: normalizeHwPreset(source?.hw_preset),
		ten_bit: source?.ten_bit ?? true,
		normalize_1080p: source?.normalize_1080p ?? true,
		max_height: source?.max_height ?? 1080,
		scale_mode: source?.scale_mode ?? 'normalize',
		pillarbox_4_3: source?.pillarbox_4_3 ?? true,
		hdr_mode: source?.hdr_mode ?? 'preserve',
		vmaf_enabled: source?.vmaf_enabled ?? true,
		vmaf_min_score: source?.vmaf_min_score ?? 93,
		vmaf_samples: source?.vmaf_samples ?? 4,
		vmaf_sample_seconds: source?.vmaf_sample_seconds ?? 20,
		vmaf_fps: source?.vmaf_fps ?? 12,
		q_min: source?.q_min ?? 15,
		q_max: source?.q_max ?? 25,
		max_iterations: source?.max_iterations ?? 5,
		cpu_preset: source?.cpu_preset ?? 4,
		cpu_crf: source?.cpu_crf ?? 24,
		max_bitrate_kbps: source?.max_bitrate_kbps ?? 10000,
		copy_if_larger: source?.copy_if_larger ?? false,
		skip_tags: Array.isArray(source?.skip_tags) ? cleanSkipTags(source.skip_tags) : [...DEFAULT_SKIP_TAGS],
		write_tag: typeof source?.write_tag === 'string' ? source.write_tag.trim() : DEFAULT_WRITE_TAG,
		keep_original_in_holding: source?.keep_original_in_holding ?? false,
		allowed_languages: [...(source?.allowed_languages ?? ['eng', 'fra', 'jpn', 'und'])],
		prefer_vfq: source?.prefer_vfq ?? true,
		audio_codec: source?.audio_codec ?? 'aac',
		audio_bitrate_kbps: source?.audio_bitrate_kbps ?? 192,
		audio_channels: source?.audio_channels ?? 2,
		keep_anime_attachments: source?.keep_anime_attachments ?? true,
		apply_edl_cuts: source?.apply_edl_cuts ?? true,
		require_audio_stream: source?.require_audio_stream ?? true,
		min_file_size_mb: source?.min_file_size_mb ?? 0
	};
}

/** Normalizes a profile draft before POST or PUT. */
export function normalizeProfile<T extends Omit<TranscodeProfile, 'id'>>(p: T): T {
	const maxHeight = Math.max(0, Math.round(Number(p.max_height) || 0));
	return {
		...p,
		name: p.name.trim(),
		description: (p.description ?? '').trim(),
		hw_preset: normalizeHwPreset(p.hw_preset),
		max_height: maxHeight,
		normalize_1080p: maxHeight > 0 && maxHeight <= 1080 ? p.normalize_1080p : false,
		vmaf_min_score: Number(p.vmaf_min_score) || 0,
		vmaf_samples: Math.max(1, Math.round(Number(p.vmaf_samples) || 1)),
		vmaf_sample_seconds: Math.max(1, Number(p.vmaf_sample_seconds) || 1),
		vmaf_fps: Math.max(1, Number(p.vmaf_fps) || 1),
		q_min: Math.round(Number(p.q_min) || 0),
		q_max: Math.round(Number(p.q_max) || 0),
		max_iterations: Math.max(1, Math.round(Number(p.max_iterations) || 1)),
		cpu_preset: Math.round(Number(p.cpu_preset) || 0),
		cpu_crf: Math.round(Number(p.cpu_crf) || 0),
		max_bitrate_kbps: Math.max(0, Math.round(Number(p.max_bitrate_kbps) || 0)),
		audio_bitrate_kbps: Math.max(0, Math.round(Number(p.audio_bitrate_kbps) || 0)),
		audio_channels: Math.max(0, Math.round(Number(p.audio_channels) || 0)),
		min_file_size_mb: Math.max(0, Number(p.min_file_size_mb) || 0),
		allowed_languages: (p.allowed_languages ?? []).map((x) => x.trim()).filter(Boolean),
		skip_tags: Array.isArray(p.skip_tags) ? cleanSkipTags(p.skip_tags) : [...DEFAULT_SKIP_TAGS],
		write_tag: typeof p.write_tag === 'string' ? p.write_tag.trim() : DEFAULT_WRITE_TAG
	};
}

/** Validates a TranscodeProfile and returns a localized error or empty string. */
export function validateProfile(p: Omit<TranscodeProfile, 'id'>): string {
	if (!p.name.trim()) return t('transcode.profiles.errName');
	return validateQualityAndAudio({
		q_min: p.q_min,
		q_max: p.q_max,
		vmaf_min_score: p.vmaf_min_score,
		preset: p.cpu_preset,
		crf: p.cpu_crf,
		max_bitrate_kbps: p.max_bitrate_kbps,
		audio_bitrate_kbps: p.audio_bitrate_kbps,
		audio_channels: p.audio_channels
	});
}

/** Returns encoder options ordered for a target codec, keeping any current or detected values. */
export function encoderChoicesForCodec(
	codec: TranscodeProfile['target_codec'],
	detected: string[],
	current = ''
): string[] {
	const set = new Set<string>(['auto', ...(CODEC_ENCODERS[codec] ?? [])]);
	for (const enc of detected) {
		if (enc.includes(codec) || (codec === 'av1' && enc.includes('svtav1')) || (codec === 'hevc' && enc.includes('265')) || (codec === 'h264' && enc.includes('264'))) {
			set.add(enc);
		}
	}
	if (current) set.add(current);
	return [...set];
}

/** Encodes a LibraryAssignment into a select value ('disabled' | 'default' | profile id). */
export function folderAssignmentValue(a: Pick<LibraryAssignment, 'enabled' | 'profile_id'>): string {
	if (!a.enabled) return 'disabled';
	if (a.profile_id == null) return 'default';
	return String(a.profile_id);
}

/** Parses a LibraryAssignment select value back into { enabled, profile_id }. */
export function parseFolderAssignmentValue(val: string): { enabled: boolean; profile_id: number | null } {
	if (val === 'disabled') return { enabled: false, profile_id: null };
	if (val === 'default' || !val) return { enabled: true, profile_id: null };
	const id = Number(val);
	return Number.isFinite(id) && id > 0 ? { enabled: true, profile_id: id } : { enabled: true, profile_id: null };
}

/** Clones and normalizes a TranscodeSettings payload identically to form hydration. */
export function normalizeSettingsClone(s: TranscodeSettings): {
	clone: TranscodeSettings;
	wasArray: boolean;
} {
	const wasArray = Array.isArray(s.gpu_devices);
	const clone: TranscodeSettings = {
		...s,
		hw_preset: normalizeHwPreset(s.hw_preset),
		skip_tags: Array.isArray(s.skip_tags) ? [...s.skip_tags] : [...DEFAULT_SKIP_TAGS],
		write_tag: typeof s.write_tag === 'string' ? s.write_tag : DEFAULT_WRITE_TAG,
		selected_gpus: [...(s.selected_gpus ?? [])],
		allowed_languages: [...(s.allowed_languages ?? [])],
		gpu_devices: wasArray ? [...(s.gpu_devices as string[])] : s.gpu_devices
	};
	return { clone, wasArray };
}

/** Merges refreshed server settings onto dirty local edits, updating only unmodified keys. */
export function mergeRefreshedSettings(
	current: TranscodeSettings,
	prevSaved: TranscodeSettings,
	freshRaw: TranscodeSettings,
	customGpus: string
): {
	settings: TranscodeSettings;
	savedJson: string;
	gpuWasArray: boolean;
	customGpus: string;
} {
	const { clone: fresh, wasArray } = normalizeSettingsClone(freshRaw);
	const next = { ...fresh };
	const rec = next as unknown as Record<string, unknown>;
	const curRec = current as unknown as Record<string, unknown>;
	const prevRec = prevSaved as unknown as Record<string, unknown>;
	for (const key of Object.keys(fresh) as (keyof TranscodeSettings)[]) {
		if (JSON.stringify(curRec[key]) !== JSON.stringify(prevRec[key])) {
			const val = curRec[key];
			rec[key] = Array.isArray(val) ? [...val] : val;
		}
	}
	const gpuUnmodified =
		JSON.stringify(current.gpu_devices) === JSON.stringify(prevSaved.gpu_devices);
	const nextCustomGpus =
		gpuUnmodified && customGpus === parseGpuList(prevSaved.gpu_devices).join(', ')
			? parseGpuList(fresh.gpu_devices).join(', ')
			: customGpus;
	return {
		settings: next,
		savedJson: JSON.stringify(fresh),
		gpuWasArray: gpuUnmodified ? wasArray : Array.isArray(next.gpu_devices),
		customGpus: nextCustomGpus
	};
}
