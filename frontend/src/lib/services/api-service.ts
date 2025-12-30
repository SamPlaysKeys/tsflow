const BASE_URL = '/api';

class APIError extends Error {
	constructor(
		public status: number,
		message: string
	) {
		super(message);
		this.name = 'APIError';
	}
}

async function request<T>(endpoint: string, options: RequestInit = {}): Promise<T> {
	const url = `${BASE_URL}${endpoint}`;

	const response = await fetch(url, {
		...options,
		headers: {
			'Content-Type': 'application/json',
			...options.headers
		}
	});

	if (!response.ok) {
		throw new APIError(response.status, `HTTP ${response.status}: ${response.statusText}`);
	}

	try {
		return await response.json();
	} catch {
		throw new APIError(response.status, 'Invalid JSON response from server');
	}
}

export const api = {
	get: <T>(endpoint: string) => request<T>(endpoint, { method: 'GET' }),
	post: <T>(endpoint: string, body: unknown) =>
		request<T>(endpoint, {
			method: 'POST',
			body: JSON.stringify(body)
		})
};

export { APIError };
