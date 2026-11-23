import { expect, test, type Browser, type Page } from '@playwright/test';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { noSidewaysScroll, openAdvanced, owner, signIn } from '../helpers/helpers.ts';
import { axe as scan } from '../helpers/axe.ts';

test.describe.configure({ mode: 'serial' });

let page: Page;
const consoleErrors: string[] = [];
const pageErrors: string[] = [];

const axe = (p: Page, where: string) => scan(p, where, { waitAll: false, include: '#main' });

function trackErrors(p: Page) {
	const ce: string[] = [];
	const pe: string[] = [];
	p.on('console', (msg) => {
		if (msg.type() === 'error') ce.push(msg.text());
	});
	p.on('pageerror', (err) => {
		pe.push(err.message);
	});
	return { ce, pe };
}

async function openContext(browser: Browser, viewport: { width: number; height: number }, theme?: 'light' | 'dark') {
	const ctx = await browser.newContext({
		viewport,
		...(theme ? { colorScheme: theme } : {})
	});
	if (theme) {
		await ctx.addInitScript((t) => localStorage.setItem('holyarrs-theme', t), theme);
	}
	const p = await ctx.newPage();
	await signIn(p, owner());
	if (theme) {
		await expect(p.locator('html')).toHaveAttribute('data-theme', theme);
	}
	return p;
}

