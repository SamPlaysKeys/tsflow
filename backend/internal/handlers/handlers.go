package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rajsinghtech/tsflow/backend/internal/database"
	"github.com/rajsinghtech/tsflow/backend/internal/services"
	tailscale "tailscale.com/client/tailscale/v2"
)

const (
	// ChunkThreshold is the duration above which log queries are chunked
	ChunkThreshold = 7 * 24 * time.Hour
	// ChunkSize is the size of each chunk for large log queries
	ChunkSize = 24 * time.Hour
	// MaxParallelChunks limits concurrent chunk fetches
	MaxParallelChunks = 2
	// MaxLogsInMemory limits logs held in memory during chunked queries
	MaxLogsInMemory = 10000
	// MaxLogsInResponse limits logs returned in a single response
	MaxLogsInResponse = 50000
	// DefaultQueryTimeout is the default timeout for database queries
	DefaultQueryTimeout = 30 * time.Second
	// ShortQueryTimeout is the timeout for quick database queries
	ShortQueryTimeout = 10 * time.Second
	// AggregationQueryTimeout is the timeout for heavy aggregation queries
	AggregationQueryTimeout = 60 * time.Second
)

type Handlers struct {
	tailscaleService *services.TailscaleService
	store            database.Store
	poller           *services.Poller
}

func NewHandlers(tailscaleService *services.TailscaleService, store database.Store, poller *services.Poller) *Handlers {
	return &Handlers{
		tailscaleService: tailscaleService,
		store:            store,
		poller:           poller,
	}
}

func (h *Handlers) HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":    "healthy",
		"timestamp": time.Now().UTC(),
		"service":   "tsflow-backend",
	})
}

func (h *Handlers) GetDevices(c *gin.Context) {
	devices, err := h.tailscaleService.GetDevices()
	if err != nil {
		log.Printf("ERROR GetDevices failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to fetch devices",
			"message": err.Error(),
		})
		return
	}

	log.Printf("SUCCESS GetDevices: returned devices successfully")
	c.JSON(http.StatusOK, devices)
}

func (h *Handlers) GetServicesAndRecords(c *gin.Context) {
	ctx := c.Request.Context()

	// Fetch VIP services
	vipServices, servicesErr := h.tailscaleService.GetVIPServices(ctx)
	if servicesErr != nil {
		log.Printf("WARNING GetVIPServices failed: %v", servicesErr)
		vipServices = make(map[string]services.VIPServiceInfo)
	}

	// Fetch static records
	staticRecords, recordsErr := h.tailscaleService.GetStaticRecords(ctx)
	if recordsErr != nil {
		log.Printf("WARNING GetStaticRecords failed: %v", recordsErr)
		staticRecords = make(map[string]services.StaticRecordInfo)
	}

	response := gin.H{
		"services": vipServices,
		"records":  staticRecords,
	}

	log.Printf("SUCCESS GetServicesAndRecords: returned %d services and %d records", len(vipServices), len(staticRecords))
	c.JSON(http.StatusOK, response)
}

