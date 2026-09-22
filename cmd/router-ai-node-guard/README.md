# 路由器 AI 美国线路节点守护

v1.1.0 是运行在 ARM64 路由器本机的轻量守护程序，不依赖 PC 或 Codex 客户端持续在线。同一份静态二进制可用于 BE10000 与 AX9000；程序不判断机型。二进制位于 `<ShellClash>/tools/` 时会从自身位置发现根目录，也可用 `--shellcrash-root`、独立参数或 `NODE_GUARD_*` 环境变量覆盖。

正常情况下由 cron 每 30 分钟执行一次，检查：

- `AI-US-STABLE` 与 `GITHUB-US` 的结构、顺序和可用状态；
- 六个专用代理提供器是否仍各自匹配一个预期节点；
- 完整订阅中三个预期节点是否仍存在；
- 当前主线路对 OpenAI、Anthropic 和 GitHub 的可达性。

第一次运行不会读取 v1.0.0 的旧状态做切换。它会把旧 schema 视为未初始化，对当前订阅内全部符合协议和美国地区条件的候选执行一轮三目标探测，建立 v2 基线后才进入常规调度。后续普通故障先刷新提供器并记录；连续两次故障、两个业务组同时不可用，或节点改名时才进行深度候选测试。

自动修复要求同时找到两条通过三目标重复测试的美国 Hysteria2 节点和一条 VLESS 节点。修复会同步更新正式配置与模板，经过 CrashCore 校验、原配置备份、热加载、规则检查和真实流量验证。任何一步失败都会尝试原子回滚，并把回滚时间与结果写入状态文件。证据不足时保留原配置并写入告警。

## BE10000 部署示例

```sh
ROOT=/mnt/usb-49174f5f/ShellClash
install -m 700 router-ai-node-guard-linux-arm64 "$ROOT/tools/router-ai-node-guard"
"$ROOT/tools/router-ai-node-guard" --shellcrash-root "$ROOT"
"$ROOT/tools/router-ai-node-guard" --shellcrash-root "$ROOT" --status
```

cron 每 30 分钟执行一次：

```cron
*/30 * * * * /mnt/usb-49174f5f/ShellClash/tools/router-ai-node-guard --shellcrash-root /mnt/usb-49174f5f/ShellClash >>/mnt/usb-49174f5f/ShellClash/logs/ai-node-guard-cron.log 2>&1
```

AX9000 只需把根路径换成其实际 ShellClash 目录。控制台应配置同一个二进制、状态与日志路径；调度继续由 `/etc/crontabs/root` 管理。

## 参数

```text
--controller-url
--shellcrash-root
--runtime-config
--persistent-config
--template-config
--crash-core
--state-file
--log-file
--lock-file
--exercise-switch
--status
--version
```

除 `--version` 外，程序需要从自身位置发现 ShellCrash 根目录、接收 `--shellcrash-root`，或显式提供 persistent/template/state/log 四个路径。默认控制器为路由器本机 `127.0.0.1:9097`，运行态配置和校验内核默认位于 `/tmp/ShellCrash`，两者都可覆盖。

`--exercise-switch` 仅用于验收：它对候选做双轮三目标探测，切换到一组已验证的不同顺序，完成规则与真实流量验证后再恢复原顺序。任一阶段失败均调用与生产故障相同的回滚路径，并把演练结果写入状态文件。

## 构建与验证

```sh
go test -race ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o router-ai-node-guard-linux-arm64 .
```
