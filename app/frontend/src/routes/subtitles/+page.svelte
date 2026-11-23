<script lang="ts">
	// Subtitles: coverage, speed metrics, active/waiting/history/failed jobs, integrity checks, and what is still missing.
	import PageLoader from '#lib/ui/PageLoader.svelte';
	import Poster from '#lib/ui/Poster.svelte';
	import Button from '#lib/ui/Button.svelte';
	import EmptyState from '#lib/ui/EmptyState.svelte';
	import LoadError from '#lib/ui/LoadError.svelte';
	import ProgressBar from '#lib/ui/ProgressBar.svelte';
	import SegmentedControl from '#lib/ui/SegmentedControl.svelte';
	import Icon from '#lib/ui/Icon.svelte';
	import { titleArt, pager, LibraryToolPage } from '#lib/mediafiles/index.ts';
	import {
		SubtitlesActiveCard, SubtitlesFailedTab, SubtitlesHistoryTab, SubtitlesIntegrityTab, SubtitlesSettingsTab,
		SubtitlesStatsBar, SubtitlesWaitingTab, fileBaseName, jobName, jobWord, langName, missingName, missingNeedQuery,
		missingWords, nextStep, parseSubtitlesTab, siteState, siteUse, siteUseWords, speechState, stepWords,
		syncActiveSubtitles, wantLabel, workingCount,
		type Language, type Need, type NeedRow, type Overview, type OverviewJob, type SiteKind, type SiteView,
		type SubtitlesTab
	} from '#lib/subtitles/index.ts';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { to } from '#lib/core/paths.ts';
	import { api, message } from '#lib/core/api.ts';
	import { dateTime } from '#lib/core/format.ts';
	import { live } from '#lib/core/live.svelte.ts';
	import { session } from '#lib/core/session.svelte.ts';
	import { toasts } from '#lib/core/toasts.svelte.ts';
	import { plainReason } from '#lib/core/plain.ts';
	import { appLocale, t as tr } from '#lib/core/i18n/i18n.svelte.ts';

	const limit = 50;
	let over = $state<Overview | null>(null);
	let sites = $state<SiteView[]>([]);
	let kinds = $state<SiteKind[]>([]);
	let langs = $state<Language[]>([]);
	let rows = $state<NeedRow[]>([]);
	let total = $state(0);
	let offset = $state(0);
	let need = $state<Need>('');
	let language = $state('');
	let loaded = $state(false);
	let error = $state('');
	let busyRow = $state('');
	let busyAction = $state<'search' | 'transcribe' | 'skip' | null>(null);
	let stoppingId = $state<number | null>(null);
	let searchingAll = $state(false);
	let pausing = $state(false);
	let art = $state<Map<number, string>>(new Map());
	let waitingReload = $state(0);
	let historyReload = $state(0);
	let failedReload = $state(0);
	let integrityReload = $state(0);
	let tablist = $state<HTMLElement>();
	let refocus = false;

	const tab = $derived(parseSubtitlesTab(page.url.searchParams.get('tab')));
	const num = (n: number) => n.toLocaleString(appLocale());
	const pages = $derived(pager(total, limit, offset));
	const work = $derived(over ? workingCount(over) : { running: 0, queued: 0 });
	const activeJobs = $derived((over?.jobs ?? []).filter((j) => j.state === 'running'));
	const historyCount = $derived(
		(over?.done_counts?.downloaded ?? 0) +
			(over?.done_counts?.translated ?? 0) +
			(over?.done_counts?.speech ?? 0) +
			(over?.skipped_count ?? 0)
	);
	const speech = $derived(over ? speechState(over) : 'notHere');
	const canSettings = $derived(session.can('settings'));
	const activeTab = $derived<SubtitlesTab>(tab === 'settings' && !canSettings ? 'overview' : tab);
	const verifying = $derived(
		!!over && ((over.running?.subtitle_verify ?? 0) > 0 || (over.queued?.subtitle_verify ?? 0) > 0 || over.jobs.some((j) => j.kind === 'subtitle_verify'))
	);
	const tabs = $derived<{ id: SubtitlesTab; label: string; count: number; dot: 'fail' | 'wait' | null }[]>([
		{ id: 'overview', label: tr('subtitles.tabs.overview'), count: 0, dot: null },
		{ id: 'waiting', label: tr('subtitles.tabs.waiting'), count: work.queued, dot: null },
		{ id: 'history', label: tr('subtitles.tabs.history'), count: historyCount, dot: null },
		{ id: 'failed', label: tr('subtitles.tabs.failed'), count: over?.failed_count ?? 0, dot: (over?.failed_count ?? 0) > 0 ? 'fail' : null },
		{ id: 'integrity', label: tr('subtitles.tabs.integrity'), count: over?.integrity?.bad ?? 0, dot: (over?.integrity?.bad ?? 0) > 0 ? 'wait' : null },
		...(canSettings ? [{ id: 'settings' as const, label: tr('subtitles.tabs.settings'), count: 0, dot: null }] : [])
	]);
	const needOptions = $derived([
		{ value: '' as Need, label: tr('subtitles.page.need.all') },
		{ value: 'speech' as Need, label: tr('subtitles.page.job.transcribe') },
		{ value: 'text' as Need, label: tr('subtitles.page.need.text') }
	]);

	$effect(() => {
		const active = activeTab;
		const list = tablist;
		if (!list) return;
		requestAnimationFrame(() => {
			const el = list.querySelector<HTMLElement>(`#sub-tab-${active}`);
			el?.scrollIntoView({ inline: 'nearest', block: 'nearest' });
			if (refocus) {
				refocus = false;
				el?.focus();
			}
		});
	});

	function setTab(next: SubtitlesTab) {
		const u = new URL(page.url.href);
		if (next === 'overview') u.searchParams.delete('tab');
		else u.searchParams.set('tab', next);
		void goto(u.pathname + u.search, { reset: false, replace: true });
	}

	function tabKey(e: KeyboardEvent) {
		const i = tabs.findIndex((tb) => tb.id === activeTab);
		const n = e.key === 'ArrowRight' ? i + 1 : e.key === 'ArrowLeft' ? i - 1 : e.key === 'Home' ? 0 : e.key === 'End' ? tabs.length - 1 : -1;
		if (n < 0 || n >= tabs.length || n === i) return;
		e.preventDefault();
		refocus = true;
		tablist?.querySelector<HTMLElement>(`#sub-tab-${tabs[n].id}`)?.focus();
		setTab(tabs[n].id);
	}

	const isAbort = (e: unknown) => e instanceof DOMException && e.name === 'AbortError';

	// Only the newest read may write the page.
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
		const overviewP = api.get<Overview>('/subtitles/overview', signal).then(
			(value) => {
				if (!alive || (n !== seq && loaded)) return;
				over = value;
				syncActiveSubtitles(value);
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
		const listP = api
			.get<{ missing: NeedRow[]; total: number }>('/subtitles/missing?' + missingNeedQuery({ need, language }, limit, offset), signal)
			.then(
				(value) => {
					if (!alive || n !== seq) return;
					rows = value.missing;
					total = value.total;
					listLoaded = true;
				},
				() => {
					if (!alive || n !== seq) return;
					listLoaded = true;
				}
			);
		const sitesP = api.get<{ sites: SiteView[] }>('/subtitle-sites', signal).then(
			(value) => {
				if (!alive || n !== seq) return;
				sites = value.sites;
			},
			() => {}
		);
		try {
			await Promise.all([overviewP, listP, sitesP]);
		} finally {
			if (n === seq) loadingNow = false;
			if (alive && pendingReload && !loadingNow) {
				pendingReload = false;
				void load();
			}
		}
	}

	// Jobs move often; only the overview is read again for them.
	async function loadJobs() {
		if (!alive) return;
		try {
			const o = await api.get<Overview>('/subtitles/overview', mountCtrl?.signal);
			if (!alive) return;
			over = o;
			syncActiveSubtitles(o);
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
		api.get<{ languages: Language[] }>('/subtitles/languages', mountCtrl.signal).then(
			(r) => {
				if (alive) langs = r.languages;
			},
			() => {}
		);
		api.get<{ kinds: SiteKind[] }>('/subtitle-sites/kinds', mountCtrl.signal).then(
			(r) => {
				if (alive) kinds = r.kinds;
			},
			() => {}
		);
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
		// While something runs, the page keeps itself fresh even if the live stream is quiet.
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
				if (!kind.startsWith('subtitle_')) return;
				const state = String(j?.state ?? j?.State ?? '');
				const progress = Number(j?.progress ?? j?.Progress ?? 0);
				if (['done', 'failed', 'cancelled'].includes(state)) {
					soon();
					waitSoon();
					historyReload++;
					failedReload++;
					if (kind === 'subtitle_verify' || state === 'done') integrityReload++;
				} else {
					jobsSoon();
					if (state === 'queued' || (state === 'running' && (!Number.isFinite(progress) || progress <= 0.05))) {
						waitSoon();
					}
				}
			}),
			live.on('event', (d) => {
				if (String((d as { Kind?: string })?.Kind).startsWith('subtitle_')) {
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
				integrityReload++;
			})
		];
		return () => {
			alive = false;
			mountCtrl?.abort();
			clearTimeout(timer);
			clearTimeout(jobTimer);
			clearTimeout(waitTimer);
			clearInterval(tick);
			offs.forEach((off) => off());
		};
	});

	function filter() {
		offset = 0;
		void load();
	}

	function pick(n: Need) {
		need = n;
		filter();
	}

	function go(off: number | null) {
		if (off === null) return;
		offset = off;
		void load();
	}

	const rowKey = (r: NeedRow) => `${r.file_id}:${r.language}:${r.forced}`;

	async function stopJob(j: OverviewJob) {
		if (stoppingId !== null) return;
		stoppingId = j.id;
		try {
			await api.post(`/subtitles/jobs/${j.id}/stop`);
			const label = jobName(j) || fileBaseName(j.relative_path) || jobWord(j.kind);
			toasts.push(tr('subtitles.page.jobs.stopped'), 'info', label ? { body: label } : undefined);
			await loadJobs();
			waitingReload++;
			failedReload++;
		} catch (e) {
			toasts.push(tr('subtitles.page.jobs.stopFailed'), 'fail', { body: message(e) });
		} finally {
			stoppingId = null;
		}
	}

	async function searchAll() {
		if (searchingAll) return;
		searchingAll = true;
		try {
			const body: Record<string, unknown> = {};
			if (language) body.language = language;
			const r = await api.post<{ queued: number }>('/subtitles/missing/search', body);
			toasts.push(tr('shows.sheet.searchingSubs'), 'info', { body: tr('mediafiles.unit.file.' + (r.queued === 1 ? 'one' : 'other'), { n: r.queued }) });
			waitingReload++;
			void loadJobs();
		} catch (e) {
			toasts.push(tr('mediafiles.title.startFailed'), 'fail', { body: message(e) });
		} finally {
			searchingAll = false;
		}
	}

	// Pause holds searches, translations and speech to text in the queue; what runs now ends by itself.
	async function togglePause() {
		if (!over || pausing) return;
		pausing = true;
		try {
			const r = await api.put<{ paused: boolean }>('/subtitles/pause', { paused: !over.paused });
			over = { ...over, paused: r.paused };
			syncActiveSubtitles(over);
			void loadJobs();
		} catch (e) {
			toasts.push(tr('subtitles.page.pauseFailed'), 'fail', { body: message(e) });
		} finally {
			pausing = false;
		}
	}

	async function searchOne(r: NeedRow) {
		if (busyRow) return;
		busyRow = rowKey(r);
		busyAction = 'search';
		try {
			await api.post('/subtitles/search', { file_id: r.file_id });
			toasts.push(tr('shows.sheet.searchingSubs'), 'info', { body: missingName(r) });
			waitingReload++;
			void loadJobs();
		} catch (e) {
			toasts.push(tr('mediafiles.title.startFailed'), 'fail', { body: message(e) });
		} finally {
			busyRow = '';
			busyAction = null;
		}
	}

	async function transcribe(r: NeedRow) {
		if (busyRow) return;
		busyRow = rowKey(r);
		busyAction = 'transcribe';
		try {
			await api.post(`/files/${r.file_id}/subtitles/transcribe`);
			toasts.push(tr('subtitles.panel.transcribing'), 'info', { body: missingName(r) });
			waitingReload++;
			void loadJobs();
		} catch (e) {
			toasts.push(tr('subtitles.panel.transcribeFailed'), 'fail', { body: message(e) });
		} finally {
			busyRow = '';
			busyAction = null;
		}
	}

	async function skipMissing(r: NeedRow) {
		if (busyRow) return;
		busyRow = rowKey(r);
		busyAction = 'skip';
		try {
			await api.post<{ file_id: number; skipped: boolean; count: number }>(
				`/files/${r.file_id}/subtitles/skip`,
				{ language: r.language, forced: r.forced }
			);
			toasts.push(tr('subtitles.actions.skipped'), 'info', { body: missingName(r) });
			waitingReload++;
			historyReload++;
			void load();
		} catch (e) {
			toasts.push(tr('subtitles.actions.skipFailed'), 'fail', { body: message(e) });
		} finally {
			busyRow = '';
			busyAction = null;
		}
	}
