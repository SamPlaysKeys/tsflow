package services

import (
	"context"
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
	// RetentionPeriod is how long to keep logs before cleanup
	RetentionPeriod time.Duration
	// CleanupInterval is how often to run cleanup
	CleanupInterval time.Duration
}

// DefaultPollerConfig returns sensible defaults
func DefaultPollerConfig() PollerConfig {
	return PollerConfig{
		PollInterval:    5 * time.Minute,
		InitialBackfill: 1 * time.Hour,
		RetentionPeriod: 7 * 24 * time.Hour, // 7 days
		CleanupInterval: 1 * time.Hour,
	}
}

// Poller fetches network logs from Tailscale API and stores them in the database
type Poller struct {
	tsService *TailscaleService
	store     database.Store
	config    PollerConfig

	mu       sync.RWMutex
	running  bool
	stopChan chan struct{}
	doneChan chan struct{}

	// Stats
	lastPollTime   time.Time
	lastPollCount  int
	totalPolled    int64
	pollErrors     int64
}

// NewPoller creates a new background poller
func NewPoller(tsService *TailscaleService, store database.Store, config PollerConfig) *Poller {
	return &Poller{
		tsService: tsService,
		store:     store,
		config:    config,
		stopChan:  make(chan struct{}),
		doneChan:  make(chan struct{}),
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

	log.Printf("Starting background poller (interval: %v, retention: %v)", p.config.PollInterval, p.config.RetentionPeriod)

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
	p.stopChan = nil // Prevent double-close
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
		// Continue from where we left off (with small overlap to catch any missed logs)
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

	// Convert and store logs
	flowLogs := p.convertLogs(logsResp)
	var insertedCount int
	if len(flowLogs) > 0 {
		// InsertFlowLogs returns only newly inserted logs (excludes duplicates)
		insertedLogs, err := p.store.InsertFlowLogs(ctx, flowLogs)
		if err != nil {
			return err
		}
		insertedCount = len(insertedLogs)
		// Update bandwidth rollups only for newly inserted logs (prevents double-counting)
		if insertedCount > 0 {
			if err := p.store.UpdateBandwidthRollups(ctx, insertedLogs); err != nil {
				log.Printf("Warning: failed to update bandwidth rollups: %v", err)
				// Don't fail the poll, rollups can be rebuilt
			}
		}
	}

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
		log.Printf("Polled %d flow logs, %d new (%v to %v)", len(flowLogs), insertedCount, start.Format(time.RFC3339), end.Format(time.RFC3339))
	}

	return nil
}

func (p *Poller) cleanup(ctx context.Context) error {
	// Cleanup old flow logs
	deleted, err := p.store.CleanupOldLogs(ctx, p.config.RetentionPeriod)
	if err != nil {
		return err
	}
	if deleted > 0 {
		log.Printf("Cleaned up %d old flow logs", deleted)
	}

	// Cleanup old rollup data (minutely: 24h, hourly: 30d)
	rollupDeleted, err := p.store.CleanupOldRollups(ctx)
	if err != nil {
		log.Printf("Warning: failed to cleanup old rollups: %v", err)
		// Don't fail cleanup, flow_logs cleanup succeeded
	} else if rollupDeleted > 0 {
		log.Printf("Cleaned up %d old rollup buckets", rollupDeleted)
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
			PeriodStart: tsLog.Start,
			PeriodEnd:   tsLog.End,
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
			PeriodStart: tsLog.Start,
			PeriodEnd:   tsLog.End,
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
			PeriodStart: tsLog.Start,
			PeriodEnd:   tsLog.End,
			TrafficType: "physical",
			Protocol:    traffic.Proto,
			SrcIP:       extractIP(traffic.Src),
			SrcPort:     extractPort(traffic.Src),
			DstIP:       extractIP(traffic.Dst),
			DstPort:     extractPort(traffic.Dst),
			TxBytes:     int64(traffic.TxBytes),
			RxBytes:     0, // Physical traffic doesn't have RxBytes
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
	start, _ := time.Parse(time.RFC3339, getString(logMap, "start"))
	end, _ := time.Parse(time.RFC3339, getString(logMap, "end"))

	// Process each traffic type
	for _, trafficType := range []string{"virtualTraffic", "subnetTraffic", "physicalTraffic"} {
		if traffic, ok := logMap[trafficType].([]interface{}); ok {
			typeName := strings.TrimSuffix(trafficType, "Traffic")
			for _, t := range traffic {
				if tMap, ok := t.(map[string]interface{}); ok {
					flowLogs = append(flowLogs, database.FlowLog{
						LoggedAt:    logged,
						NodeID:      nodeID,
						PeriodStart: start,
						PeriodEnd:   end,
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
