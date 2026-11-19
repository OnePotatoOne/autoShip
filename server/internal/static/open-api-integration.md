# autoShip 对外开放接口（Open API v1）对接开发指南

> **文档版本**：v0.2.1（UTF-8）  
> **适用对象**：需要接入 `autoShip` 平台提交业务需求、上报缺陷、进行 AI 客服澄清或同步需求研发/上线进度的第三方/内部业务系统前后端开发人员。

---

## 1. 接入架构与前置准备

### 1.1 独立外部调用端口与安全边界
- **物理端口隔离**：`autoShip` 将「管理后台页面端口」（如 `:9444`，仅限专网访问）与「对外业务调用端口」（默认 `:9443`，管理员可在 `autoShip` 后台随时配置修改并热生效）严格分离。**外部业务系统仅访问独立外部调用端口下的 `/open/v1/*` 路径**。
- **后端对后端调用原则**：`plainAppSecret` 属于高敏签名密钥，**严禁下发到浏览器前端或移动端 App**。请由业务系统自身后端持有密钥并完成请求签名后转发至 `autoShip`。
- **全程强制 HTTPS**：禁止明文 HTTP。`autoShip` 使用自建根 CA 签发 TLS 证书，业务系统后端 HTTP 客户端请加载管理员提供的 `autoship-ca.crt` 根证书（或固定根 CA 的 SHA-256 指纹）。

### 1.2 对接前向 `autoShip` 管理员获取的 4 项配置
1. **外部接口 Base URL**：例如 `https://<autoShip服务器IP或域名>:9443`
2. **根 CA 证书文件**：`autoship-ca.crt`（可从后台直接下载）
3. **接入方凭据**：`AppID`（如 `app_8f3c...`）与一次性明文签名密钥 `AppSecret`
4. **授权产品编码（`productCode`）**：如 `erp-web`、`crm-portal`（同时请将贵方后端服务器固定出口 IP 报备给管理员填入接入方白名单）

---

## 2. 统一鉴权与 HMAC-SHA256 签名规范

所有 `/open/v1/*` 请求均须携带以下 4 个认证请求头：

| 请求头名称 | 必填 | 示例值 | 说明 |
| :--- | :---: | :--- | :--- |
| `X-AutoShip-App-Id` | 是 | `app_9a1b2c3d4e5f` | 接入方唯一标识 |
| `X-AutoShip-Timestamp` | 是 | `1795089600` | 当前 Unix 秒级时间戳（允许与服务端最大时钟偏差 `±300` 秒） |
| `X-AutoShip-Nonce` | 是 | `7f4c2e19a80b4d129c3e` | 每次请求唯一的防重放随机字符串（6 分钟内重复 Nonce 会被拒绝） |
| `X-AutoShip-Signature` | 是 | `64位小写hex字符串` | 按下方规范计算的 `HMAC-SHA256` 签名值 |
| `Idempotency-Key` | 选填 | `req-20261119-0001` | 幂等键（建议在 `POST /open/v1/issues`、`conversations`、`confirm` 传入，防止网络重试重复提单） |

### 2.1 规范化签名原文（Canonical Payload）拼接规则

将以下 **5 个要素** 用英文换行符 `\n`（ASCII `0x0A`，最后一行末尾不加 `\n`）连接成 UTF-8 字符串：

```text
<HTTP_METHOD_UPPERCASE> + "\n" +
<REQUEST_PATH_WITH_RAW_QUERY> + "\n" +
<TIMESTAMP> + "\n" +
<NONCE> + "\n" +
<LOWERCASE_HEX_SHA256_OF_RAW_REQUEST_BODY>
```

**关键细节说明**：
1. `HTTP_METHOD_UPPERCASE`：全大写方法名，如 `GET`、`POST`。
2. `REQUEST_PATH_WITH_RAW_QUERY`：以 `/` 开头的请求路径；若 URL 带有 Query 参数，必须原样附带 `?query`，例如：
   - 无查询参数：`/open/v1/issues`
   - 含查询参数：`/open/v1/events?cursor=0&limit=50`
