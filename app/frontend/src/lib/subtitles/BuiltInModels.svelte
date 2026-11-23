<script lang="ts">
	// The built-in translator's own settings: which model it loads (with download, cancel and delete), its graphics card and its CPU threads.
	import { onMount } from 'svelte';
	import { t } from '#lib/core/i18n/i18n.svelte.ts';
	import { api } from '#lib/core/api.ts';
	import { plainMessage as message } from '#lib/core/settings.ts';
	import Button from '#lib/ui/Button.svelte';
	import Field from '#lib/ui/Field.svelte';
	import { activeSpeechModel, modelAction, modelBadges, sizeWords, type ModelList, type ModelView, type TranslatorForm } from './api.ts';

	// kinds: which models the list shows. '' are translation models (the built-in translator's own); 'speech' and 'vad' are the transcriber's.
	let { tr = $bindable(), models = $bindable([]), speech = false, speechModel = $bindable('') }: { tr?: TranslatorForm; models?: ModelView[]; speech?: boolean; speechModel?: string } = $props();
	const mine = (m: ModelView) => (speech ? m.kind === 'speech' || m.kind === 'vad' : !m.kind);

	let list = $state<ModelList | null>(null);
	const activeSpeech = $derived(activeSpeechModel(speechModel, list?.models ?? models));
	let error = $state('');
	let busy = $state('');
	const base = '/settings/subtitles/translator/models';
	const path = (m: ModelView) => `${base}/${encodeURIComponent(m.id)}`;

	async function load() {
		try {
			list = await api.get<ModelList>(base);
			models = list.models;
			error = '';
		} catch (err) {
			error = message(err);
		}
	}

	// While a model is being fetched the list is read again every two seconds; otherwise once.
	onMount(() => {
		void load();
		const timer = setInterval(() => {
			if (list?.models.some((m) => m.state === 'downloading')) void load();
		}, 2000);
		return () => clearInterval(timer);
	});

	async function act(m: ModelView, what: 'download' | 'cancel' | 'delete') {
		busy = m.id;
		error = '';
		try {
			if (what === 'download') await api.post(`${path(m)}/download`);
			else if (what === 'cancel') await api.del(`${path(m)}/download`);
			else await api.del(path(m));
			await load();
		} catch (err) {
			error = message(err);
		} finally {
			busy = '';
		}
	}

	// The thread box is empty for the default.
	let threads = $state(tr?.threads ? String(tr.threads) : '');
	$effect(() => {
		if (!tr) return;
		const n = Number.parseInt(threads, 10);
		tr.threads = Number.isInteger(n) && n > 0 ? n : 0;
	});
</script>

