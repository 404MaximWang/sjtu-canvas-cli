# daemon 接口契约

`sjtu daemon` 是本机常驻服务，通过 Unix Domain Socket 复用预热会话执行 CLI 命令，并提供定时健康自检与异常通知。Socket 路径为 `~/.local/state/sjtu/sjtud.sock`（文件权限为 0600）。服务仅提供两个 HTTP 端点：`POST /run` 与 `GET /healthz`；请求方法不匹配返回 405，未知路径返回 404。

## POST /run

执行一条命令。请求体（上限 1 MiB）：

```json
{"argv": ["ls", "/courses"], "stream": false}
```

- `argv`：非空字符串数组，等价于在终端执行 `sjtu <argv...>`。
- `stream`：默认为 `false`（缓冲模式）；设为 `true` 启用流式模式。
- 生命周期绑定：客户端断开连接或 daemon 退出时，命令立即取消；单条命令执行异常不影响 daemon 进程自身。
- 凭证失效自愈：遭遇认证失效时，daemon 自动重读凭据存储并重试一次（stream 模式已产生输出除外）。

### buffered 模式（`stream: false`）

HTTP 状态码固定为 200，执行结果由响应体中的 `code` 判定：

```json
{"code": 0, "stdout": "...", "stderr": "..."}
```

- `code`：进程退出码，与 CLI 一致。
- `stdout`：上限 32 MiB；超限则中止命令并返回 `{"code": -1, "error": "output_too_large_use_stream"}`；若包含非合法 UTF-8 字节，则返回 `{"code": -1, "error": "binary_output_use_stream"}`（二进制输出须改用 stream 模式）。
- `stderr`：上限 1 MiB；超限部分自动截断并标记 `"stderr_truncated": true`；包含非 UTF-8 序列时有损替换并标记 `"stderr_lossy": true`（截断与替换不中止命令）。
- 请求体格式错误：返回 HTTP 400 `{"error": "bad_request", "detail": "..."}`。

### stream 模式（`stream: true`）

命令的标准输出原始字节直接实时流式发送给客户端。响应头在**首个 stdout 字节产生**或**命令退出**（先到为准）时发送：

- 产生输出：返回 HTTP 200 与 `Content-Type: application/octet-stream`，随后持续传输原始数据流。
- 无输出且成功（exit 0）：返回 HTTP 200 与空响应体。
- 无输出且失败：返回 HTTP 500 与 JSON 错误体 `{"code": N, "error": "command_failed", "stderr": "..."}`。
- 输出中途失败：无法在协议内带内传递错误，直接截断连接（curl 表现为 exit 18「传输中断」），客户端可安全重试。

### Socket 禁用清单

依赖交互式终端、会产生递归常驻或执行凭证写操作的命令在 Socket 接口中禁用，调用时按缓冲模式返回 `code: 2`：

- `daemon`（含 `install` / `uninstall`）
- `tui`
- `auth` 下的 `login` 系列（交互式扫码与输入）与 `logout` 系列（凭证删除须为本机显式操作）

`auth status` 为只读操作，不在禁用之列。

## GET /healthz

HTTP 状态码固定为 200，各域健康状态均在响应体中体现：

```json
{
  "domains": {
    "canvas":    {"state": "ok", "at": "2026-10-02T12:00:00+08:00"},
    "jaccount":  {"state": "fail", "detail": "<补救指引>", "at": "..."},
    "video":     {"state": "unknown", "detail": "not probed yet", "at": "..."},
    "mlearning": {"state": "ok", "at": "..."}
  }
}
```

- `state`：可用状态，包含 `ok`（正常）、`fail`（失效）与 `unknown`（首轮自检完成前，或未配置自检参数）。
- `at`：最近一次状态变更的时间戳（RFC 3339 格式，精确到秒）。
- 四个域默认每小时自检一次；真实业务请求中的认证结果也会实时刷新对应域的状态（jaccount 域由 video 与 mlearning 共享）。
- 状态为 `fail` 时，`detail` 提供具体的登录补救指引；自检失败不会导致 daemon 退出。

## 通知

仅在域健康状态发生**变迁**时触发（进入 `fail` 触发一次，恢复 `ok` 触发一次；保持失败状态不重复发送；启动首轮自检为 `fail` 视作一次事件）：

1. 若配置了 `notify.command`，则作为子进程执行，事件 JSON 通过标准输入（stdin）传入，超时为 5 秒，执行失败仅记录日志：

   ```json
   {"event": "fail|recover", "domain": "canvas", "detail": "...", "at": "..."}
   ```

2. 若处于图形桌面环境中，将同时发送系统桌面通知（macOS 默认支持，Linux 需活跃的桌面会话）。

## 配置（config.json）

```json
{
  "canvas_base_url": "https://oc.sjtu.edu.cn",
  "probe":  {"video_course_id": 12345},
  "notify": {"command": ["/path/to/hook", "arg"]}
}
```

- `canvas_base_url`：Canvas 实例根地址，默认为主校区。
- `probe.video_course_id`：视频域健康自检所用课程 ID；未配置时该域恒为 `unknown`。`daemon install` 前会对此项与 `notify.command` 做预检警告（不阻塞安装）。
- `notify.command`：状态变迁通知钩子的命令参数数组。

## 服务管理

- `sjtu daemon install`（别名 `enable`）：将当前二进制的物理绝对路径写入系统服务配置并激活启动：
  - macOS：写入 `~/Library/LaunchAgents/cn.edu.sjtu.sjtud.plist` 并调用 `launchctl bootstrap`。
  - Linux：写入 `~/.config/systemd/user/sjtu-daemon.service` 并调用 `systemctl --user enable --now`。
- `sjtu daemon uninstall`（别名 `disable`）：停止并注销系统服务，删除对应的服务配置文件；未安装时操作保持幂等。
- 优雅停机：收到 SIGTERM 信号后，daemon 停止接收新连接，等待在途请求最多 5 秒后退出；Linux systemd 预设 15 秒后触发 SIGKILL 强制回收。
- 运行日志：写入 `~/.local/state/sjtu/sjtud.log`（单文件达 4 MB 时轮转备份为 `.bak`，与 CLI 日志相互独立）。