3. `LOWERCASE_HEX_SHA256_OF_RAW_REQUEST_BODY`：原始 HTTP 请求体字节流的 SHA-256 小写十六进制摘要（64 字符）：
   - 当 `GET` 等无请求体（空字节 `""`）时，其 SHA-256 固定为：  
     `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`
   - 当 `POST` 发送 JSON 或 `multipart/form-data` 时，请先在内存中序列化出最终要发送的原始字节数组 `rawBodyBytes`，对其计算 SHA-256，并将完全一致的 `rawBodyBytes` 作为 HTTP Body 发出。
4. 使用 `AppSecret` 作为密钥，对上述 5 行拼接字符串计算 `HMAC-SHA256`，结果转为 **64 位全小写十六进制字符串** 填入 `X-AutoShip-Signature`。

### 2.2 多语言可直接运行签名示例

#### Python 3 示例
```python
import hashlib
import hmac
import json
import secrets
import time
import urllib.request
import ssl

BASE_URL = "https://100.90.160.50:9443"
APP_ID = "app_your_app_id"
APP_SECRET = "your_plain_app_secret"
CA_CERT_PATH = "./autoship-ca.crt"

def autoship_request(method: str, path_with_query: str, body_obj=None, idempotency_key: str = ""):
    method = method.upper()
    raw_body = b"" if body_obj is None else json.dumps(body_obj, ensure_ascii=False).encode("utf-8")
    ts = str(int(time.time()))
    nonce = secrets.token_hex(16)
    body_sha256 = hashlib.sha256(raw_body).hexdigest()

    canonical = "\n".join([method, path_with_query, ts, nonce, body_sha256]).encode("utf-8")
    signature = hmac.new(APP_SECRET.encode("utf-8"), canonical, hashlib.sha256).hexdigest()

    headers = {
        "X-AutoShip-App-Id": APP_ID,
        "X-AutoShip-Timestamp": ts,
        "X-AutoShip-Nonce": nonce,
        "X-AutoShip-Signature": signature,
    }
    if body_obj is not None:
        headers["Content-Type"] = "application/json; charset=utf-8"
    if idempotency_key:
        headers["Idempotency-Key"] = idempotency_key

    ctx = ssl.create_default_context(cafile=CA_CERT_PATH)
    req = urllib.request.Request(
        BASE_URL + path_with_query,
        data=raw_body if method != "GET" else None,
        headers=headers,
        method=method,
    )
    with urllib.request.urlopen(req, context=ctx, timeout=15) as resp:
        return json.loads(resp.read().decode("utf-8"))

# 1 秒验证连通性与签名
print(autoship_request("GET", "/open/v1/ping"))
```

#### Node.js / TypeScript 示例
```typescript
import crypto from 'node:crypto';
import fs from 'node:fs';
import https from 'node:https';

const BASE_URL = 'https://100.90.160.50:9443';
const APP_ID = 'app_your_app_id';
const APP_SECRET = 'your_plain_app_secret';
const httpsAgent = new https.Agent({ ca: fs.readFileSync('./autoship-ca.crt') });

export function buildAutoShipHeaders(
  method: string,
  pathWithQuery: string,
  rawBody: Buffer = Buffer.alloc(0),
  idempotencyKey?: string
): Record<string, string> {
  const ts = Math.floor(Date.now() / 1000).toString();
  const nonce = crypto.randomBytes(16).toString('hex');
  const bodyHash = crypto.createHash('sha256').update(rawBody).digest('hex');
  const canonical = [method.toUpperCase(), pathWithQuery, ts, nonce, bodyHash].join('\n');
  const signature = crypto.createHmac('sha256', APP_SECRET).update(canonical, 'utf8').digest('hex');

  const headers: Record<string, string> = {
    'X-AutoShip-App-Id': APP_ID,
    'X-AutoShip-Timestamp': ts,
    'X-AutoShip-Nonce': nonce,
    'X-AutoShip-Signature': signature,
  };
  if (idempotencyKey) {
    headers['Idempotency-Key'] = idempotencyKey;
  }
  return headers;
}
```

