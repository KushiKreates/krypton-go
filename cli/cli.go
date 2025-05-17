package cli

import (
    "fmt"
    "os"
    "os/signal"
    "path/filepath"
    "strings"
    "syscall"
    "bufio"
    "strconv"
    "time"

    "github.com/spf13/cobra"
    "gopkg.in/yaml.v3"
)

// KryptonCLI represents the main CLI structure
type KryptonCLI struct {
    rootCmd     *cobra.Command
    cacheDir    string
    configPath  string
    version     string
    isDevelopment bool
}

// NewCLI creates a new CLI instance
func NewCLI(version string) *KryptonCLI {
    cli := &KryptonCLI{
        version:     version,
        cacheDir:    "./cache",
        configPath:  "./config.yml",
        isDevelopment: false,
    }

    // Initialize root command
    cli.rootCmd = &cobra.Command{
        Use:     "krypton",
        Short:   "Krypton - Argon's server control daemon",
        Version: version,
        Long: `Krypton is Argon's server control daemon built for the modern times 
of the hosting industry and designed to be performant and secure.`,
        Run: func(cmd *cobra.Command, args []string) {
            // Process flags in order of priority
            
            // Check if cache create flag was used
            if create, _ := cmd.Flags().GetBool("create-cache"); create {
                cli.createCacheDir()
                return
            }
            
            // Check if cache clear flag was used
            if clear, _ := cmd.Flags().GetBool("clear-cache"); clear {
                cli.clearCache()
                return
            }
            
            // Check if configure flag was used
            if configure, _ := cmd.Flags().GetBool("configure"); configure {
                cli.runConfigureWizard()
                return
            }
            
            // If no flags are provided, start the server
            cli.startServer()
        },
    }

    // Add global flags
    cli.rootCmd.PersistentFlags().StringVar(&cli.configPath, "config", "./config.yml", "Path to configuration file")
    cli.rootCmd.PersistentFlags().BoolVar(&cli.isDevelopment, "dev", false, "Run in development mode")
    
    // Add operation flags
    cli.rootCmd.Flags().Bool("create-cache", false, "Create cache directory")
    cli.rootCmd.Flags().Bool("clear-cache", false, "Clear cache directory")
    cli.rootCmd.Flags().Bool("configure", false, "Run configuration wizard")
    
    return cli


}

/**
 * Execute runs the root command
 * This function is the entry point for the CLI application.
 * This is where all CLI commands are registered and taken in @
 **/
func (cli *KryptonCLI) Execute() error {
    return cli.rootCmd.Execute()
}

// Add cache-related commands
func (cli *KryptonCLI) addCacheCommands() {
    cacheCmd := &cobra.Command{
        Use:   "cache",
        Short: "Manage Krypton cache",
        Long:  "Create, clear, or inspect the Krypton cache directory",
    }

    createCacheCmd := &cobra.Command{
        Use:   "create",
        Short: "Create cache directory",
        Run: func(cmd *cobra.Command, args []string) {
            cli.createCacheDir()
        },
    }

    clearCacheCmd := &cobra.Command{
        Use:   "clear",
        Short: "Clear cache directory",
        Run: func(cmd *cobra.Command, args []string) {
            cli.clearCache()
        },
    }

    // Add subcommands to cache command
    cacheCmd.AddCommand(createCacheCmd, clearCacheCmd)
    
    // Add cache command to root
    cli.rootCmd.AddCommand(cacheCmd)
}

// Add configuration-related commands
func (cli *KryptonCLI) addConfigCommands() {
    configCmd := &cobra.Command{
        Use:   "configure",
        Short: "Configure Krypton",
        Long:  "Create or update Krypton configuration",
        Run: func(cmd *cobra.Command, args []string) {
            cli.runConfigureWizard()
        },
    }
    
    cli.rootCmd.AddCommand(configCmd)
}

