import { CommonModule } from '@angular/common';
import { Component, OnInit, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { ActivatedRoute } from '@angular/router';
import { NzAlertModule } from 'ng-zorro-antd/alert';
import { NzBadgeModule } from 'ng-zorro-antd/badge';
import { NzButtonModule } from 'ng-zorro-antd/button';
import { NzCheckboxModule } from 'ng-zorro-antd/checkbox';
import { NzDescriptionsModule } from 'ng-zorro-antd/descriptions';
import { NzDividerModule } from 'ng-zorro-antd/divider';
import { NzDrawerModule } from 'ng-zorro-antd/drawer';
import { NzEmptyModule } from 'ng-zorro-antd/empty';
import { NzFormModule } from 'ng-zorro-antd/form';
import { NzIconModule } from 'ng-zorro-antd/icon';
import { NzInputModule } from 'ng-zorro-antd/input';
import { NzInputNumberModule } from 'ng-zorro-antd/input-number';
import { NzMessageService } from 'ng-zorro-antd/message';
import { NzModalModule } from 'ng-zorro-antd/modal';
import { NzPopconfirmModule } from 'ng-zorro-antd/popconfirm';
import { NzSelectModule } from 'ng-zorro-antd/select';
import { NzSpinModule } from 'ng-zorro-antd/spin';
import { NzSwitchModule } from 'ng-zorro-antd/switch';
import { NzTableModule } from 'ng-zorro-antd/table';
import { NzTabsModule } from 'ng-zorro-antd/tabs';
import { NzTagModule } from 'ng-zorro-antd/tag';
import {
  AgentEnrollTokenResult,
  AgentNode,
  DeployTarget,
  IntegrationItem,
  Product,
  ProductFullBundle,
  ProductKnowledge,
  ProductModule,
  ProductScopeRule,
  SystemSettingsView
} from '../../core/models/api.models';
import { AdminApiService } from '../../core/services/admin-api.service';
import {
  copyTextToClipboard,
  formatDateTime,
  parseStringArray
} from '../../core/utils/format.utils';

/**
 * autoShip 管理后台系统设置中心组件（`/settings`）。
 * 对应设计文档第 5.2、6、7.3、7.4、7.5、8.3、8.5 节：
 * 1. 产品档案全配置（主配置、构建测试命令、预览配方、模块划分、范围与公共区规则、沙箱权限档案与一键放行、产品知识库）；
 * 2. Open API 外部接入方管理（创建时一次性弹窗展示 `plainAppSecret`）；
 * 3. 开发/更新 Agent 一次性注册令牌（15 分钟有效）签发与凭据吊销；
 * 4. 全局模型地址（Mac 中继 / 直连切换）、飞书通知 Webhook、自建根 CA 指纹展示与 `/api/v1/admin/pki/ca.crt` 证书下载。
 */
@Component({
  selector: 'app-settings',
  standalone: true,
  imports: [
    CommonModule,
    FormsModule,
    NzTabsModule,
    NzTableModule,
    NzButtonModule,
    NzSelectModule,
    NzInputModule,
    NzInputNumberModule,
    NzTagModule,
    NzBadgeModule,
    NzIconModule,
    NzDrawerModule,
    NzModalModule,
    NzFormModule,
    NzDescriptionsModule,
    NzAlertModule,
    NzEmptyModule,
    NzSpinModule,
    NzSwitchModule,
    NzCheckboxModule,
    NzPopconfirmModule,
    NzDividerModule
  ],
  template: `
    <div class="as-page-header">
      <div>
        <h1 class="as-page-title">
          <span nz-icon nzType="setting"></span>
          <span>平台配置与安全凭据中心</span>
        </h1>
        <p class="as-page-subtitle">
          集中维护多产品研发/部署档案、沙箱隔离白名单、Open API 接入方密钥、Agent 身份令牌、模型通道与 PKI 证书
        </p>
      </div>
      <div>
        <button nz-button nzType="default" (click)="reloadAll()">
          <span nz-icon nzType="reload" [nzSpin]="loading()"></span>
          <span>刷新全部配置</span>
        </button>
      </div>
    </div>

    <div class="as-card">
      <nz-tabs [(nzSelectedIndex)]="mainTabIndex">
        <!-- ==================== Tab 1：产品档案全配置 ==================== -->
        <nz-tab [nzTitle]="'产品档案全配置 (' + products().length + ')'">
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 14px;">
            <span style="color: #64748b; font-size: 13px;">
              每个产品独立声明代码仓库、构建/测试/巡检命令、模块目录、公共区规则、沙箱权限档案与知识库
            </span>
            <button nz-button nzType="primary" (click)="openNewProductModal()">
              <span nz-icon nzType="plus"></span>新建产品档案
            </button>
          </div>

          <nz-table
            #prodTable
            [nzData]="products()"
            nzSize="middle"
            [nzShowPagination]="false"
            nzNoResult="尚未创建任何产品档案，点击右上角【新建产品档案】开始接入"
          >
            <thead>
              <tr>
                <th nzWidth="70px">ID</th>
                <th nzWidth="180px">产品名称 / 编码</th>
                <th>Git 仓库地址与默认分支</th>
                <th nzWidth="260px">三项接入能力开关</th>
                <th nzWidth="230px">操作</th>
              </tr>
            </thead>
            <tbody>
              @for (p of prodTable.data; track p.id) {
                <tr>
                  <td class="as-mono">#{{ p.id }}</td>
                  <td>
                    <b>{{ p.name }}</b>
                    <div class="as-mono" style="font-size: 12px; color: #64748b;">{{ p.code }}</div>
                  </td>
                  <td>
                    <div class="as-mono" style="font-size: 12.5px;">{{ p.repoUrl || '仅接入需求收集（未配仓库）' }}</div>
                    <div style="font-size: 12px; color: #64748b;">分支: {{ p.defaultBranch || 'main' }}</div>
                  </td>
                  <td>
                    <nz-tag [nzColor]="p.intakeEnabled ? 'success' : 'default'">需求收集</nz-tag>
                    <nz-tag [nzColor]="p.autoDevEnabled ? 'blue' : 'default'">自动开发</nz-tag>
                    <nz-tag [nzColor]="p.autoDeployEnabled ? 'purple' : 'default'">自动部署</nz-tag>
                  </td>
                  <td>
                    <div style="display: flex; gap: 6px; flex-wrap: wrap;">
                      <button nz-button nzType="primary" nzSize="small" (click)="openProductBundleDrawer(p.id)">
                        全量档案配置
                      </button>
                      <button
                        nz-button
                        nzDanger
                        nzSize="small"
                        nz-popconfirm
                        nzPopconfirmTitle="确认删除该产品档案吗？"
                        (nzOnConfirm)="deleteProduct(p.id)"
                      >
                        删除
                      </button>
                    </div>
                  </td>
                </tr>
              }
            </tbody>
          </nz-table>
        </nz-tab>

        <!-- ==================== Tab 2：Open API 接入方管理 ==================== -->
        <nz-tab [nzTitle]="'Open API 接入方 (' + integrations().length + ')'">
          <!-- 独立外部调用端口与对接文档工具栏 -->
          <div class="as-subtle-box" style="margin-bottom: 14px; display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 12px;">
            <div>
              <div style="display: flex; align-items: center; gap: 8px; flex-wrap: wrap;">
                <span style="font-weight: 600; color: #0f172a;">独立外部接口监听地址：</span>
                <nz-tag nzColor="green" class="as-mono" style="font-size: 13px;">
                  https://{{ pkiInfo().publicListenAddr || '0.0.0.0:9443' }}/open/v1/*
                </nz-tag>
                <span style="font-size: 12px; color: #64748b;">
                  （与当前管理页面端口 <code class="as-mono">{{ pkiInfo().tailnetListenAddr }}</code> 物理隔离）
                </span>
              </div>
              <div style="font-size: 12.5px; color: #64748b; margin-top: 4px;">
                外部业务系统后端通过独立 AppID + HMAC-SHA256 签名 + 固定来源 IP 白名单访问外部端口提交需求或同步进度
              </div>
            </div>

            <div style="display: flex; gap: 8px; flex-wrap: wrap;">
              <button nz-button nzType="default" (click)="openExternalPortModal()">
                <span nz-icon nzType="api"></span>修改外部调用端口
              </button>
              <button nz-button nzType="default" (click)="openApiDocDrawer()">
                <span nz-icon nzType="read"></span>在线查看对接文档
              </button>
              <a
                nz-button
                nzType="default"
                href="/api/v1/admin/integrations/open-api-doc?download=1"
                target="_blank"
                download="autoShip-OpenAPI-Integration-Guide.md"
              >
                <span nz-icon nzType="download"></span>下载接口文档 (.md)
              </a>
              <button nz-button nzType="primary" (click)="openIntegrationModal()">
                <span nz-icon nzType="plus"></span>新建接入方
              </button>
            </div>
          </div>

          <nz-table
            #intTable
            [nzData]="integrations()"
            nzSize="middle"
            [nzShowPagination]="false"
            nzNoResult="暂无外部接入方配置"
          >
            <thead>
              <tr>
                <th nzWidth="70px">ID</th>
                <th nzWidth="170px">接入方名称</th>
                <th nzWidth="210px">AppID (公钥标识)</th>
                <th>来源 IP / CIDR 白名单</th>
                <th nzWidth="120px">限流 (RPM)</th>
                <th nzWidth="90px">状态</th>
                <th nzWidth="150px">操作</th>
              </tr>
            </thead>
            <tbody>
              @for (it of intTable.data; track it.id) {
                <tr>
                  <td class="as-mono">#{{ it.id }}</td>
                  <td><b>{{ it.name }}</b></td>
                  <td class="as-mono">
                    {{ it.appId }}
                    <a style="margin-left: 6px;" (click)="copyVal(it.appId, 'AppID')">复制</a>
                  </td>
                  <td class="as-mono" style="font-size: 12px;">{{ it.allowedIpsCsv || '未配置白名单' }}</td>
                  <td class="as-mono">{{ it.rateLimitRpm }} / min</td>
                  <td>
                    <nz-badge
                      [nzStatus]="it.enabled ? 'success' : 'default'"
                      [nzText]="it.enabled ? '启用' : '停用'"
                    ></nz-badge>
                  </td>
                  <td>
                    <div style="display: flex; gap: 6px;">
                      <button nz-button nzSize="small" (click)="openIntegrationModal(it)">编辑</button>
                      <button
                        nz-button
                        nzDanger
                        nzSize="small"
                        nz-popconfirm
                        nzPopconfirmTitle="确认删除该接入方并吊销其密钥吗？"
                        (nzOnConfirm)="deleteIntegration(it.id)"
                      >
                        删除
                      </button>
                    </div>
                  </td>
                </tr>
              }
            </tbody>
          </nz-table>
        </nz-tab>

        <!-- ==================== Tab 3：Agent 注册令牌与管理 ==================== -->
        <nz-tab [nzTitle]="'Agent 注册与吊销 (' + agents().length + ')'">
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 14px;">
            <span style="color: #64748b; font-size: 13px;">
              生成 15 分钟有效的一次性注册令牌，Agent 首次启动在本地生成 Ed25519 密钥对并登记公钥，私钥永不出本机
            </span>
            <button nz-button nzType="primary" (click)="openAgentTokenModal()">
              <span nz-icon nzType="key"></span>签发一次性注册令牌 (15分钟有效)
            </button>
          </div>

          <nz-table
            #agTable
            [nzData]="agents()"
            nzSize="middle"
            [nzShowPagination]="false"
            nzNoResult="暂无已登记或待注册的 Agent"
          >
            <thead>
              <tr>
                <th nzWidth="70px">ID</th>
                <th nzWidth="170px">Agent 名称</th>
                <th nzWidth="150px">角色与授权范围</th>
                <th nzWidth="120px">状态</th>
                <th>Ed25519 公钥 / Tailnet 地址</th>
                <th nzWidth="155px">最近心跳</th>
                <th nzWidth="110px">操作</th>
              </tr>
            </thead>
            <tbody>
              @for (ag of agTable.data; track ag.id) {
                <tr>
                  <td class="as-mono">#{{ ag.id }}</td>
                  <td><b>{{ ag.name }}</b></td>
                  <td>
                    @if ((ag.agentType || ag.type) === 'dev') {
                      <nz-tag nzColor="geekblue">开发 Agent (Mac)</nz-tag>
                    } @else {
                      <nz-tag nzColor="cyan">更新 Agent (目标#{{ ag['deployTargetId'] || ag.targetId }})</nz-tag>
                    }
                  </td>
                  <td>
                    @if (ag.status === 'revoked') {
                      <nz-badge nzStatus="error" nzText="已吊销"></nz-badge>
                    } @else if (ag.status === 'pending') {
                      <nz-badge nzStatus="warning" nzText="待用令牌登记"></nz-badge>
                    } @else if (ag.online) {
                      <nz-badge nzStatus="success" nzText="在线活跃"></nz-badge>
                    } @else {
                      <nz-badge nzStatus="default" nzText="离线"></nz-badge>
                    }
                  </td>
                  <td class="as-mono" style="font-size: 12px;">
                    <div>公钥: {{ ag.publicKeyB64 ? ag.publicKeyB64.slice(0, 24) + '...' : '尚未登记' }}</div>
                    @if (ag['tailnetIp'] || ag['relayListenAddr']) {
                      <div style="color: #64748b;">
                        Tailnet: {{ ag['tailnetIp'] }} {{ ag['relayListenAddr'] ? ('(中继 ' + ag['relayListenAddr'] + ')') : '' }}
                      </div>
                    }
                  </td>
                  <td class="as-mono">{{ formatDateTime(ag.lastHeartbeatAt) }}</td>
                  <td>
                    @if (ag.status !== 'revoked') {
                      <button
                        nz-button
                        nzDanger
                        nzSize="small"
                        nz-popconfirm
                        nzPopconfirmTitle="吊销后该 Agent 所有签名请求将立即失效，确认吊销？"
                        (nzOnConfirm)="revokeAgent(ag.id)"
                      >
                        吊销凭据
                      </button>
                    } @else {
                      <span style="color: #94a3b8; font-size: 12px;">已失效</span>
                    }
                  </td>
                </tr>
              }
            </tbody>
          </nz-table>
        </nz-tab>

        <!-- ==================== Tab 4：研发流转策略、模型地址、飞书通知与自建 CA 证书 ==================== -->
        <nz-tab nzTitle="研发流转 · 模型通道 · 飞书 · PKI">
          <div style="display: grid; grid-template-columns: 1.2fr 1fr; gap: 20px;">
            <!-- 左列：研发流转自动化、模型调用通道与飞书配置 -->
            <div>
              <form nz-form nzLayout="vertical" (ngSubmit)="saveGlobalSettings()">
                <h3 class="as-section-title">
                  <span nz-icon nzType="thunderbolt"></span>
                  <span>需求研发自动流转策略（联动 Mac 开发 Agent / Antigravity）</span>
                </h3>
                <div class="as-subtle-box" style="margin-bottom: 18px;">
                  <div style="display: flex; justify-content: space-between; align-items: center; gap: 12px; margin-bottom: 10px;">
                    <div>
                      <div style="font-weight: 600; color: #0f172a;">新增需求自动进入研发状态</div>
                      <div style="font-size: 12.5px; color: #64748b; margin-top: 2px;">
                        关闭（默认）：新需求收到后先待你确认要做、修改需求状态后再开始开发；开启：新需求入池后自动流转为「进行中」并下发开发 Agent (Antigravity) 开始研发。
                      </div>
                    </div>
                    <nz-switch
                      name="wfAutoNew"
                      [(ngModel)]="settingsForm.workflow.autoStartOnNewRequirement"
                      nzCheckedChildren="自动入研"
                      nzUnCheckedChildren="人工确认"
                    ></nz-switch>
                  </div>

                  <div style="display: flex; justify-content: space-between; align-items: center; gap: 12px; margin-bottom: 10px; padding-top: 8px; border-top: 1px dashed #e2e8f0;">
                    <div>
                      <div style="font-weight: 600; color: #0f172a;">确认立项改状态后自动开始研发</div>
                      <div style="font-size: 12.5px; color: #64748b; margin-top: 2px;">
                        当你在需求池把待确认需求改为「待排期 / 确认要做」或「进行中」时，自动创建开发任务并派发给开发 Agent。
                      </div>
                    </div>
                    <nz-switch
                      name="wfAutoConfirm"
                      [(ngModel)]="settingsForm.workflow.autoStartOnConfirmStatus"
                      nzCheckedChildren="已开启"
                      nzUnCheckedChildren="仅改状态"
                    ></nz-switch>
                  </div>

                  <nz-form-item style="margin: 8px 0;">
                    <label nz-checkbox name="wfPlanConfirm" [(ngModel)]="settingsForm.workflow.defaultPlanConfirm">
                      自动触发研发任务时默认开启「方案确认门」（先由 Antigravity 生成方案供确认后再改代码）
                    </label>
                  </nz-form-item>

                  <div style="display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 12px;">
                    <nz-form-item style="margin-bottom: 0;">
                      <nz-form-label>默认单任务 Token 预算</nz-form-label>
                      <nz-form-control>
                        <nz-input-number
                          name="wfTok"
                          [(ngModel)]="settingsForm.workflow.defaultTokenBudget"
                          [nzMin]="20000"
                          [nzStep]="50000"
                          style="width: 100%;"
                        ></nz-input-number>
                      </nz-form-control>
                    </nz-form-item>
                    <nz-form-item style="margin-bottom: 0;">
                      <nz-form-label>默认最大交互轮数</nz-form-label>
                      <nz-form-control>
                        <nz-input-number
                          name="wfTurns"
                          [(ngModel)]="settingsForm.workflow.defaultMaxTurns"
                          [nzMin]="5"
                          [nzMax]="300"
                          style="width: 100%;"
                        ></nz-input-number>
                      </nz-form-control>
                    </nz-form-item>
                    <nz-form-item style="margin-bottom: 0;">
                      <nz-form-label>默认最大时长 (分钟)</nz-form-label>
                      <nz-form-control>
                        <nz-input-number
                          name="wfMins"
                          [(ngModel)]="settingsForm.workflow.defaultMaxMinutes"
                          [nzMin]="10"
                          [nzMax]="480"
                          style="width: 100%;"
                        ></nz-input-number>
                      </nz-form-control>
                    </nz-form-item>
                  </div>
                </div>

                <h3 class="as-section-title">
                  <span nz-icon nzType="robot"></span>
                  <span>模型调用通道（中继 / 直连开关）与飞书通知配置</span>
                </h3>
                <nz-form-item>
                  <nz-form-label nzRequired>客服与分拣模型路由模式</nz-form-label>
                  <nz-form-control>
                    <nz-select name="llmMode" [(ngModel)]="settingsForm.llm.mode">
                      <nz-option
                        nzValue="relay"
                        nzLabel="Tailnet 中继模式 (relay)：经本 Mac 开发 Agent 转发至 workbuddy2api-hub-local"
                      ></nz-option>
                      <nz-option
                        nzValue="direct"
                        nzLabel="直连模式 (direct)：Server 直接请求下方配置的兼容 OpenAI 接口"
                      ></nz-option>
                    </nz-select>
                  </nz-form-control>
                </nz-form-item>

                @if (settingsForm.llm.mode === 'direct') {
                  <div class="as-subtle-box" style="margin-bottom: 14px;">
                    <nz-form-item>
                      <nz-form-label nzRequired>直连 API Base URL</nz-form-label>
                      <nz-form-control>
                        <input
                          nz-input
                          name="directUrl"
                          [(ngModel)]="settingsForm.llm.directBaseURL"
                          placeholder="https://api.example.com/v1"
                        />
                      </nz-form-control>
                    </nz-form-item>

                    <nz-form-item style="margin-bottom: 0;">
                      <nz-form-label>
                        直连 API Key {{ hasSavedDirectKey() ? '（已加密保存，留空表示保持不变）' : '' }}
                      </nz-form-label>
                      <nz-form-control>
                        <input
                          nz-input
                          type="password"
                          name="directKey"
                          [(ngModel)]="settingsForm.llm.directApiKey"
                          placeholder="输入新密钥将使用 AES-256 加密入库"
                        />
                      </nz-form-control>
                    </nz-form-item>
                  </div>
                }

                <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px;">
                  <nz-form-item>
                    <nz-form-label>客服澄清角色模型名 (intake)</nz-form-label>
                    <nz-form-control>
                      <input
                        nz-input
                        name="intakeMod"
                        [(ngModel)]="settingsForm.llm.intakeModel"
                        placeholder="deepseek-v4.1-flash"
                      />
                    </nz-form-control>
                  </nz-form-item>

                  <nz-form-item>
                    <nz-form-label>需求分拣角色模型名 (triage)</nz-form-label>
                    <nz-form-control>
                      <input
                        nz-input
                        name="triageMod"
                        [(ngModel)]="settingsForm.llm.triageModel"
                        placeholder="deepseek-v4.1-flash"
                      />
                    </nz-form-control>
                  </nz-form-item>
                </div>

                <div style="display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 12px;">
                  <nz-form-item>
                    <nz-form-label>单会话 Token 预算</nz-form-label>
                    <nz-form-control>
                      <nz-input-number
                        name="maxSessTok"
                        [(ngModel)]="settingsForm.llm.maxSessionTokens"
                        [nzMin]="4000"
                        [nzStep]="4000"
                        style="width: 100%;"
                      ></nz-input-number>
                    </nz-form-control>
                  </nz-form-item>

                  <nz-form-item>
                    <nz-form-label>单请求最大输出 Token</nz-form-label>
                    <nz-form-control>
                      <nz-input-number
                        name="maxReqTok"
                        [(ngModel)]="settingsForm.llm.maxRequestTokens"
                        [nzMin]="512"
                        [nzStep]="512"
                        style="width: 100%;"
                      ></nz-input-number>
                    </nz-form-control>
                  </nz-form-item>

                  <nz-form-item>
                    <nz-form-label>制品保留最近版本数</nz-form-label>
                    <nz-form-control>
                      <nz-input-number
                        name="keepCnt"
                        [(ngModel)]="settingsForm.artifactKeepCount"
                        [nzMin]="1"
                        [nzMax]="50"
                        style="width: 100%;"
                      ></nz-input-number>
                    </nz-form-control>
                  </nz-form-item>
                </div>

                <nz-form-item>
                  <label nz-checkbox name="maskPii" [(ngModel)]="settingsForm.llm.maskPIIBeforeSend">
                    发送给大模型前自动对用户内容中的 IP 地址与邮箱执行脱敏（推荐开启）
                  </label>
                </nz-form-item>

                <nz-divider></nz-divider>

                <nz-form-item>
                  <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 6px;">
                    <span style="font-weight: 500;">飞书机器人通知 Webhook 地址</span>
                    <nz-switch
                      name="fsEnabled"
                      [(ngModel)]="settingsForm.notify.enabled"
                      nzCheckedChildren="已启用推送"
                      nzUnCheckedChildren="已暂停"
                    ></nz-switch>
                  </div>
                  <input
                    nz-input
                    name="fsUrl"
                    [(ngModel)]="settingsForm.notify.feishuWebhookURL"
                    placeholder="请输入飞书自定义机器人 Webhook 地址"
                  />
                </nz-form-item>

                <button nz-button nzType="primary" [nzLoading]="submitting()" type="submit">
                  保存全局系统配置
                </button>
              </form>
            </div>

            <!-- 右列：自建 PKI 根 CA 证书指纹与下载 -->
            <div>
              <h3 class="as-section-title">
                <span nz-icon nzType="safety-certificate"></span>
                <span>自建 PKI 根 CA 证书与指纹固定（第 7.4 节）</span>
              </h3>
              <div class="as-subtle-box">
                <p style="font-size: 13px; color: #475569; margin-top: 0;">
                  各端（开发 Agent、更新 Agent、接入方后端）均固定下方根 CA 指纹，日后更换 IP 或轮换服务端证书无需修改各端配置。请在您的电脑与手机系统信任库中安装一次根证书，即可消除后台与 Mac 预览实例的浏览器证书警告。
                </p>

                <nz-descriptions nzBordered [nzColumn]="1" nzSize="small" style="background: #fff; margin-bottom: 16px;">
                  <nz-descriptions-item nzTitle="根 CA SHA-256 指纹">
                    <div class="as-mono" style="word-break: break-all; font-weight: 600; color: #0f172a;">
                      {{ pkiInfo().caFingerprint || '尚未读取' }}
                    </div>
                    @if (pkiInfo().caFingerprint) {
                      <button
                        nz-button
                        nzSize="small"
                        style="margin-top: 6px;"
                        type="button"
                        (click)="copyVal(pkiInfo().caFingerprint, 'CA 证书指纹')"
                      >
                        <span nz-icon nzType="copy"></span>复制 CA 指纹
                      </button>
                    }
                  </nz-descriptions-item>

                  <nz-descriptions-item nzTitle="管理后台页面端口 (Tailnet)">
                    <span class="as-mono">{{ pkiInfo().tailnetListenAddr || '-' }}</span>
                  </nz-descriptions-item>

                  <nz-descriptions-item nzTitle="独立外部调用端口 (Open API)">
                    <div style="display: flex; align-items: center; justify-content: space-between; gap: 8px; flex-wrap: wrap;">
                      <span class="as-mono" style="font-weight: 600; color: #15803d;">{{ pkiInfo().publicListenAddr || '-' }}</span>
                      <button nz-button nzSize="small" type="button" (click)="openExternalPortModal()">
                        修改外部端口
                      </button>
                    </div>
                  </nz-descriptions-item>

                  <nz-descriptions-item nzTitle="Server 签名公钥 (Ed25519)">
                    <span class="as-mono" style="word-break: break-all;">
                      {{ pkiInfo().serverSignPubB64 || '-' }}
                    </span>
                  </nz-descriptions-item>
                </nz-descriptions>

                <!-- 根 CA 证书下载直链 -->
                <a
                  nz-button
                  nzType="primary"
                  nzBlock
                  href="/api/v1/admin/pki/ca.crt"
                  target="_blank"
                  download="autoship-ca.crt"
                >
                  <span nz-icon nzType="download"></span>
                  <span>下载 autoShip 根 CA 证书 (/api/v1/admin/pki/ca.crt)</span>
                </a>
              </div>
            </div>
          </div>
        </nz-tab>
      </nz-tabs>
    </div>

    <!-- 新建产品弹窗 -->
    <nz-modal
      [nzVisible]="newProductModalVisible()"
      nzTitle="新建纳管产品主档案"
      [nzWidth]="660"
      [nzOkLoading]="submitting()"
      (nzOnCancel)="newProductModalVisible.set(false)"
      (nzOnOk)="submitCreateProduct()"
    >
      <ng-container *nzModalContent>
        <form nz-form nzLayout="vertical">
          <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px;">
            <nz-form-item>
              <nz-form-label nzRequired>产品唯一英文编码</nz-form-label>
              <nz-form-control>
                <input nz-input name="npCode" [(ngModel)]="newProductForm.code" placeholder="如 seccenter" />
              </nz-form-control>
            </nz-form-item>
            <nz-form-item>
              <nz-form-label nzRequired>产品中文名称</nz-form-label>
              <nz-form-control>
                <input nz-input name="npName" [(ngModel)]="newProductForm.name" placeholder="如 安全运营中心" />
              </nz-form-control>
            </nz-form-item>
          </div>
          <nz-form-item>
            <nz-form-label>Git 代码仓库地址</nz-form-label>
            <nz-form-control>
              <input nz-input name="npRepo" [(ngModel)]="newProductForm.repoUrl" placeholder="git@github.com:org/repo.git" />
            </nz-form-control>
          </nz-form-item>
          <nz-form-item>
            <nz-form-label>产品简介</nz-form-label>
            <nz-form-control>
              <textarea nz-input name="npDesc" rows="2" [(ngModel)]="newProductForm.description"></textarea>
            </nz-form-control>
          </nz-form-item>
        </form>
      </ng-container>
    </nz-modal>

    <!-- 产品全量档案配置宽抽屉（5 大子面板） -->
    <nz-drawer
      [nzVisible]="productDrawerVisible()"
      [nzWidth]="980"
      [nzTitle]="'产品档案全量配置 · ' + (productBundle()?.product?.name || '')"
      (nzOnClose)="productDrawerVisible.set(false)"
    >
      <ng-container *nzDrawerContent>
        <nz-spin [nzSpinning]="productBundleLoading()">
          @if (productBundle(); as pb) {
            <nz-tabs [(nzSelectedIndex)]="productSubTabIndex">
              <!-- 子面板 1：仓库、构建测试命令、预览配方与发布配方 -->
              <nz-tab nzTitle="1. 仓库 · 命令 · 预览与发布配方">
                <form nz-form nzLayout="vertical" (ngSubmit)="saveProductMain()">
                  <div style="display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 12px;">
                    <nz-form-item>
                      <nz-form-label nzRequired>产品中文名</nz-form-label>
                      <nz-form-control>
                        <input nz-input name="pmName" [(ngModel)]="pb.product.name" />
                      </nz-form-control>
                    </nz-form-item>
                    <nz-form-item>
                      <nz-form-label>Git 仓库地址</nz-form-label>
                      <nz-form-control>
                        <input nz-input name="pmRepo" [(ngModel)]="pb.product.repoUrl" />
                      </nz-form-control>
                    </nz-form-item>
                    <nz-form-item>
                      <nz-form-label>默认主分支</nz-form-label>
                      <nz-form-control>
                        <input nz-input name="pmBranch" [(ngModel)]="pb.product.defaultBranch" />
                      </nz-form-control>
                    </nz-form-item>
                  </div>

                  <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px;">
                    <nz-form-item>
                      <nz-form-label>安装依赖命令 (installCmd)</nz-form-label>
                      <nz-form-control>
                        <input nz-input class="as-mono" name="pmInst" [(ngModel)]="pb.product.installCmd" />
                      </nz-form-control>
                    </nz-form-item>
                    <nz-form-item>
                      <nz-form-label>全量构建命令 (buildCmd)</nz-form-label>
                      <nz-form-control>
                        <input nz-input class="as-mono" name="pmBuild" [(ngModel)]="pb.product.buildCmd" />
                      </nz-form-control>
                    </nz-form-item>
                    <nz-form-item>
                      <nz-form-label>全量测试命令 (testCmd)</nz-form-label>
                      <nz-form-control>
                        <input nz-input class="as-mono" name="pmTest" [(ngModel)]="pb.product.testCmd" />
                      </nz-form-control>
                    </nz-form-item>
                    <nz-form-item>
                      <nz-form-label>静态检查命令 (lintCmd)</nz-form-label>
                      <nz-form-control>
                        <input nz-input class="as-mono" name="pmLint" [(ngModel)]="pb.product.lintCmd" />
                      </nz-form-control>
                    </nz-form-item>
                  </div>

                  <nz-divider nzText="Mac 预览实例配方"></nz-divider>
                  <div style="display: grid; grid-template-columns: 1.5fr 1fr 1fr; gap: 12px;">
                    <nz-form-item>
                      <nz-form-label>预览启动命令 (previewStartCmd)</nz-form-label>
                      <nz-form-control>
                        <input nz-input class="as-mono" name="pmPvCmd" [(ngModel)]="pb.product.previewStartCmd" />
                      </nz-form-control>
                    </nz-form-item>
                    <nz-form-item>
                      <nz-form-label>需分配端口数</nz-form-label>
                      <nz-form-control>
                        <nz-input-number name="pmPvPort" [(ngModel)]="pb.product.previewPortCount" [nzMin]="1" style="width:100%;"></nz-input-number>
                      </nz-form-control>
                    </nz-form-item>
                    <nz-form-item>
                      <nz-form-label>测试/预览库前缀</nz-form-label>
                      <nz-form-control>
                        <input nz-input class="as-mono" name="pmPvDb" [(ngModel)]="pb.product.previewDbPrefix" />
                      </nz-form-control>
                    </nz-form-item>
                  </div>

                  <div style="display: grid; grid-template-columns: 1.5fr 1fr; gap: 12px;">
                    <nz-form-item>
                      <nz-form-label>演示数据生成命令 (previewSeedCmd)</nz-form-label>
                      <nz-form-control>
                        <input nz-input class="as-mono" name="pmSeed" [(ngModel)]="pb.product.previewSeedCmd" />
                      </nz-form-control>
                    </nz-form-item>
                    <nz-form-item>
                      <nz-form-label>单命令最大耗时秒数</nz-form-label>
                      <nz-form-control>
                        <nz-input-number name="pmTo" [(ngModel)]="pb.product.commandTimeoutSec" [nzMin]="30" style="width:100%;"></nz-input-number>
                      </nz-form-control>
                    </nz-form-item>
                  </div>

                  <nz-divider nzText="发布签名与部署钩子契约"></nz-divider>
                  <div style="display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 12px;">
                    <nz-form-item>
                      <nz-form-label>健康检查路径</nz-form-label>
                      <nz-form-control>
                        <input nz-input class="as-mono" name="pmHc" [(ngModel)]="pb.product.healthCheckPath" />
                      </nz-form-control>
                    </nz-form-item>
                    <nz-form-item>
                      <nz-form-label>变更日志文件</nz-form-label>
                      <nz-form-control>
                        <input nz-input class="as-mono" name="pmCl" [(ngModel)]="pb.product.changelogFile" />
                      </nz-form-control>
                    </nz-form-item>
                    <nz-form-item>
                      <nz-form-label>部署钩子相对路径</nz-form-label>
                      <nz-form-control>
                        <input nz-input class="as-mono" name="pmHook" [(ngModel)]="pb.product.deployHookPath" />
                      </nz-form-control>
                    </nz-form-item>
                  </div>

                  <nz-form-item>
                    <nz-form-label>制品清单验签公钥 (Ed25519 Base64)</nz-form-label>
                    <nz-form-control>
                      <input nz-input class="as-mono" name="pmSignPub" [(ngModel)]="pb.product.releaseSignPubB64" />
                    </nz-form-control>
                  </nz-form-item>

                  <nz-form-item>
                    <nz-form-label>产品专属开发规范约束 (最高优先级注入编码与评审模型)</nz-form-label>
                    <nz-form-control>
                      <textarea nz-input rows="3" name="pmRules" [(ngModel)]="pb.product.devRulesPrompt"></textarea>
                    </nz-form-control>
                  </nz-form-item>

                  <div style="display: flex; gap: 20px; flex-wrap: wrap; margin-bottom: 16px;">
                    <label nz-checkbox name="c1" [(ngModel)]="pb.product.intakeEnabled">启用需求收集</label>
                    <label nz-checkbox name="c2" [(ngModel)]="pb.product.autoDevEnabled">启用自动开发</label>
                    <label nz-checkbox name="c3" [(ngModel)]="pb.product.autoDeployEnabled">启用自动部署</label>
                    <label nz-checkbox name="c4" [(ngModel)]="pb.product.planConfirmDefault">默认开启方案确认门</label>
                    <label nz-checkbox name="c5" [(ngModel)]="pb.product.previewSupportsTls">预览程序原生支持外部 TLS 证书</label>
                  </div>

                  <button nz-button nzType="primary" [nzLoading]="submitting()" type="submit">
                    保存产品主档案
                  </button>
                </form>
              </nz-tab>

              <!-- 子面板 2：业务模块表 -->
              <nz-tab [nzTitle]="'2. 业务模块表 (' + editModules.length + ')'">
                <div style="margin-bottom: 12px; display: flex; justify-content: space-between;">
                  <span style="font-size: 13px; color: #64748b;">
                    用于自动分拣归属、划定“本模块区 vs 其他模块区”以及 Chrome 页面巡检主路由对比
                  </span>
                  <button nz-button nzSize="small" (click)="addModuleRow()">+ 添加模块</button>
                </div>

                @for (m of editModules; track $index) {
                  <div class="as-subtle-box" style="margin-bottom: 10px;">
                    <div style="display: grid; grid-template-columns: 1fr 1fr 1.5fr auto; gap: 10px; margin-bottom: 8px;">
                      <input nz-input [(ngModel)]="m.code" placeholder="模块编码 (如 auth)" />
                      <input nz-input [(ngModel)]="m.name" placeholder="模块中文名 (如 认证鉴权)" />
                      <input nz-input [(ngModel)]="m.testCmd" placeholder="模块增量单测命令 (可选)" />
                      <button nz-button nzDanger nzSize="small" (click)="editModules.splice($index, 1)">移除</button>
                    </div>
                    <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 10px;">
                      <input nz-input class="as-mono" [(ngModel)]="m.pathsJson" placeholder='目录前缀 JSON，如 ["internal/modules/auth","web/src/app/auth"]' />
                      <input nz-input class="as-mono" [(ngModel)]="m.pageUrlsJson" placeholder='巡检主路由 JSON，如 ["/login","/users"]' />
                    </div>
                  </div>
                }

                <button nz-button nzType="primary" [nzLoading]="submitting()" (click)="saveModules()">
                  保存业务模块划分
                </button>
              </nz-tab>

              <!-- 子面板 3：范围与公共区规则 -->
              <nz-tab [nzTitle]="'3. 范围与公共区规则 (' + editScopeRules.length + ')'">
                <div style="margin-bottom: 12px; display: flex; justify-content: space-between;">
                  <span style="font-size: 13px; color: #64748b;">
                    命中公共区（public_zone）的改动不拦截，但自动打特殊标记并追加一轮独立“系统性影响审计”
                  </span>
                  <button nz-button nzSize="small" (click)="addScopeRuleRow()">+ 添加范围规则</button>
                </div>

                @for (sr of editScopeRules; track $index) {
                  <div class="as-subtle-box" style="display: grid; grid-template-columns: 180px 1.2fr 1fr auto; gap: 10px; margin-bottom: 8px; align-items: center;">
                    <nz-select [(ngModel)]="sr.zoneType">
                      <nz-option nzValue="public_zone" nzLabel="公共区 (public_zone)"></nz-option>
                      <nz-option nzValue="protected" nzLabel="受保护路径 (protected)"></nz-option>
                    </nz-select>
                    <input nz-input class="as-mono" [(ngModel)]="sr.pathPattern" placeholder="如 web/src/app/shared/** 或 go.mod" />
                    <input nz-input [(ngModel)]="sr.note" placeholder="用途说明" />
                    <button nz-button nzDanger nzSize="small" (click)="editScopeRules.splice($index, 1)">删除</button>
                  </div>
                }

                <button nz-button nzType="primary" [nzLoading]="submitting()" (click)="saveScopeRules()">
                  保存改动范围规则
                </button>
              </nz-tab>

              <!-- 子面板 4：沙箱权限档案与被拦截路径一键放行（8.3 节） -->
              <nz-tab nzTitle="4. 权限档案与拦截一键放行">
                <nz-alert
                  nzType="info"
                  nzShowIcon
                  nzMessage="沙箱原则：只限制开发 Agent 启动的子进程，不限制您自己的终端与账号；若隔离妨碍依赖安装或工具运行，可一键放行路径或切为关闭档。"
                  style="margin-bottom: 14px;"
                ></nz-alert>

                <!-- 被拦截路径一键放行面板 -->
                <div class="blocked-allow-box">
                  <div style="font-weight: 600; color: #cf1322; margin-bottom: 6px;">
                    <span nz-icon nzType="safety"></span>
                    <span>被权限档案拦截路径 · 一键加入允许清单</span>
                    @if (pb.permissionProfile?.lastBlockedPath; as blockedPath) {
                      <nz-tag nzColor="error" style="margin-left: 8px;">
                        最近拦截: {{ blockedPath }}
                      </nz-tag>
                    }
                  </div>
                  <div style="display: flex; gap: 10px;">
                    <input
                      nz-input
                      class="as-mono"
                      [(ngModel)]="quickAllowPath"
                      placeholder="输入或确认被沙箱拦截的目录/文件路径，例如 /Users/user1/.cache/custom-tool"
                    />
                    <button
                      nz-button
                      nzType="primary"
                      [nzLoading]="submitting()"
                      (click)="submitAllowBlockedPath()"
                    >
                      一键放行此路径
                    </button>
                  </div>
                </div>

                <form nz-form nzLayout="vertical" (ngSubmit)="savePermissionProfile()">
                  <nz-form-item>
                    <nz-form-label nzRequired>产品隔离档位</nz-form-label>
                    <nz-form-control>
                      <nz-select name="permMode" [(ngModel)]="permForm.mode">
                        <nz-option nzValue="protected" nzLabel="保护档（默认）：禁止读取敏感密钥清单，写入限定在 worktree 与允许目录"></nz-option>
                        <nz-option nzValue="custom" nzLabel="自定义档：在保护档基础上自定义增减路径"></nz-option>
                        <nz-option nzValue="off" nzLabel="关闭档：不做系统级沙箱限制，仅靠工具守卫与全量审计"></nz-option>
                      </nz-select>
                    </nz-form-control>
                  </nz-form-item>

                  <nz-form-item>
                    <nz-form-label>额外允许写入目录列表（每行一个绝对路径）</nz-form-label>
                    <nz-form-control>
                      <textarea
                        nz-input
                        class="as-mono"
                        rows="4"
                        name="permWrite"
                        [(ngModel)]="permForm.allowedWriteText"
                        placeholder="/tmp/custom-build&#10;/Users/user1/.npm"
                      ></textarea>
                    </nz-form-control>
                  </nz-form-item>

                  <nz-form-item>
                    <nz-form-label>额外禁止读取敏感路径列表（每行一个路径）</nz-form-label>
                    <nz-form-control>
                      <textarea
                        nz-input
                        class="as-mono"
                        rows="3"
                        name="permDeny"
                        [(ngModel)]="permForm.extraDeniedText"
                      ></textarea>
                    </nz-form-control>
                  </nz-form-item>

                  <button nz-button nzType="primary" [nzLoading]="submitting()" type="submit">
                    保存权限档案配置
                  </button>
                </form>
              </nz-tab>

              <!-- 子面板 5：产品知识库 -->
              <nz-tab [nzTitle]="'5. 产品知识库 (' + editKnowledge.length + ')'">
                <div style="margin-bottom: 12px; display: flex; justify-content: space-between;">
                  <span style="font-size: 13px; color: #64748b;">
                    供客服 AI 当场解答用户咨询及按产品 UI 风格生成低保真 HTML 原型（每次发布后也会自动同步项目地图）
                  </span>
                  <button nz-button nzSize="small" (click)="addKnowledgeRow()">+ 新增知识条目</button>
                </div>

                @for (k of editKnowledge; track $index) {
                  <div class="as-subtle-box" style="margin-bottom: 10px;">
                    <div style="display: grid; grid-template-columns: 160px 1fr auto; gap: 10px; margin-bottom: 8px;">
                      <nz-select [(ngModel)]="k.category">
                        <nz-option nzValue="overview" nzLabel="产品概览 (overview)"></nz-option>
                        <nz-option nzValue="faq" nzLabel="常见问题 (faq)"></nz-option>
                        <nz-option nzValue="ui_style" nzLabel="原型风格规范 (ui_style)"></nz-option>
                        <nz-option nzValue="module" nzLabel="模块说明 (module)"></nz-option>
                        <nz-option nzValue="project_map" nzLabel="项目地图 (project_map)"></nz-option>
                      </nz-select>
                      <input nz-input [(ngModel)]="k.title" placeholder="知识条目标题" />
                      <button nz-button nzDanger nzSize="small" (click)="editKnowledge.splice($index, 1)">删除</button>
                    </div>
                    <textarea nz-input rows="3" [(ngModel)]="k.content" placeholder="Markdown 或结构化产品知识正文"></textarea>
                  </div>
                }

                <button nz-button nzType="primary" [nzLoading]="submitting()" (click)="saveKnowledge()">
                  保存产品知识库
                </button>
              </nz-tab>
            </nz-tabs>
          }
        </nz-spin>
      </ng-container>
    </nz-drawer>

    <!-- 新建/编辑接入方弹窗 -->
    <nz-modal
      [nzVisible]="integrationModalVisible()"
      [nzTitle]="editingIntegrationId ? '编辑接入方配置' : '新建 Open API 外部接入方'"
      [nzOkLoading]="submitting()"
      (nzOnCancel)="integrationModalVisible.set(false)"
      (nzOnOk)="submitIntegration()"
    >
      <ng-container *nzModalContent>
        <form nz-form nzLayout="vertical">
          <nz-form-item>
            <nz-form-label nzRequired>接入方业务系统名称</nz-form-label>
            <nz-form-control>
              <input nz-input name="igName" [(ngModel)]="integrationForm.name" placeholder="如 SecCenter 生产后端" />
            </nz-form-control>
          </nz-form-item>

          <nz-form-item>
            <nz-form-label nzRequired>允许访问公网口的固定来源 IP 白名单（逗号分隔）</nz-form-label>
            <nz-form-control>
              <input
                nz-input
                class="as-mono"
                name="igIps"
                [(ngModel)]="integrationForm.allowedIpsCsv"
                placeholder="例如 203.0.113.25,198.51.100.0/24"
              />
            </nz-form-control>
          </nz-form-item>

          <nz-form-item>
            <nz-form-label>绑定允许提交反馈的产品</nz-form-label>
            <nz-form-control>
              <nz-select
                nzMode="multiple"
                name="igProds"
                [(ngModel)]="integrationForm.productIds"
                nzPlaceHolder="选择一个或多个产品"
              >
                @for (p of products(); track p.id) {
                  <nz-option [nzValue]="p.id" [nzLabel]="p.name + ' (' + p.code + ')'"></nz-option>
                }
              </nz-select>
            </nz-form-control>
          </nz-form-item>

          <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px;">
            <nz-form-item>
              <nz-form-label>每分钟请求配额 (RPM)</nz-form-label>
              <nz-form-control>
                <nz-input-number name="igRpm" [(ngModel)]="integrationForm.rateLimitRpm" [nzMin]="10" style="width:100%;"></nz-input-number>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label>启用状态</nz-form-label>
              <nz-form-control>
                <nz-switch name="igEn" [(ngModel)]="integrationForm.enabled"></nz-switch>
              </nz-form-control>
            </nz-form-item>
          </div>
        </form>
      </ng-container>
    </nz-modal>

    <!-- 接入方创建成功：一次性展示 plainAppSecret 弹窗 -->
    <nz-modal
      [nzVisible]="secretModalVisible()"
      nzTitle="接入方创建成功 · 请立即保存一次性 AppSecret"
      [nzClosable]="false"
      nzOkText="我已妥善保存密钥"
      [nzCancelText]="null"
      (nzOnOk)="secretModalVisible.set(false)"
    >
      <ng-container *nzModalContent>
        <nz-alert
          nzType="warning"
          nzShowIcon
          nzMessage="安全提醒：以下明文 HMAC 签名密钥（plainAppSecret）在服务端仅加密存储，关闭本弹窗后将无法再次查看明文！"
          style="margin-bottom: 14px;"
        ></nz-alert>
        <nz-descriptions nzBordered [nzColumn]="1" nzSize="small">
          <nz-descriptions-item nzTitle="AppID">
            <span class="as-mono">{{ createdSecretInfo()?.appId }}</span>
          </nz-descriptions-item>
          <nz-descriptions-item nzTitle="plainAppSecret（一次性明文）">
            <div class="as-terminal" style="padding: 8px 10px; margin-bottom: 8px;">{{ createdSecretInfo()?.plainAppSecret }}</div>
            <button
              nz-button
              nzType="primary"
              nzSize="small"
              (click)="copyVal(createdSecretInfo()?.plainAppSecret || '', 'plainAppSecret')"
            >
              <span nz-icon nzType="copy"></span>一键复制 plainAppSecret
            </button>
          </nz-descriptions-item>
        </nz-descriptions>
      </ng-container>
    </nz-modal>

    <!-- 签发 Agent 一次性注册令牌弹窗 -->
    <nz-modal
      [nzVisible]="agentTokenModalVisible()"
      nzTitle="新建 Agent 并生成一次性注册令牌（15 分钟有效）"
      [nzOkLoading]="submitting()"
      (nzOnCancel)="agentTokenModalVisible.set(false)"
      (nzOnOk)="submitCreateAgentToken()"
    >
      <ng-container *nzModalContent>
        <form nz-form nzLayout="vertical">
          <nz-form-item>
            <nz-form-label nzRequired>Agent 名称标识</nz-form-label>
            <nz-form-control>
              <input nz-input name="agName" [(ngModel)]="agentTokenForm.name" placeholder="例如 macbook-dev-agent 或 prod-node1-updater" />
            </nz-form-control>
          </nz-form-item>

          <nz-form-item>
            <nz-form-label nzRequired>Agent 角色类型</nz-form-label>
            <nz-form-control>
              <nz-select name="agType" [(ngModel)]="agentTokenForm.agentType">
                <nz-option nzValue="dev" nzLabel="开发 Agent (本 Mac · Tailnet 通信)"></nz-option>
                <nz-option nzValue="update" nzLabel="更新 Agent (产品服务器 · 绑定专属部署目标)"></nz-option>
              </nz-select>
            </nz-form-control>
          </nz-form-item>

          @if (agentTokenForm.agentType === 'update') {
            <nz-form-item>
              <nz-form-label nzRequired>绑定的专属部署目标</nz-form-label>
              <nz-form-control>
                <nz-select name="agTarget" [(ngModel)]="agentTokenForm.deployTargetId" nzPlaceHolder="选择该更新 Agent 归属的部署目标">
                  @for (t of deployTargets(); track t.id) {
                    <nz-option [nzValue]="t.id" [nzLabel]="t.name + ' (#' + t.id + ')'"></nz-option>
                  }
                </nz-select>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label>允许访问公网口的服务器固定 IP 白名单</nz-form-label>
              <nz-form-control>
                <input nz-input class="as-mono" name="agIp" [(ngModel)]="agentTokenForm.allowedIpsCsv" placeholder="如 203.0.113.10" />
              </nz-form-control>
            </nz-form-item>
          }
        </form>
      </ng-container>
    </nz-modal>

    <!-- 一次性展示生成的 Agent 注册令牌结果弹窗 -->
    <nz-modal
      [nzVisible]="tokenResultModalVisible()"
      nzTitle="一次性 Agent 注册令牌已签发（15 分钟内有效）"
      [nzClosable]="false"
      nzOkText="我已复制注册令牌"
      [nzCancelText]="null"
      (nzOnOk)="tokenResultModalVisible.set(false)"
    >
      <ng-container *nzModalContent>
        <nz-alert
          nzType="info"
          nzShowIcon
          [nzMessage]="'有效截止时间：' + formatDateTime(issuedTokenResult()?.expiresAt) + '（首次完成公钥登记后立即自动销毁）'"
          style="margin-bottom: 14px;"
        ></nz-alert>
        <div style="margin-bottom: 10px; font-weight: 600;">一次性注册令牌：</div>
        <div class="as-terminal" style="margin-bottom: 10px;">{{ resolvedIssuedToken() }}</div>
        <div style="display: flex; gap: 10px; margin-bottom: 12px;">
          <button nz-button nzType="primary" nzSize="small" (click)="copyVal(resolvedIssuedToken(), 'Agent 注册令牌')">
            <span nz-icon nzType="copy"></span>复制注册令牌
          </button>
          @if (issuedTokenResult()?.caFingerprint) {
            <button nz-button nzSize="small" (click)="copyVal(issuedTokenResult()!.caFingerprint!, 'CA 指纹')">
              复制固定 CA 指纹
            </button>
          }
        </div>
      </ng-container>
    </nz-modal>

    <!-- 修改独立外部调用端口弹窗（热切换立即生效） -->
    <nz-modal
      [nzVisible]="extPortModalVisible()"
      nzTitle="修改独立外部调用端口（Open API & 更新 Agent）"
      nzOkText="校验并立即切换端口"
      [nzOkLoading]="submitting()"
      (nzOnCancel)="extPortModalVisible.set(false)"
      (nzOnOk)="submitUpdateExternalPort()"
    >
      <ng-container *nzModalContent>
        <nz-alert
          nzType="info"
          nzShowIcon
          [nzMessage]="'当前管理页面监听于 ' + (pkiInfo().tailnetListenAddr || '-') + '。外部调用端口禁止与管理页面共用同一端口，保存后服务端将在线热绑定新端口并写入配置文件。'"
          style="margin-bottom: 14px;"
        ></nz-alert>
        <form nz-form nzLayout="vertical">
          <nz-form-item style="margin-bottom: 0;">
            <nz-form-label nzRequired>外部调用监听地址或端口号</nz-form-label>
            <nz-form-control nzExtra="支持直接填纯端口号（如 9443 -> 自动绑定 0.0.0.0:9443）或完整网卡地址（如 0.0.0.0:19443）；若使用云服务器请同步在安全组放行该端口。">
              <input
                nz-input
                class="as-mono"
                name="extListenAddr"
                [(ngModel)]="extPortFormAddr"
                placeholder="如 9443 或 0.0.0.0:19443"
              />
            </nz-form-control>
          </nz-form-item>
        </form>
      </ng-container>
    </nz-modal>

    <!-- 对外开放接口（Open API v1）对接开发文档宽抽屉 -->
    <nz-drawer
      [nzVisible]="apiDocDrawerVisible()"
      [nzWidth]="920"
      nzTitle="autoShip 对外开放接口（Open API v1）对接开发指南"
      (nzOnClose)="apiDocDrawerVisible.set(false)"
    >
      <ng-container *nzDrawerContent>
        <nz-spin [nzSpinning]="apiDocLoading()">
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; flex-wrap: wrap; gap: 8px;">
            <span style="font-size: 13px; color: #64748b;">
              可直接复制下方完整文档或下载 Markdown 文件发送给需要对接提需求的外部业务系统研发人员
            </span>
            <div style="display: flex; gap: 8px;">
              <button nz-button nzSize="small" (click)="copyVal(apiDocMarkdown(), 'Open API 对接文档')">
                <span nz-icon nzType="copy"></span>一键复制全文
              </button>
              <a
                nz-button
                nzType="primary"
                nzSize="small"
                href="/api/v1/admin/integrations/open-api-doc?download=1"
                target="_blank"
                download="autoShip-OpenAPI-Integration-Guide.md"
              >
                <span nz-icon nzType="download"></span>下载 .md 文件
              </a>
            </div>
          </div>
          <pre class="as-terminal" style="max-height: calc(100vh - 170px); overflow-y: auto; white-space: pre-wrap; line-height: 1.6; font-size: 12.5px;">{{ apiDocMarkdown() }}</pre>
        </nz-spin>
      </ng-container>
    </nz-drawer>
  `,
  styles: [
    `
      .blocked-allow-box {
        background: #fff1f0;
        border: 1px solid #ffa39e;
        border-radius: 8px;
        padding: 12px 14px;
        margin-bottom: 16px;
      }
    `
  ]
})
export class SettingsComponent implements OnInit {
  private readonly api = inject(AdminApiService);
  private readonly route = inject(ActivatedRoute);
  private readonly message = inject(NzMessageService);

  readonly loading = signal<boolean>(false);
  readonly submitting = signal<boolean>(false);

  mainTabIndex = 0;
  productSubTabIndex = 0;

  readonly products = signal<Product[]>([]);
  readonly integrations = signal<IntegrationItem[]>([]);
  readonly agents = signal<AgentNode[]>([]);
  readonly deployTargets = signal<DeployTarget[]>([]);
  readonly hasSavedDirectKey = signal<boolean>(false);
  readonly pkiInfo = signal<SystemSettingsView['pki']>({
    caFingerprint: '',
    publicListenAddr: '',
    tailnetListenAddr: '',
    serverSignPubB64: ''
  });

  // 全局设置表单
  settingsForm = {
    llm: {
      mode: 'relay',
      directBaseURL: '',
      directApiKey: '',
      intakeModel: 'deepseek-v4.1-flash',
      triageModel: 'deepseek-v4.1-flash',
      maxSessionTokens: 32000,
      maxRequestTokens: 4096,
      maskPIIBeforeSend: true
    },
    notify: {
      feishuWebhookURL: '',
      enabled: true
    },
    workflow: {
      autoStartOnNewRequirement: false,
      autoStartOnConfirmStatus: true,
      defaultPlanConfirm: true,
      defaultTokenBudget: 400000,
      defaultMaxTurns: 60,
      defaultMaxMinutes: 90
    },
    artifactKeepCount: 5,
    publicListenAddr: '0.0.0.0:9443'
  };

  // 新建产品弹窗
  readonly newProductModalVisible = signal<boolean>(false);
  newProductForm = {
    code: '',
    name: '',
    repoUrl: '',
    description: ''
  };

  // 产品全量配置抽屉状态
  readonly productDrawerVisible = signal<boolean>(false);
  readonly productBundleLoading = signal<boolean>(false);
  readonly productBundle = signal<ProductFullBundle | null>(null);
  editModules: ProductModule[] = [];
  editScopeRules: ProductScopeRule[] = [];
  editKnowledge: ProductKnowledge[] = [];
  permForm = {
    mode: 'protected',
    allowedWriteText: '',
    extraDeniedText: ''
  };
  quickAllowPath = '';

  // 接入方管理弹窗与一次性密钥弹窗
  readonly integrationModalVisible = signal<boolean>(false);
  editingIntegrationId: number | null = null;
  integrationForm = {
    name: '',
    allowedIpsCsv: '',
    rateLimitRpm: 300,
    enabled: true,
    productIds: [] as number[]
  };
  readonly secretModalVisible = signal<boolean>(false);
  readonly createdSecretInfo = signal<{ appId: string; plainAppSecret: string } | null>(null);

  // Agent 注册令牌弹窗
  readonly agentTokenModalVisible = signal<boolean>(false);
  agentTokenForm = {
    name: '',
    agentType: 'dev',
    deployTargetId: 0,
    allowedIpsCsv: ''
  };
  readonly tokenResultModalVisible = signal<boolean>(false);
  readonly issuedTokenResult = signal<AgentEnrollTokenResult | null>(null);

  // 独立外部调用端口修改弹窗与对接文档抽屉状态
  readonly extPortModalVisible = signal<boolean>(false);
  extPortFormAddr = '0.0.0.0:9443';
  readonly apiDocDrawerVisible = signal<boolean>(false);
  readonly apiDocLoading = signal<boolean>(false);
  readonly apiDocMarkdown = signal<string>('');

  readonly formatDateTime = formatDateTime;

  /**
   * 初始化加载产品、接入方、Agent、部署目标与全局设置，并响应 URL 参数直达产品权限档案。
   * @returns void
   */
  ngOnInit(): void {
    this.reloadAll();
    this.route.queryParamMap.subscribe((q) => {
      const pid = Number(q.get('productId') || 0);
      const sub = q.get('subTab');
      if (pid > 0) {
        this.mainTabIndex = 0;
        this.openProductBundleDrawer(pid, sub === 'permission' ? 3 : 0);
      }
    });
  }

  /**
   * 并行拉取设置中心全部配置数据。
   * @returns void
   */
  reloadAll(): void {
    this.loading.set(true);
    this.api.listProducts().subscribe({
      next: (list) => {
        this.loading.set(false);
        this.products.set(list);
      },
      error: () => this.loading.set(false)
    });
    this.api.listIntegrations().subscribe({
      next: (list) => this.integrations.set(list),
      error: () => {}
    });
    this.api.listAgents().subscribe({
      next: (list) => this.agents.set(list),
      error: () => {}
    });
    this.api.listDeployTargets().subscribe({
      next: (list) => this.deployTargets.set(list),
      error: () => {}
    });
    this.api.getSystemSettings().subscribe({
      next: (s) => {
        if (!s) {
          return;
        }
        this.hasSavedDirectKey.set(Boolean(s.llm?.hasDirectApiKey));
        this.pkiInfo.set(
          s.pki || {
            caFingerprint: '',
            publicListenAddr: '',
            tailnetListenAddr: '',
            serverSignPubB64: ''
          }
        );
        this.settingsForm = {
          llm: {
            mode: s.llm?.mode || 'relay',
            directBaseURL: s.llm?.directBaseURL || '',
            directApiKey: '',
            intakeModel: s.llm?.intakeModel || 'deepseek-v4.1-flash',
            triageModel: s.llm?.triageModel || 'deepseek-v4.1-flash',
            maxSessionTokens: s.llm?.maxSessionTokens || 32000,
            maxRequestTokens: s.llm?.maxRequestTokens || 4096,
            maskPIIBeforeSend: s.llm?.maskPIIBeforeSend !== false
          },
          notify: {
            feishuWebhookURL: s.notify?.feishuWebhookURL || '',
            enabled: s.notify?.enabled !== false
          },
          workflow: {
            autoStartOnNewRequirement: Boolean(s.workflow?.autoStartOnNewRequirement),
            autoStartOnConfirmStatus: s.workflow?.autoStartOnConfirmStatus !== false,
            defaultPlanConfirm: s.workflow?.defaultPlanConfirm !== false,
            defaultTokenBudget: s.workflow?.defaultTokenBudget || 400000,
            defaultMaxTurns: s.workflow?.defaultMaxTurns || 60,
            defaultMaxMinutes: s.workflow?.defaultMaxMinutes || 90
          },
          artifactKeepCount: s.artifactKeepCount || 5,
          publicListenAddr: s.pki?.publicListenAddr || '0.0.0.0:9443'
        };
      },
      error: () => {}
    });
  }

  // ==================== 产品档案配置方法 ====================

  /**
   * 打开新建产品弹窗。
   * @returns void
   */
  openNewProductModal(): void {
    this.newProductForm = { code: '', name: '', repoUrl: '', description: '' };
    this.newProductModalVisible.set(true);
  }

  /**
   * 提交创建新产品档案，创建后自动打开全量配置抽屉。
   * @returns void
   */
  submitCreateProduct(): void {
    if (!this.newProductForm.code.trim() || !this.newProductForm.name.trim()) {
      this.message.warning('请填写产品英文编码与中文名称');
      return;
    }
    this.submitting.set(true);
    this.api
      .createProduct({
        ...this.newProductForm,
        defaultBranch: 'main',
        intakeEnabled: true,
        autoDevEnabled: true,
        autoDeployEnabled: true,
        planConfirmDefault: true
      })
      .subscribe({
        next: (created) => {
          this.submitting.set(false);
          this.newProductModalVisible.set(false);
          this.message.success('产品档案创建成功');
          this.reloadAll();
          if (created?.id) {
            this.openProductBundleDrawer(created.id);
          }
        },
        error: (err: Error) => {
          this.submitting.set(false);
          this.message.error(err.message || '创建产品档案失败');
        }
      });
  }

  /**
   * 删除指定产品档案。
   * @param id 产品主键 ID
   * @returns void
   */
  deleteProduct(id: number): void {
    this.api.deleteProduct(id).subscribe({
      next: () => {
        this.message.success('产品已删除');
        this.reloadAll();
      },
      error: (err: Error) => this.message.error(err.message || '删除产品失败')
    });
  }

  /**
   * 打开产品全量配置抽屉并初始化 5 大子面板表单状态。
   * @param productId 产品主键 ID
   * @param defaultSubTab 默认激活的子面板序号
   * @returns void
   */
  openProductBundleDrawer(productId: number, defaultSubTab = 0): void {
    this.productSubTabIndex = defaultSubTab;
    this.productDrawerVisible.set(true);
    this.productBundleLoading.set(true);
    this.api.getProductBundle(productId).subscribe({
      next: (bundle) => {
        this.productBundleLoading.set(false);
        this.applyBundleState(bundle);
      },
      error: (err: Error) => {
        this.productBundleLoading.set(false);
        this.message.error(err.message || '读取产品详情失败');
      }
    });
  }

  /**
   * 将后端返回的聚合产品档案同步到各编辑面板缓存。
   * @param bundle 产品聚合对象
   * @returns void
   */
  private applyBundleState(bundle: ProductFullBundle): void {
    this.productBundle.set(bundle);
    this.editModules = (bundle.modules || []).map((m) => ({ ...m }));
    this.editScopeRules = (bundle.scopeRules || []).map((r) => ({ ...r }));
    this.editKnowledge = (bundle.knowledge || []).map((k) => ({ ...k }));
    const prof = bundle.permissionProfile;
    const allowArr = parseStringArray(prof?.allowedWritePaths || prof?.allowedWritePathsJson);
    const denyArr = parseStringArray(prof?.extraDeniedReadPaths || prof?.extraDeniedReadJson);
    this.permForm = {
      mode: prof?.mode || 'protected',
      allowedWriteText: allowArr.join('\n'),
      extraDeniedText: denyArr.join('\n')
    };
    this.quickAllowPath = prof?.lastBlockedPath || '';
  }

  /**
   * 保存产品主档案（仓库、构建测试命令、预览配方、发布配方与规范）。
   * @returns void
   */
  saveProductMain(): void {
    const prod = this.productBundle()?.product;
    if (!prod) {
      return;
    }
    this.submitting.set(true);
    this.api.updateProduct(prod.id, prod).subscribe({
      next: () => {
        this.submitting.set(false);
        this.message.success('产品主档案与配方已保存');
        this.reloadAll();
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '保存产品主档案失败');
      }
    });
  }

  /**
   * 添加一行待编辑业务模块。
   * @returns void
   */
  addModuleRow(): void {
    this.editModules.push({
      code: '',
      name: '',
      pathsJson: '[]',
      testCmd: '',
      pageUrlsJson: '[]',
      description: ''
    });
  }

  /**
   * 批量保存产品的业务模块表。
   * @returns void
   */
  saveModules(): void {
    const pid = this.productBundle()?.product?.id;
    if (!pid) {
      return;
    }
    this.submitting.set(true);
    this.api.saveProductModules(pid, this.editModules).subscribe({
      next: (bundle) => {
        this.submitting.set(false);
        this.applyBundleState(bundle);
        this.message.success('业务模块表已保存');
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '保存模块表失败');
      }
    });
  }

  /**
   * 添加一行代码改动范围规则。
   * @returns void
   */
  addScopeRuleRow(): void {
    this.editScopeRules.push({
      zoneType: 'public_zone',
      pathPattern: '',
      note: ''
    });
  }

  /**
   * 批量保存代码改动范围与公共区规则。
   * @returns void
   */
  saveScopeRules(): void {
    const pid = this.productBundle()?.product?.id;
    if (!pid) {
      return;
    }
    this.submitting.set(true);
    this.api.saveProductScopeRules(pid, this.editScopeRules).subscribe({
      next: (bundle) => {
        this.submitting.set(false);
        this.applyBundleState(bundle);
        this.message.success('范围与公共区规则已保存');
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '保存范围规则失败');
      }
    });
  }

  /**
   * 保存产品的开发 Agent 沙箱权限档案配置。
   * @returns void
   */
  savePermissionProfile(): void {
    const pid = this.productBundle()?.product?.id;
    if (!pid) {
      return;
    }
    this.submitting.set(true);
    this.api
      .updateProductPermissionProfile(pid, {
        mode: this.permForm.mode,
        allowedWritePaths: parseStringArray(this.permForm.allowedWriteText),
        extraDeniedReadPaths: parseStringArray(this.permForm.extraDeniedText)
      })
      .subscribe({
        next: () => {
          this.submitting.set(false);
          this.message.success('产品沙箱权限档案已更新');
          this.openProductBundleDrawer(pid, 3);
        },
        error: (err: Error) => {
          this.submitting.set(false);
          this.message.error(err.message || '保存权限档案失败');
        }
      });
  }

  /**
   * 调用 `/api/v1/admin/products/:id/permission-profile/allow-path` 一键放行被沙箱拦截的路径。
   * @returns void
   */
  submitAllowBlockedPath(): void {
    const pid = this.productBundle()?.product?.id;
    const p = this.quickAllowPath.trim();
    if (!pid || !p) {
      this.message.warning('请输入要放行的具体路径');
      return;
    }
    this.submitting.set(true);
    this.api.allowProductBlockedPath(pid, p).subscribe({
      next: () => {
        this.submitting.set(false);
        this.message.success(`已将路径 ${p} 一键加入允许清单`);
        this.openProductBundleDrawer(pid, 3);
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '一键放行路径失败');
      }
    });
  }

  /**
   * 新增一行产品领域知识条目。
   * @returns void
   */
  addKnowledgeRow(): void {
    this.editKnowledge.push({
      category: 'faq',
      title: '',
      content: '',
      source: 'manual'
    });
  }

  /**
   * 批量保存产品领域知识库。
   * @returns void
   */
  saveKnowledge(): void {
    const pid = this.productBundle()?.product?.id;
    if (!pid) {
      return;
    }
    this.submitting.set(true);
    this.api.saveProductKnowledge(pid, this.editKnowledge).subscribe({
      next: (bundle) => {
        this.submitting.set(false);
        this.applyBundleState(bundle);
        this.message.success('产品知识库已更新');
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '保存产品知识失败');
      }
    });
  }

  // ==================== 接入方管理方法 ====================

  /**
   * 打开新建或编辑接入方弹窗。
   * @param item 可选待编辑接入方
   * @returns void
   */
  openIntegrationModal(item?: IntegrationItem): void {
    if (item) {
      this.editingIntegrationId = item.id;
      this.integrationForm = {
        name: item.name,
        allowedIpsCsv: item.allowedIpsCsv || '',
        rateLimitRpm: item.rateLimitRpm || 300,
        enabled: item.enabled,
        productIds: item.productIds || []
      };
    } else {
      this.editingIntegrationId = null;
      this.integrationForm = {
        name: '',
        allowedIpsCsv: '',
        rateLimitRpm: 300,
        enabled: true,
        productIds: this.products().map((p) => p.id)
      };
    }
    this.integrationModalVisible.set(true);
  }

  /**
   * 提交新建或更新接入方；新建成功时弹出一次性明文密钥 `plainAppSecret` 窗口。
   * @returns void
   */
  submitIntegration(): void {
    if (!this.integrationForm.name.trim()) {
      this.message.warning('请填写接入方名称');
      return;
    }
    this.submitting.set(true);
    if (this.editingIntegrationId) {
      this.api.updateIntegration(this.editingIntegrationId, this.integrationForm).subscribe({
        next: () => {
          this.submitting.set(false);
          this.integrationModalVisible.set(false);
          this.message.success('接入方配置已更新');
          this.reloadAll();
        },
        error: (err: Error) => {
          this.submitting.set(false);
          this.message.error(err.message || '更新接入方失败');
        }
      });
      return;
    }

    this.api.createIntegration(this.integrationForm).subscribe({
      next: (res) => {
        this.submitting.set(false);
        this.integrationModalVisible.set(false);
        this.createdSecretInfo.set({
          appId: res.integration.appId,
          plainAppSecret: res.plainAppSecret
        });
        this.secretModalVisible.set(true);
        this.reloadAll();
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '创建接入方失败');
      }
    });
  }

  /**
   * 删除指定外部接入方。
   * @param id 接入方主键 ID
   * @returns void
   */
  deleteIntegration(id: number): void {
    this.api.deleteIntegration(id).subscribe({
      next: () => {
        this.message.success('接入方已删除');
        this.reloadAll();
      },
      error: (err: Error) => this.message.error(err.message || '删除接入方失败')
    });
  }

  // ==================== Agent 注册令牌与吊销方法 ====================

  /**
   * 打开签发一次性 Agent 注册令牌弹窗。
   * @returns void
   */
  openAgentTokenModal(): void {
    this.agentTokenForm = {
      name: '',
      agentType: 'dev',
      deployTargetId: this.deployTargets()[0]?.id || 0,
      allowedIpsCsv: ''
    };
    this.agentTokenModalVisible.set(true);
  }

  /**
   * 提交生成 15 分钟有效的 Agent 注册令牌。
   * @returns void
   */
  submitCreateAgentToken(): void {
    if (!this.agentTokenForm.name.trim()) {
      this.message.warning('请填写 Agent 名称');
      return;
    }
    if (this.agentTokenForm.agentType === 'update' && !this.agentTokenForm.deployTargetId) {
      this.message.warning('更新 Agent 必须选择一个绑定的部署目标');
      return;
    }
    this.submitting.set(true);
    this.api
      .createAgentToken({
        name: this.agentTokenForm.name.trim(),
        agentType: this.agentTokenForm.agentType,
        deployTargetId:
          this.agentTokenForm.agentType === 'update' ? this.agentTokenForm.deployTargetId : 0,
        allowedIpsCsv: this.agentTokenForm.allowedIpsCsv.trim()
      })
      .subscribe({
        next: (res) => {
          this.submitting.set(false);
          this.agentTokenModalVisible.set(false);
          this.issuedTokenResult.set(res);
          this.tokenResultModalVisible.set(true);
          this.reloadAll();
        },
        error: (err: Error) => {
          this.submitting.set(false);
          this.message.error(err.message || '生成注册令牌失败');
        }
      });
  }

  /**
   * 提取签发响应中的明文注册令牌字符串。
   * @returns 注册令牌文本
   */
  resolvedIssuedToken(): string {
    const r = this.issuedTokenResult();
    return String(r?.registrationToken || r?.token || r?.enrollToken || '');
  }

  /**
   * 立即吊销指定 Agent 的身份权限。
   * @param id Agent 主键 ID
   * @returns void
   */
  revokeAgent(id: number): void {
    this.api.revokeAgent(id).subscribe({
      next: () => {
        this.message.success('该 Agent 凭据已成功吊销');
        this.reloadAll();
      },
      error: (err: Error) => this.message.error(err.message || '吊销 Agent 失败')
    });
  }

  // ==================== 全局模型与飞书设置保存 ====================

  /**
   * 打开修改独立外部调用端口弹窗。
   * @returns void
   */
  openExternalPortModal(): void {
    this.extPortFormAddr = this.pkiInfo().publicListenAddr || '0.0.0.0:9443';
    this.extPortModalVisible.set(true);
  }

  /**
   * 提交新外部调用监听端口，由服务端校验与管理后台端口不冲突后在线热切换并持久化。
   * @returns void
   */
  submitUpdateExternalPort(): void {
    const candidate = this.extPortFormAddr.trim();
    if (!candidate) {
      this.message.warning('请输入外部调用监听端口（如 9443 或 0.0.0.0:19443）');
      return;
    }
    this.submitting.set(true);
    this.api
      .updateSystemSettings({
        ...this.settingsForm,
        publicListenAddr: candidate
      })
      .subscribe({
        next: (updated) => {
          this.submitting.set(false);
          this.extPortModalVisible.set(false);
          if (updated?.pki) {
            this.pkiInfo.set(updated.pki);
            this.settingsForm.publicListenAddr = updated.pki.publicListenAddr;
          }
          this.message.success(
            `外部调用端口已热切换至 ${updated?.pki?.publicListenAddr || candidate}`
          );
        },
        error: (err: Error) => {
          this.submitting.set(false);
          this.message.error(err.message || '切换外部调用端口失败');
        }
      });
  }

  /**
   * 打开对外接口（Open API v1）对接开发指南抽屉并拉取最新文档内容。
   * @returns void
   */
  openApiDocDrawer(): void {
    this.apiDocDrawerVisible.set(true);
    if (this.apiDocMarkdown()) {
      return;
    }
    this.apiDocLoading.set(true);
    this.api.getOpenApiDoc().subscribe({
      next: (res) => {
        this.apiDocLoading.set(false);
        this.apiDocMarkdown.set(res?.markdown || '');
      },
      error: (err: Error) => {
        this.apiDocLoading.set(false);
        this.message.error(err.message || '加载对外接口文档失败');
      }
    });
  }

  /**
   * 保存全局系统设置（模型通道切换、飞书通知地址与制品保留份数）。
   * @returns void
   */
  saveGlobalSettings(): void {
    this.submitting.set(true);
    this.api.updateSystemSettings(this.settingsForm).subscribe({
      next: (updated) => {
        this.submitting.set(false);
        this.hasSavedDirectKey.set(Boolean(updated?.llm?.hasDirectApiKey));
        this.settingsForm.llm.directApiKey = '';
        this.message.success('全局系统配置已保存并持久化生效');
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '保存全局配置失败');
      }
    });
  }

  /**
   * 复制指定字段到剪贴板。
   * @param val 待复制文本
   * @param label 提示名称
   * @returns Promise<void>
   */
  async copyVal(val: string, label: string): Promise<void> {
    const ok = await copyTextToClipboard(val);
    if (ok) {
      this.message.success(`已复制${label}`);
    }
  }
}
