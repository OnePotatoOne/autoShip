#!/usr/bin/env bash
# install-server.sh —— 在 x86_64 Linux 服务器上安装或升级 autoShip Server（支持用户级 systemctl --user 免 root 部署）
# 文件编码: UTF-8
#
# 用法:
#   ./install-server.sh <autoship-server二进制路径> [版本号，如 v0.1.0] [Tailnet监听地址，如 100.90.160.50:9444] [公网监听地址，如 0.0.0.0:9443]

set -euo pipefail

BIN_SRC="${1:-}"
VERSION="${2:-v0.1.0}"
TAILNET_ADDR="${3:-127.0.0.1:9444}"
PUBLIC_ADDR="${4:-0.0.0.0:9443}"

if [[ -z "${BIN_SRC}" || ! -f "${BIN_SRC}" ]]; then
  echo "用法: $0 <autoship-server二进制路径> [版本号] [Tailnet监听地址] [公网监听地址]" >&2
  exit 1
fi

APP_DIR="${HOME}/apps/autoship"
CFG_DIR="${APP_DIR}/configs"
DATA_DIR="${APP_DIR}/data"
LOG_DIR="${APP_DIR}/logs"
SYSTEMD_USER_DIR="${HOME}/.config/systemd/user"

mkdir -p "${APP_DIR}" "${CFG_DIR}" "${DATA_DIR}" "${LOG_DIR}" "${SYSTEMD_USER_DIR}"
chmod 700 "${CFG_DIR}" "${DATA_DIR}"

VERSIONED_BIN="${APP_DIR}/autoship-server-${VERSION}-linux-amd64"
cp -f "${BIN_SRC}" "${VERSIONED_BIN}"
chmod 755 "${VERSIONED_BIN}"
sha256sum "${VERSIONED_BIN}" > "${APP_DIR}/SHA256SUMS-${VERSION}"
ln -sfn "${VERSIONED_BIN}" "${APP_DIR}/autoship-server"

TAILNET_IP="${TAILNET_ADDR%:*}"
CFG_FILE="${CFG_DIR}/server.yaml"
if [[ ! -f "${CFG_FILE}" ]]; then
  cat > "${CFG_FILE}" <<EOF
tailnetListenAddr: "${TAILNET_ADDR}"
tailnetSanIPs:
  - "127.0.0.1"
  - "${TAILNET_IP}"
publicListenAddr: "${PUBLIC_ADDR}"
publicSanIPs:
  - "127.0.0.1"
publicAllowIPs: []
dataDir: "${DATA_DIR}"
cookiePrefix: "autoship_"
artifactKeepCount: 10
database:
  host: "127.0.0.1"
  port: 3306
  user: "autoship"
  password: ""
  dbName: "autoship"
  maxOpenConns: 32
  maxIdleConns: 8
  connMaxLifetime: 1800
llm:
  mode: "relay"
  intakeModel: "intake"
  triageModel: "triage"
  maxSessionTokens: 64000
  maxRequestTokens: 4096
  maskPIIBeforeSend: true
notify:
  enabled: false
  feishuWebhookURL: ""
EOF
  chmod 600 "${CFG_FILE}"
fi

cat > "${SYSTEMD_USER_DIR}/autoship-server.service" <<EOF
[Unit]
Description=autoShip Server (${VERSION})
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${APP_DIR}
ExecStart=${APP_DIR}/autoship-server --config ${CFG_FILE}
Restart=on-failure
RestartSec=5
NoNewPrivileges=true

[Install]
WantedBy=default.target
EOF

systemctl --user daemon-reload
systemctl --user enable autoship-server.service
systemctl --user restart autoship-server.service
sleep 1
systemctl --user --no-pager --full status autoship-server.service || true
echo "=========================================="
echo "autoShip Server 已部署完成：https://${TAILNET_ADDR}"
