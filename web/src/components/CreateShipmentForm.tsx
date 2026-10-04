'use client';

import { useState, type FormEvent } from 'react';
import { Card } from './ui';
import { api, RequestError } from '@/lib/api';

export function CreateShipmentForm({ onCreated }: { onCreated: () => void }) {
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [publish, setPublish] = useState(false);
  const [guess, setGuess] = useState('');
  const [form, setForm] = useState({
    trackingNumber: '',
    reference: '',
    carrier: 'maersk',
    mode: 'ocean',
    origin: '',
    destination: '',
  });

  const set = (k: keyof typeof form) => (e: { target: { value: string } }) => {
    const v = e.target.value;
    setForm((f) => ({ ...f, [k]: v }));
    if (k === 'trackingNumber') {
      const tn = v.trim();
      if (tn.length >= 6) {
        fetch(`/api/v1/carriers/detect?tracking=${encodeURIComponent(tn)}`, { credentials: 'include' })
          .then((r) => (r.ok ? r.json() : null))
          .then((body) => {
            const top = body?.data?.[0];
            if (top && top.carrier && top.carrier !== form.carrier) {
              setGuess(`Looks like ${top.carrier} (${Math.round(top.confidence * 100)}% match)`);
            } else {
              setGuess('');
            }
          })
          .catch(() => undefined);
      } else {
        setGuess('');
      }
    }
  };

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    setBusy(true);
    try {
      await api.post('/api/v1/shipments', { ...form, isPublic: publish });
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
          {guess ? (
            <p className="-mt-2 text-xs text-slate-500 md:col-span-3">
              {guess} —{' '}
              <button
                type="button"
                onClick={() => {
                  const m = guess.match(/Looks like (\S+)/);
                  if (m) setForm((f) => ({ ...f, carrier: m[1] }));
                  setGuess('');
                }}
                className="font-semibold text-accent-500 hover:underline"
              >
                use it
              </button>
            </p>
          ) : null}
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
          <div className="flex items-center gap-2 md:col-span-3">
            <input
              id="new-shipment-public"
              type="checkbox"
              checked={publish}
              onChange={(e) => setPublish(e.target.checked)}
              className="h-4 w-4 accent-[#ff6b00]"
            />
            <label htmlFor="new-shipment-public" className="text-sm text-slate-600">
              Publish to public tracking portal (private by default)
            </label>
          </div>
          {error ? <p role="alert" className="text-sm text-red-600 md:col-span-3">{error}</p> : null}
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
