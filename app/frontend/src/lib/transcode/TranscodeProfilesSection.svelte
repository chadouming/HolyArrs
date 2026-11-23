<script lang="ts">
	import { ask } from '#lib/ui/ask.svelte.ts';
	import Button from '#lib/ui/Button.svelte';
	import Chip from '#lib/ui/Chip.svelte';
	import EmptyState from '#lib/ui/EmptyState.svelte';
	import Field from '#lib/ui/Field.svelte';
	import LoadError from '#lib/ui/LoadError.svelte';
	import PageLoader from '#lib/ui/PageLoader.svelte';
	import SettingsCard from '#lib/ui/SettingsCard.svelte';
	import { api, message } from '#lib/core/api.ts';
	import { t as tr } from '#lib/core/i18n/i18n.svelte.ts';
	import { plainReason } from '#lib/core/plain.ts';
	import { toasts } from '#lib/core/toasts.svelte.ts';
	import { onMount } from 'svelte';
	import TranscodeTagFields from './TranscodeTagFields.svelte';
	import {
		createDefaultProfileDraft,
		DEFAULT_SKIP_TAGS,
		DEFAULT_WRITE_TAG,
		encoderChoicesForCodec,
		folderAssignmentValue,
		HW_PRESETS,
		normalizeHwPreset,
		normalizeProfile,
		parseFolderAssignmentValue,
		validateProfile
	} from './helpers.ts';
	import type { LibraryAssignment, TranscodeProfile } from './types.ts';

	interface ProfilesResponse {
		profiles: TranscodeProfile[];
		library_folders: LibraryAssignment[];
	}

	let {
		detectedEncoders = [],
		canSettings = false,
		onchanged,
		onprofilesaved
	}: {
		detectedEncoders?: string[];
		canSettings?: boolean;
		onchanged?: () => void;
		onprofilesaved?: (saved: TranscodeProfile) => void;
	} = $props();

	let profiles = $state<TranscodeProfile[]>([]);
	let libraryFolders = $state<LibraryAssignment[]>([]);
	let loaded = $state(false);
	let error = $state('');

	let selectedId = $state<number | null>(null);
	let draft = $state<Omit<TranscodeProfile, 'id'> | null>(null);
	let langDraft = $state('');
	let profileError = $state('');
	let savingProfile = $state(false);
	let deletingProfile = $state(false);
	let savingFolderId = $state<number | null>(null);
	let tagFields = $state<ReturnType<typeof TranscodeTagFields> | undefined>();

	const defaultProfile = $derived(profiles.find((p) => p.is_default) ?? profiles[0] ?? null);
	const encoderChoices = $derived(
		draft ? encoderChoicesForCodec(draft.target_codec, detectedEncoders, draft.preferred_encoder) : ['auto']
	);
	const activeProfile = $derived(
		selectedId !== null ? (profiles.find((p) => p.id === selectedId) ?? null) : null
	);
	const canDelete = $derived(
		canSettings &&
			selectedId !== null &&
			profiles.length > 1 &&
			!(activeProfile?.is_default ?? false)
	);

	function pickProfile(p: TranscodeProfile) {
		selectedId = p.id;
		const { id: _id, ...rest } = p;
		draft = {
			...rest,
			hw_preset: normalizeHwPreset(rest.hw_preset),
			skip_tags: Array.isArray(rest.skip_tags) ? [...rest.skip_tags] : [...DEFAULT_SKIP_TAGS],
			write_tag: typeof rest.write_tag === 'string' ? rest.write_tag : DEFAULT_WRITE_TAG,
			allowed_languages: [...(rest.allowed_languages ?? [])]
		};
		langDraft = '';
		profileError = '';
	}

	function startNewProfile() {
		selectedId = null;
		draft = createDefaultProfileDraft(
			defaultProfile ?? undefined,
			tr('transcode.profiles.newName')
		);
		langDraft = '';
		profileError = '';
	}

	function duplicateCurrent() {
		if (!draft) return;
		selectedId = null;
		draft = createDefaultProfileDraft(
			draft,
			tr('transcode.profiles.copyName', { name: draft.name || tr('transcode.profiles.newName') })
		);
		langDraft = '';
		profileError = '';
	}

	async function load(preferId?: number | null, preserveUnsavedDraft = false) {
		try {
			const res = await api.get<ProfilesResponse>('/transcode/profiles');
			profiles = res.profiles ?? [];
			libraryFolders = res.library_folders ?? [];
			error = '';
			if (preserveUnsavedDraft && selectedId === null) {
				return;
			}
			const target =
				(preferId != null ? profiles.find((p) => p.id === preferId) : null) ??
				(selectedId != null ? profiles.find((p) => p.id === selectedId) : null) ??
				profiles.find((p) => p.is_default) ??
				profiles[0];
			if (target) pickProfile(target);
			else draft = null;
		} catch (e) {
			error = message(e);
		} finally {
			loaded = true;
		}
	}

	export async function reload() {
		await load(undefined, true);
	}

	onMount(() => {
		void load();
	});

	function onCodecChange() {
		if (!draft) return;
		const valid = encoderChoicesForCodec(draft.target_codec, detectedEncoders);
		if (!valid.includes(draft.preferred_encoder)) {
			draft.preferred_encoder = 'auto';
		}
	}

	function addLang() {
		if (!draft || !canSettings) return;
		const parts = langDraft
			.split(/[,\s]+/)
			.map((x) => x.trim().toLowerCase())
			.filter(Boolean);
		if (parts.length === 0) return;
		const next = [...draft.allowed_languages];
		for (const code of parts) {
			if (!next.includes(code)) next.push(code);
		}
		draft.allowed_languages = next;
		langDraft = '';
	}

	function removeLang(code: string) {
		if (!draft || !canSettings) return;
		draft.allowed_languages = draft.allowed_languages.filter((x) => x !== code);
	}

	function onLangKey(e: KeyboardEvent) {
		if (e.key === 'Enter' || e.key === ',') {
			e.preventDefault();
			addLang();
		}
	}

	async function saveProfile(e: SubmitEvent) {
		e.preventDefault();
		if (!draft || !canSettings || savingProfile) return;
		addLang();
		tagFields?.commitPending();
		const normalized = normalizeProfile(draft);
		const err = validateProfile(normalized);
		if (err) {
			profileError = err;
			return;
		}
		profileError = '';
		savingProfile = true;
		try {
			let saved: TranscodeProfile;
			if (selectedId === null) {
				saved = await api.post<TranscodeProfile>('/transcode/profiles', normalized);
			} else {
				saved = await api.put<TranscodeProfile>(`/transcode/profiles/${selectedId}`, {
					id: selectedId,
					...normalized
				});
			}
			toasts.push(tr('transcode.profiles.saved'), 'ok', { body: saved.name });
			await load(saved.id);
			onprofilesaved?.(saved);
			onchanged?.();
		} catch (errRes) {
			profileError = message(errRes);
			toasts.push(tr('transcode.profiles.saveFailed'), 'fail', { body: profileError });
		} finally {
			savingProfile = false;
		}
	}

	async function deleteProfile() {
		if (!canDelete || selectedId === null || deletingProfile || !activeProfile) return;
		const confirmed = await ask(
			tr('transcode.profiles.deleteConfirm', { name: activeProfile.name }),
			{ yes: tr('transcode.profiles.delete'), danger: true }
		);
		if (!confirmed) return;
		deletingProfile = true;
		try {
			await api.del(`/transcode/profiles/${selectedId}`);
			toasts.push(tr('transcode.profiles.deleted'), 'info', { body: activeProfile.name });
			selectedId = null;
			await load();
			onchanged?.();
		} catch (e) {
			toasts.push(tr('transcode.profiles.deleteFailed'), 'fail', { body: message(e) });
		} finally {
			deletingProfile = false;
		}
	}

	async function assignFolder(folder: LibraryAssignment, value: string) {
		if (!canSettings || savingFolderId !== null) return;
		const payload = parseFolderAssignmentValue(value);
		savingFolderId = folder.library_folder_id;
		try {
			const updated = await api.put<LibraryAssignment>(
				`/transcode/library-folders/${folder.library_folder_id}`,
				payload
			);
			libraryFolders = libraryFolders.map((f) =>
				f.library_folder_id === folder.library_folder_id ? updated : f
			);
			toasts.push(tr('transcode.folders.saved'), 'ok', { body: folder.label || folder.path });
			onchanged?.();
		} catch (e) {
			toasts.push(tr('transcode.folders.saveFailed'), 'fail', { body: message(e) });
		} finally {
			savingFolderId = null;
		}
	}
