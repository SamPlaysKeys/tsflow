<script lang="ts">
	import { RefreshCw, PanelLeft, ScrollText, Sun, Moon, Monitor, Network, Link, Activity } from 'lucide-svelte';
	import { uiStore, loadNetworkData, networkStats, filteredNodes, themeStore } from '$lib/stores';
	import { formatBytes } from '$lib/utils';
	import type { ThemeMode } from '$lib/stores';

	let isRefreshing = $state(false);

	async function handleRefresh() {
		isRefreshing = true;
		await loadNetworkData();
		isRefreshing = false;
	}

	const avgTrafficPerNode = $derived.by(() => {
		if ($networkStats.totalNodes === 0) return 0;
		return $networkStats.totalBytes / $networkStats.totalNodes;
	});

	const peakNode = $derived.by(() => {
		if ($filteredNodes.length === 0) return null;
		return $filteredNodes.reduce((max, node) =>
			node.totalBytes > max.totalBytes ? node : max
		, $filteredNodes[0]);
	});

	function cycleTheme() {
		themeStore.toggle();
	}

	function getThemeIcon(mode: ThemeMode) {
		switch (mode) {
			case 'light': return Sun;
			case 'dark': return Moon;
			case 'system': return Monitor;
		}
	}

	function getThemeLabel(mode: ThemeMode) {
		switch (mode) {
			case 'light': return 'Light';
			case 'dark': return 'Dark';
			case 'system': return 'System';
		}
	}

	const ThemeIcon = $derived(getThemeIcon($themeStore));
</script>

<header class="flex h-14 items-center justify-between border-b border-border bg-card px-4">
	<!-- Left section: Filter toggle + Logo -->
	<div class="flex items-center gap-4">
		<button
			onclick={() => uiStore.toggleFilterPanel()}
			class="rounded-md p-2 hover:bg-secondary"
			title="Toggle filter panel"
		>
			<PanelLeft class="h-5 w-5" />
		</button>

		<div class="flex items-center gap-2">
			<Activity class="h-5 w-5 text-primary" />
			<h1 class="text-lg font-semibold">TSFlow</h1>
		</div>
	</div>

	<!-- Center section: Network Stats -->
	<div class="hidden items-center gap-6 lg:flex">
		<!-- Active Nodes -->
		<div class="flex items-center gap-2">
			<Network class="h-4 w-4 text-muted-foreground" />
			<div class="text-sm">
				<span class="font-semibold">{$networkStats.totalNodes}</span>
				<span class="text-muted-foreground"> nodes</span>
			</div>
		</div>

		<!-- Connections -->
		<div class="flex items-center gap-2">
			<Link class="h-4 w-4 text-muted-foreground" />
			<div class="text-sm">
				<span class="font-semibold">{$networkStats.totalConnections}</span>
				<span class="text-muted-foreground"> flows</span>
			</div>
		</div>

		<!-- Separator -->
		<div class="h-6 w-px bg-border"></div>

		<!-- Total Traffic -->
		<div class="text-sm">
			<span class="text-muted-foreground">Traffic:</span>
			<span class="ml-1 font-semibold text-primary">{formatBytes($networkStats.totalBytes)}</span>
		</div>

		<!-- Avg per Node -->
		<div class="text-sm">
			<span class="text-muted-foreground">Avg/Node:</span>
			<span class="ml-1 font-semibold">{formatBytes(avgTrafficPerNode)}</span>
		</div>

		<!-- Peak Node -->
		{#if peakNode}
			<div class="text-sm" title="{peakNode.displayName} ({peakNode.ip}) - {formatBytes(peakNode.totalBytes)}">
				<span class="text-muted-foreground">Peak:</span>
				<span class="ml-1 font-semibold">{peakNode.displayName}</span>
				<span class="ml-1 text-xs text-muted-foreground">({formatBytes(peakNode.totalBytes)})</span>
			</div>
		{/if}

	</div>

	<!-- Right section: Actions -->
	<div class="flex items-center gap-2">
		<button
			onclick={handleRefresh}
			class="flex items-center gap-2 rounded-md px-3 py-1.5 hover:bg-secondary"
			disabled={isRefreshing}
		>
			<RefreshCw class="h-4 w-4 {isRefreshing ? 'animate-spin' : ''}" />
			<span class="hidden text-sm sm:inline">Refresh</span>
		</button>

		<button
			onclick={cycleTheme}
			class="flex items-center gap-2 rounded-md p-2 hover:bg-secondary"
			title="Theme: {getThemeLabel($themeStore)} (click to cycle)"
		>
			<ThemeIcon class="h-5 w-5" />
		</button>

		<button
			onclick={() => uiStore.toggleLogViewer()}
			class="rounded-md p-2 hover:bg-secondary"
			title="Toggle log viewer"
		>
			<ScrollText class="h-5 w-5" />
		</button>
	</div>
</header>
