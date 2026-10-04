'use client';

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from 'react';
import { authApi, RequestError, type Tenant, type User } from './api';

interface AuthState {
  user: User | null;
  tenant: Tenant | null;
  loading: boolean;
  /** MFA challenge flow: set when login returns mfaRequired. */
  challenge: string | null;
  /**
   * MFA challenge delivered out-of-band after an SSO redirect. The challenge
   * token is in an HttpOnly cookie, so script never sees it and there is
   * nothing to hold here — only the fact that a challenge is pending.
   */
  ssoChallenge: boolean;
  startSsoChallenge: () => void;
  login: (email: string, password: string) => Promise<'ok' | 'mfa'>;
  verifyMfa: (code: string) => Promise<void>;
  register: (orgName: string, name: string, email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  clearChallenge: () => void;
}

const AuthCtx = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [tenant, setTenant] = useState<Tenant | null>(null);
  const [loading, setLoading] = useState(true);
  const [challenge, setChallenge] = useState<string | null>(null);
  const [ssoChallenge, setSsoChallenge] = useState(false);

  // Restore session on boot (cookie-based).
  useEffect(() => {
    authApi
      .me()
      .then(({ data }) => setUser(data.user))
      .catch((e: unknown) => {
        if (e instanceof RequestError && e.status === 401) return; // signed out
        console.error('session restore failed', e);
      })
      .finally(() => setLoading(false));
  }, []);

  const login = useCallback(async (email: string, password: string) => {
    const { data } = await authApi.login(email, password);
    if (data.mfaRequired && data.challenge) {
      setChallenge(data.challenge);
      return 'mfa' as const;
    }
    if (data.user) setUser(data.user);
    return 'ok' as const;
  }, []);

  const verifyMfa = useCallback(
    async (code: string) => {
      if (!challenge && !ssoChallenge) throw new Error('No MFA challenge in progress');
      // An empty challenge tells the server to read the HttpOnly cookie.
      const { data } = await authApi.mfaVerify(challenge ?? '', code);
      setUser(data.user);
      setChallenge(null);
      setSsoChallenge(false);
    },
    [challenge, ssoChallenge],
  );

  const register = useCallback(
    async (orgName: string, name: string, email: string, password: string) => {
      const { data } = await authApi.register(orgName, name, email, password);
      setUser(data.user);
      setTenant(data.tenant);
    },
    [],
  );

  const logout = useCallback(async () => {
    await authApi.logout().catch(() => undefined);
    setUser(null);
    setTenant(null);
    setChallenge(null);
  }, []);

  return (
    <AuthCtx.Provider
      value={{
        user,
        tenant,
        loading,
        challenge,
        ssoChallenge,
        startSsoChallenge: () => setSsoChallenge(true),
        login,
        verifyMfa,
        register,
        logout,
        clearChallenge: () => {
          setChallenge(null);
          setSsoChallenge(false);
        },
      }}
    >
      {children}
    </AuthCtx.Provider>
  );
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthCtx);
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider');
  return ctx;
}