<div class="models" data-testid="builtin-models">
	<div class="label">{t('settings.subtitles.trModelLabel')}</div>
	{#if list && list.models.filter(mine).length === 0}<p class="hint">{t('settings.subtitles.trModelNone')}</p>{/if}
	{#each (list?.models ?? []).filter(mine) as m (m.id)}
		{@const action = modelAction(m)}
		{@const speechInUse = speech && m.kind === 'speech' && activeSpeech === m.id}
		<div class="model" class:chosen={speech ? speechInUse : tr?.builtin_model === m.id} data-testid="builtin-model">
			<div class="what">
				<div class="title-row">
					<b>{m.name}</b>
					{#each modelBadges(m) as badge}
						<span class="badge" data-testid="model-badge">{badge}</span>
					{/each}
				</div>
				<span class="small muted">{m.own ? t('settings.subtitles.trModelOwn') : m.license} · {t('settings.subtitles.trModelNeeds', { size: sizeWords(m.size), need: sizeWords(m.need) })}{#if !m.own && m.source} · <span class="mono">{m.source}</span>{/if}</span>
				{#if !m.fits}<span class="small warn-text">{t('settings.subtitles.trModelTooBig')}</span>{/if}
				{#if m.state === 'downloading'}<progress max="1" value={m.progress} aria-label={m.name}></progress>{/if}
			</div>
			<div class="row">
				{#if action === 'download'}
					<Button size="sm" type="button" disabled={busy === m.id} busy={busy === m.id} onclick={() => act(m, 'download')} data-testid="builtin-download">{t('settings.subtitles.trModelDownload')}</Button>
				{:else if action === 'cancel'}
					<span class="small mono">{Math.round(m.progress * 100)}%</span>
					<Button size="sm" type="button" disabled={busy === m.id} onclick={() => act(m, 'cancel')}>{t('settings.subtitles.trModelCancel')}</Button>
				{:else}
					{#if speech && m.kind === 'speech'}
						{#if activeSpeech === m.id}
							<span class="chip ok">{t('settings.subtitles.trModelInUse')}</span>
						{:else}
							<Button size="sm" type="button" onclick={() => (speechModel = m.id)} data-testid="speech-use">{t('settings.subtitles.trModelUse')}</Button>
						{/if}
					{:else if !tr}
						<span class="chip ok">{t('settings.subtitles.trModelDownloaded')}</span>
					{:else if tr.builtin_model === m.id}
						<span class="chip ok">{t('settings.subtitles.trModelInUse')}</span>
					{:else}
						<Button size="sm" type="button" onclick={() => tr && (tr.builtin_model = m.id)} data-testid="builtin-use">{t('settings.subtitles.trModelUse')}</Button>
					{/if}
					<Button size="sm" type="button" variant="plain" disabled={busy === m.id || (speech ? speechInUse : action === 'inUse')} onclick={() => act(m, 'delete')}>{t('settings.subtitles.trModelDelete')}</Button>
				{/if}
			</div>
		</div>
	{/each}
	{#if error}<p class="form-error" role="alert">{error}</p>{/if}
</div>
{#if tr}
<div class="two">
	<Field id="tr-gpu" label={t('settings.subtitles.trGPU')} hint={t('settings.subtitles.trGPUHint')}>
		<select id="tr-gpu" class="input" bind:value={tr.gpu} data-testid="builtin-gpu">
			<option value="">{t('settings.subtitles.trGPUNone')}</option>
			{#if (list?.devices.length ?? 0) > 1 || tr.gpu === 'all'}<option value="all">{t('settings.subtitles.trGPUAll', { n: list?.devices.length ?? 0 })}</option>{/if}
			{#each list?.devices ?? [] as d (d.id)}<option value={d.id}>{d.name} ({d.id}, {sizeWords(d.memory_mb * 2 ** 20)})</option>{/each}
			{#if tr.gpu && tr.gpu !== 'all' && !(list?.devices ?? []).some((d) => d.id === tr.gpu)}<option value={tr.gpu}>{tr.gpu}</option>{/if}
		</select>
	</Field>
	<Field id="tr-threads" label={t('settings.subtitles.trThreads')} hint={list ? t('settings.subtitles.trThreadsHint', { n: list.threads_default, max: list.threads_max }) : ''}>
		<input id="tr-threads" class="input mono" type="number" min="1" max={list?.threads_max ?? 256} step="1" bind:value={threads} placeholder={list ? String(list.threads_default) : ''} />
	</Field>
</div>
{/if}

<style>
	.models {
		display: grid;
		gap: var(--s-2);
	}
	.label {
		font-weight: 600;
	}
	.model {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--s-3);
		padding: var(--s-3);
		background: var(--surface-2);
		border: 1px solid var(--line);
		border-radius: var(--r-btn);
		min-width: 0;
	}
	.model.chosen {
		background: var(--surface-1);
		border-color: var(--line-strong);
		box-shadow: 0 1px 3px rgb(0 0 0 / 0.08);
	}
	.what {
		display: grid;
		gap: 2px;
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.title-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 6px;
	}
	.badge {
		display: inline-flex;
		align-items: center;
		padding: 1px 7px;
		border-radius: var(--r-pill);
		background: var(--surface-1);
		border: 1px solid var(--line);
		color: var(--text-muted);
		font-size: var(--text-xs);
		font-family: var(--font-mono);
		font-weight: var(--weight-medium);
		line-height: 1.35;
	}
	.warn-text {
		color: var(--wait-text);
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--s-2);
		flex-shrink: 0;
	}
	:global(.settings-content) .chip.ok {
		color: var(--ok-text);
	}
	.two,
	:global(.settings-content :is(.ui-settings, .card)) .two:has(> :global(.field)) {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		align-items: start;
		gap: 14px 16px;
		margin-top: var(--s-4);
	}
	.two :global(.field),
	.two :global(.field + .field) {
		display: flex !important;
		flex-direction: column;
		align-items: stretch !important;
		gap: 6px;
		min-width: 0;
		margin-top: 0 !important;
		padding: 0 !important;
		border-top: 0 !important;
	}
	.two :global(.field > label:first-child) {
		padding-top: 0 !important;
	}
	.two :global(.input) {
		width: 100%;
		max-width: none;
		height: 40px;
		min-height: 40px;
	}
	progress {
		width: 100%;
	}
	@media (max-width: 639px) {
		.two,
		:global(.settings-content :is(.ui-settings, .card)) .two:has(> :global(.field)) {
			grid-template-columns: minmax(0, 1fr);
		}
		.model {
			flex-direction: column;
			align-items: stretch;
		}
		.row {
			justify-content: flex-end;
		}
	}
	@media (pointer: coarse) {
		.two :global(.input) {
			height: 44px;
			min-height: 44px;
		}
	}
</style>
