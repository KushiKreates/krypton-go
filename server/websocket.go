package server

/** I have given up on this section 
Cluade has also given up on this so yes
* Krypton Daemon in GO
* Websocket section is cooked

**/

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "net/http"
    "strings"
    "sync"
    "time"

    "github.com/docker/docker/api/types"
    "github.com/docker/docker/api/types/container"
    "github.com/gin-gonic/gin"
    "github.com/gorilla/websocket"
)

// WSMessage represents a WebSocket message
type WSMessage struct {
    Event string      `json:"event"`
    Data  interface{} `json:"data"`
}

// LogType defines the type of log message
type LogType string

const (
    LogTypeInfo    LogType = "info"
    LogTypeSuccess LogType = "success"
    LogTypeError   LogType = "error"
    LogTypeWarning LogType = "warning"
    LogTypeDaemon  LogType = "daemon"
)

// ConsoleSession represents an active console session
type ConsoleSession struct {
    Socket       *websocket.Conn
    ContainerId  string
    ServerId     string
    InternalId   string
    UserId       string
    Authenticated bool
    LastHeartbeat time.Time
    LogStream     io.ReadCloser
}

// ValidationResponse represents the response from the panel API
type ValidationResponse struct {
    Validated bool `json:"validated"`
    Server    struct {
        ID         string `json:"id"`
        Name       string `json:"name"`
        InternalID string `json:"internalId"`
        Node       struct {
            ID   string `json:"id"`
            Name string `json:"name"`
            FQDN string `json:"fqdn"`
            Port int    `json:"port"`
        } `json:"node"`
    } `json:"server"`
}

// CachedValidation represents a cached validation response
type CachedValidation struct {
    Validation ValidationResponse
    Timestamp  time.Time
}

// StatsData represents container stats for the WebSocket
type StatsData struct {
    State     string       `json:"state"`
    CpuPercent float64     `json:"cpu_percent"`
    Memory    MemoryStats  `json:"memory"`
    Network   NetworkStats `json:"network,omitempty"`
}

// MemoryStats represents memory statistics
type MemoryStats struct {
    Used    uint64  `json:"used"`
    Limit   uint64  `json:"limit"`
    Percent float64 `json:"percent"`
}

// NetworkStats represents network statistics
type NetworkStats struct {
    RxBytes uint64 `json:"rx_bytes"`
    TxBytes uint64 `json:"tx_bytes"`
    RxRate  float64 `json:"rx_rate,omitempty"`
    TxRate  float64 `json:"tx_rate,omitempty"`
}

// DockerStats represents simple Docker stats
type DockerStats struct {
    CPUStats struct {
        CPUUsage struct {
            TotalUsage  uint64 `json:"total_usage"`
            PercpuUsage []uint64 `json:"percpu_usage"`
        } `json:"cpu_usage"`
        SystemUsage uint64 `json:"system_cpu_usage"`
        OnlineCPUs  uint32 `json:"online_cpus"`
    } `json:"cpu_stats"`
    PreCPUStats struct {
        CPUUsage struct {
            TotalUsage  uint64 `json:"total_usage"`
            PercpuUsage []uint64 `json:"percpu_usage"`
        } `json:"cpu_usage"`
        SystemUsage uint64 `json:"system_cpu_usage"`
    } `json:"precpu_stats"`
    MemoryStats struct {
        Usage uint64 `json:"usage"`
        Limit uint64 `json:"limit"`
    } `json:"memory_stats"`
    Networks map[string]struct {
        RxBytes uint64 `json:"rx_bytes"`
        TxBytes uint64 `json:"tx_bytes"`
    } `json:"networks"`
}

// WebSocketManager manages WebSocket connections and Docker interactions
type WebSocketManager struct {
    app                 *AppState
    sessions            map[*websocket.Conn]*ConsoleSession
    sessionsMutex       sync.RWMutex
    logBuffers          map[string][]string
    logBuffersMutex     sync.RWMutex
    validationCache     map[string]CachedValidation
    validationCacheMutex sync.RWMutex
    maxLogs             int
    initialLogs         int
    cacheTTL            time.Duration
    upgrader            websocket.Upgrader
}

