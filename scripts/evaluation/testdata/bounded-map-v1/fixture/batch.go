package fixture

import "context"

// Map applies work and returns results in input order. This initial sequential
// implementation passes the public example but does not satisfy task.md.
func Map(ctx context.Context, input []int, limit int, work func(context.Context, int) (int, error)) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]int, len(input))
	for i, value := range input {
		result, err := work(ctx, value)
		if err != nil {
			return nil, err
		}
		out[i] = result
	}
	return out, nil
}
