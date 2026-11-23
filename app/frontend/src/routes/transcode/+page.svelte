<script lang="ts">
	// Transcode: library conversion progress, space savings, active jobs, history, failures, and hardware settings.
	import Button from '#lib/ui/Button.svelte';
	import EmptyState from '#lib/ui/EmptyState.svelte';
	import Icon from '#lib/ui/Icon.svelte';
	import LoadError from '#lib/ui/LoadError.svelte';
	import PageLoader from '#lib/ui/PageLoader.svelte';
	import Poster from '#lib/ui/Poster.svelte';
	import SegmentedControl from '#lib/ui/SegmentedControl.svelte';
	import { LibraryToolPage, pager, titleArt } from '#lib/mediafiles/index.ts';
	import {
		TranscodeActiveCard,
		TranscodeFailedTab,
		TranscodeHistoryTab,
		TranscodeSettingsTab,
		TranscodeStatsBar,
		TranscodeWaitingTab,
		fileBaseName,
		formatBytes,
		formatSavedDelta,
		itemsQuery,
		itemTitle,
		parseTranscodeTab,
		resolutionLabel,
		syncActiveTranscode,
		workingCount,
		type ItemRow,
		type ItemStateFilter,
		type Overview,
		type OverviewJob,
		type TranscodeTab
	} from '#lib/transcode/index.ts';
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, message } from '#lib/core/api.ts';
	import { appLocale, t as tr } from '#lib/core/i18n/i18n.svelte.ts';
	import { live } from '#lib/core/live.svelte.ts';
	import { to } from '#lib/core/paths.ts';
	import { plainReason } from '#lib/core/plain.ts';
	import { session } from '#lib/core/session.svelte.ts';
	import { toasts } from '#lib/core/toasts.svelte.ts';

	const limit = 50;
	let over = $state<Overview | null>(null);
	let items = $state<ItemRow[]>([]);
	let total = $state(0);
	let offset = $state(0);
	let stateFilter = $state<ItemStateFilter>('pending');
	let search = $state('');
	let loaded = $state(false);
	let error = $state('');
	let pausing = $state(false);
	let scanning = $state(false);
	let queueingAll = $state(false);
	let busyFileId = $state<number | null>(null);
	let busyFileAction = $state<'transcode' | 'skip' | null>(null);
	let stoppingId = $state<number | null>(null);
	let art = $state<Map<number, string>>(new Map());
	let waitingReload = $state(0);
	let historyReload = $state(0);
	let failedReload = $state(0);
	let tablist = $state<HTMLElement>();
	let refocus = false;
	let searchTimer: ReturnType<typeof setTimeout> | undefined;

	const tab = $derived(parseTranscodeTab(page.url.searchParams.get('tab')));
	const num = (n: number) => n.toLocaleString(appLocale());
	const pages = $derived(pager(total, limit, offset));
	const work = $derived(over ? workingCount(over) : { running: 0, queued: 0 });
	const activeJobs = $derived((over?.jobs ?? []).filter((j) => j.state === 'running'));
	const canSettings = $derived(session.can('settings'));
	const historyCount = $derived((over?.counts?.done ?? 0) + (over?.counts?.skipped ?? 0));
	const failedCount = $derived(over?.failed_count ?? over?.counts?.failed ?? 0);

	const tabs = $derived<
		{ id: TranscodeTab; label: string; count: number; dot: 'fail' | null }[]
	>([
		{ id: 'overview', label: tr('transcode.tabs.overview'), count: 0, dot: null },
		{ id: 'waiting', label: tr('transcode.tabs.waiting'), count: work.queued, dot: null },
		{ id: 'history', label: tr('transcode.tabs.history'), count: historyCount, dot: null },
		{
			id: 'failed',
			label: tr('transcode.tabs.failed'),
			count: failedCount,
			dot: failedCount > 0 ? 'fail' : null
		},
		{ id: 'settings', label: tr('transcode.tabs.settings'), count: 0, dot: null }
	]);

	const stateOptions = $derived([
		{ value: 'pending' as ItemStateFilter, label: tr('transcode.filter.pending') },
		{ value: 'all' as ItemStateFilter, label: tr('transcode.filter.all') },
		{ value: 'done' as ItemStateFilter, label: tr('transcode.filter.done') },
		{ value: 'skipped' as ItemStateFilter, label: tr('transcode.filter.skipped') }
	]);

	$effect(() => {
		const active = tab;
		const list = tablist;
		if (!list) return;
		requestAnimationFrame(() => {
			const el = list.querySelector<HTMLElement>(`#tc-tab-${active}`);
			el?.scrollIntoView({ inline: 'nearest', block: 'nearest' });
			if (refocus) {
				refocus = false;
				el?.focus();
			}
		});
	});

	function setTab(next: TranscodeTab) {
		const u = new URL(page.url.href);
		if (next === 'overview') u.searchParams.delete('tab');
		else u.searchParams.set('tab', next);
		void goto(u.pathname + u.search, { reset: false, replace: true });
	}

	function tabKey(e: KeyboardEvent) {
		const i = tabs.findIndex((tb) => tb.id === tab);
		const n =
			e.key === 'ArrowRight'
				? i + 1
				: e.key === 'ArrowLeft'
					? i - 1
					: e.key === 'Home'
						? 0
						: e.key === 'End'
							? tabs.length - 1
							: -1;
		if (n < 0 || n >= tabs.length || n === i) return;
		e.preventDefault();
		refocus = true;
		tablist?.querySelector<HTMLElement>(`#tc-tab-${tabs[n].id}`)?.focus();
		setTab(tabs[n].id);
	}

	const isAbort = (e: unknown) => e instanceof DOMException && e.name === 'AbortError';

	let seq = 0;
	let loadingNow = false;
	let pendingReload = false;
	let listLoaded = $state(false);
	let alive = true;
	let mountCtrl: AbortController | undefined;

	function refreshFromEvent() {
		if (!alive) return;
		if (loadingNow) {
			pendingReload = true;
			return;
		}
		void load();
	}

	async function load() {
		if (!alive) return;
		const n = ++seq;
		loadingNow = true;
		pendingReload = false;
		const signal = mountCtrl?.signal;
		const q = itemsQuery({ state: stateFilter, q: search, limit, offset });
		const overviewP = api.get<Overview>('/transcode/overview', signal).then(
			(value) => {
				if (!alive || (n !== seq && loaded)) return;
				over = value;
				syncActiveTranscode(value);
				error = '';
				loaded = true;
			},
			(err) => {
				if (!alive || isAbort(err)) return;
				if (!over && n === seq) {
					error = message(err);
					loaded = true;
				}
			}
		);
		const listP = api.get<{ items: ItemRow[]; total: number }>(`/transcode/items?${q}`, signal).then(
			(value) => {
				if (!alive || n !== seq) return;
				items = value.items ?? [];
				total = value.total ?? 0;
				listLoaded = true;
			},
			() => {
				if (!alive || n !== seq) return;
				listLoaded = true;
			}
		);
		try {
			await Promise.all([overviewP, listP]);
		} finally {
			if (n === seq) loadingNow = false;
			if (alive && pendingReload && !loadingNow) {
				pendingReload = false;
				void load();
			}
		}
	}

	async function loadJobs() {
		if (!alive) return;
		try {
			const o = await api.get<Overview>('/transcode/overview', mountCtrl?.signal);
			if (!alive) return;
			over = o;
			syncActiveTranscode(o);
			if (!loaded) {
				error = '';
				loaded = true;
			}
		} catch {
			// The next tick tries again.
		}
	}

	onMount(() => {
		alive = true;
		mountCtrl = new AbortController();
		void load();
		void titleArt().then((m) => {
			if (alive) art = m;
		});
		let timer: ReturnType<typeof setTimeout> | undefined;
		const soon = () => {
			clearTimeout(timer);
			timer = setTimeout(refreshFromEvent, 600);
		};
		let jobTimer: ReturnType<typeof setTimeout> | undefined;
		const jobsSoon = () => {
			if (jobTimer) return;
			jobTimer = setTimeout(() => {
				jobTimer = undefined;
				void loadJobs();
			}, 1000);
		};
		let waitTimer: ReturnType<typeof setTimeout> | undefined;
		const waitSoon = () => {
			clearTimeout(waitTimer);
			waitTimer = setTimeout(() => {
				waitingReload++;
			}, 500);
		};
		const tick = setInterval(() => {
			if (work.running + work.queued > 0) void loadJobs();
		}, 5000);
		const offs = [
			live.on('job', (d) => {
				const j = d as {
					kind?: string;
					Kind?: string;
					state?: string;
					State?: string;
					progress?: number;
					Progress?: number;
				};
				const kind = String(j?.kind ?? j?.Kind ?? '');
				if (!kind.startsWith('transcode_')) return;
				const state = String(j?.state ?? j?.State ?? '');
				const progress = Number(j?.progress ?? j?.Progress ?? 0);
				if (['done', 'failed', 'cancelled'].includes(state)) {
					soon();
					waitSoon();
					historyReload++;
					failedReload++;
				} else {
					jobsSoon();
					if (state === 'queued' || (state === 'running' && (!Number.isFinite(progress) || progress <= 0.05))) {
						waitSoon();
					}
				}
			}),
			live.on('event', (d) => {
				const kind = String((d as { Kind?: string })?.Kind ?? '');
				if (kind === 'transcode_done' || kind.startsWith('transcode_')) {
					soon();
					waitSoon();
					historyReload++;
				}
			}),
			live.on('resync', () => {
				soon();
				waitSoon();
				historyReload++;
				failedReload++;
			})
		];
		return () => {
			alive = false;
			mountCtrl?.abort();
			clearTimeout(timer);
			clearTimeout(jobTimer);
			clearTimeout(waitTimer);
			clearTimeout(searchTimer);
			clearInterval(tick);
			offs.forEach((off) => off());
		};
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

	async function togglePause() {
		if (!over || pausing) return;
		pausing = true;
		try {
			const res = await api.put<{ paused: boolean }>('/transcode/pause', { paused: !over.paused });
			over = { ...over, paused: res.paused };
			syncActiveTranscode(over);
			void loadJobs();
		} catch (e) {
			toasts.push(tr('transcode.actions.pauseFailed'), 'fail', { body: message(e) });
		} finally {
			pausing = false;
		}
	}

	async function scanLibrary() {
		if (scanning) return;
		scanning = true;
		try {
			const res = await api.post<{ job_id: number; queued?: boolean; created?: boolean }>('/transcode/scan', {
				queue_missing: false
			});
			toasts.push(
				res.queued || res.created ? tr('transcode.actions.scanStarted') : tr('transcode.actions.scanRunning'),
				'info'
			);
			void loadJobs();
		} catch (e) {
			toasts.push(tr('transcode.actions.scanFailed'), 'fail', { body: message(e) });
		} finally {
			scanning = false;
		}
	}

	async function queueAll() {
		if (queueingAll) return;
		queueingAll = true;
		try {
			const res = await api.post<{ queued: number }>('/transcode/queue-all', {});
			toasts.push(
				tr(
					res.queued === 1 ? 'transcode.actions.queuedAll.one' : 'transcode.actions.queuedAll.other',
					{ n: num(res.queued) }
				),
				'info'
			);
			waitingReload++;
			void load();
		} catch (e) {
			toasts.push(tr('transcode.actions.queueAllFailed'), 'fail', { body: message(e) });
		} finally {
			queueingAll = false;
		}
	}

	async function stopJob(j: OverviewJob) {
		if (stoppingId !== null) return;
		stoppingId = j.id;
		try {
			await api.post(`/transcode/jobs/${j.id}/stop`);
			const label = itemTitle(j) || fileBaseName(j.relative_path);
			toasts.push(tr('transcode.jobs.stopped'), 'info', label ? { body: label } : undefined);
			await loadJobs();
			waitingReload++;
			failedReload++;
		} catch (e) {
			toasts.push(tr('transcode.jobs.stopFailed'), 'fail', { body: message(e) });
		} finally {
			stoppingId = null;
		}
	}

	async function transcodeOne(item: ItemRow) {
		if (busyFileId !== null) return;
		busyFileId = item.file_id;
		busyFileAction = 'transcode';
		try {
			const force = item.state === 'done' || item.state === 'skipped';
			await api.post(`/files/${item.file_id}/transcode`, { force });
			toasts.push(tr('transcode.actions.queuedFile'), 'info', {
				body: itemTitle(item) || fileBaseName(item.relative_path)
			});
			waitingReload++;
			void load();
		} catch (e) {
			toasts.push(tr('transcode.actions.queueFailed'), 'fail', { body: message(e) });
		} finally {
			busyFileId = null;
			busyFileAction = null;
		}
	}

	async function skipFile(item: ItemRow) {
		if (busyFileId !== null) return;
		busyFileId = item.file_id;
		busyFileAction = 'skip';
		try {
			await api.post<{ skipped: boolean; file_id: number }>(`/files/${item.file_id}/transcode/skip`);
			toasts.push(tr('transcode.actions.skippedFile'), 'info', {
				body: itemTitle(item) || fileBaseName(item.relative_path)
			});
			waitingReload++;
			historyReload++;
			void load();
		} catch (e) {
			toasts.push(tr('transcode.actions.skipFailed'), 'fail', { body: message(e) });
		} finally {
			busyFileId = null;
			busyFileAction = null;
		}
	}
</script>

<svelte:head><title>{tr('transcode.page.title')} · HolyArrs</title></svelte:head>

<LibraryToolPage
	title={tr('transcode.page.title')}
	count={over ? tr('transcode.page.count', { n: num(over.counts.pending) }) : ''}
>
	{#snippet bar()}
		<span class="tp-gap"></span>
		{#if over}
			<span class="tp-cap tc-cap">
				<Button
					icon={over.paused ? 'play' : 'pause'}
					onclick={togglePause}
					busy={pausing}
					aria-pressed={over.paused}
					data-testid="transcode-pause"
				>{over.paused ? tr('transcode.actions.resume') : tr('transcode.actions.pause')}</Button>
			</span>
		{/if}
		<span class="tp-cap tc-cap">
			<Button
				icon="search"
				onclick={scanLibrary}
				busy={scanning}
				disabled={!over}
				data-testid="transcode-scan"
			>{tr('transcode.actions.scan')}</Button>
		</span>
		<span class="tp-cap tc-cap">
			<Button
				variant="primary"
				icon="play"
				onclick={queueAll}
				busy={queueingAll}
				disabled={!over || !over.ready || over.counts.pending === 0}
				data-testid="transcode-queue-all"
			>{tr('transcode.actions.queueAll')}</Button>
		</span>
	{/snippet}

	{#if error && !over}
		<LoadError hasData={false} message={plainReason(error).text} onretry={load} />
	{:else}
		<PageLoader loading={!loaded} label={tr('transcode.page.loading')} onretry={() => void load()}>
			{#if over}
				<!-- svelte-ignore a11y_no_static_element_interactions -->
				<div class="tabrow" onkeydown={tabKey}>
					<div class="tabs" role="tablist" aria-label={tr('transcode.tabs.label')} bind:this={tablist}>
						{#each tabs as tb (tb.id)}
							<div class="pw" class:on={tab === tb.id}>
								<button
									type="button"
									role="tab"
									id="tc-tab-{tb.id}"
									class="tab"
									aria-selected={tab === tb.id}
									aria-controls={tab === tb.id ? `tc-panel-${tb.id}` : undefined}
									tabindex={tab === tb.id ? 0 : -1}
									data-testid="transcode-tab-{tb.id}"
									onclick={() => setTab(tb.id)}
								>
									<span class="lb">{tb.label}</span>
									{#if tb.count > 0}<b class="tcount">{num(tb.count)}</b>{/if}
									{#if tb.dot}<i class="badge {tb.dot}" aria-hidden="true"></i>{/if}
								</button>
							</div>
						{/each}
					</div>
				</div>

				<div class="tabpanel" role="tabpanel" id="tc-panel-{tab}" aria-labelledby="tc-tab-{tab}">
					{#if tab === 'overview'}
						{#if !over.ready}
							<div class="tp-msg warn-msg" role="status" data-testid="transcode-not-ready">
								<Icon name="alert" size={16} />
								<span class="msg-text">
									{#if !over.ffmpeg_found || !over.ffprobe_found}
										{tr('transcode.banner.noFfmpeg')}
									{:else if over.encoders.length === 0}
										{tr('transcode.banner.noEncoders')}
									{:else}
										{tr('transcode.banner.notReady')}
									{/if}
								</span>
								<Button variant="plain" size="sm" onclick={() => setTab('settings')}>
									{tr('transcode.banner.openSettings')}
								</Button>
							</div>
						{/if}

						{#if over.paused}
							<p class="tp-msg" role="status" data-testid="transcode-paused">
								<Icon name="pause" size={16} />
								{tr('transcode.page.pausedNote')}
							</p>
						{/if}

						<TranscodeStatsBar overview={over} />

						<TranscodeActiveCard
							{activeJobs}
							runningCount={work.running}
							queuedCount={work.queued}
							{art}
							{stoppingId}
							onstop={stopJob}
							onopenwaiting={() => setTab('waiting')}
						/>

						<div class="filter-row">
							<SegmentedControl
								options={stateOptions}
								bind:value={stateFilter}
								label={tr('transcode.filter.label')}
								onchange={changeFilter}
								testid="transcode-state-filter"
							/>
							<div class="search-box">
								<input
									type="search"
									class="input"
									bind:value={search}
									oninput={onSearchInput}
									placeholder={tr('transcode.searchPlaceholder')}
									aria-label={tr('transcode.searchPlaceholder')}
									data-testid="transcode-search"
								/>
							</div>
						</div>

						{#if listLoaded && total === 0}
							<EmptyState
								compact
								icon="transcode"
								title={search.trim()
									? tr('transcode.emptySearch')
									: stateFilter === 'pending'
										? tr('transcode.emptyPending')
										: tr('transcode.emptyFiltered')}
							/>
						{:else if total > 0}
							<section class="tp-sheet" aria-label={tr('transcode.queue.title')}>
								<ul class="tp-rows">
									{#each items as item (item.file_id)}
										{@const name = itemTitle(item) || fileBaseName(item.relative_path)}
										{@const resLabel = resolutionLabel(item.source_width, item.source_height)}
										{@const delta = formatSavedDelta(item.input_bytes, item.output_bytes, item.saved_bytes)}
										<li class="frow" data-testid="transcode-item-row" data-state={item.state}>
											<div class="fmain">
												<span class="tp-art" style="--poster-ratio: 2 / 3">
													<Poster src={art.get(item.title_id)} {name} />
												</span>
												<div class="finfo">
													<div class="fhead">
														{#if item.title_id}
															<a class="fname" href={to(`/library/${item.title_id}`)}>{name}</a>
														{:else}
															<span class="fname">{name}</span>
														{/if}
														{#if stateFilter !== 'pending' || item.state !== 'pending'}
															<span
																class="pill"
																class:ok={item.state === 'done'}
																class:wait={item.state === 'queued' || item.state === 'running' || item.state === 'skipped'}
																class:fail={item.state === 'failed'}
															>{tr(`transcode.state.${item.state}`)}</span>
														{/if}
														{#if item.state === 'done' && delta.positive}
															<span class="pill ok" data-testid="transcode-delta-pill">-{delta.percentText} · {delta.savedText}</span>
														{:else if item.state === 'done' && delta.negative}
															<span class="pill fail" data-testid="transcode-delta-pill">+{delta.percentText} · +{delta.savedText}</span>
														{/if}
														{#if item.fileflows_tagged}
															<span class="pill">{tr('transcode.badge.tagged')}</span>
														{/if}
													</div>
													{#if item.relative_path}
														<p class="fpath">{item.relative_path}</p>
													{/if}
													<div class="jpills">
														{#if item.profile_name}<span class="pill">{item.profile_name}</span>{/if}
														{#if item.source_codec}<span class="pill">{item.source_codec}</span>{/if}
														{#if resLabel}<span class="pill">{resLabel}</span>{/if}
														{#if item.source_hdr}<span class="pill">{item.source_hdr}</span>{/if}
														{#if item.state === 'done' && item.output_bytes > 0}
															<span class="pill">{formatBytes(item.input_bytes)} → {formatBytes(item.output_bytes)}</span>
														{:else if item.input_bytes > 0}
															<span class="pill">{formatBytes(item.input_bytes)}</span>
														{/if}
													</div>
												</div>
											</div>
											<div class="fact">
												{#if item.state === 'pending'}
													<Button
														variant="plain"
														size="sm"
														icon="x"
														busy={busyFileId === item.file_id && busyFileAction === 'skip'}
														disabled={busyFileId !== null}
														onclick={() => skipFile(item)}
														data-testid="transcode-skip-file-btn"
													>{tr('transcode.actions.skip')}</Button>
												{/if}
												<Button
													size="sm"
													icon="play"
													busy={busyFileId === item.file_id && busyFileAction === 'transcode'}
													disabled={busyFileId !== null || item.state === 'running' || item.state === 'queued'}
													onclick={() => transcodeOne(item)}
													data-testid="transcode-file-btn"
												>{tr('transcode.actions.transcode')}</Button>
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
					{:else if tab === 'waiting'}
						<TranscodeWaitingTab {art} reloadSignal={waitingReload} onchanged={() => void load()} />
					{:else if tab === 'history'}
						<TranscodeHistoryTab {art} reloadSignal={historyReload} onchanged={() => void load()} />
					{:else if tab === 'failed'}
						<TranscodeFailedTab {art} reloadSignal={failedReload} onchanged={() => void loadJobs()} />
					{:else}
						<TranscodeSettingsTab overview={over} {canSettings} onchanged={() => void loadJobs()} />
					{/if}
				</div>
			{/if}
		</PageLoader>
	{/if}
</LibraryToolPage>

<style>
	.tabrow { display: flex; align-items: center; min-width: 0; overflow-x: auto; scrollbar-width: none; }
	.tabrow::-webkit-scrollbar { display: none; }
	.tabs {
		display: flex; flex: none; align-items: center; gap: 2px; width: max-content; padding: 2px; border-radius: 999px; background: var(--surface-2);
		--hl-bg: var(--seg-thumb); --hl-fg: var(--text); --hl-shadow: 0 0 0 1px var(--line-strong), 0 1px 2px rgb(0 0 0 / 0.12), 0 2px 8px rgb(0 0 0 / 0.08);
	}
	.pw { display: flex; align-items: center; flex: none; border-radius: 999px; background: transparent; transition: background-color 200ms var(--ease-out), box-shadow 200ms var(--ease-out); }
	.pw.on { background: var(--hl-bg); box-shadow: var(--hl-shadow, none); }
	.tab { display: inline-flex; align-items: center; flex: none; gap: 6px; height: 30px; padding: 0 14px; border: 0; border-radius: 999px; background: none; color: color-mix(in srgb, var(--text) 76%, var(--bg)); font: var(--t-footnote); font-weight: 500; white-space: nowrap; cursor: pointer; transition: color 200ms var(--ease-out); }
	.tab .tcount { color: inherit; font: 600 12px/1 var(--font-ui); font-variant-numeric: tabular-nums; }
	.tab:hover { color: var(--text); }
	.pw.on .tab { color: var(--hl-fg); font-weight: 600; }
	.tab:focus-visible { outline: none; box-shadow: var(--focus-ring); }
	.badge { width: 7px; height: 7px; border-radius: 50%; background: var(--fail); flex: none; }
	.tabpanel { display: flex; flex-direction: column; gap: 14px; min-width: 0; }

	.warn-msg { border: 1px solid color-mix(in srgb, var(--wait) 40%, transparent); }
	.msg-text { flex: 1; min-width: 0; }

	.frow { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px 16px; min-width: 0; }
	.fmain { display: flex; align-items: flex-start; gap: 12px; min-width: 0; flex: 1 1 auto; }
	.finfo { display: flex; flex-direction: column; gap: 5px; min-width: 0; flex: 1 1 auto; }
	.fhead { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 8px; min-width: 0; }
	.fname { color: var(--text); font: 600 14px/1.3 var(--font-ui); text-decoration: none; overflow-wrap: anywhere; }
	a.fname:hover { text-decoration: underline; text-underline-offset: 3px; }
	.fpath { margin: 0; color: var(--text-muted); font: var(--t-footnote); overflow-wrap: anywhere; }
	.jpills { display: flex; flex-wrap: wrap; gap: 5px; }
	.fact { flex: none; display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }

	.filter-row { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 10px; margin-top: 4px; }
	.search-box { flex: 1 1 220px; max-width: 340px; min-width: 180px; }
	.search-box .input { min-height: 36px; height: 36px; }

	.pill { flex: none; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 999px; font: 600 11.5px/1 var(--font-ui); font-variant-numeric: tabular-nums; background: color-mix(in srgb, var(--text) 8%, transparent); color: color-mix(in srgb, var(--text) 82%, var(--bg)); }
	.pill.ok { background: color-mix(in srgb, var(--ok) 16%, transparent); color: var(--ok-text, var(--ok)); }
	.pill.wait { background: color-mix(in srgb, var(--wait, var(--accent)) 16%, transparent); color: var(--wait-text, var(--wait, var(--text))); }
	.pill.fail { background: color-mix(in srgb, var(--fail) 16%, transparent); color: var(--fail-text, var(--fail)); }

	@media (max-width: 639px) {
		.tc-cap :global(.ui-btn) { width: 100%; justify-content: center; }
		.search-box { max-width: none; flex: 1 1 100%; }
		.frow { flex-direction: column; align-items: stretch; gap: 10px; }
		.fact { justify-content: flex-end; }
	}
	@media (pointer: coarse) {
		.tab { height: 36px; }
	}
	@media (prefers-reduced-motion: reduce) {
		.pw, .tab { transition: none; }
	}
</style>
