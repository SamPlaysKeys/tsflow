import { api } from './api-service';
import type { Device, NetworkLogsResponse } from '$lib/types';

export interface DevicesResponse {
	devices: Device[];
}

export interface ServicesResponse {
	services: Record<string, { name: string; addrs: string[] }>;
	records: Record<string, { addrs: string[]; comment?: string }>;
}

export interface DataRange {
	earliest: string;
	latest: string;
	count: number;
}

export interface StoredFlowLog {
	id: number;
	loggedAt: string;
	nodeId: string;
	periodStart: string;
	periodEnd: string;
	trafficType: string;
	protocol: number;
	srcIp: string;
	srcPort: number;
	dstIp: string;
	dstPort: number;
	txBytes: number;
	rxBytes: number;
	txPkts: number;
	rxPkts: number;
	createdAt: string;
}

export interface StoredFlowLogsResponse {
	logs: StoredFlowLog[];
	metadata: {
		count: number;
		start: string;
		end: string;
		limit: number;
		source: string;
	};
}

export interface AggregatedFlow {
	nodeId: string;
	trafficType: string;
	protocol: number;
	srcIp: string;
	srcPort: number;
	dstIp: string;
	dstPort: number;
	totalTxBytes: number;
	totalRxBytes: number;
	totalTxPkts: number;
	totalRxPkts: number;
	flowCount: number;
	firstSeen: string;
	lastSeen: string;
}

export interface AggregatedFlowsResponse {
	flows: AggregatedFlow[];
	metadata: {
		count: number;
		start: string;
		end: string;
		source: string;
	};
}

export interface PollerStatus {
	running: boolean;
	lastPollTime: string;
	lastPollCount: number;
	totalPolled: number;
	pollErrors: number;
	pollInterval: string;
	database?: {
		totalLogs: number;
		byTrafficType: Record<string, number>;
		dbSizeBytes: number;
		dataRange: DataRange;
	};
}

export interface BandwidthBucket {
	time: string;
	txBytes: number;
	rxBytes: number;
}

export interface BandwidthResponse {
	buckets: BandwidthBucket[];
	metadata: {
		count: number;
		start: string;
		end: string;
		bucketSeconds: number;
	};
}

export const tailscaleService = {
	async getDevices(): Promise<Device[]> {
		const response = await api.get<DevicesResponse>('/devices');
		return response.devices || [];
	},

	async getNetworkLogs(start: Date, end: Date): Promise<NetworkLogsResponse> {
		const startISO = start.toISOString();
		const endISO = end.toISOString();
		return api.get<NetworkLogsResponse>(`/network-logs?start=${startISO}&end=${endISO}`);
	},

	async getServicesRecords(): Promise<ServicesResponse> {
		return api.get<ServicesResponse>('/services-records');
	},

	// New methods for stored historical data
	async getStoredFlowLogs(start: Date, end: Date, limit = 50000): Promise<StoredFlowLogsResponse> {
		const startISO = start.toISOString();
		const endISO = end.toISOString();
		return api.get<StoredFlowLogsResponse>(
			`/flow-logs?start=${startISO}&end=${endISO}&limit=${limit}`
		);
	},

	// Aggregated flows for scalable network graph rendering (no limits)
	async getAggregatedFlows(start: Date, end: Date): Promise<AggregatedFlowsResponse> {
		const startISO = start.toISOString();
		const endISO = end.toISOString();
		return api.get<AggregatedFlowsResponse>(
			`/flow-logs/aggregated?start=${startISO}&end=${endISO}`
		);
	},

	async getDataRange(): Promise<DataRange> {
		return api.get<DataRange>('/flow-logs/range');
	},

	async getPollerStatus(): Promise<PollerStatus> {
		return api.get<PollerStatus>('/poller/status');
	},

	async getBandwidth(start: Date, end: Date, ips?: string[]): Promise<BandwidthResponse> {
		const startISO = start.toISOString();
		const endISO = end.toISOString();
		let url = `/bandwidth?start=${startISO}&end=${endISO}`;
		if (ips && ips.length > 0) {
			url += `&ips=${ips.join(',')}`;
		}
		return api.get<BandwidthResponse>(url);
	}
};
