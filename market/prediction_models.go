package market

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
)

// ModelSpec is deliberately small and serializable. Boosting uses regression
// stumps (depth-one trees), not XGBoost or a hidden external dependency.
type ModelSpec struct {
	Name         string  `json:"name"`
	Window       int     `json:"window,omitempty"`
	Alpha        float64 `json:"alpha,omitempty"`
	Rounds       int     `json:"rounds,omitempty"`
	LearningRate float64 `json:"learning_rate,omitempty"`
}

func candidateModels() []ModelSpec {
	return []ModelSpec{{Name: "persistence"}, {Name: "moving-average", Window: 5}, {Name: "moving-average", Window: 20}, {Name: "ridge", Alpha: .01}, {Name: "ridge", Alpha: .1}, {Name: "ridge", Alpha: 1}, {Name: "gradient-boosted-stumps", Rounds: 32, LearningRate: .05}, {Name: "gradient-boosted-stumps", Rounds: 64, LearningRate: .05}}
}

type RegressionStump struct {
	Feature   int     `json:"feature"`
	Threshold float64 `json:"threshold"`
	Left      float64 `json:"left"`
	Right     float64 `json:"right"`
}

// FittedPredictor contains everything required for offline replay.
type FittedPredictor struct {
	Version   string            `json:"version"`
	Spec      ModelSpec         `json:"spec"`
	Mean      []float64         `json:"mean,omitempty"`
	Scale     []float64         `json:"scale,omitempty"`
	Weights   []float64         `json:"weights,omitempty"`
	Intercept float64           `json:"intercept"`
	Stumps    []RegressionStump `json:"stumps,omitempty"`
}

func fitPredictor(ctx context.Context, spec ModelSpec, rows []predictionSample) (FittedPredictor, error) {
	m := FittedPredictor{Version: PredictionVersion, Spec: spec}
	if len(rows) < 2 {
		return m, ErrInsufficientHistory
	}
	if err := ctx.Err(); err != nil {
		return m, err
	}
	switch spec.Name {
	case "persistence":
		return m, nil
	case "moving-average":
		if spec.Window != 5 && spec.Window != 20 {
			return m, errors.New("moving-average window must be 5 or 20")
		}
		return m, nil
	case "ridge":
		if spec.Alpha <= 0 || !finite(spec.Alpha) {
			return m, errors.New("ridge alpha must be positive")
		}
		p := len(predictionFeatureNames)
		m.Mean = make([]float64, p)
		m.Scale = make([]float64, p)
		for _, row := range rows {
			m.Intercept += row.Y
			for j, x := range row.X {
				m.Mean[j] += x
			}
		}
		m.Intercept /= float64(len(rows))
		for j := range m.Mean {
			m.Mean[j] /= float64(len(rows))
		}
		for _, row := range rows {
			for j, x := range row.X {
				d := x - m.Mean[j]
				m.Scale[j] += d * d
			}
		}
		for j := range m.Scale {
			m.Scale[j] = math.Sqrt(m.Scale[j] / float64(len(rows)))
			if m.Scale[j] < 1e-12 {
				m.Scale[j] = 1
			}
		}
		a := make([][]float64, p)
		for j := range a {
			a[j] = make([]float64, p+1)
		}
		for _, row := range rows {
			for j := 0; j < p; j++ {
				xj := (row.X[j] - m.Mean[j]) / m.Scale[j]
				a[j][p] += xj * (row.Y - m.Intercept) / float64(len(rows))
				for k := 0; k < p; k++ {
					a[j][k] += xj * (row.X[k] - m.Mean[k]) / m.Scale[k] / float64(len(rows))
				}
			}
		}
		for j := 0; j < p; j++ {
			a[j][j] += spec.Alpha
		}
		var err error
		m.Weights, err = solveLinear(a)
		return m, err
	case "gradient-boosted-stumps":
		if spec.Rounds < 1 || spec.Rounds > 128 || spec.LearningRate <= 0 || spec.LearningRate > 1 {
			return m, errors.New("invalid boosting parameters")
		}
		for _, row := range rows {
			m.Intercept += row.Y
		}
		m.Intercept /= float64(len(rows))
		pred := make([]float64, len(rows))
		for i := range pred {
			pred[i] = m.Intercept
		}
		orders := make([][]int, len(predictionFeatureNames))
		for j := range orders {
			orders[j] = make([]int, len(rows))
			for i := range rows {
				orders[j][i] = i
			}
			sort.SliceStable(orders[j], func(a, b int) bool { return rows[orders[j][a]].X[j] < rows[orders[j][b]].X[j] })
		}
		for round := 0; round < spec.Rounds; round++ {
			if err := ctx.Err(); err != nil {
				return m, err
			}
			residual := make([]float64, len(rows))
			var sum, sq float64
			for i, row := range rows {
				residual[i] = row.Y - pred[i]
				sum += residual[i]
				sq += residual[i] * residual[i]
			}
			bestLoss := sq
			best := RegressionStump{Feature: -1}
			for j, order := range orders {
				var left, leftSq float64
				stride := max(1, len(rows)/32)
				for k, idx := range order {
					left += residual[idx]
					leftSq += residual[idx] * residual[idx]
					n := k + 1
					if n < 5 || len(rows)-n < 5 || n%stride != 0 || rows[idx].X[j] == rows[order[k+1]].X[j] {
						continue
					}
					right := sum - left
					loss := leftSq - left*left/float64(n) + (sq - leftSq) - right*right/float64(len(rows)-n)
					if loss < bestLoss-1e-15 {
						bestLoss = loss
						best = RegressionStump{j, (rows[idx].X[j] + rows[order[k+1]].X[j]) / 2, spec.LearningRate * left / float64(n), spec.LearningRate * right / float64(len(rows)-n)}
					}
				}
			}
			if best.Feature < 0 {
				break
			}
			m.Stumps = append(m.Stumps, best)
			for i, row := range rows {
				if row.X[best.Feature] <= best.Threshold {
					pred[i] += best.Left
				} else {
					pred[i] += best.Right
				}
			}
		}
		return m, nil
	default:
		return m, fmt.Errorf("unknown prediction model %q", spec.Name)
	}
}

