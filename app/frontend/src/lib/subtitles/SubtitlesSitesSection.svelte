<script lang="ts">
	import { editForm, withoutEditing } from '#lib/ui/edit-in-place.ts';
	import { ask } from '#lib/ui/ask.svelte.ts';
	import { t } from '#lib/core/i18n/i18n.svelte.ts';
	import Field from '#lib/ui/Field.svelte';
	import Button from '#lib/ui/Button.svelte';
	import TestTag from '#lib/ui/TestTag.svelte';
	import Icon from '#lib/ui/Icon.svelte';
	import EmptyState from '#lib/ui/EmptyState.svelte';
	import {
		accountMissing,
		accountWords,
		kindCovers,
		siteBody,
		siteForm,
		siteState,
		type SiteForm,
		type SiteKind,
		type SiteView
	} from '#lib/subtitles/index.ts';
	import { onMount } from 'svelte';
	import { testCount } from '#lib/core/test-tag.ts';
	import { api } from '#lib/core/api.ts';
	import { plainMessage as message } from '#lib/core/settings.ts';
	import { dateTime } from '#lib/core/format.ts';
	import { toasts } from '#lib/core/toasts.svelte.ts';

	let {
		sites,
		kinds,
		onreload,
		onchanged
	}: {
		sites: SiteView[];
		kinds: SiteKind[];
		onreload: () => Promise<void>;
		onchanged?: () => void;
	} = $props();

	let editing = $state<SiteView | null>(null);
	let isNew = $state(false);
	let form = $state<SiteForm | null>(null);
	let formError = $state('');
	let busy = $state(false);
	let testing = $state(0);
	let results = $state<Record<number, { ok: boolean; text: string }>>({});
	let alive = true;

	onMount(() => {
		alive = true;
		return () => {
			alive = false;
		};
	});

	const kindOf = (k: string) => kinds.find((x) => x.kind === k);
	const formKind = $derived(form ? kindOf(form.kind) : undefined);
	const needs = $derived(formKind && form ? accountMissing(formKind, form, !isNew && !!editing?.has_account) : []);

	function startAdd() {
		const k = kinds.find((x) => !x.needs_account && !x.later && !x.default_off) ?? kinds.find((x) => !x.later);
		if (!k) return;
		isNew = true;
		editing = { ID: 0 } as SiteView;
		form = siteForm(k);
		formError = '';
	}

	function pickKind(k: SiteKind) {
		form = siteForm(k);
	}

	function startChange(s: SiteView) {
		const k = kindOf(s.kind);
		if (!k) return;
		isNew = false;
		editing = s;
		form = siteForm(k, s);
		formError = '';
	}

	async function save(e: SubmitEvent) {
		e.preventDefault();
		if (!form) return;
		if (needs.length) {
			formError = t('settings.subtitles.needsAccount', {
				fields: needs.map((n) => accountWords[n].toLowerCase()).join(t('settings.subtitles.and'))
			});
			return;
		}
		busy = true;
		formError = '';
		try {
			const body = siteBody(form);
			const savedSite = isNew
				? await api.post<SiteView>('/subtitle-sites', body)
				: await api.put<SiteView>(`/subtitle-sites/${editing!.ID}`, body);
			if (!alive) return;
			toasts.push(t('settings.subtitles.siteSaved', { name: savedSite.name }), 'ok');
			editing = null;
			form = null;
			await onreload();
			if (!alive) return;
			await test(savedSite);
			if (!alive) return;
			onchanged?.();
		} catch (err) {
			if (alive) formError = message(err);
		} finally {
			if (alive) busy = false;
		}
	}

	async function test(s: SiteView) {
		testing = s.ID;
		try {
			const r = await api.post<{ ok: boolean; message: string }>(`/subtitle-sites/${s.ID}/test`);
			if (alive) results[s.ID] = { ok: r.ok, text: r.message };
		} catch (err) {
			if (alive) results[s.ID] = { ok: false, text: message(err) };
		} finally {
			if (alive) testing = 0;
		}
	}

	async function setPaused(s: SiteView, paused: boolean) {
		const k = kindOf(s.kind);
		if (!k) return;
		try {
			// A change without account details keeps the stored ones.
			await api.put(`/subtitle-sites/${s.ID}`, { ...siteBody(siteForm(k, s)), enabled: !paused });
			if (!alive) return;
			toasts.push(
				paused ? t('settings.subtitles.paused', { name: s.name }) : t('settings.subtitles.resumed', { name: s.name }),
				'ok',
				paused ? { body: t('settings.subtitles.skipIt') } : undefined
			);
			await onreload();
			if (!alive) return;
			onchanged?.();
		} catch (err) {
			toasts.push(paused ? t('settings.subtitles.couldntPause') : t('settings.subtitles.couldntResume'), 'fail', {
				body: message(err)
			});
		}
	}

	async function remove(s: SiteView) {
		if (!(await ask(t('settings.subtitles.confirmRemove', { name: s.name }), { yes: t('ui.ask.remove'), danger: true }))) return;
		if (!alive) return;
		try {
			await api.del(`/subtitle-sites/${s.ID}`);
			if (!alive) return;
			await onreload();
			if (!alive) return;
			onchanged?.();
		} catch (err) {
			toasts.push(t('settings.subtitles.removeFailed'), 'fail', { body: message(err) });
		}
	}