// newWebSocketManager creates a new WebSocket manager
func newWebSocketManager(app *AppState) *WebSocketManager {
    m := &WebSocketManager{
        app:             app,
        sessions:        make(map[*websocket.Conn]*ConsoleSession),
        logBuffers:      make(map[string][]string),
        validationCache: make(map[string]CachedValidation),
        maxLogs:         100,
        initialLogs:     10,
        cacheTTL:        10 * time.Minute,
        upgrader: websocket.Upgrader{
            CheckOrigin: func(r *http.Request) bool {
                return true
            },
        },
    }

    // Start cache cleanup goroutine
    go m.startCacheCleanup()
    
    // Start heartbeat check goroutine
    go m.startHeartbeatCheck()

    return m
}

// startCacheCleanup periodically cleans up expired cache entries
func (m *WebSocketManager) startCacheCleanup() {
    ticker := time.NewTicker(1 * time.Minute)
    defer ticker.Stop()

    for range ticker.C {
        now := time.Now()
        m.validationCacheMutex.Lock()
        for key, cached := range m.validationCache {
            if now.Sub(cached.Timestamp) > m.cacheTTL {
                delete(m.validationCache, key)
            }
        }
        m.validationCacheMutex.Unlock()
    }
}

// startHeartbeatCheck periodically checks for stale connections
func (m *WebSocketManager) startHeartbeatCheck() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for range ticker.C {
        now := time.Now()
        m.sessionsMutex.Lock()
        for conn, session := range m.sessions {
            if now.Sub(session.LastHeartbeat) > 2*time.Minute {
                // Close stale connection
                conn.WriteMessage(websocket.CloseMessage, 
                    websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Session timed out"))
                conn.Close()
                delete(m.sessions, conn)
            }
        }
        m.sessionsMutex.Unlock()
    }
}

