package config

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr            string
	DataDir         string
	ShutdownTimeout time.Duration
	Workers         int
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func Parse(args []string, output io.Writer) (Config, error) {
	var c Config
	flags := flag.NewFlagSet("stratad", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&c.Addr, "addr", env("STRATA_ADDR", "127.0.0.1:8093"), "HTTP 监听地址")
	flags.StringVar(&c.DataDir, "data", env("STRATA_DATA", "./data"), "剖面数据目录")
	shutdown := flags.String("shutdown", env("STRATA_SHUTDOWN", "10s"), "优雅退出期限")
	workers := flags.String("workers", env("STRATA_WORKERS", "4"), "成组对比并发计算上限（1–16）")
	if err := flags.Parse(args); err != nil {
		return c, err
	}
	if len(flags.Args()) > 0 {
		return c, fmt.Errorf("不支持位置参数")
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return c, fmt.Errorf("数据目录不能为空")
	}
	host, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return c, fmt.Errorf("监听地址必须为 host:port: %w", err)
	}
	if host != "" && host != "localhost" && net.ParseIP(host) == nil {
		return c, fmt.Errorf("监听地址必须使用 IP 或 localhost")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return c, fmt.Errorf("端口必须为 0 到 65535")
	}
	c.ShutdownTimeout, err = time.ParseDuration(*shutdown)
	if err != nil || c.ShutdownTimeout < time.Second || c.ShutdownTimeout > time.Minute {
		return c, fmt.Errorf("退出期限必须为 1s 到 1m")
	}
	c.Workers, err = strconv.Atoi(strings.TrimSpace(*workers))
	if err != nil || c.Workers < 1 || c.Workers > 16 {
		return c, fmt.Errorf("并发上限必须为 1 到 16")
	}
	c.DataDir, err = filepath.Abs(c.DataDir)
	if err != nil {
		return c, err
	}
	return c, nil
}
