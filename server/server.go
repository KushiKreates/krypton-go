package server

import (
    "context"
    "encoding/json"
    "fmt"
    "io/ioutil"
    "log"
    "net/http"
    "os"
    "path/filepath"
    "strings"

    "github.com/docker/docker/api/types"
    "github.com/docker/docker/client"
    "github.com/gin-gonic/gin"
    "github.com/gorilla/websocket"
    "gopkg.in/yaml.v3"
)

// ServerState represents the current state of a server
type ServerState string

const (
    ServerStateCreating      ServerState = "creating"
    ServerStateInstalling    ServerState = "installing"
    ServerStateInstallFailed ServerState = "install_failed"
    ServerStateInstalled     ServerState = "installed"
    ServerStateStarting      ServerState = "starting"
    ServerStateRunning       ServerState = "running"
    ServerStateUpdating      ServerState = "updating"
    ServerStateUpdateFailed  ServerState = "update_failed"
    ServerStateStopping      ServerState = "stopping"
    ServerStateStopped       ServerState = "stopped"
    ServerStateErrored       ServerState = "errored"
    ServerStateDeleting      ServerState = "deleting"
)

// ServerVariable represents a configurable server variable
type ServerVariable struct {
    Name         string `json:"name"`
    Description  string `json:"description,omitempty"`
    CurrentValue string `json:"currentValue,omitempty"`
    DefaultValue string `json:"defaultValue"`
    Rules        string `json:"rules"`
}

// ServerInstallConfig represents the installation configuration for a server
type ServerInstallConfig struct {
    DockerImage string `json:"dockerImage"`
    Script      string `json:"script"`
    Entrypoint  string `json:"entrypoint,omitempty"`
}

// ServerConfigFile represents a configuration file for a server
type ServerConfigFile struct {
    Path    string `json:"path"`
    Content string `json:"content"`
}

// ServerConfig represents the configuration for a server
type ServerConfig struct {
    DockerImage    string             `json:"dockerImage"`
    StartupCommand string             `json:"startupCommand"`
    Install        ServerInstallConfig `json:"install"`
    Variables      []ServerVariable   `json:"variables"`
    ConfigFiles    []ServerConfigFile `json:"configFiles"`
    Cargo          []CargoFile        `json:"cargo,omitempty"`
}

// CargoFile represents a cargo file for a server
type CargoFile struct {
    ID         string `json:"id"`
    URL        string `json:"url"`
    TargetPath string `json:"targetPath"`
    Properties struct {
        Hidden           bool                   `json:"hidden,omitempty"`
        Readonly         bool                   `json:"readonly,omitempty"`
        NoDelete         bool                   `json:"noDelete,omitempty"`
        CustomProperties map[string]interface{} `json:"customProperties,omitempty"`
    } `json:"properties"`
}

// Allocation represents a network allocation for a server
type Allocation struct {
    BindAddress string `json:"bindAddress"`
    Port        int    `json:"port"`
}

// Server represents a server instance
type Server struct {
    ID             string          `json:"id"`
    DockerID       string          `json:"dockerId,omitempty"`
    Name           string          `json:"name"`
    Image          string          `json:"image"`
    State          ServerState     `json:"state"`
    MemoryLimit    int             `json:"memoryLimit"`
    CPULimit       int             `json:"cpuLimit"`
    Variables      []ServerVariable `json:"variables"`
    StartupCommand string          `json:"startupCommand"`
    Allocation     Allocation      `json:"allocation"`
    Config         ServerConfig    `json:"config,omitempty"`
}

// Config represents the application configuration
type Config struct {
    APIKey           string `yaml:"apiKey"`
    BindAddress      string `yaml:"bindAddress"`
    BindPort         int    `yaml:"bindPort"`
    VolumesDirectory string `yaml:"volumesDirectory"`
    AppURL           string `yaml:"appUrl"`
    CorsOrigin       string `yaml:"corsOrigin"`
}

// AppState represents the current state of the application
type AppState struct {
    DockerClient    *client.Client
    Config          Config
    ServersDir      string
    WSClients       map[*websocket.Conn]bool
    WSBroadcast     chan WSMessage
    BootstrapDir    string
    Version         string
    WebSocketManager *WebSocketManager
}

// WSMessage represents a WebSocket message


// Start initializes and starts the Krypton server
func Start(version string) error {
    log.Printf("Starting Krypton Server v%s", version)
    
    // Initialize application state
    app, err := initAppState(version)
    if err != nil {
        return fmt.Errorf("failed to initialize application: %v", err)
    }

    // Initialize WebSocket manager
    app.WebSocketManager = newWebSocketManager(app)

    // Start WebSocket broadcaster
    go app.runWSBroadcaster()

    // Set up and start HTTP server
    listenAddr := fmt.Sprintf("%s:%d", app.Config.BindAddress, app.Config.BindPort)
    log.Printf("Listening on %s", listenAddr)
    
    router := setupRouter(app)
    return router.Run(listenAddr)
}