async function installQaRoutes(p: Page) {
	let waitingJobs = [
		{
			id: 9101,
			kind: 'transcode_file',
			state: 'queued',
			priority: 'normal',
			position: 1,
			file_id: 201,
			title_id: 0,
			title: 'Waiting Film One',
			media_kind: 'movie',
			relative_path: 'Waiting Film One (2024)/Waiting.Film.One.2024.mkv',
			profile_name: 'Default',
			source_codec: 'h264',
			source_width: 1920,
			source_height: 1080,
			input_bytes: 3_600_000_000,
			created_at: '2026-01-01T00:00:00Z',
			run_after: '2026-01-01T00:00:00Z'
		},
		{
			id: 9102,
			kind: 'transcode_file',
			state: 'queued',
			priority: 'normal',
			position: 2,
			file_id: 202,
			title_id: 0,
			title: 'Waiting Film Two',
			media_kind: 'movie',
			relative_path: 'Waiting Film Two (2024)/Waiting.Film.Two.2024.mkv',
			profile_name: 'Default',
			source_codec: 'hevc',
			source_width: 3840,
			source_height: 2160,
			input_bytes: 8_400_000_000,
			created_at: '2026-01-01T00:01:00Z',
			run_after: '2026-01-01T00:01:00Z'
		},
		{
			id: 9103,
			kind: 'transcode_file',
			state: 'queued',
			priority: 'normal',
			position: 3,
			file_id: 203,
			title_id: 0,
			title: 'Waiting Show',
			media_kind: 'episode',
			season: 1,
			episode: 3,
			relative_path: 'Waiting Show/Season 01/Waiting.Show.S01E03.mkv',
			profile_name: 'Default',
			source_codec: 'h264',
			source_width: 1920,
			source_height: 1080,
			input_bytes: 1_900_000_000,
			created_at: '2026-01-01T00:02:00Z',
			run_after: '2026-01-01T00:02:00Z'
		}
	];

	const doneItem = {
		file_id: 101,
		title_id: 0,
		title: 'Completed Movie',
		media_kind: 'movie',
		relative_path: 'Completed Movie (2024)/Completed.Movie.2024.mkv',
		state: 'done',
		fileflows_tagged: false,
		source_codec: 'h264',
		source_width: 1920,
		source_height: 1080,
		source_hdr: '',
		target_codec: 'av1',
		selected_q: 22,
		mean_vmaf: 94.2,
		gpu_device: '/dev/dri/renderD128',
		input_bytes: 5_000_000_000,
		output_bytes: 2_100_000_000,
		saved_bytes: 2_900_000_000,
		encode_seconds: 540,
		avg_fps: 148,
		avg_speed: 6.2,
		edl_applied: false,
		tonemapped: false,
		profile_name: 'Default',
		completed_at: '2026-01-01T00:10:00Z'
	};

	const skippedItem = {
		file_id: 102,
		title_id: 0,
		title: 'Skipped Episode',
		media_kind: 'episode',
		season: 1,
		episode: 4,
		relative_path: 'Skipped Show/Season 01/Skipped.Show.S01E04.mkv',
		state: 'skipped',
		skip_reason: 'user_skipped',
		fileflows_tagged: false,
		source_codec: 'hevc',
		source_width: 1920,
		source_height: 1080,
		source_hdr: '',
		target_codec: '',
		selected_q: null,
		mean_vmaf: null,
		gpu_device: '',
		input_bytes: 1_800_000_000,
		output_bytes: 0,
		saved_bytes: 0,
		encode_seconds: 0,
		avg_fps: 0,
		avg_speed: 0,
		edl_applied: false,
		tonemapped: false,
		profile_name: 'Default',
		completed_at: '2026-01-01T00:12:00Z'
	};

	const pendingItem = {
		file_id: 103,
		title_id: 0,
		title: 'Pending Documentary',
		media_kind: 'movie',
		relative_path: 'Pending Documentary (2025)/Pending.Documentary.2025.mkv',
		state: 'pending',
		fileflows_tagged: false,
		source_codec: 'h264',
		source_width: 1920,
		source_height: 1080,
		source_hdr: '',
		target_codec: '',
		selected_q: null,
		mean_vmaf: null,
		gpu_device: '',
		input_bytes: 3_200_000_000,
		output_bytes: 0,
		saved_bytes: 0,
		encode_seconds: 0,
		avg_fps: 0,
		avg_speed: 0,
		edl_applied: false,
		tonemapped: false,
		profile_name: 'Default'
	};

	await p.route('**/api/v1/subtitles/overview', (rt) =>
		rt.fulfill({
			status: 200,
			json: {
				missing: 4,
				files: 12,
				need_speech: 1,
				can_translate: 1,
				speech_held: 0,
				transcriber: { available: true, ready: true, model: 'medium', models: [] },
				transcribe_missing: true,
				translator_ready: true,
				translator_name: 'DeepL',
				paused: false,
				done: [],
				done_counts: { downloaded: 3, translated: 1, speech: 1 },
				running: { subtitle_transcribe: 1 },
				queued: {},
				failed_count: 0,
				jobs: [
					{
						id: 8001,
						kind: 'subtitle_transcribe',
						state: 'running',
						progress: 0.45,
						name: 'Sample Film',
						language: 'en'
					}
				]
			}
		})
	);

	await p.route('**/api/v1/transcode/overview', (rt) =>
		rt.fulfill({
			status: 200,
			json: {
				paused: false,
				auto_transcode: true,
				ready: true,
				ffmpeg_found: true,
				ffprobe_found: true,
				vmaf_available: true,
				gpus: [
					{
						path: '/dev/dri/renderD128',
						vendor: 'intel',
						name: 'Intel Arc A380',
						pci_slot: '0000:02:00.0',
						discrete: false,
						busy_jobs: 1,
						selected: true
					},
					{
						path: '/dev/dri/renderD129',
						vendor: 'intel',
						name: 'Intel Arc B580',
						pci_slot: '0000:03:00.0',
						discrete: true,
						busy_jobs: 1,
						selected: true
					}
				],
				encoders: ['av1_qsv', 'hevc_qsv', 'libsvtav1'],
				preferred_encoder: 'av1_qsv',
				counts: {
					total: 12,
					done: 4,
					skipped: 1,
					pending: 2,
					queued: waitingJobs.length,
					running: 2,
					failed: 1,
					unprobed: 0
				},
				coverage: { total: 12, completed: 5, percent: 41.7 },
				savings: { input_bytes: 20_000_000_000, output_bytes: 9_000_000_000, saved_bytes: 11_000_000_000, percent: 55, files_done: 4 },
				speed: { fps: 145, realtime_x: 6.0, live_fps: 152, live_realtime_x: 6.3, samples: 4 },
				running: { transcode_file: 2 },
				queued: { transcode_file: waitingJobs.length },
				failed_count: 1,
				jobs: [
					{
						id: 9001,
						kind: 'transcode_file',
						state: 'running',
						file_id: 42,
						title_id: 0,
						title: 'Sample Film',
						media_kind: 'movie',
						relative_path: 'Sample Film (2024)/Sample.Film.2024.1080p.mkv',
						progress: 0.62,
						phase: 'encoding',
						note: '',
						fps: 152,
						speed: 6.3,
						selected_q: 21,
						mean_vmaf: 93.8,
						encoder: 'av1_qsv',
						gpu_device: '/dev/dri/renderD128',
						input_bytes: 4_500_000_000,
						profile_name: 'Default',
						created_at: '2026-01-01T00:00:00Z'
					},
					{
						id: 9003,
						kind: 'transcode_file',
						state: 'running',
						file_id: 44,
						title_id: 0,
						title: 'Night Runner',
						media_kind: 'episode',
						season: 1,
						episode: 2,
						relative_path: 'Night Runner/Season 01/Night.Runner.S01E02.1080p.mkv',
						progress: 0.34,
						phase: 'encoding',
						note: '',
						fps: 118,
						speed: 4.9,
						selected_q: 23,
						mean_vmaf: 92.4,
						encoder: 'av1_qsv',
						gpu_device: '/dev/dri/renderD129',
						input_bytes: 2_200_000_000,
						profile_name: 'Default',
						created_at: '2026-01-01T00:01:00Z'
					}
				]
			}
		})
	);

	await p.route('**/api/v1/transcode/jobs/9001/stop', (rt) =>
		rt.fulfill({ status: 200, json: { id: 9001, state: 'cancelled' } })
	);

	await p.route('**/api/v1/transcode/waiting**', (rt) =>
		rt.fulfill({
			status: 200,
			json: {
				jobs: waitingJobs,
				total: waitingJobs.length,
				limit: 50,
				offset: 0
			}
		})
	);

	await p.route('**/api/v1/transcode/jobs/*/bump', (rt) => {
		const m = /\/jobs\/(\d+)\/bump/.exec(rt.request().url());
		const id = Number(m?.[1] ?? 0);
		const idx = waitingJobs.findIndex((j) => j.id === id);
		if (idx >= 0) {
			const [picked] = waitingJobs.splice(idx, 1);
			waitingJobs.unshift({ ...picked, priority: 'now' });
			waitingJobs = waitingJobs.map((j, i) => ({ ...j, position: i + 1 }));
		}
		return rt.fulfill({ status: 200, json: { id, priority: 'now', state: 'queued' } });
	});

	await p.route('**/api/v1/transcode/jobs/*/skip', (rt) => {
		const m = /\/jobs\/(\d+)\/skip/.exec(rt.request().url());
		const id = Number(m?.[1] ?? 0);
		const target = waitingJobs.find((j) => j.id === id);
		waitingJobs = waitingJobs
			.filter((j) => j.id !== id)
			.map((j, i) => ({ ...j, position: i + 1 }));
		return rt.fulfill({ status: 200, json: { id, skipped: true, file_id: target?.file_id ?? 0 } });
	});

	await p.route('**/api/v1/transcode/items**', (rt) => {
		const state = new URL(rt.request().url()).searchParams.get('state') ?? 'pending';
		if (state === 'done') {
			return rt.fulfill({ status: 200, json: { items: [doneItem], total: 1 } });
		}
		if (state === 'skipped') {
			return rt.fulfill({ status: 200, json: { items: [skippedItem], total: 1 } });
		}
		if (state === 'history' || state === 'all') {
			return rt.fulfill({ status: 200, json: { items: [doneItem, skippedItem], total: 2 } });
		}
		return rt.fulfill({ status: 200, json: { items: [pendingItem], total: 1 } });
	});

	await p.route('**/api/v1/transcode/failed**', (rt) =>
		rt.fulfill({
			status: 200,
			json: {
				jobs: [
					{
						id: 9002,
						kind: 'transcode_file',
						state: 'failed',
						file_id: 43,
						title_id: 0,
						title: 'Broken Clip',
						media_kind: 'movie',
						relative_path: 'Broken Clip (2023)/Broken.Clip.2023.mkv',
						progress: 0.2,
						note: '',
						error: 'encoder exited with code 1: invalid frame rate',
						attempts: 2,
						max_attempts: 3,
						created_at: '2026-01-01T00:00:00Z',
						updated_at: '2026-01-01T00:05:00Z',
						finished_at: '2026-01-01T00:05:00Z'
					}
				],
				total: 1,
				failed_count: 1,
				cancelled_count: 0
			}
		})
	);
}

