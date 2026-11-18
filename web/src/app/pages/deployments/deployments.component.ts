import { CommonModule } from '@angular/common';
import { Component, OnInit, computed, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { NzAlertModule } from 'ng-zorro-antd/alert';
import { NzBadgeModule } from 'ng-zorro-antd/badge';
import { NzButtonModule } from 'ng-zorro-antd/button';
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
import { NzTimelineModule } from 'ng-zorro-antd/timeline';
import {
  DbBackupItem,
  DeployTarget,
  DeploymentDetailBundle,
  DeploymentItem,
  Product
} from '../../core/models/api.models';
import { AdminApiService } from '../../core/services/admin-api.service';
import {
  formatBytes,
  formatDateTime,
  getCheckStatusMeta,
  getDeploymentStatusMeta
} from '../../core/utils/format.utils';

/**
 * autoShip 管理后台部署中心组件（`/deployments`）。
 * 对应设计文档第 5.2、9.1 ~ 9.5 节：
 * 1. 部署执行流水、9 步固定升级进度与故障日志、按部署 ID 幂等手动回滚；
 * 2. 部署目标节点 CRUD 与手动触发数据库备份；
 * 3. 数据库导出备份目录列表、配额占用超限告警与人工恢复指引。
 */
@Component({
  selector: 'app-deployments',
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
    NzTimelineModule,
    NzAlertModule,
    NzEmptyModule,
    NzSpinModule,
    NzSwitchModule,
    NzPopconfirmModule,
    NzDividerModule
  ],
  template: `
    <div class="as-page-header">
      <div>
        <h1 class="as-page-title">
          <span nz-icon nzType="cloud-server"></span>
          <span>生产部署与备份中心</span>
        </h1>
        <p class="as-page-subtitle">
          权限分离更新 Agent + autoship-apply 二次验签升级；失败自动回滚并支持按部署 ID 幂等手动回滚
        </p>
      </div>
      <div style="display: flex; gap: 10px;">
        <button nz-button nzType="default" (click)="refreshAll()">
          <span nz-icon nzType="reload" [nzSpin]="loading()"></span>
          <span>刷新数据</span>
        </button>
        <button nz-button nzType="primary" (click)="openTargetModal()">
          <span nz-icon nzType="plus"></span>
          <span>新建部署目标</span>
        </button>
      </div>
    </div>

    <!-- 全局备份配额超限告警提示 -->
    @if (exceededTargets().length > 0) {
      <nz-alert
        nzType="error"
        nzShowIcon
        nzMessage="存在数据库备份专用目录超出存储上限的部署目标！"
        [nzDescription]="'超限目标：' + exceededTargetNames() + '。平台不自动删除旧备份，请登录对应服务器清理旧归档或在下方【部署目标管理】调高配额上限。'"
        style="margin-bottom: 16px;"
      ></nz-alert>
    }

    <div class="as-card">
      <nz-tabs>
        <!-- ==================== Tab 1：部署记录与分步日志 ==================== -->
        <nz-tab nzTitle="部署执行记录与回滚">
          <div class="as-filter-bar">
            <nz-select
              style="width: 190px;"
              nzPlaceHolder="按产品筛选"
              nzAllowClear
              [(ngModel)]="depFilterProductId"
              (ngModelChange)="searchDeployments()"
            >
              @for (p of products(); track p.id) {
                <nz-option [nzValue]="p.id" [nzLabel]="p.name"></nz-option>
              }
            </nz-select>

            <nz-select
              style="width: 190px;"
              nzPlaceHolder="按部署目标筛选"
              nzAllowClear
              [(ngModel)]="depFilterTargetId"
              (ngModelChange)="searchDeployments()"
            >
              @for (t of targets(); track t.id) {
                <nz-option [nzValue]="t.id" [nzLabel]="t.name"></nz-option>
              }
            </nz-select>

            <nz-select
              style="width: 175px;"
              nzPlaceHolder="部署状态"
              nzAllowClear
              [(ngModel)]="depFilterStatus"
              (ngModelChange)="searchDeployments()"
            >
              <nz-option nzValue="pending_confirm" nzLabel="待确认"></nz-option>
              <nz-option nzValue="backing_up" nzLabel="备份中"></nz-option>
              <nz-option nzValue="deploying" nzLabel="部署中"></nz-option>
              <nz-option nzValue="succeeded" nzLabel="成功"></nz-option>
              <nz-option nzValue="rolled_back" nzLabel="失败已回滚"></nz-option>
              <nz-option nzValue="rollback_failed" nzLabel="回滚失败（紧急）"></nz-option>
            </nz-select>

            <button nz-button nzType="primary" (click)="searchDeployments()">查询</button>
          </div>

          <nz-table
            #depTable
            [nzData]="deployments()"
            [nzLoading]="loading()"
            [nzFrontPagination]="false"
            [nzTotal]="depTotal()"
            [nzPageIndex]="depPage()"
            [nzPageSize]="depPageSize()"
            (nzPageIndexChange)="onDepPageChange($event)"
            nzSize="middle"
          >
            <thead>
              <tr>
                <th nzWidth="80px">ID</th>
                <th nzWidth="150px">产品</th>
                <th nzWidth="165px">部署目标节点</th>
                <th nzWidth="185px">版本变更</th>
                <th nzWidth="110px">数据库备份</th>
                <th nzWidth="145px">执行状态</th>
                <th nzWidth="160px">触发/开始时间</th>
                <th nzWidth="180px">操作</th>
              </tr>
            </thead>
            <tbody>
              @for (d of depTable.data; track d.id) {
                <tr>
                  <td class="as-mono">#{{ d.id }}</td>
                  <td>{{ getProductName(d.productId, d.productName) }}</td>
                  <td><b>{{ getTargetName(d.targetId, d.targetName) }}</b></td>
                  <td class="as-mono">
                    <span>{{ d.previousVersion || d.fromVersion || '初始' }}</span>
                    <span style="color: #1677ff; margin: 0 5px;">→</span>
                    <b>{{ d.targetVersion || d.version }}</b>
                  </td>
                  <td>
                    @if (d.backupDb) {
                      <nz-tag nzColor="cyan">先备份</nz-tag>
                    } @else {
                      <span style="color: #94a3b8;">不备份</span>
                    }
                  </td>
                  <td>
                    <nz-tag [nzColor]="getDeploymentStatusMeta(d.status).color">
                      {{ getDeploymentStatusMeta(d.status).label }}
                    </nz-tag>
                  </td>
                  <td class="as-mono">{{ formatDateTime(d.startedAt || d.createdAt) }}</td>
                  <td>
                    <div style="display: flex; gap: 6px;">
                      <button nz-button nzSize="small" (click)="openDeploymentDrawer(d.id)">
                        分步日志
                      </button>
                      <button
                        nz-button
                        nzDanger
                        nzSize="small"
                        (click)="openRollbackModal(d)"
                      >
                        手动回滚
                      </button>
                    </div>
                  </td>
                </tr>
              }
            </tbody>
          </nz-table>
        </nz-tab>

        <!-- ==================== Tab 2：部署目标管理 ==================== -->
        <nz-tab [nzTitle]="'部署目标节点 (' + targets().length + ')'">
          <div class="as-filter-bar">
            <nz-select
              style="width: 210px;"
              nzPlaceHolder="按产品过滤目标节点"
              nzAllowClear
              [(ngModel)]="targetFilterProductId"
              (ngModelChange)="loadTargets()"
            >
              @for (p of products(); track p.id) {
                <nz-option [nzValue]="p.id" [nzLabel]="p.name"></nz-option>
              }
            </nz-select>
            <button nz-button nzType="primary" (click)="openTargetModal()">+ 新建部署目标</button>
          </div>

          <nz-table
            #tTable
            [nzData]="targets()"
            nzSize="middle"
            [nzShowPagination]="false"
            nzNoResult="尚未配置生产部署目标"
          >
            <thead>
              <tr>
                <th nzWidth="72px">ID</th>
                <th>目标名称 / 环境 / IP</th>
                <th nzWidth="140px">所属产品</th>
                <th nzWidth="125px">当前运行版本</th>
                <th>免登录健康检查接口</th>
                <th nzWidth="220px">备份目录占用 / 配额上限</th>
                <th nzWidth="85px">启用</th>
                <th nzWidth="230px">操作</th>
              </tr>
            </thead>
            <tbody>
              @for (t of tTable.data; track t.id) {
                <tr>
                  <td class="as-mono">#{{ t.id }}</td>
                  <td>
                    <div>
                      <b>{{ t.name }}</b>
                      <nz-tag style="margin-left: 6px;">{{ t.environment || t.env || 'prod' }}</nz-tag>
                    </div>
                    @if (t.serverIp) {
                      <div class="as-mono" style="font-size: 12px; color: #64748b;">IP: {{ t.serverIp }}</div>
                    }
                  </td>
                  <td>{{ getProductName(t.productId, t.productName) }}</td>
                  <td class="as-mono"><b>{{ t.currentVersion || '未上报' }}</b></td>
                  <td class="as-mono" style="font-size: 12px;">{{ t.healthCheckUrl }}</td>
                  <td>
                    <div style="font-size: 12.5px;">
                      <span class="as-mono">{{ formatBytes(t.backupUsedBytes || 0) }}</span> /
                      <span class="as-mono">{{ t.backupQuotaMb || 10240 }} MB</span>
                      @if (isTargetExceeded(t)) {
                        <nz-tag nzColor="error" style="margin-left: 4px;">超限</nz-tag>
                      }
                    </div>
                    <div style="font-size: 11.5px; color: #64748b;">
                      建议保留 {{ t.backupRetainCount || 5 }} 份
                    </div>
                  </td>
                  <td>
                    <nz-badge
                      [nzStatus]="t['enabled'] === false ? 'default' : 'success'"
                      [nzText]="t['enabled'] === false ? '停用' : '启用'"
                    ></nz-badge>
                  </td>
                  <td>
                    <div style="display: flex; gap: 6px; flex-wrap: wrap;">
                      <button nz-button nzSize="small" (click)="openTargetModal(t)">编辑</button>
                      <button
                        nz-button
                        nzType="dashed"
                        nzSize="small"
                        nz-popconfirm
                        nzPopconfirmTitle="确认向该节点的更新 Agent 立即下发数据库备份指令？"
                        (nzOnConfirm)="triggerManualBackup(t)"
                      >
                        <span nz-icon nzType="database"></span>备份库
                      </button>
                      <button
                        nz-button
                        nzDanger
                        nzSize="small"
                        nz-popconfirm
                        nzPopconfirmTitle="确定删除该部署目标吗？"
                        (nzOnConfirm)="deleteTarget(t.id)"
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

        <!-- ==================== Tab 3：数据库备份列表与恢复指引 ==================== -->
        <nz-tab [nzTitle]="'数据库备份与配额监控 (' + backups().length + ')'">
          <nz-alert
            nzType="info"
            nzShowIcon
            nzMessage="数据库备份安全策略：由产品部署钩子 backup-db 以 root 导出至专用目录（更新 Agent 不持有数据库账号）；回滚程序时不自动覆盖生产数据库，如需回退数据请按下方路径与指引人工校核恢复。"
            style="margin-bottom: 14px;"
          ></nz-alert>

          <div class="as-filter-bar">
            <nz-select
              style="width: 190px;"
              nzPlaceHolder="按产品过滤备份"
              nzAllowClear
              [(ngModel)]="bakFilterProductId"
              (ngModelChange)="loadBackups()"
            >
              @for (p of products(); track p.id) {
                <nz-option [nzValue]="p.id" [nzLabel]="p.name"></nz-option>
              }
            </nz-select>

            <nz-select
              style="width: 190px;"
              nzPlaceHolder="按目标节点过滤"
              nzAllowClear
              [(ngModel)]="bakFilterTargetId"
              (ngModelChange)="loadBackups()"
            >
              @for (t of targets(); track t.id) {
                <nz-option [nzValue]="t.id" [nzLabel]="t.name"></nz-option>
              }
            </nz-select>

            <button nz-button nzType="default" (click)="loadBackups()">刷新备份列表</button>
          </div>

          <nz-table
            #bakTable
            [nzData]="backups()"
            nzSize="small"
            [nzPageSize]="15"
            nzNoResult="暂无数据库备份导出记录"
          >
            <thead>
              <tr>
                <th nzWidth="76px">ID</th>
                <th nzWidth="150px">部署目标</th>
                <th nzWidth="105px">关联部署 ID</th>
                <th>专用备份目录路径 (root 专属)</th>
                <th nzWidth="115px">归档大小</th>
                <th nzWidth="150px">校验和摘要</th>
                <th>人工恢复指引</th>
                <th nzWidth="155px">备份时间</th>
              </tr>
            </thead>
            <tbody>
              @for (b of bakTable.data; track b.id) {
                <tr>
                  <td class="as-mono">#{{ b.id }}</td>
                  <td>{{ getTargetName(b.targetId, b.targetName) }}</td>
                  <td class="as-mono">{{ b.deploymentId ? ('#' + b.deploymentId) : '手动备份' }}</td>
                  <td class="as-mono" style="font-size: 12px;">{{ b.backupDir }}</td>
                  <td class="as-mono">{{ formatBytes(b.sizeBytes) }}</td>
                  <td class="as-mono" style="font-size: 11.5px;">
                    {{ (b['checksum'] || b.checksumSha256 || '-').slice(0, 16) }}
                  </td>
                  <td style="font-size: 12.5px; color: #475569;">
                    {{ b.restoreGuide || '以 root 登录服务器按备份目录内归档手动核验恢复' }}
                  </td>
                  <td class="as-mono">{{ formatDateTime(b.createdAt) }}</td>
                </tr>
              }
            </tbody>
          </nz-table>
        </nz-tab>
      </nz-tabs>
    </div>

    <!-- 新建/编辑部署目标弹窗 -->
    <nz-modal
      [nzVisible]="targetModalVisible()"
      [nzTitle]="editingTargetId ? '编辑部署目标节点' : '新建生产部署目标节点'"
      [nzOkLoading]="submitting()"
      (nzOnCancel)="targetModalVisible.set(false)"
      (nzOnOk)="submitTargetForm()"
    >
      <ng-container *nzModalContent>
        <form nz-form nzLayout="vertical">
          <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px;">
            <nz-form-item>
              <nz-form-label nzRequired>所属产品</nz-form-label>
              <nz-form-control>
                <nz-select name="tgProd" [(ngModel)]="targetForm.productId">
                  @for (p of products(); track p.id) {
                    <nz-option [nzValue]="p.id" [nzLabel]="p.name + ' (' + p.code + ')'"></nz-option>
                  }
                </nz-select>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label nzRequired>环境标识</nz-form-label>
              <nz-form-control>
                <nz-select name="tgEnv" [(ngModel)]="targetForm.environment">
                  <nz-option nzValue="prod" nzLabel="生产环境 (prod)"></nz-option>
                  <nz-option nzValue="staging" nzLabel="预发布环境 (staging)"></nz-option>
                </nz-select>
              </nz-form-control>
            </nz-form-item>
          </div>

          <div style="display: grid; grid-template-columns: 1.2fr 1fr; gap: 12px;">
            <nz-form-item>
              <nz-form-label nzRequired>部署目标名称</nz-form-label>
              <nz-form-control>
                <input
                  nz-input
                  name="tgName"
                  [(ngModel)]="targetForm.name"
                  placeholder="例如：华南生产主节点-01"
                />
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label>服务器固定公网/标识 IP</nz-form-label>
              <nz-form-control>
                <input
                  nz-input
                  name="tgIp"
                  [(ngModel)]="targetForm.serverIp"
                  placeholder="如 203.0.113.10"
                />
              </nz-form-control>
            </nz-form-item>
          </div>

          <nz-form-item>
            <nz-form-label nzRequired>本机免登录健康检查 URL</nz-form-label>
            <nz-form-control>
              <input
                nz-input
                name="tgHealth"
                [(ngModel)]="targetForm.healthCheckUrl"
                placeholder="http://127.0.0.1:8080/api/healthz"
              />
            </nz-form-control>
          </nz-form-item>

          <div style="display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 12px;">
            <nz-form-item>
              <nz-form-label nzRequired>备份配额上限 (MB)</nz-form-label>
              <nz-form-control>
                <nz-input-number
                  name="tgQuota"
                  [(ngModel)]="targetForm.backupQuotaMb"
                  [nzMin]="256"
                  [nzStep]="1024"
                  style="width: 100%;"
                ></nz-input-number>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label nzRequired>建议保留份数</nz-form-label>
              <nz-form-control>
                <nz-input-number
                  name="tgRetain"
                  [(ngModel)]="targetForm.backupRetainCount"
                  [nzMin]="1"
                  [nzMax]="50"
                  style="width: 100%;"
                ></nz-input-number>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label>启用参与部署</nz-form-label>
              <nz-form-control>
                <nz-switch name="tgEnabled" [(ngModel)]="targetForm.enabled"></nz-switch>
              </nz-form-control>
            </nz-form-item>
          </div>
        </form>
      </ng-container>
    </nz-modal>

    <!-- 手动回滚确认弹窗 -->
    <nz-modal
      [nzVisible]="rollbackModalVisible()"
      [nzTitle]="'手动触发幂等回滚 · 部署 #' + (activeRollbackDep()?.id || '')"
      [nzOkLoading]="submitting()"
      nzOkText="确认立即下发回滚指令"
      [nzOkDanger]="true"
      (nzOnCancel)="rollbackModalVisible.set(false)"
      (nzOnOk)="submitRollback()"
    >
      <ng-container *nzModalContent>
        <nz-alert
          nzType="warning"
          nzShowIcon
          nzMessage="autoship-apply 将按本次部署 ID 精确定位升级前的程序备份并执行幂等回滚（重复执行不会装回坏版本）。注意：数据库不会自动覆盖，如需恢复数据请对照备份路径手动执行。"
          style="margin-bottom: 14px;"
        ></nz-alert>
        <form nz-form nzLayout="vertical">
          <nz-form-item>
            <nz-form-label nzRequired>手动回滚原因说明</nz-form-label>
            <nz-form-control>
              <textarea
                nz-input
                name="rbReason"
                rows="3"
                [(ngModel)]="rollbackReason"
                placeholder="填写线上观察到的异常现象与手动回滚原因..."
              ></textarea>
            </nz-form-control>
          </nz-form-item>
        </form>
      </ng-container>
    </nz-modal>

    <!-- 单次部署分步日志与故障诊断抽屉 -->
    <nz-drawer
      [nzVisible]="depDrawerVisible()"
      [nzWidth]="880"
      [nzTitle]="'部署分步执行详情 #' + (depDetail()?.deployment?.id || '')"
      (nzOnClose)="depDrawerVisible.set(false)"
    >
      <ng-container *nzDrawerContent>
        <nz-spin [nzSpinning]="depDetailLoading()">
          @if (depDetail()?.deployment; as dp) {
            <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 14px;">
              <div style="display: flex; gap: 8px; align-items: center;">
                <nz-tag [nzColor]="getDeploymentStatusMeta(dp.status).color">
                  {{ getDeploymentStatusMeta(dp.status).label }}
                </nz-tag>
                <span class="as-mono">
                  {{ dp.previousVersion || '初始' }} → <b>{{ dp.targetVersion || dp.version }}</b>
                </span>
              </div>
              <div style="display: flex; gap: 8px;">
                <button nz-button nzSize="small" (click)="openDeploymentDrawer(dp.id)">
                  <span nz-icon nzType="reload"></span>刷新进度
                </button>
                <button nz-button nzDanger nzSize="small" (click)="openRollbackModal(dp)">
                  <span nz-icon nzType="rollback"></span>手动回滚此次部署
                </button>
              </div>
            </div>

            <nz-descriptions nzBordered [nzColumn]="2" nzSize="small" style="margin-bottom: 16px;">
              <nz-descriptions-item nzTitle="安全部署标识 UID">
                <span class="as-mono">{{ dp['deploymentUid'] || dp.id }}</span>
              </nz-descriptions-item>
              <nz-descriptions-item nzTitle="操作确认人">
                {{ dp['confirmedBy'] || dp.triggeredBy || 'admin' }}
              </nz-descriptions-item>
              <nz-descriptions-item nzTitle="开始时间">
                <span class="as-mono">{{ formatDateTime(dp.startedAt || dp.createdAt) }}</span>
              </nz-descriptions-item>
              <nz-descriptions-item nzTitle="结束时间">
                <span class="as-mono">{{ formatDateTime(dp.finishedAt) }}</span>
              </nz-descriptions-item>
            </nz-descriptions>

            <!-- 故障信息面板（当部署回滚或失败时展示） -->
            @if (dp.failedStep || dp['errorTail'] || dp.errorExcerpt || dp['logTail']) {
              <div style="margin-bottom: 16px;">
                <h4 class="as-section-title" style="color: #cf1322;">
                  <span nz-icon nzType="alert"></span>
                  <span>故障现场回传详情（步骤: {{ dp.failedStep || '-' }} / 退出码: {{ dp.exitCode ?? 0 }}）</span>
                </h4>
                @if (dp['errorTail'] || dp.errorExcerpt) {
                  <div style="font-size: 12px; color: #64748b; margin-bottom: 4px;">标准错误末尾：</div>
                  <pre class="as-terminal" style="margin-bottom: 10px;">{{ dp['errorTail'] || dp.errorExcerpt }}</pre>
                }
                @if (dp['logTail']) {
                  <div style="font-size: 12px; color: #64748b; margin-bottom: 4px;">服务启动日志末尾：</div>
                  <pre class="as-terminal" style="margin-bottom: 10px;">{{ dp['logTail'] }}</pre>
                }
                @if (dp['healthResponse'] || dp.healthCheckResp) {
                  <div style="font-size: 12px; color: #64748b; margin-bottom: 4px;">健康检查接口响应：</div>
                  <pre class="as-terminal">{{ dp['healthResponse'] || dp.healthCheckResp }}</pre>
                }
              </div>
            }

            <h4 class="as-section-title">9 步固定升级流程分步轨迹</h4>
            @if ((depDetail()?.steps?.length || 0) === 0) {
              <nz-empty nzNotFoundContent="等待更新 Agent 长轮询拉取指令并上报分步状态..."></nz-empty>
            } @else {
              <nz-timeline>
                @for (st of depDetail()?.steps || []; track $index) {
                  <nz-timeline-item
                    [nzColor]="st.status === 'failed' ? 'red' : (st.status === 'succeeded' ? 'green' : 'blue')"
                  >
                    <div style="display: flex; align-items: center; gap: 8px;">
                      <b class="as-mono">{{ st.stepName || st.name || st.stepCode }}</b>
                      <nz-tag [nzColor]="getCheckStatusMeta(st.status).color">
                        {{ getCheckStatusMeta(st.status).label }}
                      </nz-tag>
                      <span class="as-mono" style="font-size: 12px; color: #64748b;">
                        {{ formatDateTime(st['updatedAt'] || st.finishedAt || st['createdAt']) }}
                      </span>
                    </div>
                    <div style="font-size: 13px; margin-top: 4px; color: #334155;">
                      {{ st['message'] || st.logOutput || st.output || '-' }}
                    </div>
                  </nz-timeline-item>
                }
              </nz-timeline>
            }

            @if ((depDetail()?.backups?.length || 0) > 0) {
              <nz-divider></nz-divider>
              <h4 class="as-section-title">本次部署关联的数据库导出备份</h4>
              @for (bk of depDetail()?.backups || []; track bk.id) {
                <div class="as-subtle-box" style="margin-bottom: 8px;">
                  <div>备份路径：<b class="as-mono">{{ bk.backupDir }}</b> ({{ formatBytes(bk.sizeBytes) }})</div>
                  <div style="font-size: 12px; color: #475569; margin-top: 4px;">
                    恢复说明：{{ bk.restoreGuide }}
                  </div>
                </div>
              }
            }
          }
        </nz-spin>
      </ng-container>
    </nz-drawer>
  `
})
export class DeploymentsComponent implements OnInit {
  private readonly api = inject(AdminApiService);
  private readonly message = inject(NzMessageService);

