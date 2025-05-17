package server

/**
 * Krypton's File system router
 * This file contains the API endpoints for file system operations.
 * It includes functions for listing files, getting file contents :D

 **/

import (
    "archive/zip"
    _"encoding/json"
    "fmt"
    "io"
    "mime"
    "net/http"
    "os"
    "path/filepath"
    "strings"
    "time"

    "github.com/gin-gonic/gin"
    "github.com/google/uuid"
)

// FileStats represents file information
type FileStats struct {
    Name             string                 `json:"name"`
    Mode             string                 `json:"mode"`
    Size             int64                  `json:"size"`
    IsFile           bool                   `json:"isFile"`
    IsSymlink        bool                   `json:"isSymlink"`
    ModifiedAt       int64                  `json:"modifiedAt"`
    CreatedAt        int64                  `json:"createdAt"`
    Mime             string                 `json:"mime"`
    Hidden           bool                   `json:"hidden,omitempty"`
    Readonly         bool                   `json:"readonly,omitempty"`
    NoDelete         bool                   `json:"noDelete,omitempty"`
    IsCargoFile      bool                   `json:"isCargoFile,omitempty"`
    CustomProperties map[string]interface{} `json:"customProperties,omitempty"`
}

// configureFilesystemRouter sets up the filesystem API routes
// configureFilesystemRouter sets up the filesystem API routes
func configureFilesystemRouter(app *AppState, router *gin.Engine) {
    fs := router.Group("/api/v1/servers/:server/files")
    fs.Use(apiKeyMiddleware(app))

    // List files in directory
    fs.GET("/list/*path", listFiles(app))

    // Get file contents
    fs.GET("/contents/*path", getFileContents(app))

    // Write file contents
    fs.POST("/write/*path", writeFileContents(app))

    // Create directory
    fs.POST("/create-directory/*path", createDirectory(app))

    // Rename a file or directory
    fs.PUT("/rename", renameFile(app))

    // Delete files or directories
    fs.DELETE("/delete", deleteFiles(app))

    // Upload file
    fs.POST("/upload", uploadFile(app))

    // Download files as archive
    fs.GET("/download", downloadFiles(app))

    // Compress files into an archive
    fs.POST("/compress", compressFiles(app))

    // Extract an archive
    fs.POST("/extract", extractArchive(app))
}

// getServerVolumePath returns the path to the server's volume
func getServerVolumePath(app *AppState, serverId string) (string, error) {
    server, err := app.getServer(serverId)
	fmt.Println(server)
    if err != nil {
        return "", fmt.Errorf("server not found: %w", err)
    }

    return filepath.Join(app.Config.VolumesDirectory, serverId), nil
}

// validateFilePath ensures the path is within the server's volume
func validateFilePath(volumePath, requestedPath string) (string, error) {
    // Clean the path to prevent directory traversal
    requestedPath = filepath.Clean(strings.TrimPrefix(requestedPath, "/"))
    
    // Join with volume path
    fullPath := filepath.Join(volumePath, requestedPath)
    
    // Ensure the path is still within the volume
    rel, err := filepath.Rel(volumePath, fullPath)
    if err != nil || strings.HasPrefix(rel, "..") {
        return "", fmt.Errorf("invalid path: path traversal attempt")
    }
    
    return fullPath, nil
}

// getFileStats returns stats for a file or directory
func getFileStats(path string, relativeTo string) (FileStats, error) {
    info, err := os.Stat(path)
    if err != nil {
        return FileStats{}, err
    }

    // Get relative path for name
    name := filepath.Base(path)
    if relativeTo != "" {
        if rel, err := filepath.Rel(relativeTo, path); err == nil {
            name = rel
        }
    }

	

    // Determine mime type for files
    mimeType := "inode/directory"
    if info.Mode().IsRegular() {
        mimeType = mime.TypeByExtension(filepath.Ext(path))
        if mimeType == "" {
            mimeType = "application/octet-stream"
        }
    }

    // Get file creation time (not available in all OS/filesystems)
    createdAt := info.ModTime().Unix()

    return FileStats{
        Name:       name,
        Mode:       info.Mode().String(),
        Size:       info.Size(),
        IsFile:     info.Mode().IsRegular(),
        IsSymlink:  info.Mode()&os.ModeSymlink != 0,
        ModifiedAt: info.ModTime().Unix(),
        CreatedAt:  createdAt,
        Mime:       mimeType,
        Hidden:     strings.HasPrefix(filepath.Base(path), "."),
    }, nil
}

