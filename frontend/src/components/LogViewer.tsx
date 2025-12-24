import React, { useState, useMemo, useCallback, useRef, useEffect } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { useDebounce } from 'use-debounce'
import { 
  ChevronUp, 
  ChevronDown, 
  Download, 
  Search,
  ArrowDown,
  Loader,
  X
} from 'lucide-react'
import type { NetworkFlowLog, TrafficFlow } from '@/types/tailscale'

// Simplified device type for log viewer
interface Device {
  name: string
  addresses: string[]
}

interface LogEntry {
  id: string
  timestamp: string
  nodeId: string
  srcDevice: string
  dstDevice: string
  srcIP: string
  dstIP: string
  srcPort?: number
  dstPort?: number
  protocol: string
  trafficType: 'virtual' | 'subnet' | 'physical'
  txBytes: number
  rxBytes: number
  txPackets: number
  rxPackets: number
  tags: string[]
  timestampMs: number // For faster sorting
}

interface LogViewerProps {
  networkLogs: NetworkFlowLog[]
  devices: Device[]
  searchQuery?: string
  protocolFilters?: Set<string>
  trafficTypeFilters?: Set<string>
  selectedNode?: { id: string; displayName: string; ips?: string[]; ip: string } | null
  selectedLink?: { source: string; target: string; originalSource: string; originalTarget: string } | null
  onSelectLog?: (entry: LogEntry) => void
  onHeightChange?: (height: number) => void
  onClearSelection?: () => void
}

// Worker for heavy data processing
const processLogsWorker = (networkLogs: NetworkFlowLog[], devices: Device[]) => {
  return new Promise<LogEntry[]>((resolve) => {
    // Use setTimeout to prevent blocking the main thread
    setTimeout(() => {
      const entries: LogEntry[] = []
      let entryId = 0

      // Helper functions (duplicated for worker context)
      const extractIP = (address: string): string => {
        if (address.startsWith('[') && address.includes(']:')) {
          return address.substring(1, address.indexOf(']:'))
        }
        const colonIndex = address.lastIndexOf(':')
        if (colonIndex > 0 && !address.includes('::')) {
          return address.substring(0, colonIndex)
        }
        return address
      }

      const extractPort = (address: string): number | null => {
        if (address.startsWith('[') && address.includes(']:')) {
          const portStr = address.split(']:')[1]
          return portStr ? parseInt(portStr, 10) : null
        }
        if (address.includes(':')) {
          const portStr = address.split(':').pop()
          return portStr ? parseInt(portStr, 10) : null
        }
        return null
      }

      const getProtocolName = (proto: number): string => {
        switch (proto) {
          case 1: return 'ICMP'
          case 6: return 'TCP'
          case 17: return 'UDP'
          case 255: return 'Reserved'
          default: return `Proto-${proto}`
        }
      }

      const getDeviceName = (ip: string, devices: Device[]): string => {
        const device = devices.find(d => 
          d.addresses.some(addr => addr === ip || addr.startsWith(ip))
        )
        if (device) {
          const shortName = device.name.split('.')[0]
          return shortName || device.name
        }
        return ip
      }

      // Process in chunks to prevent blocking
      const processChunk = (logs: NetworkFlowLog[], startIndex: number, chunkSize: number = 1000) => {
        const endIndex = Math.min(startIndex + chunkSize, logs.length)
        
        for (let i = startIndex; i < endIndex; i++) {
          const log = logs[i]
          const processTraffic = (traffic: TrafficFlow[], type: 'virtual' | 'subnet' | 'physical') => {
            traffic.forEach((flow) => {
              // Handle both capitalized and lowercase field names
              const srcIP = extractIP((flow as any).Src || (flow as any).src)
              const dstIP = extractIP((flow as any).Dst || (flow as any).dst)
              const timestamp = new Date((log as any).Logged || (log as any).logged)
              
              entries.push({
                id: `${(log as any).NodeID || (log as any).nodeId}-${entryId++}`,
                timestamp: (log as any).Logged || (log as any).logged,
                timestampMs: timestamp.getTime(),
                nodeId: (log as any).NodeID || (log as any).nodeId,
                srcDevice: getDeviceName(srcIP, devices),
                dstDevice: getDeviceName(dstIP, devices),
                srcIP,
                dstIP,
                srcPort: extractPort((flow as any).Src || (flow as any).src) || undefined,
                dstPort: extractPort((flow as any).Dst || (flow as any).dst) || undefined,
                protocol: getProtocolName((flow as any).Proto || (flow as any).proto),
                trafficType: type,
                txBytes: (flow as any).TxBytes || (flow as any).txBytes || 0,
                rxBytes: (flow as any).RxBytes || (flow as any).rxBytes || 0,
                txPackets: (flow as any).TxPkts || (flow as any).txPackets || 0,
                rxPackets: (flow as any).RxPkts || (flow as any).rxPackets || 0,
                tags: []
              })
            })
          }

          // Handle both capitalized and lowercase field names
          const virtualTraffic = (log as any).VirtualTraffic || (log as any).virtualTraffic
          const subnetTraffic = (log as any).SubnetTraffic || (log as any).subnetTraffic
          const physicalTraffic = (log as any).PhysicalTraffic || (log as any).physicalTraffic

          if (virtualTraffic) processTraffic(virtualTraffic, 'virtual')
          if (subnetTraffic) processTraffic(subnetTraffic, 'subnet')
          if (physicalTraffic) processTraffic(physicalTraffic, 'physical')
        }

        if (endIndex < logs.length) {
          // Continue with next chunk
          setTimeout(() => processChunk(logs, endIndex, chunkSize), 0)
        } else {
          // All done, sort by timestamp (newest first)
          entries.sort((a, b) => b.timestampMs - a.timestampMs)
          resolve(entries)
        }
      }

      processChunk(networkLogs, 0)
    }, 0)
  })
}

