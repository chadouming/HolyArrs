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
	import { to } from '#lib/core/paths.ts';
	import { plainReason } from '#lib/core/plain.ts';
	import { toasts } from '#lib/core/toasts.svelte.ts';
	import { onMount, untrack } from 'svelte';
	import { langName, type Language } from './api.ts';
	import {
		doneParts,
		fileBaseName,
		historyQuery,
		howWord,
		jobName,
		type HistoryItem,
		type HistoryStateFilter,
		type How
	} from './overview.ts';

	interface HistoryResponse {
		items: HistoryItem[];
		total: number;
		done_count: number;
		skipped_count: number;
		done_counts?: Record<How, number>;
		limit: number;
		offset: number;
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
	let stateFilter = $state<HistoryStateFilter>('done');
	let search = $state('');
	let offset = $state(0);
	let items = $state<HistoryItem[]>([]);
	let total = $state(0);
	let doneCounts = $state<Record<How, number> | undefined>(undefined);
	let loaded = $state(false);
	let error = $state('');
	let busyFileId = $state<number | null>(null);
	let searchTimer: ReturnType<typeof setTimeout> | undefined;

	const num = (n: number) => n.toLocaleString(appLocale());
	const pages = $derived(pager(total, limit, offset));
	const summaryPills = $derived(doneParts(doneCounts));
	const filterOptions = $derived([
		{ value: 'done' as HistoryStateFilter, label: tr('subtitles.history.filterDone') },
		{ value: 'skipped' as HistoryStateFilter, label: tr('subtitles.history.filterSkipped') },
		{ value: 'all' as HistoryStateFilter, label: tr('subtitles.history.filterAll') }
	]);

	let seq = 0;
	async function load() {
		const n = ++seq;
		try {
			const q = historyQuery({ state: stateFilter, q: search, limit, offset });
			const res = await api.get<HistoryResponse>(`/subtitles/history?${q}`);
			if (n !== seq) return;
			items = res.items ?? [];
			total = res.total ?? 0;
			doneCounts = res.done_counts;
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
		return () => clearTimeout(searchTimer);
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

	function onSearchInput() {
		clearTimeout(searchTimer);
		searchTimer = setTimeout(() => {
			offset = 0;
			void load();
		}, 250);
	}

	function go(nextOffset: number | null) {
		if (nextOffset === null) return;
		offset = nextOffset;
		void load();
	}

	async function searchAgain(item: HistoryItem) {
		if (busyFileId !== null) return;
		busyFileId = item.file_id;
		const label = jobName(item) || fileBaseName(item.relative_path);
		try {
			await api.post('/subtitles/search', { file_id: item.file_id });
			toasts.push(tr('shows.sheet.searchingSubs'), 'info', label ? { body: label } : undefined);
			await load();
			onchanged?.();
		} catch (e) {
			toasts.push(tr('mediafiles.title.startFailed'), 'fail', { body: message(e) });
		} finally {
			busyFileId = null;
		}
	}
</script>

<div class="history-tab" data-testid="subtitles-history-tab">
	<div class="filter-row">
		<SegmentedControl
			options={filterOptions}
			bind:value={stateFilter}
			label={tr('subtitles.history.filterLabel')}
			onchange={changeFilter}
			testid="subtitles-history-filter"
		/>
		<div class="search-box">
			<input
				type="search"
				class="input"
				bind:value={search}
				oninput={onSearchInput}
				placeholder={tr('subtitles.searchPlaceholder')}
				aria-label={tr('subtitles.searchPlaceholder')}
				data-testid="subtitles-history-search"
			/>
		</div>
	</div>

	{#if summaryPills.length > 0}
		<div class="dcounts">
			{#each summaryPills as p (p.how)}
				<span class="pill" data-testid="subtitles-done-count" data-how={p.how}>
					{howWord(p.how)} {num(p.n)}
				</span>
			{/each}
		</div>
	{/if}

	{#if error && items.length === 0}
		<LoadError hasData={false} message={plainReason(error).text} onretry={load} />
	{:else}
		<PageLoader loading={!loaded} label={tr('subtitles.history.loading')}>
			{#if total === 0}
				<EmptyState
					compact
					icon="clock"
					title={search.trim() ? tr('subtitles.emptySearch') : tr('subtitles.history.none')}
				/>
			{:else}
				<section class="tp-sheet" aria-label={tr('subtitles.tabs.history')}>
					<ul class="tp-rows">
						{#each items as item (`${item.state}:${item.id}:${item.file_id}:${item.language}`)}
							{@const name = jobName(item) || fileBaseName(item.relative_path)}
							<li class="hrow" data-testid="subtitles-history-row" data-state={item.state} data-how={item.how}>
								<div class="hmain">
									<span class="tp-art" style="--poster-ratio: 2 / 3">
										<Poster src={item.title_id ? art.get(item.title_id) : undefined} {name} />
									</span>
									<div class="hinfo">
										<div class="hhead">
											{#if item.title_id}
												<a class="hname" href={to(`/library/${item.title_id}`)}>{name}</a>
											{:else}
												<span class="hname">{name}</span>
											{/if}
											<span class="pill" class:ok={item.state === 'done'} class:wait={item.state === 'skipped'}>
												{item.state === 'done' ? tr('subtitles.history.stateDone') : tr('subtitles.history.stateSkipped')}
											</span>
										</div>

										{#if item.relative_path}
											<p class="hpath">{item.relative_path}</p>
										{/if}

										<div class="pills">
											{#if item.language}<span class="pill">{langName(item.language, langs)}</span>{/if}
											{#if item.forced}<span class="pill">{tr('subtitles.kind.forced')}</span>{/if}
											{#if item.how && item.how !== 'skipped'}<span class="pill">{howWord(item.how)}</span>{/if}
											{#if item.source}<span class="pill">{item.source}</span>{/if}
										</div>

										{#if item.at}
											<p class="hmeta">
												<span>{dateTime(item.at)}</span>
											</p>
										{/if}
									</div>
								</div>

								<div class="hact">
									<Button
										variant="secondary"
										size="sm"
										icon="search"
										busy={busyFileId === item.file_id}
										disabled={busyFileId !== null}
										aria-label={tr('subtitles.history.searchAgainFor', { name })}
										onclick={() => searchAgain(item)}
										data-testid="subtitles-history-retry"
									>{tr('subtitles.history.searchAgain')}</Button>
								</div>
							</li>
						{/each}
					</ul>
				</section>

				{#if pages.prev !== null || pages.next !== null}
					<nav class="tp-foot" aria-label={tr('mediafiles.releaseList.pages')}>
						<Button icon="chevronLeft" disabled={pages.prev === null} onclick={() => go(pages.prev)}>{tr('mediafiles.releaseList.back')}</Button>
						<span class="tp-note">{tr('library.copies.range', { from: num(pages.from), to: num(pages.to), total: num(total) })}</span>
						<Button iconAfter="chevronRight" disabled={pages.next === null} onclick={() => go(pages.next)}>{tr('mediafiles.releaseList.more')}</Button>
					</nav>
				{/if}
			{/if}
		</PageLoader>
	{/if}
</div>

<style>
	.history-tab { display: flex; flex-direction: column; gap: 14px; min-width: 0; }
	.filter-row { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 10px; }
	.search-box { flex: 1 1 220px; max-width: 340px; min-width: 180px; }
	.search-box .input { min-height: 36px; height: 36px; }
	.dcounts { display: flex; flex-wrap: wrap; gap: 6px; }
	.hrow { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px 16px; min-width: 0; }
	.hmain { display: flex; align-items: flex-start; gap: 12px; min-width: 0; flex: 1 1 auto; }
	.hinfo { display: flex; flex-direction: column; gap: 5px; min-width: 0; flex: 1 1 auto; }
	.hhead { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 8px; min-width: 0; }
	.hname { color: var(--text); font: 600 14px/1.3 var(--font-ui); text-decoration: none; overflow-wrap: anywhere; }
	a.hname:hover { text-decoration: underline; text-underline-offset: 3px; }
	.hpath { margin: 0; color: var(--text-muted); font: var(--t-footnote); overflow-wrap: anywhere; }
	.pills { display: flex; flex-wrap: wrap; gap: 5px; }
	.hmeta { margin: 0; display: flex; flex-wrap: wrap; gap: 4px 10px; color: var(--text-muted); font: var(--t-footnote); font-variant-numeric: tabular-nums; }
	.hact { flex: none; display: flex; align-items: center; }
	.pill { flex: none; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 999px; font: 600 11.5px/1 var(--font-ui); font-variant-numeric: tabular-nums; background: color-mix(in srgb, var(--text) 8%, transparent); color: color-mix(in srgb, var(--text) 82%, var(--bg)); }
	.pill.ok { background: color-mix(in srgb, var(--ok) 16%, transparent); color: var(--ok-text, var(--ok)); }
	.pill.wait { background: color-mix(in srgb, var(--wait, var(--accent)) 16%, transparent); color: var(--wait-text, var(--wait, var(--text))); }

	@media (max-width: 639px) {
		.search-box { max-width: none; flex: 1 1 100%; }
		.hrow { flex-direction: column; align-items: stretch; gap: 10px; }
		.hact { justify-content: flex-end; }
	}
</style>
