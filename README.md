# TeleNexus

TeleNexus 是使用 Go 开发的 Telegram 多账号管理服务。当前提供管理台、
账号元数据管理、SQLite 持久化、MTProto 二维码/手机号登录、可选两步验证、
账号会话搜索、个人资料编辑、消息转发/删除、账号分组、关键词监控、
消息索引检索、数据报表、系统配置、健康检查和 REST API。

## 环境要求

- Go 1.24 或更高版本
- 在 [my.telegram.org](https://my.telegram.org/) 为本项目单独申请
  `api_id` 和 `api_hash`
- 真实登录必须使用自己的 Telegram API 凭据，不可复用参考项目或第三方客户端的凭据

## 快速开始

```sh
cp .env.example .env
# 编辑 .env，填写 TELEGRAM_API_ID 和 TELEGRAM_API_HASH
go run ./cmd/telenexus
```

服务默认仅监听 `http://127.0.0.1:9487`。打开
`http://localhost:9487/login` 完成本机管理员初始化，然后进入账号列表点击
“添加账号”。首次初始化只允许本机请求；管理员密码使用 bcrypt 保存。
默认使用二维码登录，
也可以切换到手机号登录；用户名方式用于核对账号，最终仍须使用该账号扫描
二维码完成授权。

健康检查：

```sh
curl http://localhost:9487/healthz
```

未配置 API 凭据时服务仍可启动，管理台会显示“待配置 API 凭据”，登录按钮
不可提交。

管理台已启用管理员会话、HttpOnly Cookie、CSRF 校验和登录限流。默认仍
只监听本机；需要局域网访问时必须使用反向代理提供 TLS 和额外访问控制，
并显式设置 `HTTP_ADDR`。

## Docker 部署

```sh
docker compose up --build -d
docker compose ps
```

Compose 仅把宿主机端口发布到 `127.0.0.1:9487`，容器内部监听
`0.0.0.0:9487`。不得把端口映射直接改成 `0.0.0.0:9487:9487`；公网访问
必须先经过提供 TLS、身份认证和访问控制的反向代理。生产数据保存在
`telenexus-data` 卷中，停止容器不会删除该卷。`.dockerignore` 会排除
`temp/`、本地数据、授权会话和真实环境文件，避免它们进入构建上下文。
容器使用非 root 用户、只读根文件系统、`no-new-privileges` 并移除全部
Linux capabilities，只允许写入 `/data` 与受限的 `/tmp`。镜像内同时包含
`/app/telenexus-backup`，可通过
`docker compose run --rm --entrypoint /app/telenexus-backup telenexus ...`
在持久化卷上执行备份、校验和演练。

## 备份、校验与恢复

备份通过本机专用命令执行，不开放任意文件路径的 HTTP 接口。先停止写入
服务，再创建快照：

```sh
go run ./cmd/telenexus-backup create \
  --source ./data/telenexus.db \
  --output ./data/backups
go run ./cmd/telenexus-backup verify \
  --backup ./data/backups/telenexus-<timestamp>.db
go run ./cmd/telenexus-backup drill \
  --backup ./data/backups/telenexus-<timestamp>.db
```

`create` 使用 SQLite 在线快照，并验证完整性、必需表和 SHA-256；备份目录
必须仅操作者可访问，数据库与 `.manifest.json` 权限为 `0600`。`verify`
检查数据库大小、哈希、完整性及清单；`drill` 在临时目录中实际恢复并返回
各必需表记录数，演练后自动清理临时文件。恢复目标必须是新的路径，且父目录
不得是符号链接或允许组/其他用户访问：

```sh
go run ./cmd/telenexus-backup restore \
  --backup ./data/backups/telenexus-<timestamp>.db \
  --target ./data/restore/telenexus.db
```

恢复命令不会覆盖已有文件。确认恢复库与清单一致后，再将服务切换到该文件；
回滚时停止服务并切回原数据库及对应备份。授权会话目录不属于 SQLite 快照，
必须按同等敏感级别单独备份。备份、清单、恢复库和会话均不得提交到 Git。

## 账号登录

登录流程使用 Telegram MTProto：

1. 默认为 Telegram 二维码授权；二维码过期后可在页面重新生成。
2. 手机号方式会按国家区号和本地号码创建短期登录会话，再手动输入验证码。
3. 账号启用两步验证时，继续输入 Telegram 云密码。
4. 登录成功后按 Telegram 用户 ID 幂等写入账号列表，并保存授权会话。

短期登录状态仅保存在内存中；授权会话位于
`data/telegram-sessions/accounts/<telegram-id>.session`。目录权限为
`0700`，会话文件权限为 `0600`。新登录会从常见 Apple iPhone 型号、iOS
版本和 Telegram iOS 版本中随机选择设备信息，并保存到 `.session.device`
sidecar；后续连接沿用同一资料，不会使用项目名称作为设备型号。

管理台不保存手机号输入草稿、验证码或云密码。授权会话失效后，账号会显示
为“已掉线”，需要通过二维码或手机号重新授权。

账号列表支持按姓名、完整手机号、用户名和 Telegram ID 搜索，也可按状态和
账号分组筛选。筛选后可批量加入分组、逐账号重新校验授权状态或删除本地
账号；顶部刷新和单账号“校验状态”都会调用真实 Telegram 校验并持久化
结果，成功后同步更新“最近校验”时间。每个账号都保留“重新登录”入口，
在线账号也可主动重新授权；从账号 Web 发现会话失效时会直接打开该账号的
二维码登录流程。删除操作进入 24 小时待删除状态，账号页可切换“待删除”
视图并恢复；到期后的清理会遵守活动法律保留，并写入不可修改的销毁记录。

## 配置

配置从进程环境变量、系统配置页或 `.env` 读取。优先级为进程环境变量 →
系统配置 → `.env` → 默认值。系统配置在 `SETTINGS_PATH` 指定的本地 JSON
文件中保存，文件权限为 `0600`；页面保存后需重启服务。API Hash 只写不回显。
“系统配置 → 基础配置”可修改运行环境和服务监听地址；“Telegram 应用”
子标签管理 API 凭据，“运行参数”调整日志级别。其他子标签分别展示页面
布局、设备身份、登录安全和只读服务状态。

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `APP_ENV` | `development` | 运行环境 |
| `HTTP_ADDR` | `127.0.0.1:9487` | HTTP 监听地址 |
| `LOG_LEVEL` | `info` | `debug`、`info`、`warn` 或 `error` |
| `DATABASE_PATH` | `./data/telenexus.db` | SQLite 数据文件 |
| `SETTINGS_PATH` | `./data/settings.json` | 系统设置本地文件 |
| `AUTH_PATH` | `./data/auth.json` | 管理员凭据文件，权限 `0600` |
| `SHUTDOWN_TIMEOUT` | `10s` | HTTP 启动检查与关闭超时 |
| `TELEGRAM_API_ID` | 空 | Telegram 应用 ID |
| `TELEGRAM_API_HASH` | 空 | Telegram 应用 Hash |
| `TELEGRAM_SESSION_DIR` | `./data/telegram-sessions` | 授权会话目录 |

`.env`、API 凭据、手机号、验证码、云密码、授权会话、数据库和
`temp/` 均不得提交到 Git。`.env.example` 仅包含空模板。

## API

除登录、初始化和 `/healthz` 外，API 均要求管理员会话。写请求还必须携带
与 HttpOnly Cookie 配套的 `X-CSRF-Token`。

账号查询：

```sh
curl 'http://localhost:9487/api/v1/accounts?status=online&limit=20'
curl http://localhost:9487/api/v1/accounts/10001
curl -X PUT http://localhost:9487/api/v1/accounts/10001 \
  -H 'Content-Type: application/json' \
  -d '{"phone_number":"+15550001001","username":"example","display_name":"Example"}'
curl -X DELETE http://localhost:9487/api/v1/accounts/10001
curl -X POST http://localhost:9487/api/v1/accounts/10001/restore \
  -H 'Content-Type: application/json' -d '{}'
```

`DELETE` 将账号置为 24 小时待删除状态；使用
`GET /api/v1/accounts?include_deleted=true` 可查看待删除账号。宽限期结束前
可通过恢复接口撤回，进入清理阶段后只能等待重试或最终销毁。

登录相关端点：

```text
GET    /api/v1/telegram/auth/config
POST   /api/v1/telegram/auth/qr
GET    /api/v1/telegram/auth/qr/{login_id}
DELETE /api/v1/telegram/auth/qr/{login_id}
POST   /api/v1/telegram/auth/send-code
POST   /api/v1/telegram/auth/{login_id}/code
POST   /api/v1/telegram/auth/{login_id}/password
DELETE /api/v1/telegram/auth/{login_id}
```

系统配置与聊天查看：

```text
GET    /api/v1/settings
PUT    /api/v1/settings
POST   /api/v1/accounts/refresh
POST   /api/v1/accounts/{id}/refresh
POST   /api/v1/accounts/{id}/restore
GET    /api/v1/accounts/{id}/chats
GET    /api/v1/accounts/{id}/search?q={username}
GET    /api/v1/accounts/{id}/chats/{type}/{peer_id}/messages
POST   /api/v1/accounts/{id}/chats/{type}/{peer_id}/messages/forward
POST   /api/v1/accounts/{id}/chats/{type}/{peer_id}/messages/delete
GET    /api/v1/accounts/{id}/profile
PUT    /api/v1/accounts/{id}/profile
GET    /api/v1/accounts/{id}/chats/{type}/{peer_id}/profile
GET    /api/v1/accounts/{id}/chats/{type}/{peer_id}/avatar
GET    /api/v1/dashboard
GET    /api/v1/groups
POST   /api/v1/groups
DELETE /api/v1/groups/{id}
PUT    /api/v1/groups/{id}/accounts
GET    /api/v1/monitor/rules
POST   /api/v1/monitor/rules
PUT    /api/v1/monitor/rules/{id}
DELETE /api/v1/monitor/rules/{id}
GET    /api/v1/monitor/channels
POST   /api/v1/monitor/run
GET    /api/v1/monitor/status
GET    /api/v1/monitor/alerts
POST   /api/v1/monitor/alerts/{id}/resolve
GET    /api/v1/messages
POST   /api/v1/messages/cleanup
GET    /api/v1/retention
PUT    /api/v1/retention
GET    /api/v1/retention/legal-holds
POST   /api/v1/retention/legal-holds
DELETE /api/v1/retention/legal-holds/{id}
GET    /api/v1/reports?days=7
```

聊天接口只读取账号目录中与 `{id}` 对应的授权会话，`type` 支持 `user`、
`chat` 和 `channel`。消息历史使用 `before_id` 和 `limit` 分页。点击
“打开 Web”进入独立客户端后，可搜索用户名、查看真实头像、打开消息右键
菜单、多选后转发或删除消息，并查看或编辑当前账号资料。删除消息时可选择
“仅为我删除”或“为所有人删除”，默认使用更安全的仅本地删除。头像按需在浏览器
读取，不会下载为本地会话归档。

账号分组、监控规则、告警和消息索引保存在同一个 SQLite 数据库。监控调度器
每分钟读取启用规则；第一次运行只建立最新消息基线，不会把大量历史消息回填
为告警。规则可关联通知渠道，当前内置“站内通知”；渠道配置不包含接码服务，
也不会保存手机号、验证码或登录链接。消息索引默认保留 30 天，可在浏览器中
检索，不提供会话文件下载。采集批次保存内容指纹；使用相同 `batch_id` 和
相同内容重试会返回原结果，不会重复写入证据或告警，相同 ID 的不同内容会被
明确拒绝。

清理请求可使用 `{"dry_run":true}` 预览候选消息、证据数量和被法律保留阻断
的消息数量。正式清理会在同一事务中重新检查活动法律保留，删除消息投影和
对应证据、清空告警预览并写入不可修改的销毁记录；无法确认删除范围时整批
回滚。法律保留支持 `account`、`chat` 和 `message` 范围，解除后仍保留原始
保留记录与操作者。自动清理每次最多处理 1000 条过期消息，并通过
`has_more` 指示后续周期是否仍需处理。

管理台工作台、分组、监控、消息记录和报表页面均读取真实接口，没有数据时
显示空状态，不生成虚构统计。

错误响应统一为：

```json
{"error":{"code":"invalid_code","message":"验证码不正确"}}
```

## 开发与验证

```sh
gofmt -w cmd internal
go test -race ./...
go vet ./...
go build -o bin/telenexus ./cmd/telenexus
```

源码按 `cmd/telenexus/` 入口和 `internal/` 领域包组织。管理台使用 Go
`html/template` 与原生 CSS/JavaScript，不依赖 Node 构建。真实 Telegram
联调需要有效 API 凭据和可接收验证码的测试账号；自动化测试使用驱动接口
模拟 Telegram，不访问外部账号。
