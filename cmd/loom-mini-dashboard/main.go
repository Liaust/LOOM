package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	loomconfig "loom.local/loom/internal/config"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/minidashboard"
)

func main() {
	cfg := minidashboard.DefaultConfig()
	listen := flag.String("listen", cfg.ListenAddress, "loopback listen address")
	state := flag.String("state", cfg.StatePath, "last-good cache path")
	socket := flag.String("loomd-socket", loomconfig.DefaultSocketPath, "loomd Unix socket")
	timeout := flag.Duration("loomd-timeout", cfg.RequestTimeout, "per-request loomd timeout")
	data := flag.String("data-dir", loomconfig.DefaultDataDir, "LOOM data filesystem to measure")
	flag.Parse()
	cfg.ListenAddress, cfg.StatePath, cfg.RequestTimeout = *listen, *state, *timeout
	client := localclient.New(*socket)
	app := minidashboard.App{Config: cfg, Collector: minidashboard.NewHostCollector(*data), Poller: &minidashboard.DomainPoller{Client: client, Cache: minidashboard.DomainCache{Path: cfg.StatePath}, RequestTimeout: *timeout}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
