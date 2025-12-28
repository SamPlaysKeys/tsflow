package services

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/rajsinghtech/tsflow/backend/internal/database"
	tailscale "tailscale.com/client/tailscale/v2"
)

// PollerConfig holds configuration for the background poller
type PollerConfig struct {
	// PollInterval is how often to poll for new logs
	PollInterval time.Duration
	// InitialBackfill is how far back to fetch on first run
	InitialBackfill time.Duration
	// RetentionMinutely is how long to keep minutely data (default 24h)
	RetentionMinutely time.Duration
	// RetentionHourly is how long to keep hourly data (default 7d)
	RetentionHourly time.Duration
	// RetentionDaily is how long to keep daily data (0 = forever)
	RetentionDaily time.Duration
	// CleanupInterval is how often to run cleanup
	CleanupInterval time.Duration
	// DeviceCacheRefresh is how often to refresh device cache
	DeviceCacheRefresh time.Duration
}

// DefaultPollerConfig returns sensible defaults
func DefaultPollerConfig() PollerConfig {
	return PollerConfig{
		PollInterval:       5 * time.Minute,
		InitialBackfill:    1 * time.Hour,
		RetentionMinutely:  24 * time.Hour,
		RetentionHourly:    7 * 24 * time.Hour,
		RetentionDaily:     0, // Keep forever
		CleanupInterval:    1 * time.Hour,
		DeviceCacheRefresh: 5 * time.Minute,
	}
}

// DeviceCache maps IPs to device info for fast lookups
type DeviceCache struct {
	mu          sync.RWMutex
	ipToDevice  map[string]*DeviceCacheEntry
	idToDevice  map[string]*DeviceCacheEntry
	lastRefresh time.Time
}

type DeviceCacheEntry struct {
	ID          string
	Name        string
	Hostname    string
	IPs         []string
	IsTailscale bool
}

func NewDeviceCache() *DeviceCache {
	return &DeviceCache{
		ipToDevice: make(map[string]*DeviceCacheEntry),
		idToDevice: make(map[string]*DeviceCacheEntry),
	}
}

func (c *DeviceCache) Update(devices []Device) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.ipToDevice = make(map[string]*DeviceCacheEntry)
	c.idToDevice = make(map[string]*DeviceCacheEntry)

	for _, d := range devices {
		entry := &DeviceCacheEntry{
			ID:          d.ID,
			Name:        d.Name,
			Hostname:    d.Hostname,
			IPs:         d.Addresses,
			IsTailscale: true,
		}
		c.idToDevice[d.ID] = entry
		for _, ip := range d.Addresses {
			c.ipToDevice[ip] = entry
		}
	}
	c.lastRefresh = time.Now()
}

// ResolveIP returns the device ID for an IP, or the IP itself if not found
func (c *DeviceCache) ResolveIP(ip string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if entry, ok := c.ipToDevice[ip]; ok {
		return entry.ID
	}
	return ip // Return IP as-is for external addresses
}

// GetDevice returns device info by ID
func (c *DeviceCache) GetDevice(id string) *DeviceCacheEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.idToDevice[id]
}

// NeedsRefresh returns true if cache is stale
func (c *DeviceCache) NeedsRefresh(maxAge time.Duration) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Since(c.lastRefresh) > maxAge
}

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

	// Maximum age of cached data (default 1 hour)
	maxAge time.Duration
}

func NewRollingWindowCache(maxAge time.Duration) *RollingWindowCache {
	return &RollingWindowCache{
		nodePairs:     make(map[int64][]database.NodePairAggregate),
		bandwidth:     make(map[int64]*database.BandwidthBucket),
		nodeBandwidth: make(map[int64]map[string]*database.NodeBandwidth),
		maxAge:        maxAge,
	}
}

// Update adds new aggregates to the cache and prunes old data
func (c *RollingWindowCache) Update(
	nodePairs []database.NodePairAggregate,
	bandwidth []database.BandwidthBucket,
	nodeBandwidth []database.NodeBandwidth,
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Add node pairs by bucket
	for _, np := range nodePairs {
		c.nodePairs[np.Bucket] = append(c.nodePairs[np.Bucket], np)
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

// GetBandwidth returns cached bandwidth for a time range
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

	return result
}

// GetNodeBandwidth returns cached bandwidth for a specific node
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

	return result
}

