package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/api"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/config"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func run() error {
	c, err := config.Parse(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	repo, err := persistence.Open(c.DataDir)
	if err != nil {
		return err
	}
	defer repo.Close()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	handler := api.New(catalog.New(repo), logger)
	listener, err := net.Listen("tcp", c.Addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	failed := make(chan error, 1)
	go func() { failed <- server.Serve(listener) }()
	fmt.Printf("STRATA_LISTEN=%s\n", listener.Addr().String())
	select {
	case err = <-failed:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), c.ShutdownTimeout)
		defer cancel()
		if err = server.Shutdown(shutdown); err != nil {
			server.Close()
			return err
		}
		err = <-failed
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
