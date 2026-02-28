<script lang="ts">
	import type { Snippet } from 'svelte';
	import { TrendingUp, TrendingDown, Minus } from 'lucide-svelte';

	let { label, value, subtitle, delta, icon, sparkline, sparkColor = 'var(--color-primary)' }: {
		label: string;
		value: string;
		subtitle?: string;
		delta?: number;
		icon?: Snippet;
		sparkline?: number[];
		sparkColor?: string;
	} = $props();

	const sparkPath = $derived.by(() => {
		if (!sparkline || sparkline.length < 2) return '';
		const max = Math.max(...sparkline, 1);
		const w = 100;
		const h = 24;
		const step = w / (sparkline.length - 1);
		return sparkline
			.map((v, i) => `${i === 0 ? 'M' : 'L'}${(i * step).toFixed(1)},${(h - (v / max) * h).toFixed(1)}`)
			.join('');
	});

	const sparkFill = $derived.by(() => {
		if (!sparkPath) return '';
		const w = 100;
		const h = 24;
		const step = w / ((sparkline?.length || 2) - 1);
		const last = ((sparkline?.length || 2) - 1) * step;
		return `${sparkPath}L${last.toFixed(1)},${h}L0,${h}Z`;
	});
</script>

<div class="rounded-lg border border-border bg-card p-3 sm:p-4">
	<div class="flex items-center justify-between">
		<div class="flex items-center gap-1.5 text-xs text-muted-foreground sm:gap-2 sm:text-sm">
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
	<div class="flex items-end justify-between gap-3">
		<div>
			<div class="mt-1 text-lg font-bold sm:text-2xl">{value}</div>
			{#if subtitle}
				<div class="mt-0.5 text-xs text-muted-foreground/70">{subtitle}</div>
			{/if}
		</div>
		{#if sparkPath}
			<svg viewBox="0 0 100 24" class="h-6 w-16 shrink-0 sm:h-8 sm:w-20" preserveAspectRatio="none">
				<path d={sparkFill} fill={sparkColor} opacity="0.15" />
				<path d={sparkPath} fill="none" stroke={sparkColor} stroke-width="1.5" vector-effect="non-scaling-stroke" />
			</svg>
		{/if}
	</div>
</div>
