interface OnchainArtworkPreviewProps {
  title: string;
  caption: string;
  accentColor: string;
}

/**
 * Faithful visual preview of the on-chain SVG ticket the contract renders —
 * the dark card IS the artwork's design, so it intentionally keeps its own
 * palette, framed with the Atlas border treatment.
 */
export function OnchainArtworkPreview({
  title,
  caption,
  accentColor,
}: OnchainArtworkPreviewProps) {
  return (
    <div
      style={{
        position: 'relative',
        overflow: 'hidden',
        borderRadius: 20,
        border: '3px solid var(--ink)',
        boxShadow: '0 6px 0 0 var(--ink)',
        background: 'linear-gradient(135deg, #0f172a, #020617)',
        padding: 24,
      }}
    >
      <div
        style={{
          position: 'absolute',
          right: -40,
          top: -40,
          height: 160,
          width: 160,
          borderRadius: 999,
          filter: 'blur(48px)',
          backgroundColor: `${accentColor}40`,
        }}
      />
      <div
        style={{
          position: 'absolute',
          bottom: -48,
          left: -48,
          height: 208,
          width: 208,
          borderRadius: 999,
          filter: 'blur(48px)',
          backgroundColor: `${accentColor}24`,
        }}
      />

      <div style={{ position: 'relative' }}>
        <div
          style={{
            fontFamily: 'var(--df)',
            fontSize: 10,
            fontWeight: 700,
            textTransform: 'uppercase',
            letterSpacing: '0.18em',
            color: 'rgba(148,163,184,0.9)',
          }}
        >
          AI Mint Ticket
        </div>
        <h3 style={{ margin: '18px 0 0', fontFamily: 'var(--df)', fontWeight: 800, fontSize: 28, color: '#fff' }}>
          {title || 'Genesis Ticket'}
        </h3>
        <p style={{ margin: '10px 0 0', maxWidth: 420, fontSize: 13, lineHeight: 1.6, color: 'rgba(203,213,225,0.95)' }}>
          {caption || 'Minted from the MetaLand NFT Studio'}
        </p>
        <div
          style={{
            marginTop: 22,
            height: 4,
            width: '100%',
            borderRadius: 999,
            background: `linear-gradient(90deg, ${accentColor}, #ffffff)`,
          }}
        />
        <div style={{ marginTop: 36, fontFamily: 'var(--df)', fontWeight: 900, fontSize: 52, letterSpacing: '-0.03em', color: 'rgba(255,255,255,0.9)' }}>
          #AMT
        </div>
        <div
          style={{
            marginTop: 34,
            display: 'inline-flex',
            borderRadius: 999,
            border: `1.5px solid ${accentColor}66`,
            padding: '4px 12px',
            fontFamily: 'var(--mf)',
            fontSize: 11,
            fontWeight: 600,
            color: accentColor,
          }}
        >
          On-chain SVG
        </div>
      </div>
    </div>
  );
}
