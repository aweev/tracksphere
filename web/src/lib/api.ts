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
  patch: <T>(path: string, payload?: unknown) =>
    request<T>(path, { method: 'PATCH', body: JSON.stringify(payload ?? {}) }),
  put: <T>(path: string, payload?: unknown) =>
    request<T>(path, { method: 'PUT', body: JSON.stringify(payload ?? {}) }),
  del: <T>(path: string) => request<T>(path, { method: 'DELETE' }),
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

  // Read-model fields. These decide what an operator works next and are
  // computed once server-side, so the list can be sorted by consequence.
  riskScore?: number
  riskTier?: RiskTier
  staleHours?: number
  openAlerts?: number
  criticalAlerts?: number
  /** 'carrier' | 'estimated' | 'lane_model' | 'none' — never render an
   *  estimate identically to a carrier-published date. */
  etaSource?: 'carrier' | 'estimated' | 'lane_model' | 'none'
  etaConfidence?: number
  valueAtRisk?: number
  customerNotified?: boolean
  dwellHours?: number
  expectedDwellHours?: number
  dwellRatio?: number
  lat?: number
  lng?: number
  /**
   * Per-term score explanation exactly as the server computed it. Render it;
   * never recompute the weights client-side. Absent on rows predating the
   * breakdown column — treat as "not yet computed", not as zero.
   */
  riskBreakdown?: RiskBreakdown
}

/** Mirrors readmodel.Breakdown. All values are points contributed, except
 *  valueKnown (whether a cargo value was declared) and relief (negative). */
export interface RiskBreakdown {
  dwell: number
  slip: number
  stale: number
  critical: number
  alerts: number
  value: number
  valueKnown: boolean
  relief: number
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

export type AlertSeverity = 'info' | 'warning' | 'critical';
export type AlertKind =
  | 'delay'
  | 'customs_hold'
  | 'port_congestion'
  | 'sla_breach'
  | 'eta_revised'
  | 'disruption'
  | 'delivery_failed'
  | 'stale'
  | 'dwell'
  | 'dwell_critical'
  | 'eta_slip'
  | 'recurring';

export type AlertRootCause =
  | 'documentation'
  | 'customs_duty'
  | 'customs_processing'
  | 'carrier_delay'
  | 'port_congestion'
  | 'weather'
  | 'capacity'
  | 'missed_connection'
  | 'blank_sailing'
  | 'carrier_error'
  | 'shipper_delay'
  | 'other';

export type RiskTier = 'clear' | 'watch' | 'at_risk' | 'critical';

export interface Alert {
  id: string;
  shipmentId: string;
  kind: AlertKind;
  severity: AlertSeverity;
  title: string;
  message: string;
  status: 'open' | 'resolved';
  createdAt: string;
  trackingNumber?: string;
  shipmentStatus?: string;
  shipmentEta?: string;

  // Ownership loop. An alert with no assignee belongs to nobody, so in a team
  // nobody works it — unowned work is invisible work.
  assignedTo?: string;
  assignedToName?: string;
  assignedAt?: string;
  acknowledgedAt?: string;

  // SLA pressure.
  dueAt?: string;
  escalatedAt?: string;
  snoozedUntil?: string;

  // Closure feedback: the root cause is what makes carrier scorecards and ETA
  // calibration possible.
  rootCause?: AlertRootCause;
  note?: string;
  resolution?: string;
  valueAtRisk?: number;

  detectedAt: string;
  lastSeenAt: string;

  // Read-model context so the queue answers "how bad, and is the customer told?"
  riskScore?: number;
  riskTier?: RiskTier;
  staleHours?: number;
  openAlertCount?: number;
  customerNotified?: boolean;
}

export interface NotificationItem {
  id: number
  shipmentId?: string
  channel: string
  recipient: string
  subject: string
  status: string
  createdAt: string
}

export interface ApiKey {
  id: string
  name: string
  prefix: string
  role: string
  revoked: boolean
  lastUsedAt?: string
  createdAt: string
}

export interface WebhookEndpoint {
  id: string
  url: string
  events: string[]
  active: boolean
}

export interface TeamMember {
  id: string
  tenantId: string
  email: string
  name: string
  role: 'owner' | 'admin' | 'member'
  totpEnabled: boolean
  active: boolean
  createdAt: string
}

export interface SessionItem {
  id: string
  createdAt: string
  expiresAt: string
  lastSeenAt: string
  ip: string
  current: boolean
}

export interface Billing {
  plan: string
  trialEndsAt: string
  trialDaysLeft: number
  trialActive: boolean
  usage: { shipments: number; seats: number; apiKeys: number; endpoints: number }
  limits: { shipments: number; seats: number; apiKeys: number; endpoints: number }
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
  /** none | carrier | estimated | lane_model — provenance of `eta`. */
  etaSource?: string
  shippedAt?: string
  deliveredAt?: string
  lastUpdate: string
  events: ShipmentEvent[]
  brand?: { company: string; color: string; logo: string; support: string }
}

export interface Leg {
  id: string
  seq: number
  carrier: string
  mode: string
  origin: string
  destination: string
  status: string
  eta?: string
}

export interface DocItem {
  id: string
  filename: string
  contentType: string
  sizeBytes: number
  createdAt: string
}

export interface AuditItem {
  actor: string
  action: string
  detail: Record<string, unknown>
  createdAt: string
}

export interface Analytics {
  total: number
  delivered: number
  onTime: number
  onTimePct?: number
  exceptions: number
  avgTransitDays?: number
  etaAccuracyPct?: number
  etaSamples?: number
  carriers: Array<{ carrier: string; total: number; onTime: number; onTimePct?: number; avgTransitDays?: number }>
  lanes: Array<{ origin: string; destination: string; total: number; exceptions: number }>
}

export interface Digest {
  weekStart: string
  delivered: number
  onTimePct?: number
  exceptions: number
  openAlerts: number
  staleShipments: number
  topCarriers: Array<{ carrier: string; total: number }>
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
      // The password flow hands the challenge to us in the login response. The
      // SSO flow cannot: it is a browser redirect, so the challenge travels in
      // an HttpOnly cookie scoped to this endpoint and there is nothing to send
      // from here. Omitting the field (rather than sending "") keeps the server
      // on its cookie path.
      body: JSON.stringify(challenge ? { challenge, code } : { code }),
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
  resolveAlert: (id: string, body?: { rootCause?: AlertRootCause; note?: string }) =>
    request<{ ok: boolean }>(`/api/v1/alerts/${id}/resolve`, {
      method: 'POST',
      body: JSON.stringify(body ?? {}),
    }),
  assignAlert: (
    id: string,
    body: {
      userId?: string;
      snoozeMinutes?: number;
      rootCause?: AlertRootCause;
      note?: string;
    },
  ) =>
    request<{ ok: boolean }>(`/api/v1/alerts/${id}/assign`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  noteAlert: (id: string, body: { note: string; rootCause?: AlertRootCause }) =>
    request<{ ok: boolean }>(`/api/v1/alerts/${id}/note`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  listTeam: () => api.get<TeamMember[]>('/api/v1/team').then((r) => r.data),
};
