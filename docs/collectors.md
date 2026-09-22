# 资源采集器的数据源与降级策略

资源页保留原有 `cpu`、`memory`、`storage`、`links`、`mesh` 与 `interfaces` 字段，并新增 `extended` 对象。`extended` 固定返回八个采集器的状态；浏览器不会因为某项没有数据而误以为它不存在。

每个采集器独立设置超时和刷新周期。HTTP 请求只读取最近缓存，过期采集在后台 goroutine 中启动；panic、读取错误、外部命令失败和超时只会把当前项目标成不可用，不会阻塞两秒一次的基础指标。

| 采集器 | 主要数据源 | 正常刷新 | 失败表现 |
|---|---|---:|---|
| `ethernet` | `/sys/class/net/*/{carrier,operstate,speed,duplex}`、UCI WAN 角色 | 5 秒 | 没有物理接口显示“不支持”；异常或负速率归零，端口仍保留 |
| `temperature` | `/sys/class/thermal/thermal_zone*/temp`、`/sys/class/hwmon/hwmon*/temp*_input` | 10 秒 | 没有传感器显示“不支持”；存在但全部不可读显示“不可用” |
| `fan` | `/sys/class/hwmon/hwmon*/fan*_input`；缺失时只读 `ubus call mitempctrl status` | 10 秒 | 两种数据源都不存在才显示“不支持”；温控停转时的 0 RPM 是有效值 |
| `usb` | `/proc/mounts`、`/sys/block/*/{size,stat,queue/hw_sector_size}` | 15 秒 | 没有 USB 块设备显示“不支持”；单个计数器缺失时容量或累计读写为 0 |
| `docker` | `/var/run/docker.sock` 的 `/version`、`/containers/json`、`/system/df` | 15 秒 | socket 不存在显示“不支持”；Engine 不响应显示“不可用”；磁盘统计是可选项 |
| `swap` | `/proc/meminfo`、`/proc/swaps` | 5 秒 | 没有启用 Swap 时正常返回 0；文件不可读才显示“不可用” |
| `pppoe` | `/etc/config/network`、只读 `ubus call network.interface.wan status` | 10 秒 | WAN 不是 PPPoE 显示“不支持”；不会执行重拨或写 UCI |
| `mesh` | 小米 Mesh UCI 配置与自动发现的节点缓存 | 5 秒 | 未启用显示“不支持”；缓存超过 5 分钟时 `fresh=false`，节点不得冒充在线 |

`Collector` 接口如下，新增监控项只需增加新的实现与注册项：

```go
type Collector interface {
    Name() string
    Collect(ctx context.Context) (any, error)
}
```

Docker 和 PPPoE 采集均使用标准库；程序仍是单一静态 ARM64 二进制，不增加 Python、Node、数据库或守护进程依赖。