// Initialize application state
func initAppState(version string) (*AppState, error) {
    config, err := loadConfig()
    if err != nil {
        return nil, fmt.Errorf("failed to load config: %w", err)
    }

    // Initialize Docker client
    dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
    if err != nil {
        return nil, fmt.Errorf("failed to create Docker client: %w", err)
    }

    // Check Docker connectivity
    if _, err := dockerClient.Ping(context.Background()); err != nil {
        return nil, fmt.Errorf("failed to connect to Docker: %w", err)
    }

    // Create bootstrap and server directories
    bootstrapDir := "cache/bootstrap"
    serversDir := filepath.Join(bootstrapDir, "servers")
    
    for _, dir := range []string{
        bootstrapDir,
        serversDir,
        config.VolumesDirectory,
    } {
        if err := os.MkdirAll(dir, 0755); err != nil {
            return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
        }
    }

    return &AppState{
        DockerClient: dockerClient,
        Config:       config,
        ServersDir:   serversDir,
        WSClients:    make(map[*websocket.Conn]bool),
        WSBroadcast:  make(chan WSMessage),
        BootstrapDir: bootstrapDir,
        Version:      version,
    }, nil
}

// Load configuration from file
func loadConfig() (Config, error) {
    configPath := "config.yml"
    defaultConfig := Config{
        APIKey:           "your-secret-key",
        BindAddress:      "0.0.0.0",
        BindPort:         8080,
        VolumesDirectory: "./volumes",
        AppURL:           "http://localhost:3000",
        CorsOrigin:       "http://localhost:5173",
    }

    // If config file doesn't exist, create it with default values
    if _, err := os.Stat(configPath); os.IsNotExist(err) {
        configData, err := yaml.Marshal(defaultConfig)
        if err != nil {
            return defaultConfig, err
        }

        if err := ioutil.WriteFile(configPath, configData, 0644); err != nil {
            return defaultConfig, err
        }

        return defaultConfig, nil
    }

    // Read and parse config file
    configData, err := ioutil.ReadFile(configPath)
    if err != nil {
        return defaultConfig, err
    }

    var config Config
    if err := yaml.Unmarshal(configData, &config); err != nil {
        return defaultConfig, err
    }

    return config, nil
}

// Get all servers
func (app *AppState) getAllServers() ([]Server, error) {
    files, err := ioutil.ReadDir(app.ServersDir)
    if err != nil {
        return nil, err
    }

    var servers []Server
    for _, file := range files {
        if !strings.HasSuffix(file.Name(), ".json") {
            continue
        }

        filePath := filepath.Join(app.ServersDir, file.Name())
        data, err := ioutil.ReadFile(filePath)
        if err != nil {
            log.Printf("Warning: Failed to read server file %s: %v", filePath, err)
            continue
        }

        var server Server
        if err := json.Unmarshal(data, &server); err != nil {
            log.Printf("Warning: Failed to parse server file %s: %v", filePath, err)
            continue
        }

        // Update server status from Docker if it has a container
        if server.DockerID != "" {
            container, err := app.DockerClient.ContainerInspect(context.Background(), server.DockerID)
            if err == nil {
                server.State = determineServerState(container.State)
            } else {
                server.State = ServerStateErrored
            }
        }

        servers = append(servers, server)
    }

    return servers, nil
}

// Get a specific server by ID
func (app *AppState) getServer(id string) (*Server, error) {
    filePath := filepath.Join(app.ServersDir, id+".json")
    data, err := ioutil.ReadFile(filePath)
    if err != nil {
        if os.IsNotExist(err) {
            return nil, fmt.Errorf("server not found")
        }
        return nil, err
    }

    var server Server
    if err := json.Unmarshal(data, &server); err != nil {
        return nil, err
    }

    // Update server status from Docker if it has a container
    if server.DockerID != "" {
        container, err := app.DockerClient.ContainerInspect(context.Background(), server.DockerID)
        if err == nil {
            server.State = determineServerState(container.State)
        } else {
            server.State = ServerStateErrored
        }
    }

    return &server, nil
}

// Save a server to disk
func (app *AppState) saveServer(server *Server) error {
    data, err := json.MarshalIndent(server, "", "  ")
    if err != nil {
        return err
    }

    filePath := filepath.Join(app.ServersDir, server.ID+".json")
    return ioutil.WriteFile(filePath, data, 0644)
}

// Delete a server from disk
func (app *AppState) deleteServer(id string) error {
    filePath := filepath.Join(app.ServersDir, id+".json")
    return os.Remove(filePath)
}

// Determine server state from Docker container state
func determineServerState(state *types.ContainerState) ServerState {
    if state.Running {
        return ServerStateRunning
    } else if state.Dead {
        return ServerStateErrored
    } else if state.Restarting {
        return ServerStateStarting
    } else if state.Paused {
        return ServerStateStopped
    } else {
        return ServerStateStopped
    }
}

// Broadcast WebSocket message to all clients
func (app *AppState) broadcastMessage(event string, data interface{}) {
    message := WSMessage{
        Event: event,
        Data:  data,
    }
    app.WSBroadcast <- message
}

