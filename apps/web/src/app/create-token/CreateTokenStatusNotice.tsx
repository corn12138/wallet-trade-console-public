import type { SubmissionNotice } from './create-token.types';

const toneStyles: Record<SubmissionNotice['tone'], React.CSSProperties> = {
  idle: { background: 'var(--bg-2)', color: 'var(--ink-2)' },
  info: { background: 'var(--c)', color: '#fff' },
  success: { background: 'var(--g)', color: 'var(--ink)' },
  error: { background: 'var(--o)', color: '#fff' },
};

export function CreateTokenStatusNotice({
  notice,
}: {
  notice: SubmissionNotice | null;
}) {
  if (!notice || notice.tone === 'idle') {
    return null;
  }

  return (
    <div
      role="status"
      className="block tight"
      style={{
        padding: '12px 14px',
        fontFamily: 'var(--mf)',
        fontSize: 12.5,
        lineHeight: 1.5,
        boxShadow: 'none',
        ...toneStyles[notice.tone],
      }}
    >
      {notice.message}
    </div>
  );
}
