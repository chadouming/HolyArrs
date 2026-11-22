<script lang="ts">
	// The built-in translator's own settings: which model it loads (with download, cancel and delete), its graphics card and its CPU threads.
	import { onMount } from 'svelte';
	import { t } from '#lib/core/i18n/i18n.svelte.ts';
	import { api } from '#lib/core/api.ts';
	import { plainMessage as message } from '#lib/core/settings.ts';
	import Button from '#lib/ui/Button.svelte';
	import Field from '#lib/ui/Field.svelte';
	import { activeSpeechModel, modelAction, sizeWords, type ModelList, type ModelView, type TranslatorForm } from './api.ts';

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
				<b>{m.name}</b>
				<span class="small muted">{m.own ? t('settings.subtitles.trModelOwn') : m.license} · {t('settings.subtitles.trModelNeeds', { size: sizeWords(m.size), need: sizeWords(m.need) })}</span>
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
<div class="two" style="margin-top: var(--s-4)">
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
		border: 1px solid var(--border);
		border-radius: var(--r-2);
	}
	.model.chosen {
		border-color: var(--accent);
	}
	.what {
		display: grid;
		gap: 2px;
		min-width: 0;
	}
	.row {
		display: flex;
		align-items: center;
		gap: var(--s-2);
		flex-shrink: 0;
	}
	progress {
		width: 100%;
	}
</style>
