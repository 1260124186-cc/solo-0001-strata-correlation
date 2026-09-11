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
	// 锁定完整性门槛配置（原始字符串，由领域层解析为规则集合）。
	SealRules        string
	MinLayerMM       *int64
	MarkerPairGroups string
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
	flags.StringVar(&c.SealRules, "seal-rules", env("STRATA_SEAL_RULES", "coverage"),
		"锁定完整性规则列表，逗号分隔：coverage,adjacent,min_thickness,marker_paired")
	minLayer := flags.String("min-layer-mm", env("STRATA_MIN_LAYER_MM", ""),
		"启用 min_thickness 规则时的单层最小厚度（整数毫米）")
	flags.StringVar(&c.MarkerPairGroups, "marker-pair-groups", env("STRATA_MARKER_PAIR_GROUPS", "上,下;顶,底"),
		"启用 marker_paired 规则时的标志层方位组，组间用分号、组内用逗号分隔")
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
	if strings.TrimSpace(*minLayer) != "" {
		value, err := strconv.ParseInt(strings.TrimSpace(*minLayer), 10, 64)
		if err != nil || value < 1 || value > 1000000 {
			return c, fmt.Errorf("最小单层厚度必须为 1 到 1000000 毫米")
		}
		c.MinLayerMM = &value
	}
	c.DataDir, err = filepath.Abs(c.DataDir)
	if err != nil {
		return c, err
	}
	return c, nil
}