async function assertAlignedGridRows(p: Page) {
	const settings = p.getByTestId('transcode-settings-tab');
	const rows = settings.locator('.two-col, .three-col, .four-col');
	const count = await rows.count();
	expect(count).toBeGreaterThan(0);

	const checked = { two: 0, three: 0, four: 0 };
	for (let i = 0; i < count; i++) {
		const row = rows.nth(i);
		if (!(await row.isVisible())) continue;

		const cls = (await row.getAttribute('class')) ?? '';
		if (cls.includes('two-col')) checked.two++;
		if (cls.includes('three-col')) checked.three++;
		if (cls.includes('four-col')) checked.four++;

		const fields = row.locator(':scope > .field');
		const fieldCount = await fields.count();
		expect(fieldCount).toBeGreaterThan(1);

		const firstFieldBox = await fields.nth(0).boundingBox();
		const firstInputBox = await fields.nth(0).locator('input, select').first().boundingBox();
		expect(firstFieldBox).not.toBeNull();
		expect(firstInputBox).not.toBeNull();

		for (let j = 1; j < fieldCount; j++) {
			const fieldBox = await fields.nth(j).boundingBox();
			const inputBox = await fields.nth(j).locator('input, select').first().boundingBox();
			expect(fieldBox).not.toBeNull();
			expect(inputBox).not.toBeNull();
			expect(Math.abs(fieldBox!.y - firstFieldBox!.y), `row ${i} field ${j} top y`).toBeLessThanOrEqual(2);
			expect(Math.abs(inputBox!.y - firstInputBox!.y), `row ${i} input ${j} top y`).toBeLessThanOrEqual(2);
		}
	}

	expect(checked.two).toBeGreaterThan(0);
	expect(checked.three).toBeGreaterThan(0);
	expect(checked.four).toBeGreaterThan(0);
}

async function settleShot(p: Page, resetScroll = true) {
	await p.evaluate(async (doReset) => {
		const hl = document.getElementById('hl');
		if (hl) {
			await Promise.all(
				hl.getAnimations({ subtree: true })
					.filter((a) => a.effect?.getComputedTiming().iterations !== Infinity)
					.map((a) => a.finished.catch(() => undefined))
			);
		}
		await Promise.all(
			document.getAnimations()
				.filter((a) => a.effect?.getComputedTiming().iterations !== Infinity)
				.map((a) => a.finished.catch(() => undefined))
		);
		if (doReset) window.scrollTo(0, 0);
	}, resetScroll);
	await p.waitForFunction(() => {
		const hl = document.getElementById('hl');
		if (!hl) return true;
		const cs = getComputedStyle(hl);
		return cs.display === 'none' || cs.visibility === 'hidden' || parseFloat(cs.opacity) <= 0.01;
	});
}

test.beforeAll(async ({ browser }) => {
	const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
	page = await ctx.newPage();
	await signIn(page, owner());
	page.on('console', (msg) => {
		if (msg.type() === 'error') consoleErrors.push(msg.text());
	});
	page.on('pageerror', (err) => {
		pageErrors.push(err.message);
	});
});

test.afterAll(async () => {
	await page.context().close();
});

test('Sidebar & navigation: Transcode link appears under Activity and opens /transcode without errors', async () => {
	const nav = page.getByRole('navigation', { name: 'Main' });
	const activity = nav.locator('div.section').filter({ has: page.getByRole('heading', { name: 'Activity' }) });
	const transcodeLink = activity.locator('a[href="/transcode"]');
	await expect(transcodeLink).toBeVisible();
	await expect(transcodeLink).toContainText('Transcode');

	await transcodeLink.click();
	await expect(page).toHaveURL(/\/transcode$/);
	await expect(page.getByRole('heading', { level: 1, name: 'Transcode' })).toBeVisible();
	await expect(page.getByTestId('transcode-stats')).toBeVisible();
	expect(consoleErrors).toEqual([]);
	expect(pageErrors).toEqual([]);
});

