package services

import (
	"sort"
	"sync"
	"time"

	"github.com/rajsinghtech/tsflow/backend/internal/database"
)

// RollingWindowCache provides fast in-memory access to recent aggregates
// This enables instant responses for live view queries without DB access
type RollingWindowCache struct {
	mu sync.RWMutex

	// Node pair aggregates by minute bucket
	nodePairs map[int64][]database.NodePairAggregate

	// Total bandwidth by minute bucket
	bandwidth map[int64]*database.BandwidthBucket

	// Per-node bandwidth by (bucket, nodeID)
	nodeBandwidth map[int64]map[string]*database.NodeBandwidth

	// Network-wide traffic stats by minute bucket
	trafficStats map[int64]*database.TrafficStats

	// Maximum age of cached data (default 1 hour)
	maxAge time.Duration
}

func NewRollingWindowCache(maxAge time.Duration) *RollingWindowCache {
	return &RollingWindowCache{
		nodePairs:     make(map[int64][]database.NodePairAggregate),
		bandwidth:     make(map[int64]*database.BandwidthBucket),
		nodeBandwidth: make(map[int64]map[string]*database.NodeBandwidth),
		trafficStats:  make(map[int64]*database.TrafficStats),
		maxAge:        maxAge,
	}
}

// Update adds new aggregates to the cache and prunes old data
func (c *RollingWindowCache) Update(
	nodePairs []database.NodePairAggregate,
	bandwidth []database.BandwidthBucket,
	nodeBandwidth []database.NodeBandwidth,
	trafficStats []database.TrafficStats,
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Add node pairs by bucket, deduplicating by (src, dst, trafficType)
	for _, np := range nodePairs {
		existing := c.nodePairs[np.Bucket]
		found := false
		for i := range existing {
			if existing[i].SrcNodeID == np.SrcNodeID && existing[i].DstNodeID == np.DstNodeID && existing[i].TrafficType == np.TrafficType {
				existing[i].TxBytes += np.TxBytes
				existing[i].RxBytes += np.RxBytes
				existing[i].TxPkts += np.TxPkts
				existing[i].RxPkts += np.RxPkts
				existing[i].FlowCount += np.FlowCount
				found = true
				break
			}
		}
		if !found {
			c.nodePairs[np.Bucket] = append(c.nodePairs[np.Bucket], np)
		}
	}

	// Add bandwidth by bucket
	for _, b := range bandwidth {
		bucket := b.Time.Unix()
		if existing, ok := c.bandwidth[bucket]; ok {
			existing.TxBytes += b.TxBytes
			existing.RxBytes += b.RxBytes
		} else {
			c.bandwidth[bucket] = &database.BandwidthBucket{
				Time:    b.Time,
				TxBytes: b.TxBytes,
				RxBytes: b.RxBytes,
			}
		}
	}

	// Add node bandwidth by bucket and node
	for _, nb := range nodeBandwidth {
		if c.nodeBandwidth[nb.Bucket] == nil {
			c.nodeBandwidth[nb.Bucket] = make(map[string]*database.NodeBandwidth)
		}
		nodeMap := c.nodeBandwidth[nb.Bucket]
		if existing, ok := nodeMap[nb.NodeID]; ok {
			existing.TxBytes += nb.TxBytes
			existing.RxBytes += nb.RxBytes
		} else {
			nodeMap[nb.NodeID] = &database.NodeBandwidth{
				Bucket:  nb.Bucket,
				NodeID:  nb.NodeID,
				TxBytes: nb.TxBytes,
				RxBytes: nb.RxBytes,
			}
		}
	}

	// Add traffic stats by bucket (additive for byte/flow counters, replace for uniquePairs/topPorts)
	for _, ts := range trafficStats {
		if existing, ok := c.trafficStats[ts.Bucket]; ok {
			existing.TCPBytes += ts.TCPBytes
			existing.UDPBytes += ts.UDPBytes
			existing.OtherProtoBytes += ts.OtherProtoBytes
			existing.VirtualBytes += ts.VirtualBytes
			existing.SubnetBytes += ts.SubnetBytes
			existing.PhysicalBytes += ts.PhysicalBytes
			existing.TotalFlows += ts.TotalFlows
			if ts.UniquePairs > existing.UniquePairs {
			existing.UniquePairs = ts.UniquePairs // max, consistent with DB upsert
		}
			existing.TopPorts = ts.TopPorts       // replace: latest snapshot
		} else {
			copied := ts
			c.trafficStats[ts.Bucket] = &copied
		}
	}

	// Prune old data
	c.prune()
}

