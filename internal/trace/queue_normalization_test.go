package trace

import (
	"reflect"
	"testing"
)

func TestNormalizeQueueName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "arn form",
			in:   "arn:aws:sqs:us-east-1:123456789012:my-queue",
			want: "my-queue",
		},
		{
			name: "url form",
			in:   "https://sqs.us-east-1.amazonaws.com/123456789012/my-queue.fifo",
			want: "my-queue.fifo",
		},
		{
			name: "slash separated form",
			in:   "tenant/prod/my-queue",
			want: "my-queue",
		},
		{
			name: "trim spaces",
			in:   "  my-queue  ",
			want: "my-queue",
		},
		{
			name: "azure app setting placeholder",
			in:   "%APP_PUBLISH_DOCUMENT_QUEUE%",
			want: "APP_PUBLISH_DOCUMENT_QUEUE",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeQueueName(tt.in)
			if got != tt.want {
				t.Fatalf("normalizeQueueName(%q): got %q want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestQueueNameVariants_NormalizedAndDeterministic(t *testing.T) {
	t.Parallel()

	got := queueNameVariants("ACME-orders.FIFO", []string{"acme-", "prod-"})
	want := []string{"acme-orders", "acme-orders.fifo", "orders", "orders.fifo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("queueNameVariants mismatch:\n  got:  %#v\n  want: %#v", got, want)
	}
}

func TestQueueNameVariants_IncludeAzurePlaceholderVariant(t *testing.T) {
	t.Parallel()

	got := queueNameVariants("APP_PUBLISH_DOCUMENT_QUEUE", nil)
	want := []string{"%app_publish_document_queue%", "app_publish_document_queue"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("queueNameVariants mismatch:\n  got:  %#v\n  want: %#v", got, want)
	}
}