</script>

<div class="profiles-wrap" data-testid="transcode-profiles-section">
	{#if error && profiles.length === 0 && libraryFolders.length === 0}
		<LoadError hasData={false} message={plainReason(error).text} onretry={() => void load()} />
	{:else}
		<PageLoader loading={!loaded} label={tr('transcode.profiles.loading')}>
			<SettingsCard
				title={tr('transcode.folders.title')}
				description={tr('transcode.folders.description')}
				icon="folder"
			>
				{#if libraryFolders.length === 0}
					<EmptyState compact icon="folder" title={tr('transcode.folders.none')} />
				{:else}
					<ul class="folder-list" data-testid="transcode-folder-assignments">
						{#each libraryFolders as f (f.library_folder_id)}
							<li class="folder-row" data-testid="transcode-folder-row">
								<div class="folder-meta">
									<div class="folder-title">
										<span class="folder-name">{f.label || f.path}</span>
										<span class="pill">{f.kind === 'show' ? tr('transcode.folders.show') : tr('transcode.folders.movie')}</span>
										{#if !f.enabled}<span class="pill wait">{tr('transcode.folders.disabledShort')}</span>{/if}
									</div>
									<span class="tp-note mono">{f.path}</span>
								</div>
								<div class="folder-pick">
									<select
										class="input"
										aria-label={tr('transcode.folders.selectFor', { name: f.label || f.path })}
										value={folderAssignmentValue(f)}
										disabled={!canSettings || savingFolderId === f.library_folder_id}
										onchange={(e) => void assignFolder(f, (e.currentTarget as HTMLSelectElement).value)}
										data-testid="transcode-folder-select"
									>
										<option value="default">
											{tr('transcode.folders.useDefault', { name: defaultProfile?.name ?? 'Default' })}
										</option>
										{#each profiles as p (p.id)}
											<option value={String(p.id)}>
												{p.name}{#if p.is_default} ({tr('transcode.profiles.defaultBadge')}){/if}
											</option>
										{/each}
										<option value="disabled">{tr('transcode.folders.disabledOption')}</option>
									</select>
								</div>
							</li>
						{/each}
					</ul>
				{/if}
			</SettingsCard>

			<SettingsCard
				title={tr('transcode.profiles.title')}
				description={tr('transcode.profiles.description')}
				icon="sliders"
			>
				<div class="profile-bar">
					<div class="profile-pills" role="group" aria-label={tr('transcode.profiles.title')}>
						{#each profiles as p (p.id)}
							<button
								type="button"
								class="prof-pill"
								class:active={selectedId === p.id}
								onclick={() => pickProfile(p)}
								data-testid="transcode-profile-pill"
							>
								<span>{p.name}</span>
								{#if p.is_default}
									<span class="def-tag">{tr('transcode.profiles.defaultBadge')}</span>
								{/if}
							</button>
						{/each}
					</div>
					{#if canSettings}
						<Button
							variant="secondary"
							size="sm"
							icon="plus"
							onclick={startNewProfile}
							data-testid="transcode-new-profile"
						>{tr('transcode.profiles.new')}</Button>
					{/if}
				</div>

				{#if draft}
					<form class="prof-form" onsubmit={saveProfile}>
						<div class="two-col">
							<Field id="prof-name" label={tr('transcode.profiles.name')} required>
								<input
									id="prof-name"
									class="input"
									type="text"
									bind:value={draft.name}
									disabled={!canSettings}
									data-testid="transcode-profile-name"
								/>
							</Field>
							<Field id="prof-desc" label={tr('transcode.profiles.descLabel')}>
								<input
									id="prof-desc"
									class="input"
									type="text"
									bind:value={draft.description}
									disabled={!canSettings}
								/>
							</Field>
						</div>

						<label class="switch multi">
							<input
								type="checkbox"
								bind:checked={draft.is_default}
								disabled={!canSettings || (activeProfile?.is_default ?? false)}
							/>
							<span class="sw" aria-hidden="true"></span>
							<span>
								<span class="label">{tr('transcode.profiles.isDefault')}</span>
								<span class="hint block">{tr('transcode.profiles.isDefaultHint')}</span>
							</span>
						</label>

						<div class="prof-group">
							<h3 class="group-title">{tr('transcode.profiles.videoGroup')}</h3>
							<div class="three-col">
								<Field id="prof-codec" label={tr('transcode.profiles.targetCodec')}>
									<select
										id="prof-codec"
										class="input"
										bind:value={draft.target_codec}
										onchange={onCodecChange}
										disabled={!canSettings}
									>
										<option value="av1">AV1</option>
										<option value="hevc">HEVC (H.265)</option>
										<option value="h264">H.264</option>
									</select>
								</Field>
								<Field id="prof-encoder" label={tr('transcode.settings.preferredEncoder')}>
									<select id="prof-encoder" class="input" bind:value={draft.preferred_encoder} disabled={!canSettings}>
										{#each encoderChoices as enc (enc)}
											<option value={enc}>{enc}</option>
										{/each}
									</select>
								</Field>
								<Field id="prof-maxh" label={tr('transcode.profiles.maxHeight')}>
									<select id="prof-maxh" class="input" bind:value={draft.max_height} disabled={!canSettings}>
										<option value={0}>{tr('transcode.profiles.heightOriginal')}</option>
										<option value={720}>720p</option>
										<option value={1080}>1080p</option>
										<option value={2160}>2160p (4K)</option>
									</select>
								</Field>
							</div>

							<div class="three-col">
								<Field id="prof-scale" label={tr('transcode.profiles.scaleMode')}>
									<select id="prof-scale" class="input" bind:value={draft.scale_mode} disabled={!canSettings}>
										<option value="normalize">{tr('transcode.profiles.scaleNormalize')}</option>
										<option value="downscale_only">{tr('transcode.profiles.scaleDownscale')}</option>
										<option value="none">{tr('transcode.profiles.scaleNone')}</option>
									</select>
								</Field>
								<Field id="prof-hdr" label={tr('transcode.settings.hdrMode')}>
									<select id="prof-hdr" class="input" bind:value={draft.hdr_mode} disabled={!canSettings}>
										<option value="preserve">{tr('transcode.profiles.hdrPreserve')}</option>
										<option value="tonemap_sdr">{tr('transcode.profiles.hdrTonemap')}</option>
									</select>
								</Field>
								<Field id="prof-hw-preset" label={tr('transcode.settings.hwPreset')}>
									<select id="prof-hw-preset" class="input" bind:value={draft.hw_preset} disabled={!canSettings} data-testid="transcode-profile-hw-preset">
										{#each HW_PRESETS as preset (preset)}
											<option value={preset}>{tr(`transcode.hwPreset.${preset}`)}</option>
										{/each}
									</select>
								</Field>
							</div>

							<div class="switch-grid">
								<label class="switch">
									<input type="checkbox" bind:checked={draft.ten_bit} disabled={!canSettings} />
									<span class="sw" aria-hidden="true"></span>
									<span class="label">{tr('transcode.profiles.tenBit')}</span>
								</label>
								<label class="switch">
									<input type="checkbox" bind:checked={draft.pillarbox_4_3} disabled={!canSettings} />
									<span class="sw" aria-hidden="true"></span>
									<span class="label">{tr('transcode.settings.pillarbox43')}</span>
								</label>
							</div>
						</div>

						<div class="prof-group">
							<h3 class="group-title">{tr('transcode.profiles.qualityGroup')}</h3>
							<label class="switch multi">
								<input type="checkbox" bind:checked={draft.vmaf_enabled} disabled={!canSettings} />
								<span class="sw" aria-hidden="true"></span>
								<span>
									<span class="label">{tr('transcode.settings.vmafEnabled')}</span>
									<span class="hint block">{tr('transcode.settings.vmafEnabledHint')}</span>
								</span>
							</label>

							<div class="three-col">
								<Field id="prof-vmaf" label={tr('transcode.settings.vmafMinScore')}>
									<input id="prof-vmaf" class="input" type="number" min="50" max="100" step="0.5" bind:value={draft.vmaf_min_score} disabled={!canSettings || !draft.vmaf_enabled} />
								</Field>
								<Field id="prof-qmin" label={tr('transcode.settings.qMin')}>
									<input id="prof-qmin" class="input" type="number" min="1" max="63" step="1" bind:value={draft.q_min} disabled={!canSettings} />
								</Field>
								<Field id="prof-qmax" label={tr('transcode.settings.qMax')}>
									<input id="prof-qmax" class="input" type="number" min="1" max="63" step="1" bind:value={draft.q_max} disabled={!canSettings} />
								</Field>
							</div>

							<div class="three-col">
								<Field id="prof-samples" label={tr('transcode.settings.vmafSamples')}>
									<input id="prof-samples" class="input" type="number" min="1" max="12" step="1" bind:value={draft.vmaf_samples} disabled={!canSettings} />
								</Field>
								<Field id="prof-secs" label={tr('transcode.settings.vmafSampleSeconds')}>
									<input id="prof-secs" class="input" type="number" min="3" max="120" step="1" bind:value={draft.vmaf_sample_seconds} disabled={!canSettings} />
								</Field>
								<Field id="prof-fps" label={tr('transcode.settings.vmafFps')}>
									<input id="prof-fps" class="input" type="number" min="1" max="60" step="1" bind:value={draft.vmaf_fps} disabled={!canSettings} />
								</Field>
							</div>

							<div class="four-col">
								<Field id="prof-iter" label={tr('transcode.settings.maxIterations')}>
									<input id="prof-iter" class="input" type="number" min="1" max="12" step="1" bind:value={draft.max_iterations} disabled={!canSettings} />
								</Field>
								<Field id="prof-maxbr" label={tr('transcode.settings.maxBitrateKbps')}>
									<input id="prof-maxbr" class="input" type="number" min="100" max="100000" step="100" bind:value={draft.max_bitrate_kbps} disabled={!canSettings} />
								</Field>
								<Field id="prof-preset" label={tr('transcode.profiles.cpuPreset')}>
									<input id="prof-preset" class="input" type="number" min="0" max="13" step="1" bind:value={draft.cpu_preset} disabled={!canSettings} />
								</Field>
								<Field id="prof-crf" label={tr('transcode.profiles.cpuCrf')}>
									<input id="prof-crf" class="input" type="number" min="0" max="63" step="1" bind:value={draft.cpu_crf} disabled={!canSettings} />
								</Field>
							</div>

							<label class="switch multi">
								<input type="checkbox" bind:checked={draft.copy_if_larger} disabled={!canSettings} />
								<span class="sw" aria-hidden="true"></span>
								<span>
									<span class="label">{tr('transcode.settings.copyIfLarger')}</span>
									<span class="hint block">{tr('transcode.settings.copyIfLargerHint')}</span>
								</span>
							</label>
						</div>

						<div class="prof-group">
							<h3 class="group-title">{tr('transcode.profiles.audioFilesGroup')}</h3>
							<div class="four-col">
								<Field id="prof-acodec" label={tr('transcode.profiles.audioCodec')}>
									<select id="prof-acodec" class="input" bind:value={draft.audio_codec} disabled={!canSettings}>
										<option value="aac">AAC</option>
										<option value="opus">Opus</option>
										<option value="copy">{tr('transcode.profiles.audioCopy')}</option>
									</select>
								</Field>
								<Field id="prof-ach" label={tr('transcode.settings.audioChannels')}>
									<input id="prof-ach" class="input" type="number" min="1" max="8" step="1" bind:value={draft.audio_channels} disabled={!canSettings} />
								</Field>
								<Field id="prof-abr" label={tr('transcode.settings.audioBitrateKbps')}>
									<input id="prof-abr" class="input" type="number" min="32" max="1536" step="16" bind:value={draft.audio_bitrate_kbps} disabled={!canSettings} />
								</Field>
								<Field id="prof-minmb" label={tr('transcode.settings.minFileSizeMb')}>
									<input id="prof-minmb" class="input" type="number" min="0" step="1" bind:value={draft.min_file_size_mb} disabled={!canSettings} />
								</Field>
							</div>

							<div class="field">
								<label for="prof-lang-input">{tr('transcode.settings.allowedLanguages')}</label>
								{#if draft.allowed_languages.length > 0}
									<div class="chips">
										{#each draft.allowed_languages as code (code)}
											<Chip label={code} onremove={canSettings ? () => removeLang(code) : undefined} />
										{/each}
									</div>
								{/if}
								<div class="lang-entry">
									<input
										id="prof-lang-input"
										class="input mono"
										type="text"
										bind:value={langDraft}
										onkeydown={onLangKey}
										disabled={!canSettings}
										placeholder="eng, jpn, und"
									/>
									<Button variant="secondary" size="md" onclick={addLang} disabled={!canSettings || !langDraft.trim()}>
										{tr('transcode.settings.addLanguage')}
									</Button>
								</div>
								<span class="hint">{tr('transcode.settings.allowedLanguagesHint')}</span>
							</div>

							<TranscodeTagFields
								bind:this={tagFields}
								bind:skipTags={draft.skip_tags}
								bind:writeTag={draft.write_tag}
								disabled={!canSettings}
								idPrefix="prof"
								testidPrefix="transcode-profile"
							/>

							<div class="switch-grid">
								<label class="switch">
									<input type="checkbox" bind:checked={draft.prefer_vfq} disabled={!canSettings} />
									<span class="sw" aria-hidden="true"></span>
									<span class="label">{tr('transcode.settings.preferVfq')}</span>
								</label>
								<label class="switch">
									<input type="checkbox" bind:checked={draft.keep_anime_attachments} disabled={!canSettings} />
									<span class="sw" aria-hidden="true"></span>
									<span class="label">{tr('transcode.settings.keepAttachments')}</span>
								</label>
								<label class="switch">
									<input type="checkbox" bind:checked={draft.apply_edl_cuts} disabled={!canSettings} />
									<span class="sw" aria-hidden="true"></span>
									<span class="label">{tr('transcode.settings.applyEdl')}</span>
								</label>
								<label class="switch">
									<input type="checkbox" bind:checked={draft.require_audio_stream} disabled={!canSettings} />
									<span class="sw" aria-hidden="true"></span>
									<span class="label">{tr('transcode.settings.requireAudio')}</span>
								</label>
								<label class="switch">
									<input type="checkbox" bind:checked={draft.keep_original_in_holding} disabled={!canSettings} />
									<span class="sw" aria-hidden="true"></span>
									<span class="label">{tr('transcode.settings.keepHolding')}</span>
								</label>
							</div>
						</div>

						{#if profileError}
							<p class="form-error" role="alert">{profileError}</p>
						{/if}

						{#if canSettings}
							<div class="prof-actions">
								<Button
									variant="destructive"
									size="sm"
									icon="trash"
									busy={deletingProfile}
									disabled={!canDelete}
									onclick={deleteProfile}
									data-testid="transcode-delete-profile"
								>{tr('transcode.profiles.delete')}</Button>
								<span class="spacer"></span>
								<Button
									variant="secondary"
									size="sm"
									icon="copy"
									onclick={duplicateCurrent}
									data-testid="transcode-duplicate-profile"
								>{tr('transcode.profiles.duplicate')}</Button>
								<Button
									variant="primary"
									size="sm"
									type="submit"
									busy={savingProfile}
									data-testid="transcode-save-profile"
								>{tr('transcode.profiles.save')}</Button>
							</div>
						{/if}
					</form>
				{/if}
			</SettingsCard>
		</PageLoader>
	{/if}
</div>

<style>
	.profiles-wrap { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
	.folder-list { display: flex; flex-direction: column; gap: 10px; }
	.folder-row {
		display: flex; align-items: center; justify-content: space-between; gap: 12px 16px;
		padding: 12px 14px; border-radius: var(--r-btn); background: var(--surface-2); border: 1px solid var(--line);
		min-width: 0;
	}
	.folder-meta { display: flex; flex-direction: column; gap: 3px; min-width: 0; flex: 1 1 auto; }
	.folder-meta .tp-note { min-width: 0; overflow-wrap: anywhere; }
	.folder-title { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
	.folder-name { font: 600 14px/1.3 var(--font-ui); color: var(--text); overflow-wrap: anywhere; }
	.folder-pick { flex: none; min-width: 220px; max-width: 280px; }
	.pill { display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 999px; font: 600 11.5px/1 var(--font-ui); background: color-mix(in srgb, var(--text) 8%, transparent); color: color-mix(in srgb, var(--text) 82%, var(--bg)); }
	.pill.wait { background: color-mix(in srgb, var(--wait, var(--accent)) 16%, transparent); color: var(--wait-text, var(--wait, var(--text))); }
	.folder-row .pill:not(.wait) { color: var(--text); }

	.profile-bar { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 10px; padding-bottom: 12px; border-bottom: 1px solid var(--line); }
	.profile-pills { display: flex; flex-wrap: wrap; gap: 6px; }
	.prof-pill {
		display: inline-flex; align-items: center; gap: 6px; min-height: 34px; padding: 0 12px;
		border-radius: 999px; background: var(--surface-2); border: 1px solid var(--line);
		color: var(--text); font: 500 13px/1 var(--font-ui); cursor: pointer;
		transition: background-color var(--dur-fast) var(--ease-out), border-color var(--dur-fast) var(--ease-out);
	}
	.prof-pill:hover { background: var(--surface-3); }
	.prof-pill.active {
		background: var(--seg-thumb, var(--surface-3));
		border-color: var(--line-strong);
		box-shadow: 0 1px 3px rgb(0 0 0 / 0.14);
		color: var(--text);
		font-weight: 600;
	}
	.def-tag { font: 600 10.5px/1 var(--font-ui); padding: 3px 6px; border-radius: 999px; background: color-mix(in srgb, var(--text) 10%, transparent); color: color-mix(in srgb, var(--text) 82%, var(--bg)); }
	.prof-pill.active .def-tag { background: color-mix(in srgb, var(--text) 14%, transparent); color: var(--text); }

	.prof-form { display: flex; flex-direction: column; gap: 16px; padding-top: 4px; }
	.prof-group { display: flex; flex-direction: column; gap: 14px; padding-top: 14px; border-top: 1px solid var(--line); }
	.group-title { margin: 0; font: 600 14px/1.3 var(--font-ui); color: var(--text); }
	.two-col { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: start; gap: 14px 16px; }
	.three-col { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); align-items: start; gap: 14px 16px; }
	.four-col { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); align-items: start; gap: 14px 16px; }
	.two-col :global(.field),
	.three-col :global(.field),
	.four-col :global(.field),
	.two-col :global(.field + .field),
	.three-col :global(.field + .field),
	.four-col :global(.field + .field) { margin-top: 0; }
	.profiles-wrap :global(.input),
	.profiles-wrap :global(select.input) { height: 40px; min-height: 40px; }
	.switch-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: start; gap: 8px 16px; }
	.switch-grid .switch { align-items: center; }
	.switch.multi { align-items: flex-start; }
	.switch.multi .sw,
	.switch.multi input { margin-top: 2px; }
	.block { display: block; }
	.chips { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 4px; }
	.lang-entry { display: flex; gap: 8px; align-items: center; }
	.lang-entry .input { flex: 1; }
	.lang-entry :global(.ui-btn) { height: 40px; min-height: 40px; flex: none; }
	.prof-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding-top: 12px; border-top: 1px solid var(--line); }
	.spacer { flex: 1 1 auto; }

	@media (max-width: 899px) {
		.four-col { grid-template-columns: repeat(2, minmax(0, 1fr)); }
	}
	@media (max-width: 767px) {
		.folder-row { flex-direction: column; align-items: stretch; }
		.folder-pick { min-width: 0; max-width: none; }
		.two-col, .three-col, .switch-grid { grid-template-columns: minmax(0, 1fr); }
	}
	@media (max-width: 639px) {
		.four-col { grid-template-columns: minmax(0, 1fr); }
	}
	@media (pointer: coarse) {
		.profiles-wrap :global(.input),
		.profiles-wrap :global(select.input),
		.lang-entry :global(.ui-btn) { height: 44px; min-height: 44px; }
	}
</style>