</script>

<svelte:head><title>{tr('subtitles.page.title')} · HolyArrs</title></svelte:head>

<LibraryToolPage title={tr('subtitles.page.title')} count={over ? tr('subtitles.page.count', { n: num(over.missing) }) : ''}>
	{#snippet bar()}
		<span class="tp-gap"></span>
		{#if over}
			<span class="tp-cap sub-cap"><Button icon={over.paused ? 'play' : 'pause'} onclick={togglePause} busy={pausing} aria-pressed={over.paused} data-testid="subtitles-pause">{over.paused ? tr('subtitles.page.resume') : tr('subtitles.page.pause')}</Button></span>
		{/if}
		<span class="tp-cap sub-cap">
			<Button variant="primary" icon="search" onclick={searchAll} disabled={!over || over.missing === 0} busy={searchingAll} data-testid="subtitles-search-all">{tr('library.missingSubs.searchAll')}</Button>
		</span>
	{/snippet}

	{#if error && !over}
		<LoadError hasData={false} message={plainReason(error).text} onretry={load} />
	{:else}
	<PageLoader loading={!loaded} label={tr('subtitles.page.loading')} onretry={() => void load()}>
	{#if over}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div class="tabrow" onkeydown={tabKey}>
			<div class="tabs" role="tablist" aria-label={tr('subtitles.tabs.label')} bind:this={tablist}>
				{#each tabs as tb (tb.id)}
					<div class="pw" class:on={activeTab === tb.id}>
						<button
							type="button"
							role="tab"
							id="sub-tab-{tb.id}"
							class="tab"
							aria-selected={activeTab === tb.id}
							aria-controls={activeTab === tb.id ? `sub-panel-${tb.id}` : undefined}
							tabindex={activeTab === tb.id ? 0 : -1}
							data-testid="subtitles-tab-{tb.id}"
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

		<div class="tabpanel" role="tabpanel" id="sub-panel-{activeTab}" aria-labelledby="sub-tab-{activeTab}">
			{#if activeTab === 'overview'}
				{#if over.paused}<p class="tp-msg" role="status" data-testid="subtitles-paused"><Icon name="pause" size={16} />{tr('subtitles.page.pausedNote')}</p>{/if}

				<SubtitlesStatsBar overview={over} />

				<SubtitlesActiveCard
					{activeJobs}
					runningCount={work.running}
					queuedCount={work.queued}
					{langs}
					{art}
					{stoppingId}
					onstop={stopJob}
					onopenwaiting={() => setTab('waiting')}
				/>

				<section class="tp-sheet" aria-labelledby="sub-sites">
					<h2 id="sub-sites" class="head">{tr('subtitles.page.sites.title')}</h2>
					{#if sites.length === 0}
						<p class="tp-note pad" data-testid="subtitles-no-sites">{tr('subtitles.page.sites.none')}</p>
					{:else}
						<ul class="sites-grid">
							{#each sites as s (s.ID)}
								{@const st = siteState(s, kinds.find((k) => k.kind === s.kind), dateTime)}
								{@const use = siteUse(s)}
								<li class="site-tile" data-testid="subtitles-site">
									<div class="jtop">
										<span class="jname">{s.name}</span>
										<span class="pill {st.tone}">{st.label}</span>
									</div>
									<p class="tp-note">{st.text}</p>
									<p class="tp-note">{siteUseWords(s)}</p>
									{#if use.cap > 0}<ProgressBar value={use.percent} size="sm" tone={use.percent >= 100 ? 'info' : 'accent'} label={tr('subtitles.page.sites.usage', { name: s.name })} />{/if}
								</li>
							{/each}
						</ul>
					{/if}
				</section>

				<div class="tiles" data-testid="subtitles-tiles">
					<button type="button" class="tile tp-sheet" class:on={need === ''} aria-pressed={need === ''} onclick={() => pick('')} data-testid="tile-missing">
						<span class="lab"><Icon name="subtitle" size={16} />{tr('subtitles.page.tile.missing')}</span>
						<span class="n">{num(over.missing)}</span>
						<span class="note">{tr('subtitles.page.tile.files', { n: num(over.files) })}</span>
					</button>
					<button type="button" class="tile tp-sheet" class:on={need === 'speech'} aria-pressed={need === 'speech'} onclick={() => pick('speech')} data-testid="tile-speech">
						<span class="lab"><Icon name="music" size={16} />{tr('subtitles.page.tile.speech')}</span>
						<span class="n">{num(over.need_speech)}</span>
						{#if speech !== 'auto'}<span class="note warn">{tr(`subtitles.page.speech.${speech}`)}</span>{/if}
					</button>
					<button type="button" class="tile tp-sheet" class:on={need === 'text'} aria-pressed={need === 'text'} onclick={() => pick('text')} data-testid="tile-translate">
						<span class="lab"><Icon name="sparkle" size={16} />{tr('subtitles.page.tile.translate')}</span>
						<span class="n">{num(over.can_translate)}</span>
						<span class="note" class:warn={!over.translator_ready}>{over.translator_ready ? tr('subtitles.page.translate.with', { name: over.translator_name }) : tr('subtitles.page.translate.none')}</span>
					</button>
					<div class="tile tp-sheet still" data-testid="tile-working">
						<span class="lab"><Icon name="clock" size={16} />{tr('subtitles.page.tile.working')}</span>
						<span class="n">{num(work.running)}</span>
						<span class="note">{tr('subtitles.page.tile.waiting', { n: num(work.queued) })}</span>
					</div>
				</div>

				<div class="listbar">
					<SegmentedControl options={needOptions} bind:value={need} label={tr('subtitles.page.need.label')} onchange={filter} testid="subtitles-need" />
					<span class="tp-cap sub-cap">
						<select aria-label={tr('library.missingSubs.language')} bind:value={language} onchange={filter}>
							<option value="">{tr('library.missingSubs.every')}</option>
							{#each langs as l (l.code)}<option value={l.code}>{l.name}</option>{/each}
						</select>
					</span>
				</div>

				{#if listLoaded && total === 0}
					<EmptyState compact icon="subtitle" title={need || language ? tr('library.copies.nothingMatches') : tr('mediafiles.list.nothingMissing')} />
				{:else if total > 0}
					<div class="tp-sheet table-wrap">
						<table class="tp-table missing">
							<caption class="sr-only">{tr('library.missingSubs.caption')}</caption>
							<thead><tr><th scope="col">{tr('mediafiles.list.title')}</th><th scope="col">{tr('library.missingSubs.language')}</th><th scope="col">{tr('subtitles.page.next')}</th><th scope="col"><span class="sr-only">{tr('mediafiles.dialogs.search')}</span></th></tr></thead>
							<tbody>
								{#each rows as r (rowKey(r))}
									{@const step = nextStep(r, over)}
									{@const isBusy = busyRow === rowKey(r)}
									<tr data-testid="subtitles-missing-row">
										<td><span class="tp-who"><span class="tp-art" style="--poster-ratio: 2 / 3"><Poster src={art.get(r.title_id)} name={missingName(r)} /></span><a href={to(`/library/${r.title_id}`)}>{missingName(r)}</a></span></td>
										<td>{wantLabel({ language: r.language, hearing_impaired: r.hearing_impaired ? 'hi' : 'normal', forced: r.forced }, langs)}</td>
										<td class="status"><span class="step" class:warn={step === 'speechOff' || step === 'speechFinal' || step === 'speechFailed'} data-testid="subtitles-step" data-step={step}>{stepWords(step, r.speech_reason)}</span><span class="tp-sub">{missingWords(r, dateTime)}</span></td>
										<td class="act">
											{#if !r.has_text && over.transcriber.ready}
												<Button variant="secondary" size="sm" busy={isBusy && busyAction === 'transcribe'} disabled={busyRow !== ''} onclick={() => transcribe(r)} data-testid="subtitles-transcribe">{tr('subtitles.panel.makeFromAudio')}</Button>
											{/if}
											<Button
												variant="plain"
												size="sm"
												icon="x"
												busy={isBusy && busyAction === 'skip'}
												disabled={busyRow !== ''}
												onclick={() => skipMissing(r)}
												aria-label={tr('subtitles.actions.skipMissingFor', { language: langName(r.language, langs), name: missingName(r) })}
												data-testid="subtitles-skip-missing"
											>{tr('subtitles.actions.skip')}</Button>
											<Button
												size="sm"
												icon="search"
												busy={isBusy && busyAction === 'search'}
												disabled={busyRow !== ''}
												onclick={() => searchOne(r)}
												aria-label={tr('library.missingSubs.searchFor', { language: langName(r.language, langs), name: missingName(r) })}
												data-testid="subtitles-search">{tr('mediafiles.dialogs.search')}</Button
											>
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
					{#if pages.prev !== null || pages.next !== null}
						<nav class="tp-foot" aria-label={tr('mediafiles.releaseList.pages')}>
							<Button icon="chevronLeft" disabled={pages.prev === null} onclick={() => go(pages.prev)}>{tr('mediafiles.releaseList.back')}</Button>
							<span class="tp-note">{tr('library.copies.range', { from: num(pages.from), to: num(pages.to), total: num(total) })}</span>
							<Button iconAfter="chevronRight" disabled={pages.next === null} onclick={() => go(pages.next)}>{tr('mediafiles.releaseList.more')}</Button>
						</nav>
					{/if}
				{/if}
			{:else if activeTab === 'waiting'}
				<SubtitlesWaitingTab {langs} {art} reloadSignal={waitingReload} onchanged={() => void load()} />
			{:else if activeTab === 'history'}
				<SubtitlesHistoryTab {langs} {art} reloadSignal={historyReload} onchanged={() => void load()} />
			{:else if activeTab === 'failed'}
				<SubtitlesFailedTab {langs} {art} reloadSignal={failedReload} onchanged={() => void loadJobs()} />
			{:else if activeTab === 'settings'}
				<SubtitlesSettingsTab onchanged={() => void load()} />
			{:else}
				<SubtitlesIntegrityTab {langs} {art} fallbackSummary={over.integrity} {verifying} reloadSignal={integrityReload} onchanged={() => void load()} />
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
	.badge.wait { background: var(--wait); }
	.tabpanel { display: flex; flex-direction: column; gap: 14px; min-width: 0; }

	.head { margin: 0; padding: 16px 20px 6px; font: var(--t-headline); }
	.pad { padding: 6px 20px 16px; }

	.sites-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
		gap: 12px;
		padding: 10px 20px 18px;
		margin: 0;
		list-style: none;
	}
	.site-tile {
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
		padding: 12px 14px;
		border-radius: 14px;
		border: 1px solid var(--line);
		background: color-mix(in srgb, var(--text) 3.5%, transparent);
	}
	.site-tile .tp-note { overflow-wrap: anywhere; }
	.jtop { display: flex; align-items: flex-start; justify-content: space-between; gap: 8px 10px; min-width: 0; }
	.jname { min-width: 0; color: var(--text); font: 600 14px/1.3 var(--font-ui); overflow-wrap: anywhere; }

	.tiles { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 14px; }
	.tile { display: flex; flex-direction: column; align-items: flex-start; gap: 6px; padding: 16px 20px; border: 0; color: var(--text); text-align: start; font: inherit; cursor: pointer; transition: transform var(--dur-fast) var(--ease-out), box-shadow var(--dur-fast) var(--ease-out); }
	.tile.still { cursor: default; }
	.tile:not(.still):hover { transform: translateY(-1px); }
	.tile:focus-visible { outline: none; box-shadow: var(--focus-ring); }
	.tile.on { box-shadow: var(--edge), var(--drop), inset 0 0 0 2px var(--accent); }
	.lab { display: inline-flex; align-items: center; gap: 6px; color: var(--text-muted); font: 600 12px/1.3 var(--font-ui); }
	.tile .n { font: 700 30px/1.1 var(--font-display, var(--font-ui)); letter-spacing: -0.022em; font-variant-numeric: tabular-nums; }
	.note { color: var(--text-muted); font: var(--t-footnote); }
	.note.warn, .step.warn { color: var(--wait-text, var(--wait, var(--text-muted))); }

	.pill { flex: none; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 999px; font: 600 11.5px/1 var(--font-ui); font-variant-numeric: tabular-nums; background: color-mix(in srgb, var(--text) 8%, transparent); color: color-mix(in srgb, var(--text) 82%, var(--bg)); }
	.pill.ok { background: color-mix(in srgb, var(--ok) 16%, transparent); color: var(--ok-text, var(--ok)); }
	.pill.wait { background: color-mix(in srgb, var(--wait, var(--accent)) 16%, transparent); color: var(--wait-text, var(--wait, var(--text))); }
	.pill.fail { background: color-mix(in srgb, var(--fail) 16%, transparent); color: var(--fail-text, var(--fail)); }

	.listbar { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-top: 6px; }
	.table-wrap { overflow-x: auto; }
	.missing .tp-who a, .missing .tp-sub { overflow-wrap: anywhere; }
	.status { color: var(--text-muted); font: var(--t-footnote); }
	.step { display: block; color: var(--text); overflow-wrap: anywhere; }
	.act { width: 1%; text-align: right; white-space: nowrap; }
	.act :global(.ui-btn + .ui-btn) { margin-inline-start: 6px; }

	@media (max-width: 1023px) { .tiles { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
	@media (max-width: 639px) {
		.tabrow { overflow-x: visible; }
		.tabs { flex-wrap: wrap; width: 100%; border-radius: 16px; }
		.sub-cap :global(.ui-btn), .sub-cap select { width: 100%; justify-content: center; }
		.head { padding: 14px 16px 6px; }
		.sites-grid { grid-template-columns: minmax(0, 1fr); padding: 8px 16px 16px; }
		.tile .n { font-size: 24px; }
		.table-wrap { overflow-x: visible; }
		.missing thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
		.missing, .missing tbody { display: block; width: 100%; }
		.missing tbody tr { display: grid; grid-template-columns: minmax(0, 1fr); gap: 8px; padding: 14px 16px; box-shadow: none; }
		.missing tbody tr + tr { border-top: 1px solid var(--line); }
		.missing tbody tr + tr td { box-shadow: none; }
		.missing td { display: block; padding: 0; width: auto; }
		.missing td:nth-child(2) { color: var(--text-muted); font: var(--t-footnote); }
		.missing .act { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 6px; white-space: normal; text-align: left; width: auto; }
		.missing .act :global(.ui-btn + .ui-btn) { margin-inline-start: 0; }
	}
	@media (pointer: coarse) {
		.tab { height: 36px; }
		.missing .tp-who > a { display: inline-block; padding-block: 10px; }
	}
	@media (prefers-reduced-motion: reduce) {
		.pw, .tab, .tile { transition: none; }
		.tile:not(.still):hover { transform: none; }
	}
</style>
