package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type fanCollector struct {
	hwmonRoot   string
	ubusCommand string
	statusPath  string
}

func (c *fanCollector) Name() string { return "fan" }

func (c *fanCollector) Collect(ctx context.Context) (any, error) {
	paths, err := filepath.Glob(filepath.Join(c.hwmonRoot, "hwmon*", "fan*_input"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return c.collectMitempctrl(ctx)
	}
	readings := make([]SensorReading, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(readSmallFile(path)), 64)
		if err != nil || value < 0 || value > 1000000 {
			continue
		}
		readings = append(readings, SensorReading{Name: sensorLabel(path, "", c.hwmonRoot), Source: path, Value: value, Unit: "RPM"})
	}
	if len(readings) == 0 {
		return nil, fmt.Errorf("风扇传感器存在但当前不可读")
	}
	return map[string]any{"fans": readings}, nil
}

func (c *fanCollector) collectMitempctrl(ctx context.Context) (any, error) {
	var data []byte
	var err error
	if c.statusPath != "" {
		data, err = os.ReadFile(c.statusPath)
	} else if c.ubusCommand != "" {
		data, err = exec.CommandContext(ctx, c.ubusCommand, "call", "mitempctrl", "status").CombinedOutput()
	} else {
		return nil, fmt.Errorf("%w: 未发现风扇转速传感器", errCollectorNotSupported)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: 未发现可用风扇数据源", errCollectorNotSupported)
	}
	var status struct {
		Fan struct {
			Mode  int     `json:"mode"`
			Speed float64 `json:"speed"`
		} `json:"fan"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("解析温控风扇状态失败: %w", err)
	}
	if status.Fan.Speed < 0 || status.Fan.Speed > 1000000 {
		return nil, fmt.Errorf("温控风扇转速异常")
	}
	return map[string]any{
		"fans": []SensorReading{{Name: "系统风扇", Source: "ubus:mitempctrl", Value: status.Fan.Speed, Unit: "RPM"}},
		"mode": status.Fan.Mode,
	}, nil
}
