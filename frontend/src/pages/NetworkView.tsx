import React, { useEffect, useState, useMemo } from 'react'
import { RefreshCw, XCircle, ChevronLeft, Sidebar } from 'lucide-react'
import useSWR from 'swr'
import NetworkGraph from '@/components/NetworkGraph'
import LogViewer from '@/components/LogViewer'
import { fetcher } from '@/lib/api'




interface TailscaleDevice {
  id: string
  name: string
  addresses: string[]
  os: string
  tags?: string[]
  user?: string
  lastSeen: string
}

interface VIPServiceInfo {
  name: string
  addrs: string[]
}

interface StaticRecordInfo {
  addrs: string[]
  comment?: string
}

interface NetworkNode {
  id: string
  ip: string
  displayName: string
  nodeType: 'ip'
  totalBytes: number
  txBytes: number
  rxBytes: number
  connections: number
  tags: string[]
  user?: string
  isTailscale: boolean
  ips?: string[] // Track all IPs for merged devices
  incomingPorts: Set<number> // Ports this device receives traffic on
  outgoingPorts: Set<number> // Ports this device sends traffic to
  protocols: Set<string> // Track protocols (TCP, UDP, ICMP, etc.)
  x?: number
  y?: number
  fx?: number | null
  fy?: number | null
}

interface NetworkLink {
  source: string | NetworkNode
  target: string | NetworkNode
  originalSource: string
  originalTarget: string
  totalBytes: number
  txBytes: number
  rxBytes: number
  packets: number
  txPackets: number
  rxPackets: number
  protocol: string
  trafficType: 'virtual' | 'subnet' | 'physical'
}

// Helper function to extract IP from address (remove port if present)
const extractIP = (address: string): string => {
  // Handle IPv6 addresses
  if (address.startsWith('[') && address.includes(']:')) {
    return address.substring(1, address.indexOf(']:'))
  }
  // Handle IPv4 addresses with ports
  const colonIndex = address.lastIndexOf(':')
  if (colonIndex > 0 && !address.includes('::')) {
    return address.substring(0, colonIndex)
  }
  return address
}

// Helper function to get protocol name
const getProtocolName = (proto: number): string => {
  switch (proto) {
    case 1: return 'ICMP'
    case 6: return 'TCP'
    case 17: return 'UDP'
    case 255: return 'Reserved'
    default: return `Proto-${proto}`
  }
}

// Helper function to extract port from address:port string
const extractPort = (address: string): number | null => {
  // Handle IPv6 addresses like [fd7a:115c:a1e0::9001:b818]:62574
  if (address.startsWith('[') && address.includes(']:')) {
    const portStr = address.split(']:')[1]
    return portStr ? parseInt(portStr, 10) : null
  }
  
  // Handle IPv4 addresses like 100.72.184.20:53221
  if (address.includes(':')) {
    const portStr = address.split(':').pop()
    return portStr ? parseInt(portStr, 10) : null
  }
  
  return null
}

// Helper function to categorize IP addresses
const categorizeIP = (ip: string): string[] => {
  // DERP servers
  if (ip === '127.3.3.40') return ['derp']
  
  // IPv4 Tailscale addresses (100.64.0.0/10 CGNAT range used by Tailscale)
  if (ip.startsWith('100.')) return ['tailscale']
  
  // IPv6 Tailscale addresses (fd7a:115c:a1e0::/48 is Tailscale's IPv6 prefix)
  if (ip.startsWith('fd7a:115c:a1e0:')) return ['tailscale']
  
  // IPv4 private addresses (RFC 1918)
  if (ip.startsWith('192.168.') || ip.startsWith('10.') || 
      (ip.startsWith('172.') && parseInt(ip.split('.')[1]) >= 16 && parseInt(ip.split('.')[1]) <= 31)) {
    return ['private']
  }
  
  // IPv6 private/link-local addresses (RFC 4193, RFC 4291)
  // fc00::/7 (Unique Local Addresses), fe80::/10 (Link-Local)
  if (ip.startsWith('fe80:') || ip.startsWith('fc00:') || ip.startsWith('fd00:')) {
    // Note: fd00::/8 is part of fc00::/7, but excluding Tailscale's specific prefix
    if (!ip.startsWith('fd7a:115c:a1e0:')) {
      return ['private']
    }
  }
  
  // IPv6 addresses (contains colons) - treat as public if not private or Tailscale
  if (ip.includes(':')) {
    return ['public']
  }
  
  // Public IPv4 addresses
  return ['public']
}


// Helper function to normalize IPv6 addresses for comparison
const normalizeIPv6 = (ip: string): string => {
  // Remove brackets if present
  const cleaned = ip.replace(/^\[|\]$/g, '')
  
  // For IPv6, we need to handle different representations of the same address
  // This is a simplified normalization - in production you might want a more robust solution
  return cleaned.toLowerCase()
}

// Helper function to check if two IPs match, accounting for IPv6 variations
const ipMatches = (ip1: string, ip2: string): boolean => {
  // Direct match first
  if (ip1 === ip2) return true
  
  // For IPv6 addresses, normalize and compare
  if (ip1.includes(':') || ip2.includes(':')) {
    const normalized1 = normalizeIPv6(ip1)
    const normalized2 = normalizeIPv6(ip2)
    return normalized1 === normalized2
  }
  
  return false
}

// Helper function to get device name from IP
const getDeviceName = (
  ip: string, 
  devices: TailscaleDevice[] = [], 
  services: Record<string, VIPServiceInfo> = {}, 
  records: Record<string, StaticRecordInfo> = {}
): string => {
  // Check regular devices first
  const device = devices.find(d => 
    d.addresses.some(addr => ipMatches(ip, addr))
  )
  if (device) {
    // Extract just the device name part before the first dot
    // e.g. "kartiks-macbook-pro.keiretsu.ts.net" -> "kartiks-macbook-pro"
    const shortName = device.name.split('.')[0]
    return shortName || device.name
  }

  // Check VIP services
  for (const [serviceName, serviceInfo] of Object.entries(services)) {
    if (serviceInfo.addrs && Array.isArray(serviceInfo.addrs)) {
      if (serviceInfo.addrs.some((addr: string) => ipMatches(ip, addr))) {
        // Remove "svc:" prefix if present
        const cleanName = serviceName.startsWith('svc:') ? serviceName.substring(4) : serviceName
        return `📦 ${cleanName}` // Add icon to indicate it's a service
      }
    }
  }

  // Check static records
  for (const [recordName, recordInfo] of Object.entries(records)) {
    if (recordInfo.addrs && Array.isArray(recordInfo.addrs)) {
      if (recordInfo.addrs.some((addr: string) => ipMatches(ip, addr))) {
        return `📍 ${recordName}` // Add icon to indicate it's a static record
      }
    }
  }

  return ip
}

// Helper function to get device data from IP
const getDeviceData = (ip: string, devices: TailscaleDevice[] = []): TailscaleDevice | null => {
  return devices.find(d => 
    d.addresses.some(addr => ipMatches(ip, addr))
  ) || null
}


