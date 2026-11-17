# 常用网站自动选路（控制台 0.10.0）

质量守护在控制台进程内运行，由已有 procd 服务开机启动，不依赖 PC 或 Codex 定时任务。AI 美国节点仍由独立 AI 守护管理；本次将其升级到 1.1.3，修复新版 Mihomo 订阅节点不能经全局 `/proxies/:name/delay` 探测的问题，改用订阅节点专用接口。

## 分组与规则

| 分组 | 用途 | 策略 |
|---|---|---|
| AI-US-STABLE | AI、GitHub，以及 Gemini/AI Studio 等明确 AI 域名 | 沿用美国三线自动回退 |
| YOUTUBE-2K | YouTube 页面、API、图片、视频；名称兼容旧规则，可播放4K | 有下载样本后才引入新视频节点 |
| X-STABLE | X/Twitter、twimg、t.co 等 | 优先保持当前出口地区，失效时允许回退 |
| GOOGLE-AUTO | 普通 Google 搜索及相关服务 | 两个检测窗口确认改善后调整 |
| AUTO | PROXY 默认出口使用的其他国外访问 | 电信推荐候选池自动回退 |

仅接受 Hysteria2/VLESS，候选名称匹配电信或三网推荐，排除“避免晚高峰”“合适下载”以及订阅流量提示条目。已存在的 AI、YouTube 专用规则和国内/内网直连优先级保留；新增 Google 通用规则放在它们之后、通用规则集之前。

每个业务使用 fallback 外层和最多三个隐藏的单节点选择器，明确候选顺序。不使用 PUT 长期固定外层分组。候选改名导致空组时回退 REJECT，避免误变成直连。质量守护暂停或控制台进程退出后，Mihomo 的失效回退仍继续。

## 检测与保护

- 默认60秒轻量检测；每个候选重复两次，限制并发。正常轮询检查当前池并逐步轮换订阅内其他候选，首次检查扩大候选范围。
- 响应更快30%以上、连续两个检查窗口确认，才调整普通业务首选；冷却至少5分钟。连续不可用可以越过冷却。
- 带宽检测默认活跃业务每30分钟一次；连续异常或连续出现低流量，仅作为疑似信号，在至少5分钟间隔后触发主动复核。暂停、缓存充分、码率低不直接作为切换依据。
- 每批最多三个节点、交错两轮，每次最多8MiB、最长14秒。量测来自路由器上的临时 Mihomo，代理和控制接口均仅在 loopback/Unix socket；不占用主代理分组，不安装 TUN 或 DNS 监听。
- 默认每日256MiB测速预算（可配置，Asia/Shanghai），在请求前持久化预扣，失败和部分下载也计入；重启保留。预算按测速负载预扣，不含协议开销及轻量检查流量。账本损坏或不可写时停止带宽探测。预算用完后轻量检测与内核回退继续。
- 拒绝错误状态、24字节“成功页”和不足1MiB的下载样本。限量读取，即使源站忽略Range也不能无界下载。样本排除CONNECT/TLS首字节等待，不能当作运营商承诺带宽。
- YouTube 候选在两轮下载均优于当前30%以上后才提升；当前不可用时可选择已验证的替代。正常改进不意味着一定达到4K所需带宽。
- 确认切换后仅重连受影响业务旧节点上的连接（最多100条）；不清空其他业务或AI会话。健康的普通浏览连接保留。
- 不能读取 App 的缓冲条或真实播放码率，无法承诺瞬时、完全无感切换，也无法修复所有节点拥塞或家中 Wi-Fi 弱信号。

## 配置与控制台

主配置新增 `routing_guard_policy_path`，指向 USB 上策略 JSON。完整字段可参考 `routingPolicy`：

```json
{
  "enabled": true,
  "shellcrash_root": "/mnt/usb-example/ShellClash",
  "provider": "household",
  "candidate_filter": "电信|三网推荐",
  "check_seconds": 60,
  "quality_seconds": 1800,
  "cooldown_seconds": 300,
  "daily_bytes": 268435456,
  "probe_bytes": 8388608,
  "timezone": "Asia/Shanghai",
  "groups": [
    {"name":"YOUTUBE-2K","label":"YouTube 4K","target":"https://www.youtube.com/generate_204","expected":204,"nodes":["实际电信推荐节点名称"]},
    {"name":"X-STABLE","label":"X / Twitter","target":"https://abs.twimg.com/favicons/twitter.3.ico","expected":200,"nodes":["实际电信推荐节点名称"]},
    {"name":"GOOGLE-AUTO","label":"Google 搜索","target":"https://www.google.com/generate_204","expected":204,"nodes":["实际电信推荐节点名称"]},
    {"name":"AUTO","label":"其他国外网站","target":"https://www.gstatic.com/generate_204","expected":204,"nodes":["实际电信推荐节点名称"]}
  ]
}
```

策略文件旁 `.state.json` 保存预算、候选顺序与有限事件，不含订阅URL或控制器密钥。每分钟的状态只保存在内存，预算预扣及调整时才写持久状态。近期下载样本在控制台重启后继续复用。

原“节点守护”栏目增加常用网站区块，展示当前实际节点、候选、响应、下载样本时间、预算及切换记录；AI 区块独立保留。

- GET `/api/routing-guard`：认证后读取状态。
- POST `/api/routing-guard/action`：认证和CSRF校验；`run` 手动检测，`restore`/`enable` 恢复自动，`disable` 暂停质量调整。暂停时手动检测不应用候选。

## 事务与回滚

正式配置和模板共同更新。与AI守护共用 flock，避开模式切换；候选分别通过 CrashCore -t，备份后原子替换，再热加载和核对组成员。校验或应用失败恢复两份原件；最近三份本功能备份保留，其他备份不动。正式加载路径必须位于内核 home/SAFE_PATHS 中，不使用不受信任的 `/tmp/ShellCrash/config.yaml` 别名。

程序异常退出时，Linux 子测速进程设置父进程死亡信号，避免变成常驻孤儿服务。平时样本完成即退出并清除RAM目录。

验证：`go test -race ./...`、`go vet ./...`、`node --check web/app.js`，再交叉编译ARM64。覆盖预算重启/损坏、下载边界、两窗口/冷却、AI规则不变、恢复固定选择、只重连对应业务、鉴权/CSRF和备份归属。真实路由器另做隔离内核回退演练和页面验收。
