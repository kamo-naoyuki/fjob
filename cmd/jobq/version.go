package main

import "fmt"

var version = "dev"

func printVersion() {
	fmt.Printf("jobq %s\n", version)
}
