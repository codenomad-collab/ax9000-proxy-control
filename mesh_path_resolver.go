package main

import (
	"os"
	"strings"
	"sync"
)

// meshNodesResolver 在运行期惰性解析 Mesh 节点缓存路径。
//
// 背景：Mesh 节点缓存（BE10000 上是 /tmp/xq_whc_quire）由固件在开机后异步生成，
// 且位于 tmpfs，重启即清空。若只在进程启动时探测一次，控制台启动早于缓存生成时
// 会永久丢失该数据源，之后不再重试。
//
// 因此启动探测结果只作为初始提示，真正读取数据前再次确认路径：
//   - 显式配置（override）优先级最高，且不检查当前是否存在 —— 文件稍后生成即可读取；
//   - 自动发现的路径若已失效，重新探测候选列表。
//
// 并发安全：current 由 RWMutex 保护，可被多个采集协程同时调用。
type meshNodesResolver struct {
	mu       sync.RWMutex
	override string
	current  string
	discover func(string) string
}

func newMeshNodesResolver(override, initial string) *meshNodesResolver {
	return &meshNodesResolver{
		override: strings.TrimSpace(override),
		current:  strings.TrimSpace(initial),
		discover: discoverMeshNodesPath,
	}
}

// Resolve 返回当前可用的 Mesh 节点缓存路径，无法确定时返回空字符串。
func (r *meshNodesResolver) Resolve() string {
	// 显式配置具有最高优先级。即使文件当前尚不存在，也保留这个路径；
	// 文件稍后生成后，下一轮采集即可读取，不需要重启控制台。
	if r.override != "" {
		return r.override
	}

	r.mu.RLock()
	current := r.current
	r.mu.RUnlock()

	if regularReadableFile(current) {
		return current
	}

	discovered := r.discover("")
	if discovered == "" {
		return ""
	}

	r.mu.Lock()
	r.current = discovered
	r.mu.Unlock()
	return discovered
}

func regularReadableFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && fileReadable(path)
}
