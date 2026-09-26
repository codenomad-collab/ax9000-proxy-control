package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type USBDeviceStats struct {
	Device       string `json:"device"`
	Filesystem   string `json:"filesystem"`
	MountPoint   string `json:"mount_point"`
	SizeBytes    uint64 `json:"size_bytes"`
	ReadBytes    uint64 `json:"read_bytes,omitempty"`
	WrittenBytes uint64 `json:"written_bytes,omitempty"`
}

type usbCollector struct {
	mountsPath   string
	sysBlockRoot string
}

func (c *usbCollector) Name() string { return "usb" }

func (c *usbCollector) Collect(ctx context.Context) (any, error) {
	file, err := os.Open(c.mountsPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	devices := make([]USBDeviceStats, 0)
	seenDevices := map[string]bool{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || !strings.HasPrefix(fields[0], "/dev/") {
			continue
		}
		device := unescapeMountField(fields[0])
		if seenDevices[device] {
			continue
		}
		mountPoint := unescapeMountField(fields[1])
		base := blockDeviceBase(filepath.Base(device))
		if !strings.HasPrefix(mountPoint, "/mnt/usb-") && !isUSBBlockDevice(filepath.Join(c.sysBlockRoot, base)) {
			continue
		}
		stats := USBDeviceStats{Device: device, Filesystem: fields[2], MountPoint: mountPoint}
		blockPath := filepath.Join(c.sysBlockRoot, base)
		partitionPath := filepath.Join(blockPath, filepath.Base(device))
		if info, err := os.Stat(partitionPath); err == nil && info.IsDir() {
			blockPath = partitionPath
		}
		stats.SizeBytes, stats.ReadBytes, stats.WrittenBytes = readBlockStats(blockPath)
		devices = append(devices, stats)
		seenDevices[device] = true
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("%w: 未发现已挂载 USB 块设备", errCollectorNotSupported)
	}
	return map[string]any{"devices": devices}, nil
}

func blockDeviceBase(name string) string {
	if strings.HasPrefix(name, "nvme") || strings.HasPrefix(name, "mmcblk") {
		if index := strings.LastIndex(name, "p"); index > 0 {
			if _, err := strconv.Atoi(name[index+1:]); err == nil {
				return name[:index]
			}
		}
	}
	return strings.TrimRight(name, "0123456789")
}

func isUSBBlockDevice(blockPath string) bool {
	target, err := filepath.EvalSymlinks(filepath.Join(blockPath, "device"))
	return err == nil && strings.Contains(strings.ToLower(target), "usb")
}

func readBlockStats(blockPath string) (size, readBytes, writtenBytes uint64) {
	sectors, _ := strconv.ParseUint(strings.TrimSpace(readSmallFile(filepath.Join(blockPath, "size"))), 10, 64)
	sectorSize, err := strconv.ParseUint(strings.TrimSpace(readSmallFile(filepath.Join(blockPath, "queue", "hw_sector_size"))), 10, 64)
	if err != nil || sectorSize == 0 {
		sectorSize = 512
	}
	size = sectors * sectorSize
	fields := strings.Fields(readSmallFile(filepath.Join(blockPath, "stat")))
	if len(fields) >= 7 {
		readSectors, readErr := strconv.ParseUint(fields[2], 10, 64)
		writtenSectors, writeErr := strconv.ParseUint(fields[6], 10, 64)
		if readErr == nil {
			readBytes = readSectors * sectorSize
		}
		if writeErr == nil {
			writtenBytes = writtenSectors * sectorSize
		}
	}
	return
}
