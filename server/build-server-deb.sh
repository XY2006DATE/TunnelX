#!/bin/bash
set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 配置变量
VERSION="0.1.0"
ARCH="amd64"
PACKAGE_NAME="tunnelx-server"
BUILD_DIR="$(pwd)/package/${PACKAGE_NAME}"
DEB_FILE="$(pwd)/package/${PACKAGE_NAME}_${VERSION}_${ARCH}.deb"

echo -e "${GREEN}=== TunnelX Server DEB Package Builder ===${NC}"
echo "Version: ${VERSION}"
echo "Architecture: ${ARCH}"
echo ""

# 1. 编译服务端
echo -e "${YELLOW}Step 1: Building server binary...${NC}"
cd server
mkdir -p bin
go build -o bin/tunnelx-server -ldflags="-s -w" .
if [ ! -f "bin/tunnelx-server" ]; then
    echo -e "${RED}Build failed!${NC}"
    exit 1
fi
echo -e "${GREEN}Build successful: $(ls -lh bin/tunnelx-server | awk '{print $5}')${NC}"
cd ..

# 2. 清理旧的打包目录
echo -e "${YELLOW}Step 2: Cleaning old package directory...${NC}"
rm -rf "${BUILD_DIR}"
rm -f "${DEB_FILE}"

# 3. 创建deb包目录结构
echo -e "${YELLOW}Step 3: Creating package structure...${NC}"
mkdir -p "${BUILD_DIR}/DEBIAN"
mkdir -p "${BUILD_DIR}/usr/local/bin"
mkdir -p "${BUILD_DIR}/etc/tunnelx"
mkdir -p "${BUILD_DIR}/lib/systemd/system"
mkdir -p "${BUILD_DIR}/var/log"

# 4. 复制文件
echo -e "${YELLOW}Step 4: Copying files...${NC}"
cp server/bin/tunnelx-server "${BUILD_DIR}/usr/local/bin/"
chmod 755 "${BUILD_DIR}/usr/local/bin/tunnelx-server"

# 5. 创建配置文件
echo -e "${YELLOW}Step 5: Creating configuration files...${NC}"
cat > "${BUILD_DIR}/etc/tunnelx/server.yaml" << 'EOF'
server:
    bind_addr: 0.0.0.0
    bind_port: 7100
auth:
    method: token
    token: your_secret_token_here
tls:
    enable: false
    cert_file: ""
    key_file: ""
log:
    level: info
    file: /var/log/tunnelx-server.log
    max_size: 100
    max_backups: 3
dashboard:
    enable: true
    port: 7100
    password_file: ""
port_pool:
    start: 3001
    end: 20000
heartbeat_timeout: 90
EOF

# 将服务端配置声明为 Debian conffile。升级软件包时保留管理员已经
# 修改过的端口、令牌与 HTTPS 证书配置。
cat > "${BUILD_DIR}/DEBIAN/conffiles" << 'EOF'
/etc/tunnelx/server.yaml
EOF

# 6. 创建systemd服务文件
cat > "${BUILD_DIR}/lib/systemd/system/tunnelx-server.service" << 'EOF'
[Unit]
Description=TunnelX NAT Traversal Server
After=network.target

[Service]
Type=simple
User=tunnelx
Group=tunnelx
ExecStart=/usr/local/bin/tunnelx-server /etc/tunnelx/server.yaml
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

# 7. 创建control文件
cat > "${BUILD_DIR}/DEBIAN/control" << EOF
Package: ${PACKAGE_NAME}
Version: ${VERSION}
Section: net
Priority: optional
Architecture: ${ARCH}
Maintainer: TunnelX Team <admin@tunnelx.io>
Description: TunnelX NAT Traversal Server
 TunnelX is a powerful NAT traversal solution that enables remote access
 to services behind firewalls and NAT without port forwarding.
 This package contains the server component.
EOF

# 8. 创建postinst脚本
cat > "${BUILD_DIR}/DEBIAN/postinst" << 'EOF'
#!/bin/bash
set -e

# 创建系统用户
if ! id tunnelx >/dev/null 2>&1; then
    useradd -r -s /bin/false -d /nonexistent tunnelx
fi

# 设置目录和文件权限
chown -R tunnelx:tunnelx /etc/tunnelx
chmod 755 /etc/tunnelx
chmod 644 /etc/tunnelx/server.yaml

# 创建日志文件
touch /var/log/tunnelx-server.log
chown tunnelx:tunnelx /var/log/tunnelx-server.log
chmod 644 /var/log/tunnelx-server.log

# 重载systemd并启用服务
systemctl daemon-reload
systemctl enable tunnelx-server.service

echo "TunnelX Server installed successfully!"
echo "Configuration files are located in /etc/tunnelx/"
echo "Start the service with: systemctl start tunnelx-server"

exit 0
EOF

# 9. 创建prerm脚本
cat > "${BUILD_DIR}/DEBIAN/prerm" << 'EOF'
#!/bin/bash
set -e

# 停止服务
if systemctl is-active --quiet tunnelx-server; then
    systemctl stop tunnelx-server
fi

# 禁用服务
systemctl disable tunnelx-server || true

exit 0
EOF

# 10. 设置脚本权限
chmod 755 "${BUILD_DIR}/DEBIAN/postinst"
chmod 755 "${BUILD_DIR}/DEBIAN/prerm"

# 11. 构建deb包
echo -e "${YELLOW}Step 6: Building DEB package...${NC}"
dpkg-deb --build "${BUILD_DIR}" "${DEB_FILE}"

# 12. 显示包信息
echo ""
echo -e "${GREEN}=== Build Complete ===${NC}"
echo -e "Package: ${GREEN}${DEB_FILE}${NC}"
echo -e "Size: ${GREEN}$(ls -lh ${DEB_FILE} | awk '{print $5}')${NC}"
echo ""
echo -e "${YELLOW}Package Information:${NC}"
dpkg-deb -I "${DEB_FILE}"
echo ""
echo -e "${GREEN}Installation command:${NC}"
echo "  sudo dpkg -i ${DEB_FILE}"
echo ""
echo -e "${GREEN}Complete removal command:${NC}"
echo "  sudo dpkg --purge tunnelx-server"