func (h *Handlers) GetNetworkLogs(c *gin.Context) {
	start := c.Query("start")
	end := c.Query("end")

	if start == "" || end == "" {
		now := time.Now()
		start = now.Add(-5 * time.Minute).Format(time.RFC3339)
		end = now.Format(time.RFC3339)
	}

	st, err := time.Parse(time.RFC3339, start)
	if err != nil {
		log.Printf("ERROR GetNetworkLogs: invalid start time %s: %v", start, err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "bad start time",
			"message": err.Error(),
		})
		return
	}

	et, err := time.Parse(time.RFC3339, end)
	if err != nil {
		log.Printf("ERROR GetNetworkLogs: invalid end time %s: %v", end, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad end time", "message": err.Error()})
		return
	}

	if et.Before(st) {
		log.Printf("ERROR GetNetworkLogs: end time before start time: %s < %s", end, start)
		c.JSON(http.StatusBadRequest, gin.H{"error": "end time before start time"})
		return
	}

	now := time.Now()
	if st.After(now) {
		log.Printf("ERROR GetNetworkLogs: future start time not allowed: %s", start)
		c.JSON(http.StatusBadRequest, gin.H{"error": "future start time not allowed"})
		return
	}

	duration := et.Sub(st)
	// Use chunking for queries longer than threshold to prevent response size issues
	if duration > ChunkThreshold {
		chunks, err := h.tailscaleService.GetNetworkLogsChunkedParallel(start, end, ChunkSize, MaxParallelChunks)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "Failed to fetch network logs",
				"message": err.Error(),
				"hint":    "Try selecting a smaller time range",
			})
			return
		}

		var allLogs []any

	chunkLoop:
		for _, chunk := range chunks {
			if logsArray, ok := chunk.([]any); ok {
				if len(allLogs)+len(logsArray) > MaxLogsInMemory {
					remaining := MaxLogsInMemory - len(allLogs)
					if remaining > 0 {
						allLogs = append(allLogs, logsArray[:remaining]...)
					}
					break
				}
				allLogs = append(allLogs, logsArray...)
			} else if logsMap, ok := chunk.(map[string]any); ok {
				if logs, exists := logsMap["logs"]; exists {
					if logsArray, ok := logs.([]any); ok {
						if len(allLogs)+len(logsArray) > MaxLogsInMemory {
							remaining := MaxLogsInMemory - len(allLogs)
							if remaining > 0 {
								allLogs = append(allLogs, logsArray[:remaining]...)
							}
							break
						}
						allLogs = append(allLogs, logsArray...)
					} else if logsArray, ok := logs.([]tailscale.NetworkFlowLog); ok {
						for _, log := range logsArray {
							if len(allLogs) >= MaxLogsInMemory {
								break chunkLoop
							}
							allLogs = append(allLogs, log)
						}
					}
				}
			}
		}

		// Sample logs if too many to prevent response size issues
		finalLogs := allLogs
		if len(allLogs) > MaxLogsInResponse {
			sampleRate := max(len(allLogs)/MaxLogsInResponse, 1)
			sampledLogs := make([]any, 0, MaxLogsInResponse)
			for i := 0; i < len(allLogs); i += sampleRate {
				sampledLogs = append(sampledLogs, allLogs[i])
			}
			finalLogs = sampledLogs
		}

		sampleRate := 1
		if len(finalLogs) > 0 && len(allLogs) >= len(finalLogs) {
			sampleRate = len(allLogs) / len(finalLogs)
		}
		c.JSON(http.StatusOK, gin.H{
			"logs": finalLogs,
			"metadata": gin.H{
				"chunked":    true,
				"chunks":     len(chunks),
				"duration":   duration.String(),
				"totalLogs":  len(allLogs),
				"sampled":    len(finalLogs) < len(allLogs),
				"sampleRate": sampleRate,
			},
		})
		return
	}

	logs, err := h.tailscaleService.GetNetworkLogs(start, end)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to fetch network logs",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, logs)
}

// GetStoredFlowLogs retrieves recent flow logs from the local SQLite database
func (h *Handlers) GetStoredFlowLogs(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Database not configured",
		})
		return
	}

	start := c.Query("start")
	end := c.Query("end")
	limitStr := c.Query("limit")

	var startTime, endTime time.Time
	var err error
	var limit int

	if start == "" {
		startTime = time.Now().Add(-10 * time.Minute)
	} else {
		startTime, err = time.Parse(time.RFC3339, start)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start time"})
			return
		}
	}

	if end == "" {
		endTime = time.Now()
	} else {
		endTime, err = time.Parse(time.RFC3339, end)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end time"})
			return
		}
	}

	if limitStr != "" {
		if _, err := fmt.Sscanf(limitStr, "%d", &limit); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
			return
		}
	}
	if limit <= 0 || limit > MaxLogsInResponse {
		limit = MaxLogsInResponse
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), DefaultQueryTimeout)
	defer cancel()

	logs, err := h.store.GetFlowLogsInRange(ctx, startTime, endTime, limit)
	if err != nil {
		log.Printf("ERROR GetStoredFlowLogs: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to fetch stored logs",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"logs": logs,
		"metadata": gin.H{
			"count":  len(logs),
			"start":  startTime,
			"end":    endTime,
			"limit":  limit,
			"source": "database",
		},
	})
}