// listFiles handles the directory listing endpoint
func listFiles(app *AppState) gin.HandlerFunc {
    return func(c *gin.Context) {
        serverId := c.Param("server")
        requestedPath := c.Param("path")

        volumePath, err := getServerVolumePath(app, serverId)
        if err != nil {
            c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
            return
        }

        fullPath, err := validateFilePath(volumePath, requestedPath)
        if err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
            return
        }

        // Check if path exists
        info, err := os.Stat(fullPath)
        if err != nil {
            if os.IsNotExist(err) {
                c.JSON(http.StatusNotFound, gin.H{"error": "Path does not exist"})
            } else {
                c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
            }
            return
        }

        // If it's a file, return its stats
        if !info.IsDir() {
            stats, err := getFileStats(fullPath, filepath.Dir(fullPath))
            if err != nil {
                c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
                return
            }
            c.JSON(http.StatusOK, []FileStats{stats})
            return
        }

        // For directories, read contents and return stats for each item
        files, err := os.ReadDir(fullPath)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
            return
        }

        var fileStats []FileStats
        for _, file := range files {
            filePath := filepath.Join(fullPath, file.Name())
            stats, err := getFileStats(filePath, fullPath)
            if err != nil {
                continue // Skip files that can't be stat'd
            }
            fileStats = append(fileStats, stats)
        }

        c.JSON(http.StatusOK, fileStats)
    }
}

// getFileContents handles retrieving file contents
func getFileContents(app *AppState) gin.HandlerFunc {
    return func(c *gin.Context) {
        serverId := c.Param("server")
        requestedPath := c.Param("path")

        volumePath, err := getServerVolumePath(app, serverId)
        if err != nil {
            c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
            return
        }

        fullPath, err := validateFilePath(volumePath, requestedPath)
        if err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
            return
        }

        // Check if path exists and is a file
        info, err := os.Stat(fullPath)
        if err != nil {
            if os.IsNotExist(err) {
                c.JSON(http.StatusNotFound, gin.H{"error": "File does not exist"})
            } else {
                c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
            }
            return
        }

        if info.IsDir() {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Path is a directory, not a file"})
            return
        }

        // Read file contents
        content, err := os.ReadFile(fullPath)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
            return
        }

        c.String(http.StatusOK, string(content))
    }
}

// writeFileContents handles writing to a file
func writeFileContents(app *AppState) gin.HandlerFunc {
    return func(c *gin.Context) {
        serverId := c.Param("server")
        requestedPath := c.Param("path")

        volumePath, err := getServerVolumePath(app, serverId)
        if err != nil {
            c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
            return
        }

        fullPath, err := validateFilePath(volumePath, requestedPath)
        if err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
            return
        }

        // Get the request body (file contents)
        content, err := io.ReadAll(c.Request.Body)
        if err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read request body"})
            return
        }

        // Create parent directory if it doesn't exist
        dir := filepath.Dir(fullPath)
        if err := os.MkdirAll(dir, 0755); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create directory"})
            return
        }

        // Write the file
        if err := os.WriteFile(fullPath, content, 0644); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
            return
        }

        c.JSON(http.StatusOK, gin.H{"success": true})
    }
}

// createDirectory handles directory creation
func createDirectory(app *AppState) gin.HandlerFunc {
    return func(c *gin.Context) {
        serverId := c.Param("server")
        requestedPath := c.Param("path")

        volumePath, err := getServerVolumePath(app, serverId)
        if err != nil {
            c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
            return
        }

        fullPath, err := validateFilePath(volumePath, requestedPath)
        if err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
            return
        }

        // Create the directory
        if err := os.MkdirAll(fullPath, 0755); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
            return
        }

        c.JSON(http.StatusOK, gin.H{"success": true})
    }
}

// renameFile handles file/directory renaming
func renameFile(app *AppState) gin.HandlerFunc {
    return func(c *gin.Context) {
        var req struct {
            From string `json:"from"`
            To   string `json:"to"`
        }

        if err := c.BindJSON(&req); err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
            return
        }

        serverId := c.Param("server")
        volumePath, err := getServerVolumePath(app, serverId)
        if err != nil {
            c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
            return
        }

        fromPath, err := validateFilePath(volumePath, req.From)
        if err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid source path"})
            return
        }

        toPath, err := validateFilePath(volumePath, req.To)
        if err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid destination path"})
            return
        }

        // Check if source exists
        if _, err := os.Stat(fromPath); os.IsNotExist(err) {
            c.JSON(http.StatusNotFound, gin.H{"error": "Source path does not exist"})
            return
        }

        // Create parent directory for destination if needed
        if err := os.MkdirAll(filepath.Dir(toPath), 0755); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create directory"})
            return
        }

        // Perform the rename
        if err := os.Rename(fromPath, toPath); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
            return
        }

        c.JSON(http.StatusOK, gin.H{"success": true})
    }
}

