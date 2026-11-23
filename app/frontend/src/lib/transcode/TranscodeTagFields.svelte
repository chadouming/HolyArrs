<script lang="ts">
	import Button from '#lib/ui/Button.svelte';
	import Chip from '#lib/ui/Chip.svelte';
	import Field from '#lib/ui/Field.svelte';
	import { t as tr } from '#lib/core/i18n/i18n.svelte.ts';
	import { cleanSkipTags } from './helpers.ts';

	let {
		skipTags = $bindable<string[]>([]),
		writeTag = $bindable('HolyArrs'),
		disabled = false,
		idPrefix,
		testidPrefix
	}: {
		skipTags?: string[];
		writeTag?: string;
		disabled?: boolean;
		idPrefix: string;
		testidPrefix: string;
	} = $props();

	let draft = $state('');

	/** Commits any pending comma-separated skip tags typed into the input box. */
	export function commitPending() {
		if (disabled || !draft.trim()) return;
		skipTags = cleanSkipTags([...skipTags, ...draft.split(',')]);
		draft = '';
	}

	function removeTag(tag: string) {
		if (disabled) return;
		const lower = tag.toLowerCase();
		skipTags = skipTags.filter((x) => x.toLowerCase() !== lower);
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Enter' || e.key === ',') {
			e.preventDefault();
			commitPending();
		}
	}
</script>

<div class="tag-fields">
	<div class="field" data-testid="{testidPrefix}-skip-tags">
		<label for="{idPrefix}-skip-tag-input">{tr('transcode.settings.skipTags')}</label>
		{#if skipTags.length > 0}
			<div class="chips">
				{#each skipTags as tag (tag)}
					<Chip label={tag} onremove={disabled ? undefined : () => removeTag(tag)} />
				{/each}
			</div>
		{/if}
		<div class="tag-entry">
			<input
				id="{idPrefix}-skip-tag-input"
				class="input"
				type="text"
				bind:value={draft}
				onkeydown={onKey}
				onblur={commitPending}
				{disabled}
				placeholder={tr('transcode.settings.skipTagPlaceholder')}
				data-testid="{testidPrefix}-skip-tag-input"
			/>
			<Button
				variant="secondary"
				size="md"
				onclick={commitPending}
				disabled={disabled || !draft.trim()}
				data-testid="{testidPrefix}-skip-tag-add"
			>{tr('transcode.settings.addSkipTag')}</Button>
		</div>
		<span class="hint">{tr('transcode.settings.skipTagsHint')}</span>
	</div>

	<Field
		id="{idPrefix}-write-tag"
		label={tr('transcode.settings.writeTag')}
		hint={tr('transcode.settings.writeTagHint')}
	>
		<input
			id="{idPrefix}-write-tag"
			class="input"
			type="text"
			bind:value={writeTag}
			placeholder="HolyArrs"
			{disabled}
			data-testid="{testidPrefix}-write-tag"
		/>
	</Field>
</div>

<style>
	.tag-fields {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		align-items: start;
		gap: 14px 16px;
	}
	.tag-fields :global(.field),
	.tag-fields :global(.field + .field) {
		margin-top: 0;
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		margin-bottom: 4px;
	}
	.tag-entry {
		display: flex;
		gap: 8px;
		align-items: center;
	}
	.tag-entry .input {
		flex: 1;
		min-width: 0;
	}
	.tag-entry :global(.ui-btn) {
		height: 40px;
		min-height: 40px;
		flex: none;
	}
	@media (max-width: 640px) {
		.tag-fields {
			grid-template-columns: minmax(0, 1fr);
		}
	}
	@media (pointer: coarse) {
		.tag-entry :global(.ui-btn) {
			height: 44px;
			min-height: 44px;
		}
	}
</style>
