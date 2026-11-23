<script lang="ts">
	import { t } from '#lib/core/i18n/i18n.svelte.ts';
	import { SkSettings } from '#lib/ui/sk/index.ts';
	import Field from '#lib/ui/Field.svelte';
	import SettingsCard from '#lib/ui/SettingsCard.svelte';
	import MoreOptions from '#lib/ui/MoreOptions.svelte';
	import SaveBar from '#lib/ui/SaveBar.svelte';
	import Button from '#lib/ui/Button.svelte';
	import LoadError from '#lib/ui/LoadError.svelte';
	import TestTag from '#lib/ui/TestTag.svelte';
	import SubtitlesSitesSection from './SubtitlesSitesSection.svelte';
	import {
		BuiltInModels,
		LanguageWants as WishEditor,
		speechReady,
		translatorBody,
		translatorReady,
		wishProblem,
		type Language,
		type ModelView,
		type SiteKind,
		type SiteView,
		type SubtitleSettings,
		type Transcriber,
		type Translator,
		type TranslatorForm
	} from '#lib/subtitles/index.ts';
	import { onMount } from 'svelte';
	import { api } from '#lib/core/api.ts';
	import { plainMessage as message } from '#lib/core/settings.ts';
	import { toasts } from '#lib/core/toasts.svelte.ts';
	import { live } from '#lib/core/live.svelte.ts';

	let { onchanged }: { onchanged?: () => void } = $props();

	let settings = $state<SubtitleSettings | null>(null);
	let langs = $state<Language[]>([]);
	let kinds = $state<SiteKind[]>([]);
	let sites = $state<SiteView[]>([]);
	let loaded = $state(false);
	let saved = $state('');
	const dirty = $derived(!!settings && !!saved && JSON.stringify(settings) !== saved);
	let error = $state('');
	let setBusy = $state(false);
	let setError = $state('');

	// The translator saves on its own: its key never joins the page's draft.
	let tr = $state<TranslatorForm | null>(null);
	let trSaved = $state('');
	const trDirty = $derived(!!tr && !!trSaved && JSON.stringify(tr) !== trSaved);
	let trBusy = $state(false);
	let trTesting = $state(false);
	let trError = $state('');
	let trResult = $state<{ ok: boolean; message: string; models: string[] } | null>(null);
	// The built-in translator's models, kept in step by its own part of the card.
	let trModels = $state<ModelView[]>([]);
	// Whether subtitles can be made from audio here: the programs in this image, and its two models.
	let transcriber = $state<Transcriber | null>(null);
	let sttModels = $state<ModelView[]>([]);
	const sttReady = $derived(speechReady(sttModels));
	const trReady = $derived(!!tr && translatorReady(JSON.parse(trSaved || '{"kind":"","url":""}'), trModels));

	let alive = true;

	function setTranslator(x: Translator) {
		tr = { ...x, key: '', clear_key: false };
		trSaved = JSON.stringify(tr);
	}

	async function saveTranslator(e: SubmitEvent) {
		e.preventDefault();
		if (!tr) return;
		trBusy = true;
		trError = '';
		try {
			const updated = await api.put<Translator>('/settings/subtitles/translator', translatorBody(tr));
			if (!alive) return;
			setTranslator(updated);
			toasts.push(t('settings.subtitles.trSaved'), 'ok');
			if (tr.kind) await testTranslator();
			onchanged?.();
		} catch (err) {
			if (alive) trError = message(err);
		} finally {
			if (alive) trBusy = false;
		}
	}

	async function testTranslator() {
		trTesting = true;
		try {
			const res = await api.post<{ ok: boolean; message: string; models: string[] }>('/settings/subtitles/translator/test');
			if (alive) trResult = res;
		} catch (err) {
			if (alive) trResult = { ok: false, message: message(err), models: [] };
		} finally {
			if (alive) trTesting = false;
		}
	}

	async function loadSites() {
		if (!alive) return;
		try {
			const res = await api.get<{ sites: SiteView[] }>('/subtitle-sites');
			if (!alive) return;
			sites = res.sites;
		} catch (e) {
			if (alive) error = message(e);
		}
	}

	function load() {
		if (!alive) return Promise.resolve();
		error = '';
		return Promise.all([
			api.get<SubtitleSettings>('/settings/subtitles').then((s) => {
				if (!alive) return;
				settings = s;
				saved = JSON.stringify(s);
			}),
			api.get<{ languages: Language[] }>('/subtitles/languages').then((r) => {
				if (alive) langs = r.languages;
			}),
			api.get<Translator>('/settings/subtitles/translator').then((x) => {
				if (alive) setTranslator(x);
			}),
			api.get<Transcriber>('/settings/subtitles/transcriber').then(
				(x) => {
					if (alive) transcriber = x;
				},
				() => {}
			),
			api.get<{ kinds: SiteKind[] }>('/subtitle-sites/kinds').then((r) => {
				if (alive) kinds = r.kinds;
			}),
			loadSites()
		])
			.catch((e) => {
				if (alive) error = message(e);
			})
			.finally(() => {
				if (alive) loaded = true;
			});
	}

	onMount(() => {
		alive = true;
		void load();
		const offs = [
			live.on('event', (d) => {
				if (String((d as { Kind?: string })?.Kind) === 'subtitle_site_paused') void loadSites();
			}),
			live.on('resync', () => {
				void loadSites();
			})
		];
		return () => {
			alive = false;
			offs.forEach((off) => off());
		};
	});

	async function saveSettings(e: SubmitEvent) {
		e.preventDefault();
		if (!settings) return;
		setError = wishProblem(settings.languages);
		if (setError) return;
		setBusy = true;
		try {
			const body = { ...settings };
			for (const k of ['pass_mark_movie', 'pass_mark_episode', 'search_hours', 'better_hours', 'better_days', 'batch', 'sync_from_fps', 'sync_to_fps', 'sync_step_ms'] as const) body[k] = Number(body[k]);
			const updated = await api.put<SubtitleSettings>('/settings/subtitles', body);
			if (!alive) return;
			settings = updated;
			saved = JSON.stringify(updated);
			toasts.push(t('settings.subtitles.saved'), 'ok');
			onchanged?.();
		} catch (err) {
			if (alive) setError = message(err);
		} finally {
			if (alive) setBusy = false;
		}
	}
