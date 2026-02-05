<script lang="ts">
	import { Search, X, RefreshCw, ChevronDown } from 'lucide-svelte';
	import { onMount } from 'svelte';
	import { filterStore, loadNetworkData, dataSourceStore } from '$lib/stores';
	import type { TrafficType } from '$lib/types';
	import TimelineSlider from '$lib/components/timeline/TimelineSlider.svelte';

	// Local state
	let isRefreshing = $state(false);

	// Traffic type options with exit added
	const trafficTypes: { value: TrafficType; label: string; defaultOn: boolean }[] = [
		{ value: 'virtual', label: 'Virtual', defaultOn: true },
		{ value: 'exit', label: 'Exit Node', defaultOn: false },
		{ value: 'subnet', label: 'Subnet', defaultOn: false },
		{ value: 'physical', label: 'Physical', defaultOn: false }
	];

	// Initialize with defaults (virtual ON, others OFF)
	let selectedTrafficTypes = $state<Set<string>>(new Set(['virtual']));

	// Initialize filter store with defaults once on mount
	onMount(() => {
		filterStore.setTrafficTypes([...selectedTrafficTypes] as TrafficType[]);
	});

	async function handleRefresh() {
		isRefreshing = true;
		await loadNetworkData();
		isRefreshing = false;
	}

	function toggleTrafficType(type: string) {
		if (selectedTrafficTypes.has(type)) {
			selectedTrafficTypes.delete(type);
		} else {
			selectedTrafficTypes.add(type);
		}
		selectedTrafficTypes = new Set(selectedTrafficTypes);
		filterStore.setTrafficTypes([...selectedTrafficTypes] as TrafficType[]);
	}

	function selectAllTrafficTypes() {
		selectedTrafficTypes = new Set(trafficTypes.map((t) => t.value));
		filterStore.setTrafficTypes([...selectedTrafficTypes] as TrafficType[]);
	}

	function clearAllTrafficTypes() {
		selectedTrafficTypes = new Set();
		filterStore.setTrafficTypes([]);
	}

	// Get color for traffic type indicator
	function getTrafficTypeColor(type: string): string {
		switch (type) {
			case 'virtual':
				return 'bg-blue-500';
			case 'exit':
				return 'bg-purple-500';
			case 'subnet':
				return 'bg-green-500';
			case 'physical':
				return 'bg-amber-500';
			default:
				return 'bg-gray-500';
		}
	}
</script>

<div class="flex h-full flex-col overflow-y-auto p-4">
	<div class="mb-4 flex items-center justify-between">
		<h2 class="text-lg font-semibold">Filters</h2>
		<button class="text-muted-foreground hover:text-foreground">
			<ChevronDown class="h-4 w-4" />
		</button>
	</div>

	<!-- Search -->
	<div class="mb-4">
		<label for="search-input" class="mb-1 block text-sm font-medium">Search</label>
		<div class="relative">
			<input
				id="search-input"
				type="text"
				placeholder="Search devices, tag:k8s, ip:100..."
				class="w-full rounded-md border border-input bg-background py-2 pl-3 pr-8 text-sm"
				value={$filterStore.search}
				oninput={(e) => filterStore.setSearch(e.currentTarget.value)}
			/>
			{#if $filterStore.search}
				<button
					onclick={() => filterStore.setSearch('')}
					class="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
				>
					<X class="h-4 w-4" />
				</button>
			{/if}
		</div>
		<ul class="mt-1 space-y-0.5 text-xs text-muted-foreground">
			<li>• <span class="text-primary">tag:k8s</span> - Find devices with specific tags</li>
			<li>• <span class="text-primary">ip:100.88</span> - Find devices by IP address</li>
			<li>• <span class="text-primary">user@github</span> - Find devices by user</li>
			<li>• Regular text searches device names, IPs, and tags</li>
		</ul>
	</div>

	<!-- Refresh Button -->
	<button
		onclick={handleRefresh}
		disabled={isRefreshing}
		class="mb-4 flex w-full items-center justify-center gap-2 rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
	>
		<RefreshCw class="h-4 w-4 {isRefreshing ? 'animate-spin' : ''}" />
		Refresh Data
	</button>

	<!-- Traffic Type -->
	<fieldset class="mb-4">
		<legend class="mb-1 block text-sm font-medium">Traffic Type</legend>
		<div class="space-y-1">
			{#each trafficTypes as type}
				<label class="flex items-center gap-2 text-sm">
					<input
						type="checkbox"
						class="rounded border-input"
						checked={selectedTrafficTypes.has(type.value)}
						onchange={() => toggleTrafficType(type.value)}
					/>
					<span class="flex items-center gap-1">
						<span class="h-2 w-2 rounded-full {getTrafficTypeColor(type.value)}"></span>
						{type.label}
					</span>
				</label>
			{/each}
		</div>
		<div class="mt-1 flex gap-2 text-xs">
			<button onclick={selectAllTrafficTypes} class="text-primary hover:underline">Select all</button
			>
			<button onclick={clearAllTrafficTypes} class="text-primary hover:underline">
				Clear all ({selectedTrafficTypes.size})
			</button>
		</div>
	</fieldset>

	<!-- Timeline / Data Source -->
	<div class="mb-4 border-t border-border pt-4">
		<TimelineSlider />
	</div>
</div>
