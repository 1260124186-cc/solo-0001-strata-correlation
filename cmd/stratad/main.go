package main

import (
	"context"
	"encoding/json"
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

// errDiagnosedUnstartable 表示诊断成功执行但结论为不可启动。
var errDiagnosedUnstartable = errors.New("snapshot diagnosed as unstartable")

func run() int {
	c, err := config.Parse(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if c.Diagnostic {
		if err := runDiagnostic(c); err != nil {
			if errors.Is(err, errDiagnosedUnstartable) {
				return 1
			}
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return 0
	}
	if err := serve(c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// runDiagnostic 执行只读诊断：获取同一把数据目录锁、复用正常启动的
// 读取与校验步骤，输出一份结构化 JSON 报告，然后立即释放锁退出。
func runDiagnostic(c config.Config) error {
	report, err := persistence.Diagnose(c.DataDir)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	if !report.Startable {
		return errDiagnosedUnstartable
	}
	return nil
}

func serve(c config.Config) error {
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
	os.Exit(run())
}