#### Go 示例
```go
package autoshipclient

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SignRequest 为出站 http.Request 写入 autoShip Open API 签名请求头。
func SignRequest(req *http.Request, appID, appSecret, nonce string, rawBody []byte) {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	bodySum := sha256.Sum256(rawBody)
	pathWithQuery := req.URL.Path
	if req.URL.RawQuery != "" {
		pathWithQuery += "?" + req.URL.RawQuery
	}
	canonical := strings.Join([]string{
		strings.ToUpper(req.Method),
		pathWithQuery,
		ts,
		nonce,
		hex.EncodeToString(bodySum[:]),
	}, "\n")

	mac := hmac.New(sha256.New, []byte(appSecret))
	_, _ = mac.Write([]byte(canonical))
	sig := hex.EncodeToString(mac.Sum(nil))

	req.Header.Set("X-AutoShip-App-Id", appID)
	req.Header.Set("X-AutoShip-Timestamp", ts)
	req.Header.Set("X-AutoShip-Nonce", nonce)
	req.Header.Set("X-AutoShip-Signature", sig)
}
```

#### Java (JDK 11+) 示例
```java
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.util.HexFormat;

public class AutoShipSigner {
    public static String sign(String secret, String method, String pathWithQuery,
                              String timestamp, String nonce, byte[] rawBody) throws Exception {
        MessageDigest sha256 = MessageDigest.getInstance("SHA-256");
        String bodyHex = HexFormat.of().formatHex(sha256.digest(rawBody == null ? new byte[0] : rawBody));
        String canonical = String.join("\n", method.toUpperCase(), pathWithQuery, timestamp, nonce, bodyHex);

        Mac mac = Mac.getInstance("HmacSHA256");
        mac.init(new SecretKeySpec(secret.getBytes(StandardCharsets.UTF_8), "HmacSHA256"));
        return HexFormat.of().formatHex(mac.doFinal(canonical.getBytes(StandardCharsets.UTF_8)));
    }
}
```

---

## 3. 两种提需求接入模式选择

在第三方业务平台开发“提需求 / 意见反馈”页面时，可根据交互复杂度任选一种（或同时使用）：

| 接入模式 | 适用页面形态 | 调用链路 |
| :--- | :--- | :--- |
| **模式 A：单接口表单直提（推荐快速接入）** | 业务系统自己画一个「提需求 / 报缺陷」表单弹窗，用户填完点提交即入库 | `POST /open/v1/issues`（1 个接口一步入需求池） |
| **模式 B：AI 客服多轮对话澄清 + 原型确认** | 业务系统内嵌智能反馈聊天窗，由 `autoShip` 客服 AI 追问细节、绘制单文件低保真原型并生成确认卡 | `POST /open/v1/conversations` → `POST /open/v1/conversations/:id/messages` (SSE) → `POST /open/v1/conversations/:id/confirm` |

---

## 4. 全量接口详细参考（共 9 个接口）

所有常规 JSON 接口统一返回如下响应信封：
```json
{
  "requestId": "c44debc86c9248a5b8e4c6d55ff59014",
  "code": 0,
  "message": "success",
  "data": {}
}
```
> `code === 0` 表示业务成功；非 `0`（或 HTTP 状态码为 `4xx/5xx`）时 `message` 字段为可读中文错误原因。

---

### 4.1 连通性与签名自检：`GET /open/v1/ping`
用于首次联调验证端口连通性、来源 IP 白名单与 HMAC-SHA256 签名算法是否正确。

- **响应 `data` 示例**：
```json
{
  "ok": true,
  "integrationId": 1,
  "integrationName": "ERP业务系统",
  "appId": "app_9a1b2c3d4e5f",
  "serverTimestamp": 1795089605
}
```

---

### 4.2 模式 A — 结构化表单一步直提需求：`POST /open/v1/issues`
无需创建对话会话，直接将用户在业务系统页面填写的需求或缺陷一步提交进入 `autoShip` 需求池（若 `autoShip` 后台开启了「新增需求自动进入研发状态」，提交后将直接触发 Antigravity 自动研发）。

