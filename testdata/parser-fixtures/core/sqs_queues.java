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

  public void recordQueueName() {
    log.info("queue {}", eventQueue);
    metrics.tag(eventQueue);
  }

  public SendMessageRequest buildRequest(String body) {
    return new SendMessageRequest()
        .withQueueUrl(eventQueue)
        .withMessageBody(body);
  }
}

class PrefixedConsumer implements MessageHandler<String> {
  @Inject
  PrefixedConsumer(@QueueConfig.QueueBinding(QueueNames.OPERATIONAL_MESSAGE) final String queue) {
  }

  public boolean handle(String message) {
    return true;
  }
}

class QueueUrlAssignmentConsumer implements MessageHandler<String> {
  private String eventUpdatesQueue;

  @Inject
  QueueUrlAssignmentConsumer(Boolean isProd) {
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

class BaseEventConsumer extends BaseMessageHandler<String> {
  @Inject
  BaseEventConsumer(@QueueBinding(EVENT_UPDATES) String eventQueue) {
  }

  public boolean handle(String message) {
    return true;
  }

  public String getQueueUrl() {
    return "";
  }
}

class RetryConsumer implements MessageHandler<String> {
  private int retryLimit;
  private int maxQueueSize;

  @Inject
  RetryConsumer() {
    this.retryLimit = DEFAULT_RETRY_LIMIT;
    this.maxQueueSize = DEFAULT_RETRY_LIMIT;
  }

  public boolean handle(String message) {
    return true;
  }
}