// handleConnection handles incoming WebSocket connections
func (m *WebSocketManager) handleConnection(c *gin.Context) {
    // Extract query parameters
    serverId := c.Query("server")
    token := c.Query("token")

    if serverId == "" || token == "" {
        c.String(400, "Missing server ID or token")
        return
    }

    // Sanitize server ID
    serverId = m.sanitizeId(serverId)

    // Upgrade HTTP connection to WebSocket
    ws, err := m.upgrader.Upgrade(c.Writer, c.Request, nil)
    if err != nil {
        log.Printf("Failed to set up WebSocket connection: %v", err)
        return
    }

    // Set connection timeout
    connectionTimeout := time.AfterFunc(5*time.Second, func() {
        ws.Close()
    })
    defer connectionTimeout.Stop()

    // Validate token
    validation, err := m.validateToken(serverId, token)
    if err != nil || !validation.Validated {
        ws.WriteMessage(websocket.CloseMessage, 
            websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Invalid token or access denied"))
        ws.Close()
        return
    }

    // Set up container session
    session, err := m.setupContainerSession(ws, serverId, validation)
    if err != nil {
        ws.WriteMessage(websocket.CloseMessage, 
            websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Failed to setup session"))
        ws.Close()
        return
    }

    // Cancel the connection timeout since we're authenticated
    connectionTimeout.Stop()

    // Set initial heartbeat
    session.LastHeartbeat = time.Now()

    // Listen for WebSocket messages in a goroutine
    go m.listenForMessages(ws, session)
}

// listenForMessages handles incoming WebSocket messages
func (m *WebSocketManager) listenForMessages(ws *websocket.Conn, session *ConsoleSession) {
    defer func() {
        m.cleanupSession(ws)
        ws.Close()
    }()

    for {
        // Read message
        messageType, message, err := ws.ReadMessage()
        if err != nil {
            log.Printf("Error reading WebSocket message: %v", err)
            break
        }

        // Update heartbeat timestamp
        m.sessionsMutex.Lock()
        if s, exists := m.sessions[ws]; exists {
            s.LastHeartbeat = time.Now()
        }
        m.sessionsMutex.Unlock()

        // Handle message based on type
        if messageType == websocket.TextMessage {
            var msg WSMessage
            if err := json.Unmarshal(message, &msg); err != nil {
                log.Printf("Error parsing WebSocket message: %v", err)
                continue
            }

            // Handle different message events
            switch msg.Event {
            case "send_command":
                if cmdData, ok := msg.Data.(string); ok {
                    m.handleSendCommand(session, cmdData)
                }

            case "power_action":
                if actionMap, ok := msg.Data.(map[string]interface{}); ok {
                    if action, ok := actionMap["action"].(string); ok {
                        m.handlePowerAction(session, action)
                    }
                }

            case "heartbeat":
                ws.WriteJSON(WSMessage{
                    Event: "heartbeat_ack",
                })
            }
        }
    }
}

// cleanupSession removes a session and cleans up resources
func (m *WebSocketManager) cleanupSession(ws *websocket.Conn) {
    m.sessionsMutex.Lock()
    defer m.sessionsMutex.Unlock()

    if session, exists := m.sessions[ws]; exists {
        // Close log stream if it exists
        if session.LogStream != nil {
            session.LogStream.Close()
        }
        
        delete(m.sessions, ws)
    }
}

// sanitizeId removes non-alphanumeric characters from ID
func (m *WebSocketManager) sanitizeId(id string) string {
    var result strings.Builder
    for _, r := range id {
        if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
            result.WriteRune(r)
        }
    }
    return result.String()
}

// generateCacheKey creates a unique cache key from server ID and token
func (m *WebSocketManager) generateCacheKey(internalId, token string) string {
    hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", internalId, token)))
    return hex.EncodeToString(hash[:])
}

// validateToken validates a token against the panel API or cache
func (m *WebSocketManager) validateToken(internalId, token string) (*ValidationResponse, error) {
    cacheKey := m.generateCacheKey(internalId, token)
    
    // Check cache first
    m.validationCacheMutex.RLock()
    if cached, exists := m.validationCache[cacheKey]; exists {
        if time.Since(cached.Timestamp) < m.cacheTTL {
            m.validationCacheMutex.RUnlock()
            return &cached.Validation, nil
        }
    }
    m.validationCacheMutex.RUnlock()

    // Make API request to validate token
    url := fmt.Sprintf("%s/api/servers/%s/validate/%s", m.app.Config.AppURL, internalId, token)
    
    // Create HTTP client with timeout
    client := &http.Client{
        Timeout: 5 * time.Second,
    }
    
    req, err := http.NewRequest("GET", url, nil)
    if err != nil {
        return nil, err
    }
    req.Header.Set("Authorization", "Bearer "+token)
    
    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("panel returned status code %d", resp.StatusCode)
    }
    
    var validation ValidationResponse
    if err := json.NewDecoder(resp.Body).Decode(&validation); err != nil {
        return nil, err
    }
    
    // Store in cache
    m.validationCacheMutex.Lock()
    m.validationCache[cacheKey] = CachedValidation{
        Validation: validation,
        Timestamp:  time.Now(),
    }
    m.validationCacheMutex.Unlock()
    
    return &validation, nil
}

