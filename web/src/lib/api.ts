// Typed API client. Same-origin (/api/* is rewritten to the Go backend by
// Next), so the HttpOnly session cookie rides along automatically.

export interface ApiError {
  code: string
  message: string
}

interface Envelope<T> {
  data?: T
  error?: ApiError
  meta?: { total: number; limit?: number; page?: number }
}

export interface PageMeta {
  total: number
  limit?: number
  page?: number
}

export class RequestError extends Error {
  readonly code: string
  readonly status: number
  constructor(status: number, code: string, message: string) {
    super(message)
    this.code = code
    this.status = status
  }
}

async function request<T>(
  path: string,
  init?: RequestInit,
): Promise<{ data: T; meta?: PageMeta }> {
  const res = await fetch(path, {
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', ...init?.headers },
    ...init,
  })
  let body: Envelope<T> | null = null
  try {
    body = (await res.json()) as Envelope<T>
  } catch {
    /* non-JSON error body */
  }
  if (!res.ok) {
    const err = body?.error
    throw new RequestError(res.status, err?.code ?? 'http_error', err?.message ?? res.statusText)
  }
  return { data: body?.data as T, meta: body?.meta }
}

export const api = {
  get: <T>(path: string) => request<T>(path),
  post: <T>(path: string, payload?: unknown) =>
    request<T>(path, { method: 'POST', body: JSON.stringify(payload ?? {}) }),
}

// ── Domain types (mirror of the Go models) ──────────────────────────────

export interface User {
  id: string
  tenantId: string
  email: string
  name: string
  role: 'owner' | 'admin' | 'member'
  totpEnabled: boolean
  createdAt: string
}

export interface Tenant {
  id: string
  name: string
  slug: string
  plan: string
  createdAt: string
}

export type ShipmentStatus =
  | 'booked'
  | 'in_transit'
  | 'at_customs'
  | 'out_for_delivery'
  | 'delivered'
  | 'exception'
  | 'cancelled'

export interface Shipment {
  id: string
  trackingNumber: string
  reference: string
  carrier: string
  mode: 'ocean' | 'air' | 'road' | 'rail'
  origin: string
  destination: string
  status: ShipmentStatus
  eta?: string
  shippedAt?: string
  deliveredAt?: string
  isPublic: boolean
  createdAt: string
  updatedAt: string
}

export interface ShipmentEvent {
  id: string
  shipmentId: string
  carrier: string
  code: string
  description: string
  location: string
  lat?: number
  lng?: number
  occurredAt: string
  receivedAt: string
  source: string
}

export interface Alert {
  id: string
  shipmentId: string
  kind: 'delay' | 'customs_hold' | 'port_congestion' | 'sla_breach' | 'eta_revised'
  severity: 'info' | 'warning' | 'critical'
  title: string
  message: string
  status: 'open' | 'resolved'
  createdAt: string
}

export interface DashboardStats {
  activeShipments: number
  deliveredShipments: number
  exceptionShipments: number
  overdueShipments: number
  openAlerts: number
}

export interface PublicTracking {
  trackingNumber: string
  carrier: string
  mode: string
  origin: string
  destination: string
  status: ShipmentStatus
  eta?: string
  shippedAt?: string
  deliveredAt?: string
  lastUpdate: string
  events: ShipmentEvent[]
}

// Auth calls carry special response shapes.
export const authApi = {
  login: (email: string, password: string) =>
    request<{ user?: User; mfaRequired?: boolean; challenge?: string }>('/api/v1/auth/login', {
      method: 'POST',
      body: JSON.stringify({ email, password }),
    }),
  mfaVerify: (challenge: string, code: string) =>
    request<{ user: User }>('/api/v1/auth/mfa/verify', {
      method: 'POST',
      body: JSON.stringify({ challenge, code }),
    }),
  register: (orgName: string, name: string, email: string, password: string) =>
    request<{ user: User; tenant: Tenant }>('/api/v1/auth/register', {
      method: 'POST',
      body: JSON.stringify({ orgName, name, email, password }),
    }),
  me: () => request<{ user: User }>('/api/v1/auth/me'),
  logout: () => request<{ ok: boolean }>('/api/v1/auth/logout', { method: 'POST' }),
  mfaEnroll: () => request<{ secret: string; otpauthURI: string }>('/api/v1/auth/mfa/enroll', { method: 'POST' }),
  mfaEnable: (code: string) =>
    request<{ totpEnabled: boolean }>('/api/v1/auth/mfa/enable', {
      method: 'POST',
      body: JSON.stringify({ code }),
    }),
  mfaDisable: (code: string) =>
    request<{ totpEnabled: boolean }>('/api/v1/auth/mfa/disable', {
      method: 'POST',
      body: JSON.stringify({ code }),
    }),
  resolveAlert: (id: string) =>
    request<{ ok: boolean }>(`/api/v1/alerts/${id}/resolve`, { method: 'POST' }),
};
