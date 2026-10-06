package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/seppaleinen/idle-deck/test/stubs"
)

func main() {
	repo := "acme/widgets"
	t0 := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)

	// Tracker stub
	tState := stubs.NewTrackerState(repo)
	tServer := stubs.NewTrackerServer(tState)
	defer tServer.Close()

	// Seed some initial data
	tState.UpsertIssue(&stubs.GitHubIssue{
		Number:    412,
		Title:     "Add caching layer",
		Body:      "We need Redis-backed caching.",
		CreatedAt: t0,
		UpdatedAt: t0,
		User:      stubs.GitHubUser{Type: "User"},
	})
	tState.AdvanceWatermark(t0.Add(-time.Hour))

	// Harness stub
	hState := stubs.NewHarnessState("stub-gpt-4o")
	hServer := stubs.NewHarnessServer(hState)
	defer hServer.Close()

	// Print URLs for the user
	fmt.Fprintf(os.Stderr, "Tracker stub: %s\n", tServer.URL())
	fmt.Fprintf(os.Stderr, "Harness stub: %s\n", hServer.URL())

	// Wait for interrupt
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	<-ctx.Done()
	fmt.Fprintln(os.Stderr, "Shutting down stubs...")
}
