import type { Device, NetworkLog, NetworkNode, NetworkLink, TrafficType } from '$lib/types';
import { extractIP, extractPort, categorizeIP, ipMatches } from './ip-utils';
import { getProtocolName } from './protocol';

interface VIPServiceInfo {
	name: string;
	addrs: string[];
}

interface StaticRecordInfo {
	addrs: string[];
	comment?: string;
}

interface ProcessedNetwork {
	nodes: NetworkNode[];
	links: NetworkLink[];
}

// Get device name from IP
function getDeviceName(
	ip: string,
	devices: Device[] = [],
	services: Record<string, VIPServiceInfo> = {},
	records: Record<string, StaticRecordInfo> = {}
): string {
	// Check regular devices first
	const device = devices.find((d) => d.addresses.some((addr) => ipMatches(ip, addr)));
	if (device) {
		const shortName = device.name.split('.')[0];
		return shortName || device.name;
	}

	// Check VIP services
	for (const [serviceName, serviceInfo] of Object.entries(services)) {
		if (serviceInfo.addrs?.some((addr) => ipMatches(ip, addr))) {
			const cleanName = serviceName.startsWith('svc:') ? serviceName.substring(4) : serviceName;
			return cleanName;
		}
	}

	// Check static records
	for (const [recordName, recordInfo] of Object.entries(records)) {
		if (recordInfo.addrs?.some((addr) => ipMatches(ip, addr))) {
			return recordName;
		}
	}

	return ip;
}

// Get device data from IP
function getDeviceData(ip: string, devices: Device[] = []): Device | null {
	return devices.find((d) => d.addresses.some((addr) => ipMatches(ip, addr))) || null;
}

