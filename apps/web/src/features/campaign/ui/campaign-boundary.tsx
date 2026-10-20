'use client';

import { Component, type ReactNode } from 'react';

export class CampaignBoundary extends Component<{ children: ReactNode; message: string; retry: string }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() { return { failed: true }; }
  render() {
    if (this.state.failed) return <div className="block" role="alert"><p>{this.props.message}</p>
      <button type="button" className="btn btn-sm" onClick={() => this.setState({ failed: false })}>{this.props.retry}</button></div>;
    return this.props.children;
  }
}