// PredictLogReturn cannot receive a target label or future price.
func (m FittedPredictor) PredictLogReturn(features []float64, closePrice, sma5, sma20 int64) (float64, error) {
	if m.Version != PredictionVersion || len(features) != len(predictionFeatureNames) || closePrice <= 0 {
		return 0, errors.New("invalid predictor version or inputs")
	}
	for _, x := range features {
		if !finite(x) {
			return 0, errors.New("non-finite predictor input")
		}
	}
	y := m.Intercept
	switch m.Spec.Name {
	case "persistence":
		y = 0
	case "moving-average":
		avg := sma5
		if m.Spec.Window == 20 {
			avg = sma20
		} else if m.Spec.Window != 5 {
			return 0, errors.New("invalid moving-average artifact")
		}
		if avg <= 0 {
			return 0, errors.New("invalid moving average")
		}
		y = math.Log(float64(avg) / float64(closePrice))
	case "ridge":
		if len(m.Weights) != len(features) || len(m.Mean) != len(features) || len(m.Scale) != len(features) {
			return 0, errors.New("invalid ridge artifact")
		}
		for j, x := range features {
			if m.Scale[j] <= 0 {
				return 0, errors.New("invalid ridge scale")
			}
			y += m.Weights[j] * (x - m.Mean[j]) / m.Scale[j]
		}
	case "gradient-boosted-stumps":
		for _, s := range m.Stumps {
			if s.Feature < 0 || s.Feature >= len(features) || !finite(s.Threshold) {
				return 0, errors.New("invalid stump artifact")
			}
			if features[s.Feature] <= s.Threshold {
				y += s.Left
			} else {
				y += s.Right
			}
		}
	default:
		return 0, errors.New("unknown predictor artifact")
	}
	if !finite(y) {
		return 0, errors.New("non-finite prediction")
	}
	return math.Max(-.5, math.Min(.5, y)), nil
}

func predictSample(m FittedPredictor, s predictionSample) (float64, error) {
	return m.PredictLogReturn(s.X, s.Close, s.SMA5, s.SMA20)
}

func solveLinear(a [][]float64) ([]float64, error) {
	n := len(a)
	for col := 0; col < n; col++ {
		pivot := col
		for i := col + 1; i < n; i++ {
			if math.Abs(a[i][col]) > math.Abs(a[pivot][col]) {
				pivot = i
			}
		}
		if math.Abs(a[pivot][col]) < 1e-14 {
			return nil, errors.New("singular ridge system")
		}
		a[col], a[pivot] = a[pivot], a[col]
		v := a[col][col]
		for j := col; j <= n; j++ {
			a[col][j] /= v
		}
		for i := 0; i < n; i++ {
			if i == col {
				continue
			}
			v = a[i][col]
			for j := col; j <= n; j++ {
				a[i][j] -= v * a[col][j]
			}
		}
	}
	w := make([]float64, n)
	for i := range w {
		w[i] = a[i][n]
		if !finite(w[i]) {
			return nil, errors.New("non-finite ridge weight")
		}
	}
	return w, nil
}
