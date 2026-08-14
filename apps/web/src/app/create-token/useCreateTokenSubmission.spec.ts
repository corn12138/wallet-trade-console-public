import { act, renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { INITIAL_TOKEN_FORM, type TokenForm } from './create-token.types';
import { useCreateTokenSubmission } from './useCreateTokenSubmission';

/**
 * Selected media must actually be uploaded and the RETURNED durable URLs must
 * flow into the createAndLaunch args — never a generated placeholder — and an
 * upload failure must block the wallet write entirely.
 */

const mockUploadMediaFile = vi.fn();
const mockWriteContract = vi.fn();

vi.mock('@/lib/api/media', () => ({
    uploadMediaFile: (file: File) => mockUploadMediaFile(file),
}));

vi.mock('@/lib/api/auth-fetch', () => ({
    fetchApi: vi.fn(),
}));

vi.mock('@/hooks/web3/usePersistTransactionLifecycle', () => ({
    usePersistTransactionLifecycle: vi.fn(),
}));

vi.mock('@/lib/web3/contracts', () => ({
    CONTRACT_ABIS: { TokenFactory: [] },
    getTokenFactoryAddress: () => '0x00000000000000000000000000000000000000fa',
}));

vi.mock('wagmi', () => ({
    useWriteContract: () => ({
        writeContract: mockWriteContract,
        data: undefined,
        isPending: false,
        error: null,
    }),
    useWaitForTransactionReceipt: () => ({
        isLoading: false,
        isSuccess: false,
        data: undefined,
    }),
}));

const t = (key: string, values?: Record<string, string | number>) =>
    values?.message ? `${key}:${values.message}` : key;

function formWith(overrides: Partial<TokenForm>): TokenForm {
    return {
        ...INITIAL_TOKEN_FORM,
        name: 'Real Token',
        symbol: 'REAL',
        description: 'no placeholders',
        ...overrides,
    };
}

function renderSubmission(form: TokenForm) {
    return renderHook(() =>
        useCreateTokenSubmission({
            chainId: 11155111,
            form,
            isConnected: true,
            walletAddress: '0x1111111111111111111111111111111111111111',
            onCreated: vi.fn(),
            t,
        }),
    );
}

describe('useCreateTokenSubmission media handling', () => {
    beforeEach(() => {
        vi.clearAllMocks();
    });

    it('uploads the selected image/banner and passes returned URLs to the contract', async () => {
        const image = new File(['png-bytes'], 'icon.png', { type: 'image/png' });
        const banner = new File(['banner-bytes'], 'banner.png', { type: 'image/png' });
        mockUploadMediaFile.mockImplementation(async (file: File) => ({
            url: `https://api.test/api/media/files/${file.name}`,
        }));

        const { result } = renderSubmission(formWith({ image, banner }));
        await act(async () => {
            await result.current.handleSubmit();
        });

        expect(mockUploadMediaFile).toHaveBeenCalledTimes(2);
        expect(mockUploadMediaFile).toHaveBeenCalledWith(image);
        expect(mockUploadMediaFile).toHaveBeenCalledWith(banner);

        expect(mockWriteContract).toHaveBeenCalledTimes(1);
        const args = mockWriteContract.mock.calls[0][0].args as string[];
        expect(args[3]).toBe('https://api.test/api/media/files/icon.png');
        expect(args[4]).toBe('https://api.test/api/media/files/banner.png');
        // No DiceBear/Picsum placeholder can sneak back in.
        const stringArgs = args.filter((a): a is string => typeof a === 'string').join('|');
        expect(stringArgs).not.toMatch(/dicebear|picsum/i);
    });

    it('blocks the wallet write when an upload fails', async () => {
        const image = new File(['png-bytes'], 'icon.png', { type: 'image/png' });
        mockUploadMediaFile.mockRejectedValue(new Error('media storage is not configured'));

        const { result } = renderSubmission(formWith({ image }));
        await act(async () => {
            await result.current.handleSubmit();
        });

        expect(mockWriteContract).not.toHaveBeenCalled();
        expect(result.current.notice?.tone).toBe('error');
        expect(result.current.notice?.message).toContain('media storage is not configured');
    });

    it('submits honest empty strings (not placeholders) when no files are selected', async () => {
        const { result } = renderSubmission(formWith({ image: null, banner: null }));
        await act(async () => {
            await result.current.handleSubmit();
        });

        expect(mockUploadMediaFile).not.toHaveBeenCalled();
        expect(mockWriteContract).toHaveBeenCalledTimes(1);
        const args = mockWriteContract.mock.calls[0][0].args as string[];
        expect(args[3]).toBe('');
        expect(args[4]).toBe('');
    });
});
