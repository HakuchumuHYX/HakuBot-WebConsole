# Deployment templates

完整的双仓库配置、构建、systemd、Nginx、HTTPS、Basic Auth 和迁移说明见仓库
根目录的 `README.md`。

本目录下的模板包含占位符，不能未经替换直接安装：

```text
__INSTALL_ROOT__
__DATA_DIR__
__SERVER_NAME__
__TLS_FULLCHAIN_PATH__
__TLS_PRIVATE_KEY_PATH__
__HTPASSWD_PATH__
__NGINX_LOG_DIR__
```