// Helper function to create cache directory
func (cli *KryptonCLI) createCacheDir() {
    if err := os.MkdirAll(cli.cacheDir, 0755); err != nil {
        fmt.Printf("❌ Error creating cache directory: %v\n", err)
        return
    }
    
    // Create subdirectories
    directories := []string{"temp", "downloads", "uploads", "extractions"}
    for _, dir := range directories {
        dirPath := filepath.Join(cli.cacheDir, dir)
        if err := os.MkdirAll(dirPath, 0755); err != nil {
            fmt.Printf("❌ Error creating directory %s: %v\n", dirPath, err)
            return
        }
    }
    
    fmt.Println("✅ Cache directories created successfully!")
    fmt.Printf("📁 Cache location: %s\n", cli.cacheDir)
}

// Helper function to clear cache
func (cli *KryptonCLI) clearCache() {
    // Check if cache directory exists
    if _, err := os.Stat(cli.cacheDir); os.IsNotExist(err) {
        fmt.Println("❌ Cache directory doesn't exist.")
        return
    }

    // Remove contents but keep the directory structure
    directories := []string{"loaders", "retarded"}
    for _, dir := range directories {
        dirPath := filepath.Join(cli.cacheDir, dir)
        
        // Skip if directory doesn't exist
        if _, err := os.Stat(dirPath); os.IsNotExist(err) {
            continue
        }
        
        // Read directory contents
        entries, err := os.ReadDir(dirPath)
        if err != nil {
            fmt.Printf("❌ Error reading directory %s: %v\n", dirPath, err)
            continue
        }
        
        // Delete each entry
        for _, entry := range entries {
            entryPath := filepath.Join(dirPath, entry.Name())
            if err := os.RemoveAll(entryPath); err != nil {
                fmt.Printf("❌ Error removing %s: %v\n", entryPath, err)
            }
        }
    }
    
    fmt.Println("✅ Cache cleared successfully!")
}

// Run configuration wizard
func (cli *KryptonCLI) runConfigureWizard() {
    fmt.Println("\n\033[1m\033[34mKrypton Configuration Wizard\033[0m")
    fmt.Println("\033[36mLet's set up your Krypton instance together.\033[0m\n")

    reader := bufio.NewReader(os.Stdin)

    // Helper function to get input with default value
    getInput := func(question, defaultValue string) string {
        fmt.Printf("\033[36m? \033[0m%s \033[2m[%s]\033[0m: ", question, defaultValue)
        input, _ := reader.ReadString('\n')
        input = strings.TrimSpace(input)
        if input == "" {
            return defaultValue
        }
        return input
    }

    // Get panel URL
    appURL := getInput("What's your panel's URL", "https://panel.example.io")

    // Confirm node setup
    nodeConfirmed := getInput("Please confirm that you've made a node that points towards this instance of Krypton", "y")
    if strings.ToLower(nodeConfirmed) != "y" {
        fmt.Println("\033[31m✗ Please set up a node in your panel before continuing.\033[0m")
        return
    }

    // Get port
    bindPortStr := getInput("What port did you choose?", "8080")
    bindPort, err := strconv.Atoi(bindPortStr)
    if err != nil {
        fmt.Println("\033[31m✗ Invalid port number.\033[0m")
        return
    }

    // Get connection key
    fmt.Print("\n\033[2mSecurity Configuration\033[0m\n")
    fmt.Print("\033[36m? \033[0mWhat's the connection key? \033[2m(Node -> Configure)\033[0m: ")
    apiKey, _ := reader.ReadString('\n')
    apiKey = strings.TrimSpace(apiKey)
    if apiKey == "" {
        fmt.Println("\033[31m✗ Connection key is required.\033[0m")
        return
    }

    // Check for additional CORS origins
    needsAdditionalCors := getInput("Will this instance be accessed from anywhere else other than your panel?", "n")
    
    corsOrigins := []string{appURL}
    if strings.ToLower(needsAdditionalCors) == "y" {
        fmt.Println("\n\033[1mEnter additional CORS origins (one per line, press Enter twice to finish):\033[0m")
        
        for {
            fmt.Print("\033[2m> \033[0m")
            input, _ := reader.ReadString('\n')
            input = strings.TrimSpace(input)
            
            if input == "" {
                break
            }
            
            corsOrigins = append(corsOrigins, input)
        }
    }

    // Create config object
    config := map[string]interface{}{
        "apiKey":           apiKey,
        "bindAddress":      "0.0.0.0",
        "bindPort":         bindPort,
        "volumesDirectory": "./volumes",
        "appUrl":           appURL,
        "corsOrigin":       strings.Join(corsOrigins, ","),
    }

    // Create warning header
    warningHeader := fmt.Sprintf(`# WARNING: AUTOMATICALLY GENERATED CONFIGURATION FILE
# 
# This configuration file was automatically generated by Argon.
# Manual modifications to this file can break your Krypton instance and may result in errors.
# Powered by Nadhi.dev and the Argon team.
#
# Only modify this file if you:
# 1. Fully understand the consequences of your changes
# 2. Have a backup of the original configuration
# 3. Know how to troubleshoot issues that *will* arise if you misconfigure something
#
# For support and documentation, visit the Argon GitHub repository.
#
# Generated on: %s for Krypton version 1.x
#
`, time.Now().Format(time.RFC3339))

    // Display config
    fmt.Println("\n\033[2mConfiguration Preview\033[0m")
    
    // Convert config to YAML
    configYaml, err := yaml.Marshal(config)
    if err != nil {
        fmt.Printf("\033[31m✗ Error generating YAML: %v\033[0m\n", err)
        return
    }
    
    fmt.Println(warningHeader + string(configYaml))

    // Confirm configuration
    confirmed := getInput("Are we all set?", "y")
    if strings.ToLower(confirmed) != "y" {
        fmt.Println("\033[33m⚠ Starting over...\033[0m")
        return
    }

    // Write config file
    err = os.WriteFile(cli.configPath, []byte(warningHeader+string(configYaml)), 0644)
    if err != nil {
        fmt.Printf("\033[31m✗ Error writing configuration file: %v\033[0m\n", err)
        return
    }

    fmt.Println("\n\033[32m✓ Configuration saved successfully!\033[0m")
    fmt.Println("\n\033[36mNext steps:\033[0m")
    fmt.Println("\033[1m1. Start your Krypton instance:\033[0m")
    fmt.Println("   \033[2m$\033[0m go run main.go")
    fmt.Println("\033[1m2. For background operation, check our documentation\033[0m")
    fmt.Println("\n\033[2mThank you for using Argon!\033[0m")
}