// GetAggregatedFlowLogs returns pre-aggregated node-to-node traffic
// This is the scalable endpoint for large networks - uses pre-computed node pairs
func (h *Handlers) GetAggregatedFlowLogs(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Database not configured",
		})
		return
	}

	start := c.Query("start")
	end := c.Query("end")

	var startTime, endTime time.Time
	var err error

	if start == "" {
		startTime = time.Now().Add(-1 * time.Hour)
	} else {
		startTime, err = time.Parse(time.RFC3339, start)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start time"})
			return
		}
	}

	if end == "" {
		endTime = time.Now()
	} else {
		endTime, err = time.Parse(time.RFC3339, end)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end time"})
			return
		}
	}

	// Calculate bucket size based on time range
	duration := endTime.Sub(startTime)
	var bucketSize int64 = 60 // 1 minute
	if duration > 24*time.Hour {
		bucketSize = 3600 // 1 hour
	}
	if duration > 7*24*time.Hour {
		bucketSize = 86400 // 1 day
	}

	var aggregates []database.NodePairAggregate
	source := "database"

	// Try rolling cache first for recent data (within last hour)
	if h.poller != nil && duration <= time.Hour {
		cache := h.poller.GetRollingCache()
		if cache.HasDataFor(startTime, endTime) {
			aggregates = cache.GetNodePairs(startTime, endTime)
			source = "cache"
		}
	}

	// Fall back to database if cache miss or no data
	if len(aggregates) == 0 {
		ctx, cancel := context.WithTimeout(c.Request.Context(), AggregationQueryTimeout)
		defer cancel()

		// Use pre-computed node pair aggregates
		aggregates, err = h.store.GetNodePairAggregates(ctx, startTime, endTime, bucketSize)
		if err != nil {
			log.Printf("ERROR GetAggregatedFlowLogs: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "Failed to fetch aggregated flows",
				"message": err.Error(),
			})
			return
		}
		source = "database"
	}

	// Convert to frontend-expected format
	flows := make([]gin.H, 0, len(aggregates))
	for _, agg := range aggregates {
		flows = append(flows, gin.H{
			"srcNodeId":    agg.SrcNodeID,
			"dstNodeId":    agg.DstNodeID,
			"trafficType":  agg.TrafficType,
			"totalTxBytes": agg.TxBytes,
			"totalRxBytes": agg.RxBytes,
			"totalTxPkts":  agg.TxPkts,
			"totalRxPkts":  agg.RxPkts,
			"flowCount":    agg.FlowCount,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"flows": flows,
		"metadata": gin.H{
			"count":      len(flows),
			"start":      startTime,
			"end":        endTime,
			"bucketSize": bucketSize,
			"source":     source,
		},
	})
}

// GetDataRange returns the available time range of stored data
func (h *Handlers) GetDataRange(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Database not configured",
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), ShortQueryTimeout)
	defer cancel()

	dataRange, err := h.store.GetDataRange(ctx)
	if err != nil {
		log.Printf("ERROR GetDataRange: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to get data range",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, dataRange)
}

// GetPollerStatus returns the current status of the background poller
func (h *Handlers) GetPollerStatus(c *gin.Context) {
	if h.poller == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Poller not configured",
		})
		return
	}

	stats := h.poller.Stats()

	// Add database stats if available
	if h.store != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), ShortQueryTimeout)
		defer cancel()

		dbStats, err := h.store.GetStats(ctx)
		if err == nil {
			stats["database"] = dbStats
		}
	}

	c.JSON(http.StatusOK, stats)
}

// TriggerPoll manually triggers a poll (for testing/debugging)
func (h *Handlers) TriggerPoll(c *gin.Context) {
	if h.poller == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Poller not configured",
		})
		return
	}

	// The poller will poll on next tick, but we can return current stats
	stats := h.poller.Stats()
	c.JSON(http.StatusOK, gin.H{
		"message": "Poll will occur on next interval",
		"stats":   stats,
	})
}

