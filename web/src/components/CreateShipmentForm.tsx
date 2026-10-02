'use client';

import { useState, type FormEvent } from 'react';
import { Card } from './ui';
import { api, RequestError } from '@/lib/api';

export function CreateShipmentForm({ onCreated }: { onCreated: () => void }) {
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [form, setForm] = useState({
    trackingNumber: '',
    reference: '',
    carrier: 'maersk',
    mode: 'ocean',
    origin: '',
    destination: '',
  });

  const set = (k: keyof typeof form) => (e: { target: { value: string } }) =>
    setForm((f) => ({ ...f, [k]: e.target.value }));

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    setBusy(true);
    try {
      await api.post('/api/v1/shipments', form);
      onCreated();
    } catch (err) {
      setError(err instanceof RequestError ? err.message : 'Create failed');
    } finally {
      setBusy(false);
    }
  };

  const input =
    'w-full rounded-xl border border-slate-300 px-3 py-2.5 text-sm focus:border-accent-500 focus:outline-none';

  return (
    <div className="mb-6">
      <Card title="New shipment">
        <form onSubmit={onSubmit} className="grid grid-cols-1 gap-4 md:grid-cols-3">
          <Input label="Tracking number *" required value={form.trackingNumber} onChange={set('trackingNumber')} className={input} placeholder="TS-1234-ABC" />
          <Input label="Reference" value={form.reference} onChange={set('reference')} className={input} placeholder="PO-2026-0001" />
          <Input label="Carrier *" required value={form.carrier} onChange={set('carrier')} className={input} placeholder="maersk" />
          <div>
            <label className="mb-1 block text-xs font-semibold text-slate-500">Mode *</label>
            <select value={form.mode} onChange={set('mode')} className={input}>
              <option value="ocean">Ocean</option>
              <option value="air">Air</option>
              <option value="road">Road</option>
              <option value="rail">Rail</option>
            </select>
          </div>
          <Input label="Origin *" required value={form.origin} onChange={set('origin')} className={input} placeholder="Shanghai, CN" />
          <Input label="Destination *" required value={form.destination} onChange={set('destination')} className={input} placeholder="Lagos, NG" />
          {error ? <p className="text-sm text-red-600 md:col-span-3">{error}</p> : null}
          <div className="md:col-span-3">
            <button
              disabled={busy}
              className="rounded-xl bg-navy-950 px-5 py-2.5 text-sm font-semibold text-white hover:bg-navy-900 disabled:opacity-50"
            >
              {busy ? 'Creating…' : 'Create shipment'}
            </button>
          </div>
        </form>
      </Card>
    </div>
  );
}

function Input(props: {
  label: string;
  required?: boolean;
  value: string;
  onChange: (e: { target: { value: string } }) => void;
  className: string;
  placeholder?: string;
}) {
  const { label, ...rest } = props;
  return (
    <div>
      <label className="mb-1 block text-xs font-semibold text-slate-500">{label}</label>
      <input {...rest} />
    </div>
  );
}
