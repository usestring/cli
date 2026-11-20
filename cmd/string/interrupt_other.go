//go:build !unix

package main

import "os"

// endInterrupted ends string with 130, where there is no SIGINT to raise again.
func endInterrupted() {
	os.Exit(exitInterrupted)
}
