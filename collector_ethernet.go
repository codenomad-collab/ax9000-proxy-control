package main

import (
	"context"
	"fmt"
)

type ethernetCollector struct {
	sysClassNet  string
	wanInterface string
}

func (c *ethernetCollector) Name() string { return "ethernet" }

func (c *ethernetCollector) Collect(ctx context.Context) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	links := readEthernetLinks(c.sysClassNet, c.wanInterface)
	if len(links) == 0 {
		return nil, fmt.Errorf("%w: 未发现物理以太网接口", errCollectorNotSupported)
	}
	for index := range links {
		if links[index].SpeedMbps < 0 || links[index].SpeedMbps > 400000 {
			links[index].SpeedMbps = 0
		}
	}
	return map[string]any{"links": links}, nil
}
