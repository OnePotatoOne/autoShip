import { Injectable, inject, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { Observable, tap, map } from 'rxjs';
import {
  AdminUser,
  ApiEnvelope,
  AuthLoginResult,
  AuthSetupResult,
  AuthStatusData,
  OtpSetupResult
} from '../models/api.models';

/**
 * 管理后台会话鉴权、首次初始化、OTP 双因素激活及 CSRF 令牌管理服务。
 * 通过 Angular DI 注入 `inject(AuthService)` 在登录组件、路由守卫及 HTTP 拦截器中调用。
 */
@Injectable({ providedIn: 'root' })
export class AuthService {
  private readonly http = inject(HttpClient);

  /** 系统是否已完成首次管理员初始化 */
  readonly initialized = signal<boolean>(true);
  /** 当前浏览器会话是否已通过服务端认证 */
  readonly authenticated = signal<boolean>(false);
  /** 当前已登录管理员信息 */
  readonly currentUser = signal<AdminUser | null>(null);
  /** 是否已完成过至少一次服务端状态探测 */
  readonly statusLoaded = signal<boolean>(false);

  private csrfTokenValue = '';

  /**
   * 获取当前会话绑定的 CSRF 令牌（优先读取内存缓存，降级从 `autoship_csrf` Cookie 读取）。
   * @returns 当前有效的 CSRF 令牌字符串；无令牌时返回空字符串
   */
  getCsrfToken(): string {
    if (this.csrfTokenValue) {
      return this.csrfTokenValue;
    }
    const fromCookie = this.readCookieBySuffix('csrf');
    if (fromCookie) {
      this.csrfTokenValue = fromCookie;
    }
    return this.csrfTokenValue;
  }

  /**
   * 更新内存中的 CSRF 令牌缓存。
   * @param token 服务端响应下发的最新 CSRF 令牌
   * @returns void
   */
  setCsrfToken(token?: string): void {
    if (token) {
      this.csrfTokenValue = token;
    }
  }

  /**
   * 清空客户端本地认证状态（在 401 过期或主动退出登录时调用）。
   * @returns void
   */
  clearSessionState(): void {
    this.authenticated.set(false);
    this.currentUser.set(null);
    this.csrfTokenValue = '';
  }

  /**
   * 请求 `/api/v1/admin/auth/status` 获取系统初始化状态与当前会话有效性。
   * @returns 返回包含 `AuthStatusData` 的 Observable 流；供路由守卫与页面初始化调用
   */
  loadStatus(): Observable<AuthStatusData> {
    return this.http.get<ApiEnvelope<AuthStatusData>>('/api/v1/admin/auth/status').pipe(
      map((env) => {
        if (env.code !== 0) {
          throw new Error(env.message || '查询登录状态失败');
        }
        return env.data;
      }),
      tap((status) => {
        this.initialized.set(Boolean(status.initialized));
        this.authenticated.set(Boolean(status.authenticated));
        this.currentUser.set(status.user ?? null);
        if (status.csrfToken) {
          this.csrfTokenValue = status.csrfToken;
        }
        this.statusLoaded.set(true);
      })
    );
  }

  /**
   * 在首次启动且数据库尚未连通时，调用 `/api/v1/admin/auth/db-setup` 连接并自动建库迁移 MySQL 8。
   * @param payload 包含主机、端口、用户名、密码与数据库名的连接配置
   * @returns 返回连接与初始化状态结果的 Observable 流
   */
  setupDatabase(payload: {
    host: string;
    port: number;
    user: string;
    password: string;
    dbName: string;
  }): Observable<{ dbReady: boolean; initialized: boolean }> {
    return this.http
      .post<ApiEnvelope<{ dbReady: boolean; initialized: boolean }>>('/api/v1/admin/auth/db-setup', payload)
      .pipe(
        map((env) => {
          if (env.code !== 0) {
            throw new Error(env.message || '连接数据库失败');
          }
          return env.data;
        })
      );
  }

  /**
   * 在平台首次启动尚未创建管理员时调用 `/api/v1/admin/auth/setup` 初始化首个管理员账号。
   * @param username 初始管理员登录名
   * @param password 初始管理员登录密码（不限制长度或复杂度，非空即可）
   * @returns 返回一次性 OTP 绑定密钥、URI、恢复码与会话信息的 Observable 流
   */
  setup(username: string, password: string): Observable<AuthSetupResult> {
    return this.http
      .post<ApiEnvelope<AuthSetupResult>>('/api/v1/admin/auth/setup', { username, password })
      .pipe(
        map((env) => {
          if (env.code !== 0) {
            throw new Error(env.message || '初始化管理员失败');
          }
          return env.data;
        }),
        tap((res) => {
          this.initialized.set(true);
          this.authenticated.set(true);
          this.currentUser.set(res.user);
          this.setCsrfToken(res.csrfToken);
          this.statusLoaded.set(true);
        })
      );
  }

  /**
   * 调用 `/api/v1/admin/auth/login` 执行账号密码 + OTP 动态口令或一次性恢复码登录。
   * @param payload 包含用户名、密码及可选 otpCode / recoveryCode 的登录参数
   * @returns 返回包含用户信息与 `otpSetupRequired` 引导标识的 Observable 流
   */
  login(payload: {
    username: string;
    password: string;
    otpCode?: string;
    recoveryCode?: string;
  }): Observable<AuthLoginResult> {
    return this.http.post<ApiEnvelope<AuthLoginResult>>('/api/v1/admin/auth/login', payload).pipe(
      map((env) => {
        if (env.code !== 0) {
          throw new Error(env.message || '登录验证未通过');
        }
        return env.data;
      }),
      tap((res) => {
        this.initialized.set(true);
        this.authenticated.set(true);
        this.currentUser.set(res.user);
        this.setCsrfToken(res.csrfToken);
        this.statusLoaded.set(true);
      })
    );
  }

  /**
   * 为已登录但尚未启用双因素认证的管理员请求 `/api/v1/admin/auth/otp/setup` 生成新 OTP 密钥与恢复码。
   * @returns 返回包含 `secret`、`uri`、`recoveryCodes` 的 Observable 流
   */
  setupOtp(): Observable<OtpSetupResult> {
    return this.http.post<ApiEnvelope<OtpSetupResult>>('/api/v1/admin/auth/otp/setup', {}).pipe(
      map((env) => {
        if (env.code !== 0) {
          throw new Error(env.message || '生成 OTP 密钥失败');
        }
        return env.data;
      })
    );
  }

  /**
   * 提交 6 位动态验证码到 `/api/v1/admin/auth/otp/enable` 正式开启强制双因素认证。
   * @param code 身份验证器生成的 6 位数字动态口令
   * @returns 返回激活结果的 Observable 流
   */
  enableOtp(code: string): Observable<{ enabled: boolean }> {
    return this.http
      .post<ApiEnvelope<{ enabled: boolean }>>('/api/v1/admin/auth/otp/enable', { code })
      .pipe(
        map((env) => {
          if (env.code !== 0) {
            throw new Error(env.message || 'OTP 动态口令校验失败');
          }
          return env.data;
        }),
        tap((res) => {
          if (res.enabled && this.currentUser()) {
            this.currentUser.set({ ...this.currentUser()!, otpEnabled: true });
          }
        })
      );
  }

  /**
   * 调用 `/api/v1/admin/auth/logout` 注销当前会话并清理前端缓存状态。
   * @returns 返回注销结果的 Observable 流
   */
  logout(): Observable<Record<string, unknown>> {
    return this.http
      .post<ApiEnvelope<Record<string, unknown>>>('/api/v1/admin/auth/logout', {})
      .pipe(
        map((env) => env.data ?? {}),
        tap(() => {
          this.clearSessionState();
        })
      );
  }

  /**
   * 从浏览器 `document.cookie` 中按指定名称或后缀读取 Cookie 值（如 `autoship_csrf`）。
   * @param suffix 目标 Cookie 后缀名称
   * @returns 匹配到的解码后 Cookie 字符串；未找到时返回空字符串
   */
  private readCookieBySuffix(suffix: string): string {
    if (typeof document === 'undefined' || !document.cookie) {
      return '';
    }
    const pairs = document.cookie.split(';');
    for (const pair of pairs) {
      const idx = pair.indexOf('=');
      if (idx <= 0) {
        continue;
      }
      const key = pair.slice(0, idx).trim();
      const val = pair.slice(idx + 1).trim();
      if (key === `autoship_${suffix}` || key.endsWith(`_${suffix}`)) {
        try {
          return decodeURIComponent(val);
        } catch {
          return val;
        }
      }
    }
    return '';
  }
}