// Process network logs to create nodes and links
export function processNetworkLogs(
	logs: NetworkLog[],
	devices: Device[],
	services: Record<string, VIPServiceInfo> = {},
	records: Record<string, StaticRecordInfo> = {}
): ProcessedNetwork {
	if (!logs || logs.length === 0) {
		return { nodes: [], links: [] };
	}

	const nodeMap = new Map<string, NetworkNode>();
	const linkMap = new Map<string, NetworkLink>();

	logs.forEach((log) => {
		const allTraffic = [
			...(log.virtualTraffic || []).map((t) => ({ ...t, type: 'virtual' as TrafficType })),
			...(log.subnetTraffic || []).map((t) => ({ ...t, type: 'subnet' as TrafficType })),
			...(log.physicalTraffic || []).map((t) => ({
				...t,
				type: 'physical' as TrafficType,
				proto: t.proto || 0
			}))
		];

		allTraffic.forEach((traffic) => {
			const srcIP = extractIP(traffic.src);
			const dstIP = extractIP(traffic.dst);

			// Create or update source node
			const srcDeviceName = getDeviceName(srcIP, devices, services, records);
			const srcNodeId = srcDeviceName !== srcIP ? srcDeviceName : srcIP;

			if (!nodeMap.has(srcNodeId)) {
				const isTailscale = categorizeIP(srcIP).includes('tailscale');
				const deviceData = getDeviceData(srcIP, devices);
				const ipTags = categorizeIP(srcIP);
				const deviceTags = deviceData?.tags || [];
				const allTags = [...new Set([...ipTags, ...deviceTags])];

				nodeMap.set(srcNodeId, {
					id: srcNodeId,
					ip: srcIP,
					displayName: srcDeviceName,
					nodeType: 'ip',
					totalBytes: 0,
					txBytes: 0,
					rxBytes: 0,
					connections: 0,
					tags: allTags,
					user: deviceData?.user,
					isTailscale,
					ips: [srcIP],
					incomingPorts: new Set<number>(),
					outgoingPorts: new Set<number>(),
					protocols: new Set<string>(),
					device: deviceData || undefined
				});
			} else {
				const existingNode = nodeMap.get(srcNodeId)!;
				if (!existingNode.ips.includes(srcIP)) {
					existingNode.ips.push(srcIP);
					const newTags = categorizeIP(srcIP);
					const deviceData = getDeviceData(srcIP, devices);
					const deviceTags = deviceData?.tags || [];
					[...newTags, ...deviceTags].forEach((tag) => {
						if (!existingNode.tags.includes(tag)) {
							existingNode.tags.push(tag);
						}
					});
					if (!existingNode.user && deviceData?.user) {
						existingNode.user = deviceData.user;
					}
				}
			}

			// Create or update destination node
			const dstDeviceName = getDeviceName(dstIP, devices, services, records);
			const dstNodeId = dstDeviceName !== dstIP ? dstDeviceName : dstIP;

			if (!nodeMap.has(dstNodeId)) {
				const isTailscale = categorizeIP(dstIP).includes('tailscale');
				const deviceData = getDeviceData(dstIP, devices);
				const ipTags = categorizeIP(dstIP);
				const deviceTags = deviceData?.tags || [];
				const allTags = [...new Set([...ipTags, ...deviceTags])];

				nodeMap.set(dstNodeId, {
					id: dstNodeId,
					ip: dstIP,
					displayName: dstDeviceName,
					nodeType: 'ip',
					totalBytes: 0,
					txBytes: 0,
					rxBytes: 0,
					connections: 0,
					tags: allTags,
					user: deviceData?.user,
					isTailscale,
					ips: [dstIP],
					incomingPorts: new Set<number>(),
					outgoingPorts: new Set<number>(),
					protocols: new Set<string>(),
					device: deviceData || undefined
				});
			} else {
				const existingNode = nodeMap.get(dstNodeId)!;
				if (!existingNode.ips.includes(dstIP)) {
					existingNode.ips.push(dstIP);
					const newTags = categorizeIP(dstIP);
					const deviceData = getDeviceData(dstIP, devices);
					const deviceTags = deviceData?.tags || [];
					[...newTags, ...deviceTags].forEach((tag) => {
						if (!existingNode.tags.includes(tag)) {
							existingNode.tags.push(tag);
						}
					});
					if (!existingNode.user && deviceData?.user) {
						existingNode.user = deviceData.user;
					}
				}
			}

			// Update node traffic volumes
			const srcNode = nodeMap.get(srcNodeId)!;
			const dstNode = nodeMap.get(dstNodeId)!;

			srcNode.txBytes += traffic.txBytes || 0;
			srcNode.rxBytes += traffic.rxBytes || 0;
			srcNode.totalBytes = srcNode.txBytes + srcNode.rxBytes;

			dstNode.txBytes += traffic.rxBytes || 0;  // What source received = what dest sent
			dstNode.rxBytes += traffic.txBytes || 0;  // What source sent = what dest received
			dstNode.totalBytes = dstNode.txBytes + dstNode.rxBytes;

			// Track port and protocol information
			const protocolName = getProtocolName(traffic.proto || 0);
			srcNode.protocols.add(protocolName);
			dstNode.protocols.add(protocolName);

			if (traffic.proto === 6 || traffic.proto === 17) {
				const srcPort = extractPort(traffic.src);
				const dstPort = extractPort(traffic.dst);

				if (srcPort !== null) srcNode.outgoingPorts.add(srcPort);
				if (dstPort !== null) dstNode.incomingPorts.add(dstPort);
			}

			// Create or update link - use bidirectional key to combine A->B and B->A
			const sortedIds = [srcNodeId, dstNodeId].sort();
			const linkKey = `${sortedIds[0]}<->${sortedIds[1]}|${traffic.type}`;

			if (!linkMap.has(linkKey)) {
				linkMap.set(linkKey, {
					id: linkKey,
					source: sortedIds[0],
					target: sortedIds[1],
					originalSource: srcIP,
					originalTarget: dstIP,
					totalBytes: 0,
					txBytes: 0,
					rxBytes: 0,
					packets: 0,
					protocol: protocolName,
					trafficType: traffic.type,
					ports: new Set<number>()
				});
			}

			const link = linkMap.get(linkKey)!;
			link.txBytes += traffic.txBytes || 0;
			link.rxBytes += traffic.rxBytes || 0;
			link.totalBytes = link.txBytes + link.rxBytes;
			link.packets += (traffic.txPkts || 0) + (traffic.rxPkts || 0);
		});
	});

	// Update connection counts
	linkMap.forEach((link) => {
		const srcNode = nodeMap.get(link.source);
		const dstNode = nodeMap.get(link.target);
		if (srcNode) srcNode.connections++;
		if (dstNode) dstNode.connections++;
	});

	return {
		nodes: Array.from(nodeMap.values()),
		links: Array.from(linkMap.values())
	};
}
