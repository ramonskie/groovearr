package metadata

import (
	"slices"
	"testing"
)

// namedItem is a minimal Named for exercising the generic ordering helper
// without a full provider implementation.
type namedItem string

func (n namedItem) Name() string { return string(n) }

func TestProviderOrderCurrent(t *testing.T) {
	var nilOrder *ProviderOrder
	if got := nilOrder.Current(); got != nil {
		t.Errorf("nil receiver Current() = %v, want nil", got)
	}
	if got := NewProviderOrder(nil).Current(); got != nil {
		t.Errorf("unset getter Current() = %v, want nil", got)
	}
}

func TestOrder(t *testing.T) {
	tests := []struct {
		name  string
		order []string // nil => no ProviderOrder wired at all
		items []namedItem
		want  []namedItem
	}{
		{
			name:  "nil order keeps registration order",
			order: nil,
			items: []namedItem{"a", "b", "c"},
			want:  []namedItem{"a", "b", "c"},
		},
		{
			name:  "empty order keeps registration order",
			order: []string{},
			items: []namedItem{"a", "b"},
			want:  []namedItem{"a", "b"},
		},
		{
			name:  "listed items are ordered, the rest appended in place",
			order: []string{"c", "a"},
			items: []namedItem{"a", "b", "c"},
			want:  []namedItem{"c", "a", "b"},
		},
		{
			name:  "unknown names are ignored",
			order: []string{"missing", "b"},
			items: []namedItem{"a", "b"},
			want:  []namedItem{"b", "a"},
		},
		{
			name:  "empty items",
			order: []string{"a"},
			items: nil,
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var o *ProviderOrder
			if tt.order != nil {
				order := tt.order
				o = NewProviderOrder(func() []string { return order })
			}
			got := Order(o, tt.items)
			if !slices.Equal(got, tt.want) {
				t.Errorf("Order() = %v, want %v", got, tt.want)
			}
		})
	}
}