// GetBandwidthAggregated returns aggregated bandwidth data for the chart
func (h *Handlers) GetBandwidthAggregated(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Database not configured",
		})
		return
	}

	start := c.Query("start")
	end := c.Query("end")
	nodeID := c.Query("nodeId") // Optional: filter by device ID

	var err error
	var startTime, endTime time.Time

	if start == "" {
		startTime = time.Now().Add(-1 * time.Hour)
	} else {
		startTime, err = time.Parse(time.RFC3339, start)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start time"})
			return
		}
	}

	if end == "" {
		endTime = time.Now()
	} else {
		endTime, err = time.Parse(time.RFC3339, end)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end time"})
			return
		}
	}

	var buckets []database.BandwidthBucket
	source := "database"

	// Try rolling cache first for recent data (within last hour)
	if h.poller != nil {
		cache := h.poller.GetRollingCache()
		if cache.HasDataFor(startTime, endTime) {
			if nodeID != "" {
				buckets = cache.GetNodeBandwidth(startTime, endTime, nodeID)
			} else {
				buckets = cache.GetBandwidth(startTime, endTime)
			}
			source = "cache"
		}
	}

	// Fall back to database if cache miss or no data
	if len(buckets) == 0 {
		ctx, cancel := context.WithTimeout(c.Request.Context(), DefaultQueryTimeout)
		defer cancel()

		// If nodeId provided, filter by that node
		if nodeID != "" {
			buckets, err = h.store.GetNodeBandwidth(ctx, startTime, endTime, nodeID)
		} else {
			buckets, err = h.store.GetBandwidth(ctx, startTime, endTime)
		}

		if err != nil {
			log.Printf("ERROR GetBandwidthAggregated: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "Failed to fetch bandwidth data",
				"message": err.Error(),
			})
			return
		}
		source = "database"
	}

	c.JSON(http.StatusOK, gin.H{
		"buckets": buckets,
		"metadata": gin.H{
			"count":  len(buckets),
			"start":  startTime,
			"end":    endTime,
			"nodeId": nodeID,
			"source": source,
		},
	})
}

// GetBandwidthByIPs returns bandwidth data filtered by IP addresses
// This is for backwards compatibility - converts IPs to node IDs using device cache
func (h *Handlers) GetBandwidthByIPs(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Database not configured",
		})
		return
	}

	start := c.Query("start")
	end := c.Query("end")
	ipsStr := c.Query("ips") // Comma-separated list of IPs

	var err error
	var startTime, endTime time.Time

	if start == "" {
		startTime = time.Now().Add(-1 * time.Hour)
	} else {
		startTime, err = time.Parse(time.RFC3339, start)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start time"})
			return
		}
	}

	if end == "" {
		endTime = time.Now()
	} else {
		endTime, err = time.Parse(time.RFC3339, end)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end time"})
			return
		}
	}

	// If no IPs provided, return total bandwidth
	if ipsStr == "" {
		redirectURL := "/api/bandwidth?start=" + url.QueryEscape(startTime.Format(time.RFC3339)) + "&end=" + url.QueryEscape(endTime.Format(time.RFC3339))
		c.Redirect(http.StatusTemporaryRedirect, redirectURL)
		return
	}

	// Parse IPs and resolve to node IDs using device cache
	ips := strings.Split(ipsStr, ",")
	for i := range ips {
		ips[i] = strings.TrimSpace(ips[i])
	}

	// Use poller's device cache to resolve IPs to node IDs
	nodeIDs := make(map[string]bool)
	if h.poller != nil {
		cache := h.poller.GetDeviceCache()
		for _, ip := range ips {
			nodeID := cache.ResolveIP(ip)
			nodeIDs[nodeID] = true
		}
	} else {
		// Fallback: use IPs as node IDs (for external IPs)
		for _, ip := range ips {
			nodeIDs[ip] = true
		}
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), DefaultQueryTimeout)
	defer cancel()

	// Aggregate bandwidth for all node IDs
	var allBuckets []database.BandwidthBucket
	bucketMap := make(map[int64]*database.BandwidthBucket)

	for nodeID := range nodeIDs {
		buckets, err := h.store.GetNodeBandwidth(ctx, startTime, endTime, nodeID)
		if err != nil {
			continue // Skip errors for individual nodes
		}
		for _, b := range buckets {
			bucket := b.Time.Unix()
			if existing, ok := bucketMap[bucket]; ok {
				existing.TxBytes += b.TxBytes
				existing.RxBytes += b.RxBytes
			} else {
				bucketMap[bucket] = &database.BandwidthBucket{
					Time:    b.Time,
					TxBytes: b.TxBytes,
					RxBytes: b.RxBytes,
				}
			}
		}
	}

	// Convert map to slice and sort by time
	for _, b := range bucketMap {
		allBuckets = append(allBuckets, *b)
	}
	sort.Slice(allBuckets, func(i, j int) bool {
		return allBuckets[i].Time.Before(allBuckets[j].Time)
	})

	c.JSON(http.StatusOK, gin.H{
		"buckets": allBuckets,
		"metadata": gin.H{
			"count": len(allBuckets),
			"start": startTime,
			"end":   endTime,
			"ips":   ipsStr,
		},
	})
}