// setupContainerSession sets up a container session for a WebSocket connection
func (m *WebSocketManager) setupContainerSession(ws *websocket.Conn, internalId string, validation *ValidationResponse) (*ConsoleSession, error) {
    // Get container ID from database
    serverData, err := m.app.getServer(internalId)
    if err != nil {
        return nil, fmt.Errorf("server not found: %v", err)
    }
    
    if serverData.DockerID == "" {
        return nil, fmt.Errorf("no container assigned to server")
    }
    
    // Create session
    session := &ConsoleSession{
        Socket:        ws,
        ContainerId:   serverData.DockerID,
        ServerId:      validation.Server.ID,
        InternalId:    validation.Server.InternalID,
        UserId:        validation.Server.ID,
        Authenticated: true,
        LastHeartbeat: time.Now(),
    }
    
    // Store session
    m.sessionsMutex.Lock()
    m.sessions[ws] = session
    m.sessionsMutex.Unlock()
    
    // Get container info
    ctx := context.Background()
    containerInfo, err := m.app.DockerClient.ContainerInspect(ctx, serverData.DockerID)
    if err != nil {
        return nil, fmt.Errorf("failed to inspect container: %v", err)
    }
    
    // Send initial logs
    logs := m.getLogsForSession(internalId)
    for _, log := range logs[max(0, len(logs)-m.initialLogs):] {
        ws.WriteJSON(WSMessage{
            Event: "console_output",
            Data:  map[string]string{"message": log},
        })
    }
    
    // Send initial stats
    stats, err := m.app.DockerClient.ContainerStats(ctx, serverData.DockerID, false)
    if err == nil {
        defer stats.Body.Close()
        
        var statsData DockerStats
        if err := json.NewDecoder(stats.Body).Decode(&statsData); err == nil {
            cpuPercent := m.calculateCPUPercent(&statsData)
            memoryUsage := statsData.MemoryStats.Usage
            memoryLimit := statsData.MemoryStats.Limit
            
            memoryPercent := 0.0
            if memoryLimit > 0 {
                memoryPercent = float64(memoryUsage) / float64(memoryLimit) * 100.0
            }
            
            networkRx := uint64(0)
            networkTx := uint64(0)
            if statsData.Networks != nil {
                if eth0, ok := statsData.Networks["eth0"]; ok {
                    networkRx = eth0.RxBytes
                    networkTx = eth0.TxBytes
                }
            }
            
            ws.WriteJSON(WSMessage{
                Event: "stats",
                Data: StatsData{
                    State:     m.formatContainerState(containerInfo.State.Status),
                    CpuPercent: cpuPercent,
                    Memory: MemoryStats{
                        Used:    memoryUsage,
                        Limit:   memoryLimit,
                        Percent: memoryPercent,
                    },
                    Network: NetworkStats{
                        RxBytes: networkRx,
                        TxBytes: networkTx,
                    },
                },
            })
        }
    }
    
    // Send auth success
    ws.WriteJSON(WSMessage{
        Event: "auth_success",
        Data: map[string]string{
            "state": m.formatContainerState(containerInfo.State.Status),
        },
    })
    
    // Attach to logs
    go m.attachLogs(session)
    
    // Start monitoring resources
    go m.startResourceMonitoring(session)
    
    return session, nil
}

// formatContainerState formats the container state for frontend
func (m *WebSocketManager) formatContainerState(state string) string {
    if state == "exited" {
        return "stopped"
    }
    return state
}

