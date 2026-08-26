# Deployment templates

完整的双仓库配置、构建、systemd、Nginx、HTTPS、Basic Auth 和迁移说明见仓库
根目录的 `README.md`。

`setup-hermes-dashboard-auth.sh` 是唯一会处理 Hermes Dashboard 密码的部署辅助
脚本：它交互读取密码并仅将哈希写入 `/etc/hermes-dashboard.env`。该文件不能复制到
本目录、不能提交 Git，也不能输出到部署日志。

本目录下的模板包含占位符，不能未经替换直接安装：

```text
__INSTALL_ROOT__
__DATA_DIR__
__PUBLIC_IP_OR_HOST__
__TLS_FULLCHAIN_PATH__
__TLS_PRIVATE_KEY_PATH__
__HTPASSWD_PATH__
__NGINX_LOG_DIR__
__WEBCONSOLE_PROXY_INCLUDE__
__ACME_WEBROOT__
```

`certbot-deploy-hook.sh` 默认调用 PATH 中的 `nginx` 和
`/etc/nginx/nginx.conf`。自定义 Nginx 安装可通过 `NGINX_BIN` 和
`NGINX_CONFIG` 覆盖。