func (h *Handlers) GetNetworkMap(c *gin.Context) {
	networkMap, err := h.tailscaleService.GetNetworkMap()
	if err != nil {
		log.Printf("ERROR GetNetworkMap failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to fetch network map",
			"message": err.Error(),
		})
		return
	}

	log.Printf("SUCCESS GetNetworkMap: returned network map")
	c.JSON(http.StatusOK, networkMap)
}

func (h *Handlers) GetDeviceFlows(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"error":   "Not implemented",
		"message": "Tailscale does not expose per-device flow data",
	})
}

func (h *Handlers) GetDNSNameservers(c *gin.Context) {
	nameservers, err := h.tailscaleService.GetDNSNameservers()
	if err != nil {
		log.Printf("ERROR GetDNSNameservers failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to fetch DNS nameservers",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, nameservers)
}

// parseTimeRange extracts and validates start/end query params.
// Defaults: start = 1 hour ago, end = now.
func (h *Handlers) parseTimeRange(c *gin.Context) (time.Time, time.Time, error) {
	var startTime, endTime time.Time
	var err error

	if s := c.Query("start"); s != "" {
		startTime, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start time: %w", err)
		}
	} else {
		startTime = time.Now().Add(-1 * time.Hour)
	}

	if e := c.Query("end"); e != "" {
		endTime, err = time.Parse(time.RFC3339, e)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end time: %w", err)
		}
	} else {
		endTime = time.Now()
	}

	if endTime.Before(startTime) {
		return time.Time{}, time.Time{}, fmt.Errorf("end time before start time")
	}

	return startTime, endTime, nil
}

// GetStatsOverview returns network-wide statistics for a time range
func (h *Handlers) GetStatsOverview(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Database not configured"})
		return
	}

	startTime, endTime, err := h.parseTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var buckets []database.TrafficStats
	source := "database"

	// Try rolling cache first for recent data
	duration := endTime.Sub(startTime)
	if h.poller != nil && duration <= time.Hour {
		cache := h.poller.GetRollingCache()
		if cache.HasDataFor(startTime, endTime) {
			buckets = cache.GetTrafficStats(startTime, endTime)
			source = "cache"
		}
	}

	// Fall back to database
	if len(buckets) == 0 {
		ctx, cancel := context.WithTimeout(c.Request.Context(), AggregationQueryTimeout)
		defer cancel()

		buckets, err = h.store.GetTrafficStats(ctx, startTime, endTime)
		if err != nil {
			log.Printf("ERROR GetStatsOverview: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "Failed to fetch traffic stats",
				"message": err.Error(),
			})
			return
		}
		source = "database"
	}

	// Aggregate buckets into summary
	var tcpBytes, udpBytes, otherProtoBytes int64
	var virtualBytes, subnetBytes, physicalBytes int64
	var totalFlows, maxUniquePairs int64
	for _, b := range buckets {
		tcpBytes += b.TCPBytes
		udpBytes += b.UDPBytes
		otherProtoBytes += b.OtherProtoBytes
		virtualBytes += b.VirtualBytes
		subnetBytes += b.SubnetBytes
		physicalBytes += b.PhysicalBytes
		totalFlows += b.TotalFlows
		if b.UniquePairs > maxUniquePairs {
			maxUniquePairs = b.UniquePairs
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"summary": gin.H{
			"tcpBytes":        tcpBytes,
			"udpBytes":        udpBytes,
			"otherProtoBytes": otherProtoBytes,
			"virtualBytes":    virtualBytes,
			"subnetBytes":     subnetBytes,
			"physicalBytes":   physicalBytes,
			"totalFlows":      totalFlows,
			"uniquePairs":     maxUniquePairs,
		},
		"buckets": buckets,
		"metadata": gin.H{
			"start":       startTime,
			"end":         endTime,
			"bucketCount": len(buckets),
			"source":      source,
		},
	})
}