- **建议请求头**：附带 `Idempotency-Key: <业务系统生成的唯一请求号>`
- **请求 JSON 参数**：

| 字段名 | 类型 | 必填 | 说明 |
| :--- | :--- | :---: | :--- |
| `productCode` | string | 是 | 目标产品唯一英文编码（需已在后台开启需求收集并授权给该接入方） |
| `issueType` | string | 是 | 类型：`feature`（新增功能）、`bug`（缺陷修复）、`improvement`（体验改进） |
| `title` | string | 是 | 需求或缺陷标题 |
| `moduleCode` | string | 否 | 所属业务模块编码（如 `order`、`auth`，留空可由 AI 自动分拣归类） |
| `priority` | string | 否 | 期望优先级：`P0` / `P1` / `P2`（默认） / `P3` |
| `description` | string | 条件必填 | 背景与详细需求描述（`description`、`reproSteps`、`acceptanceCriteria` 至少填一项） |
| `expectedBehavior` | string | 否 | 期望行为或期望结果 |
| `actualBehavior` | string | 否 | 缺陷实际表现与影响范围 |
| `reproSteps` | string | 否 | 缺陷复现操作步骤与频率 |
| `outOfScope` | string | 否 | 明确不包含的范围 |
| `acceptanceCriteria` | string | 否 | 可验证的验收标准（推荐 `Given / When / Then` 描述） |
| `reporter` | object | 是 | 提交人信息：`{ "externalUid": "u_10086", "displayName": "张三", "attributes": {"dept": "财务部"} }`（`externalUid` 必填，用于后续按人查询反馈进度） |
| `envInfo` | object | 否 | 提交时所在页面路由、浏览器、角色或业务系统版本号等任意 JSON 键值 |

- **请求体示例**：
```json
{
  "productCode": "erp-web",
  "moduleCode": "invoice",
  "issueType": "feature",
  "priority": "P1",
  "title": "发票列表页支持按开票日期区间批量导出 Excel",
  "description": "财务人员月底对账时需要一次性导出所选日期区间内的全部销项发票明细。",
  "expectedBehavior": "筛选日期区间后点击【批量导出】，生成包含发票号、金额、税率、购方名称的 xlsx 文件。",
  "acceptanceCriteria": "Given 选择 30 天内日期区间 When 点击批量导出 Then 5 秒内下载完整 Excel 且金额合计一致",
  "reporter": {
    "externalUid": "emp_2048",
    "displayName": "李会计",
    "attributes": { "role": "finance_auditor" }
  },
  "envInfo": {
    "appVersion": "v2.4.1",
    "pageRoute": "/finance/invoices"
  }
}
```

- **响应 `data` 示例**：
```json
{
  "issueId": 42,
  "status": "submitted",
  "title": "发票列表页支持按开票日期区间批量导出 Excel",
  "activeWorkItemId": 0
}
```
> 若 `autoShip` 后台开启了「新增需求自动进入研发状态」，返回的 `status` 将直接为 `"in_progress"`，`activeWorkItemId` 为已创建的研发任务 ID。

---

### 4.3 上传截图附件：`POST /open/v1/attachments`
支持 `multipart/form-data` 上传报错或界面截图（仅支持真实魔数为 `image/png`、`image/jpeg`、`image/webp` 的图片，单文件上限 `10 MB`）。

- **表单字段**：
  - `file`（必填，文件流）
  - `productId`（选填，数字 ID）
  - `conversationId`（选填，关联会话 ID）
- **响应 `data` 示例**：
```json
{
  "id": 15,
  "fileName": "error-toast.png",
  "mimeType": "image/png",
  "sizeBytes": 184520,
  "sha256": "5f9c..."
}
```

---

### 4.4 模式 B（第 1 步）— 创建 AI 澄清会话：`POST /open/v1/conversations`
发起一次智能客服澄清对话。当服务端模型通道在线时返回 `mode: "chat"`；当模型离线维护时自动平滑降级返回 `mode: "form"` 及动态结构化表单字段定义 `formSchemas`。