// deleteFiles handles file/directory deletion
func deleteFiles(app *AppState) gin.HandlerFunc {
    return func(c *gin.Context) {
        var req struct {
            Files []string `json:"files"`
        }

        if err := c.BindJSON(&req); err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
            return
        }

        serverId := c.Param("server")
        volumePath, err := getServerVolumePath(app, serverId)
        if err != nil {
            c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
            return
        }

        results := make(map[string]string)

        for _, file := range req.Files {
            fullPath, err := validateFilePath(volumePath, file)
            if err != nil {
                results[file] = err.Error()
                continue
            }

            if err := os.RemoveAll(fullPath); err != nil {
                results[file] = err.Error()
            } else {
                results[file] = "success"
            }
        }

        c.JSON(http.StatusOK, gin.H{"results": results})
    }
}

// uploadFile handles file uploads
func uploadFile(app *AppState) gin.HandlerFunc {
    return func(c *gin.Context) {
        serverId := c.Param("server")
        volumePath, err := getServerVolumePath(app, serverId)
        if err != nil {
            c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
            return
        }

        // Get destination directory from query parameters
        directory := c.DefaultQuery("directory", "/")
        
        // Create multipart form
        if err := c.Request.ParseMultipartForm(100 << 20); err != nil { // 100 MB limit
            c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to parse form"})
            return
        }

        files := c.Request.MultipartForm.File["files"]
        results := make([]string, 0)

        for _, fileHeader := range files {
            file, err := fileHeader.Open()
            if err != nil {
                c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open uploaded file"})
                return
            }
            defer file.Close()

            // Create destination path
            fullPath, err := validateFilePath(volumePath, filepath.Join(directory, fileHeader.Filename))
            if err != nil {
                c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
                return
            }

            // Create parent directory if needed
            if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
                c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create directory"})
                return
            }

            // Create the destination file
            dst, err := os.Create(fullPath)
            if err != nil {
                c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create destination file"})
                return
            }
            defer dst.Close()

            // Copy the file
            if _, err = io.Copy(dst, file); err != nil {
                c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file"})
                return
            }

            results = append(results, fileHeader.Filename)
        }

        c.JSON(http.StatusOK, gin.H{"success": true, "files": results})
    }
}

// downloadFiles handles downloading files as an archive
func downloadFiles(app *AppState) gin.HandlerFunc {
    return func(c *gin.Context) {
        serverId := c.Param("server")
        var req struct {
            Files []string `json:"files"`
        }

        if err := c.BindJSON(&req); err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
            return
        }

        volumePath, err := getServerVolumePath(app, serverId)
        if err != nil {
            c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
            return
        }

        // Create a temporary file for the zip
        tempFile := filepath.Join(os.TempDir(), uuid.New().String()+".zip")
        defer os.Remove(tempFile)

        // Create the zip file
        zipFile, err := os.Create(tempFile)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create archive"})
            return
        }
        defer zipFile.Close()

        // Create a zip writer
        zipWriter := zip.NewWriter(zipFile)
        defer zipWriter.Close()

        // Add each file to the zip
        for _, filePath := range req.Files {
            fullPath, err := validateFilePath(volumePath, filePath)
            if err != nil {
                continue // Skip invalid paths
            }

            info, err := os.Stat(fullPath)
            if err != nil {
                continue // Skip files that don't exist
            }

            if info.IsDir() {
                // For directories, add all files recursively
                if err := addDirectoryToZip(zipWriter, fullPath, filePath, volumePath); err != nil {
                    continue
                }
            } else {
                // For files, add them directly
                if err := addFileToZip(zipWriter, fullPath, filePath); err != nil {
                    continue
                }
            }
        }

        zipWriter.Close()
        zipFile.Close()

        // Serve the zip file
        c.FileAttachment(tempFile, fmt.Sprintf("server-files-%s.zip", time.Now().Format("20060102150405")))
    }
}

// addFileToZip adds a file to a zip archive
func addFileToZip(zipWriter *zip.Writer, fullPath, relativePath string) error {
    fileToZip, err := os.Open(fullPath)
    if err != nil {
        return err
    }
    defer fileToZip.Close()

    info, err := fileToZip.Stat()
    if err != nil {
        return err
    }

    header, err := zip.FileInfoHeader(info)
    if err != nil {
        return err
    }

    header.Name = relativePath
    header.Method = zip.Deflate

    writer, err := zipWriter.CreateHeader(header)
    if err != nil {
        return err
    }

    _, err = io.Copy(writer, fileToZip)
    return err
}

