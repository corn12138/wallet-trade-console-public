'use client';
import React from 'react';

/**
 * HeroIsoArt is a purely decorative isometric illustration for the marketing
 * hero/CTA. The former `MiniChart` (a Math.random candlestick SVG) was removed
 * with the fake home terminal preview — the home page now renders the real
 * live trading chart via HomeLivePreview → TradeCandleChart.
 */
export function HeroIsoArt({ small }: { small?: boolean }) {
  return (
    <svg viewBox="0 0 600 480" style={{ width: '100%', height: small ? 280 : 460 }}>
      <ellipse cx="300" cy="430" rx="220" ry="22" fill="rgba(26,18,7,0.15)" />
      <g className="float">
        <polygon points="100,200 220,140 340,200 220,260" fill="#FFD23F" stroke="#1A1207" strokeWidth="4" strokeLinejoin="round" />
        <polygon points="100,200 100,290 220,350 220,260" fill="#D9A82B" stroke="#1A1207" strokeWidth="4" strokeLinejoin="round" />
        <polygon points="340,200 340,290 220,350 220,260" fill="#B08820" stroke="#1A1207" strokeWidth="4" strokeLinejoin="round" />
        <text x="220" y="222" fontFamily="Unbounded" fontWeight="900" fontSize="42" fill="#1A1207" textAnchor="middle">
          ₿
        </text>
        <text x="150" y="318" fontFamily="Unbounded" fontWeight="800" fontSize="13" fill="#1A1207">
          BTC
        </text>
        <text x="295" y="318" fontFamily="JetBrains Mono" fontWeight="700" fontSize="13" fill="#1A1207" textAnchor="end">
          +2.84%
        </text>
      </g>
      <g className="float f2">
        <polygon points="280,90 400,30 520,90 400,150" fill="#2EC4B6" stroke="#1A1207" strokeWidth="4" strokeLinejoin="round" />
        <polygon points="280,90 280,180 400,240 400,150" fill="#1D9489" stroke="#1A1207" strokeWidth="4" strokeLinejoin="round" />
        <polygon points="520,90 520,180 400,240 400,150" fill="#16766C" stroke="#1A1207" strokeWidth="4" strokeLinejoin="round" />
        <text x="400" y="113" fontFamily="Unbounded" fontWeight="900" fontSize="42" fill="#FFF" textAnchor="middle">
          Ξ
        </text>
      </g>
      <g className="float f3">
        <polygon points="350,260 470,200 560,250 440,310" fill="#C44CE8" stroke="#1A1207" strokeWidth="4" strokeLinejoin="round" />
        <polygon points="350,260 350,340 440,395 440,310" fill="#9436B5" stroke="#1A1207" strokeWidth="4" strokeLinejoin="round" />
        <polygon points="560,250 560,330 440,395 440,310" fill="#762890" stroke="#1A1207" strokeWidth="4" strokeLinejoin="round" />
        <text x="455" y="282" fontFamily="Unbounded" fontWeight="900" fontSize="36" fill="#FFF" textAnchor="middle">
          ◎
        </text>
      </g>
      <g className="float">
        <circle cx="80" cy="120" r="22" fill="#FF7043" stroke="#1A1207" strokeWidth="4" />
        <text x="80" y="128" fontFamily="Unbounded" fontWeight="900" fontSize="20" fill="#FFF" textAnchor="middle">
          $
        </text>
      </g>
      <g className="float f2">
        <rect x="500" y="380" width="40" height="40" rx="10" fill="#5BD66B" stroke="#1A1207" strokeWidth="4" transform="rotate(-12 520 400)" />
      </g>
    </svg>
  );
}
