//go:build manual

// Command detect tries feature detection and the terminfo lookup against the
// terminal it runs in. It is a manual harness, excluded from normal builds:
//
//	go run -tags manual ./internal/manual/detect
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/adamvduke/pst"
)

func main() {
	fmt.Printf("TERM=%s  stdout is a terminal: %v  inside tmux: %v\n",
		os.Getenv("TERM"), pst.IsTerminal(os.Stdout), pst.InTmux())

	ok, err := pst.TerminfoHasPst("")
	fmt.Printf("terminfo Pst: %v (err: %v)\n", ok, err)

	for _, timeout := range []time.Duration{200 * time.Millisecond, time.Second} {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		start := time.Now()
		s, err := pst.Detect(ctx, nil)
		cancel()
		fmt.Printf("Detect (timeout %v): %v in %v (err: %v)\n", timeout, s, time.Since(start).Round(time.Millisecond), err)
	}

	st := pst.NewForFile(os.Stdout, pst.WithApp("pst-manual"))
	st.Working("Manual detection harness")
	time.Sleep(time.Second)
	st.Done("Manual detection harness finished")
	fmt.Println("sent working, then done; check your terminal's status UI")
}
