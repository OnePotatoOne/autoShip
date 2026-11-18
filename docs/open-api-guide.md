# autoShip 产品接入指南（Open API v1 与部署契约）

> 编码：UTF-8 ｜ 适用版本：v0.3

autoShip 的三项核心能力（**需求收集**、**自动开发**、**自动部署**）支持按需独立接入。本文档定义业务系统接入 Open API 与自动部署的标准契约。

---

## 1. 网络与证书信任（固定 CA 指纹）

- 业务系统用户的浏览器**不得直连** autoShip，所有请求统一经接入方后端签名转发至 autoShip 公网端口（默认 `:8443`）；
- 接入方服务器的固定出口公网 IP 须同时加入腾讯云安全组与 autoShip 后台「接入方管理」的来源 IP 白名单；
- 全程强制 TLS 1.3，接入方后端只需在 HTTPS 客户端固定 **autoShip 根 CA 的 SHA-256 指纹**（无需绑定服务端叶证书，以后服务端换 IP 或轮换叶证书时接入方零改动）。

---

## 2. Open API 认证与防重放签名算法

每个接入方在后台创建后获得一对独立的 `AppID` 与 `AppSecret`。每次请求须附带以下 HTTP 请求头：

| 请求头 | 必填 | 说明 |
|---|---|---|
| `X-AutoShip-App-Id` | 是 | 接入方唯一标识（如 `as_...`） |
| `X-AutoShip-Timestamp` | 是 | 当前秒级 Unix 时间戳（允许最大时钟偏差 5 分钟） |
| `X-AutoShip-Nonce` | 是 | 每次请求唯一的随机字符串（6 分钟内重复将按重放拒绝） |
| `X-AutoShip-Signature` | 是 | 小写十六进制 `HMAC-SHA256` 签名 |
| `Idempotency-Key` | 写接口推荐 | 幂等键，网络超时重试提交同一键会返回首次缓存结果 |

### 规范化签名原文（Canonical Payload）

以换行符 `\n` 拼接以下五要素后，使用 `AppSecret` 计算 `HMAC-SHA256` 并转为小写十六进制：

```text
<UPPERCASE_HTTP_METHOD>\n
<PATH_WITH_RAW_QUERY>\n
<TIMESTAMP>\n
<NONCE>\n
<LOWERCASE_HEX_SHA256_OF_RAW_BODY>
```

> 示例：`GET /open/v1/events?cursor=10` 无请求体时，第 2 行为 `/open/v1/events?cursor=10`，第 5 行为空字节数组的 SHA-256（`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`）。

---

## 3. 标准接口列表

### 3.1 新建澄清会话 `POST /open/v1/conversations`

请求体：

```json
{
  "productCode": "seccenter",
  "issueType": "feature",
  "reporter": {
    "externalUid": "u_10086",
    "displayName": "张工",
    "attributes": { "role": "auditor" }
  },
  "envInfo": {
    "version": "v1.4.2",
    "route": "/assets/list",
    "browser": "Chrome 157"
  }
}
```

- 注意：接入方后端在发出前须对 `envInfo` 中的敏感资产或凭据做脱敏；
- 当模型在线时返回 `"mode": "chat"`；当 Mac 离线或模型不可用时自动降级返回 `"mode": "form"` 及 `formSchemas`（接入方按表单定义直接渲染留言表单，模型恢复后自动按 FIFO 生成追问并通过 `events` 接口通知）。

### 3.2 发送消息并接收流式回复 `POST /open/v1/conversations/{id}/messages`

响应格式为标准 `text/event-stream`（SSE），事件类型如下：

- `message.delta`：`{"delta": "..."}` 文字流式增量；
- `ask`：`{"question": "...", "options": ["选项A", "选项B"]}` 一次只问一个问题，最多 4 个快捷选项 + 自由输入；
- `prototype`：`{"id": 3, "versionNum": 1, "title": "...", "htmlContent": "<!doctype html>..."}` 单文件无外链低保真 HTML 原型；
- `summary`：结构化需求卡或缺陷复现卡，提示用户点击确认；
- `status`：`{"status": "...", "requirementId": 0}` 会话当前状态；
- `error`：`{"message": "..."}` 异常提示；
- `done`：`{"conversationId": 12}` 本轮流式输出结束。

### 3.3 沙箱渲染原型安全要求

接入方前端展示 `prototype.htmlContent` 时，**必须**放入隔离沙箱 `<iframe>` 中渲染，严禁赋予 `allow-same-origin`：

```html
<iframe
  sandbox=""
  referrerpolicy="no-referrer"
  [srcdoc]="prototypeHtml">
</iframe>
```

### 3.4 其余接口

- `POST /open/v1/attachments`：`multipart/form-data` 上传截图（字段 `file`、`productId`、`conversationId`；仅限 `png` / `jpg` / `webp` 且大小不超过 10 MB）；
- `POST /open/v1/conversations/{id}/confirm`：提交人明确点击确认需求卡后调用，触发服务端完成门校验并正式入库；
- `GET /open/v1/reporters/{rid}/issues`：查询指定外部用户 `externalUid` 提交的全部反馈与当前状态；
- `GET /open/v1/issues/{id}`：查询单条反馈详情与状态时间线；
- `GET /open/v1/events?cursor=0`：增量轮询状态变化（`issue.status_changed`）、离线恢复新追问（`issue.follow_up`）及新版本上线（`issue.released`）。

---

## 4. 自动部署产品契约

接入自动部署的产品须满足：

1. **免登录健康检查接口**（如 `/api/healthz`）：返回 JSON `{ "status": "ok", "version": "v1.2.0", "migrationDone": true, "probes": { "auth": "ok" } }`；
2. **随制品打包发布的部署钩子**（参考仓库内 `deploy/hook-example.sh`），支持子命令：
   - `install <binary> --deployment <ID>`：以业务低权限账号预检候选程序、按部署 ID 备份旧版、安装并启动（失败自行恢复并返回非零）；
   - `rollback --deployment <ID>`：按部署 ID 幂等回退到该次部署前的程序版本（重复执行结果不变）；
   - `backup-db --deployment <ID> --dir <目录>`：导出数据库至指定专用目录；
   - `status` / `logs <N>` / `cleanup`。
