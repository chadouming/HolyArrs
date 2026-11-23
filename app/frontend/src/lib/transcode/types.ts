// Shared API contract types and view filters for the Transcode module.

export interface GPUDevice {
	path: string;
	vendor: string;
	name: string;
	pci_slot: string;
	discrete: boolean;
	busy_jobs: number;
	selected?: boolean;
}

export interface OverviewJob {
	id: number;
	kind: string;
	state: string;
	file_id?: number;
	title_id?: number;
	title?: string;
	media_kind?: string;
	season?: number | null;
	episode?: number | null;
	relative_path?: string;
	progress: number;
	phase: string;
	note: string;
	fps: number;
	speed: number;
	selected_q?: number | null;
	mean_vmaf?: number | null;
	encoder: string;
	gpu_device: string;
	input_bytes: number;
	profile_name?: string;
	started_at?: string | null;
	created_at: string;
}

export interface Overview {
	paused: boolean;
	auto_transcode: boolean;
	ready: boolean;
	ffmpeg_found: boolean;
	ffprobe_found: boolean;
	vmaf_available: boolean;
	gpus: GPUDevice[];
	encoders: string[];
	preferred_encoder: string;
	counts: {
		total: number;
		done: number;
		skipped: number;
		pending: number;
		queued: number;
		running: number;
		failed: number;
		unprobed: number;
	};
	coverage: {
		total: number;
		completed: number;
		percent: number;
	};
	savings: {
		input_bytes: number;
		output_bytes: number;
		saved_bytes: number;
		percent: number;
		files_done: number;
	};
	speed: {
		fps: number;
		realtime_x: number;
		live_fps: number;
		live_realtime_x: number;
		samples: number;
	};
	running: Record<string, number>;
	queued: Record<string, number>;
	failed_count: number;
	jobs: OverviewJob[];
	eta_seconds?: number;
}

export interface ItemRow {
	file_id: number;
	title_id: number;
	title: string;
	media_kind: string;
	season?: number | null;
	episode?: number | null;
	episode_name?: string;
	relative_path: string;
	state: string;
	skip_reason?: string;
	fileflows_tagged: boolean;
	source_codec: string;
	source_width: number;
	source_height: number;
	source_hdr: string;
	target_codec: string;
	selected_q?: number | null;
	mean_vmaf?: number | null;
	gpu_device: string;
	input_bytes: number;
	output_bytes: number;
	saved_bytes: number;
	encode_seconds: number;
	avg_fps: number;
	avg_speed: number;
	edl_applied: boolean;
	tonemapped: boolean;
	profile_name?: string;
	last_error?: string;
	probed_at?: string | null;
	completed_at?: string | null;
}

export interface FailedJob {
	id: number;
	kind: string;
	state: string;
	file_id?: number;
	title_id?: number;
	title?: string;
	media_kind?: string;
	season?: number | null;
	episode?: number | null;
	relative_path?: string;
	progress: number;
	note: string;
	error: string;
	attempts: number;
	max_attempts: number;
	created_at: string;
	updated_at: string;
	finished_at?: string | null;
}

export interface HardwareInfo {
	ready: boolean;
	ffmpeg_found: boolean;
	ffprobe_found: boolean;
	vmaf_available: boolean;
	ffmpeg_path: string;
	ffprobe_path: string;
	vmaf_ffmpeg_path: string;
	tonemapx_available: boolean;
	zscale_available: boolean;
	encoders: string[];
	preferred_encoder: string;
	gpus: GPUDevice[];
}

export interface TranscodeSettings {
	auto_transcode: boolean;
	concurrent_jobs: number;
	preferred_encoder: string;
	hw_preset: string;
	gpu_devices: string[] | string;
	selected_gpus?: string[];
	allow_cpu_fallback: boolean;
	keep_original_in_holding: boolean;
	copy_if_larger: boolean;
	skip_tags: string[];
	write_tag: string;
	normalize_1080p: boolean;
	pillarbox_4_3: boolean;
	hdr_mode: string;
	vmaf_enabled: boolean;
	vmaf_min_score: number;
	vmaf_samples: number;
	vmaf_sample_seconds: number;
	vmaf_fps: number;
	q_min: number;
	q_max: number;
	max_iterations: number;
	svtav1_preset: number;
	svtav1_crf: number;
	max_bitrate_kbps: number;
	allowed_languages: string[];
	prefer_vfq: boolean;
	audio_bitrate_kbps: number;
	audio_channels: number;
	keep_anime_attachments: boolean;
	apply_edl_cuts: boolean;
	require_audio_stream: boolean;
	min_file_size_mb: number;
	scratch_dir: string;
}

export interface TranscodeProfile {
	id: number;
	name: string;
	description: string;
	is_default: boolean;
	target_codec: 'av1' | 'hevc' | 'h264';
	preferred_encoder: string;
	hw_preset: string;
	ten_bit: boolean;
	normalize_1080p: boolean;
	max_height: number;
	scale_mode: 'normalize' | 'downscale_only' | 'none';
	pillarbox_4_3: boolean;
	hdr_mode: 'preserve' | 'tonemap_sdr';
	vmaf_enabled: boolean;
	vmaf_min_score: number;
	vmaf_samples: number;
	vmaf_sample_seconds: number;
	vmaf_fps: number;
	q_min: number;
	q_max: number;
	max_iterations: number;
	cpu_preset: number;
	cpu_crf: number;
	max_bitrate_kbps: number;
	copy_if_larger: boolean;
	skip_tags: string[];
	write_tag: string;
	keep_original_in_holding: boolean;
	allowed_languages: string[];
	prefer_vfq: boolean;
	audio_codec: 'aac' | 'opus' | 'copy';
	audio_bitrate_kbps: number;
	audio_channels: number;
	keep_anime_attachments: boolean;
	apply_edl_cuts: boolean;
	require_audio_stream: boolean;
	min_file_size_mb: number;
}

export interface LibraryAssignment {
	library_folder_id: number;
	label: string;
	path: string;
	kind: string;
	enabled: boolean;
	profile_id: number | null;
	profile_name: string;
}

export interface WaitingJob {
	id: number;
	kind: string;
	state: string;
	priority: 'now' | 'normal' | 'idle' | string;
	position: number;
	file_id?: number;
	title_id?: number;
	title?: string;
	media_kind?: string;
	season?: number | null;
	episode?: number | null;
	relative_path?: string;
	profile_name?: string;
	source_codec?: string;
	source_width?: number;
	source_height?: number;
	input_bytes: number;
	created_at: string;
	run_after: string;
}

export type TranscodeTab = 'overview' | 'waiting' | 'history' | 'failed' | 'settings';
export type ItemStateFilter = 'pending' | 'all' | 'done' | 'skipped';
export type HistoryStateFilter = 'done' | 'skipped' | 'all';
