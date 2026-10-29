package fixture

import (
	"context"
	"slices"
	"testing"
)

func TestPublicMapExample(t *testing.T) {
	got, err := Map(context.Background(), []int{1, 2, 3}, 2, func(_ context.Context, value int) (int, error) {
		return value * value, nil
	})
	if err != nil || !slices.Equal(got, []int{1, 4, 9}) {
		t.Fatalf("Map = %v, %v", got, err)
	}
}
