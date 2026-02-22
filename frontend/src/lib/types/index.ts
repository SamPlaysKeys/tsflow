// Device types from Tailscale API
export interface Device {
	id: string;
	name: string;
	hostname: string;
	addresses: string[];
	os: string;
	clientVersion: string;
	lastSeen: string;
	online: boolean;
	tags?: string[];
	user: string;
	authorized: boolean;
	isExternal: boolean;
}

// Network traffic types
export type TrafficType = 'virtual' | 'subnet' | 'physical' | 'exit';
export type Protocol = 'tcp' | 'udp' | 'icmp' | 'other';

export interface TrafficEntry {
	proto: number;
	src: string;
	dst: string;
	txPkts: number;
	txBytes: number;
	rxPkts?: number;
	rxBytes?: number;
}

export interface NetworkLog {
	logged: string;
	nodeId: string;
	start: string;
	end: string;
	virtualTraffic: TrafficEntry[];
	subnetTraffic: TrafficEntry[];
	physicalTraffic: TrafficEntry[];
}

export interface NetworkLogsResponse {
	logs: NetworkLog[];
}

// Graph node/edge types
export interface NetworkNode {
	id: string;
	ip: string;
	displayName: string;
	nodeType: 'ip';
	totalBytes: number;
	txBytes: number;
	rxBytes: number;
	connections: number;
	tags: string[];
	user?: string;
	isTailscale: boolean;
	ips: string[];
	incomingPorts: Set<number>;
	outgoingPorts: Set<number>;
	protocols: Set<string>;
	device?: Device;
	isVIPService: boolean;
}

export interface NetworkLink {
	id: string;
	source: string;
	target: string;
	originalSource: string;
	originalTarget: string;
	totalBytes: number;
	txBytes: number;
	rxBytes: number;
	packets: number;
	protocol: Protocol;
	trafficType: TrafficType;
	ports: Set<number>;
}

// Filter types
export interface FilterState {
	search: string;
	protocols: Protocol[];
	trafficTypes: TrafficType[];
	minBandwidth: number;
	maxBandwidth: number;
	minConnections: number;
	showIpv4: boolean;
	showIpv6: boolean;
	selectedTags: string[];
}

export interface TimeRange {
	label: string;
	value: string;
	minutes?: number;
	start?: Date;
	end?: Date;
}

// UI state types
export interface UIState {
	showFilterPanel: boolean;
	showLogViewer: boolean;
	selectedNodeId: string | null;
	selectedEdgeId: string | null;
	isLoading: boolean;
	error: string | null;
}
