package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

type meshCollector struct {
	configPath string
	nodesPath  string // 保留，兼容现有单元测试
	resolver   *meshNodesResolver
}

func (c *meshCollector) Name() string { return "mesh" }

// resolvedNodesPath 返回当前可用的节点缓存路径。
// 优先使用共享解析器，使扩展采集器与顶层 system-metrics 保持一致。
func (c *meshCollector) resolvedNodesPath() string {
	if c.resolver != nil {
		return c.resolver.Resolve()
	}
	return c.nodesPath
}

func (c *meshCollector) Collect(ctx context.Context) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	nodesPath := c.resolvedNodesPath()

	if c.configPath == "" && nodesPath == "" {
		return nil, fmt.Errorf("%w: 未发现 Mesh 数据源", errCollectorNotSupported)
	}
	if c.configPath != "" {
		if _, err := os.Stat(c.configPath); err != nil && nodesPath == "" {
			return nil, fmt.Errorf("Mesh 配置不可读: %w", err)
		}
	}
	stats := readMeshStats(c.configPath, nodesPath, time.Now())
	if !stats.Enabled {
		return nil, fmt.Errorf("%w: 当前未启用 Mesh", errCollectorNotSupported)
	}
	return stats, nil
}
