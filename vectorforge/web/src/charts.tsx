export interface Point {
  x: number
  y: number
  label?: string
}

export interface Series {
  name: string
  points: Point[]
}

const W = 640
const H = 300
const M = { top: 16, right: 16, bottom: 44, left: 56 }

function niceTicks(lo: number, hi: number, count = 5): number[] {
  if (hi <= lo) return [lo]
  const raw = (hi - lo) / count
  const mag = 10 ** Math.floor(Math.log10(raw))
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw) ?? raw
  const out: number[] = []
  for (let v = Math.ceil(lo / step) * step; v <= hi + step * 1e-9; v += step) out.push(+v.toPrecision(12))
  return out
}

function logTicks(lo: number, hi: number): number[] {
  const out: number[] = []
  for (let e = Math.floor(Math.log10(lo)); e <= Math.ceil(Math.log10(hi)); e++) {
    for (const m of [1, 2, 5]) {
      const v = m * 10 ** e
      if (v >= lo && v <= hi) out.push(v)
    }
  }
  return out
}

interface LineChartProps {
  series: Series[]
  xLabel: string
  yLabel: string
  logX?: boolean
  yDomain?: [number, number]
  formatX?: (x: number) => string
  formatY?: (y: number) => string
  title?: string
}

export function LineChart({ series, xLabel, yLabel, logX, yDomain, formatX = String, formatY = String, title }: LineChartProps) {
  const pts = series.flatMap((s) => s.points)
  if (pts.length === 0) return <p className="muted">No data.</p>
  let [x0, x1] = [Math.min(...pts.map((p) => p.x)), Math.max(...pts.map((p) => p.x))]
  let [y0, y1] = yDomain ?? [Math.min(...pts.map((p) => p.y)), Math.max(...pts.map((p) => p.y))]
  if (x0 === x1) [x0, x1] = logX ? [x0 / 2, x1 * 2] : [x0 - 1, x1 + 1]
  if (y0 === y1) [y0, y1] = [y0 - 1, y1 + 1]
  if (logX) [x0, x1] = [x0 / 1.15, x1 * 1.15]
  const pw = W - M.left - M.right
  const ph = H - M.top - M.bottom
  const sx = (x: number) =>
    M.left + (logX ? (Math.log(x) - Math.log(x0)) / (Math.log(x1) - Math.log(x0)) : (x - x0) / (x1 - x0)) * pw
  const sy = (y: number) => M.top + (1 - (y - y0) / (y1 - y0)) * ph
  const xt = logX ? logTicks(x0, x1) : niceTicks(x0, x1)
  const yt = niceTicks(y0, y1)
  return (
    <figure className="chart">
      {title && <figcaption>{title}</figcaption>}
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={title ?? `${yLabel} vs ${xLabel}`}>
        {yt.map((v) => (
          <g key={`y${v}`}>
            <line className="grid" x1={M.left} x2={W - M.right} y1={sy(v)} y2={sy(v)} />
            <text className="tick" x={M.left - 6} y={sy(v)} textAnchor="end" dominantBaseline="middle">
              {formatY(v)}
            </text>
          </g>
        ))}
        {xt.map((v) => (
          <g key={`x${v}`}>
            <line className="grid" x1={sx(v)} x2={sx(v)} y1={M.top} y2={H - M.bottom} />
            <text className="tick" x={sx(v)} y={H - M.bottom + 16} textAnchor="middle">
              {formatX(v)}
            </text>
          </g>
        ))}
        <text className="axis-label" x={M.left + pw / 2} y={H - 6} textAnchor="middle">
          {xLabel}
        </text>
        <text className="axis-label" transform={`translate(14 ${M.top + ph / 2}) rotate(-90)`} textAnchor="middle">
          {yLabel}
        </text>
        {series.map((s, i) => {
          const sorted = [...s.points].sort((a, b) => a.x - b.x)
          return (
            <g key={s.name} className={`series series-${i % 8}`}>
              <polyline points={sorted.map((p) => `${sx(p.x)},${sy(p.y)}`).join(' ')} />
              {sorted.map((p, j) => (
                <circle key={j} cx={sx(p.x)} cy={sy(p.y)} r={3.5}>
                  <title>{`${s.name}${p.label ? ` · ${p.label}` : ''}\n${xLabel}: ${formatX(p.x)}\n${yLabel}: ${formatY(p.y)}`}</title>
                </circle>
              ))}
            </g>
          )
        })}
      </svg>
      <ul className="legend">
        {series.map((s, i) => (
          <li key={s.name} className={`series-${i % 8}`}>
            <span className="swatch" />
            {s.name}
          </li>
        ))}
      </ul>
    </figure>
  )
}

export function ListSizeChart({ sizes, mean }: { sizes: number[]; mean: number }) {
  const sorted = [...sizes].sort((a, b) => b - a)
  const max = Math.max(1, ...sorted)
  const pw = W - M.left - M.right
  const ph = H - M.top - M.bottom
  const bw = pw / sorted.length
  const sy = (v: number) => M.top + (1 - v / max) * ph
  return (
    <figure className="chart">
      <figcaption>Vectors per inverted list (sorted)</figcaption>
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label="Inverted list sizes">
        {niceTicks(0, max).map((v) => (
          <g key={v}>
            <line className="grid" x1={M.left} x2={W - M.right} y1={sy(v)} y2={sy(v)} />
            <text className="tick" x={M.left - 6} y={sy(v)} textAnchor="end" dominantBaseline="middle">
              {v}
            </text>
          </g>
        ))}
        {sorted.map((v, i) => (
          <rect key={i} className="bar" x={M.left + i * bw + bw * 0.1} width={Math.max(bw * 0.8, 0.5)} y={sy(v)} height={sy(0) - sy(v)}>
            <title>{`${v} vectors`}</title>
          </rect>
        ))}
        <line className="mean-line" x1={M.left} x2={W - M.right} y1={sy(mean)} y2={sy(mean)} />
        <text className="tick" x={W - M.right} y={sy(mean) - 5} textAnchor="end">
          mean {mean.toFixed(0)}
        </text>
        <text className="axis-label" x={M.left + pw / 2} y={H - 6} textAnchor="middle">
          {sorted.length} lists, largest to smallest
        </text>
      </svg>
    </figure>
  )
}
