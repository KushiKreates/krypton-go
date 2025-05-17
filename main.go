package main

import (
    "fmt"
    "os"

    "nadhi.dev/krypton/v2/cli"
    "nadhi.dev/krypton/v2/server"
)

const VERSION = "1.0.0"

func main() {
    // Process CLI commands
    if len(os.Args) > 1 {
        if err := cli.Execute(VERSION); err != nil {
            fmt.Println(err)
            os.Exit(1)
        }
        return
    }

    // If no CLI arguments, start the server
    if err := server.Start(VERSION); err != nil {
        fmt.Println(err)
        os.Exit(1)
    }
}