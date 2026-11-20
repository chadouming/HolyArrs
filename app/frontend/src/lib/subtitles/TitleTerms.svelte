<script lang="ts">
	// A title's names and terms for the transcriber: the owner's own list, one a line, and the terms gathered from the title's subtitles, each of which can be taken out.
	import { t } from '#lib/core/i18n/i18n.svelte.ts';
	import { api } from '#lib/core/api.ts';
	import { plainMessage as message } from '#lib/core/settings.ts';
	import { toasts } from '#lib/core/toasts.svelte.ts';
	import Button from '#lib/ui/Button.svelte';
	import { langName, termsBody, type Language, type TitleTerms } from './api.ts';

	let { titleId, langs, languages, id }: { titleId: number; langs: Language[]; languages: string[]; id: string } = $props();

	// The language the lists are for: the title's first wanted language at first.
	let language = $state('');
	let terms = $state<TitleTerms | null>(null);
	let ownText = $state('');
	let saved = $state('');
	let busy = $state(false);
	let error = $state('');
	const path = $derived(`/titles/${titleId}/subtitle-terms`);
	const dirty = $derived(ownText !== saved);

	function show(x: TitleTerms) {
		terms = x;
		ownText = saved = x.own.join('\n');
	}

	async function load(lang: string) {
		if (!lang) return;
		error = '';
		try {
			show(await api.get<TitleTerms>(`${path}?language=${encodeURIComponent(lang)}`));
		} catch (err) {
			error = message(err);
		}
	}

	$effect(() => {
		if (!language && languages.length) language = languages[0];
	});
	$effect(() => {
		void load(language);
	});

	async function save(removed: string[]) {
		busy = true;
		error = '';
		try {
			show(await api.put<TitleTerms>(path, termsBody(language, ownText, removed)));
			toasts.push(t('subtitles.terms.saved'), 'ok');
		} catch (err) {
			error = message(err);
		} finally {
			busy = false;
		}
	}
</script>

<details class="terms" data-testid="title-terms">
	<summary class="small"><b>{t('subtitles.terms.title')}</b> <span class="muted">{t('subtitles.terms.hint')}</span></summary>
	{#if languages.length > 1}
		<div class="field">
			<label for="{id}-terms-lang">{t('subtitles.terms.language')}</label>
			<select id="{id}-terms-lang" class="input" bind:value={language}>
				{#each languages as l (l)}<option value={l}>{langName(l, langs)}</option>{/each}
			</select>
		</div>
	{/if}
	<div class="field">
		<label for="{id}-terms-own">{t('subtitles.terms.own')}</label>
		<textarea id="{id}-terms-own" class="input" rows="4" bind:value={ownText} spellcheck="false" placeholder={t('subtitles.terms.ownHint')}></textarea>
	</div>
	{#if terms?.gathered.length}
		<p class="small muted">{t('subtitles.terms.gathered')}</p>
		<ul class="chips">
			{#each terms.gathered as g (g)}
				<li class="chip">{g} <button type="button" class="x" disabled={busy} aria-label={t('subtitles.terms.remove', { term: g })} onclick={() => save([...(terms?.removed ?? []), g])}>×</button></li>
			{/each}
		</ul>
	{:else if terms}
		<p class="small muted">{t('subtitles.terms.noneGathered')}</p>
	{/if}
	{#if terms?.removed.length}
		<p class="small muted">{t('subtitles.terms.removed', { count: terms.removed.length })} <Button variant="plain" size="sm" disabled={busy} onclick={() => save([])}>{t('subtitles.terms.restore')}</Button></p>
	{/if}
	{#if error}<p class="form-error" role="alert">{error}</p>{/if}
	<div class="row actions">
		<Button variant="primary" size="sm" disabled={busy || !dirty} busy={busy} onclick={() => save(terms?.removed ?? [])}>{t('subtitles.terms.save')}</Button>
	</div>
</details>

<style>
	.terms {
		margin-top: var(--s-3);
	}
	.terms summary {
		cursor: pointer;
	}
	.field {
		display: grid;
		gap: var(--s-1);
		margin-top: var(--s-3);
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--s-1);
		list-style: none;
		padding: 0;
		margin: var(--s-1) 0;
	}
	.x {
		border: 0;
		background: none;
		cursor: pointer;
		color: inherit;
		padding: 0 2px;
	}
</style>
