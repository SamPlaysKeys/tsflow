import type { Protocol } from '$lib/types';

// Protocol number to name mapping
export function getProtocolName(proto: number): Protocol {
	switch (proto) {
		case 1:
			return 'icmp';
		case 6:
			return 'tcp';
		case 17:
			return 'udp';
		default:
			return 'other';
	}
}
