# 机型适配说明

`router-proxy-control` 0.7.0 使用“能力探测优先、配置显式覆盖”的方式运行在不同的小米路由器上。程序不读取型号或序列号来选择代码分支，同一份静态 ARM64 二进制可以用于 AX9000 与 BE10000。

## 启动时自动发现

| 能力 | 数据源 | 选择规则 | 失败时行为 |
|---|---|---|---|
| 外接存储 | `/proc/mounts`、`statfs`、写入探针 | 只考虑可写、持久化、非系统分区且至少剩余 64 MiB 的设备；优先 Linux 原生文件系统，再按可用容量和路径稳定排序 | 外接程序和节点守护路径保持未配置，诊断接口给出原因 |
| WAN 物理接口 | `/etc/config/network` | 依次解析 `wan`、`wan6`、`pppoe`、`wan_pppoe` 的 `device`/`ifname` | 链路仍可显示，但不标记 WAN；诊断接口提示设置覆盖项 |
| 物理以太网口 | `/sys/class/net` | 具备 `speed` 或 `carrier`，排除虚拟、无线、隧道和逻辑聚合接口 | 返回空清单，其它指标继续工作 |
| Docker | `/var/run/docker.sock` Engine API | Unix socket 可连接并能读取 `/version` | 显示未启用或不可用，不改变代理切换链路 |
| 温度与风扇 | `/sys/class/thermal`、`/sys/class/hwmon` | 返回所有可读的传感器文件 | 返回空清单，不显示 0 |
| Mesh | `/etc/config/xiaoqiang` 与 `/tmp` 下 Mesh/WHC 缓存 | 显式路径优先；否则选择首个可读候选 | Mesh 指标不可用并给出诊断提示 |

探测结果在登录后的诊断接口 `GET /api/diagnostics` 的 `capabilities` 字段中返回。探测只在启动时执行并缓存，HTTP 请求不会重复执行慢探测。

## 配置覆盖

以下配置仅用于覆盖自动发现结果：

- `external_storage`
- `wan_interface`
- `mesh_config_path`
- `mesh_nodes_path`
- `network_config_path`

`listen` 必须显式配置为路由器的一个 LAN IPv4 地址。程序拒绝 `0.0.0.0` 与 IPv6 监听地址。ShellCrash 与节点守护的持久化路径默认从选中的外接存储根派生；若显式配置了完整路径，程序原样保留，便于旧 AX9000 部署继续使用 `/extdisks/sda1`。

## BE10000 推荐配置

```json
{
  "listen": "192.168.50.1:9098",
  "external_storage": "/mnt/usb-49174f5f",
  "wan_interface": "",
  "network_config_path": "/etc/config/network",
  "mesh_config_path": "/etc/config/xiaoqiang",
  "mesh_nodes_path": ""
}
```

`external_storage` 可以留空使用自动发现。生产环境显式填写它可以防止同时插入多个 U 盘后应用目录随容量排序发生变化。`wan_interface` 与 `mesh_nodes_path` 建议留空，只有固件布局无法识别时再覆盖。

## 程序与数据布局

安装器先确定外接存储根，然后派生以下路径：

```text
<external_storage>/router-proxy-control/bin/router-proxy-control
<external_storage>/router-proxy-control/backups/
<external_storage>/ShellClash/tools/router-ai-node-guard
<external_storage>/ShellClash/tools/ai-node-guard-state.json
<external_storage>/ShellClash/logs/ai-node-guard.log
```

内部持久化分区只保存小型配置、启动器与 procd 入口：

```text
/data/router-proxy-web/config.json
/data/router-proxy-web/launcher.sh
/data/router-proxy-web/app-path
/data/router-proxy-web/persist.sh
/data/router-proxy-web/router-proxy-web.init
/etc/init.d/router-proxy-web
```

`launcher.sh` 从 `app-path` 读取真实二进制路径，因此 procd 文件不依赖任何机型的 U 盘挂载点。RC01 会在重启时重建 `/etc/init.d`，`persist.sh` 由持久化 cron 每分钟检查一次，只在 init 缺失或内容变化时原子恢复，并在监听端口缺失时启动服务。

## 已验证范围

- BE10000 RC01 1.1.56：外接 ext4 存储、WAN、6 个物理网口、Docker、温度传感器与 Mesh 缓存均由同一二进制发现；控制台仅监听配置的 LAN IPv4，procd 重启正常。
- AX9000：显式旧路径覆盖由单元测试覆盖，保留 `/extdisks/sda1`、`eth4` 与旧节点守护路径的既有行为。该设备已经恢复出厂并作为 Mesh 节点，无法再做旧代理主机的在线回归；历史配置与程序保存在离线备份中。

## 变更清单

- `capabilities.go`：新增启动时能力探测、确定性存储选择与运行路径派生。
- `config.go`：移除机型专属默认值，增加覆盖项并强制 LAN 单地址监听。
- `metrics.go`：使用自动发现的接口、WAN、存储与 Mesh 路径。
- `api.go`、`app.go`、`main.go`：缓存探测结果并加入诊断接口。
- `packaging/`、`scripts/`：安装器和 procd 启动链路改为从 `app-path` 读取实际路径。
- `capabilities_test.go`、`metrics_test.go`、`testdata/`：覆盖失败、部分成功、多挂载点、接口识别及旧路径兼容。