test('Overview tab & stats: renders 4 stat cards, persists Pause/Resume, scans library, and switches filters', async () => {
	await page.goto('/transcode');
	const stats = page.getByTestId('transcode-stats');
	await expect(stats).toBeVisible();
	await expect(stats.getByTestId('transcode-stat-coverage')).toBeVisible();
	await expect(stats.getByTestId('transcode-stat-savings')).toBeVisible();
	await expect(stats.getByTestId('transcode-stat-speed')).toBeVisible();
	await expect(stats.getByTestId('transcode-stat-hardware')).toBeVisible();

	const pauseBtn = page.getByTestId('transcode-pause');
	await expect(pauseBtn).toHaveText('Pause');
	const pauseRes = page.waitForResponse((r) => r.url().includes('/api/v1/transcode/pause') && r.request().method() === 'PUT');
	await pauseBtn.click();
	expect((await pauseRes).status()).toBe(200);
	await expect(page.getByTestId('transcode-paused')).toBeVisible();
	await expect(pauseBtn).toHaveText('Resume');

	await page.reload();
	await expect(page.getByTestId('transcode-paused')).toBeVisible();
	await expect(page.getByTestId('transcode-pause')).toHaveText('Resume');

	const resumeRes = page.waitForResponse((r) => r.url().includes('/api/v1/transcode/pause') && r.request().method() === 'PUT');
	await page.getByTestId('transcode-pause').click();
	expect((await resumeRes).status()).toBe(200);
	await expect(page.getByTestId('transcode-paused')).toHaveCount(0);
	await expect(page.getByTestId('transcode-pause')).toHaveText('Pause');

	const scanRes = page.waitForResponse((r) => r.url().includes('/api/v1/transcode/scan') && r.request().method() === 'POST');
	await page.getByTestId('transcode-scan').click();
	expect((await scanRes).status()).toBe(202);
	await expect(page.getByRole('status').filter({ hasText: /Library scan/ })).toBeVisible();

	const filter = page.getByTestId('transcode-state-filter');
	for (const label of ['All', 'Done', 'Skipped', 'Pending'] as const) {
		const itemsRes = page.waitForResponse((r) => r.url().includes('/api/v1/transcode/items') && r.request().method() === 'GET');
		const radio = filter.getByRole('radio', { name: label, exact: true });
		await radio.click();
		expect((await itemsRes).status()).toBe(200);
		await expect(radio).toHaveAttribute('aria-checked', 'true');
	}
	expect(consoleErrors).toEqual([]);
	expect(pageErrors).toEqual([]);
});