/**
 * Start the Krypton server
 * This function initializes the server and starts listening for requests.
 * It checks for the existence of the configuration file and cache directory.

 */


func (cli *KryptonCLI) startServer() {
    fmt.Println("\033[1m\033[34mStarting Krypton Server\033[0m")
    fmt.Printf("\033[36mVersion: %s\033[0m\n", cli.version)
    
    // Check if configuration file exists
    if _, err := os.Stat(cli.configPath); os.IsNotExist(err) {
        fmt.Printf("\033[31m✗ Configuration file not found: %s\033[0m\n", cli.configPath)
        fmt.Println("\033[33m⚠ Run 'krypton --configure' to create a configuration file\033[0m")
        return
    }
    
    // Ensure cache directory exists
    if _, err := os.Stat(cli.cacheDir); os.IsNotExist(err) {
        fmt.Println("\033[33m⚠ Cache directory not found, creating it now...\033[0m")
        cli.createCacheDir()
    }
    
    fmt.Println("\033[32m✓ Configuration loaded\033[0m")
    fmt.Printf("\033[36mConfig path: %s\033[0m\n", cli.configPath)
    fmt.Printf("\033[36mCache directory: %s\033[0m\n", cli.cacheDir)
    
    if cli.isDevelopment {
        fmt.Println("\033[33m⚠ Running in development mode\033[0m")
    }
    
    // later actually start the websockets and etc
    fmt.Println("\033[2mPress Ctrl+C to stop the server...\033[0m")
    
    // Keep the process running until interrupted
    c := make(chan os.Signal, 1)
    signal.Notify(c, os.Interrupt, syscall.SIGTERM)
    <-c
    
    fmt.Println("\n\033[33m⚠ Shutting down server...\033[0m")
}

// For easy access in main.go
func Execute(version string) error {
    cli := NewCLI(version)
    return cli.Execute()
}