const NetworkView: React.FC = () => {
  const [selectedNode, setSelectedNode] = useState<NetworkNode | null>(null)
  const [selectedLink, setSelectedLink] = useState<NetworkLink | null>(null)
  const [searchQuery, setSearchQuery] = useState('')
  const [loading, setLoading] = useState(true)
  const [logViewerHeight, setLogViewerHeight] = useState(40) // Track log viewer height
  
  // Sidebar visibility states
  const [leftSidebarVisible, setLeftSidebarVisible] = useState(true)
  
  // Custom time range states
  const [useCustomTimeRange, setUseCustomTimeRange] = useState(false)
  const [startDate, setStartDate] = useState('')
  const [endDate, setEndDate] = useState('')
  
  // Filter states - Initialize with all options selected
  const [protocolFilters, setProtocolFilters] = useState<Set<string>>(new Set())
  const [trafficTypeFilters, setTrafficTypeFilters] = useState<Set<string>>(new Set())
  const [ipCategoryFilters, setIpCategoryFilters] = useState<Set<string>>(new Set())
  const [ipVersionFilter, setIpVersionFilter] = useState<string>('all') // IPv4/IPv6 filter
  const [timeRangeFilter, setTimeRangeFilter] = useState<string>('5m')
  const [minBandwidth, setMinBandwidth] = useState<number>(0)
  const [maxBandwidth, setMaxBandwidth] = useState<number>(1000000000) // 1GB
  const [nodeCountFilter, setNodeCountFilter] = useState<number>(0) // Minimum connections

  // Fetch Tailscale devices
  const { data: deviceData, error: deviceError, mutate: refetchDevices } = useSWR('/devices', fetcher, {
    errorRetryCount: 2,
    revalidateOnFocus: false,
    refreshInterval: 300000 // Refresh every 5 minutes
  })

  const devices = useMemo(() => {
    if (!Array.isArray(deviceData) || deviceData.length === 0) {
      return []
    }
    
    // Type guard to check if it's a TailscaleDevice array
    if ('name' in deviceData[0] && 'addresses' in deviceData[0]) {
      return deviceData as TailscaleDevice[]
    }
    
    return []
  }, [deviceData])

  // Fetch Tailscale services and records
  const { data: servicesRecordsData } = useSWR('/services-records', fetcher, {
    errorRetryCount: 2,
    revalidateOnFocus: false,
    refreshInterval: 300000 // Refresh every 5 minutes
  })

  const servicesRecords = useMemo(() => {
    if (!servicesRecordsData || typeof servicesRecordsData !== 'object') {
      return { services: {} as Record<string, VIPServiceInfo>, records: {} as Record<string, StaticRecordInfo> }
    }
    const data = servicesRecordsData as { 
      services?: Record<string, VIPServiceInfo>, 
      records?: Record<string, StaticRecordInfo> 
    }
    return {
      services: data.services || {},
      records: data.records || {}
    }
  }, [servicesRecordsData])

  // Validate custom date range - check that start is before end
  const isCustomDateRangeValid = useMemo(() => {
    if (!useCustomTimeRange || !startDate || !endDate) return true
    const start = new Date(startDate)
    const end = new Date(endDate)
    return !isNaN(start.getTime()) && !isNaN(end.getTime()) && start < end
  }, [useCustomTimeRange, startDate, endDate])

  // Fetch Tailscale network logs - refresh when time range changes
  const networkLogsApiUrl = useMemo(() => {
    const baseUrl = '/network-logs'
    const params = new URLSearchParams()

    if (useCustomTimeRange && startDate && endDate) {
      // Skip fetch if date range is invalid (prevents errors while typing)
      const start = new Date(startDate)
      const end = new Date(endDate)
      if (isNaN(start.getTime()) || isNaN(end.getTime()) || start >= end) {
        return null // SWR skips fetch when key is null
      }
      params.append('start', start.toISOString())
      params.append('end', end.toISOString())
    } else if (timeRangeFilter !== 'all') {
      // Convert time range to timestamp
      const now = new Date()
      let since: Date
      switch (timeRangeFilter) {
        case '1m':
          since = new Date(now.getTime() - 1 * 60 * 1000)
          break
        case '5m':
          since = new Date(now.getTime() - 5 * 60 * 1000)
          break
        case '15m':
          since = new Date(now.getTime() - 15 * 60 * 1000)
          break
        case '30m':
          since = new Date(now.getTime() - 30 * 60 * 1000)
          break
        case '1h':
          since = new Date(now.getTime() - 60 * 60 * 1000)
          break
        case '6h':
          since = new Date(now.getTime() - 6 * 60 * 60 * 1000)
          break
        case '24h':
          since = new Date(now.getTime() - 24 * 60 * 60 * 1000)
          break
        case '7d':
          since = new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000)
          break
        case '30d':
          since = new Date(now.getTime() - 30 * 24 * 60 * 60 * 1000)
          break
        default:
          since = new Date(now.getTime() - 5 * 60 * 1000) // Default to last 5m
      }
      params.append('start', since.toISOString())
      params.append('end', now.toISOString())
    }
    
    return `${baseUrl}?${params.toString()}`
  }, [timeRangeFilter, useCustomTimeRange, startDate, endDate])

  const { data: networkLogsData, error: networkLogsError, mutate: refetchNetworkLogs } = useSWR(networkLogsApiUrl, fetcher, {
    errorRetryCount: 2,
    revalidateOnFocus: false,
    refreshInterval: 300000 // Refresh every 5 minutes
  })

  const networkLogs = useMemo(() => {
    if (!networkLogsData) {
      return []
    }
    
    // Handle both array and object responses from API
    let logsArray: any[] = []
    
    if (Array.isArray(networkLogsData)) {
      logsArray = networkLogsData
    } else if (typeof networkLogsData === 'object' && 'logs' in networkLogsData) {
      logsArray = (networkLogsData as any).logs || []
    }
    
    // Validate that we have actual log entries
    const validLogs = logsArray.filter(log => {
      return log && typeof log === 'object' && ('logged' in log || 'Logged' in log)
    })
    
    return validLogs
  }, [networkLogsData])

  // Set default date range to show most recent data (last 5 minutes)
  useEffect(() => {
    const now = new Date()
    const fiveMinutesAgo = new Date(now.getTime() - 5 * 60 * 1000)
    
    // Format for datetime-local input (YYYY-MM-DDTHH:mm)
    const formatForInput = (date: Date) => {
      const year = date.getFullYear()
      const month = String(date.getMonth() + 1).padStart(2, '0')
      const day = String(date.getDate()).padStart(2, '0')
      const hours = String(date.getHours()).padStart(2, '0')
      const minutes = String(date.getMinutes()).padStart(2, '0')
      return `${year}-${month}-${day}T${hours}:${minutes}`
    }
    
    setStartDate(formatForInput(fiveMinutesAgo))
    setEndDate(formatForInput(now))
    setLoading(false)
  }, [])

  
  // Process network logs to create nodes and links
  const { nodes, links } = useMemo(() => {
    if (!networkLogs || networkLogs.length === 0) {
      return { nodes: [], links: [] }
    }

    const nodeMap = new Map<string, NetworkNode>()
    const linkMap = new Map<string, NetworkLink>()

    networkLogs.forEach((log) => {
      // Combine all traffic types into a single array
      const allTraffic = [
        ...(log.virtualTraffic || []).map((t: any) => ({ ...t, type: 'virtual' as const })),
        ...(log.subnetTraffic || []).map((t: any) => ({ ...t, type: 'subnet' as const })),
        ...(log.physicalTraffic || []).map((t: any) => ({ ...t, type: 'physical' as const, proto: t.proto || 0 }))
      ]

      allTraffic.forEach((traffic) => {
        const srcIP = extractIP(traffic.src)
        const dstIP = extractIP(traffic.dst)
        
        // Create or update source node (merge by device name for Tailscale devices)
        const srcDeviceName = getDeviceName(srcIP, devices, servicesRecords.services, servicesRecords.records)
        const srcNodeId = srcDeviceName !== srcIP ? srcDeviceName : srcIP
        if (!nodeMap.has(srcNodeId)) {
          const isTailscale = categorizeIP(srcIP).includes('tailscale')
          const deviceData = getDeviceData(srcIP, devices)
          
          // Combine IP-derived tags with device tags
          const ipTags = categorizeIP(srcIP)
          const deviceTags = deviceData?.tags || []
          const allTags = [...ipTags, ...deviceTags].filter((tag, index, arr) => arr.indexOf(tag) === index)
          
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
            protocols: new Set<string>()
          })
        } else {
          // Add this IP to the existing device node if not already present
          const existingNode = nodeMap.get(srcNodeId)!
          if (!existingNode.ips?.includes(srcIP)) {
            existingNode.ips = [...(existingNode.ips || []), srcIP]
            // Update tags to include IPv6 if this IP is IPv6
            const newTags = categorizeIP(srcIP)
            const deviceData = getDeviceData(srcIP, devices)
            const deviceTags = deviceData?.tags || []
            const combinedTags = [...newTags, ...deviceTags]
            
            combinedTags.forEach(tag => {
              if (!existingNode.tags.includes(tag)) {
                existingNode.tags.push(tag)
              }
            })
            
            // Update user if not already set
            if (!existingNode.user && deviceData?.user) {
              existingNode.user = deviceData.user
            }
          }
        }
        
        // Create or update destination node (merge by device name for Tailscale devices)
        const dstDeviceName = getDeviceName(dstIP, devices, servicesRecords.services, servicesRecords.records)
        const dstNodeId = dstDeviceName !== dstIP ? dstDeviceName : dstIP
        if (!nodeMap.has(dstNodeId)) {
          const isTailscale = categorizeIP(dstIP).includes('tailscale')
          const deviceData = getDeviceData(dstIP, devices)
          
          // Combine IP-derived tags with device tags
          const ipTags = categorizeIP(dstIP)
          const deviceTags = deviceData?.tags || []
          const allTags = [...ipTags, ...deviceTags].filter((tag, index, arr) => arr.indexOf(tag) === index)
          
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
            protocols: new Set<string>()
          })
        } else {
          // Add this IP to the existing device node if not already present
          const existingNode = nodeMap.get(dstNodeId)!
          if (!existingNode.ips?.includes(dstIP)) {
            existingNode.ips = [...(existingNode.ips || []), dstIP]
            // Update tags to include IPv6 if this IP is IPv6
            const newTags = categorizeIP(dstIP)
            const deviceData = getDeviceData(dstIP, devices)
            const deviceTags = deviceData?.tags || []
            const combinedTags = [...newTags, ...deviceTags]
            
            combinedTags.forEach(tag => {
              if (!existingNode.tags.includes(tag)) {
                existingNode.tags.push(tag)
              }
            })
            
            // Update user if not already set
            if (!existingNode.user && deviceData?.user) {
              existingNode.user = deviceData.user
            }
          }
        }

        // Update node traffic volumes
        const srcNode = nodeMap.get(srcNodeId)!
        const dstNode = nodeMap.get(dstNodeId)!
        
        // For source node: txBytes from this traffic entry
        srcNode.txBytes += traffic.txBytes || 0
        srcNode.rxBytes += traffic.rxBytes || 0
        srcNode.totalBytes = srcNode.txBytes + srcNode.rxBytes
        
        // For destination node: rxBytes from this traffic entry
        dstNode.txBytes += traffic.rxBytes || 0  // Reverse for destination
        dstNode.rxBytes += traffic.rxBytes || 0  // Reverse for destination
        dstNode.totalBytes = dstNode.txBytes + dstNode.rxBytes
        
        // Track port and protocol information
        const protocolName = getProtocolName(traffic.proto || 0)
        srcNode.protocols.add(protocolName)
        dstNode.protocols.add(protocolName)
        
        // Extract ports (only for TCP/UDP protocols)
        if (traffic.proto === 6 || traffic.proto === 17) { // TCP or UDP
          const srcPort = extractPort(traffic.src)
          const dstPort = extractPort(traffic.dst)
          
          if (srcPort !== null) {
            srcNode.outgoingPorts.add(srcPort) // Source uses this port to send
          }
          if (dstPort !== null) {
            dstNode.incomingPorts.add(dstPort) // Destination receives on this port
          }
        }

        // Create or update link (use device IDs instead of IPs)
        const linkKey = `${srcNodeId}->${dstNodeId}`
        
        if (!linkMap.has(linkKey)) {
          linkMap.set(linkKey, {
            source: srcNodeId,
            target: dstNodeId,
            originalSource: srcIP,
            originalTarget: dstIP,
            totalBytes: 0,
            txBytes: 0,
            rxBytes: 0,
            packets: 0,
            txPackets: 0,
            rxPackets: 0,
            protocol: getProtocolName(traffic.proto || 0),
            trafficType: traffic.type
          })
        }

        const link = linkMap.get(linkKey)!
        link.txBytes += traffic.txBytes || 0
        link.rxBytes += traffic.rxBytes || 0
        link.totalBytes = link.txBytes + link.rxBytes
        link.txPackets += traffic.txPkts || 0
        link.rxPackets += traffic.rxPkts || 0
        link.packets = link.txPackets + link.rxPackets
      })
    })

    // Update connection counts
    linkMap.forEach(link => {
      const srcNode = nodeMap.get(typeof link.source === 'string' ? link.source : link.source.id)
      const dstNode = nodeMap.get(typeof link.target === 'string' ? link.target : link.target.id)
      if (srcNode) srcNode.connections++
      if (dstNode) dstNode.connections++
    })

    return {
      nodes: Array.from(nodeMap.values()),
      links: Array.from(linkMap.values())
    }
  }, [networkLogs, devices, servicesRecords])

  // Filter network logs based on selected node/link
  const filteredNetworkLogs = useMemo(() => {
    if (!selectedNode && !selectedLink) {
      return networkLogs
    }

    return networkLogs.filter(log => {
      const allTraffic = [
        ...(log.virtualTraffic || []).map((t: any) => ({ ...t, type: 'virtual' })),
        ...(log.subnetTraffic || []).map((t: any) => ({ ...t, type: 'subnet' })),
        ...(log.physicalTraffic || []).map((t: any) => ({ ...t, type: 'physical' }))
      ]

      return allTraffic.some(traffic => {
        const srcIP = extractIP(traffic.src)
        const dstIP = extractIP(traffic.dst)

        if (selectedNode) {
          // Check if this traffic involves the selected node
          const nodeIPs = selectedNode.ips || [selectedNode.ip]
          return nodeIPs.some(ip => ip === srcIP || ip === dstIP)
        }

        if (selectedLink) {
          // Check if this traffic matches the selected link
          const linkSourceIP = typeof selectedLink.source === 'string' ? 
            selectedLink.originalSource : 
            selectedLink.originalSource
          const linkTargetIP = typeof selectedLink.target === 'string' ? 
            selectedLink.originalTarget : 
            selectedLink.originalTarget
          
          return (srcIP === linkSourceIP && dstIP === linkTargetIP) ||
                 (srcIP === linkTargetIP && dstIP === linkSourceIP)
        }

        return false
      })
    })
  }, [networkLogs, selectedNode, selectedLink])

  // Apply filters
  const filteredData = useMemo(() => {
    let filteredNodes = nodes.filter(node => {
      // Enhanced search filter with tag:, user@, and ip: support
      if (searchQuery) {
        const query = searchQuery.toLowerCase().trim()
        
        // Check for tag: search
        if (query.startsWith('tag:')) {
          const tagSearch = query.substring(4)
          const nodeTagsLower = node.tags.map(tag => tag.toLowerCase().replace('tag:', ''))
          if (!nodeTagsLower.some(tag => tag.includes(tagSearch))) {
            return false
          }
        }
        // Check for ip: search
        else if (query.startsWith('ip:')) {
          const ipSearch = query.substring(3)
          const allIPs = node.ips || [node.ip]
          if (!allIPs.some(ip => ip.toLowerCase().includes(ipSearch))) {
            return false
          }
        }
        // Check for user@ search  
        else if (query.includes('@') && query.includes('user')) {
          const userSearch = query.replace('user@', '').replace('user:', '')
          if (!node.user || !node.user.toLowerCase().includes(userSearch)) {
            return false
          }
        }
        // Regular search (IP, display name, user, or tags)
        else {
          const allIPs = node.ips || [node.ip]
          const matchesIP = allIPs.some(ip => ip.toLowerCase().includes(query))
          const matchesName = node.displayName.toLowerCase().includes(query)
          const matchesUser = node.user?.toLowerCase().includes(query) || false
          const matchesTags = node.tags.some(tag => 
            tag.toLowerCase().replace('tag:', '').includes(query)
          )
          
          if (!matchesIP && !matchesName && !matchesUser && !matchesTags) {
            return false
          }
        }
      }

      // IP category filter (only for basic IP types, not device tags)
      if (ipCategoryFilters.size > 0) {
        const ipTypes = ['tailscale', 'private', 'public', 'derp']
        const nodeIpTypes = node.tags.filter(tag => ipTypes.includes(tag))
        if (!nodeIpTypes.some(tag => ipCategoryFilters.has(tag))) {
          return false
        }
      }

      // IP version filter (IPv4/IPv6)
      if (ipVersionFilter !== 'all') {
        const allIPs = node.ips || [node.ip]
        const hasIPv6 = allIPs.some(ip => ip.includes(':'))
        const hasIPv4 = allIPs.some(ip => !ip.includes(':'))
        
        if (ipVersionFilter === 'ipv4' && !hasIPv4) return false
        if (ipVersionFilter === 'ipv6' && !hasIPv6) return false
      }

      // Bandwidth filter
      if (node.totalBytes < minBandwidth || node.totalBytes > maxBandwidth) {
        return false
      }

      // Connection count filter
      if (node.connections < nodeCountFilter) {
        return false
      }

        return true
      })

    let filteredLinks = links.filter(link => {
      // Protocol filter
      if (protocolFilters.size > 0 && !protocolFilters.has(link.protocol)) {
        return false
      }

      // Traffic type filter
      if (trafficTypeFilters.size > 0 && !trafficTypeFilters.has(link.trafficType)) {
        return false
      }

      // Bandwidth filter for links
      if (link.totalBytes < minBandwidth || link.totalBytes > maxBandwidth) {
        return false
      }

      // Only include links where both nodes are in filtered nodes
      const sourceId = typeof link.source === 'string' ? link.source : link.source.id
      const targetId = typeof link.target === 'string' ? link.target : link.target.id
      
      return filteredNodes.some(n => n.id === sourceId) && 
             filteredNodes.some(n => n.id === targetId)
    })
    
    // Remove nodes that have no connections after link filtering
    const connectedNodeIds = new Set<string>()
    filteredLinks.forEach(link => {
      const sourceId = typeof link.source === 'string' ? link.source : link.source.id
      const targetId = typeof link.target === 'string' ? link.target : link.target.id
      connectedNodeIds.add(sourceId)
      connectedNodeIds.add(targetId)
    })
    
    filteredNodes = filteredNodes.filter(node => connectedNodeIds.has(node.id))
    
    return { nodes: filteredNodes, links: filteredLinks }
  }, [nodes, links, searchQuery, protocolFilters, trafficTypeFilters, ipCategoryFilters, ipVersionFilter, minBandwidth, maxBandwidth, nodeCountFilter])

  // Handle node click
  const handleNodeClick = (node: NetworkNode) => {
    setSelectedNode(node)
    setSelectedLink(null)
  }

  // Handle link click
  const handleLinkClick = (link: NetworkLink) => {
    setSelectedLink(link)
    setSelectedNode(null)
  }

  // Handle background click
  const handleBackgroundClick = () => {
    setSelectedNode(null)
    setSelectedLink(null)
  }



  const formatBytes = (bytes: number) => {
    if (bytes === 0) return '0 B'
    const k = 1024
    const sizes = ['B', 'KB', 'MB', 'GB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i]
  }

  // Get unique values for filters with common defaults
  const baseProtocols = ['TCP', 'UDP', 'ICMP', 'Proto-255', 'Proto-0'] // Added Proto-0 to base protocols
  const baseTrafficTypes = ['virtual', 'subnet', 'physical'] // All possible traffic types
  const baseIpCategories = ['tailscale', 'private', 'public', 'derp'] // Common IP categories

  const dataProtocols = Array.from(new Set(links.map(l => l.protocol)))
  const dataTrafficTypes = Array.from(new Set(links.map(l => l.trafficType)))
  // Only include basic IP types, not device tags
  const dataIpCategories = Array.from(new Set(
    nodes.flatMap(n => n.tags).filter(tag => 
      ['tailscale', 'private', 'public', 'derp'].includes(tag)
    )
  ))

  const uniqueProtocols = Array.from(new Set([...baseProtocols, ...dataProtocols]))
  const uniqueTrafficTypes = Array.from(new Set([...baseTrafficTypes, ...dataTrafficTypes]))
  const uniqueIpCategories = Array.from(new Set([...baseIpCategories, ...dataIpCategories]))

  // Initialize filters with all options selected when data loads (only once)
  const [filtersInitialized, setFiltersInitialized] = useState(false)
  
  useEffect(() => {
    if (filtersInitialized) {
      return // Already initialized
    }
    
    if (uniqueProtocols.length > 0 && uniqueTrafficTypes.length > 0 && uniqueIpCategories.length > 0) {
      setProtocolFilters(new Set(uniqueProtocols.filter(proto => proto !== 'Proto-99'))) // Exclude Proto-99 from default selection
      setTrafficTypeFilters(new Set(uniqueTrafficTypes))
      setIpCategoryFilters(new Set(uniqueIpCategories.filter(cat => cat !== 'derp'))) // Hide derp by default
      setFiltersInitialized(true)
    }
  }, [uniqueProtocols, uniqueTrafficTypes, uniqueIpCategories, filtersInitialized])

    // Calculate bandwidth range for slider
  const bandwidthRange = useMemo(() => {
    if (nodes.length === 0) return { min: 0, max: 1000000000 }
    const allBytes = nodes.map(n => n.totalBytes).filter(b => b > 0)
    if (allBytes.length === 0) return { min: 0, max: 1000000000 }
    const min = Math.min(...allBytes)
    const max = Math.max(...allBytes)
    return { min, max }
  }, [nodes])

  // Update max bandwidth when data changes
  useEffect(() => {
    setMaxBandwidth(bandwidthRange.max)
  }, [bandwidthRange.max])

  if (loading) {
    return (
      <div className="h-screen bg-gray-50 dark:bg-gray-900 flex items-center justify-center">
        <div className="text-center">
          <RefreshCw className="w-8 h-8 animate-spin text-blue-500 mx-auto mb-4" />
          <p className="text-gray-600 dark:text-gray-400">Loading network data...</p>
        </div>
      </div>
    )
  }

  // Show error states
  if (deviceError || networkLogsError) {
    return (
      <div className="h-screen bg-gray-50 dark:bg-gray-900 flex items-center justify-center">
        <div className="text-center max-w-lg mx-auto p-6">
          <XCircle className="w-12 h-12 text-red-500 mx-auto mb-4" />
          <h2 className="text-xl font-semibold text-gray-900 dark:text-gray-100 mb-2">
            Unable to Load Network Data
          </h2>
          <div className="text-gray-600 dark:text-gray-400 mb-6 space-y-2">
            {deviceError && (
              <p className="text-sm bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg p-3">
                <strong>Device Error:</strong> {deviceError.message}
              </p>
            )}
            {networkLogsError && (
              <p className="text-sm bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg p-3">
                <strong>Network Logs Error:</strong> {networkLogsError.message}
              </p>
            )}
            <p className="text-sm text-gray-500 dark:text-gray-400 mt-4">
              This usually means your API credentials are not configured or invalid. 
              Please check your settings and ensure your API key has the necessary permissions.
            </p>
          </div>
          <div className="space-x-3">
            <button
              onClick={async () => {
                setLoading(true)
                try {
                  await Promise.all([
                    refetchNetworkLogs(),
                    refetchDevices()
                  ])
                } finally {
                  setLoading(false)
                }
              }}
              className="px-4 py-2 bg-blue-600 dark:bg-blue-700 text-white rounded-md hover:bg-blue-700 dark:hover:bg-blue-600 focus:ring-2 focus:ring-blue-500 transition-colors"
            >
              Try Again
            </button>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="h-screen bg-gray-50 dark:bg-gray-900 flex flex-col" style={{ position: 'relative' }}>
      <div className="absolute inset-0" style={{ bottom: `${logViewerHeight}px` }}>
        <div className="flex h-full overflow-hidden">

        {/* Filters Sidebar */}
        <div className={`bg-white dark:bg-gray-800 border-r border-gray-200 dark:border-gray-700 overflow-y-auto flex-shrink-0 transition-all duration-300 ${
          leftSidebarVisible ? 'w-80' : 'w-0'
        }`} style={{ height: '100%' }}>
                      <div className={`p-6 ${leftSidebarVisible ? 'block' : 'hidden'}`}>
            <div className="flex items-center justify-between mb-4">
              <h3 className="text-lg font-semibold text-gray-900 dark:text-gray-100">Filters</h3>
              <button
                onClick={() => setLeftSidebarVisible(false)}
                className="p-1 text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200 transition-colors"
                title="Hide filters"
              >
                <ChevronLeft className="w-4 h-4" />
              </button>
            </div>
            
            {/* Search */}
            <div className="mb-6">
              <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">Search</label>
              <input
                type="text"
                placeholder="Search devices, tag:k8s, ip:100.88, user@github..."
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                className="w-full px-4 py-2 border border-gray-300 dark:border-gray-600 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-transparent bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 placeholder-gray-500 dark:placeholder-gray-400"
              />
              <div className="mt-2 text-xs text-gray-500 dark:text-gray-400">
                <div>• <code className="bg-gray-100 dark:bg-gray-700 px-1 rounded">tag:k8s</code> - Find devices with specific tags</div>
                <div>• <code className="bg-gray-100 dark:bg-gray-700 px-1 rounded">ip:100.88</code> - Find devices by IP address</div>
                <div>• <code className="bg-gray-100 dark:bg-gray-700 px-1 rounded">user@github</code> - Find devices by user</div>
                <div>• Regular text searches device names, IPs, and tags</div>
              </div>
          </div>
          
            {/* Time Range */}
            <div className="mb-6">
              <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">Time Range</label>
              
              {/* Toggle for custom time range */}
              <div className="flex items-center mb-3">
                <input
                  type="checkbox"
                  id="customTimeRange"
                  checked={useCustomTimeRange}
                  onChange={(e) => setUseCustomTimeRange(e.target.checked)}
                  className="rounded border-gray-300 dark:border-gray-600 text-blue-600 focus:ring-blue-500 bg-white dark:bg-gray-700"
                />
                <label htmlFor="customTimeRange" className="ml-2 text-sm text-gray-700 dark:text-gray-300">
                  Custom Date Range
                </label>
              </div>

              {useCustomTimeRange ? (
                <div className="space-y-3">
                  <div>
                    <label className="block text-xs text-gray-500 dark:text-gray-400 mb-1">Start Date & Time</label>
                    <input
                      type="datetime-local"
                      value={startDate}
                      onChange={(e) => setStartDate(e.target.value)}
                      className={`w-full px-3 py-2 border rounded-md focus:ring-2 focus:ring-blue-500 bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 ${
                        !isCustomDateRangeValid ? 'border-yellow-400 dark:border-yellow-500' : 'border-gray-300 dark:border-gray-600'
                      }`}
                    />
                  </div>
                  <div>
                    <label className="block text-xs text-gray-500 dark:text-gray-400 mb-1">End Date & Time</label>
                    <input
                      type="datetime-local"
                      value={endDate}
                      onChange={(e) => setEndDate(e.target.value)}
                      className={`w-full px-3 py-2 border rounded-md focus:ring-2 focus:ring-blue-500 bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 ${
                        !isCustomDateRangeValid ? 'border-yellow-400 dark:border-yellow-500' : 'border-gray-300 dark:border-gray-600'
                      }`}
                    />
                  </div>
                  {!isCustomDateRangeValid && startDate && endDate && (
                    <p className="text-xs text-yellow-600 dark:text-yellow-400">
                      Start date must be before end date
                    </p>
                  )}
                </div>
              ) : (
                <div className="flex space-x-2">
                  <select
                    value={timeRangeFilter}
                    onChange={(e) => setTimeRangeFilter(e.target.value)}
                    className="flex-1 px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-md focus:ring-2 focus:ring-blue-500 bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100"
                  >
                    <option value="1m">Last 1 Minute</option>
                    <option value="5m">Last 5 Minutes</option>
                    <option value="15m">Last 15 Minutes</option>
                    <option value="30m">Last 30 Minutes</option>
                    <option value="1h">Last Hour</option>
                    <option value="6h">Last 6 Hours</option>
                    <option value="24h">Last 24 Hours</option>
                    <option value="7d">Last 7 Days</option>
                    <option value="30d">Last 30 Days</option>
                  </select>
                </div>
              )}
            
            <button
                onClick={async () => {
                  setLoading(true)
                  try {
                    await Promise.all([
                      refetchNetworkLogs(),
                      refetchDevices()
                    ])
                  } finally {
                    setLoading(false)
                  }
                }}
                className="mt-2 w-full flex items-center justify-center px-3 py-2 bg-blue-600 dark:bg-blue-700 text-white rounded-md hover:bg-blue-700 dark:hover:bg-blue-600 focus:ring-2 focus:ring-blue-500 transition-colors"
                title="Refresh data"
              >
                <RefreshCw className="w-4 h-4 mr-2" />
                Refresh Data
            </button>
        </div>

            {/* Bandwidth Filter */}
            <div className="mb-6">
              <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">
                Traffic Volume: {formatBytes(minBandwidth)} - {formatBytes(maxBandwidth)}
              </label>
              <div className="space-y-3">
                <div>
                  <label className="text-xs text-gray-500 dark:text-gray-400 mb-1 block">Minimum</label>
                  <input
                    type="range"
                    min={bandwidthRange.min}
                    max={bandwidthRange.max}
                    value={minBandwidth}
                    onChange={(e) => setMinBandwidth(Number(e.target.value))}
                    className="w-full h-2 bg-gray-200 dark:bg-gray-600 rounded-lg appearance-none cursor-pointer accent-blue-600"
                  />
                  <div className="flex justify-between text-xs text-gray-500 dark:text-gray-400 mt-1">
                    <span>{formatBytes(bandwidthRange.min)}</span>
                    <span>{formatBytes(bandwidthRange.max)}</span>
      </div>
                </div>
                <div>
                  <label className="text-xs text-gray-500 dark:text-gray-400 mb-1 block">Maximum</label>
                  <input
                    type="range"
                    min={bandwidthRange.min}
                    max={bandwidthRange.max}
                    value={maxBandwidth}
                    onChange={(e) => setMaxBandwidth(Number(e.target.value))}
                    className="w-full h-2 bg-gray-200 dark:bg-gray-600 rounded-lg appearance-none cursor-pointer accent-blue-600"
                  />
              </div>
            </div>
            </div>

            {/* Connection Count Filter */}
            <div className="mb-6">
              <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">
                Minimum Connections: {nodeCountFilter}
              </label>
              <input
                type="range"
                min={0}
                max={Math.max(10, ...nodes.map(n => n.connections))}
                value={nodeCountFilter}
                onChange={(e) => setNodeCountFilter(Number(e.target.value))}
                className="w-full h-2 bg-gray-200 dark:bg-gray-600 rounded-lg appearance-none cursor-pointer accent-blue-600"
              />
              <div className="flex justify-between text-xs text-gray-500 dark:text-gray-400 mt-1">
                <span>0</span>
                <span>{Math.max(10, ...nodes.map(n => n.connections))}</span>
                </div>
              </div>

            {/* Protocol Filter */}
            <div className="mb-6">
              <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">Protocol</label>
              <div className="space-y-2 max-h-32 overflow-y-auto">
                {uniqueProtocols.map(protocol => (
                  <label key={protocol} className="flex items-center">
                    <input
                      type="checkbox"
                      checked={protocolFilters.has(protocol)}
                      onChange={(e) => {
                        const newFilters = new Set(protocolFilters)
                        if (e.target.checked) {
                          newFilters.add(protocol)
                        } else {
                          newFilters.delete(protocol)
                        }
                        setProtocolFilters(newFilters)
                      }}
                      className="rounded border-gray-300 dark:border-gray-600 text-blue-600 focus:ring-blue-500 bg-white dark:bg-gray-700"
                    />
                    <span className="ml-2 text-sm text-gray-700 dark:text-gray-300">{protocol}</span>
                  </label>
                ))}
            </div>
              <div className="flex items-center justify-between mt-2">
                <button
                  onClick={() => setProtocolFilters(new Set(uniqueProtocols))}
                  className="text-xs text-green-600 dark:text-green-400 hover:text-green-800 dark:hover:text-green-300 transition-colors"
                >
                  Select all
                </button>
                {protocolFilters.size > 0 && (
                  <button
                    onClick={() => setProtocolFilters(new Set())}
                    className="text-xs text-blue-600 dark:text-blue-400 hover:text-blue-800 dark:hover:text-blue-300 transition-colors"
                  >
                    Clear all ({protocolFilters.size})
                  </button>
                )}
              </div>
            </div>

            {/* Traffic Type Filter */}
            <div className="mb-6">
              <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">Traffic Type</label>
              <div className="space-y-2">
                {uniqueTrafficTypes.map(type => (
                  <label key={type} className="flex items-center">
                    <input
                      type="checkbox"
                      checked={trafficTypeFilters.has(type)}
                      onChange={(e) => {
                        const newFilters = new Set(trafficTypeFilters)
                        if (e.target.checked) {
                          newFilters.add(type)
                        } else {
                          newFilters.delete(type)
                        }
                        setTrafficTypeFilters(newFilters)
                      }}
                      className="rounded border-gray-300 dark:border-gray-600 text-blue-600 focus:ring-blue-500 bg-white dark:bg-gray-700"
                    />
                    <span className="ml-2 text-sm text-gray-700 dark:text-gray-300">{type}</span>
                </label>
                ))}
              </div>
              <div className="flex items-center justify-between mt-2">
                <button
                  onClick={() => setTrafficTypeFilters(new Set(uniqueTrafficTypes))}
                  className="text-xs text-green-600 dark:text-green-400 hover:text-green-800 dark:hover:text-green-300 transition-colors"
                >
                  Select all
                </button>
                {trafficTypeFilters.size > 0 && (
                  <button
                    onClick={() => setTrafficTypeFilters(new Set())}
                    className="text-xs text-blue-600 dark:text-blue-400 hover:text-blue-800 dark:hover:text-blue-300 transition-colors"
                  >
                    Clear all ({trafficTypeFilters.size})
                  </button>
                )}
              </div>
              </div>

            {/* IP Category Filter */}
            <div className="mb-6">
              <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">IP Category</label>
              <div className="space-y-2">
                {uniqueIpCategories.map(category => (
                  <label key={category} className="flex items-center">
                    <input
                      type="checkbox"
                      checked={ipCategoryFilters.has(category)}
                      onChange={(e) => {
                        const newFilters = new Set(ipCategoryFilters)
                        if (e.target.checked) {
                          newFilters.add(category)
                        } else {
                          newFilters.delete(category)
                        }
                        setIpCategoryFilters(newFilters)
                      }}
                      className="rounded border-gray-300 dark:border-gray-600 text-blue-600 focus:ring-blue-500 bg-white dark:bg-gray-700"
                    />
                    <span className="ml-2 text-sm text-gray-700 dark:text-gray-300">{category}</span>
                  </label>
                ))}
              </div>
              <div className="flex items-center justify-between mt-2">
                <button
                  onClick={() => setIpCategoryFilters(new Set(uniqueIpCategories))}
                  className="text-xs text-green-600 dark:text-green-400 hover:text-green-800 dark:hover:text-green-300 transition-colors"
                >
                  Select all
                </button>
                {ipCategoryFilters.size > 0 && (
                  <button
                    onClick={() => setIpCategoryFilters(new Set())}
                    className="text-xs text-blue-600 dark:text-blue-400 hover:text-blue-800 dark:hover:text-blue-300 transition-colors"
                  >
                    Clear all ({ipCategoryFilters.size})
                  </button>
                )}
              </div>
              </div>

            {/* IP Version Filter */}
            <div className="mb-6">
              <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">IP Version</label>
                <select
                value={ipVersionFilter}
                onChange={(e) => setIpVersionFilter(e.target.value)}
                className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-md focus:ring-2 focus:ring-blue-500 bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100"
              >
                <option value="all">IPv4 & IPv6</option>
                <option value="ipv4">IPv4 Only</option>
                <option value="ipv6">IPv6 Only</option>
                </select>
              </div>

            {/* Filter Summary */}
            <div className="bg-gray-50 dark:bg-gray-700 p-3 rounded-lg border border-gray-200 dark:border-gray-600">
              <h4 className="text-sm font-semibold text-gray-900 dark:text-gray-100 mb-2">Filter Summary</h4>
              <div className="space-y-1 text-xs text-gray-600 dark:text-gray-300">
                <div>Showing {filteredData.nodes.length} of {nodes.length} devices</div>
                <div>Showing {filteredData.links.length} of {links.length} flows</div>
                <div>Time: {useCustomTimeRange && startDate && endDate ? 
                  `${new Date(startDate).toLocaleDateString()} - ${new Date(endDate).toLocaleDateString()}` :
                  timeRangeFilter}</div>
                <div>Bandwidth: {formatBytes(minBandwidth)} - {formatBytes(maxBandwidth)}</div>
                {nodeCountFilter > 0 && <div>Min connections: {nodeCountFilter}</div>}
              </div>
              </div>
            </div>
          </div>

        {/* Main Network View */}
        <div className="flex-1 relative" style={{ height: '100%' }}>
          <NetworkGraph
            nodes={filteredData.nodes}
            links={filteredData.links}
            devices={devices}
            onNodeClick={handleNodeClick}
            onLinkClick={handleLinkClick}
            onBackgroundClick={handleBackgroundClick}
            selectedNode={selectedNode}
            selectedLink={selectedLink}
          />
          
          {/* Sidebar Toggle Button - Only show when sidebar is closed */}
          {!leftSidebarVisible && (
            <button
              onClick={() => setLeftSidebarVisible(true)}
              className="absolute left-4 top-4 z-10 p-2 bg-white dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded-lg shadow-lg hover:bg-gray-50 dark:hover:bg-gray-700 transition-colors"
              title="Show filters"
            >
              <Sidebar className="w-5 h-5" />
            </button>
          )}
          
          {/* Unified Network Overview Panel - Compact and informative */}
          <div className="absolute right-4 top-4 z-10">
            <div className="bg-white/95 dark:bg-gray-800/95 backdrop-blur-sm border border-gray-200 dark:border-gray-700 rounded-xl shadow-lg overflow-hidden">
              {/* Compact Header with Real-time Status */}
              <div className="px-4 py-2.5 bg-gradient-to-r from-blue-50 to-indigo-50 dark:from-gray-800 dark:to-gray-750 border-b border-gray-200 dark:border-gray-700">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <div className="w-2 h-2 bg-green-500 rounded-full animate-pulse"></div>
                    <h3 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Network Overview</h3>
                  </div>
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    {useCustomTimeRange && startDate && endDate ? 
                      `${new Date(startDate).toLocaleTimeString()} - ${new Date(endDate).toLocaleTimeString()}` : 
                      `Last ${timeRangeFilter}`
                    }
                  </span>
                </div>
              </div>
              
              {/* Main Stats Grid - Compact 2x2 Layout */}
              <div className="p-3">
                <div className="grid grid-cols-2 gap-3">
                  {/* Active Nodes */}
                  <div className="bg-gray-50 dark:bg-gray-900/50 rounded-lg p-2.5">
                    <div className="flex items-center justify-between mb-1">
                      <span className="text-xs text-gray-500 dark:text-gray-400">Active Nodes</span>
                      <svg className="w-3.5 h-3.5 text-blue-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M13 10V3L4 14h7v7l9-11h-7z" />
                      </svg>
                    </div>
                    <div className="text-lg font-bold text-gray-900 dark:text-gray-100">
                      {filteredData.nodes.length}
                      <span className="text-xs text-gray-500 dark:text-gray-400 font-normal ml-1">/ {nodes.length}</span>
                    </div>
                  </div>
                  
                  {/* Active Connections */}
                  <div className="bg-gray-50 dark:bg-gray-900/50 rounded-lg p-2.5">
                    <div className="flex items-center justify-between mb-1">
                      <span className="text-xs text-gray-500 dark:text-gray-400">Connections</span>
                      <svg className="w-3.5 h-3.5 text-green-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M8 12h.01M12 12h.01M16 12h.01M21 12c0 4.418-4.03 8-9 8a9.863 9.863 0 01-4.255-.949L3 20l1.395-3.72C3.512 15.042 3 13.574 3 12c0-4.418 4.03-8 9-8s9 3.582 9 8z" />
                      </svg>
                    </div>
                    <div className="text-lg font-bold text-gray-900 dark:text-gray-100">
                      {filteredData.links.length}
                      <span className="text-xs text-gray-500 dark:text-gray-400 font-normal ml-1">flows</span>
                    </div>
                  </div>
                </div>
                
                {/* Traffic Volume Bar */}
                <div className="mt-3 bg-gray-50 dark:bg-gray-900/50 rounded-lg p-2.5">
                  <div className="flex items-center justify-between mb-1.5">
                    <span className="text-xs text-gray-500 dark:text-gray-400">Total Traffic</span>
                    <span className="text-sm font-semibold text-gray-900 dark:text-gray-100">
                      {formatBytes(filteredData.nodes.reduce((sum, node) => sum + node.totalBytes, 0))}
                    </span>
                  </div>
                  
                  {/* Traffic Type Distribution Bar */}
                  <div className="h-2 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
                    <div className="h-full flex">
                      {(() => {
                        const virtualBytes = filteredData.links
                          .filter(link => (link as any).trafficType === 'virtual')
                          .reduce((sum, link) => sum + (link as any).totalBytes, 0);
                        const subnetBytes = filteredData.links
                          .filter(link => (link as any).trafficType === 'subnet')
                          .reduce((sum, link) => sum + (link as any).totalBytes, 0);
                        const physicalBytes = filteredData.links
                          .filter(link => (link as any).trafficType === 'physical')
                          .reduce((sum, link) => sum + (link as any).totalBytes, 0);
                        const totalBytes = virtualBytes + subnetBytes + physicalBytes;
                        
                        if (totalBytes === 0) return <div className="w-full h-full bg-gray-300 dark:bg-gray-600"></div>;
                        
                        const virtualPercent = totalBytes > 0 ? (virtualBytes / totalBytes) * 100 : 0;
                        const subnetPercent = totalBytes > 0 ? (subnetBytes / totalBytes) * 100 : 0;
                        const physicalPercent = totalBytes > 0 ? (physicalBytes / totalBytes) * 100 : 0;
                        
                        return (
                          <>
                            {virtualPercent > 0 && (
                              <div 
                                className="h-full bg-blue-500 transition-all duration-300" 
                                style={{ width: `${virtualPercent}%` }}
                                title={`Virtual: ${formatBytes(virtualBytes)} (${virtualPercent.toFixed(1)}%)`}
                              />
                            )}
                            {subnetPercent > 0 && (
                              <div 
                                className="h-full bg-green-500 transition-all duration-300" 
                                style={{ width: `${subnetPercent}%` }}
                                title={`Subnet: ${formatBytes(subnetBytes)} (${subnetPercent.toFixed(1)}%)`}
                              />
                            )}
                            {physicalPercent > 0 && (
                              <div 
                                className="h-full bg-purple-500 transition-all duration-300" 
                                style={{ width: `${physicalPercent}%` }}
                                title={`Physical: ${formatBytes(physicalBytes)} (${physicalPercent.toFixed(1)}%)`}
                              />
                            )}
                          </>
                        );
                      })()}
                    </div>
                  </div>
                  
                  {/* Traffic Type Legend */}
                  <div className="flex items-center justify-between mt-2 text-xs">
                    <div className="flex items-center gap-3">
                      <div className="flex items-center gap-1">
                        <div className="w-2 h-2 bg-blue-500 rounded-full"></div>
                        <span className="text-gray-600 dark:text-gray-400">Virtual</span>
                      </div>
                      <div className="flex items-center gap-1">
                        <div className="w-2 h-2 bg-green-500 rounded-full"></div>
                        <span className="text-gray-600 dark:text-gray-400">Subnet</span>
                      </div>
                      <div className="flex items-center gap-1">
                        <div className="w-2 h-2 bg-purple-500 rounded-full"></div>
                        <span className="text-gray-600 dark:text-gray-400">Physical</span>
                      </div>
                    </div>
                  </div>
                </div>
                
                {/* Quick Stats Row */}
                <div className="mt-3 grid grid-cols-3 gap-2 text-xs">
                  {/* Protocol Distribution */}
                  <div className="text-center">
                    <div className="text-gray-500 dark:text-gray-400 mb-0.5">Protocols</div>
                    <div className="font-semibold text-gray-900 dark:text-gray-100">
                      {(() => {
                        const protocols = new Set(filteredData.links.map(l => (l as any).protocol));
                        return protocols.size;
                      })()}
                    </div>
                  </div>
                  
                  {/* Average Bandwidth */}
                  <div className="text-center">
                    <div className="text-gray-500 dark:text-gray-400 mb-0.5">Avg/Node</div>
                    <div className="font-semibold text-gray-900 dark:text-gray-100">
                      {filteredData.nodes.length > 0 
                        ? formatBytes(filteredData.nodes.reduce((sum, node) => sum + node.totalBytes, 0) / filteredData.nodes.length)
                        : '0 B'
                      }
                    </div>
                  </div>
                  
                  {/* Peak Node */}
                  <div className="text-center">
                    <div className="text-gray-500 dark:text-gray-400 mb-0.5">Peak Node</div>
                    <div className="font-semibold text-gray-900 dark:text-gray-100 truncate px-1" title={
                      filteredData.nodes.length > 0 
                        ? filteredData.nodes.reduce((max, node) => node.totalBytes > max.totalBytes ? node : max).displayName
                        : 'None'
                    }>
                      {filteredData.nodes.length > 0 
                        ? (() => {
                            const peakNode = filteredData.nodes.reduce((max, node) => node.totalBytes > max.totalBytes ? node : max);
                            const name = peakNode.displayName;
                            return name.length > 12 ? name.substring(0, 10) + '...' : name;
                          })()
                        : 'None'
                      }
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
      </div>
      
      {/* Log Viewer */}
      <LogViewer
        networkLogs={filteredNetworkLogs as any[]}
        devices={devices}
        searchQuery={searchQuery}
        protocolFilters={protocolFilters}
        trafficTypeFilters={trafficTypeFilters}
        onHeightChange={setLogViewerHeight}
        selectedNode={selectedNode}
        selectedLink={selectedLink ? {
          ...selectedLink,
          source: typeof selectedLink.source === 'string' ? selectedLink.source : selectedLink.source.id,
          target: typeof selectedLink.target === 'string' ? selectedLink.target : selectedLink.target.id
        } : null}
        onClearSelection={() => {
          setSelectedNode(null)
          setSelectedLink(null)
        }}
        onSelectLog={(logEntry) => {
          // First, find the source and destination nodes
          const srcNode = nodes.find(n =>
            n.ips?.includes(logEntry.srcIP) || n.ip === logEntry.srcIP || n.displayName === logEntry.srcDevice
          )
          const dstNode = nodes.find(n =>
            n.ips?.includes(logEntry.dstIP) || n.ip === logEntry.dstIP || n.displayName === logEntry.dstDevice
          )

          // Try to find the corresponding link/edge between these nodes
          if (srcNode && dstNode) {
            const matchingLink = links.find(link => {
              const linkSourceId = typeof link.source === 'string' ? link.source : link.source.id
              const linkTargetId = typeof link.target === 'string' ? link.target : link.target.id

              // Check both directions (src->dst or dst->src)
              return (
                (linkSourceId === srcNode.id && linkTargetId === dstNode.id) ||
                (linkSourceId === dstNode.id && linkTargetId === srcNode.id)
              )
            })

            if (matchingLink) {
              // Select the link/edge instead of a node
              setSelectedLink(matchingLink)
              setSelectedNode(null)
            } else {
              // If no link found, fall back to selecting the source node
              setSelectedNode(srcNode)
              setSelectedLink(null)
            }
          } else if (srcNode) {
            // Only source node found
            setSelectedNode(srcNode)
            setSelectedLink(null)
          } else if (dstNode) {
            // Only destination node found
            setSelectedNode(dstNode)
            setSelectedLink(null)
          }
        }}
      />
    </div>
  )
}

export default NetworkView 