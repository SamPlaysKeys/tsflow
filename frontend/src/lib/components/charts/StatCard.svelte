<script lang="ts">
	import type { Snippet } from 'svelte';
	import { TrendingUp, TrendingDown, Minus } from 'lucide-svelte';

	let { label, value, subtitle, delta, icon }: {
		label: string;
		value: string;
		subtitle?: string;
		delta?: number;
		icon?: Snippet;
	} = $props();
</script>

<div class="rounded-lg border border-border bg-card p-4">
	<div class="flex items-center justify-between">
		<div class="flex items-center gap-2 text-sm text-muted-foreground">
			{#if icon}
				{@render icon()}
			{/if}
			{label}
		</div>
		{#if delta !== undefined && delta !== 0}
			<div class="flex items-center gap-1 text-xs" class:text-green-400={delta > 0} class:text-red-400={delta < 0}>
				{#if delta > 0}
					<TrendingUp class="h-3 w-3" />
					<span>+{delta.toFixed(1)}%</span>
				{:else}
					<TrendingDown class="h-3 w-3" />
					<span>{delta.toFixed(1)}%</span>
				{/if}
			</div>
		{:else if delta === 0}
			<div class="flex items-center gap-1 text-xs text-muted-foreground/60">
				<Minus class="h-3 w-3" />
			</div>
		{/if}
	</div>
	<div class="mt-1 text-2xl font-bold">{value}</div>
	{#if subtitle}
		<div class="mt-0.5 text-xs text-muted-foreground/70">{subtitle}</div>
	{/if}
</div>