test('Settings tab: hardware probe, GPU picker, profile CRUD, per-folder assignment, and global settings SaveBar', async () => {
	await page.goto('/transcode');
	await page.getByTestId('transcode-tab-settings').click();
	await expect(page).toHaveURL(/\/transcode\?tab=settings$/);
	await expect(page.getByTestId('transcode-settings-tab')).toBeVisible();

	const hwRes = page.waitForResponse((r) => r.url().includes('/api/v1/transcode/hardware') && r.request().method() === 'GET');
	await page.getByTestId('transcode-test-hw').click();
	expect((await hwRes).status()).toBe(200);
	const hwChips = page.getByTestId('transcode-hw-status');
	await expect(hwChips).toBeVisible();
	await expect(hwChips).toContainText(/FFmpeg:/);
	await expect(hwChips).toContainText(/FFprobe:/);

	await expect(page.getByTestId('transcode-gpu-picker')).toBeVisible();
	await expect(page.getByTestId('transcode-gpu-auto')).toBeVisible();

	const defaultPill = page.getByTestId('transcode-profile-pill').filter({ hasText: 'Default' }).first();
	await expect(defaultPill).toBeVisible();
	await expect(defaultPill).toHaveClass(/active/);
	await expect(page.locator('#prof-codec')).toHaveValue('av1');
	await expect(page.locator('#prof-maxh')).toHaveValue('1080');
	await expect(page.locator('#prof-vmaf')).toHaveValue('93');
	await expect(page.locator('#prof-acodec')).toHaveValue('aac');

	await page.getByTestId('transcode-new-profile').click();
	await page.getByTestId('transcode-profile-name').fill('Anime 720p HEVC');
	await page.locator('#prof-codec').selectOption('hevc');
	await page.locator('#prof-maxh').selectOption('720');

	const createRes = page.waitForResponse((r) => r.url().includes('/api/v1/transcode/profiles') && r.request().method() === 'POST');
	await page.getByTestId('transcode-save-profile').click();
	expect((await createRes).status()).toBe(201);
	await expect(page.getByRole('status').filter({ hasText: 'Profile saved' })).toBeVisible();

	await page.reload();
	await expect(page.getByTestId('transcode-settings-tab')).toBeVisible();
	const animePill = page.getByTestId('transcode-profile-pill').filter({ hasText: 'Anime 720p HEVC' });
	await expect(animePill).toHaveCount(1);

	const folderAssignments = page.getByTestId('transcode-folder-assignments');
	await expect(folderAssignments).toBeVisible();
	const moviesRow = folderAssignments.getByTestId('transcode-folder-row').filter({ hasText: 'Movie' }).first();
	const moviesSelect = moviesRow.getByTestId('transcode-folder-select');
	await expect(moviesSelect).toBeVisible();

	const assignRes = page.waitForResponse((r) => r.url().includes('/api/v1/transcode/library-folders/') && r.request().method() === 'PUT');
	await moviesSelect.selectOption({ label: 'Anime 720p HEVC' });
	expect((await assignRes).status()).toBe(200);
	await expect(page.getByRole('status').filter({ hasText: 'Folder assignment saved' })).toBeVisible();

	await page.reload();
	const moviesSelectAfter = page.getByTestId('transcode-folder-assignments').getByTestId('transcode-folder-row').filter({ hasText: 'Movie' }).first().getByTestId('transcode-folder-select');
	await expect(moviesSelectAfter.locator('option:checked')).toHaveText('Anime 720p HEVC');

	const restoreFolderRes = page.waitForResponse((r) => r.url().includes('/api/v1/transcode/library-folders/') && r.request().method() === 'PUT');
	await moviesSelectAfter.selectOption('default');
	expect((await restoreFolderRes).status()).toBe(200);

	await page.getByTestId('transcode-profile-pill').filter({ hasText: 'Anime 720p HEVC' }).click();
	const deleteBtn = page.getByTestId('transcode-delete-profile');
	await expect(deleteBtn).toBeEnabled();
	await deleteBtn.click();
	const deleteRes = page.waitForResponse((r) => r.url().includes('/api/v1/transcode/profiles/') && r.request().method() === 'DELETE');
	await page.getByTestId('ask-yes').click();
	expect((await deleteRes).status()).toBe(200);
	await expect(page.getByTestId('transcode-profile-pill').filter({ hasText: 'Anime 720p HEVC' })).toHaveCount(0);

	const protectedDefault = page.getByTestId('transcode-profile-pill').filter({ hasText: 'Default' }).first();
	await protectedDefault.click();
	await expect(page.getByTestId('transcode-delete-profile')).toBeDisabled();

	const concurrent = page.locator('#tc-concurrent');
	await expect(concurrent).toHaveValue('2');
	await concurrent.fill('3');
	const saveBar = page.getByTestId('savebar');
	await expect(saveBar).toBeVisible();
	const saveSetRes = page.waitForResponse((r) => r.url().includes('/api/v1/transcode/settings') && r.request().method() === 'PUT');
	await saveBar.getByRole('button', { name: 'Save settings' }).click();
	expect((await saveSetRes).status()).toBe(200);
	await expect(page.getByRole('status').filter({ hasText: 'Transcode settings saved' })).toBeVisible();

	await page.reload();
	await expect(page.locator('#tc-concurrent')).toHaveValue('3');

	await page.locator('#tc-concurrent').fill('2');
	const restoreSetRes = page.waitForResponse((r) => r.url().includes('/api/v1/transcode/settings') && r.request().method() === 'PUT');
	await page.getByTestId('savebar').getByRole('button', { name: 'Save settings' }).click();
	expect((await restoreSetRes).status()).toBe(200);
	await expect(page.locator('#tc-concurrent')).toHaveValue('2');

	expect(consoleErrors).toEqual([]);
	expect(pageErrors).toEqual([]);
});