// HasDataFor returns true if the cache has data for the given time range
func (c *RollingWindowCache) HasDataFor(start, end time.Time) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Check if the range is within our cache window
	cacheStart := time.Now().Add(-c.maxAge)
	return start.After(cacheStart) || start.Equal(cacheStart)
}

// Poller fetches network logs from Tailscale API and stores them in the database
type Poller struct {
	tsService      *TailscaleService
	store          database.Store
	config         PollerConfig
	deviceCache    *DeviceCache
	rollingCache   *RollingWindowCache

	mu       sync.RWMutex
	running  bool
	stopChan chan struct{}
	doneChan chan struct{}

	// Stats
	lastPollTime  time.Time
	lastPollCount int
	totalPolled   int64
	pollErrors    int64
}

// NewPoller creates a new background poller
func NewPoller(tsService *TailscaleService, store database.Store, config PollerConfig) *Poller {
	return &Poller{
		tsService:    tsService,
		store:        store,
		config:       config,
		deviceCache:  NewDeviceCache(),
		rollingCache: NewRollingWindowCache(time.Hour), // Keep 1 hour in memory
		stopChan:     make(chan struct{}),
		doneChan:     make(chan struct{}),
	}
}

// Start begins the background polling loop
func (p *Poller) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return nil
	}
	p.running = true
	p.mu.Unlock()

	log.Printf("Starting background poller (interval: %v, retention minutely: %v, hourly: %v)",
		p.config.PollInterval, p.config.RetentionMinutely, p.config.RetentionHourly)

	// Initial device cache refresh
	if err := p.refreshDeviceCache(ctx); err != nil {
		log.Printf("Warning: initial device cache refresh failed: %v", err)
	}

	// Initial poll
	if err := p.poll(ctx); err != nil {
		log.Printf("Initial poll failed: %v", err)
	}

	// Start background goroutine
	go p.run(ctx)

	return nil
}

// Stop stops the background poller
func (p *Poller) Stop() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.running = false
	stopChan := p.stopChan
	doneChan := p.doneChan
	p.stopChan = nil
	p.mu.Unlock()

	if stopChan != nil {
		close(stopChan)
	}
	if doneChan != nil {
		<-doneChan
	}
	log.Println("Background poller stopped")
}

// Stats returns current poller statistics
func (p *Poller) Stats() map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return map[string]interface{}{
		"running":       p.running,
		"lastPollTime":  p.lastPollTime,
		"lastPollCount": p.lastPollCount,
		"totalPolled":   p.totalPolled,
		"pollErrors":    p.pollErrors,
		"pollInterval":  p.config.PollInterval.String(),
	}
}

// GetDeviceCache returns the device cache for external use
func (p *Poller) GetDeviceCache() *DeviceCache {
	return p.deviceCache
}

// GetRollingCache returns the rolling window cache for fast recent data access
func (p *Poller) GetRollingCache() *RollingWindowCache {
	return p.rollingCache
}

func (p *Poller) run(ctx context.Context) {
	defer close(p.doneChan)

	pollTicker := time.NewTicker(p.config.PollInterval)
	defer pollTicker.Stop()

	cleanupTicker := time.NewTicker(p.config.CleanupInterval)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopChan:
			return
		case <-pollTicker.C:
			// Refresh device cache if stale
			if p.deviceCache.NeedsRefresh(p.config.DeviceCacheRefresh) {
				if err := p.refreshDeviceCache(ctx); err != nil {
					log.Printf("Warning: device cache refresh failed: %v", err)
				}
			}

			if err := p.poll(ctx); err != nil {
				log.Printf("Poll failed: %v", err)
				p.mu.Lock()
				p.pollErrors++
				p.mu.Unlock()
			}
		case <-cleanupTicker.C:
			if err := p.cleanup(ctx); err != nil {
				log.Printf("Cleanup failed: %v", err)
			}
		}
	}
}

func (p *Poller) refreshDeviceCache(ctx context.Context) error {
	devicesResp, err := p.tsService.GetDevices()
	if err != nil {
		return err
	}
	p.deviceCache.Update(devicesResp.Devices)
	log.Printf("Device cache refreshed: %d devices", len(devicesResp.Devices))
	return nil
}