// addDirectoryToZip adds a directory and its contents to a zip archive
func addDirectoryToZip(zipWriter *zip.Writer, fullPath, relativePath, volumePath string) error {
    return filepath.Walk(fullPath, func(path string, info os.FileInfo, err error) error {
        if err != nil {
            return err
        }

        // Get relative path within the zip
        relPath, err := filepath.Rel(volumePath, path)
        if err != nil {
            return err
        }

        if info.IsDir() {
            // Add directory entry (with trailing slash)
            header, err := zip.FileInfoHeader(info)
            if err != nil {
                return err
            }
            header.Name = relPath + "/"
            _, err = zipWriter.CreateHeader(header)
            return err
        }

        // Add file
        return addFileToZip(zipWriter, path, relPath)
    })
}

// compressFiles handles compressing files into an archive
func compressFiles(app *AppState) gin.HandlerFunc {
    return func(c *gin.Context) {
        serverId := c.Param("server")
        var req struct {
            Files    []string `json:"files"`
            Archive  string   `json:"archive"`
        }

        if err := c.BindJSON(&req); err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
            return
        }

        volumePath, err := getServerVolumePath(app, serverId)
        if err != nil {
            c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
            return
        }

        archivePath, err := validateFilePath(volumePath, req.Archive)
        if err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid archive path"})
            return
        }

        // Create parent directory for archive if needed
        if err := os.MkdirAll(filepath.Dir(archivePath), 0755); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create directory"})
            return
        }

        // Create the zip file
        zipFile, err := os.Create(archivePath)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create archive"})
            return
        }
        defer zipFile.Close()

        // Create a zip writer
        zipWriter := zip.NewWriter(zipFile)
        defer zipWriter.Close()

        // Add each file to the zip
        for _, filePath := range req.Files {
            fullPath, err := validateFilePath(volumePath, filePath)
            if err != nil {
                continue // Skip invalid paths
            }

            info, err := os.Stat(fullPath)
            if err != nil {
                continue // Skip files that don't exist
            }

            if info.IsDir() {
                // For directories, add all files recursively
                if err := addDirectoryToZip(zipWriter, fullPath, filepath.Base(filePath), filepath.Dir(fullPath)); err != nil {
                    continue
                }
            } else {
                // For files, add them directly
                if err := addFileToZip(zipWriter, fullPath, filepath.Base(filePath)); err != nil {
                    continue
                }
            }
        }

        c.JSON(http.StatusOK, gin.H{"success": true})
    }
}

// extractArchive handles extracting an archive
func extractArchive(app *AppState) gin.HandlerFunc {
    return func(c *gin.Context) {
        serverId := c.Param("server")
        var req struct {
            Archive   string `json:"archive"`
            Directory string `json:"directory"`
        }

        if err := c.BindJSON(&req); err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
            return
        }

        volumePath, err := getServerVolumePath(app, serverId)
        if err != nil {
            c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
            return
        }

        archivePath, err := validateFilePath(volumePath, req.Archive)
        if err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid archive path"})
            return
        }

        extractPath, err := validateFilePath(volumePath, req.Directory)
        if err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid extraction path"})
            return
        }

        // Create extraction directory if it doesn't exist
        if err := os.MkdirAll(extractPath, 0755); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create extraction directory"})
            return
        }

        // Open the zip file
        reader, err := zip.OpenReader(archivePath)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open archive"})
            return
        }
        defer reader.Close()

        // Extract each file
        for _, file := range reader.File {
            // Ensure no path traversal
            filePath := filepath.Join(extractPath, file.Name)
            rel, err := filepath.Rel(extractPath, filePath)
            if err != nil || strings.HasPrefix(rel, "..") {
                // Skip files that would be extracted outside the target directory
                continue
            }

            if file.FileInfo().IsDir() {
                // Create directory
                if err := os.MkdirAll(filePath, 0755); err != nil {
                    continue
                }
            } else {
                // Create parent directory if needed
                if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
                    continue
                }

                // Extract file
                srcFile, err := file.Open()
                if err != nil {
                    continue
                }

                destFile, err := os.Create(filePath)
                if err != nil {
                    srcFile.Close()
                    continue
                }

                _, err = io.Copy(destFile, srcFile)
                destFile.Close()
                srcFile.Close()
                if err != nil {
                    continue
                }

                // Set file permissions
                if err := os.Chmod(filePath, 0644); err != nil {
                    continue
                }
            }
        }

        c.JSON(http.StatusOK, gin.H{"success": true})
    }
}