// prune removes data older than maxAge
func (c *RollingWindowCache) prune() {
	cutoff := time.Now().Add(-c.maxAge).Unix()

	for bucket := range c.nodePairs {
		if bucket < cutoff {
			delete(c.nodePairs, bucket)
		}
	}

	for bucket := range c.bandwidth {
		if bucket < cutoff {
			delete(c.bandwidth, bucket)
		}
	}

	for bucket := range c.nodeBandwidth {
		if bucket < cutoff {
			delete(c.nodeBandwidth, bucket)
		}
	}

	for bucket := range c.trafficStats {
		if bucket < cutoff {
			delete(c.trafficStats, bucket)
		}
	}
}

// GetNodePairs returns cached node pair aggregates for a time range
func (c *RollingWindowCache) GetNodePairs(start, end time.Time) []database.NodePairAggregate {
	c.mu.RLock()
	defer c.mu.RUnlock()

	startUnix := start.Unix()
	endUnix := end.Unix()
	var result []database.NodePairAggregate

	for bucket, pairs := range c.nodePairs {
		if bucket >= startUnix && bucket <= endUnix {
			result = append(result, pairs...)
		}
	}

	return result
}

// GetBandwidth returns cached bandwidth for a time range (sorted by time)
func (c *RollingWindowCache) GetBandwidth(start, end time.Time) []database.BandwidthBucket {
	c.mu.RLock()
	defer c.mu.RUnlock()

	startUnix := start.Unix()
	endUnix := end.Unix()
	var result []database.BandwidthBucket

	for bucket, bw := range c.bandwidth {
		if bucket >= startUnix && bucket <= endUnix {
			result = append(result, *bw)
		}
	}

	// Sort by time ascending
	sort.Slice(result, func(i, j int) bool {
		return result[i].Time.Before(result[j].Time)
	})

	return result
}

// GetNodeBandwidth returns cached bandwidth for a specific node (sorted by time)
func (c *RollingWindowCache) GetNodeBandwidth(start, end time.Time, nodeID string) []database.BandwidthBucket {
	c.mu.RLock()
	defer c.mu.RUnlock()

	startUnix := start.Unix()
	endUnix := end.Unix()
	var result []database.BandwidthBucket

	for bucket, nodeMap := range c.nodeBandwidth {
		if bucket >= startUnix && bucket <= endUnix {
			if nb, ok := nodeMap[nodeID]; ok {
				result = append(result, database.BandwidthBucket{
					Time:    time.Unix(bucket, 0).UTC(),
					TxBytes: nb.TxBytes,
					RxBytes: nb.RxBytes,
				})
			}
		}
	}

	// Sort by time ascending
	sort.Slice(result, func(i, j int) bool {
		return result[i].Time.Before(result[j].Time)
	})

	return result
}

// GetTrafficStats returns cached traffic stats for a time range (sorted by bucket)
func (c *RollingWindowCache) GetTrafficStats(start, end time.Time) []database.TrafficStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	startUnix := start.Unix()
	endUnix := end.Unix()
	var result []database.TrafficStats

	for bucket, ts := range c.trafficStats {
		if bucket >= startUnix && bucket <= endUnix {
			result = append(result, *ts)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Bucket < result[j].Bucket
	})

	return result
}

// HasDataFor returns true if the cache likely has data covering the given time range.
// Both start and end must be within the cache window, and the cache must have
// buckets that overlap the requested range.
func (c *RollingWindowCache) HasDataFor(start, end time.Time) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Check if we have any data at all
	if len(c.bandwidth) == 0 && len(c.nodePairs) == 0 {
		return false
	}

	// Both start and end must be within the cache window
	cacheStart := time.Now().Add(-c.maxAge)
	if start.Before(cacheStart) {
		return false
	}

	// Verify we have at least one bucket in the requested range
	startUnix := start.Unix()
	endUnix := end.Unix()
	for bucket := range c.bandwidth {
		if bucket >= startUnix && bucket <= endUnix {
			return true
		}
	}
	for bucket := range c.nodePairs {
		if bucket >= startUnix && bucket <= endUnix {
			return true
		}
	}

	return false
}
