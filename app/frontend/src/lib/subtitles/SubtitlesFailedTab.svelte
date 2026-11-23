<script lang="ts">
	import Button from '#lib/ui/Button.svelte';
	import EmptyState from '#lib/ui/EmptyState.svelte';
	import LoadError from '#lib/ui/LoadError.svelte';
	import PageLoader from '#lib/ui/PageLoader.svelte';
	import Poster from '#lib/ui/Poster.svelte';
	import SegmentedControl from '#lib/ui/SegmentedControl.svelte';
	import { pager } from '#lib/mediafiles/index.ts';
	import { api, message } from '#lib/core/api.ts';
	import { dateTime } from '#lib/core/format.ts';
	import { appLocale, t as tr } from '#lib/core/i18n/i18n.svelte.ts';
	import { serverWords } from '#lib/core/i18n/server-words.ts';
	import { to } from '#lib/core/paths.ts';
	import { plainReason } from '#lib/core/plain.ts';
	import { toasts } from '#lib/core/toasts.svelte.ts';
	import { onMount, untrack } from 'svelte';
	import { langName, type Language } from './api.ts';
	import { fileBaseName, jobName, jobWord, type OverviewJob } from './overview.ts';

	type StateFilter = 'all' | 'failed' | 'cancelled';

	interface FailedResponse {
		jobs: OverviewJob[];
		total: number;
		failed_count: number;
		cancelled_count: number;
	}

	let {
		langs,
		art,
		reloadSignal = 0,
		onchanged
	}: {
		langs: Language[];
		art: Map<number, string>;
		reloadSignal?: number;
		onchanged?: () => void;
	} = $props();

	const limit = 50;
	let stateFilter = $state<StateFilter>('all');
	let offset = $state(0);
	let jobs = $state<OverviewJob[]>([]);
	let total = $state(0);
	let failedCount = $state(0);
	let loaded = $state(false);
	let error = $state('');
	let retryingId = $state<number | null>(null);
	let retryingAll = $state(false);

	const num = (n: number) => n.toLocaleString(appLocale());
	const pages = $derived(pager(total, limit, offset));
	const filterOptions = $derived([
		{ value: 'all' as StateFilter, label: tr('subtitles.page.need.all') },
		{ value: 'failed' as StateFilter, label: tr('subtitles.failed.stateFailed') },
		{ value: 'cancelled' as StateFilter, label: tr('subtitles.failed.stateStopped') }
	]);

	let seq = 0;
	async function load() {
		const n = ++seq;
		try {
			const q = new URLSearchParams({ state: stateFilter, limit: String(limit), offset: String(offset) });
			const res = await api.get<FailedResponse>(`/subtitles/failed?${q.toString()}`);
			if (n !== seq) return;
			jobs = res.jobs ?? [];
			total = res.total ?? 0;
			failedCount = res.failed_count ?? 0;
			error = '';
		} catch (e) {
			if (n !== seq) return;
			error = message(e);
		} finally {
			if (n === seq) loaded = true;
		}
	}

	onMount(() => {
		void load();
	});

	let lastSignal = -1;
	$effect(() => {
		const sig = reloadSignal;
		if (lastSignal === -1) {
			lastSignal = sig;
			return;
		}
		if (sig !== lastSignal) {
			lastSignal = sig;
			untrack(() => void load());
		}
	});

	function changeFilter() {
		offset = 0;
		void load();
	}

	function go(nextOffset: number | null) {
		if (nextOffset === null) return;
		offset = nextOffset;
		void load();
	}

	async function retryOne(j: OverviewJob) {
		if (retryingId !== null || retryingAll) return;
		retryingId = j.id;
		try {
			await api.post(`/subtitles/jobs/${j.id}/retry`);
			const label = jobName(j) || jobWord(j.kind);
			toasts.push(tr('subtitles.failed.retried'), 'info', label ? { body: label } : undefined);
			await load();
			onchanged?.();
		} catch (e) {
			toasts.push(tr('subtitles.failed.retryFailed'), 'fail', { body: message(e) });
		} finally {
			retryingId = null;
		}
	}

	async function retryAll() {
		if (retryingAll || retryingId !== null) return;
		retryingAll = true;
		try {
			const res = await api.post<{ retried: number; skipped: number }>('/subtitles/jobs/retry-failed');
			toasts.push(tr('subtitles.failed.retriedAll', { n: num(res.retried) }), 'info');
			await load();
			onchanged?.();
		} catch (e) {
			toasts.push(tr('subtitles.failed.retryFailed'), 'fail', { body: message(e) });
		} finally {
			retryingAll = false;
		}
	}

	function attemptsText(j: OverviewJob): string {
		if (!j.attempt) return '';
		if (j.max_attempts) return tr('subtitles.failed.attemptsOf', { n: num(j.attempt), max: num(j.max_attempts) });
		return tr('subtitles.failed.attempt', { n: num(j.attempt) });
	}
</script>