test('Active, Waiting, History & Failed tabs: 2-col active jobs, sidebar rings, waiting Bump/Skip, history skip_reason, and failed Retry', async ({ browser }) => {
	await page.goto('/transcode');

	const histRes = page.waitForResponse((r) => r.url().includes('/api/v1/transcode/items') && r.request().method() === 'GET');
	await page.getByTestId('transcode-tab-history').click();
	expect((await histRes).status()).toBe(200);
	await expect(page).toHaveURL(/\/transcode\?tab=history$/);
	await expect(page.getByTestId('transcode-history-tab')).toBeVisible();
	await expect(page.getByText('No completed or skipped files yet')).toBeVisible();

	const failRes = page.waitForResponse((r) => r.url().includes('/api/v1/transcode/failed') && r.request().method() === 'GET');
	await page.getByTestId('transcode-tab-failed').click();
	expect((await failRes).status()).toBe(200);
	await expect(page).toHaveURL(/\/transcode\?tab=failed$/);
	await expect(page.getByTestId('transcode-failed-tab')).toBeVisible();
	await expect(page.getByText('No failed or stopped jobs')).toBeVisible();

	expect(consoleErrors).toEqual([]);
	expect(pageErrors).toEqual([]);

	const qaPage = await openContext(browser, { width: 1440, height: 900 }, 'dark');
	const { ce: qaConsoleErrors, pe: qaPageErrors } = trackErrors(qaPage);
	await installQaRoutes(qaPage);

	await qaPage.goto('/transcode');
	const jobsCard = qaPage.getByTestId('transcode-jobs');
	await expect(jobsCard).toBeVisible();
	const jobs = jobsCard.getByTestId('transcode-job');
	await expect(jobs).toHaveCount(2);

	const job0 = jobs.nth(0);
	await expect(job0).toContainText('Sample Film');
	await expect(job0).toContainText('Encoding');
	await expect(job0).toContainText('/dev/dri/renderD128');
	await expect(job0).toContainText('Q 21');
	await expect(job0).toContainText('VMAF 93.8');
	await expect(job0).toContainText('152 fps · 6.3x');
	await expect(job0).toContainText('62%');
	await expect(job0.getByRole('progressbar')).toBeVisible();

	const job1 = jobs.nth(1);
	await expect(job1).toContainText('Night Runner S01E02');
	await expect(job1).toContainText('Encoding');
	await expect(job1).toContainText('/dev/dri/renderD129');
	await expect(job1).toContainText('Q 23');
	await expect(job1).toContainText('VMAF 92.4');
	await expect(job1).toContainText('118 fps · 4.9x');
	await expect(job1).toContainText('34%');
	await expect(job1.getByRole('progressbar')).toBeVisible();

	const box0 = await job0.boundingBox();
	const box1 = await job1.boundingBox();
	expect(box0).not.toBeNull();
	expect(box1).not.toBeNull();
	expect(Math.abs(box0!.y - box1!.y)).toBeLessThanOrEqual(2);
	expect(box1!.x).toBeGreaterThan(box0!.x);

	const gotoWaiting = jobsCard.getByTestId('transcode-goto-waiting');
	await expect(gotoWaiting).toBeVisible();
	await expect(gotoWaiting).toContainText('3 waiting');

	const nav = qaPage.getByRole('navigation', { name: 'Main' });
	const navTranscode = nav.locator('a[href="/transcode"]');
	const navSubtitles = nav.locator('a[href="/subtitles"]');
	await expect(navTranscode.getByTestId('nav-ring')).toBeVisible();
	await expect(navTranscode.locator('b.count')).toHaveText('5');
	await expect(navSubtitles.getByTestId('nav-ring')).toBeVisible();
	await expect(navSubtitles.locator('b.count')).toHaveText('1');

	await noSidewaysScroll(qaPage);
	await axe(qaPage, 'transcode active desktop dark');

	const stopBtn = job0.getByTestId('transcode-stop-job');
	await expect(stopBtn).toBeEnabled();
	const stopReq = qaPage.waitForResponse((r) => r.url().includes('/api/v1/transcode/jobs/9001/stop') && r.request().method() === 'POST');
	await stopBtn.click();
	expect((await stopReq).status()).toBe(200);
	await expect(qaPage.getByRole('status').filter({ hasText: 'Job stopped' })).toBeVisible();

	const waitListReq = qaPage.waitForResponse((r) => r.url().includes('/api/v1/transcode/waiting') && r.request().method() === 'GET');
	await gotoWaiting.click();
	expect((await waitListReq).status()).toBe(200);
	await expect(qaPage).toHaveURL(/\/transcode\?tab=waiting$/);
	await expect(qaPage.getByTestId('transcode-waiting-tab')).toBeVisible();

	const waitRows = qaPage.getByTestId('transcode-waiting-row');
	await expect(waitRows).toHaveCount(3);
	await expect(waitRows.nth(0)).toContainText('#1');
	await expect(waitRows.nth(1)).toContainText('#2');
	await expect(waitRows.nth(2)).toContainText('#3');

	const bumpReq = qaPage.waitForResponse((r) => r.url().includes('/api/v1/transcode/jobs/9102/bump') && r.request().method() === 'POST');
	const waitReloadAfterBump = qaPage.waitForResponse((r) => r.url().includes('/api/v1/transcode/waiting') && r.request().method() === 'GET');
	await waitRows.nth(1).getByTestId('transcode-bump-job').click();
	expect((await bumpReq).status()).toBe(200);
	expect((await waitReloadAfterBump).status()).toBe(200);
	await expect(qaPage.getByRole('status').filter({ hasText: 'Moved to top of queue' })).toBeVisible();

	await expect(waitRows).toHaveCount(3);
	await expect(waitRows.nth(0)).toHaveAttribute('data-priority', 'now');
	await expect(waitRows.nth(0)).toContainText('#1');
	await expect(waitRows.nth(0)).toContainText('Waiting Film Two');
	await expect(waitRows.nth(0)).toContainText('Priority');
	await expect(waitRows.nth(0).getByTestId('transcode-bump-job')).toBeDisabled();

	await noSidewaysScroll(qaPage);
	await axe(qaPage, 'transcode waiting desktop dark');

	const skipReq = qaPage.waitForResponse((r) => r.url().includes('/api/v1/transcode/jobs/9101/skip') && r.request().method() === 'POST');
	const waitReloadAfterSkip = qaPage.waitForResponse((r) => r.url().includes('/api/v1/transcode/waiting') && r.request().method() === 'GET');
	await waitRows.nth(1).getByTestId('transcode-skip-job').click();
	expect((await skipReq).status()).toBe(200);
	expect((await waitReloadAfterSkip).status()).toBe(200);
	await expect(qaPage.getByRole('status').filter({ hasText: 'Job skipped' })).toBeVisible();
	await expect(waitRows).toHaveCount(2);

	const qaHistReq = qaPage.waitForResponse((r) => r.url().includes('/api/v1/transcode/items') && r.request().method() === 'GET');
	await qaPage.getByTestId('transcode-tab-history').click();
	expect((await qaHistReq).status()).toBe(200);
	await expect(qaPage).toHaveURL(/\/transcode\?tab=history$/);
	await expect(qaPage.getByTestId('transcode-history-tab')).toBeVisible();

	const histAllReq = qaPage.waitForResponse((r) => r.url().includes('/api/v1/transcode/items') && r.url().includes('state=history') && r.request().method() === 'GET');
	await qaPage.getByTestId('transcode-history-filter').getByRole('radio', { name: 'All', exact: true }).click();
	expect((await histAllReq).status()).toBe(200);

	const histRows = qaPage.getByTestId('transcode-history-row');
	await expect(histRows).toHaveCount(2);
	const doneRow = qaPage.locator('[data-testid="transcode-history-row"][data-state="done"]');
	await expect(doneRow).toContainText('-58%');
	await expect(doneRow).toContainText('VMAF 94.2');
	await expect(doneRow).toContainText('Q 22');
	await expect(doneRow).toContainText('h264 → av1');

	const skippedRow = qaPage.locator('[data-testid="transcode-history-row"][data-state="skipped"]');
	await expect(skippedRow).toContainText('Skipped by user');
	await expect(skippedRow).not.toContainText('user_skipped');

	await noSidewaysScroll(qaPage);
	await axe(qaPage, 'transcode history desktop dark');

	const qaFailReq = qaPage.waitForResponse((r) => r.url().includes('/api/v1/transcode/failed') && r.request().method() === 'GET');
	await qaPage.getByTestId('transcode-tab-failed').click();
	expect((await qaFailReq).status()).toBe(200);
	const failedRow = qaPage.getByTestId('transcode-failed-row').first();
	await expect(failedRow).toBeVisible();
	await expect(failedRow).toContainText('encoder exited with code 1: invalid frame rate');
	await expect(failedRow.getByTestId('transcode-retry-job')).toBeVisible();
	await expect(failedRow.getByTestId('transcode-retry-job')).toBeEnabled();
	await expect(qaPage.getByTestId('transcode-retry-all')).toBeVisible();
	await expect(qaPage.getByTestId('transcode-retry-all')).toBeEnabled();

	await noSidewaysScroll(qaPage);
	await axe(qaPage, 'transcode failed desktop dark');

	expect(qaConsoleErrors).toEqual([]);
	expect(qaPageErrors).toEqual([]);
	await qaPage.context().close();
});

