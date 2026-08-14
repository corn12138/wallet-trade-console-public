import { buildApiUrl } from './base-url';

const WEB3_AUTH_TOKEN_KEY = 'web3_auth_token';

export function buildWeb3AuthHeaders(headers?: HeadersInit) {
  const nextHeaders = new Headers(headers);

  if (typeof window === 'undefined') {
    return nextHeaders;
  }

  const token = window.localStorage.getItem(WEB3_AUTH_TOKEN_KEY);
  if (token && !nextHeaders.has('Authorization')) {
    nextHeaders.set('Authorization', `Bearer ${token}`);
  }

  return nextHeaders;
}

export async function fetchApi(pathname: string, init: RequestInit = {}) {
  return fetch(buildApiUrl(pathname), {
    ...init,
    headers: buildWeb3AuthHeaders(init.headers),
  });
}