func (p *Poller) poll(ctx context.Context) error {
	// Get last poll state
	pollState, err := p.store.GetPollState(ctx)
	if err != nil {
		return err
	}

	var start time.Time
	end := time.Now()

	if pollState.LastPollEnd.IsZero() {
		// First poll - backfill
		start = end.Add(-p.config.InitialBackfill)
		log.Printf("First poll, backfilling from %v", start)
	} else {
		// Continue from where we left off (with small overlap)
		start = pollState.LastPollEnd.Add(-30 * time.Second)
	}

	// Fetch logs from Tailscale API
	logsResp, err := p.tsService.GetNetworkLogs(
		start.Format(time.RFC3339),
		end.Format(time.RFC3339),
	)
	if err != nil {
		return err
	}

	// Convert to flow logs
	flowLogs := p.convertLogs(logsResp)
	if len(flowLogs) == 0 {
		// Update poll state even with no logs
		return p.store.UpdatePollState(ctx, end)
	}

	// Insert raw flow logs (temporary storage)
	insertedCount, err := p.store.InsertFlowLogs(ctx, flowLogs)
	if err != nil {
		return err
	}

	// Pre-aggregate at poll time: node pairs and bandwidth
	nodePairs, totalBandwidth, nodeBandwidth := p.aggregate(flowLogs, start)

	// Store aggregates
	if len(nodePairs) > 0 {
		if err := p.store.UpsertNodePairAggregates(ctx, nodePairs); err != nil {
			log.Printf("Warning: failed to upsert node pairs: %v", err)
		}
	}

	if len(totalBandwidth) > 0 {
		if err := p.store.UpsertBandwidth(ctx, totalBandwidth, 60); err != nil {
			log.Printf("Warning: failed to upsert bandwidth: %v", err)
		}
	}

	if len(nodeBandwidth) > 0 {
		if err := p.store.UpsertNodeBandwidth(ctx, nodeBandwidth, 60); err != nil {
			log.Printf("Warning: failed to upsert node bandwidth: %v", err)
		}
	}

	// Update rolling cache for fast live view queries
	p.rollingCache.Update(nodePairs, totalBandwidth, nodeBandwidth)

	// Update poll state
	if err := p.store.UpdatePollState(ctx, end); err != nil {
		return err
	}

	// Update stats
	p.mu.Lock()
	p.lastPollTime = time.Now()
	p.lastPollCount = insertedCount
	p.totalPolled += int64(insertedCount)
	p.mu.Unlock()

	if insertedCount > 0 {
		log.Printf("Polled %d flow logs, aggregated %d node pairs, %d bandwidth buckets (%v to %v)",
			insertedCount, len(nodePairs), len(totalBandwidth),
			start.Format(time.RFC3339), end.Format(time.RFC3339))
	}

	return nil
}

