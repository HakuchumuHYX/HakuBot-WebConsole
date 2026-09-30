# HakuBot WebConsole

HakuBot WebConsole 是一个独立的 Go 网页服务，用来查看 HakuBot/NoneBot
对事件产生的真实响应、失败记录、完整警告和错误日志，以及 Bot 当前在线状态。

它只记录实际发送回复或发生故障的 Matcher。普通经过 Matcher 检查、但没有触发
回复也没有异常的事件不会进入控制台。

## 功能

- 按成功/失败、GMT+8 时间范围、群号和插件筛选事件响应；带 WARNING 的成功响应单独标识。
- 页面每 3 秒刷新当前视图；快捷时间使用滑动窗口。
- 展示 Bot QQ 和当前在线/离线，不保存掉线/恢复历史。
- 对 WARNING、ERROR、CRITICAL 和失败响应保存完整输入、输出、日志及 traceback。
- SQLite、WAL 和磁盘 spool 持久化；存储页展示占用、记录时间范围和自动保留策略。
- 按自选 GMT+8 截止时间预览、清理并增量回收旧日志。
- 服务器 CPU、内存、磁盘、网络的实时状态和 7 天历史曲线。
- systemd watchdog、崩溃自动重启和 Nginx HTTPS 反向代理。

## 两个仓库的边界

WebConsole 与 HakuBot 是两个独立仓库：

```text
HakuBot repository
└── plugins/webconsole_bridge/   # 采集并写入 SQLite

webconsole repository
├── cmd/                         # Go 入口
├── internal/                    # API、查询、存储、系统监控和 SQLite schema
├── web/                         # 内嵌网页
└── deploy/                      # 通用部署模板
```

两个进程不通过 HTTP 逐事件推送。它们通过同一个 SQLite 文件和 spool 目录协作，
因此两边配置的 `database_path` 和 `spool_path` 必须完全一致。

WebConsole 每次启动时幂等执行 `internal/database/schema.sql`（`CREATE ... IF NOT EXISTS`），
bridge 不自行建表，只检查它要写入的表是否存在。首次部署时建议先启动 WebConsole，再启动
HakuBot。如果顺序相反，bridge 会在启动时 warning 一次，并在存储可用后自动恢复采集。

`IF NOT EXISTS` 不会修改已存在的表。调整已有表结构时，需要单独写一次性 SQL 脚本手动执行。

## 要求

- Linux x86-64。
- Go 1.25 或兼容版本。
- HakuBot、NoneBot 2 和 OneBot V11 Adapter。
- Nginx。
- systemd（推荐）。
- 公网部署必须使用可信 HTTPS 和独立 Basic Auth。

## 1. 获取源码和构建

```bash
git clone <WEB_CONSOLE_REPOSITORY_URL> webconsole
cd webconsole
CGO_ENABLED=0 go build -buildvcs=false -trimpath \
  -ldflags='-s -w' -o bin/webconsole ./cmd/webconsole
```

生成的是不依赖 CGO 运行库的单文件二进制。

## 2. 创建共享数据目录

以下示例使用 `/var/lib/hakubot-webconsole`。也可以选择其他绝对路径，但两个仓库的
配置必须保持一致。

```bash
sudo groupadd --system webconsole
sudo useradd --system --gid webconsole --home-dir /nonexistent \
  --shell /usr/sbin/nologin webconsole
sudo install -d -o webconsole -g webconsole -m 2770 \
  /var/lib/hakubot-webconsole
sudo install -d -o webconsole -g webconsole -m 2770 \
  /var/lib/hakubot-webconsole/spool
```

如果 HakuBot 不以 root 运行，把它的服务用户加入 `webconsole` 组：

```bash
sudo usermod -aG webconsole <HAKUBOT_SERVICE_USER>
```

修改组后需要重启 HakuBot 服务，使新组权限生效。

## 3. 配置 WebConsole

复制示例：

```bash
cp config.example.json config.json
```

`config.json` 不进入 Git。配置结构：

```json
{
  "listen": "127.0.0.1:54322",
  "database_path": "/var/lib/hakubot-webconsole/webconsole.db",
  "spool_path": "/var/lib/hakubot-webconsole/spool",
  "public_origin": "https://console.example.com",
  "retention_days": 90
}
```

字段说明（全部必填）：

| 字段 | 说明 |
| --- | --- |
| `listen` | Go 后端监听地址，必须是字面量回环 IP |
| `database_path` | SQLite 文件的绝对路径 |
| `spool_path` | 高优先级完整诊断 spool 的绝对路径 |
| `public_origin` | 浏览器实际访问的 HTTPS Origin（非 443 端口要带上端口）；清理接口只接受这个 Origin 发起的请求 |
| `retention_days` | 自动保留天数；`0` 表示关闭自动清理 |

