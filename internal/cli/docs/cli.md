# CLI 接口契约

## 输出契约

- **stdout 仅承载命令结果**：compact JSON，单行单值，结尾换行。仅两类场景不走 JSON 契约：一是输出原始内容的命令（`cat` 输出文件字节、`docs <topic>` 输出文档原文、`completion`/`help` 输出文本），二是终端交互界面（`tui` 与终端下的裸 `sjtu`）。
- **stderr 仅承载结构化错误**：单行 JSON，格式为 `{"error":"<code>","hint":"<guidance>"}`。
- 日志不输出至 stdout/stderr，统一写入状态目录（见下文「文件位置」）。

### 退出码

| 退出码 | 含义 |
| --- | --- |
| 0 | 成功 |
| 1 | 一般错误 |
| 2 | 用法错误（命令行参数或选项解析失败） |
| 3 | 认证失败（凭证缺失或失效，由 `hint` 指明补救动作） |

### 常见 error code

| 错误码 | 含义 |
| --- | --- |
| `usage` | 参数不合法 |
| `auth_required` | 未登录或凭证失效，`hint` 指明登录命令 |
| `not_found` | VFS 路径不存在 |

## 命令一览

| 命令 | 说明 |
| --- | --- |
| `sjtu ls <path>` / `stat <path>` / `cat <path>` | 读取 VFS；CLI 模式下路径必须为绝对路径 |
| `sjtu download <vfs-path> [local-path]` | 下载至本地磁盘，内容与 `cat` 严格一致；支持 Range 的媒体节点并行下载 |
| `sjtu attendance submit <url>` | 提交点名二维码 URL，响应体原样透传 |
| `sjtu auth canvas login/logout` | Canvas API Token 管理 |
| `sjtu auth jaccount login/logout` | jAccount SSO 扫码登录（交互式，禁止在 daemon socket 中调用） |
| `sjtu auth status` | 查看凭证清单与有效性 |
| `sjtu daemon` / `daemon install` / `daemon uninstall` | 详见 `sjtu docs daemon`；`install` 别名 `enable`，`uninstall` 别名 `disable` |
| `sjtu docs [topic]` | 打印接口契约文档（本文即 `cli` 主题；另有 `daemon` 主题） |
| `sjtu tui` | 交互式 TUI Shell（终端环境下自动进入） |
| `sjtu update [--check]` | 检查并更新至 GitHub 最新版本 |
| `sjtu version` | 打印当前版本号（JSON 格式） |
| `sjtu completion <shell>` | 生成 Shell 自动补全脚本 |

不带参数执行 `sjtu` 时，终端环境下进入 TUI，非终端（管道）环境下打印帮助信息。

## vfs 布局

实体是目录，投影是 JSON 文件，媒体是字节节点：

```
/courses                                   # 课程根目录
/courses/<id>/                             # 按课程数字 ID 访问
/courses/<学期>/                           # 学期目录（如 2026-2027-1），内含 <id> 与 <课程名> 软链接
/courses/current                           # 软链 → 当前学期目录
/courses/<id>/announcements/<n>/info       # 公告元数据投影
/courses/<id>/assignments/<n>/info         # 作业投影；若存在已提交作业，则包含 submissions/latest
/courses/<id>/discussions/<n>/info         # 讨论投影
/courses/<id>/files/...                    # 课程文件树（目录/文件原样映射）
/courses/<id>/attendance/current           # 进行中的点名（TTL=0，每次打开实时取）
/courses/<id>/attendance/records           # 历史签到记录（TTL=0）
/courses/<id>/attendance/status            # 签到状态（TTL=0）
/courses/<id>/live/<场次id>/info           # 直播场次投影
/courses/<id>/live/<场次id>/<频道>         # JSON：{"url":..., "headers":{"Referer":...}}
/courses/<id>/replay/<录像id>/info         # 录像投影：名称/周次/时间/时长/vodStatus
/courses/<id>/replay/<录像id>/url          # {"views":[{"viewNum","url"}],"headers":{...}}
/courses/<id>/replay/<录像id>/video-N      # 第 N 路视角媒体流（N 为 viewNum 实测值，不假定连续编号）
/courses/<id>/replay/<录像id>/subtitle     # 字幕投影：[{"bg","ed","res"}]，毫秒偏移
/courses/<id>/replay/<录像id>/summary      # 概括投影：{summary, mindmap}
/courses/<id>/replay/<MM-DD>-<N>           # 软链接 → 录像 ID（月-日-当日讲次）
```

规则：

- 学期目录仅匹配格式符合 `YYYY-YYYY Fall|Spring|Summer` 的规范学期；命名不合规的课程仅按 ID 可达，不做模式猜测。
- 录像未就绪（`vodStatus` 非可播放状态）时，读取 `url` 与 `video-N` 返回结构化错误；`subtitle`/`summary` 是否可用取决于上游接口。
- 元数据列表维护多级 TTL 磁盘缓存（`~/.cache/sjtu/meta`）；**文件内容永不缓存**；attendance（签到）节点不缓存（TTL=0，每次实时拉取）。
- `video-N` 的大小在进入所在目录时惰性探测；支持 Range 请求的媒体节点支持并行下载。

## 文件位置

| 类型 | 路径 |
| --- | --- |
| 配置文件 | `$XDG_CONFIG_HOME/sjtu/config.json`（默认 `~/.config/sjtu/config.json`） |
| 凭证存储 | 优先使用系统密钥环（macOS Keychain，Linux 下支持 GNOME Keyring / KWallet）；无图形桌面时保存于本地文件（`$XDG_DATA_HOME/sjtu/`，默认 `~/.local/share/sjtu/`，目录权限 0700、文件权限 0600）。当前存储后端可通过 `sjtu auth status` 查看 |
| 运行日志 | `~/.local/state/sjtu/sjtu.log`（CLI）与 `sjtud.log`（daemon），单文件达 4 MB 时轮转备份为 `.bak` |
| 元数据缓存 | `~/.cache/sjtu/meta` |
| Daemon 套接字 | `~/.local/state/sjtu/sjtud.sock` |
