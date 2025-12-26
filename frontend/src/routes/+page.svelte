<script lang="ts">
	import { onMount } from 'svelte';
	import { Loader2, AlertCircle, RefreshCw } from 'lucide-svelte';
	import NetworkGraph from '$lib/components/graph/NetworkGraph.svelte';
	import FilterPanel from '$lib/components/filters/FilterPanel.svelte';
	import LogViewer from '$lib/components/logs/LogViewer.svelte';
	import PortDetails from '$lib/components/logs/PortDetails.svelte';
	import BandwidthChart from '$lib/components/charts/BandwidthChart.svelte';
	import Header from '$lib/components/layout/Header.svelte';
	import { loadNetworkData, filteredNodes, filteredEdges } from '$lib/stores/network-store';
	import { uiStore } from '$lib/stores/ui-store';

	onMount(() => {
		loadNetworkData();
	});

	// Log viewer height state
	let logViewerHeight = $state(300);
	let isResizing = $state(false);

	function handleMouseDown() {
		isResizing = true;
	}

	function handleMouseMove(e: MouseEvent) {
		if (!isResizing) return;
		const container = document.getElementById('main-content');
		if (container) {
			const rect = container.getBoundingClientRect();
			logViewerHeight = Math.max(100, Math.min(500, rect.bottom - e.clientY));
		}
	}

	function handleMouseUp() {
		isResizing = false;
	}
</script>

<svelte:window on:mousemove={handleMouseMove} on:mouseup={handleMouseUp} />

<div class="flex h-screen flex-col bg-background">
	<!-- Top Bar with Network Stats -->
	<Header />

	<!-- Main Content Area -->
	<div class="flex flex-1 overflow-hidden">
		<!-- Filter Sidebar -->
		{#if $uiStore.showFilterPanel}
			<aside class="w-64 shrink-0 overflow-y-auto border-r border-border bg-card">
				<FilterPanel />
			</aside>
		{/if}

		<!-- Graph Area -->
		<main id="main-content" class="relative flex flex-1 flex-col overflow-hidden">
			<!-- Loading State -->
			{#if $uiStore.isLoading && $filteredNodes.length === 0}
				<div class="flex flex-1 flex-col items-center justify-center gap-4">
					<Loader2 class="h-8 w-8 animate-spin text-primary" />
					<p class="text-muted-foreground">Loading network data...</p>
				</div>
			<!-- Error State -->
			{:else if $uiStore.error}
				<div class="flex flex-1 flex-col items-center justify-center gap-4">
					<AlertCircle class="h-8 w-8 text-destructive" />
					<div class="text-center">
						<p class="font-medium text-destructive">Failed to load network data</p>
						<p class="mt-1 text-sm text-muted-foreground">{$uiStore.error}</p>
					</div>
					<button
						onclick={() => loadNetworkData()}
						class="mt-2 flex items-center gap-2 rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
					>
						<RefreshCw class="h-4 w-4" />
						Retry
					</button>
				</div>
			<!-- Graph -->
			{:else}
				<div class="flex-1" style="height: calc(100% - {$uiStore.showLogViewer ? logViewerHeight + 110 : 0}px)">
					<NetworkGraph nodes={$filteredNodes} edges={$filteredEdges} />
				</div>
			{/if}

			<!-- Bottom Panel: Bandwidth Chart + Port Details + Log Viewer -->
			{#if $uiStore.showLogViewer}
				<!-- Bandwidth Chart -->
				<BandwidthChart />

				<!-- Port Details (shows when node selected) -->
				<PortDetails />

				<!-- svelte-ignore a11y_no_static_element_interactions -->
				<!-- Resize Handle -->
				<div
					class="h-1 cursor-row-resize bg-border hover:bg-primary"
					onmousedown={handleMouseDown}
					aria-hidden="true"
				></div>
				<div class="overflow-hidden border-t border-border bg-card" style="height: {logViewerHeight}px">
					<LogViewer />
				</div>
			{/if}
		</main>
	</div>
</div>
