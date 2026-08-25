export const sessionInvalidCode = 'AUTH_SESSION_INVALID'

export interface AuthenticationBridge {
  beforeMutation: () => Promise<string>
  invalidate: (code: string) => void
}

let bridge: AuthenticationBridge | null = null

export class AuthenticationUnavailableError extends Error {
  constructor() {
    super('浏览器会话不可用。')
    this.name = 'AuthenticationUnavailableError'
  }
}

export function configureAuthenticationBridge(value: AuthenticationBridge | null): void {
  bridge = value
}

export async function prepareMutationCSRF(): Promise<string> {
  if (bridge === null) throw new AuthenticationUnavailableError()
  return bridge.beforeMutation()
}

export function publishAuthenticationInvalidation(code: string): void {
  if (code === sessionInvalidCode) bridge?.invalidate(code)
}

export function isAuthenticationFailure(reason: unknown): boolean {
  return reason instanceof AuthenticationUnavailableError
    || (isCodedError(reason) && reason.code === sessionInvalidCode)
}

function isCodedError(reason: unknown): reason is { code: string } {
  return typeof reason === 'object' && reason !== null && 'code' in reason
    && typeof (reason as { code?: unknown }).code === 'string'
}
