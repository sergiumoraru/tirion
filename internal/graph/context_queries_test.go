package graph

import "testing"

func TestParseAzureServiceBusResourceNameSubscriptionPath(t *testing.T) {
	queue, topic, subscription := parseAzureServiceBusResourceName("resource-events/subscriptions/resource-worker")
	if queue != "" {
		t.Fatalf("expected no queue name, got %q", queue)
	}
	if topic != "resource-events" || subscription != "resource-worker" {
		t.Fatalf("unexpected parsed subscription resource: topic=%q subscription=%q", topic, subscription)
	}
}

func TestParseAzureServiceBusResourceNameQueue(t *testing.T) {
	queue, topic, subscription := parseAzureServiceBusResourceName("%RESOURCE_TASKS_QUEUE%")
	if queue != "%RESOURCE_TASKS_QUEUE%" {
		t.Fatalf("unexpected queue name: %q", queue)
	}
	if topic != "" || subscription != "" {
		t.Fatalf("expected queue resource to have no topic/subscription, got topic=%q subscription=%q", topic, subscription)
	}
}