  readonly products = signal<Product[]>([]);
  readonly targets = signal<DeployTarget[]>([]);
  readonly deployments = signal<DeploymentItem[]>([]);
  readonly depTotal = signal<number>(0);
  readonly depPage = signal<number>(1);
  readonly depPageSize = signal<number>(20);
  readonly backups = signal<DbBackupItem[]>([]);
  readonly loading = signal<boolean>(false);
  readonly submitting = signal<boolean>(false);

  // 过滤条件
  depFilterProductId: number | null = null;
  depFilterTargetId: number | null = null;
  depFilterStatus: string | null = null;
  targetFilterProductId: number | null = null;
  bakFilterProductId: number | null = null;
  bakFilterTargetId: number | null = null;

  // 弹窗与抽屉状态
  readonly targetModalVisible = signal<boolean>(false);
  editingTargetId: number | null = null;
  targetForm = {
    productId: 0,
    name: '',
    environment: 'prod',
    serverIp: '',
    healthCheckUrl: 'http://127.0.0.1:8080/api/healthz',
    backupQuotaMb: 10240,
    backupRetainCount: 5,
    enabled: true
  };

  readonly rollbackModalVisible = signal<boolean>(false);
  readonly activeRollbackDep = signal<DeploymentItem | null>(null);
  rollbackReason = '';

