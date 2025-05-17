package main

import (
    "fmt"
    "os"

    "nadhi.dev/krypton/v2/cli"
)

const VERSION = "1.0.0"

func main() {
    if err := cli.Execute(VERSION); err != nil {
        fmt.Println(err)
        os.Exit(1)
    }
}