<div class="failed-tab" data-testid="subtitles-failed-tab">
	<div class="fbar">
		<SegmentedControl options={filterOptions} bind:value={stateFilter} label={tr('subtitles.failed.filterLabel')} onchange={changeFilter} testid="subtitles-failed-filter" />
		<span class="spacer"></span>
		{#if failedCount > 0}
			<span class="tp-cap sub-cap">
				<Button variant="secondary" icon="refresh" busy={retryingAll} disabled={retryingId !== null} onclick={retryAll} data-testid="subtitles-retry-all">
					{tr('subtitles.failed.retryAll')}
				</Button>
			</span>
		{/if}
	</div>

	{#if error && jobs.length === 0}
		<LoadError hasData={false} message={plainReason(error).text} onretry={load} />
	{:else}
		<PageLoader loading={!loaded} label={tr('subtitles.failed.loading')}>
			{#if total === 0}
				<EmptyState compact icon="check" title={stateFilter === 'all' ? tr('subtitles.failed.none') : tr('subtitles.failed.noneFiltered')} />
			{:else}
				<section class="tp-sheet" aria-label={tr('subtitles.tabs.failed')}>
					<ul class="tp-rows">
						{#each jobs as j (j.id)}
							{@const name = jobName(j) || fileBaseName(j.relative_path)}
							{@const attempts = attemptsText(j)}
							<li class="frow" data-testid="subtitles-failed-row" data-state={j.state}>
								<div class="fmain">
									<span class="tp-art" style="--poster-ratio: 2 / 3">
										<Poster src={j.title_id ? art.get(j.title_id) : undefined} name={name || jobWord(j.kind)} />
									</span>
									<div class="finfo">
										<div class="fhead">
											{#if name && j.title_id}
												<a class="fname" href={to(`/library/${j.title_id}`)}>{name}</a>
											{:else}
												<span class="fname">{name || jobWord(j.kind)}</span>
											{/if}
											<span class="pill" class:fail={j.state === 'failed'}>
												{j.state === 'failed' ? tr('subtitles.failed.stateFailed') : tr('subtitles.failed.stateStopped')}
											</span>
										</div>
										{#if j.relative_path}
											<p class="fpath">{j.relative_path}</p>
										{/if}
										<p class="fmeta">
											<span>{j.language ? `${jobWord(j.kind)} · ${langName(j.language, langs)}` : jobWord(j.kind)}</span>
											{#if attempts}<span>{attempts}</span>{/if}
											{#if j.ended_at}<span>{dateTime(j.ended_at)}</span>{/if}
										</p>
										{#if j.error}
											<p class="ferr">{serverWords(j.error)}</p>
										{/if}
									</div>
								</div>
								<div class="fact">
									<Button
										variant="secondary"
										size="sm"
										icon="refresh"
										busy={retryingId === j.id}
										disabled={retryingAll || retryingId === j.id}
										onclick={() => retryOne(j)}
										data-testid="subtitles-job-retry"
									>{tr('subtitles.failed.retry')}</Button>
								</div>
							</li>
						{/each}
					</ul>
				</section>

				{#if pages.prev !== null || pages.next !== null}
					<nav class="tp-foot" aria-label={tr('mediafiles.releaseList.pages')}>
						<Button icon="chevronLeft" disabled={pages.prev === null} onclick={() => go(pages.prev)}>{tr('mediafiles.releaseList.back')}</Button>
						<span class="tp-note">{tr('library.copies.range', { from: pages.from, to: pages.to, total })}</span>
						<Button iconAfter="chevronRight" disabled={pages.next === null} onclick={() => go(pages.next)}>{tr('mediafiles.releaseList.more')}</Button>
					</nav>
				{/if}
			{/if}
		</PageLoader>
	{/if}
</div>

<style>
	.failed-tab { display: flex; flex-direction: column; gap: 14px; min-width: 0; }
	.fbar { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
	.spacer { flex: 1 1 auto; }
	.frow { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px 16px; min-width: 0; }
	.fmain { display: flex; align-items: flex-start; gap: 12px; min-width: 0; flex: 1 1 auto; }
	.finfo { display: flex; flex-direction: column; gap: 4px; min-width: 0; flex: 1 1 auto; }
	.fhead { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 8px; min-width: 0; }
	.fname { color: var(--text); font: 600 14px/1.3 var(--font-ui); text-decoration: none; overflow-wrap: anywhere; }
	a.fname:hover { text-decoration: underline; text-underline-offset: 3px; }
	.fpath { margin: 0; color: var(--text-muted); font: var(--t-footnote); overflow-wrap: anywhere; }
	.fmeta { margin: 0; display: flex; flex-wrap: wrap; gap: 4px 10px; color: var(--text-muted); font: var(--t-footnote); font-variant-numeric: tabular-nums; }
	.ferr { margin: 2px 0 0; color: var(--fail-text, var(--fail)); font: var(--t-footnote); overflow-wrap: anywhere; word-break: break-word; }
	.fact { flex: none; display: flex; align-items: center; }
	.pill { flex: none; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 999px; font: 600 11.5px/1 var(--font-ui); background: color-mix(in srgb, var(--text) 8%, transparent); color: color-mix(in srgb, var(--text) 82%, var(--bg)); }
	.pill.fail { background: color-mix(in srgb, var(--fail) 16%, transparent); color: var(--fail-text, var(--fail)); }

	@media (max-width: 639px) {
		.sub-cap :global(.ui-btn) { width: 100%; justify-content: center; }
		.frow { flex-direction: column; align-items: stretch; gap: 10px; }
		.fact { justify-content: flex-end; }
	}
</style>