// attachLogs attaches to container logs and streams them to the WebSocket
func (m *WebSocketManager) attachLogs(session *ConsoleSession) {
    ctx := context.Background()
    
    options := types.ContainerLogsOptions{
        ShowStdout: true,
        ShowStderr: true,
        Follow:     true,
        Tail:       "0",
    }
    
    logStream, err := m.app.DockerClient.ContainerLogs(ctx, session.ContainerId, options)
    if err != nil {
        m.broadcastToServer(session.InternalId, fmt.Sprintf("Failed to attach to logs: %v", err), LogTypeError)
        return
    }
    
    session.LogStream = logStream
    
    // Read logs in a separate goroutine
    go func() {
        defer logStream.Close()
        
        // Use a simple buffer for log lines
        buf := make([]byte, 8192)
        
        for {
            // Read header (first 8 bytes)
            header := make([]byte, 8)
            _, err := io.ReadFull(logStream, header)
            if err != nil {
                if err != io.EOF {
                    log.Printf("Error reading log header: %v", err)
                }
                break
            }
            
            // Get content size from header (bytes 4-7)
            size := int(uint32(header[4]) | uint32(header[5])<<8 | uint32(header[6])<<16 | uint32(header[7])<<24)
            
            // Read content
            content := buf[:0]
            if cap(buf) < size {
                content = make([]byte, size)
            } else {
                content = buf[:size]
            }
            
            _, err = io.ReadFull(logStream, content)
            if err != nil {
                if err != io.EOF {
                    log.Printf("Error reading log content: %v", err)
                }
                break
            }
            
            // Process log line
            line := string(content)
            line = strings.ReplaceAll(line, "pterodactyl", "argon")
            
            // Store in log buffer
            m.addLogToBuffer(session.InternalId, line)
            
            // Send to WebSocket client
            m.sessionsMutex.RLock()
            if socket, exists := m.sessions[session.Socket]; exists && socket.Authenticated {
                session.Socket.WriteJSON(WSMessage{
                    Event: "console_output",
                    Data:  map[string]string{"message": line},
                })
            }
            m.sessionsMutex.RUnlock()
        }
    }()
}

// startResourceMonitoring monitors container resources and sends updates
func (m *WebSocketManager) startResourceMonitoring(session *ConsoleSession) {
    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()

    var lastNetworkRx uint64 = 0
    var lastNetworkTx uint64 = 0
    lastCheck := time.Now()

    for range ticker.C {
        // Check if the connection still exists
        m.sessionsMutex.RLock()
        _, exists := m.sessions[session.Socket]
        m.sessionsMutex.RUnlock()
        
        if !exists {
            return // Session no longer exists
        }
        
        ctx := context.Background()
        
        // Get container info
        containerInfo, err := m.app.DockerClient.ContainerInspect(ctx, session.ContainerId)
        if err != nil {
            log.Printf("Error inspecting container: %v", err)
            continue
        }
        
        state := m.formatContainerState(containerInfo.State.Status)
        
        // If container is running, get stats
        if containerInfo.State.Running {
            stats, err := m.app.DockerClient.ContainerStats(ctx, session.ContainerId, false)
            if err != nil {
                log.Printf("Error getting container stats: %v", err)
                continue
            }
            
            var statsData DockerStats
            err = json.NewDecoder(stats.Body).Decode(&statsData)
            stats.Body.Close()
            
            if err != nil {
                log.Printf("Error decoding container stats: %v", err)
                continue
            }
            
            now := time.Now()
            timeDiff := now.Sub(lastCheck).Seconds()
            
            // Calculate CPU percentage
            cpuPercent := m.calculateCPUPercent(&statsData)
            
            // Get memory stats
            memoryUsage := statsData.MemoryStats.Usage
            memoryLimit := statsData.MemoryStats.Limit
            
            memoryPercent := 0.0
            if memoryLimit > 0 {
                memoryPercent = float64(memoryUsage) / float64(memoryLimit) * 100.0
            }
            
            // Get network stats
            networkRx := uint64(0)
            networkTx := uint64(0)
            if statsData.Networks != nil {
                if eth0, ok := statsData.Networks["eth0"]; ok {
                    networkRx = eth0.RxBytes
                    networkTx = eth0.TxBytes
                }
            }
            
            // Calculate network rates
            rxRate := float64(0)
            txRate := float64(0)
            if lastNetworkRx > 0 && timeDiff > 0 {
                rxRate = float64(networkRx-lastNetworkRx) / timeDiff
                txRate = float64(networkTx-lastNetworkTx) / timeDiff
            }
            
            lastNetworkRx = networkRx
            lastNetworkTx = networkTx
            lastCheck = now
            
            // Send stats to WebSocket client
            session.Socket.WriteJSON(WSMessage{
                Event: "stats",
                Data: StatsData{
                    State:     state,
                    CpuPercent: cpuPercent,
                    Memory: MemoryStats{
                        Used:    memoryUsage,
                        Limit:   memoryLimit,
                        Percent: memoryPercent,
                    },
                    Network: NetworkStats{
                        RxBytes: networkRx,
                        TxBytes: networkTx,
                        RxRate:  rxRate,
                        TxRate:  txRate,
                    },
                },
            })
        } else {
            // Container is not running, just send state
            session.Socket.WriteJSON(WSMessage{
                Event: "stats",
                Data: map[string]string{
                    "state": state,
                },
            })
        }
    }
}

