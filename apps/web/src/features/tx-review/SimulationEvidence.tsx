'use client';

import { useTranslations } from 'next-intl';
import { observationKinds, readSimulationEvidence } from './model/simulation-evidence';

export function SimulationEvidence({ simulation }: { simulation: unknown }) {
  const t = useTranslations('security.txReview.simulationEvidence');
  const evidence = readSimulationEvidence(simulation);
  return (
    <details className="mt-6" data-testid="simulation-evidence" style={{ fontSize: 11, overflowWrap: 'anywhere' }}>
      <summary>{t(`scope.${evidence.scope}`)} · {t(evidence.consistent ? 'consistent' : 'incomplete')}</summary>
      <p>{t('scopeHint')}</p>
      <dl style={{ marginTop: 6 }}>
        {observationKinds.map((kind) => {
          const item = evidence.observations[kind];
          return (
            <div key={kind} style={{ marginTop: 6 }}>
              <dt><b>{t(`kind.${kind}`)}</b> · {t(`status.${item?.observationStatus ?? 'unavailable'}`)}</dt>
              <dd style={{ marginLeft: 12 }}>
                {t(`source.${item?.source ?? 'unknown'}`)} · {t(`pinning.${item?.pinning ?? 'unavailable'}`)}
                {item?.blockReference && <div>{t('block', { number: item.blockReference.number })} · {item.blockReference.hash}</div>}
                {item && <div>{t('observedAt', { time: item.observedAt })}</div>}
              </dd>
            </div>
          );
        })}
      </dl>
      <p>{t('feeHint')}</p>
    </details>
  );
}
