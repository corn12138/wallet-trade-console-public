import React from 'react';

export default function TradingLayout({
    children,
}: {
    children: React.ReactNode;
}) {
    return <div className="space-y-6">{children}</div>;
}
