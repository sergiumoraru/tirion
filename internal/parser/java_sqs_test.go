package parser

import "testing"

func TestJavaQueueProducerUsesLexicalSendInputs(t *testing.T) {
	p := javaQueueFixtureParser()
	result := p.ParseFile("Publisher.java", []byte(`
class Publisher {
  @QueueBinding(QueueNames.FIRST) String queue;
  void parameter(String queue) { client.sendMessage(queue); }
  void local(String input) { String queue = input; client.sendMessage(queue); }
  void explicitField(String queue) { client.sendMessage(this.queue); }
  void unrelated(String request) { log.info(queue); client.sendMessage(request); }
  void initializer(String body) {
    Request request = new Request().withQueueUrl(queue).withBody(body);
    client.sendMessage(request);
  }
  void reassigned(String replacement) {
    String selected = queue;
    selected = replacement;
    client.sendMessage(selected);
  }
  void annotated(@QueueBinding(QueueNames.SECOND) String queue) { client.sendMessage(queue); }
  void fieldAfterMethod() { client.sendMessage(queue); }
  void foreign(Publisher other) { client.sendMessage(other.queue); }
  void lambda() { values.forEach(queue -> client.sendMessage(queue)); }
}
`))
	for _, method := range []string{"parameter", "local", "unrelated", "reassigned", "foreign", "lambda"} {
		if got := result.SqsProducers["Publisher."+method]; len(got) != 0 {
			t.Fatalf("%s borrowed an unrelated queue: %#v", method, got)
		}
	}
	for method, queue := range map[string]string{"explicitField": "FIRST", "initializer": "FIRST", "annotated": "SECOND", "fieldAfterMethod": "FIRST"} {
		got := result.SqsProducers["Publisher."+method]
		if len(got) != 1 || got[0].QueueName != queue {
			t.Fatalf("%s = %#v, want %s", method, got, queue)
		}
	}
	total := 0
	for _, producers := range result.SqsProducers {
		total += len(producers)
	}
	if total != 4 {
		t.Fatalf("unexpected producer in nested or synthetic function: %#v", result.SqsProducers)
	}
}

func TestJavaQueueBindingsAreScopedToTheirClass(t *testing.T) {
	p := javaQueueFixtureParser()
	result := p.ParseFile("Publishers.java", []byte(`
class FirstPublisher {
  FirstPublisher(@QueueBinding(QueueNames.FIRST) String queue) { this.queue = queue; }
  void publish() { client.sendMessage(queue); }
}
class SecondPublisher {
  SecondPublisher(@QueueBinding(QueueNames.SECOND) String queue) { this.queue = queue; }
  void publish() { client.sendMessage(queue); }
}
class UnboundPublisher {
  void publish(String queue) { client.sendMessage(queue); }
}
`))
	for caller, queue := range map[string]string{"FirstPublisher.publish": "FIRST", "SecondPublisher.publish": "SECOND"} {
		got := result.SqsProducers[caller]
		if len(got) != 1 || got[0].QueueName != queue {
			t.Fatalf("%s: got %#v, want queue %s", caller, got, queue)
		}
	}
	if len(result.SqsProducers["UnboundPublisher.publish"]) != 0 {
		t.Fatal("unbound class inherited another class's queue")
	}
}

func TestJavaSqsStaticImportedQueueProducerExtraction(t *testing.T) {
	parser := javaQueueFixtureParser()
	source := []byte(`
import static example.queue.QueueNames.EVENT_UPDATES;

class EventPublisher {
  private final String eventQueue;

  @Inject
  EventPublisher(@QueueBinding(EVENT_UPDATES) String eventQueue) {
    this.eventQueue = eventQueue;
  }

  public void sendEventUpdate(Integer eventId, Integer resourceId) {
    sendEventMessage(eventId, resourceId, eventQueue);
  }
}
`)

	result := parser.ParseFile("EventPublisher.java", source)
	producers := result.SqsProducers["EventPublisher.sendEventUpdate"]
	if len(producers) != 1 {
		t.Fatalf("expected one SQS producer, got %#v", result.SqsProducers)
	}
	if producers[0].QueueName != "EVENT_UPDATES" {
		t.Fatalf("unexpected queue name: %#v", producers[0])
	}
}

func TestJavaSqsQueueVarReferenceDoesNotCreateProducerWithoutSendLikeCall(t *testing.T) {
	parser := javaQueueFixtureParser()
	source := []byte(`
import static example.queue.QueueNames.EVENT_UPDATES;

class EventPublisher {
  private final String eventQueue;

  @Inject
  EventPublisher(@QueueBinding(EVENT_UPDATES) String eventQueue) {
    this.eventQueue = eventQueue;
  }

  public void recordQueueName() {
    log.info("queue {}", eventQueue);
    metrics.tag(eventQueue);
  }
}
`)

	result := parser.ParseFile("EventPublisher.java", source)
	if len(result.SqsProducers) != 0 {
		t.Fatalf("expected no SQS producers, got %#v", result.SqsProducers)
	}
}

