import { HttpErrorResponse, HttpInterceptorFn } from '@angular/common/http';
import { inject } from '@angular/core';
import { Router } from '@angular/router';
import { catchError, throwError } from 'rxjs';
import { AuthService } from '../services/auth.service';

/**
 * 全局管理后台 HTTP 安全拦截器。
 * 作用：
 * 1. 为所有请求开启 `withCredentials: true`，携带专网 HttpOnly 会话 Cookie；
 * 2. 为所有状态变更写请求（POST / PUT / DELETE / PATCH）自动附加 `X-AutoShip-CSRF` 请求头；
 * 3. 当受保护业务接口返回 401 未登录时，自动清理本地会话并平滑跳转至 `/login`。
 * @param req 原始 HTTP 请求对象
 * @param next 下游拦截器处理链
 * @returns 附加安全凭据与异常处理的 HTTP 事件 Observable 流
 */
export const authCsrfInterceptor: HttpInterceptorFn = (req, next) => {
  const authService = inject(AuthService);
  const router = inject(Router);

  const method = req.method.toUpperCase();
  const isWriteMethod = method === 'POST' || method === 'PUT' || method === 'DELETE' || method === 'PATCH';
  const csrfToken = authService.getCsrfToken();

  let headers = req.headers;
  if (isWriteMethod && csrfToken) {
    headers = headers.set('X-AutoShip-CSRF', csrfToken);
  }

  const clonedReq = req.clone({
    withCredentials: true,
    headers
  });

  return next(clonedReq).pipe(
    catchError((err: unknown) => {
      if (err instanceof HttpErrorResponse) {
        const isAuthEndpoint =
          req.url.includes('/api/v1/admin/auth/login') ||
          req.url.includes('/api/v1/admin/auth/status') ||
          req.url.includes('/api/v1/admin/auth/setup');
        if (err.status === 401 && !isAuthEndpoint) {
          authService.clearSessionState();
          void router.navigate(['/login']);
        }
      }
      return throwError(() => err);
    })
  );
};