</script>

<div class="sub-settings" data-testid="subtitles-settings-tab">
	{#if !loaded}
		<SkSettings rows={2} />
	{:else if error && !settings}
		<LoadError hasData={false} message={error} onretry={load} />
	{:else if settings}
		{#if error}<LoadError hasData message={error} onretry={load} />{/if}
		<form class="sc-form" id="subtitle-form" onsubmit={saveSettings} aria-label={t('settings.subtitles.defaultLangs')}>
			<SettingsCard id="lang-card" title={t('settings.subtitles.defaultLangs')} description={t('settings.subtitles.defaultLangsDesc')}>
				<WishEditor bind:wants={settings.languages} {langs} id="def" translate={trReady} />
				{#if settings.languages.length}<p class="xs muted hint-block">
					<b>{t('settings.subtitles.hiLabel')}</b> {t('settings.subtitles.hiText')}
					<b>{t('settings.subtitles.forcedLabel')}</b> {t('settings.subtitles.forcedText')}
				</p>{/if}

				{#snippet advanced()}
					<div class="two">
						<Field id="pm-ep" label={t('settings.subtitles.passEpisodes')} hint={t('settings.subtitles.passHint')}>
							<input id="pm-ep" class="input narrow" type="number" min="0" max="100" bind:value={settings!.pass_mark_episode} />
						</Field>
						<Field id="pm-mov" label={t('settings.subtitles.passMovies')} hint={t('settings.subtitles.passHint')}>
							<input id="pm-mov" class="input narrow" type="number" min="0" max="100" bind:value={settings!.pass_mark_movie} />
						</Field>
					</div>
					<details class="more" data-testid="pass-mark-help">
						<summary>{t('settings.subtitles.passMeans')}</summary>
						<p class="small">
							{t('settings.subtitles.pass1')}
						</p>
						<p class="small">
							{t('settings.subtitles.pass2')}
						</p>
					</details>
				{/snippet}
			</SettingsCard>
			{#if setError}<p class="form-error" role="alert">{setError}</p>{/if}
		</form>
		{#if tr}
			<form class="sc-form" onsubmit={saveTranslator} aria-label={t('settings.subtitles.translate')}>
				<SettingsCard id="translator-card" title={t('settings.subtitles.translate')} description={t('settings.subtitles.translateDesc')}>
					<Field id="tr-kind" label={t('settings.subtitles.translator')} hint={tr.kind === 'openai' ? t('settings.subtitles.trOpenAIHint') : tr.kind === 'lingarr' ? t('settings.subtitles.trLingarrHint') : tr.kind === 'builtin' ? t('settings.subtitles.trBuiltInKindHint') : !tr.builtin_available ? t('settings.subtitles.trNoRunner') : ''}>
						<select id="tr-kind" class="input" bind:value={tr.kind} data-testid="translator-kind">
							<option value="">{t('settings.subtitles.trOff')}</option>
							<option value="openai">{t('settings.subtitles.trOpenAI')}</option>
							<option value="lingarr">Lingarr</option>
							<option value="builtin" disabled={!tr.builtin_available && tr.kind !== 'builtin'}>{t('settings.subtitles.trBuiltInKind')}</option>
						</select>
					</Field>
					{#if tr.kind === 'builtin'}
						{#if !tr.builtin_available}<p class="form-error" role="alert">{t('settings.subtitles.trNoRunner')}</p>{/if}
						<BuiltInModels bind:tr bind:models={trModels} />
						<label class="checkline">
							<input type="checkbox" bind:checked={tr.built_in_auto} data-testid="translator-built-in" />
							<span><b>{t('settings.subtitles.trBuiltIn')}</b><br /><span class="hint">{t('settings.subtitles.trBuiltInHint')}</span></span>
						</label>
					{:else if tr.kind}
						<div class="two">
							<Field id="tr-url" label={t('settings.subtitles.address')}>
								<input id="tr-url" class="input mono" bind:value={tr.url} autocomplete="off" spellcheck="false" placeholder={tr.kind === 'openai' ? 'http://192.168.1.20:11434/v1' : 'http://192.168.1.20:9876'} required />
							</Field>
							{#if tr.kind === 'openai'}
								<Field id="tr-model" label={t('settings.subtitles.trModel')}>
									<input id="tr-model" class="input mono" bind:value={tr.model} list="tr-models" autocomplete="off" spellcheck="false" required />
									<datalist id="tr-models">{#each trResult?.models ?? [] as m (m)}<option value={m}></option>{/each}</datalist>
								</Field>
							{/if}
						</div>
						<Field id="tr-key" label={t('settings.subtitles.trKey')} hint={tr.has_key ? t('settings.subtitles.trKeyKept') : t('settings.subtitles.trKeyHint')}>
							<input id="tr-key" class="input mono" type="password" bind:value={tr.key} autocomplete="new-password" spellcheck="false" placeholder={tr.has_key ? '••••••••' : t('settings.subtitles.trKeyEmpty')} />
						</Field>
						{#if tr.has_key && !tr.key}
							<label class="checkline">
								<input type="checkbox" bind:checked={tr.clear_key} />
								<span><b>{t('settings.subtitles.trClearKey')}</b></span>
							</label>
						{/if}
						<label class="checkline">
							<input type="checkbox" bind:checked={tr.built_in_auto} data-testid="translator-built-in" />
							<span><b>{t('settings.subtitles.trBuiltIn')}</b><br /><span class="hint">{t('settings.subtitles.trBuiltInHint')}</span></span>
						</label>
					{/if}
					{#if trResult && !trDirty}
						<div class="testline" data-testid="translator-test"><TestTag ok={trResult.ok} text={trResult.message} />{#if !trResult.ok}<span class="small bad-text">{trResult.message}</span>{/if}</div>
					{/if}
					{#if trError}<p class="form-error" role="alert">{trError}</p>{/if}
					{#if tr.kind || trDirty}<div class="row actions">
						<Button variant="primary" type="submit" disabled={trBusy || !trDirty} busy={trBusy}>{t('settings.subtitles.save')}</Button>
						{#if tr.kind}<Button type="button" disabled={trTesting || trDirty} busy={trTesting} onclick={testTranslator} data-testid="translator-test-button">{t('settings.subtitles.test')}</Button>{/if}
					</div>{/if}
				</SettingsCard>
			</form>
		{/if}
		{#if settings && transcriber}
			<SettingsCard id="transcriber-card" title={t('settings.subtitles.stt')} description={t('settings.subtitles.sttDesc')}>
				{#if !transcriber.available}
					<p class="hint" data-testid="stt-no-programs">{t('settings.subtitles.sttNoPrograms')}</p>
				{:else}
					<label class="checkline">
						<input type="checkbox" form="subtitle-form" bind:checked={settings.transcribe_missing} data-testid="transcribe-missing" />
						<span><b>{t('settings.subtitles.sttSwitch')}</b><br /><span class="hint">{t('settings.subtitles.sttSwitchHint')}</span></span>
					</label>
					<Field id="stt-at-once" label={t('settings.subtitles.sttAtOnce')} hint={t('settings.subtitles.sttAtOnceHint')}>
						<select id="stt-at-once" class="input" style="max-width: 120px" form="subtitle-form" bind:value={settings.transcribe_at_once} data-testid="transcribe-at-once">
							{#each [1, 2, 3] as n (n)}<option value={n}>{n}</option>{/each}
						</select>
					</Field>
					<label class="checkline">
						<input type="checkbox" form="subtitle-form" bind:checked={settings.transcribe_fast} data-testid="transcribe-fast" />
						<span><b>{t('settings.subtitles.sttFast')}</b><br /><span class="hint">{t('settings.subtitles.sttFastHint')}</span></span>
					</label>
					<BuiltInModels speech bind:models={sttModels} bind:speechModel={settings.transcribe_model} />
					<p class="small" data-testid="transcriber-state">{sttReady ? t('settings.subtitles.sttReady') : t('settings.subtitles.sttNeedsModels')}</p>
				{/if}
			</SettingsCard>
		{/if}

		<SubtitlesSitesSection {sites} {kinds} onreload={loadSites} {onchanged} />

		<div class="sc-form sub-more">
			<SettingsCard id="when-to-look" title={t('settings.subtitles.whenToLook')} description={t('settings.subtitles.whenToLookDesc')}>
				<label class="checkline">
					<input id="better-subs" form="subtitle-form" type="checkbox" bind:checked={settings.better} />
					<span><b>{t('settings.subtitles.better')}</b><br /><span class="hint">{t('settings.subtitles.betterHint')}</span></span>
				</label>
				{#snippet advanced()}
					<div class="two">
						<Field id="s-hours" label={t('settings.subtitles.searchHours')}>
							<input id="s-hours" form="subtitle-form" class="input narrow" type="number" min="1" max="168" bind:value={settings!.search_hours} />
						</Field>
						<Field id="s-batch" label={t('settings.subtitles.batch')} hint={t('settings.subtitles.batchHint')}>
							<input id="s-batch" form="subtitle-form" class="input narrow" type="number" min="1" max="1000" bind:value={settings!.batch} />
						</Field>
					</div>
					<label class="checkline">
						<input id="slow-down" form="subtitle-form" type="checkbox" bind:checked={settings!.adaptive} />
						<span><b>{t('settings.subtitles.slowDown')}</b><br /><span class="hint">{t('settings.subtitles.slowDownHint')}</span></span>
					</label>
					{#if settings!.better}
						<div class="two">
							<Field id="b-hours" label={t('settings.subtitles.everyHours')}>
								<input id="b-hours" form="subtitle-form" class="input narrow" type="number" min="6" max="168" bind:value={settings!.better_hours} />
							</Field>
							<Field id="b-days" label={t('settings.subtitles.lastDays')}>
								<input id="b-days" form="subtitle-form" class="input narrow" type="number" min="0" max="30" bind:value={settings!.better_days} />
							</Field>
						</div>
					{/if}
				{/snippet}
			</SettingsCard>
		</div>
		<MoreOptions>
			<div class="sc-form">
				<SettingsCard id="inside-video" title={t('settings.subtitles.inside')}>
					<label class="checkline">
						<input id="embedded" form="subtitle-form" type="checkbox" bind:checked={settings.use_built_in} />
						<span><b>{t('settings.subtitles.countInside')}</b><br /><span class="hint">{t('settings.subtitles.countInsideHint')}</span></span>
					</label>
					{#snippet advanced()}
						<label class="checkline">
							<input id="no-picture" form="subtitle-form" type="checkbox" bind:checked={settings!.ignore_picture_tracks} disabled={!settings!.use_built_in} />
							<span><b>{t('settings.subtitles.noPicture')}</b><br /><span class="hint">{t('settings.subtitles.noPictureHint')}</span></span>
						</label>
						<Field id="hi-suffix" label={t('settings.subtitles.hiSuffix')} hint={t('settings.subtitles.hiSuffixHint')}>
							<select id="hi-suffix" form="subtitle-form" class="input narrow" bind:value={settings!.hi_suffix}>
								<option value="hi">.hi (Movie.en.hi.srt)</option>
								<option value="cc">.cc (Movie.en.cc.srt)</option>
								<option value="sdh">.sdh (Movie.en.sdh.srt)</option>
							</select>
						</Field>
					{/snippet}
				</SettingsCard>
				<SettingsCard id="sync-fix" title={t('settings.subtitles.syncFix')} description={t('settings.subtitles.syncFixDesc')}>
					<p class="small muted">
						{t('settings.subtitles.syncNote')}
					</p>
					{#snippet advanced()}
						<div class="three">
							<Field id="sy-step" label={t('settings.subtitles.shift')}>
								<input id="sy-step" form="subtitle-form" class="input narrow" type="number" min="10" max="60000" bind:value={settings!.sync_step_ms} />
							</Field>
							<Field id="sy-from" label={t('settings.subtitles.fpsFrom')}>
								<input id="sy-from" form="subtitle-form" class="input narrow" type="number" min="1" max="120" step="0.001" bind:value={settings!.sync_from_fps} />
							</Field>
							<Field id="sy-to" label={t('settings.subtitles.fpsTo')}>
								<input id="sy-to" form="subtitle-form" class="input narrow" type="number" min="1" max="120" step="0.001" bind:value={settings!.sync_to_fps} />
							</Field>
						</div>
					{/snippet}
				</SettingsCard>
			</div>
		</MoreOptions>
		<SaveBar {dirty} form="subtitle-form" saving={setBusy} ondiscard={() => (settings = JSON.parse(saved))} />
	{/if}
</div>

<style>
	.sub-settings {
		display: flex;
		flex-direction: column;
		gap: 16px;
		min-width: 0;
		container: sub-settings / inline-size;
	}
	.sub-settings:has(:global([data-testid='savebar'])) {
		padding-bottom: 96px;
	}
	:global(.settings-content) .sub-settings:has(:global([data-testid='savebar'])) {
		padding-bottom: 0;
	}
	.sub-settings .sc-form {
		display: flex;
		flex-direction: column;
		gap: 16px;
	}
	.testline { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; }
	.hint-block {
		margin-top: var(--s-3);
	}
	.two,
	:global(.settings-content :is(.ui-settings, .card)) .two:has(> :global(.field)) {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		align-items: start;
		gap: 14px 16px;
		margin-top: var(--s-4);
	}
	.three,
	:global(.settings-content :is(.ui-settings, .card)) .three:has(> :global(.field)) {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		align-items: start;
		gap: 14px 16px;
		margin-top: var(--s-4);
	}
	.two :global(.field),
	.two :global(.field + .field),
	.three :global(.field),
	.three :global(.field + .field) {
		display: flex !important;
		flex-direction: column;
		align-items: stretch !important;
		gap: 6px;
		min-width: 0;
		margin-top: 0 !important;
		padding: 0 !important;
		border-top: 0 !important;
	}
	.two :global(.field > label:first-child),
	.three :global(.field > label:first-child) {
		padding-top: 0 !important;
	}
	.two :global(.input),
	.three :global(.input) {
		width: 100%;
		max-width: none;
	}
	.sub-settings .sc-form :global(:is(input.input:not([type='checkbox'], [type='radio']), select.input)) {
		height: 40px;
		min-height: 40px;
	}
	.sub-settings :global(.checkline:not(.box)) {
		justify-content: space-between;
		flex-direction: row-reverse;
		align-items: center;
		gap: 16px;
		border-top: 1px solid var(--line);
	}
	.sub-settings :global(.checkline:not(.box):first-child) {
		border-top: 0;
	}
	.sub-settings :global(.checkline:not(.box) > :not(input)) {
		flex: 1;
		min-width: 0;
	}
	.sub-settings :global(.checkline:not(.box) > input[type='checkbox']) {
		appearance: none;
		flex: none;
		width: 40px;
		height: 24px;
		margin: 0;
		border-radius: var(--r-pill);
		background: radial-gradient(circle at 12px 50%, #fff 0 8.5px, transparent 9.5px), var(--line-strong);
		cursor: pointer;
		transition: background var(--dur) var(--ease-out);
	}
	.sub-settings :global(.checkline:not(.box) > input[type='checkbox']:checked) {
		background: radial-gradient(circle at 28px 50%, #fff 0 8.5px, transparent 9.5px), var(--ha-red-press);
	}
	:global([dir='rtl']) .sub-settings :global(.checkline:not(.box) > input[type='checkbox']) {
		background: radial-gradient(circle at 28px 50%, #fff 0 8.5px, transparent 9.5px), var(--line-strong);
	}
	:global([dir='rtl']) .sub-settings :global(.checkline:not(.box) > input[type='checkbox']:checked) {
		background: radial-gradient(circle at 12px 50%, #fff 0 8.5px, transparent 9.5px), var(--ha-red-press);
	}
	.sub-settings :global(.checkline:not(.box) > input[type='checkbox']:disabled) {
		opacity: 0.45;
		cursor: not-allowed;
	}
	.sub-settings :global(.checkline:not(.box) > input[type='checkbox']:focus-visible) {
		outline: none;
		box-shadow: var(--focus-ring);
	}
	@media (prefers-reduced-motion: reduce) {
		.sub-settings :global(.checkline:not(.box) > input[type='checkbox']) {
			transition: none;
		}
	}
	.sub-settings .checkline:not(.box) {
		align-items: flex-start;
		padding-block: 10px;
	}
	.sub-settings .checkline:not(.box) > input[type='checkbox'] {
		margin-top: 2px;
	}
	.narrow {
		max-width: 220px;
	}
	.more {
		margin-top: var(--s-4);
	}
	.more summary {
		cursor: pointer;
		font-weight: var(--weight-semibold);
	}
	.more p {
		margin-top: var(--s-2);
	}
	.actions {
		margin-top: var(--s-3);
		flex-wrap: wrap;
	}
	.bad-text {
		color: var(--fail-text);
		min-width: 0;
		overflow-wrap: anywhere;
	}
	@container sub-settings (max-width: 600px) {
		.two,
		.three,
		:global(.settings-content :is(.ui-settings, .card)) .two:has(> :global(.field)),
		:global(.settings-content :is(.ui-settings, .card)) .three:has(> :global(.field)) {
			grid-template-columns: minmax(0, 1fr);
			gap: 14px 16px;
		}
	}
	@container settings (max-width: 600px) {
		.two,
		.three,
		:global(.settings-content :is(.ui-settings, .card)) .two:has(> :global(.field)),
		:global(.settings-content :is(.ui-settings, .card)) .three:has(> :global(.field)) {
			grid-template-columns: minmax(0, 1fr);
			gap: 14px 16px;
		}
	}
	@media (pointer: coarse) {
		.sub-settings .sc-form :global(:is(input.input:not([type='checkbox'], [type='radio']), select.input)) {
			height: 44px;
			min-height: 44px;
		}
	}
	/* No languages yet: the note and Add a language share one row. */
	.sub-settings :global(#lang-card > .body:has(> .wants > li.none)) { flex-direction: row !important; flex-wrap: wrap; column-gap: 12px; align-items: center; justify-content: space-between; min-height: 64px; }
	.sub-settings :global(#lang-card > .body:has(> .wants > li.none) > *) { margin: 0 !important; box-shadow: none !important; padding-block: 12px !important; }
	.sub-settings :global(#lang-card > .body:has(> .wants > li.none) > .add) { padding-inline: 14px !important; }
	.sub-settings :global(#lang-card .add .ui-btn) { white-space: nowrap; }
</style>