  readonly depDrawerVisible = signal<boolean>(false);
  readonly depDetailLoading = signal<boolean>(false);
  readonly depDetail = signal<DeploymentDetailBundle | null>(null);

  /** 计算当前存在备份存储超限告警的目标节点列表 */
  readonly exceededTargets = computed<DeployTarget[]>(() =>
    this.targets().filter((t) => this.isTargetExceeded(t))
  );

  /** 拼接超限目标节点的名称字符串 */
  readonly exceededTargetNames = computed<string>(() =>
    this.exceededTargets()
      .map((t) => t.name)
      .join('、')
  );

  readonly formatBytes = formatBytes;
  readonly formatDateTime = formatDateTime;
  readonly getDeploymentStatusMeta = getDeploymentStatusMeta;
  readonly getCheckStatusMeta = getCheckStatusMeta;

  /**
   * 组件初始化时并行加载产品、部署目标、部署记录与数据库备份列表。
   * @returns void
   */
  ngOnInit(): void {
    this.api.listProducts().subscribe({
      next: (list) => this.products.set(list),
      error: () => {}
    });
    this.refreshAll();
  }

  /**
   * 刷新部署中心全部页签数据。
   * @returns void
   */
  refreshAll(): void {
    this.loadTargets();
    this.loadDeployments();
    this.loadBackups();
  }

