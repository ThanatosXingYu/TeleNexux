# TeleNexus

TeleNexus 是使用 Go 开发的 Telegram 多账号管理服务。当前提供管理台、
账号元数据管理、SQLite 持久化、MTProto 手机号登录、可选两步验证、
接码链接轮询、账号聊天查看、系统配置、健康检查和 REST API。

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
`http://localhost:9487/accounts`，点击“登录 Telegram 账号”，按页面提示
输入手机号、验证码及可选的云密码。

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

1. 发送验证码并创建短期登录会话。
2. 手动输入验证码，或从白名单接码地址轮询读取。
3. 账号启用两步验证时，继续输入 Telegram 云密码。
4. 登录成功后按 Telegram 用户 ID 幂等写入账号列表，并保存授权会话。

短期登录状态仅保存在内存中；授权会话位于
`data/telegram-sessions/accounts/<telegram-id>.session`。目录权限为
`0700`，会话文件权限为 `0600`。新登录会从常见 Apple iPhone 型号、iOS
版本和 Telegram iOS 版本中随机选择设备信息，并保存到 `.session.device`
sidecar；后续连接沿用同一资料，不会使用项目名称作为设备型号。

接码地址必须使用 HTTPS 默认端口，且主机必须出现在
`TELEGRAM_CODE_FETCH_HOSTS` 中。服务会在请求和重定向时校验 URL，并在
TCP 连接阶段拒绝环回、私网、链路本地、保留地址及非 443 端口，以降低
SSRF 和 DNS 重绑定风险。接码域名通过 `TELEGRAM_CODE_DNS_SERVER` 指定的
公共 DNS 解析，避免本地透明代理返回保留地址；网络错误日志不会记录完整
接码链接。

## 配置

配置从进程环境变量、系统配置页或 `.env` 读取。优先级为进程环境变量 →
系统配置 → `.env` → 默认值。系统配置在 `SETTINGS_PATH` 指定的本地 JSON
文件中保存，文件权限为 `0600`；页面保存后需重启服务。API Hash 只写不回显。
“系统配置 → 基础配置”可修改运行环境、日志级别、Telegram API 凭据、
接码网络、服务监听地址和接码 DNS。

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
| `TELEGRAM_CODE_FETCH_HOSTS` | 空 | 允许的接码主机，逗号分隔 |
| `TELEGRAM_CODE_DNS_SERVER` | `1.1.1.1:53` | 接码域名使用的 DNS 服务器 |

`.env`、API 凭据、手机号、验证码、云密码、接码链接、授权会话、数据库和
`temp/` 均不得提交到 Git。`.env.example` 仅包含空模板。

## API

账号查询：

```sh
curl 'http://localhost:9487/api/v1/accounts?status=online&limit=20'
curl http://localhost:9487/api/v1/accounts/10001
```

登录相关端点：

```text
GET    /api/v1/telegram/auth/config
POST   /api/v1/telegram/auth/send-code
POST   /api/v1/telegram/auth/{login_id}/code
POST   /api/v1/telegram/auth/{login_id}/password
POST   /api/v1/telegram/auth/{login_id}/fetch-code
DELETE /api/v1/telegram/auth/{login_id}
```

系统配置与聊天查看：

```text
GET    /api/v1/settings
PUT    /api/v1/settings
GET    /api/v1/accounts/{id}/chats
GET    /api/v1/accounts/{id}/chats/{type}/{peer_id}/messages
```

聊天接口只读取账号目录中与 `{id}` 对应的授权会话，`type` 支持 `user`、
`chat` 和 `channel`。消息历史使用 `before_id` 和 `limit` 分页。在账号列表
点击账号行或“查看聊天”按钮进入会话查看页，页面会自动打开最近的会话；
切换左侧会话即可在浏览器中查看消息，无需下载会话文件。

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
