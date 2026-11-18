import { CommonModule } from '@angular/common';
import { HttpErrorResponse } from '@angular/common/http';
import { Component, OnInit, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { Router } from '@angular/router';
import { NzAlertModule } from 'ng-zorro-antd/alert';
import { NzButtonModule } from 'ng-zorro-antd/button';
import { NzDividerModule } from 'ng-zorro-antd/divider';
import { NzFormModule } from 'ng-zorro-antd/form';
import { NzIconModule } from 'ng-zorro-antd/icon';
import { NzInputModule } from 'ng-zorro-antd/input';
import { NzMessageService } from 'ng-zorro-antd/message';
import { NzQRCodeModule } from 'ng-zorro-antd/qr-code';
import { NzSpinModule } from 'ng-zorro-antd/spin';
import { NzTagModule } from 'ng-zorro-antd/tag';
import { AuthService } from '../../core/services/auth.service';
import { copyTextToClipboard } from '../../core/utils/format.utils';

/**
 * 登录与首次初始化向导页面视图模式。
 */
type AuthViewMode = 'checking' | 'db-setup' | 'setup' | 'otp-bind' | 'login';

/**
 * autoShip 管理后台安全登录、首次管理员初始化与强制 TOTP 双因素认证绑定组件。
 * 对应设计文档第 10 节：Tailnet 专网访问 + 密码 + OTP + 一次性恢复码 + CSRF 会话保护。
 */
@Component({
  selector: 'app-login',
  standalone: true,
  imports: [
    CommonModule,
    FormsModule,
    NzFormModule,
    NzInputModule,
    NzButtonModule,
    NzAlertModule,
    NzIconModule,
    NzQRCodeModule,
    NzSpinModule,
    NzTagModule,
    NzDividerModule
  ],
  template: `
    <div class="login-backdrop">
      <div class="login-container">
        <div class="login-brand">
          <div class="brand-logo">
            <span nz-icon nzType="rocket" nzTheme="outline"></span>
          </div>
          <div>
            <h1 class="brand-title">autoShip 研发持续交付中枢</h1>
            <p class="brand-desc">多产品需求澄清 · 自研编码循环 · 签名制品库 · 安全受控部署</p>
          </div>
        </div>

        <nz-spin [nzSpinning]="mode() === 'checking'" nzTip="正在检查平台安全认证状态...">
          @if (errorMsg()) {
            <nz-alert
              nzType="error"
              nzShowIcon
              [nzMessage]="errorMsg()"
              style="margin-bottom: 16px;"
            ></nz-alert>
          }

          <!-- 模式 0：首次启动连接并初始化 MySQL 8 数据库 -->
          @if (mode() === 'db-setup') {
            <div class="auth-section">
              <div class="section-head">
                <div style="display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 8px;">
                  <nz-tag nzColor="purple">首次部署向导 · 数据库连接</nz-tag>
                  <a href="/api/v1/admin/auth/ca.crt" target="_blank" style="font-size: 12.5px;">
                    <span nz-icon nzType="safety-certificate"></span> 下载 autoShip 根 CA 证书
                  </a>
                </div>
                <h2>连接并初始化 MySQL 8 数据库</h2>
                <p>填写 MySQL 8 连接信息后，autoShip 将自动创建目标数据库（utf8mb4）、完成全部 33 张业务表与注释迁移，并以 AES-256-GCM 加密保存口令。</p>
                @if (caFingerprint()) {
                  <div class="as-mono" style="margin-top: 8px; padding: 8px 10px; background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 6px; font-size: 11.5px; word-break: break-all;">
                    <b>根 CA 固定指纹 (SHA-256)：</b>{{ caFingerprint() }}
                  </div>
                }
              </div>

              <form nz-form nzLayout="vertical" (ngSubmit)="submitDatabaseSetup()">
                <div style="display: grid; grid-template-columns: 2fr 1fr; gap: 12px;">
                  <nz-form-item>
                    <nz-form-label nzRequired>MySQL 主机地址</nz-form-label>
                    <nz-form-control>
                      <input nz-input name="dbHost" [(ngModel)]="dbForm.host" placeholder="127.0.0.1" />
                    </nz-form-control>
                  </nz-form-item>
                  <nz-form-item>
                    <nz-form-label nzRequired>端口</nz-form-label>
                    <nz-form-control>
                      <input nz-input type="number" name="dbPort" [(ngModel)]="dbForm.port" placeholder="3306" />
                    </nz-form-control>
                  </nz-form-item>
                </div>

                <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px;">
                  <nz-form-item>
                    <nz-form-label nzRequired>数据库用户名</nz-form-label>
                    <nz-form-control>
                      <input nz-input name="dbUser" [(ngModel)]="dbForm.user" placeholder="autoship 或 root" />
                    </nz-form-control>
                  </nz-form-item>
                  <nz-form-item>
                    <nz-form-label nzRequired>数据库名称</nz-form-label>
                    <nz-form-control>
                      <input nz-input name="dbName" [(ngModel)]="dbForm.dbName" placeholder="autoship" />
                    </nz-form-control>
                  </nz-form-item>
                </div>

                <nz-form-item>
                  <nz-form-label>数据库密码</nz-form-label>
                  <nz-form-control>
                    <input
                      nz-input
                      type="password"
                      name="dbPassword"
                      [(ngModel)]="dbForm.password"
                      placeholder="请输入 MySQL 账号密码（服务端加密落盘）"
                    />
                  </nz-form-control>
                </nz-form-item>

                <button
                  nz-button
                  nzType="primary"
                  nzBlock
                  nzSize="large"
                  [nzLoading]="submitting()"
                  type="submit"
                >
                  测试连接并自动建表迁移
                </button>
              </form>
            </div>
          }

          <!-- 模式 1：首次启动系统初始化 -->
          @if (mode() === 'setup') {
            <div class="auth-section">
              <div class="section-head">
                <nz-tag nzColor="blue">首次启动向导 · 步骤 1/2</nz-tag>
                <h2>创建平台超级管理员</h2>
                <p>系统检测到尚未创建任何管理员账号。请设置初始管理员凭据，随后将引导绑定强制双因素口令（OTP）。</p>
              </div>

              <form nz-form nzLayout="vertical" (ngSubmit)="submitInitialSetup()">
                <nz-form-item>
                  <nz-form-label nzRequired>管理员用户名</nz-form-label>
                  <nz-form-control>
                    <nz-input-group nzPrefixIcon="user">
                      <input
                        nz-input
                        name="setupUsername"
                        [(ngModel)]="setupUsername"
                        placeholder="请输入管理员用户名（如 admin）"
                        autocomplete="username"
                      />
                    </nz-input-group>
                  </nz-form-control>
                </nz-form-item>

                <nz-form-item>
                  <nz-form-label nzRequired>登录密码</nz-form-label>
                  <nz-form-control>
                    <nz-input-group nzPrefixIcon="lock">
                      <input
                        nz-input
                        type="password"
                        name="setupPassword"
                        [(ngModel)]="setupPassword"
                        placeholder="请输入登录密码"
                        autocomplete="new-password"
                      />
                    </nz-input-group>
                  </nz-form-control>
                </nz-form-item>

                <nz-form-item>
                  <nz-form-label nzRequired>确认登录密码</nz-form-label>
                  <nz-form-control>
                    <nz-input-group nzPrefixIcon="safety">
                      <input
                        nz-input
                        type="password"
                        name="setupConfirmPassword"
                        [(ngModel)]="setupConfirmPassword"
                        placeholder="请再次输入密码确认一致"
                        autocomplete="new-password"
                      />
                    </nz-input-group>
                  </nz-form-control>
                </nz-form-item>

                <button
                  nz-button
                  nzType="primary"
                  nzBlock
                  nzSize="large"
                  [nzLoading]="submitting()"
                  type="submit"
                >
                  创建管理员并生成 OTP 密钥
                </button>
              </form>
            </div>
          }

          <!-- 模式 2：绑定并激活 TOTP 双因素认证 -->
          @if (mode() === 'otp-bind') {
            <div class="auth-section">
              <div class="section-head">
                <nz-tag nzColor="gold">双因素安全绑定</nz-tag>
                <h2>绑定动态口令验证器（TOTP）并保存恢复码</h2>
                <p>请使用 1Password、Google Authenticator 或系统密码 App 扫描下方二维码，并输入 6 位动态口令激活。</p>
              </div>

              @if (otpUri()) {
                <div class="otp-box">
                  <div class="qr-wrap">
                    <nz-qrcode [nzValue]="otpUri()" nzType="svg" [nzSize]="176"></nz-qrcode>
                  </div>
                  <div class="otp-secret-info">
                    <div class="secret-label">手动录入 Base32 密钥：</div>
                    <div class="secret-code as-mono">{{ otpSecret() }}</div>
                    <div style="margin-top: 8px; display: flex; gap: 8px; flex-wrap: wrap;">
                      <button nz-button nzSize="small" type="button" (click)="copyValue(otpSecret(), 'OTP 密钥')">
                        <span nz-icon nzType="copy"></span>复制密钥
                      </button>
                      <button nz-button nzSize="small" type="button" (click)="copyValue(otpUri(), 'OTP URI')">
                        <span nz-icon nzType="link"></span>复制 otpauth 链接
                      </button>
                    </div>
                  </div>
                </div>
              } @else {
                <div style="margin-bottom: 14px;">
                  <button
                    nz-button
                    nzType="dashed"
                    nzBlock
                    type="button"
                    [nzLoading]="submitting()"
                    (click)="regenerateOtpSecret()"
                  >
                    <span nz-icon nzType="key"></span>点击获取 OTP 绑定二维码与恢复码
                  </button>
                </div>
              }

              @if (recoveryCodes().length > 0) {
                <nz-alert
                  nzType="warning"
                  nzShowIcon
                  nzMessage="一次性应急恢复码（仅展示本次，入库后不可逆加密，请立即离线妥善保存！）"
                  style="margin: 14px 0 10px;"
                ></nz-alert>
                <div class="recovery-grid as-mono">
                  @for (c of recoveryCodes(); track c) {
                    <span class="recovery-chip">{{ c }}</span>
                  }
                </div>
                <div style="margin-bottom: 14px; text-align: right;">
                  <button
                    nz-button
                    nzSize="small"
                    type="button"
                    (click)="copyValue(recoveryCodes().join('\n'), '全部应急恢复码')"
                  >
                    <span nz-icon nzType="copy"></span>一键复制全部恢复码
                  </button>
                </div>
              }

              <form nz-form nzLayout="vertical" (ngSubmit)="submitEnableOtp()">
                <nz-form-item>
                  <nz-form-label nzRequired>输入验证器显示的 6 位动态口令完成激活</nz-form-label>
                  <nz-form-control>
                    <nz-input-group nzPrefixIcon="safety-certificate">
                      <input
                        nz-input
                        name="otpEnableCode"
                        [(ngModel)]="otpEnableCode"
                        maxlength="6"
                        placeholder="请输入 6 位数字验证码"
                        autocomplete="one-time-code"
                      />
                    </nz-input-group>
                  </nz-form-control>
                </nz-form-item>

                <button
                  nz-button
                  nzType="primary"
                  nzBlock
                  nzSize="large"
                  [nzLoading]="submitting()"
                  type="submit"
                >
                  激活双因素认证并进入管理后台
                </button>
              </form>
            </div>
          }

          <!-- 模式 3：常规管理员双因素登录 -->
          @if (mode() === 'login') {
            <div class="auth-section">
              <div class="section-head">
                <div style="display: flex; align-items: center; justify-content: space-between;">
                  <nz-tag nzColor="geekblue">Tailnet 专网管理端</nz-tag>
                  <span style="font-size: 12px; color: #64748b;">HTTPS + HttpOnly + CSRF 保护</span>
                </div>
                <h2>管理员安全登录</h2>
              </div>

              <form nz-form nzLayout="vertical" (ngSubmit)="submitLogin()">
                <nz-form-item>
                  <nz-form-label nzRequired>管理员账号</nz-form-label>
                  <nz-form-control>
                    <nz-input-group nzPrefixIcon="user">
                      <input
                        nz-input
                        name="loginUsername"
                        [(ngModel)]="loginUsername"
                        placeholder="请输入管理员用户名"
                        autocomplete="username"
                      />
                    </nz-input-group>
                  </nz-form-control>
                </nz-form-item>

                <nz-form-item>
                  <nz-form-label nzRequired>登录密码</nz-form-label>
                  <nz-form-control>
                    <nz-input-group nzPrefixIcon="lock">
                      <input
                        nz-input
                        type="password"
                        name="loginPassword"
                        [(ngModel)]="loginPassword"
                        placeholder="请输入密码"
                        autocomplete="current-password"
                      />
                    </nz-input-group>
                  </nz-form-control>
                </nz-form-item>

                @if (!useRecoveryCode()) {
                  <nz-form-item>
                    <nz-form-label>
                      <span>6 位 OTP 动态口令</span>
                    </nz-form-label>
                    <nz-form-control>
                      <nz-input-group nzPrefixIcon="safety-certificate">
                        <input
                          nz-input
                          name="loginOtpCode"
                          [(ngModel)]="loginOtpCode"
                          maxlength="6"
                          placeholder="输入验证器 6 位数字口令（未完成首次绑定可留空）"
                          autocomplete="one-time-code"
                        />
                      </nz-input-group>
                    </nz-form-control>
                  </nz-form-item>
                } @else {
                  <nz-form-item>
                    <nz-form-label nzRequired>一次性应急恢复码</nz-form-label>
                    <nz-form-control>
                      <nz-input-group nzPrefixIcon="key">
                        <input
                          nz-input
                          name="loginRecoveryCode"
                          [(ngModel)]="loginRecoveryCode"
                          placeholder="请输入保存的应急恢复码（使用后自动核销作废）"
                        />
                      </nz-input-group>
                    </nz-form-control>
                  </nz-form-item>
                }

                <div class="switch-row">
                  <a (click)="toggleRecoveryMode()">
                    {{ useRecoveryCode() ? '返回使用 6 位 OTP 动态口令登录' : '手机不在身边？使用一次性应急恢复码' }}
                  </a>
                </div>

                <button
                  nz-button
                  nzType="primary"
                  nzBlock
                  nzSize="large"
                  [nzLoading]="submitting()"
                  type="submit"
                >
                  验证身份并进入工作台
                </button>
              </form>
            </div>
          }
        </nz-spin>

        <div class="login-footer">
          <span>autoShip v0.3 · 仅在 Tailnet 专网监听 · 全程 TLS 与操作审计已开启</span>
        </div>
      </div>
    </div>
  `,
  styles: [
    `
      .login-backdrop {
        min-height: 100vh;
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 28px 16px;
        background: radial-gradient(circle at 18% 20%, #15315b 0%, #091426 62%, #050b16 100%);
      }
      .login-container {
        width: 100%;
        max-width: 500px;
        background: #ffffff;
        border-radius: 14px;
        box-shadow: 0 20px 50px rgba(0, 0, 0, 0.38);
        padding: 28px 32px 22px;
      }
      .login-brand {
        display: flex;
        align-items: center;
        gap: 14px;
        padding-bottom: 18px;
        margin-bottom: 18px;
        border-bottom: 1px solid #eef2f7;
      }
      .brand-logo {
        width: 46px;
        height: 46px;
        border-radius: 12px;
        background: linear-gradient(135deg, #0f2b5b, #1677ff);
        color: #ffffff;
        display: flex;
        align-items: center;
        justify-content: center;
        font-size: 24px;
        flex-shrink: 0;
      }
      .brand-title {
        margin: 0;
        font-size: 19px;
        font-weight: 700;
        color: #0f172a;
      }
      .brand-desc {
        margin: 3px 0 0;
        font-size: 12px;
        color: #64748b;
      }
      .section-head {
        margin-bottom: 18px;
      }
      .section-head h2 {
        margin: 8px 0 4px;
        font-size: 17px;
        font-weight: 600;
        color: #0f172a;
      }
      .section-head p {
        margin: 0;
        font-size: 13px;
        color: #64748b;
        line-height: 1.55;
      }
      .otp-box {
        display: flex;
        gap: 16px;
        align-items: center;
        background: #f8fafc;
        border: 1px solid #e2e8f0;
        border-radius: 10px;
        padding: 14px;
        margin-bottom: 12px;
        flex-wrap: wrap;
      }
      .qr-wrap {
        background: #ffffff;
        padding: 6px;
        border-radius: 8px;
        border: 1px solid #e2e8f0;
      }
      .otp-secret-info {
        flex: 1;
        min-width: 180px;
      }
      .secret-label {
        font-size: 12px;
        color: #64748b;
        margin-bottom: 4px;
      }
      .secret-code {
        background: #0f172a;
        color: #f8fafc;
        padding: 6px 10px;
        border-radius: 6px;
        word-break: break-all;
        font-size: 13px;
      }
      .recovery-grid {
        display: grid;
        grid-template-columns: repeat(2, minmax(0, 1fr));
        gap: 6px;
        background: #fffbeb;
        border: 1px solid #fde68a;
        border-radius: 8px;
        padding: 10px 12px;
        margin-bottom: 8px;
      }
      .recovery-chip {
        font-size: 12.5px;
        color: #92400e;
        padding: 2px 6px;
      }
      .switch-row {
        margin: -4px 0 16px;
        text-align: right;
        font-size: 13px;
      }
      .login-footer {
        margin-top: 20px;
        padding-top: 14px;
        border-top: 1px solid #f1f5f9;
        text-align: center;
        font-size: 12px;
        color: #94a3b8;
      }
    `
  ]
})
export class LoginComponent implements OnInit {
  private readonly auth = inject(AuthService);
  private readonly router = inject(Router);
  private readonly message = inject(NzMessageService);

  /** 当前认证卡片展示状态 */
  readonly mode = signal<AuthViewMode>('checking');
  /** 提交加载态 */
  readonly submitting = signal<boolean>(false);
  /** 顶部错误提示信息 */
  readonly errorMsg = signal<string>('');
  /** 是否切换为使用恢复码登录 */
  readonly useRecoveryCode = signal<boolean>(false);

  /** OTP 绑定密钥与恢复码状态 */
  readonly otpSecret = signal<string>('');
  readonly otpUri = signal<string>('');
  readonly recoveryCodes = signal<string[]>([]);
  readonly caFingerprint = signal<string>('');

  // 首次启动数据库连接表单
  dbForm = {
    host: '127.0.0.1',
    port: 3306,
    user: 'root',
    password: '',
    dbName: 'autoship'
  };

  // 首次初始化表单数据
  setupUsername = 'admin';
  setupPassword = '';
  setupConfirmPassword = '';

  // 登录表单数据
  loginUsername = '';
  loginPassword = '';
  loginOtpCode = '';
  loginRecoveryCode = '';

  // OTP 绑定验证码输入
  otpEnableCode = '';

  /**
   * 组件初始化时探测 `/api/v1/admin/auth/status`，自动路由或切换至首次初始化/OTP 绑定/常规登录界面。
   * @returns void
   */
  ngOnInit(): void {
    this.auth.loadStatus().subscribe({
      next: (status) => {
        if (status.caFingerprint) {
          this.caFingerprint.set(status.caFingerprint);
        }
        if (status.dbReady === false) {
          if (status.dbConfig) {
            this.dbForm = {
              host: status.dbConfig.host || '127.0.0.1',
              port: status.dbConfig.port || 3306,
              user: status.dbConfig.user || 'root',
              password: '',
              dbName: status.dbConfig.dbName || 'autoship'
            };
          }
          this.mode.set('db-setup');
          return;
        }
        if (!status.initialized) {
          this.mode.set('setup');
          return;
        }
        if (status.authenticated && status.user) {
          if (status.user.otpEnabled) {
            void this.router.navigate(['/dashboard']);
            return;
          }
          // 已登录但尚未激活 OTP：自动拉取 OTP 绑定二维码进入激活步骤
          this.mode.set('otp-bind');
          this.regenerateOtpSecret();
          return;
        }
        this.mode.set('login');
      },
      error: (err: unknown) => {
        this.mode.set('login');
        this.errorMsg.set(this.extractErrMsg(err, '无法连接服务端认证状态接口'));
      }
    });
  }

  /**
   * 切换 OTP 6位动态口令或一次性恢复码输入方式。
   * @returns void
   */
  toggleRecoveryMode(): void {
    this.useRecoveryCode.set(!this.useRecoveryCode());
    this.loginOtpCode = '';
    this.loginRecoveryCode = '';
    this.errorMsg.set('');
  }

  /**
   * 提交首次部署时的 MySQL 8 连接参数，连通迁移后进入创建管理员向导或登录界面。
   * @returns void
   */
  submitDatabaseSetup(): void {
    this.errorMsg.set('');
    if (!this.dbForm.host.trim() || !this.dbForm.user.trim() || !this.dbForm.dbName.trim()) {
      this.errorMsg.set('请完整填写 MySQL 主机、用户名与数据库名');
      return;
    }
    this.submitting.set(true);
    this.auth
      .setupDatabase({
        host: this.dbForm.host.trim(),
        port: Number(this.dbForm.port) || 3306,
        user: this.dbForm.user.trim(),
        password: this.dbForm.password,
        dbName: this.dbForm.dbName.trim()
      })
      .subscribe({
        next: (res) => {
          this.submitting.set(false);
          this.message.success('MySQL 8 连接与 33 张数据表自动迁移成功');
          this.mode.set(res.initialized ? 'login' : 'setup');
        },
        error: (err: unknown) => {
          this.submitting.set(false);
          this.errorMsg.set(this.extractErrMsg(err, '连接或迁移 MySQL 8 失败'));
        }
      });
  }

  /**
   * 提交首次管理员初始化表单，成功后进入第 2 步绑定 OTP 与保存恢复码。
   * @returns void
   */
  submitInitialSetup(): void {
    this.errorMsg.set('');
    const username = this.setupUsername.trim();
    if (!username) {
      this.errorMsg.set('管理员用户名不能为空');
      return;
    }
    if (!this.setupPassword) {
      this.errorMsg.set('登录密码不能为空');
      return;
    }
    if (this.setupPassword !== this.setupConfirmPassword) {
      this.errorMsg.set('两次输入的登录密码不一致');
      return;
    }

    this.submitting.set(true);
    this.auth.setup(username, this.setupPassword).subscribe({
      next: (res) => {
        this.submitting.set(false);
        this.otpSecret.set(res.otpSecret || '');
        this.otpUri.set(res.otpUri || '');
        this.recoveryCodes.set(res.recoveryCodes || []);
        this.mode.set('otp-bind');
        this.message.success('初始管理员创建成功，请扫码绑定 OTP 并保存恢复码');
      },
      error: (err: unknown) => {
        this.submitting.set(false);
        this.errorMsg.set(this.extractErrMsg(err, '首次初始化失败'));
      }
    });
  }

  /**
   * 为尚未激活 OTP 的当前已登录管理员请求生成 OTP 绑定密钥与恢复码。
   * @returns void
   */
  regenerateOtpSecret(): void {
    this.errorMsg.set('');
    this.submitting.set(true);
    this.auth.setupOtp().subscribe({
      next: (res) => {
        this.submitting.set(false);
        this.otpSecret.set(res.secret || '');
        this.otpUri.set(res.uri || '');
        this.recoveryCodes.set(res.recoveryCodes || []);
      },
      error: (err: unknown) => {
        this.submitting.set(false);
        this.errorMsg.set(this.extractErrMsg(err, '生成 OTP 绑定密钥失败'));
      }
    });
  }

  /**
   * 校验 6 位动态口令并正式开启强制双因素认证，随后进入总览页。
   * @returns void
   */
  submitEnableOtp(): void {
    this.errorMsg.set('');
    const code = this.otpEnableCode.trim();
    if (!/^\d{6}$/.test(code)) {
      this.errorMsg.set('请输入验证器显示的 6 位纯数字动态口令');
      return;
    }
    this.submitting.set(true);
    this.auth.enableOtp(code).subscribe({
      next: () => {
        this.submitting.set(false);
        this.message.success('双因素认证已成功启用，欢迎进入 autoShip 管理后台');
        void this.router.navigate(['/dashboard']);
      },
      error: (err: unknown) => {
        this.submitting.set(false);
        this.errorMsg.set(this.extractErrMsg(err, '动态口令校验未通过，请确认手机时间同步后重试'));
      }
    });
  }

  /**
   * 提交管理员账号密码 + OTP/恢复码执行登录校验。
   * @returns void
   */
  submitLogin(): void {
    this.errorMsg.set('');
    const username = this.loginUsername.trim();
    if (!username || !this.loginPassword) {
      this.errorMsg.set('请输入管理员账号与登录密码');
      return;
    }

    this.submitting.set(true);
    this.auth
      .login({
        username,
        password: this.loginPassword,
        otpCode: this.useRecoveryCode() ? undefined : this.loginOtpCode.trim() || undefined,
        recoveryCode: this.useRecoveryCode()
          ? this.loginRecoveryCode.trim() || undefined
          : undefined
      })
      .subscribe({
        next: (res) => {
          this.submitting.set(false);
          if (res.otpSetupRequired || !res.user?.otpEnabled) {
            this.mode.set('otp-bind');
            this.regenerateOtpSecret();
            return;
          }
          this.message.success(`欢迎回来，${res.user.username}`);
          void this.router.navigate(['/dashboard']);
        },
        error: (err: unknown) => {
          this.submitting.set(false);
          this.errorMsg.set(this.extractErrMsg(err, '登录验证失败，请检查账号密码或动态验证码'));
        }
      });
  }

  /**
   * 复制密钥或恢复码到剪贴板并给出轻提示。
   * @param text 待复制内容
   * @param label 提示名称
   * @returns Promise<void>
   */
  async copyValue(text: string, label: string): Promise<void> {
    const ok = await copyTextToClipboard(text);
    if (ok) {
      this.message.success(`已复制${label}`);
    } else {
      this.message.warning(`复制失败，请手动选中复制${label}`);
    }
  }

  /**
   * 从异常对象中提取用户可读的错误信息。
   * @param err 捕获的未知错误对象
   * @param fallback 兜底提示文本
   * @returns 错误描述字符串
   */
  private extractErrMsg(err: unknown, fallback: string): string {
    if (err instanceof HttpErrorResponse) {
      if (typeof err.error?.message === 'string' && err.error.message.trim()) {
        return err.error.message;
      }
      return `${fallback} (HTTP ${err.status || '网络错误'})`;
    }
    if (err instanceof Error && err.message) {
      return err.message;
    }
    return fallback;
  }
}