  /**
   * 加载部署目标节点列表。
   * @returns void
   */
  loadTargets(): void {
    this.api.listDeployTargets(this.targetFilterProductId ?? undefined).subscribe({
      next: (list) => this.targets.set(list),
      error: (err: Error) => this.message.error(err.message || '查询部署目标失败')
    });
  }

  /**
   * 分页查询部署执行记录。
   * @returns void
   */
  loadDeployments(): void {
    this.loading.set(true);
    this.api
      .listDeployments({
        productId: this.depFilterProductId ?? undefined,
        targetId: this.depFilterTargetId ?? undefined,
        status: this.depFilterStatus ?? undefined,
        page: this.depPage(),
        pageSize: this.depPageSize()
      })
      .subscribe({
        next: (res) => {
          this.loading.set(false);
          this.deployments.set(res.list);
          this.depTotal.set(res.total);
        },
        error: (err: Error) => {
          this.loading.set(false);
          this.message.error(err.message || '加载部署记录失败');
        }
      });
  }

  /**
   * 查询数据库备份归档记录。
   * @returns void
   */
  loadBackups(): void {
    this.api
      .listBackups({
        productId: this.bakFilterProductId ?? undefined,
        targetId: this.bakFilterTargetId ?? undefined
      })
      .subscribe({
        next: (res) => this.backups.set(res.list),
        error: () => {}
      });
  }