test('Desktop responsive & accessibility: 1440x900 dark & light Settings alignment, Active, Waiting, History, Failed, and axe', async ({ browser }) => {
	const dir = process.env.TRANSCODE_QA_SHOTS;
	if (dir) mkdirSync(dir, { recursive: true });

	const deskDark = await openContext(browser, { width: 1440, height: 900 }, 'dark');
	const { ce: darkCe, pe: darkPe } = trackErrors(deskDark);
	await installQaRoutes(deskDark);

	await deskDark.goto('/transcode');
	await expect(deskDark.getByTestId('transcode-stats')).toBeVisible();
	await expect(deskDark.getByTestId('transcode-job')).toHaveCount(2);
	const darkNav = deskDark.getByRole('navigation', { name: 'Main' });
	await expect(darkNav.locator('a[href="/transcode"]').getByTestId('nav-ring')).toBeVisible();
	await expect(darkNav.locator('a[href="/subtitles"]').getByTestId('nav-ring')).toBeVisible();
	if (dir) {
		await settleShot(deskDark);
		await deskDark.screenshot({ path: join(dir, 'transcode-active-desktop-dark.png'), fullPage: true, animations: 'disabled' });
	}
	await noSidewaysScroll(deskDark);
	await axe(deskDark, 'transcode active desktop dark');

	await deskDark.goto('/transcode?tab=waiting');
	await expect(deskDark.getByTestId('transcode-waiting-tab')).toBeVisible();
	await expect(deskDark.getByTestId('transcode-waiting-row')).toHaveCount(3);
	if (dir) {
		await settleShot(deskDark);
		await deskDark.screenshot({ path: join(dir, 'transcode-waiting-desktop-dark.png'), fullPage: true, animations: 'disabled' });
	}
	await noSidewaysScroll(deskDark);
	await axe(deskDark, 'transcode waiting desktop dark');

	await deskDark.goto('/transcode?tab=history');
	await expect(deskDark.getByTestId('transcode-history-tab')).toBeVisible();
	const darkHistAllReq = deskDark.waitForResponse((r) => r.url().includes('/api/v1/transcode/items') && r.url().includes('state=history') && r.request().method() === 'GET');
	await deskDark.getByTestId('transcode-history-filter').getByRole('radio', { name: 'All', exact: true }).click();
	expect((await darkHistAllReq).status()).toBe(200);
	await expect(deskDark.getByTestId('transcode-history-row')).toHaveCount(2);
	if (dir) {
		await settleShot(deskDark);
		await deskDark.screenshot({ path: join(dir, 'transcode-history-desktop-dark.png'), fullPage: true, animations: 'disabled' });
	}
	await noSidewaysScroll(deskDark);
	await axe(deskDark, 'transcode history desktop dark');

	await deskDark.goto('/transcode?tab=failed');
	await expect(deskDark.getByTestId('transcode-failed-tab')).toBeVisible();
	await expect(deskDark.getByTestId('transcode-failed-row').first()).toBeVisible();
	if (dir) {
		await settleShot(deskDark);
		await deskDark.screenshot({ path: join(dir, 'transcode-failed-desktop-dark.png'), fullPage: true, animations: 'disabled' });
	}
	await noSidewaysScroll(deskDark);
	await axe(deskDark, 'transcode failed desktop dark');

	await deskDark.goto('/transcode?tab=settings');
	await expect(deskDark.getByTestId('transcode-settings-tab')).toBeVisible();
	await expect(deskDark.getByTestId('transcode-profile-pill').first()).toBeVisible();
	await expect(deskDark.locator('#transcode-settings-form')).toBeVisible();
	if (dir) {
		await settleShot(deskDark);
		await deskDark.screenshot({ path: join(dir, 'transcode-settings-desktop-dark.png'), fullPage: true, animations: 'disabled' });
	}
	await noSidewaysScroll(deskDark);
	await axe(deskDark, 'transcode settings desktop dark');
	await assertAlignedGridRows(deskDark);

	await openAdvanced(deskDark.getByTestId('transcode-settings-tab'));
	await assertAlignedGridRows(deskDark);
	expect(darkCe).toEqual([]);
	expect(darkPe).toEqual([]);
	await deskDark.context().close();

	const deskLight = await openContext(browser, { width: 1440, height: 900 }, 'light');
	const { ce: lightCe, pe: lightPe } = trackErrors(deskLight);
	await installQaRoutes(deskLight);

	await deskLight.goto('/transcode');
	await expect(deskLight.getByTestId('transcode-stats')).toBeVisible();
	await expect(deskLight.getByTestId('transcode-job')).toHaveCount(2);
	const lightNav = deskLight.getByRole('navigation', { name: 'Main' });
	await expect(lightNav.locator('a[href="/transcode"]').getByTestId('nav-ring')).toBeVisible();
	await expect(lightNav.locator('a[href="/subtitles"]').getByTestId('nav-ring')).toBeVisible();
	if (dir) {
		await settleShot(deskLight);
		await deskLight.screenshot({ path: join(dir, 'transcode-active-desktop-light.png'), fullPage: true, animations: 'disabled' });
	}
	await noSidewaysScroll(deskLight);
	await axe(deskLight, 'transcode active desktop light');

	await deskLight.goto('/transcode?tab=waiting');
	await expect(deskLight.getByTestId('transcode-waiting-tab')).toBeVisible();
	await expect(deskLight.getByTestId('transcode-waiting-row')).toHaveCount(3);
	if (dir) {
		await settleShot(deskLight);
		await deskLight.screenshot({ path: join(dir, 'transcode-waiting-desktop-light.png'), fullPage: true, animations: 'disabled' });
	}
	await noSidewaysScroll(deskLight);
	await axe(deskLight, 'transcode waiting desktop light');

	await deskLight.goto('/transcode?tab=settings');
	await expect(deskLight.getByTestId('transcode-settings-tab')).toBeVisible();
	await expect(deskLight.getByTestId('transcode-profile-pill').first()).toBeVisible();
	await expect(deskLight.locator('#transcode-settings-form')).toBeVisible();
	if (dir) {
		await settleShot(deskLight);
		await deskLight.screenshot({ path: join(dir, 'transcode-settings-desktop-light.png'), fullPage: true, animations: 'disabled' });
	}
	await noSidewaysScroll(deskLight);
	await axe(deskLight, 'transcode settings desktop light');
	await assertAlignedGridRows(deskLight);

	expect(lightCe).toEqual([]);
	expect(lightPe).toEqual([]);
	await deskLight.context().close();
});

