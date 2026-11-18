import { inject } from '@angular/core';
import { CanActivateFn, Router } from '@angular/router';
import { catchError, map, of } from 'rxjs';
import { AuthService } from '../services/auth.service';

/**
 * 管理后台受保护路由守卫：校验系统是否已初始化且当前会话已认证（同时要求完成首次 OTP 绑定）。
 * @returns 允许激活返回 `true`，未登录或待初始化时返回重定向至 `/login` 的 `UrlTree`
 */
export const adminAuthGuard: CanActivateFn = () => {
  const auth = inject(AuthService);
  const router = inject(Router);

  if (auth.statusLoaded()) {
    const user = auth.currentUser();
    if (auth.initialized() && auth.authenticated() && user && user.otpEnabled) {
      return true;
    }
    return router.createUrlTree(['/login']);
  }

  return auth.loadStatus().pipe(
    map((status) => {
      if (status.initialized && status.authenticated && status.user?.otpEnabled) {
        return true;
      }
      return router.createUrlTree(['/login']);
    }),
    catchError(() => of(router.createUrlTree(['/login'])))
  );
};
