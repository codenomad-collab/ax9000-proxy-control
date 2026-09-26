package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const version = "0.9.0"

func main() {
	configPath := flag.String("config", "/data/router-proxy-web/config.json", "configuration file")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	// 必须在 resolveRuntimeConfig 之前保存，否则启动时自动发现的路径
	// 会被误当成用户显式配置，从而失去「文件稍后生成仍可读取」的语义。
	configuredMeshNodesPath := cfg.MeshNodesPath

	capabilityContext, cancelCapabilities := context.WithTimeout(context.Background(), 5*time.Second)
	capabilities := detectCapabilities(capabilityContext, cfg)
	cancelCapabilities()
	cfg = resolveRuntimeConfig(cfg, capabilities)

	meshResolver := newMeshNodesResolver(configuredMeshNodesPath, capabilities.MeshNodesPath)

	app := newAppWithMeshResolver(cfg, *configPath, capabilities, meshResolver)
	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           app.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      50 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	go func() {
		app.audit.add("info", "system", "控制服务启动，监听 "+cfg.Listen)
		log.Printf("Router Proxy Control %s listening on %s", version, cfg.Listen)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	app.audit.add("info", "system", "控制服务正在退出")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}