func TestJavaSqsRequestBuilderDoesNotCreateProducerWithoutSend(t *testing.T) {
	parser := javaQueueFixtureParser()
	source := []byte(`
import static example.queue.QueueNames.EVENT_UPDATES;

class EventPublisher {
  private final String eventQueue;

  @Inject
  EventPublisher(@QueueBinding(EVENT_UPDATES) String eventQueue) {
    this.eventQueue = eventQueue;
  }

  public SendMessageRequest buildRequest(String body) {
    return new SendMessageRequest()
        .withQueueUrl(eventQueue)
        .withMessageBody(body);
  }
}
`)

	result := parser.ParseFile("EventPublisher.java", source)
	if len(result.SqsProducers) != 0 {
		t.Fatalf("expected no SQS producers, got %#v", result.SqsProducers)
	}
}

func TestJavaSqsPrefixedAnnotationConsumerExtraction(t *testing.T) {
	parser := javaQueueFixtureParser()
	source := []byte(`
class ConsumeOperationalMessage implements MessageHandler<String> {
  @Inject
  ConsumeOperationalMessage(@QueueConfig.QueueBinding(QueueNames.OPERATIONAL_MESSAGE) final String queue) {
  }

  public boolean handle(String message) {
    return true;
  }
}
`)

	result := parser.ParseFile("ConsumeOperationalMessage.java", source)
	if len(result.SqsConsumers) != 1 {
		t.Fatalf("expected one SQS consumer, got %#v", result.SqsConsumers)
	}
	if result.SqsConsumers[0].QueueName != "OPERATIONAL_MESSAGE" {
		t.Fatalf("unexpected queue name: %#v", result.SqsConsumers[0])
	}
}

func TestJavaSqsBaseMessageHandlerAnnotationExtraction(t *testing.T) {
	parser := javaQueueFixtureParser()
	source := []byte(`
import static example.queue.QueueNames.EVENT_UPDATES;

class EventConsumer extends BaseMessageHandler <String> {
  @Inject
  EventConsumer(@QueueBinding(EVENT_UPDATES) String eventQueue) {
  }

  public boolean handle(String message) {
    return true;
  }

  public String getQueueUrl() {
    return "";
  }
}
`)

	result := parser.ParseFile("EventConsumer.java", source)
	if len(result.SqsConsumers) != 1 {
		t.Fatalf("expected one SQS consumer, got %#v", result.SqsConsumers)
	}
	if result.SqsConsumers[0].QueueName != "EVENT_UPDATES" {
		t.Fatalf("unexpected queue name: %#v", result.SqsConsumers[0])
	}
}

func TestJavaSqsConsumerQueueURLAssignmentExtraction(t *testing.T) {
	parser := javaQueueFixtureParser()
	source := []byte(`
class EventConsumer implements MessageHandler<String> {
  private String eventUpdatesQueue;

  @Inject
  EventConsumer(Boolean isProd) {
    if (Boolean.TRUE.equals(isProd)) {
      this.eventUpdatesQueue = QUEUE_URL + EVENT_UPDATES_QUEUE;
    } else {
      this.eventUpdatesQueue = QUEUE_URL + "test_" + EVENT_UPDATES_QUEUE;
    }
  }

  public boolean handle(String message) {
    return true;
  }
}
`)

	result := parser.ParseFile("EventConsumer.java", source)
	if len(result.SqsConsumers) != 1 {
		t.Fatalf("expected one SQS consumer, got %#v", result.SqsConsumers)
	}
	if result.SqsConsumers[0].QueueName != "EVENT_UPDATES" {
		t.Fatalf("unexpected queue name: %#v", result.SqsConsumers[0])
	}
	if result.SqsConsumers[0].ClassName != "EventConsumer" {
		t.Fatalf("unexpected class name: %#v", result.SqsConsumers[0])
	}
}

func TestJavaSqsConsumerQueueURLAssignmentIgnoresQueueNamedSettings(t *testing.T) {
	parser := javaQueueFixtureParser()
	source := []byte(`
class RetryConsumer implements MessageHandler<String> {
  private int maxQueueSize;

  @Inject
  RetryConsumer() {
    this.maxQueueSize = DEFAULT_RETRY_LIMIT;
  }

  public boolean handle(String message) {
    return true;
  }
}
`)

	result := parser.ParseFile("RetryConsumer.java", source)
	if len(result.SqsConsumers) != 0 {
		t.Fatalf("expected no SQS consumers, got %#v", result.SqsConsumers)
	}
}

func TestJavaSqsConsumerQueueURLAssignmentIgnoresOrdinaryConstants(t *testing.T) {
	parser := javaQueueFixtureParser()
	source := []byte(`
class RetryConsumer implements MessageHandler<String> {
  private int retryLimit;

  @Inject
  RetryConsumer() {
    this.retryLimit = DEFAULT_RETRY_LIMIT;
  }

  public boolean handle(String message) {
    return true;
  }
}
`)

	result := parser.ParseFile("RetryConsumer.java", source)
	if len(result.SqsConsumers) != 0 {
		t.Fatalf("expected no SQS consumers, got %#v", result.SqsConsumers)
	}
}