- **请求体示例**：
```json
{
  "productCode": "erp-web",
  "issueType": "feature",
  "reporter": {
    "externalUid": "emp_2048",
    "displayName": "李会计",
    "attributes": { "role": "finance" }
  },
  "envInfo": {
    "pageRoute": "/finance/invoices",
    "browser": "Chrome 130"
  }
}
```
- **响应 `data` 示例**：
```json
{
  "conversationId": 108,
  "mode": "chat",
  "status": "clarifying",
  "issueType": "feature"
}
```

---

### 4.5 模式 B（第 2 步）— 发送澄清消息（SSE 流式响应）：`POST /open/v1/conversations/:id/messages`
发送用户自然语言描述、已上传的截图 ID 数组或离线降级表单数据。响应头为 `Content-Type: text/event-stream; charset=utf-8`。

- **请求体示例**：
```json
{
  "content": "我希望在发票页面加一个按月份批量导出 Excel 的按钮",
  "attachmentIds": [15]
}
```
> 若步骤 4.4 返回了 `mode: "form"`，可将用户填写的表单字段放入 `"formData": { "title": "...", ... }` 提交。

- **SSE 事件流类型说明**：
  - `event: message.delta`：`{"delta": "请问您需要导出的 Excel 包含哪些具体列？"}`（AI 增量打字流）
  - `event: ask`：`{"question": "请选择导出范围：", "options": ["当前筛选结果", "仅勾选行"]}`（快捷单选追问卡片）
  - `event: prototype`：`{"id": 3, "versionNum": 1, "title": "界面低保真原型", "htmlContent": "<!doctype html>..."}`（单文件无外链 HTML 原型；**外部前端展示时务必放入 `<iframe sandbox="" srcdoc="...">` 零权限沙箱中渲染**）
  - `event: summary`：输出已整理完备的结构化需求确认卡草稿，此时前端可展示【确认提交需求】按钮
  - `event: status`：`{"status": "awaiting_confirm", "requirementId": 0}`
  - `event: error`：`{"message": "..."}`
  - `event: done`：`{"conversationId": 108}`

---

### 4.6 模式 B（第 3 步）— 确认需求卡正式入池：`POST /open/v1/conversations/:id/confirm`
当 SSE 返回 `summary`（状态变为 `awaiting_confirm`）且提交人点击确认无误后调用本接口（请求体传 `{}` 即可，支持 `Idempotency-Key`）。

- **响应 `data` 示例**：
```json
{
  "issueId": 43,
  "status": "submitted",
  "title": "发票列表按月批量导出 Excel"
}
```

---

### 4.7 查询指定提交人的全部需求与状态：`GET /open/v1/reporters/:rid/issues`
供业务系统在「我的反馈」页面展示当前登录用户（`:rid` 即提交时传入的 `reporter.externalUid`）提过的所有需求及实时进展。

- **请求路径示例**：`GET /open/v1/reporters/emp_2048/issues`
- **响应 `data`**：`Requirement` 对象数组（含 `id`、`title`、`issueType`、`priority`、`status`、`releasedVersion`、`closeReason`、`createdAt` 等）。

---

### 4.8 查询单条需求详情与进度时间线：`GET /open/v1/issues/:id`
查询某条需求（`:id` 为 `issueId`）的当前状态、上线版本号与完整流转时间线。

- **请求路径示例**：`GET /open/v1/issues/42`
- **响应 `data` 示例**：
```json
{
  "issue": {
    "id": 42,
    "productId": 1,
    "moduleCode": "invoice",
    "issueType": "feature",
    "title": "发票列表页支持按开票日期区间批量导出 Excel",
    "priority": "P1",
    "status": "in_progress",
    "releasedVersion": "",
    "createdAt": "2026-11-19T19:20:00+08:00"
  },
  "events": [
    {
      "eventType": "submitted",
      "fromStatus": "clarifying",
      "toStatus": "submitted",
      "summary": "提交人已确认需求卡并正式提交",
      "createdAt": "2026-11-19T19:20:00+08:00"
    },
    {
      "eventType": "work_started",
      "fromStatus": "submitted",
      "toStatus": "in_progress",
      "summary": "已启动自动开发任务 #12",
      "createdAt": "2026-11-19T19:22:10+08:00"
    }
  ]
}
```