// resolveNodeName returns a human-readable name for a node ID using the device cache.
func (h *Handlers) resolveNodeName(nodeID string) string {
	if h.poller == nil {
		return ""
	}
	cache := h.poller.GetDeviceCache()
	if entry := cache.GetDevice(nodeID); entry != nil {
		// Prefer hostname, fall back to full name
		if entry.Hostname != "" {
			return entry.Hostname
		}
		return entry.Name
	}
	return ""
}

// GetTopTalkers returns the top N nodes by total traffic
func (h *Handlers) GetTopTalkers(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Database not configured"})
		return
	}

	startTime, endTime, err := h.parseTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	limit := 10
	if l := c.Query("limit"); l != "" {
		if _, err := fmt.Sscanf(l, "%d", &limit); err != nil || limit <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
			return
		}
	}
	if limit > 100 {
		limit = 100
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), DefaultQueryTimeout)
	defer cancel()

	talkers, err := h.store.GetTopTalkers(ctx, startTime, endTime, limit)
	if err != nil {
		log.Printf("ERROR GetTopTalkers: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to fetch top talkers",
			"message": err.Error(),
		})
		return
	}

	// Enrich with device names
	enriched := make([]gin.H, 0, len(talkers))
	for _, t := range talkers {
		enriched = append(enriched, gin.H{
			"nodeId":      t.NodeID,
			"displayName": h.resolveNodeName(t.NodeID),
			"txBytes":     t.TxBytes,
			"rxBytes":     t.RxBytes,
			"totalBytes":  t.TotalBytes,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"talkers": enriched,
		"metadata": gin.H{
			"start": startTime,
			"end":   endTime,
			"limit": limit,
			"count": len(enriched),
		},
	})
}

// GetTopPairs returns the top N node pairs by total traffic
func (h *Handlers) GetTopPairs(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Database not configured"})
		return
	}

	startTime, endTime, err := h.parseTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	limit := 10
	if l := c.Query("limit"); l != "" {
		if _, err := fmt.Sscanf(l, "%d", &limit); err != nil || limit <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
			return
		}
	}
	if limit > 100 {
		limit = 100
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), DefaultQueryTimeout)
	defer cancel()

	pairs, err := h.store.GetTopPairs(ctx, startTime, endTime, limit)
	if err != nil {
		log.Printf("ERROR GetTopPairs: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to fetch top pairs",
			"message": err.Error(),
		})
		return
	}

	// Enrich with device names
	enriched := make([]gin.H, 0, len(pairs))
	for _, p := range pairs {
		enriched = append(enriched, gin.H{
			"srcNodeId":      p.SrcNodeID,
			"srcDisplayName": h.resolveNodeName(p.SrcNodeID),
			"dstNodeId":      p.DstNodeID,
			"dstDisplayName": h.resolveNodeName(p.DstNodeID),
			"txBytes":        p.TxBytes,
			"rxBytes":        p.RxBytes,
			"totalBytes":     p.TotalBytes,
			"flowCount":      p.FlowCount,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"pairs": enriched,
		"metadata": gin.H{
			"start": startTime,
			"end":   endTime,
			"limit": limit,
			"count": len(enriched),
		},
	})
}

// GetNodeDetailStats returns detailed stats for a specific node
func (h *Handlers) GetNodeDetailStats(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Database not configured"})
		return
	}

	nodeID := c.Param("id")
	if nodeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "node ID required"})
		return
	}

	startTime, endTime, err := h.parseTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), DefaultQueryTimeout)
	defer cancel()

	stats, err := h.store.GetNodeStats(ctx, nodeID, startTime, endTime)
	if err != nil {
		log.Printf("ERROR GetNodeDetailStats: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to fetch node stats",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, stats)
}
