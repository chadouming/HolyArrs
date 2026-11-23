<script lang="ts">
	import Button from '#lib/ui/Button.svelte';
	import Chip from '#lib/ui/Chip.svelte';
	import Field from '#lib/ui/Field.svelte';
	import LoadError from '#lib/ui/LoadError.svelte';
	import PageLoader from '#lib/ui/PageLoader.svelte';
	import SaveBar from '#lib/ui/SaveBar.svelte';
	import SettingsCard from '#lib/ui/SettingsCard.svelte';
	import { api, message } from '#lib/core/api.ts';
	import { t as tr } from '#lib/core/i18n/i18n.svelte.ts';
	import { plainReason } from '#lib/core/plain.ts';
	import { toasts } from '#lib/core/toasts.svelte.ts';
	import { onMount } from 'svelte';
	import TranscodeProfilesSection from './TranscodeProfilesSection.svelte';
	import TranscodeTagFields from './TranscodeTagFields.svelte';
	import {
		encodeGpuList,
		HW_PRESETS,
		mergeRefreshedSettings,
		normalizeSettings,
		normalizeSettingsClone,
		parseGpuList,
		validateSettings
	} from './helpers.ts';
	import type { HardwareInfo, Overview, TranscodeProfile, TranscodeSettings } from './types.ts';

	let {
		overview,
		canSettings = false,
		onchanged
	}: {
		overview?: Overview | null;
		canSettings?: boolean;
		onchanged?: () => void;
	} = $props();

	let settings = $state<TranscodeSettings | null>(null);
	let savedJson = $state('');
	let gpuWasArray = $state(true);
	let customGpus = $state('');
	let langDraft = $state('');
	let loaded = $state(false);
	let error = $state('');
	let saving = $state(false);
	let formError = $state('');

	let hw = $state<HardwareInfo | null>(null);
	let testingHw = $state(false);
	let hwError = $state('');
	let tagFields = $state<ReturnType<typeof TranscodeTagFields> | undefined>();
	let profilesSection = $state<ReturnType<typeof TranscodeProfilesSection> | undefined>();

	const dirty = $derived(!!settings && !!savedJson && JSON.stringify(settings) !== savedJson);
	const detectedEncoders = $derived(hw?.encoders ?? overview?.encoders ?? []);
	const detectedGpus = $derived(hw?.gpus ?? overview?.gpus ?? []);
	const autoGpuMode = $derived((settings?.selected_gpus ?? []).length === 0);
	const encoderOptions = $derived.by(() => {
		const base = [
			'auto',
			'av1_qsv',
			'av1_vaapi',
			'av1_nvenc',
			'av1_vulkan',
			'libsvtav1',
			'hevc_qsv',
			'hevc_vaapi',
			'hevc_nvenc',
			'hevc_vulkan',
			'libx265',
			'h264_qsv',
			'h264_vaapi',
			'h264_nvenc',
			'h264_vulkan',
			'libx264'
		];
		const all = new Set<string>([...base, ...detectedEncoders]);
		if (settings?.preferred_encoder) all.add(settings.preferred_encoder);
		return [...all];
	});
	const hdrOptions = $derived.by(() => {
		const base = ['preserve', 'tonemap_sdr'];
		if (settings?.hdr_mode && !base.includes(settings.hdr_mode)) return [...base, settings.hdr_mode];
		return base;
	});

	function hydrate(s: TranscodeSettings) {
		const { clone, wasArray } = normalizeSettingsClone(s);
		gpuWasArray = wasArray;
		settings = clone;
		customGpus = parseGpuList(clone.gpu_devices).join(', ');
		savedJson = JSON.stringify(clone);
	}

	async function onProfileSaved(saved: TranscodeProfile) {
		if (!saved.is_default) return;
		try {
			const fresh = await api.get<TranscodeSettings>('/transcode/settings');
			if (!dirty || !settings || !savedJson) {
				hydrate(fresh);
				return;
			}
			const merged = mergeRefreshedSettings(
				settings,
				JSON.parse(savedJson) as TranscodeSettings,
				fresh,
				customGpus
			);
			gpuWasArray = merged.gpuWasArray;
			customGpus = merged.customGpus;
			settings = merged.settings;
			savedJson = merged.savedJson;
		} catch {
			// Ignore background refresh errors so local edits are preserved.
		}
	}

	async function load() {
		error = '';
		try {
			const s = await api.get<TranscodeSettings>('/transcode/settings');
			hydrate(s);
		} catch (e) {
			error = message(e);
		} finally {
			loaded = true;
		}
	}

	onMount(() => {
		void load();
	});

	async function testHardware() {
		if (testingHw) return;
		testingHw = true;
		hwError = '';
		try {
			hw = await api.get<HardwareInfo>('/transcode/hardware');
			onchanged?.();
		} catch (e) {
			hwError = message(e);
			toasts.push(tr('transcode.hw.testFailed'), 'fail', { body: hwError });
		} finally {
			testingHw = false;
		}
	}

	function toggleAutoGpuMode(enableAuto: boolean) {
		if (!settings || !canSettings) return;
		if (enableAuto) {
			settings.selected_gpus = [];
			return;
		}
		const initial = detectedGpus
			.filter((g) => g.selected || g.discrete)
			.map((g) => g.path);
		settings.selected_gpus = initial.length > 0 ? initial : detectedGpus.map((g) => g.path);
	}

	function isGpuChecked(path: string, selectedFlag?: boolean): boolean {
		const explicit = settings?.selected_gpus ?? [];
		if (explicit.length === 0) return Boolean(selectedFlag);
		return explicit.includes(path);
	}

	function toggleGpuNode(path: string) {
		if (!settings || !canSettings) return;
		const current = settings.selected_gpus ?? [];
		if (current.length === 0) {
			const seeded = detectedGpus.filter((g) => g.selected || g.discrete).map((g) => g.path);
			const next = seeded.includes(path)
				? seeded.filter((p) => p !== path)
				: [...seeded, path];
			settings.selected_gpus = next;
			return;
		}
		settings.selected_gpus = current.includes(path)
			? current.filter((p) => p !== path)
			: [...current, path];
	}

	function syncCustomGpus() {
		if (!settings) return;
		const list = customGpus
			.split(',')
			.map((x) => x.trim())
			.filter(Boolean);
		settings.gpu_devices = encodeGpuList(list, gpuWasArray);
	}

	function addLanguage() {
		if (!settings || !canSettings) return;
		const parts = langDraft
			.split(/[,\s]+/)
			.map((x) => x.trim().toLowerCase())
			.filter(Boolean);
		if (parts.length === 0) return;
		const next = [...settings.allowed_languages];
		for (const code of parts) {
			if (!next.includes(code)) next.push(code);
		}
		settings.allowed_languages = next;
		langDraft = '';
	}

	function removeLanguage(code: string) {
		if (!settings || !canSettings) return;
		settings.allowed_languages = settings.allowed_languages.filter((x) => x !== code);
	}

	function onLangKey(e: KeyboardEvent) {
		if (e.key === 'Enter' || e.key === ',') {
			e.preventDefault();
			addLanguage();
		}
	}

	function discard() {
		if (!savedJson) return;
		hydrate(JSON.parse(savedJson) as TranscodeSettings);
		formError = '';
	}

	async function save(e: SubmitEvent) {
		e.preventDefault();
		if (!settings || !canSettings || saving) return;
		syncCustomGpus();
		addLanguage();
		tagFields?.commitPending();
		const normalized = normalizeSettings(settings);
		const err = validateSettings(normalized);
		if (err) {
			formError = err;
			return;
		}
		formError = '';
		saving = true;
		try {
			const updated = await api.put<TranscodeSettings>('/transcode/settings', normalized);
			hydrate(updated);
			void profilesSection?.reload();
			toasts.push(tr('transcode.settings.saved'), 'ok');
			onchanged?.();
		} catch (errRes) {
			formError = message(errRes);
			toasts.push(tr('transcode.settings.saveFailed'), 'fail', { body: formError });
		} finally {
			saving = false;
		}
	}
