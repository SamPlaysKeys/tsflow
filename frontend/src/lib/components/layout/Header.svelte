<script lang="ts">
	import { RefreshCw, PanelLeft, ScrollText, Sun, Moon, Monitor, Network, Link, Activity, BarChart3 } from 'lucide-svelte';
	import { page } from '$app/stores';
	import { uiStore, loadNetworkData, networkStats, filteredNodes, themeStore } from '$lib/stores';
	import { formatBytes } from '$lib/utils';
	import type { ThemeMode } from '$lib/stores';

	const currentPath = $derived($page.url.pathname);

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

	function handleFilterToggle() {
		if (window.innerWidth >= 1024) {
			uiStore.toggleFilterPanel();
		} else {
			uiStore.toggleMobileDrawer();
		}
	}

	const ThemeIcon = $derived(getThemeIcon($themeStore));
</script>

<header class="flex h-12 items-center justify-between border-b border-border bg-card px-2 sm:h-14 sm:px-4">
	<!-- Left section: Filter toggle + Logo + Nav -->
	<div class="flex items-center gap-2 sm:gap-4">
		<button
			onclick={handleFilterToggle}
			class="rounded-md p-2.5 hover:bg-secondary sm:p-2"
			title="Toggle filter panel"
		>
			<PanelLeft class="h-5 w-5" />
		</button>

		<div class="flex items-center gap-1.5 sm:gap-2">
			<Activity class="h-4 w-4 text-primary sm:h-5 sm:w-5" />
			<h1 class="text-base font-semibold sm:text-lg">TSFlow</h1>
		</div>

		<!-- Navigation -->
		<nav class="flex items-center gap-0.5 sm:gap-1">
			<a
				href="/"
				class="flex items-center gap-1.5 rounded-md px-2.5 py-2 text-sm hover:bg-secondary sm:px-3 sm:py-1.5"
				class:bg-secondary={currentPath === '/'}
			>
				<Network class="h-4 w-4" />
				<span class="hidden sm:inline">Graph</span>
			</a>
			<a
				href="/analytics"
				class="flex items-center gap-1.5 rounded-md px-2.5 py-2 text-sm hover:bg-secondary sm:px-3 sm:py-1.5"
				class:bg-secondary={currentPath === '/analytics'}
			>
				<BarChart3 class="h-4 w-4" />
				<span class="hidden sm:inline">Analytics</span>
			</a>
		</nav>
	</div>

	<!-- Center section: Network Stats (desktop only) -->
	<div class="hidden items-center gap-6 lg:flex">
		<div class="flex items-center gap-2">
			<Network class="h-4 w-4 text-muted-foreground" />
			<div class="text-sm">
				<span class="font-semibold">{$networkStats.totalNodes}</span>
				<span class="text-muted-foreground"> nodes</span>
			</div>
		</div>

		<div class="flex items-center gap-2">
			<Link class="h-4 w-4 text-muted-foreground" />
			<div class="text-sm">
				<span class="font-semibold">{$networkStats.totalConnections}</span>
				<span class="text-muted-foreground"> flows</span>
			</div>
		</div>

		<div class="h-6 w-px bg-border"></div>

		<div class="text-sm">
			<span class="text-muted-foreground">Traffic:</span>
			<span class="ml-1 font-semibold text-primary">{formatBytes($networkStats.totalBytes)}</span>
		</div>

		<div class="text-sm">
			<span class="text-muted-foreground">Avg/Node:</span>
			<span class="ml-1 font-semibold">{formatBytes(avgTrafficPerNode)}</span>
		</div>

		{#if peakNode}
			<div class="text-sm" title="{peakNode.displayName} ({peakNode.ip}) - {formatBytes(peakNode.totalBytes)}">
				<span class="text-muted-foreground">Peak:</span>
				<span class="ml-1 font-semibold">{peakNode.displayName}</span>
				<span class="ml-1 text-xs text-muted-foreground">({formatBytes(peakNode.totalBytes)})</span>
			</div>
		{/if}
	</div>

	<!-- Compact stats for tablet (md only) -->
	<div class="hidden items-center gap-3 md:flex lg:hidden">
		<div class="text-xs">
			<span class="font-semibold">{$networkStats.totalNodes}</span>
			<span class="text-muted-foreground"> nodes</span>
		</div>
		<div class="text-xs">
			<span class="font-semibold text-primary">{formatBytes($networkStats.totalBytes)}</span>
		</div>
	</div>

	<!-- Right section: Actions -->
	<div class="flex items-center gap-1 sm:gap-2">
		<button
			onclick={handleRefresh}
			class="flex items-center gap-2 rounded-md p-2.5 hover:bg-secondary sm:px-3 sm:py-1.5"
			disabled={isRefreshing}
		>
			<RefreshCw class="h-4 w-4 {isRefreshing ? 'animate-spin' : ''}" />
			<span class="hidden text-sm sm:inline">Refresh</span>
		</button>

		<button
			onclick={cycleTheme}
			class="flex items-center gap-2 rounded-md p-2.5 hover:bg-secondary sm:p-2"
			title="Theme: {getThemeLabel($themeStore)} (click to cycle)"
		>
			<ThemeIcon class="h-5 w-5" />
		</button>

		<button
			onclick={() => uiStore.toggleLogViewer()}
			class="rounded-md p-2.5 hover:bg-secondary sm:p-2"
			title="Toggle log viewer"
		>
			<ScrollText class="h-5 w-5" />
		</button>
	</div>
</header>
