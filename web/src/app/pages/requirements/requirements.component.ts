import { CommonModule } from '@angular/common';
import { Component, OnInit, computed, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { DomSanitizer, SafeHtml } from '@angular/platform-browser';
import { Router } from '@angular/router';
import { NzAlertModule } from 'ng-zorro-antd/alert';
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
import { NzSelectModule } from 'ng-zorro-antd/select';
import { NzSpinModule } from 'ng-zorro-antd/spin';
import { NzSwitchModule } from 'ng-zorro-antd/switch';
import { NzTableModule } from 'ng-zorro-antd/table';
import { NzTabsModule } from 'ng-zorro-antd/tabs';
import { NzTagModule } from 'ng-zorro-antd/tag';
import { NzTimelineModule } from 'ng-zorro-antd/timeline';
import {
  CreateRequirementPayload,
  Product,
  ProductModule,
  RequirementDetailBundle,
  RequirementItem
} from '../../core/models/api.models';
import { AdminApiService } from '../../core/services/admin-api.service';
import {
  AGY_EFFORT_OPTIONS,
  AGY_MODEL_OPTIONS,
  formatDateTime,
  getDeploymentStatusMeta,
  getIssueTypeMeta,
  getPriorityMeta,
  getReleaseStatusMeta,
  getRequirementStatusMeta,
  getWorkStatusMeta
} from '../../core/utils/format.utils';

/**
 * autoShip 管理后台需求池工作台组件（`/requirements`）。
 * 对应设计文档第 4.1、5.2、5.3、5.4 节：支持多维筛选、手动录入需求、分拣排期、相似关联、关闭归档、
 * 一键“开工”，以及在详情抽屉内查看澄清对话、结构化需求卡、零权限 `sandbox=""` 原型沙箱与进行中三对象协同状态。
 */
@Component({
  selector: 'app-requirements',
  standalone: true,
  imports: [
    CommonModule,
    FormsModule,
    NzTableModule,
    NzButtonModule,
    NzSelectModule,
    NzInputModule,
    NzInputNumberModule,
    NzTagModule,
    NzIconModule,
    NzDrawerModule,
    NzModalModule,
    NzFormModule,
    NzTabsModule,
    NzDescriptionsModule,
    NzTimelineModule,
    NzAlertModule,
    NzEmptyModule,
    NzSpinModule,
    NzSwitchModule,
    NzCheckboxModule,
    NzDividerModule
  ],
  template: `
    <div class="as-page-header">
      <div>
        <h1 class="as-page-title">
          <span nz-icon nzType="bulb"></span>
          <span>需求池与澄清归档</span>
          <nz-tag
            [nzColor]="autoStartNewEnabled() ? 'purple' : 'blue'"
            style="margin-left: 8px; font-weight: 500; cursor: pointer;"
            (click)="jumpToWorkflowSettings()"
          >
            {{ autoStartNewEnabled() ? '当前策略：新增需求自动进入研发（点击调整）' : '当前策略：新需求先由人工确认改状态再研发（点击调整）' }}
          </nz-tag>
        </h1>
        <p class="as-page-subtitle">
          集中管理各产品反馈与手动录入需求；支持手动修改任意需求状态、确认立项触发 Antigravity 自动开发，或在系统设置开启新需求自动入研
        </p>
      </div>
      <div style="display: flex; gap: 10px;">
        <button nz-button nzType="default" (click)="loadList()">
          <span nz-icon nzType="reload" [nzSpin]="loading()"></span>
          <span>刷新</span>
        </button>
        <button nz-button nzType="primary" (click)="openCreateModal()">
          <span nz-icon nzType="plus"></span>
          <span>手动录入需求</span>
        </button>
      </div>
    </div>

    <!-- 筛选条件栏 -->
    <div class="as-card">
      <div class="as-filter-bar" style="margin-bottom: 0;">
        <nz-select
          style="width: 180px;"
          nzPlaceHolder="按产品筛选"
          nzAllowClear
          [(ngModel)]="filterProductId"
          (ngModelChange)="onFilterProductChange()"
        >
          @for (p of products(); track p.id) {
            <nz-option [nzValue]="p.id" [nzLabel]="p.name + ' (' + p.code + ')'"></nz-option>
          }
        </nz-select>

        <input
          nz-input
          style="width: 150px;"
          placeholder="模块编码 (如 auth)"
          [(ngModel)]="filterModuleCode"
          (keyup.enter)="searchFromFirstPage()"
        />

        <nz-select
          style="width: 140px;"
          nzPlaceHolder="问题类型"
          nzAllowClear
          [(ngModel)]="filterIssueType"
          (ngModelChange)="searchFromFirstPage()"
        >
          <nz-option nzValue="bug" nzLabel="缺陷修复 (bug)"></nz-option>
          <nz-option nzValue="feature" nzLabel="新增功能 (feature)"></nz-option>
          <nz-option nzValue="improvement" nzLabel="体验改进 (improvement)"></nz-option>
        </nz-select>

        <nz-select
          style="width: 150px;"
          nzPlaceHolder="业务状态"
          nzAllowClear
          [(ngModel)]="filterStatus"
          (ngModelChange)="searchFromFirstPage()"
        >
          <nz-option nzValue="clarifying" nzLabel="澄清中"></nz-option>
          <nz-option nzValue="pending_ai" nzLabel="待 AI 跟进"></nz-option>
          <nz-option nzValue="submitted" nzLabel="已提交"></nz-option>
          <nz-option nzValue="triaged" nzLabel="已分拣"></nz-option>
          <nz-option nzValue="scheduled" nzLabel="待排期"></nz-option>
          <nz-option nzValue="in_progress" nzLabel="进行中"></nz-option>
          <nz-option nzValue="released" nzLabel="已上线"></nz-option>
          <nz-option nzValue="closed" nzLabel="已关闭"></nz-option>
        </nz-select>

        <nz-input-group [nzSuffix]="suffixSearch" style="width: 220px;">
          <input
            nz-input
            placeholder="搜索标题 / 描述关键词"
            [(ngModel)]="filterKeyword"
            (keyup.enter)="searchFromFirstPage()"
          />
        </nz-input-group>
        <ng-template #suffixSearch>
          <span nz-icon nzType="search"></span>
        </ng-template>

        <button nz-button nzType="primary" (click)="searchFromFirstPage()">查询</button>
        <button nz-button nzType="default" (click)="resetFilters()">重置</button>
      </div>
    </div>

    <!-- 需求池数据表格 -->
    <div class="as-card">
      <nz-table
        #reqTable
        [nzData]="items()"
        [nzLoading]="loading()"
        [nzFrontPagination]="false"
        [nzTotal]="total()"
        [nzPageIndex]="page()"
        [nzPageSize]="pageSize()"
        (nzPageIndexChange)="onPageChange($event)"
        (nzPageSizeChange)="onPageSizeChange($event)"
        nzShowSizeChanger
        nzSize="middle"
      >
        <thead>
          <tr>
            <th nzWidth="72px">ID</th>
            <th nzWidth="150px">产品 / 模块</th>
            <th nzWidth="105px">类型</th>
            <th>需求标题</th>
            <th nzWidth="105px">优先级/规模</th>
            <th nzWidth="95px">可开工度</th>
            <th nzWidth="280px">业务状态（进行中联动三对象状态）</th>
            <th nzWidth="155px">提交时间</th>
            <th nzWidth="220px">操作</th>
          </tr>
        </thead>
        <tbody>
          @for (item of reqTable.data; track item.id) {
            <tr>
              <td class="as-mono">#{{ item.id }}</td>
              <td>
                <div style="font-weight: 500;">{{ getProductName(item.productId, item.productName) }}</div>
                <div class="as-mono" style="font-size: 12px; color: #64748b;">
                  {{ item.moduleCode || '未划模块' }}
                </div>
              </td>
              <td>
                <nz-tag [nzColor]="getIssueTypeMeta(item.issueType).color">
                  {{ getIssueTypeMeta(item.issueType).label }}
                </nz-tag>
              </td>
              <td>
                <a style="font-weight: 500;" (click)="openDetailDrawer(item)">{{ item.title }}</a>
                @if (item.releasedVersion || item.releaseVersion) {
                  <nz-tag nzColor="green" style="margin-left: 6px;">
                    {{ item.releasedVersion || item.releaseVersion }}
                  </nz-tag>
                }
              </td>
              <td>
                <nz-tag [nzColor]="getPriorityMeta(item.priority).color">
                  {{ item.priority || 'P2' }}
                </nz-tag>
                <span class="as-mono" style="font-size: 12px; color: #475569;">
                  {{ item.effortSize || 'M' }}
                </span>
              </td>
              <td class="as-mono">{{ item.readinessScore ?? 80 }} 分</td>
              <td>
                <div style="display: flex; flex-direction: column; gap: 6px; align-items: flex-start;">
                  <nz-tag [nzColor]="getRequirementStatusMeta(item.status).color">
                    {{ getRequirementStatusMeta(item.status).label }}
                  </nz-tag>
                  <!-- 4.1 节核心要求：需求进入 in_progress 后，清晰展示开发任务 / 版本 / 部署各自当前状态 -->
                  @if (item.status === 'in_progress') {
                    <div class="as-quad-status">
                      <span>任务: <b>{{ getWorkStatusMeta(resolveWorkStatus(item)).label }}</b></span>
                      <span>|</span>
                      <span>版本: <b>{{ getReleaseStatusMeta(resolveReleaseStatus(item)).label }}</b></span>
                      <span>|</span>
                      <span>部署: <b>{{ getDeploymentStatusMeta(resolveDeployStatus(item)).label }}</b></span>
                    </div>
                  }
                </div>
              </td>
              <td class="as-mono">{{ formatDateTime(item.createdAt) }}</td>
              <td>
                <div style="display: flex; gap: 6px; flex-wrap: wrap;">
                  <button nz-button nzSize="small" (click)="openDetailDrawer(item)">详情</button>
                  <button nz-button nzSize="small" (click)="openStatusModal(item)">改状态</button>
                  @if (isAwaitingConfirmToDo(item)) {
                    <button
                      nz-button
                      nzType="primary"
                      nzSize="small"
                      (click)="openConfirmToDoModal(item)"
                    >
                      确认要做
                    </button>
                  } @else if (canStartWork(item)) {
                    <button nz-button nzType="primary" nzSize="small" (click)="openStartWorkModal(item)">
                      开工
                    </button>
                  }
                  @if (item.status === 'in_progress' && item.activeWorkItemId) {
                    <button
                      nz-button
                      nzType="dashed"
                      nzSize="small"
                      (click)="jumpToWorkItem(item.activeWorkItemId)"
                    >
                      查看任务
                    </button>
                  }
                </div>
              </td>
            </tr>
          }
        </tbody>
      </nz-table>
    </div>

    <!-- 手动录入需求弹窗 -->
    <nz-modal
      [nzVisible]="createModalVisible()"
      nzTitle="手动录入结构化需求"
      [nzWidth]="680"
      [nzOkLoading]="submitting()"
      (nzOnCancel)="createModalVisible.set(false)"
      (nzOnOk)="submitCreateRequirement()"
    >
      <ng-container *nzModalContent>
        <form nz-form nzLayout="vertical">
          <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px;">
            <nz-form-item>
              <nz-form-label nzRequired>所属产品</nz-form-label>
              <nz-form-control>
                <nz-select
                  name="cProd"
                  [(ngModel)]="createForm.productId"
                  nzPlaceHolder="请选择产品"
                  (ngModelChange)="loadProductModules($event)"
                >
                  @for (p of products(); track p.id) {
                    <nz-option [nzValue]="p.id" [nzLabel]="p.name + ' (' + p.code + ')'"></nz-option>
                  }
                </nz-select>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label nzRequired>归属业务模块编码</nz-form-label>
              <nz-form-control>
                @if (selectedProductModules().length > 0) {
                  <nz-select
                    name="cModSel"
                    [(ngModel)]="createForm.moduleCode"
                    nzPlaceHolder="选择已配置的模块"
                    nzAllowClear
                  >
                    @for (m of selectedProductModules(); track m.code) {
                      <nz-option [nzValue]="m.code" [nzLabel]="m.name + ' (' + m.code + ')'"></nz-option>
                    }
                  </nz-select>
                } @else {
                  <input
                    nz-input
                    name="cModInput"
                    [(ngModel)]="createForm.moduleCode"
                    placeholder="输入模块编码，如 auth / order"
                  />
                }
              </nz-form-control>
            </nz-form-item>
          </div>

          <div style="display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 12px;">
            <nz-form-item>
              <nz-form-label nzRequired>需求类型</nz-form-label>
              <nz-form-control>
                <nz-select name="cType" [(ngModel)]="createForm.issueType">
                  <nz-option nzValue="feature" nzLabel="新增功能 (feature)"></nz-option>
                  <nz-option nzValue="bug" nzLabel="缺陷修复 (bug)"></nz-option>
                  <nz-option nzValue="improvement" nzLabel="体验改进 (improvement)"></nz-option>
                </nz-select>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label nzRequired>优先级</nz-form-label>
              <nz-form-control>
                <nz-select name="cPrio" [(ngModel)]="createForm.priority">
                  <nz-option nzValue="P0" nzLabel="P0 紧急阻断"></nz-option>
                  <nz-option nzValue="P1" nzLabel="P1 高优先级"></nz-option>
                  <nz-option nzValue="P2" nzLabel="P2 常规排期"></nz-option>
                  <nz-option nzValue="P3" nzLabel="P3 低优先级"></nz-option>
                </nz-select>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label nzRequired>录入后初始状态</nz-form-label>
              <nz-form-control>
                <nz-select name="cInitStatus" [(ngModel)]="createForm.status">
                  <nz-option nzValue="submitted" nzLabel="已提交（待我确认要做）"></nz-option>
                  <nz-option nzValue="scheduled" nzLabel="待排期"></nz-option>
                  <nz-option nzValue="in_progress" nzLabel="直接进入研发 (in_progress)"></nz-option>
                </nz-select>
              </nz-form-control>
            </nz-form-item>
          </div>

          <nz-form-item>
            <nz-form-label nzRequired>需求标题</nz-form-label>
            <nz-form-control>
              <input
                nz-input
                name="cTitle"
                [(ngModel)]="createForm.title"
                placeholder="简明准确描述问题或目标能力"
              />
            </nz-form-control>
          </nz-form-item>

          <nz-form-item>
            <nz-form-label nzRequired>背景与详细描述</nz-form-label>
            <nz-form-control>
              <textarea
                nz-input
                name="cDesc"
                rows="3"
                [(ngModel)]="createForm.description"
                placeholder="使用角色、业务痛点或报错表现详情"
              ></textarea>
            </nz-form-control>
          </nz-form-item>

          <nz-form-item>
            <nz-form-label nzRequired>可验证的验收标准 (Given / When / Then)</nz-form-label>
            <nz-form-control>
              <textarea
                nz-input
                name="cAc"
                rows="3"
                [(ngModel)]="createForm.acceptanceCriteria"
                placeholder="开发 Agent 将逐条绑定真实测试或巡检证据核验，例如：&#10;1. 当用户提交空表单时返回 HTTP 400 且提示必填项..."
              ></textarea>
            </nz-form-control>
          </nz-form-item>

          <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px;">
            <nz-form-item>
              <nz-form-label>期望行为 / 期望结果</nz-form-label>
              <nz-form-control>
                <textarea
                  nz-input
                  name="cExp"
                  rows="2"
                  [(ngModel)]="createForm.expectedBehavior"
                  placeholder="期望系统表现"
                ></textarea>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label>复现步骤（缺陷类建议填写）</nz-form-label>
              <nz-form-control>
                <textarea
                  nz-input
                  name="cRepro"
                  rows="2"
                  [(ngModel)]="createForm.reproSteps"
                  placeholder="1. 打开页面... 2. 点击..."
                ></textarea>
              </nz-form-control>
            </nz-form-item>
          </div>
        </form>
      </ng-container>
    </nz-modal>

    <!-- 手动设置需求状态 / 确认立项触发研发弹窗 -->
    <nz-modal
      [nzVisible]="statusModalVisible()"
      nzTitle="设置需求状态与自动研发联动"
      [nzOkLoading]="submitting()"
      nzOkText="确认保存状态"
      (nzOnCancel)="statusModalVisible.set(false)"
      (nzOnOk)="submitUpdateStatus()"
    >
      <ng-container *nzModalContent>
        <form nz-form nzLayout="vertical">
          <nz-form-item>
            <nz-form-label nzRequired>目标业务状态</nz-form-label>
            <nz-form-control>
              <nz-select
                name="stTarget"
                [(ngModel)]="statusForm.status"
                (ngModelChange)="onStatusTargetChange($event)"
              >
                <nz-option nzValue="submitted" nzLabel="已提交 · 待确认要做 (submitted)"></nz-option>
                <nz-option nzValue="triaged" nzLabel="已分拣 · 待确认要做 (triaged)"></nz-option>
                <nz-option nzValue="scheduled" nzLabel="确认要做 / 待排期 (scheduled)"></nz-option>
                <nz-option nzValue="in_progress" nzLabel="进入研发 / 进行中 (in_progress)"></nz-option>
                <nz-option nzValue="clarifying" nzLabel="澄清中 (clarifying)"></nz-option>
                <nz-option nzValue="pending_ai" nzLabel="待 AI 跟进 (pending_ai)"></nz-option>
                <nz-option nzValue="released" nzLabel="已上线 (released)"></nz-option>
                <nz-option nzValue="closed" nzLabel="已关闭归档 (closed)"></nz-option>
              </nz-select>
            </nz-form-control>
          </nz-form-item>

          @if (statusForm.status !== 'closed' && statusForm.status !== 'released') {
            <div class="as-subtle-box" style="margin-bottom: 14px;">
              <label
                nz-checkbox
                name="stTriggerWork"
                [(ngModel)]="statusForm.triggerStartWork"
                [nzDisabled]="statusTargetAlreadyHasWork()"
              >
                <b>保存状态后立即创建开发任务，交由开发 Agent (Antigravity) 开始研发</b>
              </label>
              @if (statusTargetAlreadyHasWork()) {
                <div style="font-size: 12px; color: #64748b; margin-top: 4px;">
                  该需求已有关联开发任务，本次仅修改需求状态字段。
                </div>
              } @else if (statusForm.triggerStartWork) {
                <div style="margin-top: 8px; margin-bottom: 10px;">
                  <label nz-checkbox name="stPlanConfirm" [(ngModel)]="statusForm.planConfirmRequired">
                    启用「方案确认门」（先生成技术方案供我确认，再修改代码）
                  </label>
                </div>
                <div style="display: grid; grid-template-columns: 2fr 1fr; gap: 10px;">
                  <nz-form-item style="margin-bottom: 0;">
                    <nz-form-label>本次调用 Antigravity 模型（默认继承系统设置）</nz-form-label>
                    <nz-form-control>
                      <nz-select name="stAgyModel" nzShowSearch [(ngModel)]="statusForm.agyModel">
                        @for (m of agyModelOptions; track m.value) {
                          <nz-option [nzValue]="m.value" [nzLabel]="m.label"></nz-option>
                        }
                      </nz-select>
                    </nz-form-control>
                  </nz-form-item>
                  <nz-form-item style="margin-bottom: 0;">
                    <nz-form-label>推理强度</nz-form-label>
                    <nz-form-control>
                      <nz-select name="stAgyEffort" [(ngModel)]="statusForm.agyEffort">
                        @for (ef of agyEffortOptions; track ef.value) {
                          <nz-option [nzValue]="ef.value" [nzLabel]="ef.label"></nz-option>
                        }
                      </nz-select>
                    </nz-form-control>
                  </nz-form-item>
                </div>
              }
            </div>
          }

          @if (statusForm.status === 'closed') {
            <nz-form-item>
              <nz-form-label nzRequired>关闭原因</nz-form-label>
              <nz-form-control>
                <nz-select name="stCloseReason" [(ngModel)]="statusForm.closeReason">
                  <nz-option nzValue="rejected" nzLabel="拒绝采纳 (rejected)"></nz-option>
                  <nz-option nzValue="duplicate" nzLabel="重复需求 (duplicate)"></nz-option>
                  <nz-option nzValue="deferred" nzLabel="暂不处理 / 延后 (deferred)"></nz-option>
                  <nz-option nzValue="answered" nzLabel="咨询已解答 (answered)"></nz-option>
                </nz-select>
              </nz-form-control>
            </nz-form-item>
          }

          <nz-form-item style="margin-bottom: 0;">
            <nz-form-label>状态变更说明（可选，记入需求时间线）</nz-form-label>
            <nz-form-control>
              <textarea
                nz-input
                name="stNote"
                rows="2"
                [(ngModel)]="statusForm.note"
                placeholder="例如：已确认要做，进入研发排期"
              ></textarea>
            </nz-form-control>
          </nz-form-item>
        </form>
      </ng-container>
    </nz-modal>

    <!-- 分拣定级弹窗 -->
    <nz-modal
      [nzVisible]="triageModalVisible()"
      nzTitle="需求人工分拣与排期定级"
      [nzOkLoading]="submitting()"
      (nzOnCancel)="triageModalVisible.set(false)"
      (nzOnOk)="submitTriage()"
    >
      <ng-container *nzModalContent>
        <form nz-form nzLayout="vertical">
          <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px;">
            <nz-form-item>
              <nz-form-label nzRequired>问题类型</nz-form-label>
              <nz-form-control>
                <nz-select name="tType" [(ngModel)]="triageForm.issueType">
                  <nz-option nzValue="bug" nzLabel="缺陷修复 (bug)"></nz-option>
                  <nz-option nzValue="feature" nzLabel="新增功能 (feature)"></nz-option>
                  <nz-option nzValue="improvement" nzLabel="体验改进 (improvement)"></nz-option>
                </nz-select>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label nzRequired>所属模块编码</nz-form-label>
              <nz-form-control>
                <input nz-input name="tMod" [(ngModel)]="triageForm.moduleCode" placeholder="如 auth" />
              </nz-form-control>
            </nz-form-item>
          </div>

          <div style="display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 12px;">
            <nz-form-item>
              <nz-form-label>优先级</nz-form-label>
              <nz-form-control>
                <nz-select name="tPrio" [(ngModel)]="triageForm.priority">
                  <nz-option nzValue="P0" nzLabel="P0"></nz-option>
                  <nz-option nzValue="P1" nzLabel="P1"></nz-option>
                  <nz-option nzValue="P2" nzLabel="P2"></nz-option>
                  <nz-option nzValue="P3" nzLabel="P3"></nz-option>
                </nz-select>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label>工作量估计</nz-form-label>
              <nz-form-control>
                <nz-select name="tEffort" [(ngModel)]="triageForm.effortSize">
                  <nz-option nzValue="S" nzLabel="S (小型)"></nz-option>
                  <nz-option nzValue="M" nzLabel="M (中型)"></nz-option>
                  <nz-option nzValue="L" nzLabel="L (大型)"></nz-option>
                </nz-select>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label>可开工度 (0-100)</nz-form-label>
              <nz-form-control>
                <nz-input-number
                  name="tScore"
                  [(ngModel)]="triageForm.readinessScore"
                  [nzMin]="0"
                  [nzMax]="100"
                  style="width: 100%;"
                ></nz-input-number>
              </nz-form-control>
            </nz-form-item>
          </div>

          <nz-form-item>
            <nz-form-label nzRequired>分拣后流转状态</nz-form-label>
            <nz-form-control>
              <nz-select name="tStatus" [(ngModel)]="triageForm.status">
                <nz-option nzValue="triaged" nzLabel="已分拣 (triaged)"></nz-option>
                <nz-option nzValue="scheduled" nzLabel="待排期 / 可开工 (scheduled)"></nz-option>
              </nz-select>
            </nz-form-control>
          </nz-form-item>
        </form>
      </ng-container>
    </nz-modal>

    <!-- 点“开工”启动开发任务弹窗 -->
    <nz-modal
      [nzVisible]="startWorkModalVisible()"
      nzTitle="启动自动研发循环（人工关口 1：开工确认）"
      [nzOkLoading]="submitting()"
      nzOkText="正式下发开工指令"
      (nzOnCancel)="startWorkModalVisible.set(false)"
      (nzOnOk)="submitStartWork()"
    >
      <ng-container *nzModalContent>
        <nz-alert
          nzType="info"
          nzShowIcon
          nzMessage="开工后 Server 将通过 Tailnet 向 Mac 开发 Agent 投递任务指令，在独立 git worktree 与测试库中执行编码与门禁。"
          style="margin-bottom: 16px;"
        ></nz-alert>
        <form nz-form nzLayout="vertical">
          <nz-form-item>
            <label nz-checkbox name="swPlan" [(ngModel)]="startWorkForm.planConfirmRequired">
              <b>启用“方案确认门”（推荐默认开启）</b>：先产出拟改动模块、公共文件与表结构变更方案，经我确认后再进入编码会话
            </label>
          </nz-form-item>

          <div style="display: grid; grid-template-columns: 2fr 1fr; gap: 12px;">
            <nz-form-item>
              <nz-form-label>Antigravity (agy) 研发模型</nz-form-label>
              <nz-form-control>
                <nz-select name="swAgyModel" nzShowSearch [(ngModel)]="startWorkForm.agyModel">
                  @for (m of agyModelOptions; track m.value) {
                    <nz-option [nzValue]="m.value" [nzLabel]="m.label"></nz-option>
                  }
                </nz-select>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label>推理强度 (--effort)</nz-form-label>
              <nz-form-control>
                <nz-select name="swAgyEffort" [(ngModel)]="startWorkForm.agyEffort">
                  @for (ef of agyEffortOptions; track ef.value) {
                    <nz-option [nzValue]="ef.value" [nzLabel]="ef.label"></nz-option>
                  }
                </nz-select>
              </nz-form-control>
            </nz-form-item>
          </div>

          <div style="display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 12px;">
            <nz-form-item>
              <nz-form-label nzRequired>Token 预算上限</nz-form-label>
              <nz-form-control>
                <nz-input-number
                  name="swTokens"
                  [(ngModel)]="startWorkForm.tokenBudget"
                  [nzMin]="10000"
                  [nzStep]="50000"
                  style="width: 100%;"
                ></nz-input-number>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label nzRequired>最大会话轮数</nz-form-label>
              <nz-form-control>
                <nz-input-number
                  name="swTurns"
                  [(ngModel)]="startWorkForm.maxTurns"
                  [nzMin]="5"
                  [nzMax]="300"
                  style="width: 100%;"
                ></nz-input-number>
              </nz-form-control>
            </nz-form-item>

            <nz-form-item>
              <nz-form-label nzRequired>最大时长 (分钟)</nz-form-label>
              <nz-form-control>
                <nz-input-number
                  name="swMins"
                  [(ngModel)]="startWorkForm.maxDurationMinutes"
                  [nzMin]="10"
                  [nzMax]="480"
                  style="width: 100%;"
                ></nz-input-number>
              </nz-form-control>
            </nz-form-item>
          </div>
        </form>
      </ng-container>
    </nz-modal>

    <!-- 相似需求关联弹窗 -->
    <nz-modal
      [nzVisible]="linkModalVisible()"
      nzTitle="建立相似或相关需求关联"
      [nzOkLoading]="submitting()"
      (nzOnCancel)="linkModalVisible.set(false)"
      (nzOnOk)="submitLinkRequirement()"
    >
      <ng-container *nzModalContent>
        <form nz-form nzLayout="vertical">
          <nz-form-item>
            <nz-form-label nzRequired>目标需求 ID</nz-form-label>
            <nz-form-control>
              <nz-input-number
                name="lnkId"
                [(ngModel)]="linkForm.targetRequirementId"
                [nzMin]="1"
                style="width: 100%;"
                nzPlaceHolder="输入同产品下的目标需求主键 ID"
              ></nz-input-number>
            </nz-form-control>
          </nz-form-item>

          <nz-form-item>
            <nz-form-label nzRequired>关联关系类型</nz-form-label>
            <nz-form-control>
              <nz-select name="lnkType" [(ngModel)]="linkForm.linkType">
                <nz-option nzValue="similar" nzLabel="相似需求 (similar)"></nz-option>
                <nz-option nzValue="duplicate" nzLabel="重复反馈参考 (duplicate)"></nz-option>
                <nz-option nzValue="related" nzLabel="上下游相关 (related)"></nz-option>
              </nz-select>
            </nz-form-control>
          </nz-form-item>
        </form>
      </ng-container>
    </nz-modal>

    <!-- 关闭并归档需求弹窗 -->
    <nz-modal
      [nzVisible]="closeModalVisible()"
      nzTitle="关闭并只读归档需求"
      [nzOkLoading]="submitting()"
      (nzOnCancel)="closeModalVisible.set(false)"
      (nzOnOk)="submitCloseRequirement()"
    >
      <ng-container *nzModalContent>
        <form nz-form nzLayout="vertical">
          <nz-form-item>
            <nz-form-label nzRequired>关闭原因</nz-form-label>
            <nz-form-control>
              <nz-select name="clsReason" [(ngModel)]="closeForm.reason">
                <nz-option nzValue="rejected" nzLabel="拒绝采纳 (rejected)"></nz-option>
                <nz-option nzValue="duplicate" nzLabel="重复需求 (duplicate)"></nz-option>
                <nz-option nzValue="deferred" nzLabel="暂不处理 / 延后 (deferred)"></nz-option>
                <nz-option nzValue="answered" nzLabel="咨询已当场解答 (answered)"></nz-option>
              </nz-select>
            </nz-form-control>
          </nz-form-item>
          <nz-form-item>
            <nz-form-label nzRequired>归档备注说明</nz-form-label>
            <nz-form-control>
              <textarea
                nz-input
                name="clsNote"
                rows="3"
                [(ngModel)]="closeForm.note"
                placeholder="填写关闭原因说明，将写入需求时间线与操作审计"
              ></textarea>
            </nz-form-control>
          </nz-form-item>
        </form>
      </ng-container>
    </nz-modal>

    <!-- 需求完整详情宽抽屉 -->
    <nz-drawer
      [nzVisible]="drawerVisible()"
      [nzWidth]="940"
      [nzTitle]="'需求详情 #' + (detail()?.requirement?.id || '') + ' · ' + (detail()?.requirement?.title || '')"
      (nzOnClose)="drawerVisible.set(false)"
    >
      <ng-container *nzDrawerContent>
        <nz-spin [nzSpinning]="detailLoading()">
          @if (detail()?.requirement; as req) {
            <!-- 顶栏状态与快捷操作 -->
            <div class="detail-topbar">
              <div style="display: flex; align-items: center; gap: 8px; flex-wrap: wrap;">
                <nz-tag [nzColor]="getRequirementStatusMeta(req.status).color">
                  {{ getRequirementStatusMeta(req.status).label }}
                </nz-tag>
                <nz-tag [nzColor]="getIssueTypeMeta(req.issueType).color">
                  {{ getIssueTypeMeta(req.issueType).label }}
                </nz-tag>
                <nz-tag [nzColor]="getPriorityMeta(req.priority).color">
                  {{ req.priority || 'P2' }}
                </nz-tag>
                <span class="as-mono" style="color: #64748b;">
                  模块: {{ req.moduleCode || '未分配' }} · 工作量: {{ req.effortSize || 'M' }} · 可开工度: {{ req.readinessScore ?? 80 }}
                </span>
              </div>

              <div style="display: flex; gap: 8px; flex-wrap: wrap;">
                <button nz-button nzSize="small" (click)="openStatusModal(req)">修改状态</button>
                @if (req.status !== 'closed' && req.status !== 'released') {
                  <button nz-button nzSize="small" (click)="openTriageModal(req)">分拣定级</button>
                  <button nz-button nzSize="small" (click)="openLinkModal(req)">关联相似需求</button>
                  <button nz-button nzDanger nzSize="small" (click)="openCloseModal(req)">关闭归档</button>
                }
                @if (isAwaitingConfirmToDo(req)) {
                  <button nz-button nzType="primary" nzSize="small" (click)="openConfirmToDoModal(req)">
                    <span nz-icon nzType="check-circle"></span>确认要做并开始研发
                  </button>
                } @else if (canStartWork(req)) {
                  <button nz-button nzType="primary" nzSize="small" (click)="openStartWorkModal(req)">
                    <span nz-icon nzType="play-circle"></span>点“开工”启动开发
                  </button>
                }
              </div>
            </div>

            <!-- 4.1 节重点：当需求处于 in_progress 时，独立卡片并排展示关联 开发任务 / 版本 / 部署 三个对象的当前状态 -->
            @if (req.status === 'in_progress') {
              <div class="quad-banner">
                <div class="quad-banner-title">
                  <span nz-icon nzType="branches"></span>
                  <span>进行中四对象独立状态看板（需求已移交研发闭环，实时联动后三环节当前状态）</span>
                </div>
                <div class="quad-cards">
                  <div class="quad-card">
                    <div class="quad-card-label">1. 关联开发任务 (WorkItem)</div>
                    <div class="quad-card-val">
                      <nz-tag [nzColor]="getWorkStatusMeta(resolveDetailWorkStatus()).color">
                        {{ getWorkStatusMeta(resolveDetailWorkStatus()).label }}
                      </nz-tag>
                      @if (req.activeWorkItemId || detail()?.workItem?.id) {
                        <a
                          class="as-mono"
                          (click)="jumpToWorkItem(req.activeWorkItemId || detail()!.workItem!.id)"
                        >
                          #{{ req.activeWorkItemId || detail()?.workItem?.id }} 查看任务 →
                        </a>
                      }
                    </div>
                  </div>

                  <div class="quad-card">
                    <div class="quad-card-label">2. 关联发布版本 (Release)</div>
                    <div class="quad-card-val">
                      <nz-tag [nzColor]="getReleaseStatusMeta(resolveDetailReleaseStatus()).color">
                        {{ getReleaseStatusMeta(resolveDetailReleaseStatus()).label }}
                      </nz-tag>
                      @if (req.releasedVersion || req.releaseVersion || detail()?.release?.version) {
                        <span class="as-mono">
                          {{ req.releasedVersion || req.releaseVersion || detail()?.release?.version }}
                        </span>
                      }
                    </div>
                  </div>

                  <div class="quad-card">
                    <div class="quad-card-label">3. 生产环境部署 (Deployment)</div>
                    <div class="quad-card-val">
                      <nz-tag [nzColor]="getDeploymentStatusMeta(resolveDetailDeployStatus()).color">
                        {{ getDeploymentStatusMeta(resolveDetailDeployStatus()).label }}
                      </nz-tag>
                    </div>
                  </div>
                </div>
              </div>
            }

            <nz-tabs>
              <!-- Tab 1：结构化需求卡 / 复现卡 -->
              <nz-tab nzTitle="结构化需求卡">
                <nz-descriptions nzBordered [nzColumn]="2" nzSize="small">
                  <nz-descriptions-item nzTitle="所属产品">
                    {{ getProductName(req.productId, req.productName) }}
                  </nz-descriptions-item>
                  <nz-descriptions-item nzTitle="模块编码">
                    <span class="as-mono">{{ req.moduleCode || '-' }}</span>
                  </nz-descriptions-item>
                  <nz-descriptions-item nzTitle="详细背景描述" [nzSpan]="2">
                    <div style="white-space: pre-wrap;">{{ req.description || '暂无描述' }}</div>
                  </nz-descriptions-item>
                  <nz-descriptions-item nzTitle="可验证验收标准 (Given/When/Then)" [nzSpan]="2">
                    <div style="white-space: pre-wrap; font-weight: 500; color: #0f172a;">
                      {{ req.acceptanceCriteria || '尚未填写结构化验收标准' }}
                    </div>
                  </nz-descriptions-item>
                  <nz-descriptions-item nzTitle="期望行为">
                    <div style="white-space: pre-wrap;">{{ req.expectedBehavior || '-' }}</div>
                  </nz-descriptions-item>
                  <nz-descriptions-item nzTitle="复现步骤 / 实际现象">
                    <div style="white-space: pre-wrap;">
                      {{ req.reproSteps || req.actualBehavior || '-' }}
                    </div>
                  </nz-descriptions-item>
                  @if (req.outOfScope) {
                    <nz-descriptions-item nzTitle="不包含的范围" [nzSpan]="2">
                      <div style="white-space: pre-wrap;">{{ req.outOfScope }}</div>
                    </nz-descriptions-item>
                  }
                  @if (req.envInfoJson) {
                    <nz-descriptions-item nzTitle="脱敏环境信息" [nzSpan]="2">
                      <div class="as-mono">{{ req.envInfoJson }}</div>
                    </nz-descriptions-item>
                  }
                  @if (req.closeReason) {
                    <nz-descriptions-item nzTitle="关闭归档说明" [nzSpan]="2">
                      <nz-tag>{{ req.closeReason }}</nz-tag> {{ req.closeNote }}
                    </nz-descriptions-item>
                  }
                </nz-descriptions>

                <!-- 提出人与关注人列表 -->
                <div style="margin-top: 16px;">
                  <h4 class="as-section-title">提交人与附议人 ({{ detail()?.reporters?.length || 0 }})</h4>
                  @if ((detail()?.reporters?.length || 0) === 0) {
                    <span style="color: #64748b; font-size: 13px;">后台管理员手动录入</span>
                  } @else {
                    <div style="display: flex; gap: 8px; flex-wrap: wrap;">
                      @for (rep of detail()?.reporters || []; track $index) {
                        <nz-tag nzColor="blue">
                          {{ rep['displayName'] || rep['externalUid'] || ('提交人 #' + rep['id']) }}
                        </nz-tag>
                      }
                    </div>
                  }
                </div>
              </nz-tab>

              <!-- Tab 2：客服 AI 澄清对话流 -->
              <nz-tab [nzTitle]="'澄清对话记录 (' + (detail()?.messages?.length || 0) + ')'">
                @if ((detail()?.messages?.length || 0) === 0) {
                  <nz-empty nzNotFoundContent="该需求无客服 AI 对话记录（手动录入或表单模式）"></nz-empty>
                } @else {
                  <div class="chat-stream">
                    @for (msg of detail()?.messages || []; track $index) {
                      <div
                        class="chat-bubble"
                        [class.chat-user]="msg['role'] === 'user'"
                        [class.chat-ai]="msg['role'] !== 'user'"
                      >
                        <div class="chat-meta">
                          <b>{{ msg['role'] === 'user' ? '业务系统提交人' : 'autoShip 客服 AI (' + (msg['role'] || 'assistant') + ')' }}</b>
                          <span class="as-mono">{{ formatDateTime(msg['createdAt']) }}</span>
                        </div>
                        <div class="chat-body">{{ msg['content'] || msg['text'] || msg['toolCallJson'] }}</div>
                      </div>
                    }
                  </div>
                }
              </nz-tab>

              <!-- Tab 3：严格零权限沙箱原型预览器 -->
              <nz-tab [nzTitle]="'沙箱原型预览 (' + (detail()?.prototypes?.length || 0) + ')'">
                @if ((detail()?.prototypes?.length || 0) === 0) {
                  <nz-empty nzNotFoundContent="暂无低保真 HTML 原型（缺陷类默认输出复现卡，不出原型）"></nz-empty>
                } @else {
                  <nz-alert
                    nzType="success"
                    nzShowIcon
                    nzMessage="安全沙箱隔离已启用：下方原型运行在严格 sandbox=&quot;&quot; 零权限 iframe 内，禁止一切脚本执行、外部请求、表单提交与同源读取。"
                    style="margin-bottom: 12px;"
                  ></nz-alert>

                  <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px;">
                    <div style="display: flex; align-items: center; gap: 8px;">
                      <span>选择原型迭代版本：</span>
                      <nz-select
                        style="width: 220px;"
                        [ngModel]="selectedPrototypeIdx()"
                        (ngModelChange)="selectedPrototypeIdx.set($event)"
                      >
                        @for (pt of detail()?.prototypes || []; track $index) {
                          <nz-option
                            [nzValue]="$index"
                            [nzLabel]="'版本 v' + (pt['versionNum'] || pt['version'] || ($index + 1)) + ' - ' + (pt['title'] || formatDateTime(pt['createdAt']))"
                          ></nz-option>
                        }
                      </nz-select>
                    </div>

                    <button nz-button nzSize="small" (click)="showProtoSource.set(!showProtoSource())">
                      {{ showProtoSource() ? '切换为渲染视图' : '查看 HTML 源码' }}
                    </button>
                  </div>

                  @if (!showProtoSource()) {
                    <div class="as-sandbox-frame-wrap">
                      <!-- 严格遵守安全契约：sandbox="" 空属性阻断全部脚本和同源权限 -->
                      <iframe
                        class="as-sandbox-iframe"
                        sandbox=""
                        referrerpolicy="no-referrer"
                        [srcdoc]="trustedSelectedPrototypeHtml()"
                        title="低保真需求原型安全沙箱预览"
                      ></iframe>
                    </div>
                  } @else {
                    <pre class="as-terminal">{{ rawSelectedPrototypeHtml() }}</pre>
                  }
                }
              </nz-tab>

              <!-- Tab 4：相似需求与状态时间线 -->
              <nz-tab nzTitle="相似关联与状态时间线">
                <div style="margin-bottom: 20px;">
                  <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px;">
                    <h4 class="as-section-title" style="margin: 0;">已关联相似/相关需求</h4>
                    <button nz-button nzSize="small" (click)="openLinkModal(req)">+ 新增关联</button>
                  </div>
                  @if ((detail()?.similarLinks?.length || 0) === 0) {
                    <div class="as-subtle-box" style="color: #64748b; font-size: 13px;">暂无关联的相似需求</div>
                  } @else {
                    <div style="display: flex; flex-direction: column; gap: 8px;">
                      @for (lnk of detail()?.similarLinks || []; track $index) {
                        <div class="as-subtle-box" style="display: flex; justify-content: space-between; align-items: center;">
                          <div>
                            <nz-tag nzColor="purple">{{ lnk['linkType'] || 'similar' }}</nz-tag>
                            <span class="as-mono">
                              关联需求 #{{ lnk['targetRequirementId'] === req.id ? lnk['sourceRequirementId'] : lnk['targetRequirementId'] }}
                            </span>
                            @if (lnk['targetTitle'] || lnk['title']) {
                              <span style="margin-left: 8px;">{{ lnk['targetTitle'] || lnk['title'] }}</span>
                            }
                          </div>
                          <span style="font-size: 12px; color: #64748b;">
                            由 {{ lnk['createdBy'] || 'system' }} 建立于 {{ formatDateTime(lnk['createdAt']) }}
                          </span>
                        </div>
                      }
                    </div>
                  }
                </div>

                <nz-divider></nz-divider>

                <h4 class="as-section-title">全生命周期状态演进时间线</h4>
                @if ((detail()?.events?.length || 0) === 0) {
                  <nz-empty nzNotFoundContent="暂无时间线事件记录"></nz-empty>
                } @else {
                  <nz-timeline>
                    @for (ev of detail()?.events || []; track $index) {
                      <nz-timeline-item nzColor="blue">
                        <div style="font-weight: 500;">
                          {{ ev['summary'] || ev['eventType'] }}
                          @if (ev['toStatus']) {
                            <nz-tag style="margin-left: 6px;" [nzColor]="getRequirementStatusMeta(ev['toStatus']).color">
                              {{ getRequirementStatusMeta(ev['toStatus']).label }}
                            </nz-tag>
                          }
                        </div>
                        <div style="font-size: 12px; color: #64748b; margin-top: 2px;">
                          操作主体: {{ ev['actor'] || 'system' }} · {{ formatDateTime(ev['createdAt']) }}
                        </div>
                      </nz-timeline-item>
                    }
                  </nz-timeline>
                }
              </nz-tab>
            </nz-tabs>
          }
        </nz-spin>
      </ng-container>
    </nz-drawer>
  `,
  styles: [
    `
      .detail-topbar {
        display: flex;
        justify-content: space-between;
        align-items: center;
        gap: 12px;
        flex-wrap: wrap;
        padding-bottom: 14px;
        margin-bottom: 14px;
        border-bottom: 1px solid #e2e8f0;
      }
      .quad-banner {
        background: linear-gradient(135deg, #f0f5ff 0%, #f9f0ff 100%);
        border: 1px solid #adc6ff;
        border-radius: 10px;
        padding: 14px 16px;
        margin-bottom: 16px;
      }
      .quad-banner-title {
        font-size: 13.5px;
        font-weight: 600;
        color: #1d39c4;
        margin-bottom: 10px;
        display: flex;
        align-items: center;
        gap: 8px;
      }
      .quad-cards {
        display: grid;
        grid-template-columns: repeat(3, minmax(0, 1fr));
        gap: 10px;
      }
      .quad-card {
        background: #ffffff;
        border-radius: 8px;
        padding: 10px 12px;
        border: 1px solid #d6e4ff;
      }
      .quad-card-label {
        font-size: 12px;
        color: #64748b;
        margin-bottom: 6px;
      }
      .quad-card-val {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 6px;
        flex-wrap: wrap;
      }
      .chat-stream {
        display: flex;
        flex-direction: column;
        gap: 12px;
        max-height: 560px;
        overflow-y: auto;
        padding: 4px;
      }
      .chat-bubble {
        border-radius: 10px;
        padding: 10px 14px;
        max-width: 88%;
      }
      .chat-user {
        align-self: flex-end;
        background: #e6f4ff;
        border: 1px solid #91caff;
      }
      .chat-ai {
        align-self: flex-start;
        background: #f8fafc;
        border: 1px solid #e2e8f0;
      }
      .chat-meta {
        display: flex;
        justify-content: space-between;
        gap: 14px;
        font-size: 12px;
        color: #64748b;
        margin-bottom: 6px;
      }
      .chat-body {
        white-space: pre-wrap;
        font-size: 13.5px;
        line-height: 1.6;
        color: #0f172a;
      }
    `
  ]
})
export class RequirementsComponent implements OnInit {
  private readonly api = inject(AdminApiService);
  private readonly sanitizer = inject(DomSanitizer);
  private readonly router = inject(Router);
  private readonly message = inject(NzMessageService);

  readonly products = signal<Product[]>([]);
  readonly selectedProductModules = signal<ProductModule[]>([]);
  readonly items = signal<RequirementItem[]>([]);
  readonly total = signal<number>(0);
  readonly page = signal<number>(1);
  readonly pageSize = signal<number>(20);
  readonly loading = signal<boolean>(false);
  readonly submitting = signal<boolean>(false);

  // 筛选条件
  filterProductId: number | null = null;
  filterModuleCode = '';
  filterIssueType: string | null = null;
  filterStatus: string | null = null;
  filterKeyword = '';

  // 弹窗与抽屉可见性
  readonly createModalVisible = signal<boolean>(false);
  readonly statusModalVisible = signal<boolean>(false);
  readonly statusTargetAlreadyHasWork = signal<boolean>(false);
  readonly triageModalVisible = signal<boolean>(false);
  readonly startWorkModalVisible = signal<boolean>(false);
  readonly linkModalVisible = signal<boolean>(false);
  readonly closeModalVisible = signal<boolean>(false);
  readonly drawerVisible = signal<boolean>(false);
  readonly detailLoading = signal<boolean>(false);
  readonly detail = signal<RequirementDetailBundle | null>(null);

  // 全局研发自动流转策略状态缓存
  readonly autoStartNewEnabled = signal<boolean>(false);
  readonly autoStartOnConfirmEnabled = signal<boolean>(true);
  readonly defaultPlanConfirmEnabled = signal<boolean>(true);
  readonly defaultAgyModel = signal<string>('');
  readonly defaultAgyEffort = signal<string>('');
  readonly agyModelOptions = AGY_MODEL_OPTIONS;
  readonly agyEffortOptions = AGY_EFFORT_OPTIONS;

  // 原型沙箱选择状态
  readonly selectedPrototypeIdx = signal<number>(0);
  readonly showProtoSource = signal<boolean>(false);

  /** 当前选中原型的原始 HTML 字符串 */
  readonly rawSelectedPrototypeHtml = computed<string>(() => {
    const protos = this.detail()?.prototypes || [];
    const idx = this.selectedPrototypeIdx();
    const target = protos[idx] || protos[0];
    if (!target) {
      return '';
    }
    return String(target['htmlContent'] || target['html'] || target['content'] || '');
  });

  /**
   * 为带 `sandbox=""` 严格零权限的 iframe 提供保留完整 `<style>` 布局标签的 srcdoc HTML。
   * 安全保障：外层 `<iframe>` 硬编码 `sandbox=""`（无任何 allow-* 权限），浏览器内核级禁止一切 JS 执行与外联。
   */
  readonly trustedSelectedPrototypeHtml = computed<SafeHtml>(() => {
    return this.sanitizer.bypassSecurityTrustHtml(this.rawSelectedPrototypeHtml());
  });

  // 操作目标 ID
  activeRequirementId = 0;

  // 各弹窗表单数据
  createForm: CreateRequirementPayload = {
    productId: 0,
    moduleCode: '',
    issueType: 'feature',
    title: '',
    priority: 'P2',
    status: 'submitted',
    description: '',
    acceptanceCriteria: '',
    expectedBehavior: '',
    reproSteps: ''
  };

  statusForm = {
    status: 'in_progress',
    triggerStartWork: true,
    planConfirmRequired: true,
    agyModel: '',
    agyEffort: '',
    closeReason: 'deferred',
    note: ''
  };

  triageForm = {
    issueType: 'feature',
    moduleCode: '',
    priority: 'P2',
    effortSize: 'M',
    readinessScore: 85,
    status: 'scheduled'
  };

  startWorkForm = {
    planConfirmRequired: true,
    tokenBudget: 400000,
    maxTurns: 60,
    maxDurationMinutes: 90,
    agyModel: '',
    agyEffort: ''
  };

  linkForm = {
    targetRequirementId: 0,
    linkType: 'similar'
  };

  closeForm: { reason: 'rejected' | 'duplicate' | 'deferred' | 'answered'; note: string } = {
    reason: 'rejected',
    note: ''
  };

  readonly formatDateTime = formatDateTime;
  readonly getRequirementStatusMeta = getRequirementStatusMeta;
  readonly getIssueTypeMeta = getIssueTypeMeta;
  readonly getPriorityMeta = getPriorityMeta;
  readonly getWorkStatusMeta = getWorkStatusMeta;
  readonly getReleaseStatusMeta = getReleaseStatusMeta;
  readonly getDeploymentStatusMeta = getDeploymentStatusMeta;

  /**
   * 初始化加载纳管产品列表与需求池第一页数据。
   * @returns void
   */
  ngOnInit(): void {
    this.api.listProducts().subscribe({
      next: (list) => this.products.set(list),
      error: () => {}
    });
    this.api.getSystemSettings().subscribe({
      next: (s) => {
        if (s?.workflow) {
          this.autoStartNewEnabled.set(Boolean(s.workflow.autoStartOnNewRequirement));
          this.autoStartOnConfirmEnabled.set(s.workflow.autoStartOnConfirmStatus !== false);
          this.defaultPlanConfirmEnabled.set(s.workflow.defaultPlanConfirm !== false);
          this.defaultAgyModel.set(s.workflow.defaultAgyModel || '');
          this.defaultAgyEffort.set(s.workflow.defaultAgyEffort || '');
        }
      },
      error: () => {}
    });
    this.loadList();
  }

  /**
   * 按当前筛选与分页条件请求需求列表。
   * @returns void
   */
  loadList(): void {
    this.loading.set(true);
    this.api
      .listRequirements({
        productId: this.filterProductId ?? undefined,
        moduleCode: this.filterModuleCode,
        issueType: this.filterIssueType ?? undefined,
        status: this.filterStatus ?? undefined,
        keyword: this.filterKeyword,
        page: this.page(),
        pageSize: this.pageSize()
      })
      .subscribe({
        next: (res) => {
          this.loading.set(false);
          this.items.set(res.list);
          this.total.set(res.total);
        },
        error: (err: Error) => {
          this.loading.set(false);
          this.message.error(err.message || '加载需求列表失败');
        }
      });
  }

  /**
   * 筛选产品改变时重置到第 1 页查询。
   * @returns void
   */
  onFilterProductChange(): void {
    this.searchFromFirstPage();
  }

  /**
   * 从第 1 页重新检索需求列表。
   * @returns void
   */
  searchFromFirstPage(): void {
    this.page.set(1);
    this.loadList();
  }

  /**
   * 清空全部筛选条件并重新加载。
   * @returns void
   */
  resetFilters(): void {
    this.filterProductId = null;
    this.filterModuleCode = '';
    this.filterIssueType = null;
    this.filterStatus = null;
    this.filterKeyword = '';
    this.page.set(1);
    this.loadList();
  }

  /**
   * 切换分页页码回调。
   * @param pageIndex 新页码
   * @returns void
   */
  onPageChange(pageIndex: number): void {
    this.page.set(pageIndex);
    this.loadList();
  }

  /**
   * 切换每页条数回调。
   * @param size 新每页大小
   * @returns void
   */
  onPageSizeChange(size: number): void {
    this.pageSize.set(size);
    this.page.set(1);
    this.loadList();
  }

  /**
   * 根据产品 ID 获取可读产品名称。
   * @param productId 产品主键 ID
   * @param fallbackName 需求项自带的产品名称
   * @returns 产品显示名
   */
  getProductName(productId: number, fallbackName?: string): string {
    if (fallbackName) {
      return fallbackName;
    }
    const found = this.products().find((p) => p.id === productId);
    return found ? found.name : `产品 #${productId}`;
  }

  /**
   * 判断需求是否处于刚收到、等待人工确认是否要做的初始阶段（submitted / triaged / clarifying / pending_ai）。
   * @param item 需求对象
   * @returns 是否处于待确认立项阶段
   */
  isAwaitingConfirmToDo(item: RequirementItem): boolean {
    const st = (item.status || '').toLowerCase();
    return st === 'submitted' || st === 'triaged' || st === 'clarifying' || st === 'pending_ai';
  }

  /**
   * 跳转到系统设置页的全局研发流转策略面板。
   * @returns void
   */
  jumpToWorkflowSettings(): void {
    void this.router.navigate(['/settings']);
  }

  /**
   * 判断当前需求是否处于允许点击“开工”的状态。
   * @param item 需求记录
   * @returns 是否可发起开发任务
   */
  canStartWork(item: RequirementItem): boolean {
    const st = (item.status || '').toLowerCase();
    return st === 'submitted' || st === 'triaged' || st === 'scheduled' || st === 'backlog' || st === 'ready';
  }

  /**
   * 解析列表项中关联开发任务的当前状态。
   * @param item 需求对象
   * @returns 开发任务状态编码
   */
  resolveWorkStatus(item: RequirementItem): string {
    return item.workStatus || item.workItemStatus || item.currentStates?.workItemStatus || 'preparing';
  }

  /**
   * 解析列表项中关联版本的当前状态。
   * @param item 需求对象
   * @returns 版本状态编码
   */
  resolveReleaseStatus(item: RequirementItem): string {
    return item.releaseStatus || item.currentStates?.releaseStatus || '';
  }

  /**
   * 解析列表项中关联部署的当前状态。
   * @param item 需求对象
   * @returns 部署状态编码
   */
  resolveDeployStatus(item: RequirementItem): string {
    return item.deployStatus || item.deploymentStatus || item.currentStates?.deploymentStatus || '';
  }

  /**
   * 解析详情抽屉中关联开发任务的当前状态。
   * @returns 开发任务状态字符串
   */
  resolveDetailWorkStatus(): string {
    const d = this.detail();
    if (!d) {
      return '';
    }
    return d.workItem?.status || this.resolveWorkStatus(d.requirement);
  }

  /**
   * 解析详情抽屉中关联版本的当前状态。
   * @returns 版本状态字符串
   */
  resolveDetailReleaseStatus(): string {
    const d = this.detail();
    if (!d) {
      return '';
    }
    return d.release?.status || this.resolveReleaseStatus(d.requirement);
  }

  /**
   * 解析详情抽屉中关联部署的当前状态。
   * @returns 部署状态字符串
   */
  resolveDetailDeployStatus(): string {
    const d = this.detail();
    if (!d) {
      return '';
    }
    return d.deployment?.status || this.resolveDeployStatus(d.requirement);
  }

  /**
   * 打开手动录入需求弹窗并默认选中第一个产品。
   * @returns void
   */
  openCreateModal(): void {
    const defaultProdId = this.products()[0]?.id || 0;
    this.createForm = {
      productId: defaultProdId,
      moduleCode: '',
      issueType: 'feature',
      title: '',
      priority: 'P2',
      status: this.autoStartNewEnabled() ? 'in_progress' : 'submitted',
      description: '',
      acceptanceCriteria: '',
      expectedBehavior: '',
      reproSteps: ''
    };
    if (defaultProdId > 0) {
      this.loadProductModules(defaultProdId);
    }
    this.createModalVisible.set(true);
  }

  /**
   * 加载指定产品下的模块配置列表供录入表单选择。
   * @param productId 产品主键 ID
   * @returns void
   */
  loadProductModules(productId: number): void {
    if (!productId) {
      this.selectedProductModules.set([]);
      return;
    }
    this.api.getProductBundle(productId).subscribe({
      next: (bundle) => {
        this.selectedProductModules.set(bundle?.modules || []);
        if (!this.createForm.moduleCode && bundle?.modules?.length) {
          this.createForm.moduleCode = bundle.modules[0].code;
        }
      },
      error: () => this.selectedProductModules.set([])
    });
  }

  /**
   * 提交手动录入的需求到后端。
   * @returns void
   */
  submitCreateRequirement(): void {
    if (!this.createForm.productId) {
      this.message.warning('请选择所属产品');
      return;
    }
    if (!this.createForm.title.trim()) {
      this.message.warning('请填写需求标题');
      return;
    }
    if (!this.createForm.acceptanceCriteria.trim()) {
      this.message.warning('请填写可验证的验收标准');
      return;
    }

    this.submitting.set(true);
    this.api.createRequirement(this.createForm).subscribe({
      next: () => {
        this.submitting.set(false);
        this.createModalVisible.set(false);
        this.message.success('需求已成功录入需求池');
        this.loadList();
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '录入需求失败');
      }
    });
  }

  /**
   * 打开需求详情宽抽屉并拉取完整对话、原型、关联与时间线数据。
   * @param item 选中的需求项
   * @returns void
   */
  openDetailDrawer(item: RequirementItem): void {
    this.drawerVisible.set(true);
    this.selectedPrototypeIdx.set(0);
    this.showProtoSource.set(false);
    this.refreshDetail(item.id);
  }

  /**
   * 刷新当前打开的需求详情聚合数据（若处于进行中且存在关联开发任务 ID，顺带补齐实时开发状态）。
   * @param id 需求主键 ID
   * @returns void
   */
  refreshDetail(id: number): void {
    this.detailLoading.set(true);
    this.api.getRequirementDetail(id).subscribe({
      next: (bundle) => {
        this.detailLoading.set(false);
        this.detail.set(bundle);
        // 若需求进行中但详情未内联返回 workItem 对象且有 activeWorkItemId，主动拉取最新开发任务状态
        const workId = bundle?.requirement?.activeWorkItemId;
        if (bundle?.requirement?.status === 'in_progress' && workId && !bundle.workItem) {
          this.api.getWorkItemDetail(workId).subscribe({
            next: (wb) => {
              const cur = this.detail();
              if (cur && cur.requirement.id === id) {
                this.detail.set({ ...cur, workItem: wb.workItem });
              }
            },
            error: () => {}
          });
        }
      },
      error: (err: Error) => {
        this.detailLoading.set(false);
        this.message.error(err.message || '读取需求详情失败');
      }
    });
  }

  /**
   * 打开手动修改需求状态弹窗（支持修改为任意合法业务状态并按需联动启动开发任务）。
   * @param item 目标需求记录
   * @returns void
   */
  openStatusModal(item: RequirementItem): void {
    this.activeRequirementId = item.id;
    const hasWork = Boolean(item.activeWorkItemId && item.activeWorkItemId > 0);
    this.statusTargetAlreadyHasWork.set(hasWork);
    const prod = this.products().find((p) => p.id === item.productId);
    const curStatus = item.status || 'submitted';
    this.statusForm = {
      status: curStatus,
      triggerStartWork: !hasWork && curStatus === 'in_progress',
      planConfirmRequired: prod ? prod.planConfirmDefault : this.defaultPlanConfirmEnabled(),
      agyModel: this.defaultAgyModel(),
      agyEffort: this.defaultAgyEffort(),
      closeReason: (item.closeReason as any) || 'deferred',
      note: ''
    };
    this.statusModalVisible.set(true);
  }

  /**
   * 打开“确认要做”快捷弹窗：默认将需求状态切为研发进行中（in_progress）并勾选立即启动 Antigravity 开发任务。
   * @param item 待确认的新需求记录
   * @returns void
   */
  openConfirmToDoModal(item: RequirementItem): void {
    this.activeRequirementId = item.id;
    const hasWork = Boolean(item.activeWorkItemId && item.activeWorkItemId > 0);
    this.statusTargetAlreadyHasWork.set(hasWork);
    const prod = this.products().find((p) => p.id === item.productId);
    const shouldAutoDev = !hasWork && this.autoStartOnConfirmEnabled();
    this.statusForm = {
      status: shouldAutoDev ? 'in_progress' : 'scheduled',
      triggerStartWork: shouldAutoDev,
      planConfirmRequired: prod ? prod.planConfirmDefault : this.defaultPlanConfirmEnabled(),
      agyModel: this.defaultAgyModel(),
      agyEffort: this.defaultAgyEffort(),
      closeReason: 'deferred',
      note: '人工已确认要做'
    };
    this.statusModalVisible.set(true);
  }

  /**
   * 当用户在状态弹窗切换目标状态时，根据系统配置自动联动“立即启动开发任务”默认勾选状态。
   * @param nextStatus 选中的目标状态编码
   * @returns void
   */
  onStatusTargetChange(nextStatus: string): void {
    if (this.statusTargetAlreadyHasWork()) {
      this.statusForm.triggerStartWork = false;
      return;
    }
    if (nextStatus === 'in_progress') {
      this.statusForm.triggerStartWork = true;
    } else if (nextStatus === 'scheduled') {
      this.statusForm.triggerStartWork = this.autoStartOnConfirmEnabled();
    } else {
      this.statusForm.triggerStartWork = false;
    }
  }

  /**
   * 提交手动状态变更（及可选联动启动研发任务）请求。
   * @returns void
   */
  submitUpdateStatus(): void {
    this.submitting.set(true);
    this.api
      .updateRequirementStatus(this.activeRequirementId, {
        status: this.statusForm.status,
        triggerStartWork: this.statusTargetAlreadyHasWork() ? false : this.statusForm.triggerStartWork,
        planConfirmRequired: this.statusForm.planConfirmRequired,
        agyModel: this.statusForm.agyModel,
        agyEffort: this.statusForm.agyEffort,
        closeReason: this.statusForm.closeReason,
        note: this.statusForm.note
      })
      .subscribe({
        next: (res) => {
          this.submitting.set(false);
          this.statusModalVisible.set(false);
          if (res?.workItemId && res.workItemId > 0) {
            this.message.success(`需求状态已更新并自动启动开发任务 #${res.workItemId}`);
          } else {
            this.message.success('需求状态已更新');
          }
          this.loadList();
          if (this.drawerVisible() && this.detail()?.requirement?.id === this.activeRequirementId) {
            this.refreshDetail(this.activeRequirementId);
          }
        },
        error: (err: Error) => {
          this.submitting.set(false);
          this.message.error(err.message || '更新需求状态失败');
        }
      });
  }

  /**
   * 打开分拣定级弹窗。
   * @param item 目标需求
   * @returns void
   */
  openTriageModal(item: RequirementItem): void {
    this.activeRequirementId = item.id;
    this.triageForm = {
      issueType: item.issueType || 'feature',
      moduleCode: item.moduleCode || '',
      priority: item.priority || 'P2',
      effortSize: item.effortSize || 'M',
      readinessScore: item.readinessScore ?? 85,
      status: 'scheduled'
    };
    this.triageModalVisible.set(true);
  }

  /**
   * 提交分拣定级请求。
   * @returns void
   */
  submitTriage(): void {
    this.submitting.set(true);
    this.api.triageRequirement(this.activeRequirementId, this.triageForm).subscribe({
      next: () => {
        this.submitting.set(false);
        this.triageModalVisible.set(false);
        this.message.success('需求分拣定级已更新');
        this.loadList();
        if (this.drawerVisible() && this.detail()?.requirement?.id === this.activeRequirementId) {
          this.refreshDetail(this.activeRequirementId);
        }
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '分拣失败');
      }
    });
  }

  /**
   * 打开点“开工”确认弹窗，读取所属产品的默认方案确认门开关。
   * @param item 待开工需求
   * @returns void
   */
  openStartWorkModal(item: RequirementItem): void {
    this.activeRequirementId = item.id;
    const prod = this.products().find((p) => p.id === item.productId);
    this.startWorkForm = {
      planConfirmRequired: prod ? prod.planConfirmDefault : true,
      tokenBudget: 400000,
      maxTurns: 60,
      maxDurationMinutes: 90,
      agyModel: this.defaultAgyModel(),
      agyEffort: this.defaultAgyEffort()
    };
    this.startWorkModalVisible.set(true);
  }

  /**
   * 正式提交“开工”指令，创建开发任务。
   * @returns void
   */
  submitStartWork(): void {
    this.submitting.set(true);
    this.api.startWorkForRequirement(this.activeRequirementId, this.startWorkForm).subscribe({
      next: (res) => {
        this.submitting.set(false);
        this.startWorkModalVisible.set(false);
        const newWorkId = res?.['workItemId'] || res?.['id'];
        this.message.success(
          newWorkId ? `已启动开发任务 #${newWorkId}` : '开工指令已下发至开发 Agent'
        );
        this.loadList();
        if (this.drawerVisible() && this.detail()?.requirement?.id === this.activeRequirementId) {
          this.refreshDetail(this.activeRequirementId);
        }
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '启动开发任务失败');
      }
    });
  }

  /**
   * 打开关联相似需求弹窗。
   * @param item 当前需求
   * @returns void
   */
  openLinkModal(item: RequirementItem): void {
    this.activeRequirementId = item.id;
    this.linkForm = { targetRequirementId: 0, linkType: 'similar' };
    this.linkModalVisible.set(true);
  }

  /**
   * 提交相似需求关联关系。
   * @returns void
   */
  submitLinkRequirement(): void {
    if (!this.linkForm.targetRequirementId || this.linkForm.targetRequirementId <= 0) {
      this.message.warning('请输入有效的目标需求 ID');
      return;
    }
    this.submitting.set(true);
    this.api.linkRequirement(this.activeRequirementId, this.linkForm).subscribe({
      next: () => {
        this.submitting.set(false);
        this.linkModalVisible.set(false);
        this.message.success('相似需求关联已建立');
        if (this.drawerVisible()) {
          this.refreshDetail(this.activeRequirementId);
        }
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '建立关联失败');
      }
    });
  }

  /**
   * 打开关闭需求弹窗。
   * @param item 待关闭需求
   * @returns void
   */
  openCloseModal(item: RequirementItem): void {
    this.activeRequirementId = item.id;
    this.closeForm = { reason: 'rejected', note: '' };
    this.closeModalVisible.set(true);
  }

  /**
   * 提交关闭归档请求。
   * @returns void
   */
  submitCloseRequirement(): void {
    this.submitting.set(true);
    this.api.closeRequirement(this.activeRequirementId, this.closeForm).subscribe({
      next: () => {
        this.submitting.set(false);
        this.closeModalVisible.set(false);
        this.message.success('需求已关闭并只读归档');
        this.loadList();
        if (this.drawerVisible()) {
          this.refreshDetail(this.activeRequirementId);
        }
      },
      error: (err: Error) => {
        this.submitting.set(false);
        this.message.error(err.message || '关闭需求失败');
      }
    });
  }

  /**
   * 跳转到开发任务页面查看指定任务详情。
   * @param workItemId 开发任务主键 ID
   * @returns void
   */
  jumpToWorkItem(workItemId: number): void {
    this.drawerVisible.set(false);
    void this.router.navigate(['/work-items'], { queryParams: { workItemId } });
  }
}