// calculateCPUPercent calculates CPU percentage from stats
func (m *WebSocketManager) calculateCPUPercent(stats *DockerStats) float64 {
    cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
    systemDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)
    cpuCount := float64(stats.CPUStats.OnlineCPUs)
    
    if cpuCount == 0 {
        cpuCount = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
    }
    
    if systemDelta > 0 && cpuDelta > 0 {
        cpuPercent := (cpuDelta / systemDelta) * cpuCount * 100.0
        if cpuPercent > 100.0 {
            return 100.0
        }
        return cpuPercent
    }
    
    return 0.0
}

// handleSendCommand sends a command to the container
func (m *WebSocketManager) handleSendCommand(session *ConsoleSession, command string) {
    if command == "" {
        return
    }

    // Check if container is running
    ctx := context.Background()
    containerInfo, err := m.app.DockerClient.ContainerInspect(ctx, session.ContainerId)
    if err != nil {
        m.broadcastToServer(session.InternalId, "Failed to get container info", LogTypeError)
        return
    }

    if containerInfo.State.Running {
        // Create exec instance
        execConfig := types.ExecConfig{
            AttachStdin:  true,
            AttachStdout: true,
            AttachStderr: true,
            Tty:          false,
            Cmd:          []string{"sh", "-c", command + "\n"},
        }

        execResponse, err := m.app.DockerClient.ContainerExecCreate(ctx, session.ContainerId, execConfig)
        if err != nil {
            m.broadcastToServer(session.InternalId, fmt.Sprintf("Failed to create exec: %v", err), LogTypeError)
            return
        }

        // Start exec instance
        execStartConfig := types.ExecStartOptions{
            Detach: false,
            Tty:    false,
        }

        execStartResponse, err := m.app.DockerClient.ContainerExecAttach(ctx, execResponse.ID, execStartConfig)
        if err != nil {
            m.broadcastToServer(session.InternalId, fmt.Sprintf("Failed to start exec: %v", err), LogTypeError)
            return
        }
        defer execStartResponse.Close()

        // Command will execute automatically
        // Wait for command to complete
        go func() {
            // Don't need to read the output as it will come through the container logs
        }()
    } else {
        m.broadcastToServer(session.InternalId, "Server is not running", LogTypeError)
    }
}