  /**
   * 重置页码至第 1 页检索部署流水。
   * @returns void
   */
  searchDeployments(): void {
    this.depPage.set(1);
    this.loadDeployments();
  }

  /**
   * 部署列表翻页处理。
   * @param idx 页码
   * @returns void
   */
  onDepPageChange(idx: number): void {
    this.depPage.set(idx);
    this.loadDeployments();
  }

  /**
   * 解析产品显示名称。
   * @param productId 产品主键 ID
   * @param fallback 预设名称
   * @returns 产品名
   */
  getProductName(productId: number, fallback?: string): string {
    if (fallback) {
      return fallback;
    }
    return this.products().find((p) => p.id === productId)?.name || `产品 #${productId}`;
  }

  /**
   * 解析部署目标节点显示名称。
   * @param targetId 部署目标 ID
   * @param fallback 预设名称
   * @returns 目标节点名称
   */
  getTargetName(targetId: number, fallback?: string): string {
    if (fallback) {
      return fallback;
    }
    return this.targets().find((t) => t.id === targetId)?.name || `目标节点 #${targetId}`;
  }

  /**
   * 判断单个部署目标备份空间是否超过配额上限。
   * @param t 部署目标
   * @returns 超出配额返回 true
   */
  isTargetExceeded(t: DeployTarget): boolean {
    if (t['quotaExceeded'] || t.storageExceeded) {
      return true;
    }
    if (t.backupQuotaMb && t.backupUsedBytes && t.backupUsedBytes > t.backupQuotaMb * 1024 * 1024) {
      return true;
    }
    return false;
  }