</script>

<div class="settings-tab" data-testid="transcode-settings-tab">
	<SettingsCard
		title={tr('transcode.hw.title')}
		description={tr('transcode.hw.description')}
		icon="gauge"
	>
		{#snippet footer()}
			<Button
				variant="secondary"
				icon="refresh"
				busy={testingHw}
				onclick={testHardware}
				data-testid="transcode-test-hw"
			>{tr('transcode.hw.test')}</Button>
		{/snippet}

		{@const ready = hw?.ready ?? overview?.ready ?? false}
		{@const ffmpegOk = hw?.ffmpeg_found ?? overview?.ffmpeg_found ?? false}
		{@const ffprobeOk = hw?.ffprobe_found ?? overview?.ffprobe_found ?? false}
		{@const vmafOk = hw?.vmaf_available ?? overview?.vmaf_available ?? false}
		<div class="hw-chips" data-testid="transcode-hw-status">
			<span class="chip" class:ok={ready} class:fail={!ready}>
				{ready ? tr('transcode.hw.ready') : tr('transcode.hw.notReady')}
			</span>
			<span class="chip" class:ok={ffmpegOk} class:fail={!ffmpegOk}>
				FFmpeg: {ffmpegOk ? tr('transcode.hw.found') : tr('transcode.hw.missing')}
			</span>
			<span class="chip" class:ok={ffprobeOk} class:fail={!ffprobeOk}>
				FFprobe: {ffprobeOk ? tr('transcode.hw.found') : tr('transcode.hw.missing')}
			</span>
			<span class="chip" class:ok={vmafOk} class:wait={!vmafOk}>
				VMAF: {vmafOk ? tr('transcode.hw.available') : tr('transcode.hw.unavailable')}
			</span>
			{#if hw}
				<span class="chip" class:ok={hw.tonemapx_available}>
					tonemapx: {hw.tonemapx_available ? tr('transcode.hw.available') : tr('transcode.hw.unavailable')}
				</span>
				<span class="chip" class:ok={hw.zscale_available}>
					zscale: {hw.zscale_available ? tr('transcode.hw.available') : tr('transcode.hw.unavailable')}
				</span>
			{/if}
		</div>

		{#if hw}
			<dl class="facts hw-paths">
				{#if hw.ffmpeg_path}<dt>FFmpeg</dt><dd>{hw.ffmpeg_path}</dd>{/if}
				{#if hw.ffprobe_path}<dt>FFprobe</dt><dd>{hw.ffprobe_path}</dd>{/if}
				{#if hw.vmaf_ffmpeg_path}<dt>VMAF FFmpeg</dt><dd>{hw.vmaf_ffmpeg_path}</dd>{/if}
				{#if hw.preferred_encoder}<dt>{tr('transcode.settings.preferredEncoder')}</dt><dd>{hw.preferred_encoder}</dd>{/if}
			</dl>
		{/if}

		{#if detectedEncoders.length > 0}
			<div class="hw-block">
				<span class="hint">{tr('transcode.hw.encoders')}</span>
				<div class="pill-list">
					{#each detectedEncoders as enc (enc)}
						<span class="pill">{enc}</span>
					{/each}
				</div>
			</div>
		{/if}

		{#if hwError}
			<p class="form-error" role="alert">{hwError}</p>
		{/if}
	</SettingsCard>

	{#if !canSettings}
		<p class="notice" role="status">{tr('transcode.settings.readOnlyNotice')}</p>
	{/if}

	<TranscodeProfilesSection
		bind:this={profilesSection}
		{detectedEncoders}
		{canSettings}
		{onchanged}
		onprofilesaved={onProfileSaved}
	/>

	{#if error && !settings}
		<LoadError hasData={false} message={plainReason(error).text} onretry={load} />
	{:else}
		<PageLoader loading={!loaded} label={tr('transcode.settings.loading')}>
			{#if settings}
				<form id="transcode-settings-form" class="stack" onsubmit={save}>
					<SettingsCard
						title={tr('transcode.settings.hardwareTitle')}
						description={tr('transcode.settings.hardwareDesc')}
						icon="film"
					>
						<div class="three-col">
							<Field id="tc-encoder" label={tr('transcode.settings.preferredEncoder')} hint={tr('transcode.settings.preferredEncoderHint')}>
								<select id="tc-encoder" class="input" bind:value={settings.preferred_encoder} disabled={!canSettings}>
									{#each encoderOptions as enc (enc)}
										<option value={enc}>{enc}</option>
									{/each}
								</select>
							</Field>
							<Field id="tc-hw-preset" label={tr('transcode.settings.hwPreset')} hint={tr('transcode.settings.hwPresetHint')}>
								<select id="tc-hw-preset" class="input" bind:value={settings.hw_preset} disabled={!canSettings} data-testid="transcode-hw-preset">
									{#each HW_PRESETS as preset (preset)}
										<option value={preset}>{tr(`transcode.hwPreset.${preset}`)}</option>
									{/each}
								</select>
							</Field>
							<Field id="tc-concurrent" label={tr('transcode.settings.concurrentJobs')} hint={tr('transcode.settings.concurrentJobsHint')}>
								<input id="tc-concurrent" class="input" type="number" min="1" max="16" step="1" bind:value={settings.concurrent_jobs} disabled={!canSettings} />
							</Field>
						</div>

						<div class="gpu-picker" data-testid="transcode-gpu-picker">
							<label class="switch multi">
								<input
									type="checkbox"
									checked={autoGpuMode}
									onchange={(e) => toggleAutoGpuMode((e.currentTarget as HTMLInputElement).checked)}
									disabled={!canSettings}
									data-testid="transcode-gpu-auto"
								/>
								<span class="sw" aria-hidden="true"></span>
								<span>
									<span class="label">{tr('transcode.settings.gpuAuto')}</span>
									<span class="hint block">{tr('transcode.settings.gpuAutoHint')}</span>
								</span>
							</label>

							{#if detectedGpus.length > 0}
								<div class="gpu-grid">
									{#each detectedGpus as g (g.path)}
										{@const checked = isGpuChecked(g.path, g.selected ?? g.discrete)}
										<label class="gpu-card" class:selected={checked} data-testid="transcode-gpu-card">
											<input
												type="checkbox"
												{checked}
												onchange={() => toggleGpuNode(g.path)}
												disabled={!canSettings}
											/>
											<div class="gpu-card-body">
												<div class="gpu-main">
													<span class="gpu-name">{g.name || g.path}</span>
													<span class="pill">{g.discrete ? tr('transcode.hw.discrete') : tr('transcode.hw.integrated')}</span>
													{#if g.vendor}<span class="pill">{g.vendor}</span>{/if}
													{#if g.busy_jobs > 0}<span class="pill wait">{tr('transcode.hw.busyJobs', { n: g.busy_jobs })}</span>{/if}
													{#if autoGpuMode && g.selected}<span class="pill ok">{tr('transcode.settings.autoSelected')}</span>{/if}
												</div>
												<span class="tp-note mono">{g.path}{#if g.pci_slot} · {g.pci_slot}{/if}</span>
											</div>
										</label>
									{/each}
								</div>
							{:else}
								<p class="tp-note">{tr('transcode.stats.noGpuDetected')}</p>
							{/if}
						</div>

						<Field id="tc-gpu-custom" label={tr('transcode.settings.gpuDevices')} hint={tr('transcode.settings.gpuDevicesHint')}>
							<input
								id="tc-gpu-custom"
								class="input mono"
								type="text"
								bind:value={customGpus}
								onblur={syncCustomGpus}
								disabled={!canSettings}
								placeholder="/dev/dri/renderD128"
							/>
						</Field>

						<label class="switch multi">
							<input type="checkbox" bind:checked={settings.allow_cpu_fallback} disabled={!canSettings} />
							<span class="sw" aria-hidden="true"></span>
							<span>
								<span class="label">{tr('transcode.settings.cpuFallback')}</span>
								<span class="hint block">{tr('transcode.settings.cpuFallbackHint')}</span>
							</span>
						</label>
					</SettingsCard>

					<SettingsCard
						title={tr('transcode.settings.pipelineTitle')}
						description={tr('transcode.settings.pipelineDesc')}
						icon="sliders"
					>
						<label class="switch multi">
							<input type="checkbox" bind:checked={settings.auto_transcode} disabled={!canSettings} />
							<span class="sw" aria-hidden="true"></span>
							<span>
								<span class="label">{tr('transcode.settings.autoTranscode')}</span>
								<span class="hint block">{tr('transcode.settings.autoTranscodeHint')}</span>
							</span>
						</label>

						<div class="two-col">
							<Field id="tc-min-size" label={tr('transcode.settings.minFileSizeMb')} hint={tr('transcode.settings.minFileSizeMbHint')}>
								<input id="tc-min-size" class="input" type="number" min="0" step="1" bind:value={settings.min_file_size_mb} disabled={!canSettings} />
							</Field>
							<Field id="tc-scratch" label={tr('transcode.settings.scratchDir')} hint={tr('transcode.settings.scratchDirHint')}>
								<input id="tc-scratch" class="input mono" type="text" bind:value={settings.scratch_dir} disabled={!canSettings} placeholder="/tmp/holyarrs-transcode" />
							</Field>
						</div>

						<TranscodeTagFields
							bind:this={tagFields}
							bind:skipTags={settings.skip_tags}
							bind:writeTag={settings.write_tag}
							disabled={!canSettings}
							idPrefix="tc"
							testidPrefix="transcode"
						/>

						{#snippet advanced()}
							{#if settings}
							<div class="adv-stack">
								<div class="switch-grid">
									<label class="switch multi">
										<input type="checkbox" bind:checked={settings.keep_original_in_holding} disabled={!canSettings} />
										<span class="sw" aria-hidden="true"></span>
										<span>
											<span class="label">{tr('transcode.settings.keepHolding')}</span>
											<span class="hint block">{tr('transcode.settings.keepHoldingHint')}</span>
										</span>
									</label>

									<label class="switch multi">
										<input type="checkbox" bind:checked={settings.copy_if_larger} disabled={!canSettings} />
										<span class="sw" aria-hidden="true"></span>
										<span>
											<span class="label">{tr('transcode.settings.copyIfLarger')}</span>
											<span class="hint block">{tr('transcode.settings.copyIfLargerHint')}</span>
										</span>
									</label>

									<label class="switch multi">
										<input type="checkbox" bind:checked={settings.apply_edl_cuts} disabled={!canSettings} />
										<span class="sw" aria-hidden="true"></span>
										<span>
											<span class="label">{tr('transcode.settings.applyEdl')}</span>
											<span class="hint block">{tr('transcode.settings.applyEdlHint')}</span>
										</span>
									</label>
								</div>

								<div class="adv-group">
									<h3 class="group-title">{tr('transcode.settings.videoTitle')}</h3>
									<div class="two-col">
										<Field id="tc-hdr" label={tr('transcode.settings.hdrMode')} hint={tr('transcode.settings.hdrModeHint')}>
											<select id="tc-hdr" class="input" bind:value={settings.hdr_mode} disabled={!canSettings}>
												{#each hdrOptions as mode (mode)}
													<option value={mode}>{mode}</option>
												{/each}
											</select>
										</Field>
										<Field id="tc-max-br" label={tr('transcode.settings.maxBitrateKbps')} hint={tr('transcode.settings.maxBitrateKbpsHint')}>
											<input id="tc-max-br" class="input" type="number" min="100" max="100000" step="100" bind:value={settings.max_bitrate_kbps} disabled={!canSettings} />
										</Field>
									</div>

									<div class="switch-grid">
										<label class="switch multi">
											<input type="checkbox" bind:checked={settings.normalize_1080p} disabled={!canSettings} />
											<span class="sw" aria-hidden="true"></span>
											<span>
												<span class="label">{tr('transcode.settings.normalize1080p')}</span>
												<span class="hint block">{tr('transcode.settings.normalize1080pHint')}</span>
											</span>
										</label>

										<label class="switch multi">
											<input type="checkbox" bind:checked={settings.pillarbox_4_3} disabled={!canSettings} />
											<span class="sw" aria-hidden="true"></span>
											<span>
												<span class="label">{tr('transcode.settings.pillarbox43')}</span>
												<span class="hint block">{tr('transcode.settings.pillarbox43Hint')}</span>
											</span>
										</label>
									</div>

									<label class="switch multi">
										<input type="checkbox" bind:checked={settings.vmaf_enabled} disabled={!canSettings} />
										<span class="sw" aria-hidden="true"></span>
										<span>
											<span class="label">{tr('transcode.settings.vmafEnabled')}</span>
											<span class="hint block">{tr('transcode.settings.vmafEnabledHint')}</span>
										</span>
									</label>

									<div class="three-col">
										<Field id="tc-vmaf-min" label={tr('transcode.settings.vmafMinScore')}>
											<input id="tc-vmaf-min" class="input" type="number" min="50" max="100" step="0.5" bind:value={settings.vmaf_min_score} disabled={!canSettings || !settings.vmaf_enabled} />
										</Field>
										<Field id="tc-q-min" label={tr('transcode.settings.qMin')}>
											<input id="tc-q-min" class="input" type="number" min="1" max="63" step="1" bind:value={settings.q_min} disabled={!canSettings} />
										</Field>
										<Field id="tc-q-max" label={tr('transcode.settings.qMax')}>
											<input id="tc-q-max" class="input" type="number" min="1" max="63" step="1" bind:value={settings.q_max} disabled={!canSettings} />
										</Field>
									</div>

									<div class="three-col">
										<Field id="tc-vmaf-samples" label={tr('transcode.settings.vmafSamples')}>
											<input id="tc-vmaf-samples" class="input" type="number" min="1" max="12" step="1" bind:value={settings.vmaf_samples} disabled={!canSettings} />
										</Field>
										<Field id="tc-vmaf-secs" label={tr('transcode.settings.vmafSampleSeconds')}>
											<input id="tc-vmaf-secs" class="input" type="number" min="3" max="120" step="1" bind:value={settings.vmaf_sample_seconds} disabled={!canSettings} />
										</Field>
										<Field id="tc-vmaf-fps" label={tr('transcode.settings.vmafFps')}>
											<input id="tc-vmaf-fps" class="input" type="number" min="1" max="60" step="1" bind:value={settings.vmaf_fps} disabled={!canSettings} />
										</Field>
									</div>

									<div class="three-col">
										<Field id="tc-max-iter" label={tr('transcode.settings.maxIterations')}>
											<input id="tc-max-iter" class="input" type="number" min="1" max="12" step="1" bind:value={settings.max_iterations} disabled={!canSettings} />
										</Field>
										<Field id="tc-svt-preset" label={tr('transcode.settings.svtav1Preset')}>
											<input id="tc-svt-preset" class="input" type="number" min="0" max="13" step="1" bind:value={settings.svtav1_preset} disabled={!canSettings} />
										</Field>
										<Field id="tc-svt-crf" label={tr('transcode.settings.svtav1Crf')}>
											<input id="tc-svt-crf" class="input" type="number" min="0" max="63" step="1" bind:value={settings.svtav1_crf} disabled={!canSettings} />
										</Field>
									</div>
								</div>

								<div class="adv-group">
									<h3 class="group-title">{tr('transcode.settings.audioTitle')}</h3>
									<div class="field">
										<label for="tc-lang-input">{tr('transcode.settings.allowedLanguages')}</label>
										{#if settings.allowed_languages.length > 0}
											<div class="chips">
												{#each settings.allowed_languages as code (code)}
													<Chip label={code} onremove={canSettings ? () => removeLanguage(code) : undefined} />
												{/each}
											</div>
										{/if}
										<div class="lang-entry">
											<input
												id="tc-lang-input"
												class="input mono"
												type="text"
												bind:value={langDraft}
												onkeydown={onLangKey}
												disabled={!canSettings}
												placeholder="eng, jpn, und"
											/>
											<Button variant="secondary" size="md" onclick={addLanguage} disabled={!canSettings || !langDraft.trim()}>
												{tr('transcode.settings.addLanguage')}
											</Button>
										</div>
										<span class="hint">{tr('transcode.settings.allowedLanguagesHint')}</span>
									</div>

									<div class="two-col">
										<Field id="tc-audio-br" label={tr('transcode.settings.audioBitrateKbps')} hint={tr('transcode.settings.audioBitrateKbpsHint')}>
											<input id="tc-audio-br" class="input" type="number" min="32" max="1536" step="16" bind:value={settings.audio_bitrate_kbps} disabled={!canSettings} />
										</Field>
										<Field id="tc-audio-ch" label={tr('transcode.settings.audioChannels')} hint={tr('transcode.settings.audioChannelsHint')}>
											<input id="tc-audio-ch" class="input" type="number" min="1" max="8" step="1" bind:value={settings.audio_channels} disabled={!canSettings} />
										</Field>
									</div>

									<div class="switch-grid">
										<label class="switch multi">
											<input type="checkbox" bind:checked={settings.prefer_vfq} disabled={!canSettings} />
											<span class="sw" aria-hidden="true"></span>
											<span>
												<span class="label">{tr('transcode.settings.preferVfq')}</span>
												<span class="hint block">{tr('transcode.settings.preferVfqHint')}</span>
											</span>
										</label>

										<label class="switch multi">
											<input type="checkbox" bind:checked={settings.keep_anime_attachments} disabled={!canSettings} />
											<span class="sw" aria-hidden="true"></span>
											<span>
												<span class="label">{tr('transcode.settings.keepAttachments')}</span>
												<span class="hint block">{tr('transcode.settings.keepAttachmentsHint')}</span>
											</span>
										</label>
									</div>

									<label class="switch multi">
										<input type="checkbox" bind:checked={settings.require_audio_stream} disabled={!canSettings} />
										<span class="sw" aria-hidden="true"></span>
										<span>
											<span class="label">{tr('transcode.settings.requireAudio')}</span>
											<span class="hint block">{tr('transcode.settings.requireAudioHint')}</span>
										</span>
									</label>
								</div>
							</div>
							{/if}
						{/snippet}
					</SettingsCard>

					{#if formError}
						<p class="form-error" role="alert">{formError}</p>
					{/if}

					{#if canSettings}
						<div class="save-actions">
							<Button
								variant="primary"
								type="submit"
								busy={saving}
								disabled={!dirty || saving}
								data-testid="transcode-save-settings"
							>{tr('transcode.settings.save')}</Button>
						</div>
					{/if}
				</form>

				{#if canSettings}
					<SaveBar
						{dirty}
						form="transcode-settings-form"
						{saving}
						ondiscard={discard}
						label={tr('transcode.settings.save')}
					/>
				{/if}
			{/if}
		</PageLoader>
	{/if}
</div>

<style>
	.settings-tab { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
	.hw-chips { display: flex; flex-wrap: wrap; gap: 8px; }
	.hw-paths { margin-top: 4px; }
	.hw-block { display: flex; flex-direction: column; gap: 6px; }
	.pill-list { display: flex; flex-wrap: wrap; gap: 6px; }
	.pill { display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 999px; font: 600 11.5px/1 var(--font-ui); background: color-mix(in srgb, var(--text) 8%, transparent); color: color-mix(in srgb, var(--text) 82%, var(--bg)); }
	.pill.ok { background: color-mix(in srgb, var(--ok) 12%, transparent); color: var(--ok-text, var(--ok)); }
	.pill.wait { background: color-mix(in srgb, var(--wait, var(--accent)) 16%, transparent); color: var(--wait-text, var(--wait, var(--text))); }
	.gpu-picker { display: flex; flex-direction: column; gap: 10px; }
	.gpu-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
	.gpu-card {
		display: flex; align-items: flex-start; gap: 12px;
		padding: 12px 14px; border-radius: var(--r-btn);
		background: var(--surface-2); border: 1px solid var(--line);
		cursor: pointer; transition: border-color var(--dur-fast) var(--ease-out), background-color var(--dur-fast) var(--ease-out);
		min-width: 0;
	}
	.gpu-card.selected {
		border-color: var(--line-strong);
		background: var(--surface-1);
		box-shadow: 0 1px 3px rgb(0 0 0 / 0.08);
	}
	.gpu-card .pill:not(.ok):not(.wait) { color: var(--text); }
	.gpu-card input { margin-top: 3px; width: 18px; height: 18px; flex: none; accent-color: var(--ha-red-press); }
	.gpu-card-body { display: flex; flex-direction: column; gap: 3px; min-width: 0; flex: 1 1 auto; }
	.gpu-card-body .tp-note { min-width: 0; overflow-wrap: anywhere; }
	.gpu-main { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
	.gpu-name { font: 600 13.5px/1.3 var(--font-ui); color: var(--text); overflow-wrap: anywhere; }
	.two-col { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: start; gap: 14px 16px; }
	.three-col { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); align-items: start; gap: 14px 16px; }
	.two-col :global(.field),
	.three-col :global(.field),
	.two-col :global(.field + .field),
	.three-col :global(.field + .field) { margin-top: 0; }
	.settings-tab :global(.input),
	.settings-tab :global(select.input) { height: 40px; min-height: 40px; }
	.adv-stack { display: flex; flex-direction: column; gap: 14px; }
	.adv-group { display: flex; flex-direction: column; gap: 14px; padding-top: 14px; border-top: 1px solid var(--line); }
	.group-title { margin: 0; font: 600 14px/1.3 var(--font-ui); color: var(--text); }
	.switch-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: start; gap: 10px 16px; }
	.switch.multi { align-items: flex-start; }
	.switch.multi .sw,
	.switch.multi input { margin-top: 2px; }
	.block { display: block; }
	.chips { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 4px; }
	.lang-entry { display: flex; gap: 8px; align-items: center; }
	.lang-entry .input { flex: 1; }
	.lang-entry :global(.ui-btn) { height: 40px; min-height: 40px; flex: none; }
	.save-actions { display: flex; justify-content: flex-end; padding-top: 4px; }

	@media (max-width: 767px) {
		.gpu-grid, .two-col, .three-col, .switch-grid { grid-template-columns: minmax(0, 1fr); }
	}
	@media (pointer: coarse) {
		.settings-tab :global(.input),
		.settings-tab :global(select.input),
		.lang-entry :global(.ui-btn) { height: 44px; min-height: 44px; }
	}
</style>
