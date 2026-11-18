# autoShip — 多产品闭环自动研发与安全发布平台

autoShip 将“业务系统用户反馈问题 → 客服 AI 澄清出原型与需求卡 → 自动编码与门禁审计 → 预览验收 → CI 编译签名发布 → 人工确认自动升级与故障回滚”打通为面向多产品的标准闭环平台。

详细架构设计见 [`docs/autoship-design.md`](docs/autoship-design.md)，外部业务系统接入说明见 [`docs/open-api-guide.md`](docs/open-api-guide.md)。

---

## 核心组成与可执行文件

编译后在 `bin/` 目录产出 6 个单文件程序：

| 程序 | 运行位置 | 职责 |
|---|---|---|
| `autoship-server` | 腾讯云服务器 | 内嵌管理后台前端单文件部署；Tailnet 监听器承载管理后台与开发 Agent 通道，公网白名单监听器承载 Open API v1 与更新 Agent 长轮询；内置自建根 CA 与客服 AI 对话循环 |
| `autoship-dev-agent` | 本机 Mac | 自研单长会话研发引擎、macOS `sandbox-exec` 权限档案、工具守卫、三区改动范围计算、Go/TS/DB/UTF8/测试/回滚演练/CDP 页面巡检门禁、独立评审、公共区系统性影响审计、本地预览与受限模型中继 |
| `autoship-bench` | 本机 Mac | 阶段 0 验证工具：从真实产品仓库回退到历史提交自动重跑修复/开发任务、检测模型多模态看图与沙箱兼容性并输出量化验证报告 |
| `autoship-release` | 本机 Mac | 独立发布命令行工具（阶段 1 手动发版场景也可一键打包、用钥匙串 Ed25519 私钥签名并分片续传至 Server） |
| `autoship-agent` | 产品服务器 | 无特权专用账号 `autoship-agent` 运行的常驻守护进程：固定 CA 指纹、30 秒心跳、长轮询、断点续传下载、健康检查与自动回滚上报 |
| `autoship-apply` | 产品服务器 | `root` 所有、经无通配参数 `sudo` 调用的受限执行器：隔离复制、Server 票据与发布清单双公钥二次验签、清空环境变量调用产品部署钩子 |

---

## 快速构建与验证

```bash
# 一键编译前端、内嵌至后端并构建全套二进制
make build

# 运行全量静态检查与单元测试
make vet
make test

# 编译产品服务器 Linux amd64 静态更新 Agent 套件
make build-linux-agents
```

---

## 启动配置参考

1. **Server**：复制 `configs/server.example.yaml` 为 `configs/server.yaml`，填写 MySQL 8 连接参数与 Tailnet/公网监听地址后启动 `./bin/autoship-server --config ./configs/server.yaml`。首次启动会自动生成根 CA 并打印固定 SHA-256 指纹，访问 Tailnet 管理后台地址完成初始管理员与 OTP 绑定。
2. **Mac 开发 Agent**：复制 `configs/dev-agent.example.yaml` 为 `configs/dev-agent.yaml`，填入后台生成的 15 分钟一次性注册令牌与 CA 指纹，启动 `./bin/autoship-dev-agent --config ./configs/dev-agent.yaml`。
3. **产品服务器更新 Agent**：将 `bin/linux-amd64/autoship-agent`、`bin/linux-amd64/autoship-apply` 与 `deploy/install-update-agent.sh` 上传至产品服务器，以 `root` 执行一次安装脚本即可自动配置无特权账号、无通配 `sudoers` 与 `systemd` 服务。
