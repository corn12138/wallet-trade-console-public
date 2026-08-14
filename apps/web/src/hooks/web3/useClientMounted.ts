'use client';

/**
 * @file useClientMounted Hook
 * @description 解决 SSR 水合问题
 *
 * 问题：
 * wagmi 在服务端和客户端的状态可能不一致，导致水合错误
 *
 * 解决：
 * 在客户端挂载前显示骨架屏或空内容
 *
 * 使用示例：
 * const mounted = useClientMounted();
 * if (!mounted) return <Skeleton />;
 * return <WalletInfo />;
 */

import { useEffect, useState } from 'react';

/**
 * 检测客户端是否已挂载
 * @returns 是否已在客户端挂载
 */
export function useClientMounted(): boolean {
  const [mounted, setMounted] = useState(false);

  useEffect(() => {
    setMounted(true);
  }, []);

  return mounted;
}
