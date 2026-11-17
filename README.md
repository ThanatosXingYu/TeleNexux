# TeleNexus

TeleNexus 是使用 Go 开发的 Telegram 多账号管理服务。当前提供管理台、
账号元数据管理、SQLite 持久化、MTProto 二维码/手机号登录、可选两步验证、
账号会话搜索、个人资料编辑、消息转发/删除、系统配置、健康检查和 REST API。

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
`http://localhost:9487/accounts`，点击“添加账号”。默认使用二维码登录，
也可以切换到手机号登录；用户名方式用于核对账号，最终仍须使用该账号扫描
二维码完成授权。

健康检查：

```sh
curl http://localhost:9487/healthz
```

未配置 API 凭据时服务仍可启动，管理台会显示“待配置 API 凭据”，登录按钮
不可提交。

当前阶段尚未实现管理员登录与权限控制，因此默认只监听本机。不要将管理台
直接暴露到公网；需要局域网访问时必须先增加反向代理鉴权并显式设置
`HTTP_ADDR`。

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
| `SHUTDOWN_TIMEOUT` | `10s` | HTTP 启动检查与关闭超时 |
| `TELEGRAM_API_ID` | 空 | Telegram 应用 ID |
| `TELEGRAM_API_HASH` | 空 | Telegram 应用 Hash |
| `TELEGRAM_SESSION_DIR` | `./data/telegram-sessions` | 授权会话目录 |

`.env`、API 凭据、手机号、验证码、云密码、授权会话、数据库和
`temp/` 均不得提交到 Git。`.env.example` 仅包含空模板。

## API

账号查询：

```sh
curl 'http://localhost:9487/api/v1/accounts?status=online&limit=20'
curl http://localhost:9487/api/v1/accounts/10001
curl -X PUT http://localhost:9487/api/v1/accounts/10001 \
  -H 'Content-Type: application/json' \
  -d '{"phone_number":"+15550001001","username":"example","display_name":"Example"}'
curl -X DELETE http://localhost:9487/api/v1/accounts/10001
```

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
GET    /api/v1/accounts/{id}/chats
GET    /api/v1/accounts/{id}/search?q={username}
GET    /api/v1/accounts/{id}/chats/{type}/{peer_id}/messages
POST   /api/v1/accounts/{id}/chats/{type}/{peer_id}/messages/forward
POST   /api/v1/accounts/{id}/chats/{type}/{peer_id}/messages/delete
GET    /api/v1/accounts/{id}/profile
PUT    /api/v1/accounts/{id}/profile
GET    /api/v1/accounts/{id}/chats/{type}/{peer_id}/profile
GET    /api/v1/accounts/{id}/chats/{type}/{peer_id}/avatar
```

聊天接口只读取账号目录中与 `{id}` 对应的授权会话，`type` 支持 `user`、
`chat` 和 `channel`。消息历史使用 `before_id` 和 `limit` 分页。点击
“打开 Web”进入独立客户端后，可搜索用户名、查看真实头像、打开消息右键
菜单、多选后转发或删除消息，并查看或编辑当前账号资料。删除消息时可选择
“仅为我删除”或“为所有人删除”，默认使用更安全的仅本地删除。头像按需在浏览器
读取，不会下载为本地会话归档。

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