---

### 4.9 增量拉取状态变更与上线通知事件：`GET /open/v1/events`
业务系统后端可定时（例如每 30~60 秒）增量拉取该接入方名下所有需求的状态流转与新版本上线事件，用于向业务系统内的原始提交人发送站内信或弹窗提醒。

- **Query 参数**：
  - `cursor`：上次拉取返回的 `nextCursor`（首次传 `0`）
  - `limit`：单次最大条数（默认 `100`）
- **请求路径示例**：`GET /open/v1/events?cursor=0&limit=50`
- **响应 `data` 示例**：
```json
{
  "events": [
    {
      "id": 19,
      "productId": 1,
      "requirementId": 42,
      "externalUid": "emp_2048",
      "eventType": "issue.released",
      "payloadJson": "{\"issueId\":42,\"title\":\"发票列表页支持按开票日期区间批量导出 Excel\",\"status\":\"released\",\"releasedVersion\":\"v1.8.0\"}"
    }
  ],
  "nextCursor": 19
}
```
- **`eventType` 枚举**：
  - `issue.status_changed`：需求状态发生变化（如分拣完成、进入研发 `in_progress`、关闭归档 `closed`）
  - `issue.released`：需求随新版本在全部生产部署目标上线完成（附带 `releasedVersion`）
  - `issue.follow_up`：离线留言会话在 AI 恢复后生成了新的追问或确认卡

---

## 5. 需求业务状态（`status`）字典与常见错误排查

### 5.1 需求 `status` 状态枚举
| 状态编码 | 中文含义 | 对外展示说明 |
| :--- | :--- | :--- |
| `clarifying` | 澄清中 | 正在通过客服对话补充细节 |
| `pending_ai` | 待 AI 跟进 | 离线表单留言已保存，等待模型上线跟进 |
| `submitted` | 已提交 | 已进入 `autoShip` 需求池，等待分拣或确认立项 |
| `triaged` | 已分拣 | AI 已完成模块与优先级评估，等待管理员确认立项 |
| `scheduled` | 待排期 | 管理员已确认要做，排入研发队列 |
| `in_progress` | 进行中 | 已创建自动研发任务，正在编码 / 验收 / 发布 / 部署闭环中 |
| `released` | 已上线 | 已在生产环境完成部署上线（见 `releasedVersion`） |
| `closed` | 已关闭 | 已关闭归档（`closeReason`：`rejected` 拒绝 / `duplicate` 重复 / `deferred` 延后 / `answered` 已解答） |

### 5.2 联调常见 HTTP 状态码排查
- **HTTP `403`（`来源 IP 未列入 autoShip 公网白名单`）**：请检查业务系统后端实际出网 IP 是否已添加到 `autoShip` 后台「系统设置 -> Open API 接入方 -> 来源 IP 白名单」。
- **HTTP `401`（`请求时间戳超出允许范围`）**：校准调用方服务器 NTP 时间（需在 `±300` 秒内），确认 `X-AutoShip-Timestamp` 单位是 **秒** 而非毫秒。
- **HTTP `401`（`Open API HMAC 签名校验不通过`）**：
  1. 检查第 2 行路径是否包含了完整的原始 `?query`（例如 `/open/v1/events?cursor=0&limit=50`）；
  2. 检查 `GET` 空请求体的 SHA-256 是否为 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`；
  3. 检查签名结果是否为 **小写** hex，且未使用已被重新创建覆盖的旧密钥。
- **连接超时或 `404`**：确认请求的是 `autoShip` **独立外部调用端口**（默认 `:9443`，可在后台查看当前端口），而非管理页面端口（`:9444`），且云服务器安全组/防火墙已对调用方 IP 放行该外部调用端口。
