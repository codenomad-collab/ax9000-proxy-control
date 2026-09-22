package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

type meshCollector struct {
	configPath string
	nodesPath  string
}

func (c *meshCollector) Name() string { return "mesh" }

func (c *meshCollector) Collect(ctx context.Context) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.configPath == "" && c.nodesPath == "" {
		return nil, fmt.Errorf("%w: 未发现 Mesh 数据源", errCollectorNotSupported)
	}
	if c.configPath != "" {
		if _, err := os.Stat(c.configPath); err != nil && c.nodesPath == "" {
			return nil, fmt.Errorf("Mesh 配置不可读: %w", err)
		}
	}
	stats := readMeshStats(c.configPath, c.nodesPath, time.Now())
	if !stats.Enabled {
		return nil, fmt.Errorf("%w: 当前未启用 Mesh", errCollectorNotSupported)
	}
	return stats, nil
}