// aggregate creates pre-computed aggregates from flow logs
func (p *Poller) aggregate(logs []database.FlowLog, baseTime time.Time) (
	[]database.NodePairAggregate,
	[]database.BandwidthBucket,
	[]database.NodeBandwidth,
) {
	// Node pair aggregation: group by (bucket, srcNode, dstNode, trafficType)
	type nodePairKey struct {
		bucket      int64
		srcNodeID   string
		dstNodeID   string
		trafficType string
	}
	nodePairMap := make(map[nodePairKey]*database.NodePairAggregate)

	// Total bandwidth: group by bucket
	bandwidthMap := make(map[int64]*database.BandwidthBucket)

	// Per-node bandwidth: group by (bucket, nodeID)
	type nodeBwKey struct {
		bucket int64
		nodeID string
	}
	nodeBwMap := make(map[nodeBwKey]*database.NodeBandwidth)

	bucketSize := int64(60) // 1-minute buckets

	for _, log := range logs {
		// Calculate bucket
		bucket := (log.LoggedAt.Unix() / bucketSize) * bucketSize

		// Resolve IPs to device IDs
		srcNodeID := p.deviceCache.ResolveIP(log.SrcIP)
		dstNodeID := p.deviceCache.ResolveIP(log.DstIP)

		// Node pair aggregate
		npKey := nodePairKey{
			bucket:      bucket,
			srcNodeID:   srcNodeID,
			dstNodeID:   dstNodeID,
			trafficType: log.TrafficType,
		}

		if agg, ok := nodePairMap[npKey]; ok {
			agg.TxBytes += log.TxBytes
			agg.RxBytes += log.RxBytes
			agg.TxPkts += log.TxPkts
			agg.RxPkts += log.RxPkts
			agg.FlowCount++
		} else {
			nodePairMap[npKey] = &database.NodePairAggregate{
				Bucket:      bucket,
				SrcNodeID:   srcNodeID,
				DstNodeID:   dstNodeID,
				TrafficType: log.TrafficType,
				TxBytes:     log.TxBytes,
				RxBytes:     log.RxBytes,
				TxPkts:      log.TxPkts,
				RxPkts:      log.RxPkts,
				FlowCount:   1,
				Protocols:   "[]",
				Ports:       "[]",
			}
		}

		// Total bandwidth
		if bw, ok := bandwidthMap[bucket]; ok {
			bw.TxBytes += log.TxBytes
			bw.RxBytes += log.RxBytes
		} else {
			bandwidthMap[bucket] = &database.BandwidthBucket{
				Time:    time.Unix(bucket, 0).UTC(),
				TxBytes: log.TxBytes,
				RxBytes: log.RxBytes,
			}
		}

		// Per-node bandwidth (track src node)
		srcBwKey := nodeBwKey{bucket: bucket, nodeID: srcNodeID}
		if bw, ok := nodeBwMap[srcBwKey]; ok {
			bw.TxBytes += log.TxBytes
		} else {
			nodeBwMap[srcBwKey] = &database.NodeBandwidth{
				Bucket:  bucket,
				NodeID:  srcNodeID,
				TxBytes: log.TxBytes,
				RxBytes: 0,
			}
		}

		// Per-node bandwidth (track dst node for RX)
		dstBwKey := nodeBwKey{bucket: bucket, nodeID: dstNodeID}
		if bw, ok := nodeBwMap[dstBwKey]; ok {
			bw.RxBytes += log.RxBytes
		} else {
			nodeBwMap[dstBwKey] = &database.NodeBandwidth{
				Bucket:  bucket,
				NodeID:  dstNodeID,
				TxBytes: 0,
				RxBytes: log.RxBytes,
			}
		}
	}

	// Convert maps to slices
	nodePairs := make([]database.NodePairAggregate, 0, len(nodePairMap))
	for _, agg := range nodePairMap {
		nodePairs = append(nodePairs, *agg)
	}

	totalBandwidth := make([]database.BandwidthBucket, 0, len(bandwidthMap))
	for _, bw := range bandwidthMap {
		totalBandwidth = append(totalBandwidth, *bw)
	}

	nodeBandwidth := make([]database.NodeBandwidth, 0, len(nodeBwMap))
	for _, bw := range nodeBwMap {
		nodeBandwidth = append(nodeBandwidth, *bw)
	}

	return nodePairs, totalBandwidth, nodeBandwidth
}

func (p *Poller) cleanup(ctx context.Context) error {
	deleted, err := p.store.Cleanup(ctx,
		p.config.RetentionMinutely,
		p.config.RetentionHourly,
		p.config.RetentionDaily,
	)
	if err != nil {
		return err
	}
	if deleted > 0 {
		log.Printf("Cleaned up %d old records", deleted)
	}
	return nil
}

// convertLogs converts Tailscale API response to database FlowLog entries
func (p *Poller) convertLogs(logsResp interface{}) []database.FlowLog {
	var flowLogs []database.FlowLog

	logsMap, ok := logsResp.(map[string]interface{})
	if !ok {
		return flowLogs
	}

	logs, ok := logsMap["logs"]
	if !ok {
		return flowLogs
	}

	// Handle []tailscale.NetworkFlowLog
	if tsLogs, ok := logs.([]tailscale.NetworkFlowLog); ok {
		for _, tsLog := range tsLogs {
			flowLogs = append(flowLogs, p.convertTailscaleLog(tsLog)...)
		}
		return flowLogs
	}

	// Handle []interface{} (generic JSON)
	if logsArray, ok := logs.([]interface{}); ok {
		for _, logItem := range logsArray {
			if logMap, ok := logItem.(map[string]interface{}); ok {
				flowLogs = append(flowLogs, p.convertMapLog(logMap)...)
			}
		}
	}

	return flowLogs
}

