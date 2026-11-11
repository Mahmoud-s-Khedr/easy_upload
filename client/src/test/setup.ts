if (!globalThis.crypto) {
  throw new Error('Web Crypto API is required for client tests')
}
