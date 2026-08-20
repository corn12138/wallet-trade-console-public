import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { IntlWrapper } from '@/test/renderWithIntl';
import AdminI18nPage from './AdminI18nPage';

const mockFetchApi = vi.fn();
vi.mock('@/lib/api/auth-fetch', () => ({
  fetchApi: (path: string, init?: RequestInit) => mockFetchApi(path, init),
}));

let mockConnected = true;
vi.mock(import('wagmi'), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    useAccount: () =>
      ({
        address: mockConnected ? '0xE2cd26322A87d2b6D8312DBcB79c38b7a226aD81' : undefined,
        isConnected: mockConnected,
      }) as ReturnType<typeof actual.useAccount>,
  };
});

const openConnect = vi.fn();
vi.mock('@/app/_atlas/AppContext', () => ({
  useApp: () => ({ openConnect }),
}));

// A signed SIWE session by default; access-state tests drive the API mock.
let mockAuthenticated = true;
vi.mock('@/lib/web3', () => ({
  useAuth: () => ({
    isAuthenticated: mockAuthenticated,
    token: mockAuthenticated ? 'test-web3-token' : null,
  }),
}));

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as Response;
}

const LOCALES = {
  locales: [
    { code: 'en', englishName: 'English', nativeName: 'English', enabled: true, isDefault: true },
    { code: 'zh', englishName: 'Chinese (Simplified)', nativeName: '简体中文', enabled: true, isDefault: false },
  ],
};

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <IntlWrapper>
      <QueryClientProvider client={qc}>
        <AdminI18nPage />
      </QueryClientProvider>
    </IntlWrapper>,
  );
}

beforeEach(() => {
  mockConnected = true;
  mockAuthenticated = true;
  mockFetchApi.mockReset();
  openConnect.mockReset();
});

describe('/admin/i18n access states', () => {
  it('prompts to connect when no wallet is connected', () => {
    mockConnected = false;
    renderPage();
    expect(screen.getByTestId('admin-i18n-connect')).toBeInTheDocument();
    expect(mockFetchApi).not.toHaveBeenCalled();
  });

  it('shows the session-required state on 401', async () => {
    mockFetchApi.mockResolvedValue(jsonResponse(401, { statusCode: 401, message: 'Missing or invalid web3 authorization' }));
    renderPage();
    await waitFor(() => expect(screen.getByTestId('admin-i18n-401')).toBeInTheDocument());
    expect(screen.getByText('Session required')).toBeInTheDocument();
  });

  it('shows the not-an-administrator state on 403', async () => {
    mockFetchApi.mockResolvedValue(jsonResponse(403, { statusCode: 403, message: 'wallet is not an i18n administrator' }));
    renderPage();
    await waitFor(() => expect(screen.getByTestId('admin-i18n-403')).toBeInTheDocument());
    expect(screen.getByText(/allowlist/i)).toBeInTheDocument();
  });

  it('shows an API-unavailable state on transport failure', async () => {
    mockFetchApi.mockRejectedValue(new Error('fetch failed'));
    renderPage();
    await waitFor(() => expect(screen.getByTestId('admin-i18n-error')).toBeInTheDocument());
  });
});

describe('/admin/i18n editor', () => {
  function wireHappyPath(overrides: Record<string, Response | ((init?: RequestInit) => Response)> = {}) {
    mockFetchApi.mockImplementation(async (path: string, init?: RequestInit) => {
      for (const [prefix, resp] of Object.entries(overrides)) {
        if (path.startsWith(prefix)) {
          return typeof resp === 'function' ? resp(init) : resp;
        }
      }
      if (path.startsWith('/i18n/admin/locales')) return jsonResponse(200, LOCALES);
      if (path.startsWith('/i18n/admin/namespaces')) {
        return jsonResponse(200, { namespaces: [{ namespace: 'app', keys: 2, missing: 1 }] });
      }
      if (path.startsWith('/i18n/admin/messages?')) {
        return jsonResponse(200, {
          items: [{
            namespace: 'app', key: 'hello', value: '你好 {name}', version: 3,
            updatedAt: '2026-07-10T00:00:00Z', defaultValue: 'Hello {name}',
          }],
          total: 1,
        });
      }
      return jsonResponse(200, { items: [], total: 0 });
    });
  }

  it('renders the console with side-by-side values and missing counts', async () => {
    wireHappyPath();
    renderPage();
    await waitFor(() => expect(screen.getByTestId('admin-i18n-console')).toBeInTheDocument());
    await waitFor(() => expect(screen.getByTestId('admin-i18n-msgs-table')).toBeInTheDocument());
    expect(screen.getByText('app.hello')).toBeInTheDocument();
    expect(screen.getByText('Hello {name}')).toBeInTheDocument();
    expect(screen.getByDisplayValue('你好 {name}')).toBeInTheDocument();
    expect(screen.getByTestId('admin-i18n-missing-total').textContent).toMatch(/1/);
  });

  it('surfaces a 409 draft conflict with a reload affordance', async () => {
    wireHappyPath({
      '/i18n/admin/messages/zh/app/hello': jsonResponse(409, { statusCode: 409, message: 'draft changed since you loaded it — reload and retry' }),
    });
    renderPage();
    await waitFor(() => expect(screen.getByTestId('admin-i18n-msgs-table')).toBeInTheDocument());
    fireEvent.change(screen.getByDisplayValue('你好 {name}'), { target: { value: '您好 {name}' } });
    fireEvent.click(screen.getByTestId('admin-i18n-save-app-hello'));
    await waitFor(() => expect(screen.getByTestId('admin-i18n-conflict')).toBeInTheDocument());
  });

  it('publishes only after explicit confirmation and shows revision/checksum', async () => {
    wireHappyPath({
      '/i18n/admin/publish/zh': jsonResponse(201, {
        id: 'rev-zh-2', locale: 'zh', version: 2,
        checksum: 'abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789',
        publishedBy: '0xe2cd26322a87d2b6d8312dbcb79c38b7a226ad81', publishedAt: '2026-07-10T01:00:00Z',
      }),
    });
    renderPage();
    await waitFor(() => expect(screen.getByTestId('admin-i18n-publish')).toBeInTheDocument());
    fireEvent.click(screen.getByTestId('admin-i18n-publish'));
    // Nothing published yet — confirmation armed.
    expect(mockFetchApi).not.toHaveBeenCalledWith('/i18n/admin/publish/zh', expect.anything());
    fireEvent.click(screen.getByTestId('admin-i18n-publish-confirm'));
    await waitFor(() => expect(screen.getByTestId('admin-i18n-publish-result')).toBeInTheDocument());
    expect(screen.getByTestId('admin-i18n-publish-result').textContent).toMatch(/v2/);
  });

  it('blocks publish with visible validation issues on 422', async () => {
    wireHappyPath({
      '/i18n/admin/publish/zh': jsonResponse(422, {
        statusCode: 422, message: 'validation failed',
        issues: [{ namespace: 'app', key: 'hello', code: 'icu-arg-mismatch', detail: 'argument {nom} does not exist in the default locale message' }],
      }),
    });
    renderPage();
    await waitFor(() => expect(screen.getByTestId('admin-i18n-publish')).toBeInTheDocument());
    fireEvent.click(screen.getByTestId('admin-i18n-publish'));
    fireEvent.click(screen.getByTestId('admin-i18n-publish-confirm'));
    await waitFor(() => expect(screen.getByTestId('admin-i18n-publish-error')).toBeInTheDocument());
    expect(screen.getByText(/icu-arg-mismatch/)).toBeInTheDocument();
  });
});
