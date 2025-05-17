package server

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "net/http"
    "os/exec"
    "strings"
    "sync"
    "time"

    "github.com/docker/docker/api/types"
    _"github.com/docker/docker/client"
    "github.com/gin-gonic/gin"
    "github.com/gorilla/websocket"
)

// LogType defines the type of log message
type LogType string

const (
    LogTypeInfo    LogType = "info"
    LogTypeSuccess LogType = "success"
    LogTypeError   LogType = "error"
    LogTypeWarning LogType = "warning"
    LogTypeDaemon  LogType = "daemon"
)

// ConsoleSession represents a WebSocket session for a console
type ConsoleSession struct {
    Socket       *websocket.Conn
    ContainerId  string
    ServerId     string
    InternalId   string
    UserId       string
    Authenticated bool
    LogStream    io.ReadCloser
    LastLogIndex int
    LastHeartbeat time.Time
    Mutex        sync.Mutex
}

// WebSocketManager manages WebSocket connections for server consoles
type WebSocketManager struct {
    app              *AppState
    sessions         map[*websocket.Conn]*ConsoleSession
    logBuffers       map[string][]string
    validationCache  map[string]time.Time
    maxLogs          int
    initialLogs      int
    cacheTTL         time.Duration
    maxPayloadSize   int
    heartbeatInterval time.Duration
    connectionTimeout time.Duration
    upgrader         websocket.Upgrader
    sessionsMutex    sync.Mutex
    logBuffersMutex  sync.Mutex
}

// Create a new WebSocket manager
func newWebSocketManager(app *AppState) *WebSocketManager {
    manager := &WebSocketManager{
        app:              app,
        sessions:         make(map[*websocket.Conn]*ConsoleSession),
        logBuffers:       make(map[string][]string),
        validationCache:  make(map[string]time.Time),
        maxLogs:          100,
        initialLogs:      10,
        cacheTTL:         10 * time.Minute,
        maxPayloadSize:   50 * 1024, // 50KB
        heartbeatInterval: 30 * time.Second,
        connectionTimeout: 5 * time.Second,
        upgrader: websocket.Upgrader{
            CheckOrigin: func(r *http.Request) bool {
                return true // Allow all origins
            },
            ReadBufferSize:  1024,
            WriteBufferSize: 1024,
        },
        sessionsMutex:   sync.Mutex{},
        logBuffersMutex: sync.Mutex{},
    }

    // Start cache cleanup routine
    go manager.startCacheCleanup()
    
    // Start heartbeat check routine
    go manager.startHeartbeatCheck()

    return manager
}

// Start periodic cache cleanup
func (m *WebSocketManager) startCacheCleanup() {
    ticker := time.NewTicker(1 * time.Minute)
    defer ticker.Stop()

    for range ticker.C {
        now := time.Now()
        for key, timestamp := range m.validationCache {
            if now.Sub(timestamp) > m.cacheTTL {
                delete(m.validationCache, key)
            }
        }
    }
}

// Start heartbeat checking
func (m *WebSocketManager) startHeartbeatCheck() {
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()

    for range ticker.C {
        now := time.Now()
        m.sessionsMutex.Lock()
        for conn, session := range m.sessions {
            if now.Sub(session.LastHeartbeat) > 2*m.heartbeatInterval {
                log.Printf("Session timed out for server %s", session.ServerId)
                conn.Close()
                delete(m.sessions, conn)
            }
        }
        m.sessionsMutex.Unlock()
    }
}

// Add a log message to the buffer
func (m *WebSocketManager) addLogToBuffer(internalId string, log string) {
    m.logBuffersMutex.Lock()
    defer m.logBuffersMutex.Unlock()

    if !m.validatePayloadSize(log) {
        return // Skip if too large
    }

    if _, exists := m.logBuffers[internalId]; !exists {
        m.logBuffers[internalId] = []string{}
    }

    buffer := m.logBuffers[internalId]
    
    // Prevent duplicate logs
    for _, existingLog := range buffer {
        if existingLog == log {
            return
        }
    }

    // Add the log
    buffer = append(buffer, log)
    
    // Trim older logs if buffer is too large
    if len(buffer) > m.maxLogs {
        buffer = buffer[len(buffer)-m.maxLogs:]
    }
    
    m.logBuffers[internalId] = buffer
}

