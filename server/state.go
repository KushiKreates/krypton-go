package server

import (
    "context"
    "net/http"
    "runtime"
    "time"

    _"github.com/docker/docker/api/types"
    "github.com/docker/docker/api/types/container"
   _ "github.com/docker/docker/api/types/filters"
    "github.com/gin-gonic/gin"
    "github.com/shirou/gopsutil/cpu"
    "github.com/shirou/gopsutil/host"
    "github.com/shirou/gopsutil/mem"
)

// SystemState represents the system state information
type SystemState struct {
    Version     string         `json:"version"`
    Kernel      string         `json:"kernel"`
    OsVersion   string         `json:"osVersion"`
    Hostname    string         `json:"hostname"`
    CpuCores    int            `json:"cpuCores"`
    MemoryTotal uint64         `json:"memoryTotal"`
    Containers  ContainerStats `json:"containers"`
}

// ContainerStats represents container statistics
type ContainerStats struct {
    Total   int `json:"total"`
    Running int `json:"running"`
    Stopped int `json:"stopped"`
}

// Cache for system state information
var (
    cachedSystemState *SystemState
    cacheTimestamp    time.Time
    cacheDuration     = 10 * time.Minute
)

// Configure state router
func configureStateRouter(app *AppState, router *gin.Engine) {
    r := router.Group("/api/v1/state")
    r.Use(apiKeyMiddleware(app))

    r.GET("/", func(c *gin.Context) {
        // Check if we have a valid cache
        if cachedSystemState != nil && time.Since(cacheTimestamp) < cacheDuration {
            c.JSON(http.StatusOK, cachedSystemState)
            return
        }

        // Get system information
        hostInfo, err := host.Info()
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get system information"})
            return
        }

        // Get memory information
        memory, err := mem.VirtualMemory()
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get memory information"})
            return
        }

        // Get CPU information
        cpuInfo, err := cpu.Info()
        var cpuCores int
        if err != nil {
            cpuCores = runtime.NumCPU()
        } else {
            cpuCores = len(cpuInfo)
        }

        // Get container counts
        ctx := context.Background()
        // Get all containers
        listOptions := container.ListOptions{
            All: true,
        }
        
        
        containers, err := app.DockerClient.ContainerList(ctx, listOptions)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get container information"})
            return
        }

        running := 0
        for _, container := range containers {
            if container.State == "running" {
                running++
            }
        }

        // Create system state
        systemState := SystemState{
            Version:     app.Version,
            Kernel:      hostInfo.KernelVersion,
            OsVersion:   hostInfo.Platform + " " + hostInfo.PlatformVersion,
            Hostname:    hostInfo.Hostname,
            CpuCores:    cpuCores,
            MemoryTotal: memory.Total,
            Containers: ContainerStats{
                Total:   len(containers),
                Running: running,
                Stopped: len(containers) - running,
            },
        }

        // Cache the result
        cachedSystemState = &systemState
        cacheTimestamp = time.Now()

        c.JSON(http.StatusOK, systemState)
    })
}