// Handle power actions (start, stop, restart)
func (m *WebSocketManager) handlePowerAction(session *ConsoleSession, action string) {
    if session.ContainerId == "" {
        m.broadcastToServer(session.InternalId, "No container available for power action", LogTypeError)
        return
    }

    if !strings.Contains("start stop restart", action) {
        m.broadcastToServer(session.InternalId, "Invalid power action", LogTypeError)
        return
    }

    m.broadcastToServer(session.InternalId, fmt.Sprintf("Performing %s action...", action), LogTypeDaemon)

    ctx := context.Background()
    var err error

    switch action {
    case "start":
        err = m.app.DockerClient.ContainerStart(ctx, session.ContainerId, types.ContainerStartOptions{})
        if err == nil {
            // Re-attach to logs after starting
            go m.attachLogs(session)
            m.broadcastToServer(session.InternalId, "Server has been started", LogTypeSuccess)
        }

    case "stop":
        timeout := 30 // seconds
        err = m.app.DockerClient.ContainerStop(ctx, session.ContainerId, container.StopOptions{Timeout: &timeout})
        if err == nil {
            m.broadcastToServer(session.InternalId, "Server has been stopped", LogTypeSuccess)
        }

    case "restart":
        timeout := 30 // seconds
        err = m.app.DockerClient.ContainerRestart(ctx, session.ContainerId, container.StopOptions{Timeout: &timeout})
        if err == nil {
            // Re-attach to logs after restarting
            go m.attachLogs(session)
            m.broadcastToServer(session.InternalId, "Server has been restarted", LogTypeSuccess)
        }
    }

    if err != nil {
        m.broadcastToServer(session.InternalId, fmt.Sprintf("Failed to %s server: %v", action, err), LogTypeError)
    }

    // Send power status update
    containerInfo, err := m.app.DockerClient.ContainerInspect(ctx, session.ContainerId)
    if err == nil {
        state := m.formatContainerState(containerInfo.State.Status)
        errorMsg := containerInfo.State.Error

        var statusMessage string
        if state == "running" {
            statusMessage = fmt.Sprintf("%s The server is now powered on.", "[Krypton Daemon]")
        } else {
            statusMessage = fmt.Sprintf("%s The server has been powered off.", "[Krypton Daemon]")
        }
        
        session.Socket.WriteJSON(WSMessage{
            Event: "power_status",
            Data: map[string]interface{}{
                "status": statusMessage,
                "action": action,
                "state":  state,
                "error":  errorMsg,
            },
        })
    }
}

// addLogToBuffer adds a log message to the buffer for a server
func (m *WebSocketManager) addLogToBuffer(internalId string, log string) {
    m.logBuffersMutex.Lock()
    defer m.logBuffersMutex.Unlock()

    if _, exists := m.logBuffers[internalId]; !exists {
        m.logBuffers[internalId] = make([]string, 0, m.maxLogs)
    }

    buffer := m.logBuffers[internalId]
    
    // Prevent duplicate logs as the last entry
    if len(buffer) > 0 && buffer[len(buffer)-1] == log {
        return
    }
    
    buffer = append(buffer, log)
    
    // Trim buffer if it exceeds max size
    if len(buffer) > m.maxLogs {
        buffer = buffer[len(buffer)-m.maxLogs:]
    }
    
    m.logBuffers[internalId] = buffer
}

// getLogsForSession gets logs for a session
func (m *WebSocketManager) getLogsForSession(internalId string) []string {
    m.logBuffersMutex.RLock()
    defer m.logBuffersMutex.RUnlock()

    if logs, exists := m.logBuffers[internalId]; exists {
        return logs
    }
    return []string{}
}

// broadcastToServer sends a message to all sessions for a server
func (m *WebSocketManager) broadcastToServer(internalId string, log string, logType LogType) {
    formattedLog := m.formatLogMessage(logType, log)
    m.addLogToBuffer(internalId, formattedLog)

    m.sessionsMutex.RLock()
    defer m.sessionsMutex.RUnlock()

    for _, session := range m.sessions {
        if session.InternalId == internalId && session.Authenticated {
            session.Socket.WriteJSON(WSMessage{
                Event: "console_output",
                Data: map[string]string{
                    "message": formattedLog,
                },
            })
        }
    }
}

// formatLogMessage formats a log message with colors
func (m *WebSocketManager) formatLogMessage(logType LogType, message string) string {
    switch logType {
    case LogTypeInfo:
        return message
    case LogTypeSuccess:
        return "[Success] " + message
    case LogTypeError:
        return "[Error] " + message
    case LogTypeWarning:
        return "[Warning] " + message
    case LogTypeDaemon:
        return "[Krypton Daemon] " + message
    default:
        return message
    }
}

// Helper function to get max of two integers
func max(a, b int) int {
    if a > b {
        return a
    }
    return b
}

// configureWebsocketRouter sets up the websocket routes
func configureWebsocketRouter(app *AppState, router *gin.Engine) {
    wsManager := newWebSocketManager(app)
    app.WebSocketManager = wsManager
    router.GET("/ws", wsManager.handleConnection)
}