// Format functions
const formatBytes = (bytes: number): string => {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i]
}

const formatRelativeTime = (timestamp: string): string => {
  const now = new Date()
  const then = new Date(timestamp)
  const diffMs = now.getTime() - then.getTime()
  const diffSec = Math.floor(diffMs / 1000)
  const diffMin = Math.floor(diffSec / 60)
  const diffHr = Math.floor(diffMin / 60)

  if (diffSec < 60) return `${diffSec}s ago`
  if (diffMin < 60) return `${diffMin}m ago`
  if (diffHr < 24) return `${diffHr}h ago`
  // Show full UTC timestamp for logs older than 24 hours
  const month = then.toLocaleString('en-US', { month: 'short', timeZone: 'UTC' })
  const day = then.getUTCDate()
  const hours = then.getUTCHours().toString().padStart(2, '0')
  const minutes = then.getUTCMinutes().toString().padStart(2, '0')
  return `${month} ${day} ${hours}:${minutes} UTC`
}

const formatUTCTimestamp = (timestamp: string): string => {
  const date = new Date(timestamp)
  return date.toISOString().replace('T', ' ').replace(/\.\d{3}Z$/, ' UTC')
}

const LogViewer: React.FC<LogViewerProps> = ({
  networkLogs,
  devices,
  searchQuery = '',
  protocolFilters = new Set(),
  trafficTypeFilters = new Set(),
  selectedNode,
  selectedLink,
  onSelectLog,
  onHeightChange,
  onClearSelection
}) => {
  const [isExpanded, setIsExpanded] = useState(false)
  const [panelHeight, setPanelHeight] = useState(300)
  const [localSearchQuery, setLocalSearchQuery] = useState('')
  const [autoScroll, setAutoScroll] = useState(true)
  const [selectedLogId, setSelectedLogId] = useState<string | null>(null)
  const [isProcessing, setIsProcessing] = useState(false)
  const [logEntries, setLogEntries] = useState<LogEntry[]>([])
  
  const scrollContainerRef = useRef<HTMLDivElement>(null)
  const resizeHandleRef = useRef<HTMLDivElement>(null)

  // Debounced search query
  const [debouncedLocalSearch] = useDebounce(localSearchQuery, 300)
  const [debouncedGlobalSearch] = useDebounce(searchQuery, 300)

  // Process network logs asynchronously
  useEffect(() => {
    if (networkLogs.length === 0) {
      setLogEntries([])
      return
    }

    setIsProcessing(true)
    processLogsWorker(networkLogs, devices)
      .then((entries) => {
        setLogEntries(entries)
        setIsProcessing(false)
      })
      .catch((error) => {
        console.error('Error processing logs:', error)
        setIsProcessing(false)
      })
  }, [networkLogs, devices])

  // Optimized filtering with memoization
  const filteredLogs = useMemo(() => {
    if (logEntries.length === 0) return []

    let filtered = logEntries
    const searchTerm = (debouncedLocalSearch || debouncedGlobalSearch).toLowerCase().trim()

    // Apply filters in order of selectivity (most selective first)
    
    // Protocol filter (usually most selective)
    if (protocolFilters.size > 0 && protocolFilters.size < 10) {
      filtered = filtered.filter(log => protocolFilters.has(log.protocol))
    }

    // Traffic type filter
    if (trafficTypeFilters.size > 0 && trafficTypeFilters.size < 3) {
      filtered = filtered.filter(log => trafficTypeFilters.has(log.trafficType))
    }

    // Search filter (can be expensive, do last)
    if (searchTerm) {
      if (searchTerm.startsWith('ip:')) {
        const ipSearch = searchTerm.substring(3)
        filtered = filtered.filter(log => 
          log.srcIP.toLowerCase().includes(ipSearch) || 
          log.dstIP.toLowerCase().includes(ipSearch)
        )
      } else {
        // Use a more efficient search
        filtered = filtered.filter(log => {
          // Check most common search targets first
          return log.srcDevice.toLowerCase().includes(searchTerm) ||
                 log.dstDevice.toLowerCase().includes(searchTerm) ||
                 log.protocol.toLowerCase().includes(searchTerm) ||
                 log.srcIP.toLowerCase().includes(searchTerm) ||
                 log.dstIP.toLowerCase().includes(searchTerm) ||
                 log.trafficType.toLowerCase().includes(searchTerm)
        })
      }
    }

    return filtered
  }, [logEntries, debouncedLocalSearch, debouncedGlobalSearch, protocolFilters, trafficTypeFilters])


  // Virtual scrolling setup
  const virtualizer = useVirtualizer({
    count: filteredLogs.length,
    getScrollElement: () => scrollContainerRef.current,
    estimateSize: () => 28, // Fixed row height for better performance
    overscan: 50, // Render 50 extra items for smoother scrolling
  })

  // Reset virtualizer when filtered data changes significantly
  useEffect(() => {
    if (virtualizer && filteredLogs.length > 0) {
      // Reset scroll position when filters change
      virtualizer.scrollToIndex(0, { align: 'start' })
    }
  }, [debouncedLocalSearch, debouncedGlobalSearch, protocolFilters, trafficTypeFilters, filteredLogs.length, virtualizer])

  // Auto-scroll to bottom when new logs arrive
  useEffect(() => {
    if (autoScroll && filteredLogs.length > 0 && virtualizer) {
      virtualizer.scrollToIndex(filteredLogs.length - 1, { align: 'end' })
    }
  }, [filteredLogs.length, autoScroll, virtualizer])

  // Handle panel resize (optimized with throttling)
  const handleMouseDown = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    const startY = e.clientY
    const startHeight = panelHeight
    let animationFrame: number | null = null

    const handleMouseMove = (e: MouseEvent) => {
      if (animationFrame) {
        cancelAnimationFrame(animationFrame)
      }
      
      animationFrame = requestAnimationFrame(() => {
        const deltaY = startY - e.clientY
        const newHeight = Math.max(100, Math.min(window.innerHeight * 0.6, startHeight + deltaY))
        setPanelHeight(newHeight)
        onHeightChange?.(newHeight)
      })
    }

    const handleMouseUp = () => {
      document.removeEventListener('mousemove', handleMouseMove)
      document.removeEventListener('mouseup', handleMouseUp)
      if (animationFrame) {
        cancelAnimationFrame(animationFrame)
      }
      localStorage.setItem('logViewerHeight', panelHeight.toString())
    }

    document.addEventListener('mousemove', handleMouseMove)
    document.addEventListener('mouseup', handleMouseUp)
  }, [panelHeight, onHeightChange])

  // Load saved height preference
  useEffect(() => {
    const savedHeight = localStorage.getItem('logViewerHeight')
    if (savedHeight) {
      setPanelHeight(parseInt(savedHeight, 10))
    }
  }, [])

  // Notify parent of height changes
  useEffect(() => {
    onHeightChange?.(isExpanded ? panelHeight : 40)
  }, [isExpanded, panelHeight, onHeightChange])

  // Export logs (chunked for large datasets)
  const handleExport = useCallback(async () => {
    const chunkSize = 10000
    const chunks = []
    
    for (let i = 0; i < filteredLogs.length; i += chunkSize) {
      chunks.push(filteredLogs.slice(i, i + chunkSize))
    }
    
    const dataStr = JSON.stringify(chunks.flat(), null, 2)
    const dataUri = 'data:application/json;charset=utf-8,'+ encodeURIComponent(dataStr)
    const exportFileDefaultName = `network-logs-${new Date().toISOString()}.json`
    
    const linkElement = document.createElement('a')
    linkElement.setAttribute('href', dataUri)
    linkElement.setAttribute('download', exportFileDefaultName)
    linkElement.click()
  }, [filteredLogs])

  const getTrafficTypeColor = useCallback((type: string) => {
    switch(type) {
      case 'virtual': return 'text-blue-600 dark:text-blue-400'
      case 'subnet': return 'text-green-600 dark:text-green-400'
      case 'physical': return 'text-gray-600 dark:text-gray-400'
      default: return 'text-gray-600 dark:text-gray-400'
    }
  }, [])

  // Memoized row component with better error handling
  const LogRow = React.memo(({ virtualItem }: { virtualItem: { index: number; key: React.Key; size: number; start: number } }) => {
    const log = filteredLogs[virtualItem.index]
    
    // Debug logging for empty entries
    if (!log) {
      console.warn(`LogRow: No log found at index ${virtualItem.index}, filteredLogs.length: ${filteredLogs.length}`)
      return (
        <div
          key={virtualItem.key}
          data-index={virtualItem.index}
          style={{
            position: 'absolute',
            top: 0,
            left: 0,
            width: '100%',
            height: `${virtualItem.size}px`,
            transform: `translateY(${virtualItem.start}px)`,
          }}
          className="flex items-center px-4 py-1 border-b border-gray-200 dark:border-gray-700 text-xs text-red-500"
        >
          <div className="flex-1">Error: Log entry not found (index: {virtualItem.index})</div>
        </div>
      )
    }

    const isSelected = selectedLogId === log.id
    const trafficTypeColor = getTrafficTypeColor(log.trafficType)

    return (
      <div
        key={virtualItem.key}
        data-index={virtualItem.index}
        style={{
          position: 'absolute',
          top: 0,
          left: 0,
          width: '100%',
          height: `${virtualItem.size}px`,
          transform: `translateY(${virtualItem.start}px)`,
        }}
        className={`flex items-center px-4 py-1 border-b border-gray-200 dark:border-gray-700 hover:bg-gray-50 dark:hover:bg-gray-800 cursor-pointer text-xs ${
          isSelected ? 'bg-blue-50 dark:bg-blue-900/20' : ''
        }`}
        onClick={() => {
          setSelectedLogId(log.id)
          onSelectLog?.(log)
        }}
      >
        <div className="w-28 text-gray-500 dark:text-gray-400" title={formatUTCTimestamp(log.timestamp)}>
          {formatRelativeTime(log.timestamp)}
        </div>
        <div className="flex-1 truncate">
          <span className="font-medium">{log.srcDevice}</span>
          {log.srcPort && <span className="text-gray-500 dark:text-gray-400">:{log.srcPort}</span>}
        </div>
        <div className="w-8 text-center text-gray-400">→</div>
        <div className="flex-1 truncate">
          <span className="font-medium">{log.dstDevice}</span>
          {log.dstPort && <span className="text-gray-500 dark:text-gray-400">:{log.dstPort}</span>}
        </div>
        <div className="w-16 text-center">{log.protocol}</div>
        <div className={`w-16 text-center ${trafficTypeColor}`}>
          {log.trafficType}
        </div>
        <div className="w-20 text-right text-gray-600 dark:text-gray-400">
          {formatBytes(log.txBytes + log.rxBytes)}
        </div>
      </div>
    )
  })

  const displayCount = Math.min(filteredLogs.length, 100000) // Limit display for extreme cases

  return (
    <div 
      className={`fixed bottom-0 left-0 right-0 bg-white dark:bg-gray-800 border-t border-gray-200 dark:border-gray-700 shadow-lg transition-all duration-200 z-20`}
      style={{ height: isExpanded ? `${panelHeight}px` : '40px' }}
    >
      {/* Resize handle */}
      {isExpanded && (
        <div
          ref={resizeHandleRef}
          className="absolute top-0 left-0 right-0 h-1 bg-gray-300 dark:bg-gray-600 hover:bg-blue-500 cursor-ns-resize transition-colors"
          onMouseDown={handleMouseDown}
        />
      )}

      {/* Header */}
      <div className="flex items-center justify-between px-4 h-10 border-b border-gray-200 dark:border-gray-700">
        <div className="flex items-center space-x-4">
          <button
            onClick={() => setIsExpanded(!isExpanded)}
            className="flex items-center space-x-2 text-sm font-medium text-gray-700 dark:text-gray-300 hover:text-gray-900 dark:hover:text-gray-100"
          >
            {isExpanded ? <ChevronDown className="w-4 h-4" /> : <ChevronUp className="w-4 h-4" />}
            <span>Network Logs ({filteredLogs.length.toLocaleString()} entries)</span>
            {isProcessing && <Loader className="w-4 h-4 animate-spin" />}
            {(selectedNode || selectedLink) && (
              <div className="flex items-center gap-1 px-2 py-0.5 bg-blue-100 dark:bg-blue-900 text-blue-800 dark:text-blue-200 text-xs rounded-full">
                <span>{selectedNode ? `Filtered by ${selectedNode.displayName}` : 'Filtered by connection'}</span>
                {onClearSelection && (
                  <button 
                    onClick={(e) => {
                      e.stopPropagation()
                      onClearSelection()
                    }}
                    className="hover:bg-blue-200 dark:hover:bg-blue-800 rounded-full p-0.5"
                    title="Clear filter"
                  >
                    <X className="w-3 h-3" />
                  </button>
                )}
              </div>
            )}
          </button>

          {isExpanded && (
            <>
              <div className="relative">
                <Search className="absolute left-2 top-1.5 w-4 h-4 text-gray-400" />
                <input
                  type="text"
                  placeholder="Filter logs..."
                  value={localSearchQuery}
                  onChange={(e) => setLocalSearchQuery(e.target.value)}
                  className="pl-8 pr-2 py-1 text-xs border border-gray-300 dark:border-gray-600 rounded-md bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 placeholder-gray-500 dark:placeholder-gray-400 focus:outline-none focus:ring-1 focus:ring-blue-500"
                />
              </div>

              <button
                onClick={() => setAutoScroll(!autoScroll)}
                className={`p-1 rounded ${autoScroll ? 'bg-blue-100 dark:bg-blue-900 text-blue-600 dark:text-blue-400' : 'text-gray-600 dark:text-gray-400 hover:text-gray-900 dark:hover:text-gray-100'}`}
                title={autoScroll ? 'Auto-scroll enabled' : 'Auto-scroll disabled'}
              >
                <ArrowDown className="w-4 h-4" />
              </button>

              <button
                onClick={handleExport}
                className="p-1 text-gray-600 dark:text-gray-400 hover:text-gray-900 dark:hover:text-gray-100"
                title="Export logs"
                disabled={isProcessing}
              >
                <Download className="w-4 h-4" />
              </button>
            </>
          )}
        </div>

        {isExpanded && (
          <div className="flex items-center space-x-2 text-xs text-gray-500 dark:text-gray-400">
            {protocolFilters.size > 0 && (
              <span className="px-2 py-1 bg-gray-100 dark:bg-gray-700 rounded">
                {protocolFilters.size} protocols
              </span>
            )}
            {trafficTypeFilters.size > 0 && (
              <span className="px-2 py-1 bg-gray-100 dark:bg-gray-700 rounded">
                {trafficTypeFilters.size} types
              </span>
            )}
            {displayCount < filteredLogs.length && (
              <span className="px-2 py-1 bg-yellow-100 dark:bg-yellow-900 text-yellow-800 dark:text-yellow-200 rounded">
                Showing {displayCount.toLocaleString()} of {filteredLogs.length.toLocaleString()}
              </span>
            )}
          </div>
        )}
      </div>

      {/* Log content */}
      {isExpanded && (
        <div className="flex flex-col h-full">
          {/* Column headers */}
          <div className="flex items-center px-4 py-2 bg-gray-50 dark:bg-gray-900 border-b border-gray-200 dark:border-gray-700 text-xs font-medium text-gray-700 dark:text-gray-300">
            <div className="w-28">Time (UTC)</div>
            <div className="flex-1">Source</div>
            <div className="w-8"></div>
            <div className="flex-1">Destination</div>
            <div className="w-16 text-center">Protocol</div>
            <div className="w-16 text-center">Type</div>
            <div className="w-20 text-right">Bytes</div>
          </div>

          {/* Virtual scrolling container */}
          <div 
            ref={scrollContainerRef}
            className="flex-1 overflow-auto"
            style={{ height: panelHeight - 80 }}
          >
            {isProcessing ? (
              <div className="flex items-center justify-center h-full">
                <div className="text-center">
                  <Loader className="w-8 h-8 animate-spin text-blue-500 mx-auto mb-2" />
                  <p className="text-sm text-gray-500 dark:text-gray-400">Processing {networkLogs.length.toLocaleString()} logs...</p>
                </div>
              </div>
            ) : filteredLogs.length > 0 ? (
              <div
                style={{
                  height: `${virtualizer.getTotalSize()}px`,
                  width: '100%',
                  position: 'relative',
                }}
              >
                {virtualizer.getVirtualItems().length > 0 ? (
                  virtualizer.getVirtualItems().map((virtualItem) => (
                    <LogRow key={virtualItem.key} virtualItem={virtualItem} />
                  ))
                ) : (
                  <div className="flex items-center justify-center h-full text-gray-500 dark:text-gray-400">
                    <div className="text-center">
                      <p>Virtual items: {virtualizer.getVirtualItems().length}</p>
                      <p>Filtered logs: {filteredLogs.length}</p>
                      <p>Total size: {virtualizer.getTotalSize()}px</p>
                    </div>
                  </div>
                )}
              </div>
            ) : (
              <div className="flex items-center justify-center h-full text-gray-500 dark:text-gray-400">
                No logs to display
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

export default LogViewer