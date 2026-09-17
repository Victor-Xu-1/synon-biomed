export type WebHostAuthUser = {
  id: string;
  username: string;
  displayName?: string;
  email?: string;
};

export type RegisterAccountParams = {
  name: string;
  email?: string;
  password: string;
  remember?: boolean;
};

export type AuthRequestErrorCode =
  | 'INVALID_NAME'
  | 'INVALID_EMAIL'
  | 'INVALID_PASSWORD'
  | 'USERNAME_EXISTS'
  | 'EMAIL_EXISTS'
  | 'TOO_MANY_ATTEMPTS'
  | 'REQUEST_TOO_LARGE'
  | 'INVALID_JSON'
  | 'INVALID_REQUEST'
  | 'NETWORK_ERROR'
  | 'SERVER_ERROR'
  | 'UNKNOWN';

export type RegisterAccountResult =
  | { success: true; user: WebHostAuthUser }
  | { success: false; code: AuthRequestErrorCode; message?: string };

export async function registerWebHostAccount(
  params: RegisterAccountParams,
  signal?: AbortSignal
): Promise<RegisterAccountResult> {
  try {
    const response = await fetch('/api/auth/register', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify(params),
      signal,
    });
    const data = (await response.json()) as {
      success?: boolean;
      user?: WebHostAuthUser;
      code?: AuthRequestErrorCode;
      message?: string;
    };
    if (response.ok && data.success && data.user) return { success: true, user: data.user };
    return {
      success: false,
      code: response.status >= 500 ? 'SERVER_ERROR' : (data.code ?? 'UNKNOWN'),
      message: data.message,
    };
  } catch {
    if (!signal?.aborted) console.error('[auth] account registration unavailable');
    return { success: false, code: 'NETWORK_ERROR' };
  }
}
