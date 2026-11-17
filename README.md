# TeleNexus

TeleNexus 是用于管理多个 Telegram 账号的 Go 服务。当前第一阶段提供账号
元数据管理、SQLite 持久化、健康检查和 REST API。

> **当前边界：** 尚未接入 Telegram MTProto。账号默认状态为 `pending`；
> 真实登录、消息采集和监控任务将在后续阶段接入。

## 快速开始

需要 Go 1.24 或更高版本。

```sh
cp .env.example .env
go run ./cmd/telenexus
```

服务默认监听 `http://localhost:9487`。检查运行状态：

```sh
curl http://localhost:9487/healthz
```

## API

创建账号；`id` 为 Telegram 用户 ID，不是数据库自增 ID：

```sh
curl -X POST http://localhost:9487/api/v1/accounts \
  -H 'Content-Type: application/json' \
  -d '{"id":10001,"phone_number":"+8613800138000","username":"demo","display_name":"Demo Account"}'
```

查询账号：

```sh
curl 'http://localhost:9487/api/v1/accounts?status=pending&limit=20'
curl http://localhost:9487/api/v1/accounts/10001
```

错误响应统一为：

```json
{"error":{"code":"invalid_account","message":"phone_number must contain between 8 and 20 digits, with optional leading +"}}
```

## 配置

配置仅从环境变量读取，启动时校验，错误配置会导致服务拒绝启动。

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `APP_ENV` | `development` | 运行环境 |
| `HTTP_ADDR` | `:9487` | HTTP 监听地址 |
| `LOG_LEVEL` | `info` | `debug`、`info`、`warn` 或 `error` |
| `DATABASE_PATH` | `./data/telenexus.db` | SQLite 数据文件 |
| `SHUTDOWN_TIMEOUT` | `10s` | HTTP 启动检查与关闭超时 |

`.env`、Telegram API 凭据、会话密钥和生产数据库不得提交。`temp/` 用于
本地缓存、环境、依赖和参考资料，同样不得提交。

## 开发

```sh
go test -race ./...
go vet ./...
go build -o bin/telenexus ./cmd/telenexus
```

源码按 `cmd/` 入口和 `internal/` 领域包组织。接口依赖通过小接口注入，
账号逻辑不依赖 HTTP 或 SQLite，便于后续增加 Telegram 连接器。