// WebSocket handler
func wsHandler(app *AppState) gin.HandlerFunc {
    upgrader := websocket.Upgrader{
        CheckOrigin: func(r *http.Request) bool {
            return true
        },
    }

    return func(c *gin.Context) {
        conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
        if err != nil {
            log.Printf("Failed to upgrade connection to WebSocket: %v", err)
            return
        }
        defer conn.Close()

        // Register client
        app.WSClients[conn] = true

        // Send welcome message
        welcomeMsg := WSMessage{
            Event: "daemon_message",
            Data:  fmt.Sprintf("Connected to Krypton Daemon v%s", app.Version),
        }
        conn.WriteJSON(welcomeMsg)

        // Handle messages
        for {
            messageType, _, err := conn.ReadMessage()
            if err != nil {
                delete(app.WSClients, conn)
                break
            }

            if messageType == websocket.CloseMessage {
                delete(app.WSClients, conn)
                break
            }
        }
    }
}

// Run the WebSocket broadcaster
func (app *AppState) runWSBroadcaster() {
    for {
        message := <-app.WSBroadcast
        for client := range app.WSClients {
            if err := client.WriteJSON(message); err != nil {
                log.Printf("WebSocket error: %v", err)
                client.Close()
                delete(app.WSClients, client)
            }
        }
    }
}

// API key middleware
func apiKeyMiddleware(app *AppState) gin.HandlerFunc {
    return func(c *gin.Context) {
        key := c.GetHeader("X-API-Key")
        if key != app.Config.APIKey {
            c.AbortWithStatusJSON(401, gin.H{"error": "Invalid API key"})
            return
        }
        c.Next()
    }
}

// Create a new server
func createServer(app *AppState) gin.HandlerFunc {
    return func(c *gin.Context) {
        var req struct {
            ServerID        string     `json:"serverId"`
            ValidationToken string     `json:"validationToken"`
            Name            string     `json:"name"`
            MemoryLimit     int        `json:"memoryLimit"`
            CPULimit        int        `json:"cpuLimit"`
            Allocation      Allocation `json:"allocation"`
        }

        if err := c.BindJSON(&req); err != nil {
            c.JSON(400, gin.H{"error": "Invalid request body"})
            return
        }

        // Check if server already exists
        if _, err := app.getServer(req.ServerID); err == nil {
            c.JSON(409, gin.H{"error": "Server already exists"})
            return
        }

        // Create new server
        server := Server{
            ID:          req.ServerID,
            Name:        req.Name,
            State:       ServerStateCreating,
            MemoryLimit: req.MemoryLimit,
            CPULimit:    req.CPULimit,
            Allocation:  req.Allocation,
        }

        // Save server to disk
        if err := app.saveServer(&server); err != nil {
            c.JSON(500, gin.H{"error": "Failed to save server"})
            return
        }

        // Return success with validation token
        c.JSON(201, gin.H{
            "id":              req.ServerID,
            "name":            req.Name,
            "state":           ServerStateCreating,
            "validationToken": req.ValidationToken,
        })

        // In a real implementation, you would start the installation process here
        // This would be done in a goroutine to not block the response
    }
}


/**
    * Krypton-GO router
    * This file contains the router setup and API endpoints for the Krypton server.
    * All routes for the daemon are present here alr
**/

// setupRouter creates and configures the Gin router
func setupRouter(app *AppState) *gin.Engine {
    router := gin.Default()

    // Configure CORS
    router.Use(func(c *gin.Context) {
        c.Writer.Header().Set("Access-Control-Allow-Origin", app.Config.CorsOrigin)
        c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
        c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
        c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")

        if c.Request.Method == "OPTIONS" {
            c.AbortWithStatus(204)
            return
        }

        c.Next()
    })

    // Set up WebSocket endpoint - using our new WebSocket manager
    //router.GET("/ws", app.WebSocketManager.handleConnection)

    // Set up API endpoints
    api := router.Group("/api/v1")
    
    // Servers API
    servers := api.Group("/servers")
    servers.Use(apiKeyMiddleware(app))
    servers.POST("/", createServer(app))
    servers.GET("/", getServers(app))
    servers.GET("/:id", getServerByID(app))
    
    // Add filesystem routes
    configureFilesystemRouter(app, router)
    
    // Add state routes
    configureStateRouter(app, router)
    
    return router
}

// Get all servers handler
func getServers(app *AppState) gin.HandlerFunc {
    return func(c *gin.Context) {
        servers, err := app.getAllServers()
        if err != nil {
            c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to get servers: %v", err)})
            return
        }
        c.JSON(200, servers)
    }
}

// Get server by ID handler
func getServerByID(app *AppState) gin.HandlerFunc {
    return func(c *gin.Context) {
        id := c.Param("id")
        server, err := app.getServer(id)
        if err != nil {
            c.JSON(404, gin.H{"error": fmt.Sprintf("Server not found: %v", err)})
            return
        }
        c.JSON(200, server)
    }
}