</script>

<section class="sites-sec" id="sites-card" aria-labelledby="h-sites" tabindex="-1">
	<div class="sites-head">
		<div>
			<h2 id="h-sites" class="h2">{t('settings.subtitles.sites')}</h2>
			<p class="small muted">{t('settings.subtitles.sitesNote')}</p>
		</div>
		{#if !editing && sites.length}<Button icon="plus" variant="primary" type="button" onclick={startAdd}>{t('settings.subtitles.add')}</Button>{/if}
	</div>

	{#if editing && form}
		<form class="card" use:editForm={isNew ? null : editing.ID} onsubmit={save} aria-labelledby="h-site-form">
			<h3 id="h-site-form" class="h3">{isNew ? t('settings.subtitles.add') : t('settings.subtitles.change', { name: editing.name })}</h3>
			{#if isNew}
				<fieldset class="group">
					<legend class="label">{t('settings.subtitles.whichSite')}</legend>
					{#each kinds.filter((x) => !x.later) as k (k.kind)}
						<label class="checkline">
							<input type="radio" name="site-kind" checked={form.kind === k.kind} disabled={k.later} onchange={() => pickKind(k)} />
							<span>
								<b>{k.name}</b> ({kindCovers(k)}){#if k.needs_account}{' '}<span class="chip fail">{t('settings.subtitles.needsYours')}</span>{/if}
								{#if k.note}<br /><span class="hint">{k.note}</span>{/if}
							</span>
						</label>
					{/each}
				</fieldset>
			{/if}
			<div class="two">
				<Field id="st-name" label={t('settings.subtitles.name')}>
					<input id="st-name" class="input" bind:value={form.name} maxlength="100" required />
				</Field>
				<Field id="st-prio" label={t('settings.subtitles.priority')} hint={t('settings.subtitles.priorityHint')}>
					<input id="st-prio" class="input narrow" type="number" min="1" max="100" bind:value={form.priority} />
				</Field>
			</div>
			<Field id="st-url" label={t('settings.subtitles.address')}>
				<input id="st-url" class="input mono" bind:value={form.url} autocomplete="off" spellcheck="false" required />
			</Field>
			{#if formKind?.needs_account}
				<fieldset class="group account" data-testid="site-account">
					<legend class="label">{t('settings.subtitles.yourAccount')}</legend>
					<p class="small muted">
						{t('settings.subtitles.accountNote')}{#if !isNew && editing.has_account}
							{t('settings.subtitles.accountKeep')}{/if}
					</p>
					{#each formKind.account ?? [] as a (a)}
						<Field id="st-acct-{a}" label={accountWords[a]}>
							{#if a === 'username'}
								<input id="st-acct-{a}" class="input" bind:value={form.username} autocomplete="off" />
							{:else if a === 'password'}
								<input id="st-acct-{a}" class="input" type="password" bind:value={form.password} autocomplete="new-password" />
							{:else}
								<input id="st-acct-{a}" class="input mono" type="password" bind:value={form.api_key} autocomplete="new-password" spellcheck="false" />
							{/if}
						</Field>
					{/each}
				</fieldset>
			{/if}
			<details class="more">
				<summary>{t('settings.subtitles.limits')}</summary>
				<div class="two">
					<Field id="st-gap" label={t('settings.subtitles.gap')}>
						<input id="st-gap" class="input narrow" type="number" min="0" max="60000" bind:value={form.limits.gap_ms} />
					</Field>
					<Field id="st-per" label={t('settings.subtitles.perWindow')}>
						<input id="st-per" class="input narrow" type="number" min="1" max="10000" bind:value={form.limits.per_window} />
					</Field>
					<Field id="st-win" label={t('settings.subtitles.window')}>
						<input id="st-win" class="input narrow" type="number" min="60" max="86400" bind:value={form.limits.window_seconds} />
					</Field>
					<Field id="st-day" label={t('settings.subtitles.perDay')}>
						<input id="st-day" class="input narrow" type="number" min="0" max="100000" bind:value={form.limits.downloads_per_day} />
					</Field>
				</div>
			</details>
			<label class="checkline">
				<input type="checkbox" bind:checked={form.allow_local_network} />
				<span><b>{t('settings.subtitles.local')}</b><br /><span class="hint">{t('settings.subtitles.localHint')}</span></span>
			</label>
			<label class="checkline">
				<input type="checkbox" bind:checked={form.machine_made} data-testid="site-machine-made" />
				<span><b>{t('settings.subtitles.machineMade')}</b><br /><span class="hint">{t('settings.subtitles.machineMadeHint')}</span></span>
			</label>
			<label class="checkline">
				<input type="checkbox" bind:checked={form.enabled} />
				<span><b>{t('settings.subtitles.on')}</b></span>
			</label>
			{#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
			<div class="row form-actions">
				<Button variant="primary" type="submit" disabled={busy || !form.name.trim() || !form.url.trim()} aria-busy={busy}>{t('settings.subtitles.save')}</Button>
				<Button type="button" onclick={() => ((editing = null), (form = null))}>{t('settings.subtitles.cancel')}</Button>
			</div>
		</form>
	{/if}

	{#if sites.length === 0 && !editing}
		<EmptyState compact icon="subtitle" title={t('settings.subtitles.none')} text={t('settings.subtitles.noneText')} action={{ label: t('settings.subtitles.add'), icon: 'plus', onclick: startAdd }} />
	{:else}
		<ul class="list">
			{#each withoutEditing(sites, editing?.ID, (x) => x.ID) as s (s.ID)}
				{@const k = kindOf(s.kind)}
				{@const st = siteState(s, k, dateTime)}
				<li class="card" data-testid="subtitle-site" data-edit-row={s.ID}>
					<div class="head">
						<span class="ic"><Icon name="subtitle" /></span>
						<div class="grow">
							<h3 class="h3">{s.name}</h3>
							<div class="mono xs muted" style="overflow-wrap: anywhere">{s.url}</div>
						</div>
						<span class="chip {st.tone}" data-testid="subtitle-site-state">{st.label}</span>
					</div>
					<p class="small" data-testid="subtitle-site-text">{st.text}</p>
					<p class="xs muted">
						{k?.name ?? s.kind} · {k ? kindCovers(k) : ''} · {t('settings.subtitles.priorityN', { n: s.priority })}
						{#if k?.needs_account} · {s.has_account ? t('settings.subtitles.accountSaved') : t('settings.subtitles.accountNotSet')}{/if}
						{#if s.allow_local_network} · {t('settings.subtitles.mayReach')}{/if}
						{#if s.limits.downloads_per_day} · {t('settings.subtitles.downloadsToday', { n: s.downloads_today, max: s.limits.downloads_per_day })}{/if}
					</p>
					{#if results[s.ID]}
						<div class="testline" data-testid="subtitle-site-test"><TestTag ok={results[s.ID].ok} text={results[s.ID].text} app={k?.name} extra={results[s.ID].ok ? testCount(results[s.ID].text) : ''} />{#if !results[s.ID].ok}<span class="small bad-text">{results[s.ID].text}</span>{/if}</div>
					{/if}
					<div class="row actions">
						<Button type="button" disabled={testing === s.ID} aria-busy={testing === s.ID} onclick={() => test(s)} aria-label={t('settings.subtitles.testName', { name: s.name })}>{t('settings.subtitles.test')}</Button>
						<!-- A saved site whose kind is no longer offered can only be tested or removed. -->
						{#if k}
							{#if s.state === 'disabled'}
								<Button type="button" onclick={() => setPaused(s, false)} aria-label={t('settings.subtitles.turnOnName', { name: s.name })}>{t('settings.subtitles.turnOn')}</Button>
							{:else}
								<Button type="button" onclick={() => setPaused(s, true)} aria-label={t('settings.subtitles.pauseName', { name: s.name })}>{t('settings.subtitles.pause')}</Button>
							{/if}
							<Button type="button" data-edit-change onclick={() => startChange(s)} aria-label={t('settings.subtitles.changeName', { name: s.name })}>{t('settings.subtitles.changeButton')}</Button>
						{/if}
						<Button type="button" onclick={() => remove(s)} aria-label={t('settings.subtitles.removeName', { name: s.name })}>{t('settings.subtitles.remove')}</Button>
					</div>
				</li>
			{/each}
		</ul>
	{/if}
</section>

<style>
	:where(.sites-sec) :is(form.card, li.card) {
		border: var(--sheet-edge, 1px solid var(--line));
		border-radius: var(--sheet-r, var(--r-panel));
		background: var(--sheet-bg, var(--surface-1));
		box-shadow: var(--sheet-shadow, none);
		padding: 16px 18px;
	}
	.sites-sec :global(:is(input.input:not([type='checkbox'], [type='radio']), select.input)) {
		height: 40px;
		min-height: 40px;
	}
	.sites-sec .checkline:not(.box) {
		align-items: flex-start;
		padding-block: 10px;
	}
	.sites-sec .checkline:not(.box) > input[type='checkbox'],
	.sites-sec .checkline > input[type='radio'] {
		margin-top: 2px;
	}
	.testline { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; }
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
	}
	.two + :global(.field) {
		margin-top: var(--s-4);
	}
	.form-actions {
		margin-top: var(--s-5);
	}
	.chip.ok { color: var(--ok-text); }
	.chip.wait { color: var(--wait-text, var(--wait)); }
	.chip.fail { color: var(--fail-text); }
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
	.sites-sec {
		margin-top: 0;
	}
	/* The heading reads like every section heading: small and grey, its note beside it. */
	.sites-head > div { display: flex; align-items: baseline; flex-wrap: wrap; gap: 2px 10px; padding: 0 6px; }
	.sites-head .h2 { color: var(--text-muted); font: 600 13px/1.2 var(--font-ui); letter-spacing: 0.02em; }
	.sites-head p { margin: 0; color: var(--text-muted); font: 400 12px/1.2 var(--font-ui); }
	.sites-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: var(--s-3);
		margin-top: var(--s-4);
		margin-bottom: 10px;
	}
	/* No sites yet: the empty note sits in the same glass sheet as every other section. */
	.sites-sec > :global(.ui-empty) {
		width: auto;
		max-width: none;
		margin: 0;
		padding: 22px 18px;
		border: var(--sheet-edge, 1px solid var(--line));
		border-radius: var(--sheet-r, var(--r-panel));
		background: var(--sheet-bg, var(--surface-1));
		box-shadow: var(--sheet-shadow, none);
	}
	.group {
		border: 0;
		padding: 0;
		margin: var(--s-4) 0 0;
	}
	.account :global(.field) {
		margin-top: var(--s-3);
	}
	.list {
		display: flex;
		flex-direction: column;
		gap: var(--s-3);
		margin-top: var(--s-4);
	}
	.list .card + .card {
		margin-top: 0;
	}
	.head {
		display: flex;
		align-items: center;
		gap: var(--s-3);
	}
	.ic {
		width: 40px;
		height: 40px;
		display: grid;
		place-items: center;
		border-radius: var(--r-btn);
		background: var(--surface-3);
		flex: none;
	}
	.grow {
		min-width: 0;
		flex: 1;
	}
	.card p {
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
		:global(.settings-content :is(.ui-settings, .card)) .two:has(> :global(.field)) {
			grid-template-columns: minmax(0, 1fr);
			gap: 14px 16px;
		}
		.sites-head {
			flex-direction: column;
			align-items: flex-start;
		}
	}
	@container settings (max-width: 600px) {
		.two,
		:global(.settings-content :is(.ui-settings, .card)) .two:has(> :global(.field)) {
			grid-template-columns: minmax(0, 1fr);
			gap: 14px 16px;
		}
		.sites-head {
			flex-direction: column;
			align-items: flex-start;
		}
	}
	@media (max-width: 639px) {
		.sites-head {
			flex-direction: column;
			align-items: flex-start;
		}
	}
	@media (pointer: coarse) {
		.sites-sec :global(:is(input.input:not([type='checkbox'], [type='radio']), select.input)) {
			height: 44px;
			min-height: 44px;
		}
	}
</style>
