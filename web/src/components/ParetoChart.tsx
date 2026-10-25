import { useMemo, useState } from 'react';

export type Point = {
  id: string;
  x: number;
  y: number;
  metrics: Record<string, number>;
  isParetoOptimal?: boolean;
};

interface Props {
  points: Point[];
  xAxisLabel: string;
  yAxisLabel: string;
}

export default function ParetoChart({ points, xAxisLabel, yAxisLabel }: Props) {
  const [hoveredPoint, setHoveredPoint] = useState<Point | null>(null);

  const { width, height, margin } = { width: 500, height: 350, margin: 40 };
  const innerWidth = width - margin * 2;
  const innerHeight = height - margin * 2;

  const { minX, maxX, minY, maxY, sortedPareto } = useMemo(() => {
    if (!points || points.length === 0) return { minX: 0, maxX: 10, minY: 0, maxY: 10, sortedPareto: [] };
    
    const xs = points.map(p => p.x);
    const ys = points.map(p => p.y);
    const xMin = Math.min(...xs);
    const xMax = Math.max(...xs);
    const yMin = Math.min(...ys);
    const yMax = Math.max(...ys);

    // X margin to prevent dots on axis
    const xPad = (xMax - xMin) * 0.1 || 1;
    const yPad = (yMax - yMin) * 0.1 || 1;

    const pareto = points.filter(p => p.isParetoOptimal).sort((a, b) => a.x - b.x);

    return { 
      minX: xMin - xPad, 
      maxX: xMax + xPad, 
      minY: yMin - yPad, 
      maxY: yMax + yPad,
      sortedPareto: pareto
    };
  }, [points]);

  const mapX = (x: number) => margin + ((x - minX) / (maxX - minX)) * innerWidth;
  const mapY = (y: number) => height - margin - ((y - minY) / (maxY - minY)) * innerHeight; // Y is flipped in SVG

  const paretoPath = useMemo(() => {
    if (sortedPareto.length === 0) return '';
    // Use the inner mapped functions inside useMemo to avoid dependency array issues
    const mx = (x: number) => margin + ((x - minX) / (maxX - minX)) * innerWidth;
    const my = (y: number) => height - margin - ((y - minY) / (maxY - minY)) * innerHeight;
    return sortedPareto.map((p, i) => `${i === 0 ? 'M' : 'L'} ${mx(p.x)} ${my(p.y)}`).join(' ');
  }, [sortedPareto, minX, maxX, minY, maxY, innerWidth, innerHeight, margin, height]);

  return (
    <div data-testid="pareto-chart" style={{ position: 'relative', width: `${width}px`, height: `${height}px` }}>
      <svg width={width} height={height} style={{ border: '1px solid #ddd', background: '#fff' }}>
        {/* Axes */}
        <line x1={margin} y1={height - margin} x2={width - margin} y2={height - margin} stroke="#ccc" />
        <line x1={margin} y1={margin} x2={margin} y2={height - margin} stroke="#ccc" />
        
        {/* Labels */}
        <text x={width / 2} y={height - 10} textAnchor="middle" fontSize="12" fill="#666">{xAxisLabel}</text>
        <text x={15} y={height / 2} textAnchor="middle" fontSize="12" fill="#666" transform={`rotate(-90 15 ${height / 2})`}>{yAxisLabel}</text>

        {/* Pareto Frontier Line */}
        {paretoPath && <path d={paretoPath} fill="none" stroke="#ff9800" strokeWidth="2" />}

        {/* Points */}
        {points.map(p => {
          const cx = mapX(p.x);
          const cy = mapY(p.y);
          return (
            <circle
              key={p.id}
              cx={cx}
              cy={cy}
              r={p.isParetoOptimal ? 5 : 4}
              fill={p.isParetoOptimal ? "#ff9800" : "#2196f3"}
              stroke="#fff"
              strokeWidth="1"
              onMouseEnter={() => setHoveredPoint(p)}
              onMouseLeave={() => setHoveredPoint(null)}
              style={{ cursor: 'pointer', transition: 'r 0.1s' }}
            />
          );
        })}
      </svg>

      {hoveredPoint && (
        <div style={{
          position: 'absolute',
          left: `${mapX(hoveredPoint.x) + 10}px`,
          top: `${mapY(hoveredPoint.y) - 10}px`,
          background: 'rgba(0,0,0,0.8)',
          color: 'white',
          padding: '8px',
          borderRadius: '4px',
          fontSize: '12px',
          pointerEvents: 'none',
          zIndex: 10
        }}>
          <strong>{hoveredPoint.id}</strong>
          <div>{xAxisLabel}: {hoveredPoint.x.toFixed(2)}</div>
          <div>{yAxisLabel}: {hoveredPoint.y.toFixed(2)}</div>
          {hoveredPoint.isParetoOptimal && <div style={{ color: '#ffb74d' }}>Pareto Optimal</div>}
          <div style={{ marginTop: '4px', borderTop: '1px solid #555', paddingTop: '4px' }}>
            {Object.entries(hoveredPoint.metrics).map(([k, v]) => (
              <div key={k}>{k}: {v.toFixed(2)}</div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
