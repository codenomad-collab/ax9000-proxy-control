package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

type DockerStats struct {
	Installed         bool   `json:"installed"`
	Version           string `json:"version,omitempty"`
	ContainersTotal   int    `json:"containers_total"`
	ContainersRunning int    `json:"containers_running"`
	StorageBytes      int64  `json:"storage_bytes,omitempty"`
}

type dockerCollector struct {
	socketPath string
	client     *http.Client
	baseURL    string
}

func (c *dockerCollector) Name() string { return "docker" }

func (c *dockerCollector) Collect(ctx context.Context) (any, error) {
	client, baseURL, err := c.httpClient()
	if err != nil {
		return nil, err
	}
	var version struct {
		Version string `json:"Version"`
	}
	if err := dockerGET(ctx, client, baseURL+"/version", &version); err != nil {
		return nil, fmt.Errorf("Docker Engine API 不可用: %w", err)
	}
	var containers []struct {
		State string `json:"State"`
	}
	if err := dockerGET(ctx, client, baseURL+"/containers/json?all=1", &containers); err != nil {
		return nil, fmt.Errorf("读取 Docker 容器失败: %w", err)
	}
	result := DockerStats{Installed: true, Version: version.Version, ContainersTotal: len(containers)}
	for _, container := range containers {
		if container.State == "running" {
			result.ContainersRunning++
		}
	}
	var disk struct {
		LayersSize int64 `json:"LayersSize"`
	}
	if dockerGET(ctx, client, baseURL+"/system/df", &disk) == nil {
		result.StorageBytes = disk.LayersSize
	}
	return result, nil
}

func (c *dockerCollector) httpClient() (*http.Client, string, error) {
	if c.client != nil {
		return c.client, c.baseURL, nil
	}
	if _, err := os.Stat(c.socketPath); err != nil {
		if os.IsNotExist(err) {
			return nil, "", fmt.Errorf("%w: Docker socket 不存在", errCollectorNotSupported)
		}
		return nil, "", err
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", c.socketPath)
		},
		DisableKeepAlives: true,
	}
	return &http.Client{Transport: transport}, "http://docker", nil
}

func dockerGET(ctx context.Context, client *http.Client, target string, output any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(output)
}
