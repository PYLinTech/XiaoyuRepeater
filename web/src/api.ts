export class ApiError extends Error {
  status: number
  code?: string

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

export interface RequestOptions {
  method?: string
  body?: unknown
  headers?: Record<string, string>
}

let unauthorizedHandler: (() => void) | null = null

export function setUnauthorizedHandler(handler: () => void) {
  unauthorizedHandler = handler
}

export async function api<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    ...(options.headers ?? {}),
  }
  const init: RequestInit = {
    method: options.method ?? (options.body !== undefined ? 'POST' : 'GET'),
    credentials: 'same-origin',
    headers,
  }
  if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(options.body)
  }

  let response: Response
  try {
    response = await fetch(path, init)
  } catch {
    throw new ApiError('无法连接服务器，请检查网络', 0)
  }

  const text = await response.text()
  let payload: unknown = null
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = null
    }
  }

  if (!response.ok) {
    const data = (payload ?? {}) as { error?: string; code?: string }
    const error = new ApiError(data.error || `请求失败（${response.status}）`, response.status, data.code)
    if (
      response.status === 401 &&
      path !== '/api/admin/login' &&
      path !== '/api/admin/session'
    ) {
      unauthorizedHandler?.()
    }
    throw error
  }

  return payload as T
}
