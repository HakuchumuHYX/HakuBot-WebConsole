# HakuBot WebConsole

HakuBot WebConsole 是一个独立的 Go 网页服务，用来查看 HakuBot/NoneBot
对事件产生的真实响应、失败记录、完整警告和错误日志，以及 Bot 当前在线状态。

它只记录实际发送回复或发生故障的 Matcher。普通经过 Matcher 检查、但没有触发
回复也没有异常的事件不会进入控制台。

## 功能

- 按成功/失败、GMT+8 时间范围、群号和插件筛选事件响应。
- 页面每 3 秒刷新当前视图；快捷时间使用滑动窗口。
- 展示 Bot 当前在线或离线，不保存掉线/恢复历史。
- 对 WARNING、ERROR、CRITICAL 和失败响应保存完整输入、输出、日志及 traceback。
- SQLite、WAL 和磁盘 spool 持久化。
- 按自选 GMT+8 截止时间预览、清理并增量回收旧日志。
- systemd watchdog、崩溃自动重启和 Nginx HTTPS 反向代理。

## 两个仓库的边界

WebConsole 与 HakuBot 是两个独立仓库：

```text
HakuBot repository
└── plugins/webconsole_bridge/   # 采集并写入 SQLite

webconsole repository
├── cmd/                         # Go 入口
├── internal/                    # API、查询、存储和安全逻辑
├── migrations/                  # SQLite schema
├── web/                         # 内嵌网页
└── deploy/                      # 通用部署模板
```

两个进程不通过 HTTP 逐事件推送。它们通过同一个 SQLite 文件和 spool 目录协作，
因此两边配置的 `database_path` 和 `spool_path` 必须完全一致。

WebConsole 负责初始化和升级数据库 schema；bridge 不自行创建 schema。首次部署时
建议先启动 WebConsole，再启动 HakuBot。如果顺序相反，bridge 会在启动时 warning
一次，并在存储可用后自动恢复采集。

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
  "public_origin": "https://203.0.113.10:54321",
  "retention_days": 90,
  "token_secret": "CHANGE_ME_NOT_VALID_BASE64!"
}
```

字段说明：

| 字段 | 说明 |
| --- | --- |
| `listen` | Go 后端监听地址，必须是字面量回环 IP |
| `database_path` | SQLite 文件的绝对路径 |
| `spool_path` | 高优先级完整诊断 spool 的绝对路径 |
| `public_origin` | 浏览器实际访问的 HTTPS Origin，包含非标准端口 |
| `retention_days` | 自动保留天数；`0` 表示关闭自动清理 |
| `token_secret` | 游标、CSRF 和清理确认令牌使用的随机密钥 |

生成随机密钥：

```bash
openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n'
```

`token_secret` 至少需要 32 字节强度的 raw URL-safe Base64。生产环境必须持久保存；
不要依赖程序每次启动时临时生成。

默认读取当前工作目录的 `config.json`。可通过以下环境变量覆盖任意配置：

```text
WEBCONSOLE_CONFIG
WEBCONSOLE_LISTEN
WEBCONSOLE_DB_PATH
WEBCONSOLE_SPOOL_PATH
WEBCONSOLE_PUBLIC_ORIGIN
WEBCONSOLE_RETENTION_DAYS
WEBCONSOLE_TOKEN_SECRET
```

优先级为：环境变量、`config.json`、非敏感默认值。

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

先运行 WebConsole，让 migration 创建数据库：

```bash
./bin/webconsole
```

本机检查：

```bash
curl http://127.0.0.1:54322/healthz
curl http://127.0.0.1:54322/readyz
```

`/readyz` 成功后再启动 HakuBot。bridge 会探测 schema 和目录权限，并开始异步采集。

## 6. systemd

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

Go 后端只能监听 `127.0.0.1:54322`，公网端口由 Nginx 提供。

部署目录包含：

```text
deploy/nginx-webconsole.conf.example
deploy/nginx-webconsole-acme.conf
deploy/webconsole_proxy.inc
deploy/certbot-deploy-hook.sh
```

替换模板中的占位符：

```text
__PUBLIC_IP_OR_HOST__
__TLS_FULLCHAIN_PATH__
__TLS_PRIVATE_KEY_PATH__
__HTPASSWD_PATH__
__NGINX_LOG_DIR__
__WEBCONSOLE_PROXY_INCLUDE__
__ACME_WEBROOT__
```

还要在 Nginx 的 `http {}` 中加入模板顶部注明的：

```nginx
limit_req_zone $binary_remote_addr zone=webconsole:10m rate=10r/s;
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

如果使用 Let’s Encrypt IP 地址证书，需要支持 IP issuance 的 Certbot 版本，并为
HTTP-01 保持 `/.well-known/acme-challenge/` 可访问。证书续期后使用
`certbot-deploy-hook.sh` 校验并 reload Nginx。标准 Nginx 无需修改；自定义安装可设置：

```text
NGINX_BIN
NGINX_CONFIG
```

只有在 `nginx -t` 成功、HTTPS 和 Basic Auth 已验证后，才开放公网 TCP 54321。
不要把 `127.0.0.1:54322` 暴露到公网。

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

项目 `.gitignore` 还会排除本地测试、私有实施文档和渲染后的部署文件。

首次提交前检查：

```bash
git add .
git status --short
git ls-files
git check-ignore -v config.json
git check-ignore -v data/webconsole.db
```

确认暂存内容中没有真实公网 IP、Bot QQ、密码、Token、数据库或服务器配置快照后，
再创建提交。

## 9. 迁移服务器

迁移时不需要回填旧日志。标准流程：

1. 分别 clone HakuBot 和 WebConsole。
2. 构建 WebConsole。
3. 创建共享数据目录和用户组。
4. 从两个 `config.example.json` 生成各自的私有 `config.json`。
5. 保证两边 SQLite 和 spool 路径一致。
6. 启动 WebConsole，等待 `/readyz`。
7. 启动 HakuBot。
8. 安装 systemd、Nginx、HTTPS 和 Basic Auth。
9. 验证公网 54321 可达、54322 不可达、Bot 在线状态和实际事件响应。

数据库无需从旧服务器复制时，新实例会从空 schema 开始，不读取或回填
`output.log`。