test('Phone responsive & accessibility: 390x844 dark Active (1-col), Waiting, Settings, and axe', async ({ browser }) => {
	const dir = process.env.TRANSCODE_QA_SHOTS;
	if (dir) mkdirSync(dir, { recursive: true });

	const phoneDark = await openContext(browser, { width: 390, height: 844 }, 'dark');
	const { ce: phoneCe, pe: phonePe } = trackErrors(phoneDark);
	await installQaRoutes(phoneDark);

	await phoneDark.goto('/transcode');
	await expect(phoneDark.getByTestId('transcode-stats')).toBeVisible();
	const phoneJobs = phoneDark.getByTestId('transcode-job');
	await expect(phoneJobs).toHaveCount(2);
	const pBox0 = await phoneJobs.nth(0).boundingBox();
	const pBox1 = await phoneJobs.nth(1).boundingBox();
	expect(pBox0).not.toBeNull();
	expect(pBox1).not.toBeNull();
	expect(Math.abs(pBox0!.x - pBox1!.x)).toBeLessThanOrEqual(2);
	expect(pBox1!.y).toBeGreaterThan(pBox0!.y);
	if (dir) {
		await settleShot(phoneDark);
		await phoneDark.screenshot({ path: join(dir, 'transcode-active-phone-dark.png'), fullPage: false, animations: 'disabled' });
		await phoneDark.getByTestId('transcode-jobs').scrollIntoViewIfNeeded();
		await settleShot(phoneDark, false);
		await phoneDark.screenshot({ path: join(dir, 'transcode-active-jobs-phone-dark.png'), fullPage: false, animations: 'disabled' });
		await phoneDark.evaluate(() => window.scrollTo(0, 0));
	}
	await noSidewaysScroll(phoneDark);
	await axe(phoneDark, 'transcode active phone dark');

	await phoneDark.goto('/transcode?tab=waiting');
	await expect(phoneDark.getByTestId('transcode-waiting-tab')).toBeVisible();
	await expect(phoneDark.getByTestId('transcode-waiting-row')).toHaveCount(3);
	if (dir) {
		await settleShot(phoneDark);
		await phoneDark.screenshot({ path: join(dir, 'transcode-waiting-phone-dark.png'), fullPage: false, animations: 'disabled' });
	}
	await noSidewaysScroll(phoneDark);
	await axe(phoneDark, 'transcode waiting phone dark');

	await phoneDark.goto('/transcode?tab=settings');
	await expect(phoneDark.getByTestId('transcode-settings-tab')).toBeVisible();
	await expect(phoneDark.getByTestId('transcode-profile-pill').first()).toBeVisible();
	await expect(phoneDark.locator('#transcode-settings-form')).toBeVisible();
	if (dir) {
		await settleShot(phoneDark);
		await phoneDark.screenshot({ path: join(dir, 'transcode-settings-phone-dark.png'), fullPage: false, animations: 'disabled' });
	}
	await noSidewaysScroll(phoneDark);
	await axe(phoneDark, 'transcode settings phone dark');

	expect(phoneCe).toEqual([]);
	expect(phonePe).toEqual([]);
	await phoneDark.context().close();
});