// Get logs for a server
func (m *WebSocketManager) getLogsForSession(internalId string) []string {
    m.logBuffersMutex.Lock()
    defer m.logBuffersMutex.Unlock()

    if logs, exists := m.logBuffers[internalId]; exists {
        return logs
    }
    return []string{}
}

// Broadcast a message to all sessions for a server
func (m *WebSocketManager) broadcastToServer(internalId string, log string, logType LogType) {
    if !m.validatePayloadSize(log) {
        log.Printf("Log message too large for broadcast")
        return
    }

    // Format the log message
    formattedLog := m.formatLogMessage(logType, log)
    
    // Add to buffer
    m.addLogToBuffer(internalId, formattedLog)
    
    // Broadcast to connected clients
    m.sessionsMutex.Lock()
    defer m.sessionsMutex.Unlock()
    
    for conn, session := range m.sessions {
        if session.InternalId == internalId && session.Authenticated {
            err := conn.WriteJSON(map[string]interface{}{
                "event": "console_output",
                "data": map[string]interface{}{
                    "message": formattedLog,
                },
            })
            if err != nil {
                log.Printf("Failed to send to WebSocket: %v", err)
            }
        }
    }
}

// Format a log message based on type
func (m *WebSocketManager) formatLogMessage(logType LogType, message string) string {
    switch logType {
    case LogTypeInfo:
        return message // Default color
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

// Validate payload size
func (m *WebSocketManager) validatePayloadSize(data string) bool {
    return len(data) <= m.maxPayloadSize
}

// Handler for WebSocket connections
func (m *WebSocketManager) handleConnection(c *gin.Context) {
    // Extract parameters
    serverId := c.Query("server")
    token := c.Query("token")

    if serverId == "" || token == "" {
        c.String(http.StatusBadRequest, "Missing server ID or token")
        return
    }

    // Upgrade to WebSocket
    conn, err := m.upgrader.Upgrade(c.Writer, c.Request, nil)
    if err != nil {
        log.Printf("Failed to upgrade connection: %v", err)
        return
    }

    // Create a timeout for authentication
    authTimeout := time.AfterFunc(m.connectionTimeout, func() {
        conn.Close()
    })
    defer authTimeout.Stop()

    // Create session
    session := &ConsoleSession{
        Socket:        conn,
        ServerId:      serverId,
        InternalId:    serverId,
        LastHeartbeat: time.Now(),
        Authenticated: false,
    }

    // Validate token
    // For MVP, we're just accepting any token, but in production you'd implement actual validation
    session.Authenticated = true

    // Cancel the timeout since auth was successful
    authTimeout.Stop()

    // Register session
    m.sessionsMutex.Lock()
    m.sessions[conn] = session
    m.sessionsMutex.Unlock()

    // Send historical logs
    logs := m.getLogsForSession(serverId)
    for _, log := range logs[len(logs)-m.initialLogs:] {
        conn.WriteJSON(map[string]interface{}{
            "event": "console_output",
            "data": map[string]interface{}{
                "message": log,
            },
        })
    }

    // Send welcome message
    conn.WriteJSON(map[string]interface{}{
        "event": "daemon_message",
        "data": fmt.Sprintf("Connected to Krypton Daemon v%s", m.app.Version),
    })

    // Get server info and container status
    server, err := m.app.getServer(serverId)
    if err == nil && server.DockerID != "" {
        session.ContainerId = server.DockerID

        // Start listening for logs from the container
        go m.attachLogs(session)

        // Start resource monitoring
        go m.startResourceMonitoring(session)
    }

    // Main message handler
    go func() {
        for {
            _, message, err := conn.ReadMessage()
            if err != nil {
                break
            }

            // Update heartbeat
            session.LastHeartbeat = time.Now()

            // Process messages
            var msg map[string]interface{}
            if err := json.Unmarshal(message, &msg); err != nil {
                continue
            }

            event, ok := msg["event"].(string)
            if !ok {
                continue
            }

            switch event {
            case "send_command":
                if data, ok := msg["data"].(string); ok {
                    m.handleSendCommand(session, data)
                }
            case "power_action":
                if data, ok := msg["data"].(map[string]interface{}); ok {
                    if action, ok := data["action"].(string); ok {
                        m.handlePowerAction(session, action)
                    }
                }
            case "heartbeat":
                conn.WriteJSON(map[string]interface{}{
                    "event": "heartbeat_ack",
                })
            }
        }

        // Clean up on disconnect
        m.sessionsMutex.Lock()
        delete(m.sessions, conn)
        m.sessionsMutex.Unlock()
        
        conn.Close()
    }()
}

// Handle sending commands to a container
func (m *WebSocketManager) handleSendCommand(session *ConsoleSession, command string) {
    if !m.validatePayloadSize(command) {
        return
    }

    // Sanitize command
    command = strings.TrimSpace(command)
    if command == "" {
        return
    }

    // If server is running, send command
    if session.ContainerId != "" {
        // Use docker exec to send command
        execCommand := exec.Command("docker", "attach", "--sig-proxy=false", session.ContainerId)
        stdin, err := execCommand.StdinPipe()
        if err != nil {
            m.broadcastToServer(session.InternalId, "Failed to send command", LogTypeError)
            return
        }

        // Start command
        if err := execCommand.Start(); err != nil {
            m.broadcastToServer(session.InternalId, "Failed to start command", LogTypeError)
            return
        }

        // Write command and newline
        if _, err := stdin.Write([]byte(command + "\n")); err != nil {
            m.broadcastToServer(session.InternalId, "Failed to write command", LogTypeError)
            return
        }

        // Close stdin
        stdin.Close()

        // Wait for command to complete
        go func() {
            execCommand.Wait()
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
            // Start log attachment
            go m.attachLogs(session)
        }
    case "stop":
        timeout := 10 * time.Second
        err = m.app.DockerClient.ContainerStop(ctx, session.ContainerId, &timeout)
    case "restart":
        timeout := 10 * time.Second
        err = m.app.DockerClient.ContainerRestart(ctx, session.ContainerId, &timeout)
        if err == nil {
            // Restart log attachment
            go m.attachLogs(session)
        }
    }

    if err != nil {
        m.broadcastToServer(session.InternalId, fmt.Sprintf("Failed to %s server: %v", action, err), LogTypeError)
        return
    }

    status := "running"
    if action == "stop" {
        status = "stopped"
    }

    message := fmt.Sprintf("Server is now %s", status)
    m.broadcastToServer(session.InternalId, message, LogTypeDaemon)

    // Send power status update
    session.Socket.WriteJSON(map[string]interface{}{
        "event": "power_status",
        "data": map[string]interface{}{
            "status": message,
            "action": action,
            "state":  status,
        },
    })
}

// Attach to container logs
func (m *WebSocketManager) attachLogs(session *ConsoleSession) {
    if session.ContainerId == "" {
        return
    }

    // Close existing log stream if it exists
    if session.LogStream != nil {
        session.LogStream.Close()
        session.LogStream = nil
    }

    // Attach to container logs
    options := types.ContainerLogsOptions{
        ShowStdout: true,
        ShowStderr: true,
        Follow:     true,
        Tail:       "50",
    }

    ctx := context.Background()
    logs, err := m.app.DockerClient.ContainerLogs(ctx, session.ContainerId, options)
    if err != nil {
        log.Printf("Failed to attach to container logs: %v", err)
        return
    }

    session.LogStream = logs

    // Read logs in a separate goroutine
    go func() {
        buffer := make([]byte, 8192)
        for {
            n, err := logs.Read(buffer)
            if err != nil {
                if err != io.EOF {
                    log.Printf("Error reading logs: %v", err)
                }
                break
            }

            if n > 8 { // Docker log format has 8-byte header
                // Extract the actual log message (skip 8-byte header)
                logMsg := string(buffer[8:n])
                
                // Clean and broadcast the message
                cleanedMsg := strings.TrimSpace(logMsg)
                if cleanedMsg != "" {
                    m.broadcastToServer(session.InternalId, cleanedMsg, LogTypeInfo)
                }
            }
        }
    }()
}

// Start monitoring container resources
func (m *WebSocketManager) startResourceMonitoring(session *ConsoleSession) {
    if session.ContainerId == "" {
        return
    }

    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()

    // Previous network stats for rate calculation
    var lastRxBytes, lastTxBytes uint64
    var lastCheck time.Time = time.Now()

    ctx := context.Background()

    for {
        select {
        case <-ticker.C:
            // Check if session still exists
            m.sessionsMutex.Lock()
            _, exists := m.sessions[session.Socket]
            m.sessionsMutex.Unlock()
            
            if !exists {
                return // Session ended
            }

            // Get container status
            containerInfo, err := m.app.DockerClient.ContainerInspect(ctx, session.ContainerId)
            if err != nil {
                log.Printf("Failed to inspect container: %v", err)
                continue
            }

            state := containerInfo.State.Status
            if state != "running" {
                // If container is not running, just send state
                session.Socket.WriteJSON(map[string]interface{}{
                    "event": "stats",
                    "data": map[string]interface{}{
                        "state": state,
                    },
                })
                continue
            }

            // Get detailed stats
            stats, err := m.app.DockerClient.ContainerStats(ctx, session.ContainerId, false)
            if err != nil {
                log.Printf("Failed to get container stats: %v", err)
                continue
            }

            var statsData types.StatsJSON
            if err := json.NewDecoder(stats.Body).Decode(&statsData); err != nil {
                log.Printf("Failed to decode stats: %v", err)
                stats.Body.Close()
                continue
            }
            stats.Body.Close()

            // Calculate rates
            now := time.Now()
            timeDiff := now.Sub(lastCheck).Seconds()
            
            // Calculate network rates - handle case where interface might not exist
            var rxBytes, txBytes uint64
            if netStats, ok := statsData.Networks["eth0"]; ok {
                rxBytes = netStats.RxBytes
                txBytes = netStats.TxBytes
            }
            
            rxRate := float64(rxBytes-lastRxBytes) / timeDiff
            txRate := float64(txBytes-lastTxBytes) / timeDiff
            
            lastRxBytes = rxBytes
            lastTxBytes = txBytes
            lastCheck = now

            // Calculate CPU percent
            cpuDelta := float64(statsData.CPUStats.CPUUsage.TotalUsage - statsData.PreCPUStats.CPUUsage.TotalUsage)
            systemDelta := float64(statsData.CPUStats.SystemUsage - statsData.PreCPUStats.SystemUsage)
            cpuPercent := 0.0
            if systemDelta > 0 {
                cpuPercent = (cpuDelta / systemDelta) * float64(len(statsData.CPUStats.CPUUsage.PercpuUsage)) * 100.0
            }

            // Send stats to client
            session.Socket.WriteJSON(map[string]interface{}{
                "event": "stats",
                "data": map[string]interface{}{
                    "state": state,
                    "cpu_percent": cpuPercent,
                    "memory": map[string]interface{}{
                        "used": statsData.MemoryStats.Usage,
                        "limit": statsData.MemoryStats.Limit,
                        "percent": float64(statsData.MemoryStats.Usage) / float64(statsData.MemoryStats.Limit) * 100.0,
                    },
                    "network": map[string]interface{}{
                        "rx_bytes": rxBytes,
                        "tx_bytes": txBytes,
                        "rx_rate": rxRate,
                        "tx_rate": txRate,
                    },
                },
            })
        }
    }
}

// Configure WebSocket routes
func configureWebsocketRouter(app *AppState, router *gin.Engine) {
    wsManager := newWebSocketManager(app)
    router.GET("/ws", wsManager.handleConnection)
}