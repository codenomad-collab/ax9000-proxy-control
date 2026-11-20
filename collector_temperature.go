package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

type SensorReading struct {
	Name   string  `json:"name"`
	Source string  `json:"source"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
}

type temperatureCollector struct {
	thermalRoot string
	hwmonRoot   string
}

func (c *temperatureCollector) Name() string { return "temperature" }

func (c *temperatureCollector) Collect(ctx context.Context) (any, error) {
	patterns := []string{
		filepath.Join(c.thermalRoot, "thermal_zone*", "temp"),
		filepath.Join(c.hwmonRoot, "hwmon*", "temp*_input"),
	}
	readings := make([]SensorReading, 0)
	candidates := 0
	for _, pattern := range patterns {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		candidates += len(paths)
		for _, path := range paths {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			value, ok := readSensorNumber(path)
			if !ok {
				continue
			}
			name := sensorLabel(path, c.thermalRoot, c.hwmonRoot)
			readings = append(readings, SensorReading{Name: name, Source: path, Value: value, Unit: "°C"})
		}
	}
	if candidates == 0 {
		return nil, fmt.Errorf("%w: 未发现温度传感器", errCollectorNotSupported)
	}
	if len(readings) == 0 {
		return nil, fmt.Errorf("温度传感器存在但当前不可读")
	}
	return map[string]any{"sensors": readings}, nil
}

func readSensorNumber(path string) (float64, bool) {
	text := strings.TrimSpace(readSmallFile(path))
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	if value > 1000 || value < -1000 {
		value /= 1000
	}
	// Router firmware commonly exposes 0 or negative placeholder channels.
	// They are not useful thermal readings and would mislead the dashboard.
	if value <= 0 || value > 250 {
		return 0, false
	}
	return value, true
}

func sensorLabel(path, thermalRoot, hwmonRoot string) string {
	directory := filepath.Dir(path)
	base := filepath.Base(path)
	if thermalRoot != "" && strings.HasPrefix(directory, thermalRoot) {
		if value := strings.TrimSpace(readSmallFile(filepath.Join(directory, "type"))); value != "" {
			return value
		}
		return filepath.Base(directory)
	}
	prefix := strings.TrimSuffix(base, "_input")
	if value := strings.TrimSpace(readSmallFile(filepath.Join(directory, prefix+"_label"))); value != "" {
		return value
	}
	if value := strings.TrimSpace(readSmallFile(filepath.Join(directory, "name"))); value != "" {
		return value + " " + prefix
	}
	return filepath.Base(directory) + " " + prefix
}
