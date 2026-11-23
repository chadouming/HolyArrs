<script lang="ts">
	import Button from '#lib/ui/Button.svelte';
	import EmptyState from '#lib/ui/EmptyState.svelte';
	import LoadError from '#lib/ui/LoadError.svelte';
	import PageLoader from '#lib/ui/PageLoader.svelte';
	import Poster from '#lib/ui/Poster.svelte';
	import { pager } from '#lib/mediafiles/index.ts';
	import { api, message } from '#lib/core/api.ts';
	import { appLocale, t as tr } from '#lib/core/i18n/i18n.svelte.ts';
	import { to } from '#lib/core/paths.ts';
	import { plainReason } from '#lib/core/plain.ts';
	import { toasts } from '#lib/core/toasts.svelte.ts';
	import { onMount, untrack } from 'svelte';
	import { langName, type Language } from './api.ts';
	import { fileBaseName, jobName, jobWord, waitingQuery, type WaitingJob } from './overview.ts';

	interface WaitingResponse {
		jobs: WaitingJob[];
		total: number;
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
	let search = $state('');
	let offset = $state(0);
	let jobs = $state<WaitingJob[]>([]);
	let total = $state(0);
	let loaded = $state(false);
	let error = $state('');
	let busyJobId = $state<number | null>(null);
	let busyAction = $state<'bump' | 'skip' | null>(null);
	let searchTimer: ReturnType<typeof setTimeout> | undefined;

	const num = (n: number) => n.toLocaleString(appLocale());
	const pages = $derived(pager(total, limit, offset));

	let seq = 0;
	async function load() {
		const n = ++seq;
		try {
			const q = waitingQuery({ q: search, limit, offset });
			const res = await api.get<WaitingResponse>(`/subtitles/waiting?${q}`);
			if (n !== seq) return;
			const nextJobs = res.jobs ?? [];
			const nextTotal = res.total ?? 0;
			if (nextJobs.length === 0 && offset > 0 && nextTotal > 0) {
				offset = Math.max(0, offset - limit);
				void load();
				return;
			}
			jobs = nextJobs;
			total = nextTotal;
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

	async function bumpJob(j: WaitingJob) {
		if (busyJobId !== null) return;
		busyJobId = j.id;
		busyAction = 'bump';
		try {
			await api.post<{ id: number; priority: string; state: string }>(`/subtitles/jobs/${j.id}/bump`);
			const label = jobName(j) || fileBaseName(j.relative_path);
			toasts.push(tr('subtitles.waiting.bumped'), 'info', label ? { body: label } : undefined);
			await load();
			onchanged?.();
		} catch (e) {
			toasts.push(tr('subtitles.waiting.bumpFailed'), 'fail', { body: message(e) });
		} finally {
			busyJobId = null;
			busyAction = null;
		}
	}

	async function skipJob(j: WaitingJob) {
		if (busyJobId !== null) return;
		busyJobId = j.id;
		busyAction = 'skip';
		try {
			await api.post<{ id: number; skipped: boolean; file_id?: number }>(`/subtitles/jobs/${j.id}/skip`);
			const label = jobName(j) || fileBaseName(j.relative_path);
			toasts.push(tr('subtitles.waiting.skipped'), 'info', label ? { body: label } : undefined);
			await load();
			onchanged?.();
		} catch (e) {
			toasts.push(tr('subtitles.waiting.skipFailed'), 'fail', { body: message(e) });
		} finally {
			busyJobId = null;
			busyAction = null;
		}
	}
</script>

<div class="waiting-tab" data-testid="subtitles-waiting-tab">
	<div class="wait-head">
		<span class="tp-note">
			{tr(total === 1 ? 'subtitles.waiting.count.one' : 'subtitles.waiting.count.other', { n: num(total) })}
		</span>
		<div class="search-box">
			<input
				type="search"
				class="input"
				bind:value={search}
				oninput={onSearchInput}
				placeholder={tr('subtitles.searchPlaceholder')}
				aria-label={tr('subtitles.searchPlaceholder')}
				data-testid="subtitles-waiting-search"
			/>
		</div>
	</div>

	{#if error && jobs.length === 0}
		<LoadError hasData={false} message={plainReason(error).text} onretry={load} />
	{:else}
		<PageLoader loading={!loaded} label={tr('subtitles.waiting.loading')}>
			{#if total === 0}
				<EmptyState
					compact
					icon="clock"
					title={search.trim() ? tr('subtitles.emptySearch') : tr('subtitles.waiting.none')}
				/>
			{:else}
				<section class="tp-sheet" aria-label={tr('subtitles.tabs.waiting')}>
					<ul class="tp-rows">
						{#each jobs as j, idx (j.id)}
							{@const rank = j.position > 0 ? j.position : offset + idx + 1}
							{@const name = jobName(j) || fileBaseName(j.relative_path) || jobWord(j.kind)}
							{@const isTop = j.position === 1 && j.priority === 'now'}
							<li class="wrow" data-testid="subtitles-waiting-row" data-priority={j.priority}>
								<div class="wmain">
									<span class="wrank">#{num(rank)}</span>
									<span class="tp-art" style="--poster-ratio: 2 / 3">
										<Poster src={j.title_id ? art.get(j.title_id) : undefined} {name} />
									</span>
									<div class="winfo">
										<div class="whead">
											{#if j.title_id}
												<a class="wname" href={to(`/library/${j.title_id}`)}>{name}</a>
											{:else}
												<span class="wname">{name}</span>
											{/if}
											{#if j.priority === 'now'}
												<span class="pill prio">{tr('subtitles.waiting.priorityNow')}</span>
											{/if}
										</div>

										{#if j.relative_path}
											<p class="wpath muted">{j.relative_path}</p>
										{/if}

										<div class="pills">
											<span class="pill">{jobWord(j.kind)}</span>
											{#if j.language}<span class="pill">{langName(j.language, langs)}</span>{/if}
											{#if j.forced}<span class="pill">{tr('subtitles.kind.forced')}</span>{/if}
										</div>
									</div>
								</div>

								<div class="wact">
									<Button
										variant="secondary"
										size="sm"
										icon="chevronUp"
										busy={busyJobId === j.id && busyAction === 'bump'}
										disabled={isTop || busyJobId !== null}
										aria-label={tr('subtitles.waiting.bumpFor', { name })}
										onclick={() => bumpJob(j)}
										data-testid="subtitles-bump-job"
									>{tr('subtitles.waiting.bump')}</Button>
									<Button
										variant="plain"
										size="sm"
										icon="x"
										busy={busyJobId === j.id && busyAction === 'skip'}
										disabled={busyJobId !== null}
										aria-label={tr('subtitles.waiting.skipFor', { name })}
										onclick={() => skipJob(j)}
										data-testid="subtitles-skip-job"
									>{tr('subtitles.waiting.skip')}</Button>
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
	.waiting-tab { display: flex; flex-direction: column; gap: 14px; min-width: 0; }
	.wait-head { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 10px; }
	.search-box { flex: 1 1 220px; max-width: 340px; min-width: 180px; }
	.search-box .input { min-height: 36px; height: 36px; }
	.wrow { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px 16px; min-width: 0; }
	.wmain { display: flex; align-items: flex-start; gap: 12px; min-width: 0; flex: 1 1 auto; }
	.wrank { flex: none; min-width: 3ch; padding-top: 2px; color: var(--text-muted); font: 600 13px/1.3 var(--font-ui); font-variant-numeric: tabular-nums; }
	.winfo { display: flex; flex-direction: column; gap: 5px; min-width: 0; flex: 1 1 auto; }
	.whead { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 8px; min-width: 0; }
	.wname { color: var(--text); font: 600 14px/1.3 var(--font-ui); text-decoration: none; overflow-wrap: anywhere; }
	a.wname:hover { text-decoration: underline; text-underline-offset: 3px; }
	.wpath { margin: 0; font: var(--t-footnote); overflow-wrap: anywhere; }
	.pills { display: flex; flex-wrap: wrap; gap: 5px; }
	.wact { flex: none; display: flex; align-items: center; gap: 6px; }
	.pill { flex: none; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 999px; font: 600 11.5px/1 var(--font-ui); font-variant-numeric: tabular-nums; background: color-mix(in srgb, var(--text) 8%, transparent); color: color-mix(in srgb, var(--text) 82%, var(--bg)); }
	.pill.prio { background: color-mix(in srgb, var(--info) 16%, transparent); color: var(--info-text, var(--info)); }

	@media (max-width: 639px) {
		.search-box { max-width: none; flex: 1 1 100%; }
		.wrow { flex-direction: column; align-items: stretch; gap: 10px; }
		.wact { justify-content: flex-end; flex-wrap: wrap; }
	}
</style>
