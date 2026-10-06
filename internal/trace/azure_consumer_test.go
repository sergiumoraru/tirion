package trace

import "testing"

func TestFindConsumerHandlerDoesNotReturnFunctionJSONConsumerIDForAzureWithoutPool(t *testing.T) {
	consumer := SqsConsumer{
		ConsumerID:  "resource-worker:functions/ProcessResource/function.json:ProcessResource",
		Repo:        "resource-worker",
		File:        "functions/ProcessResource/function.json",
		Class:       "ProcessResource",
		TriggerType: "queueTrigger",
	}

	if callerID := findConsumerHandler(nil, nil, consumer); callerID != "" {
		t.Fatalf("expected unresolved azure consumer without pool to return empty caller ID, got %q", callerID)
	}
}