func (p *Poller) convertTailscaleLog(tsLog tailscale.NetworkFlowLog) []database.FlowLog {
	var flowLogs []database.FlowLog

	// Process virtual traffic
	for _, traffic := range tsLog.VirtualTraffic {
		flowLogs = append(flowLogs, database.FlowLog{
			LoggedAt:    tsLog.Logged,
			NodeID:      tsLog.NodeID,
			TrafficType: "virtual",
			Protocol:    traffic.Proto,
			SrcIP:       extractIP(traffic.Src),
			SrcPort:     extractPort(traffic.Src),
			DstIP:       extractIP(traffic.Dst),
			DstPort:     extractPort(traffic.Dst),
			TxBytes:     int64(traffic.TxBytes),
			RxBytes:     int64(traffic.RxBytes),
			TxPkts:      int64(traffic.TxPkts),
			RxPkts:      int64(traffic.RxPkts),
		})
	}

	// Process subnet traffic
	for _, traffic := range tsLog.SubnetTraffic {
		flowLogs = append(flowLogs, database.FlowLog{
			LoggedAt:    tsLog.Logged,
			NodeID:      tsLog.NodeID,
			TrafficType: "subnet",
			Protocol:    traffic.Proto,
			SrcIP:       extractIP(traffic.Src),
			SrcPort:     extractPort(traffic.Src),
			DstIP:       extractIP(traffic.Dst),
			DstPort:     extractPort(traffic.Dst),
			TxBytes:     int64(traffic.TxBytes),
			RxBytes:     int64(traffic.RxBytes),
			TxPkts:      int64(traffic.TxPkts),
			RxPkts:      int64(traffic.RxPkts),
		})
	}

	// Process physical traffic
	for _, traffic := range tsLog.PhysicalTraffic {
		flowLogs = append(flowLogs, database.FlowLog{
			LoggedAt:    tsLog.Logged,
			NodeID:      tsLog.NodeID,
			TrafficType: "physical",
			Protocol:    traffic.Proto,
			SrcIP:       extractIP(traffic.Src),
			SrcPort:     extractPort(traffic.Src),
			DstIP:       extractIP(traffic.Dst),
			DstPort:     extractPort(traffic.Dst),
			TxBytes:     int64(traffic.TxBytes),
			RxBytes:     0,
			TxPkts:      int64(traffic.TxPkts),
			RxPkts:      0,
		})
	}

	return flowLogs
}

func (p *Poller) convertMapLog(logMap map[string]interface{}) []database.FlowLog {
	var flowLogs []database.FlowLog

	nodeID, _ := logMap["nodeId"].(string)
	logged, _ := time.Parse(time.RFC3339, getString(logMap, "logged"))

	// Process each traffic type
	for _, trafficType := range []string{"virtualTraffic", "subnetTraffic", "physicalTraffic"} {
		if traffic, ok := logMap[trafficType].([]interface{}); ok {
			typeName := strings.TrimSuffix(trafficType, "Traffic")
			for _, t := range traffic {
				if tMap, ok := t.(map[string]interface{}); ok {
					flowLogs = append(flowLogs, database.FlowLog{
						LoggedAt:    logged,
						NodeID:      nodeID,
						TrafficType: typeName,
						Protocol:    getInt(tMap, "proto"),
						SrcIP:       extractIP(getString(tMap, "src")),
						SrcPort:     extractPort(getString(tMap, "src")),
						DstIP:       extractIP(getString(tMap, "dst")),
						DstPort:     extractPort(getString(tMap, "dst")),
						TxBytes:     getInt64(tMap, "txBytes"),
						RxBytes:     getInt64(tMap, "rxBytes"),
						TxPkts:      getInt64(tMap, "txPkts"),
						RxPkts:      getInt64(tMap, "rxPkts"),
					})
				}
			}
		}
	}

	return flowLogs
}

// Helper functions
func extractIP(addr string) string {
	// Handle IPv6 with brackets: [::1]:443
	if strings.HasPrefix(addr, "[") {
		end := strings.Index(addr, "]")
		if end > 0 {
			return addr[1:end]
		}
	}
	// Handle IPv4: 192.168.1.1:443
	if idx := strings.LastIndex(addr, ":"); idx > 0 {
		return addr[:idx]
	}
	return addr
}

func extractPort(addr string) int {
	// Handle IPv6 with brackets: [::1]:443
	if strings.HasPrefix(addr, "[") {
		end := strings.Index(addr, "]:")
		if end > 0 {
			var port int
			_, _ = parsePort(addr[end+2:], &port)
			return port
		}
		return 0
	}
	// Handle IPv4: 192.168.1.1:443
	if idx := strings.LastIndex(addr, ":"); idx > 0 {
		var port int
		_, _ = parsePort(addr[idx+1:], &port)
		return port
	}
	return 0
}

func parsePort(s string, port *int) (bool, error) {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false, nil
		}
		*port = *port*10 + int(c-'0')
	}
	return true, nil
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getInt(m map[string]interface{}, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}

func getInt64(m map[string]interface{}, key string) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return 0
}

// MarshalJSON helper for protocols/ports
func toJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}