  /**
   * 打开新建或编辑部署目标弹窗。
   * @param existing 可选待编辑部署目标
   * @returns void
   */
  openTargetModal(existing?: DeployTarget): void {
    if (existing) {
      this.editingTargetId = existing.id;
      this.targetForm = {
        productId: existing.productId,
        name: existing.name,
        environment: existing.environment || existing.env || 'prod',
        serverIp: existing.serverIp || '',
        healthCheckUrl: existing.healthCheckUrl || 'http://127.0.0.1:8080/api/healthz',
        backupQuotaMb: existing.backupQuotaMb || 10240,
        backupRetainCount: existing.backupRetainCount || 5,
        enabled: existing['enabled'] !== false
      };
    } else {
      this.editingTargetId = null;
      this.targetForm = {
        productId: this.products()[0]?.id || 0,
        name: '',
        environment: 'prod',
        serverIp: '',
        healthCheckUrl: 'http://127.0.0.1:8080/api/healthz',
        backupQuotaMb: 10240,
        backupRetainCount: 5,
        enabled: true
      };
    }
    this.targetModalVisible.set(true);
  }

  /**
   * 保存部署目标配置（新建或编辑）。
   * @returns void
   */
  submitTargetForm(): void {
    if (!this.targetForm.productId || !this.targetForm.name.trim()) {
      this.message.warning('请选择所属产品并填写部署目标名称');
      return;
    }
    this.submitting.set(true);
    const obs = this.editingTargetId
      ? this.api.updateDeployTarget(this.editingTargetId, this.targetForm)
      : this.api.createDeployTarget(this.targetForm);

    obs.subscribe({
      next: () => {
        this.submitting.set(false);
        this.targetModalVisible.set(false);
        this.message.success(this.editingTargetId ? '部署目标已更新' : '部署目标已创建');
        this.loadTargets();
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '保存部署目标失败');
      }
    });
  }

  /**
   * 删除指定部署目标节点。
   * @param id 部署目标主键 ID
   * @returns void
   */
  deleteTarget(id: number): void {
    this.api.deleteDeployTarget(id).subscribe({
      next: () => {
        this.message.success('部署目标已删除');
        this.loadTargets();
      },
      error: (err: Error) => this.message.error(err.message || '删除部署目标失败')
    });
  }

  /**
   * 向选定部署目标的更新 Agent 下发手动数据库备份指令。
   * @param target 部署目标对象
   * @returns void
   */
  triggerManualBackup(target: DeployTarget): void {
    this.api.triggerTargetBackup(target.id).subscribe({
      next: (res) => {
        const uid = res?.['backupUid'] || '';
        this.message.success(
          uid ? `已向 ${target.name} 下发数据库备份指令 (${uid})` : `已向 ${target.name} 下发备份指令`
        );
        this.loadBackups();
      },
      error: (err: Error) => this.message.error(err.message || '触发数据库备份失败')
    });
  }

  /**
   * 打开单次部署的分步日志抽屉。
   * @param id 部署记录主键 ID
   * @returns void
   */
  openDeploymentDrawer(id: number): void {
    this.depDrawerVisible.set(true);
    this.depDetailLoading.set(true);
    this.api.getDeploymentDetail(id).subscribe({
      next: (bundle) => {
        this.depDetailLoading.set(false);
        this.depDetail.set(bundle);
      },
      error: (err: Error) => {
        this.depDetailLoading.set(false);
        this.message.error(err.message || '读取部署详情失败');
      }
    });
  }

  /**
   * 打开手动回滚确认弹窗。
   * @param dep 目标部署记录
   * @returns void
   */
  openRollbackModal(dep: DeploymentItem): void {
    this.activeRollbackDep.set(dep);
    this.rollbackReason = '';
    this.rollbackModalVisible.set(true);
  }

  /**
   * 提交手动回滚指令。
   * @returns void
   */
  submitRollback(): void {
    const dep = this.activeRollbackDep();
    if (!dep) {
      return;
    }
    if (!this.rollbackReason.trim()) {
      this.message.warning('请填写手动回滚原因说明');
      return;
    }
    this.submitting.set(true);
    this.api.rollbackDeployment(dep.id, { reason: this.rollbackReason.trim() }).subscribe({
      next: () => {
        this.submitting.set(false);
        this.rollbackModalVisible.set(false);
        this.message.success('手动回滚指令已投递至更新 Agent 队列');
        this.loadDeployments();
        if (this.depDrawerVisible() && this.depDetail()?.deployment?.id === dep.id) {
          this.openDeploymentDrawer(dep.id);
        }
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '触发手动回滚失败');
      }
    });
  }
}
