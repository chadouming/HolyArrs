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
	import {
		fileBaseName,
		formatBytes,
		formatFps,
		formatRealtime,
		formatSavedDelta,
		itemsQuery,
		itemTitle,
		resolutionLabel,
		skipReasonLabel
	} from './helpers.ts';
	import type { HistoryStateFilter, ItemRow } from './types.ts';

	interface ItemsResponse {
		items: ItemRow[];
		total: number;
	}

	let {
		art,
		reloadSignal = 0,
		onchanged
	}: {
		art: Map<number, string>;
		reloadSignal?: number;
		onchanged?: () => void;
	} = $props();

	const limit = 50;
	let stateFilter = $state<HistoryStateFilter>('done');
	let search = $state('');
	let offset = $state(0);
	let items = $state<ItemRow[]>([]);
	let total = $state(0);
	let loaded = $state(false);
	let error = $state('');
	let busyFileId = $state<number | null>(null);
	let searchTimer: ReturnType<typeof setTimeout> | undefined;

	const num = (n: number) => n.toLocaleString(appLocale());
	const pages = $derived(pager(total, limit, offset));
	const filterOptions = $derived([
		{ value: 'done' as HistoryStateFilter, label: tr('transcode.filter.done') },
		{ value: 'skipped' as HistoryStateFilter, label: tr('transcode.filter.skipped') },
		{ value: 'all' as HistoryStateFilter, label: tr('transcode.filter.all') }
	]);

	let seq = 0;
	async function load() {
		const n = ++seq;
		try {
			const q = itemsQuery({
				state: stateFilter === 'all' ? 'history' : stateFilter,
				q: search,
				limit,
				offset
			});
			const res = await api.get<ItemsResponse>(`/transcode/items?${q}`);
			if (n !== seq) return;
			items = res.items ?? [];
			total = res.total ?? 0;
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

	async function retranscode(item: ItemRow) {
		if (busyFileId !== null) return;
		busyFileId = item.file_id;
		try {
			await api.post(`/files/${item.file_id}/transcode`, { force: true });
			toasts.push(tr('transcode.actions.queuedFile'), 'info', { body: itemTitle(item) || fileBaseName(item.relative_path) });
			onchanged?.();
		} catch (e) {
			toasts.push(tr('transcode.actions.queueFailed'), 'fail', { body: message(e) });
		} finally {
			busyFileId = null;
		}
	}
</script>

<div class="history-tab" data-testid="transcode-history-tab">
	<div class="filter-row">
		<SegmentedControl
			options={filterOptions}
			bind:value={stateFilter}
			label={tr('transcode.history.filterLabel')}
			onchange={changeFilter}
			testid="transcode-history-filter"
		/>
		<div class="search-box">
			<input
				type="search"
				class="input"
				bind:value={search}
				oninput={onSearchInput}
				placeholder={tr('transcode.searchPlaceholder')}
				aria-label={tr('transcode.searchPlaceholder')}
				data-testid="transcode-history-search"
			/>
		</div>
	</div>

	{#if error && items.length === 0}
		<LoadError hasData={false} message={plainReason(error).text} onretry={load} />
	{:else}
		<PageLoader loading={!loaded} label={tr('transcode.history.loading')}>
			{#if total === 0}
				<EmptyState
					compact
					icon="clock"
					title={search.trim() ? tr('transcode.emptySearch') : tr('transcode.history.none')}
				/>
			{:else}
				<section class="tp-sheet" aria-label={tr('transcode.tabs.history')}>
					<ul class="tp-rows">
						{#each items as item (item.file_id)}
							{@const name = itemTitle(item) || fileBaseName(item.relative_path)}
							{@const resLabel = resolutionLabel(item.source_width, item.source_height)}
							{@const delta = formatSavedDelta(item.input_bytes, item.output_bytes, item.saved_bytes)}
							{@const fpsText = formatFps(item.avg_fps)}
							{@const speedText = formatRealtime(item.avg_speed)}
							<li class="hrow" data-testid="transcode-history-row" data-state={item.state}>
								<div class="hmain">
									<span class="tp-art" style="--poster-ratio: 2 / 3">
										<Poster src={art.get(item.title_id)} {name} />
									</span>
									<div class="hinfo">
										<div class="hhead">
											{#if item.title_id}
												<a class="hname" href={to(`/library/${item.title_id}`)}>{name}</a>
											{:else}
												<span class="hname">{name}</span>
											{/if}
											<span class="pill" class:ok={item.state === 'done'} class:wait={item.state === 'skipped'}>
												{tr(`transcode.state.${item.state}`)}
											</span>
											{#if item.state === 'done' && delta.positive}
												<span class="pill ok" data-testid="transcode-delta-pill">-{delta.percentText} · {delta.savedText}</span>
											{:else if item.state === 'done' && delta.negative}
												<span class="pill fail" data-testid="transcode-delta-pill">+{delta.percentText} · +{delta.savedText}</span>
											{/if}
										</div>

										{#if item.relative_path}
											<p class="hpath">{item.relative_path}</p>
										{/if}

										<div class="pills">
											{#if item.state === 'done' && item.output_bytes > 0}
												<span class="pill">
													{formatBytes(item.input_bytes)} → {formatBytes(item.output_bytes)}
												</span>
											{:else if item.input_bytes > 0}
												<span class="pill">{formatBytes(item.input_bytes)}</span>
											{/if}
											{#if item.source_codec || item.target_codec}
												<span class="pill">
													{item.target_codec && item.state === 'done'
														? `${item.source_codec || '?'} → ${item.target_codec}`
														: item.source_codec || '?'}
												</span>
											{/if}
											{#if resLabel}<span class="pill">{resLabel}</span>{/if}
											{#if item.selected_q != null}<span class="pill">Q {item.selected_q}</span>{/if}
											{#if item.mean_vmaf != null}<span class="pill">VMAF {Number(item.mean_vmaf.toFixed(1))}</span>{/if}
											{#if item.gpu_device}<span class="pill">{item.gpu_device}</span>{/if}
											{#if item.profile_name}<span class="pill">{item.profile_name}</span>{/if}
											{#if item.edl_applied}<span class="pill">{tr('transcode.badge.edl')}</span>{/if}
											{#if item.tonemapped}<span class="pill">{tr('transcode.badge.tonemapped')}</span>{/if}
											{#if item.fileflows_tagged}<span class="pill">{tr('transcode.badge.tagged')}</span>{/if}
										</div>

										<p class="hmeta">
											{#if item.skip_reason}
												<span>{skipReasonLabel(item.skip_reason)}</span>
											{/if}
											{#if fpsText || speedText}
												<span>{[fpsText, speedText].filter(Boolean).join(' · ')}</span>
											{/if}
											{#if item.completed_at}
												<span>{dateTime(item.completed_at)}</span>
											{/if}
										</p>
									</div>
								</div>

								<div class="hact">
									<Button
										variant="secondary"
										size="sm"
										icon="replay"
										busy={busyFileId === item.file_id}
										disabled={busyFileId !== null}
										onclick={() => retranscode(item)}
										data-testid="transcode-retranscode-btn"
									>{tr('transcode.actions.retranscode')}</Button>
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
	.pill.fail { background: color-mix(in srgb, var(--fail) 16%, transparent); color: var(--fail-text, var(--fail)); }

	@media (max-width: 639px) {
		.search-box { max-width: none; flex: 1 1 100%; }
		.hrow { flex-direction: column; align-items: stretch; gap: 10px; }
		.hact { justify-content: flex-end; }
	}
</style>
