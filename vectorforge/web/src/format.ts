export function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KiB', 'MiB', 'GiB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v < 10 ? 2 : 1)} ${units[i]}`
}

export function num(n: number, digits = 0): string {
  return n.toLocaleString(undefined, { minimumFractionDigits: digits, maximumFractionDigits: digits })
}

export function ms(n: number): string {
  if (n < 1) return `${(n * 1000).toFixed(0)} µs`
  if (n < 100) return `${n.toFixed(2)} ms`
  return `${n.toFixed(0)} ms`
}

export function micros(us: number): string {
  return us < 1000 ? `${us.toFixed(0)} µs` : `${(us / 1000).toFixed(2)} ms`
}

export function unixTime(seconds: number): string {
  return seconds ? new Date(seconds * 1000).toLocaleString() : '—'
}

export function pct(x: number, digits = 1): string {
  return `${(x * 100).toFixed(digits)}%`
}
