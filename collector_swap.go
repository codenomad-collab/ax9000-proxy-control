package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type SwapEntry struct {
	Path      string `json:"path"`
	Type      string `json:"type"`
	SizeBytes uint64 `json:"size_bytes"`
	UsedBytes uint64 `json:"used_bytes"`
}

type SwapStats struct {
	TotalBytes   uint64      `json:"total_bytes"`
	UsedBytes    uint64      `json:"used_bytes"`
	UsagePercent float64     `json:"usage_percent"`
	Entries      []SwapEntry `json:"entries"`
}

type swapCollector struct {
	meminfoPath string
	swapsPath   string
}

func (c *swapCollector) Name() string { return "swap" }

func (c *swapCollector) Collect(ctx context.Context) (any, error) {
	meminfo, err := readKeyValueKiB(c.meminfoPath)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(c.swapsPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries := make([]SwapEntry, 0)
	scanner := bufio.NewScanner(file)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		sizeKiB, sizeErr := strconv.ParseUint(fields[2], 10, 64)
		usedKiB, usedErr := strconv.ParseUint(fields[3], 10, 64)
		if sizeErr == nil && usedErr == nil {
			entries = append(entries, SwapEntry{Path: fields[0], Type: fields[1], SizeBytes: sizeKiB * 1024, UsedBytes: usedKiB * 1024})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	total := meminfo["SwapTotal"] * 1024
	free := meminfo["SwapFree"] * 1024
	used := uint64(0)
	if free <= total {
		used = total - free
	}
	return SwapStats{TotalBytes: total, UsedBytes: used, UsagePercent: percentage(used, total), Entries: entries}, nil
}

func readKeyValueKiB(path string) (map[string]uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := map[string]uint64{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err == nil {
			result[strings.TrimSuffix(fields[0], ":")] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return result, nil
}