程序只读取当前工作目录下的 `config.json`，systemd 模板通过 `WorkingDirectory` 指定工作目录。

## 4. 配置 HakuBot bridge

在 HakuBot 仓库中执行：

```bash
cd plugins/webconsole_bridge
cp config.example.json config.json
```

填写：

```json
{
  "enabled": true,
  "database_path": "/var/lib/hakubot-webconsole/webconsole.db",
  "spool_path": "/var/lib/hakubot-webconsole/spool",
  "probe_interval_seconds": 60
}
```

`config.json` 是 HakuBot 本地运行配置，不进入 Git。路径必须是绝对路径。

## 5. 首次启动

先运行 WebConsole，让它创建数据库和表：

```bash
./bin/webconsole
```

本机检查：

```bash
curl http://127.0.0.1:54322/healthz
curl http://127.0.0.1:54322/readyz
```

`/readyz` 成功后再启动 HakuBot。bridge 会探测 schema 和目录权限，并开始异步采集。

## 6. WebConsole systemd

复制 `deploy/webconsole.service`，替换：

```text
__INSTALL_ROOT__  WebConsole 仓库绝对路径
__DATA_DIR__      共享数据目录绝对路径
```

然后安装：

```bash
sudo install -m 0644 deploy/webconsole.service \
  /etc/systemd/system/webconsole.service
sudo systemctl daemon-reload
sudo systemctl enable --now webconsole.service
```

模板使用：

- `Restart=always`
- `WatchdogSec=30`
- 受限 `webconsole` 用户
- `ProtectSystem=strict`
- 只允许写共享数据目录

## 7. Nginx、HTTPS 和 Basic Auth

Go 后端只能监听 `127.0.0.1:54322`，公网入口由 Nginx 在 443 上提供，建议用独立子域名
（例如 `console.example.com`）。Nginx 只负责 TLS、Basic Auth 和限流；请求方法、清理接口的
Origin 校验以及 HSTS 以外的全部响应头都由 Go 处理。

复制 `deploy/nginx-webconsole.conf.example`，替换占位符：

```text
__SERVER_NAME__
__TLS_FULLCHAIN_PATH__
__TLS_PRIVATE_KEY_PATH__
__HTPASSWD_PATH__
__NGINX_LOG_DIR__
```

证书只要是浏览器信任的即可，例如 Let's Encrypt 的域名或通配符证书，续期后 reload Nginx。

还要在 Nginx 的 `http {}` 中加入模板顶部注明的：

```nginx
limit_req_zone $binary_remote_addr zone=webconsole:10m rate=10r/s;
limit_conn_zone $binary_remote_addr zone=perip:10m;
log_format webconsole
    '$remote_addr - $remote_user [$time_local] '
    '"$request_method $uri $server_protocol" $status $body_bytes_sent '
    '"$http_referer" "$http_user_agent"';
```

access log 使用 `$uri`，不会记录查询字符串。

创建独立 bcrypt Basic Auth：

```bash
sudo htpasswd -B -c /etc/nginx/.htpasswd_webconsole console
sudo chown root:www-data /etc/nginx/.htpasswd_webconsole
sudo chmod 0640 /etc/nginx/.htpasswd_webconsole
```

实际 Nginx worker 组可能是 `www-data`、`nginx` 或面板自定义用户，请按服务器配置调整。

`nginx -t` 通过后再 reload。不要把 `127.0.0.1:54322` 暴露到公网。

## 8. 数据与隐私

数据库可能包含完整群消息、媒体 URL、API 输入输出和 traceback。以下内容绝不能
提交到 Git：

```text
config.json
data/
bin/
*.db
*.db-wal
*.db-shm
*.log
*.pem
*.key
*.htpasswd
```

项目 `.gitignore` 还会排除私有实施文档和渲染后的部署文件。

## 9. 迁移服务器

迁移时不需要回填旧日志。标准流程：

1. 分别 clone HakuBot 和 WebConsole。
2. 构建 WebConsole。
3. 创建共享数据目录和用户组。
4. 从两个 `config.example.json` 生成各自的私有 `config.json`。
5. 保证两边 SQLite 和 spool 路径一致。
6. 启动 WebConsole，等待 `/readyz`。
7. 启动 HakuBot。
8. 安装 WebConsole systemd，以及 Nginx 子域名站点、证书和 Basic Auth。
9. 把域名解析到新服务器，验证 HTTPS 和认证可用、`127.0.0.1:54322` 不可从公网访问、
   Bot 在线状态和实际事件响应正常。

数据库无需从旧服务器复制时，新实例会从空 schema 开始，不读取或回填
`output.log`。
