package parser

import "github.com/sergiumoraru/tirion/internal/config"

func javaQueueFixtureParser() *JavaParser {
	p := NewJavaParser()
	cfg := config.MergePatterns(nil)
	cfg.JavaSQS = config.JavaSQSFramework{
		QueueAnnotation: "QueueBinding", QueueEnum: "QueueNames",
		ConsumerTypes: []string{"MessageHandler", "BaseMessageHandler"},
		HandlerMethod: "handle", ConstantSuffix: "_QUEUE",
		QueueURLFields: []string{"eventUpdatesQueue"}, SendMethods: []string{"sendEventMessage"},
	}
	p.SetConfig(cfg)
	return p
}

func configuredQueueFixtureParser() *JavaScriptParser {
	p := NewJavaScriptParser()
	cfg := config.MergePatterns(nil)
	cfg.JavaScriptQueues = config.JavaScriptQueuePatterns{
		LoadReceivers: []string{"serviceBus", "publisher"},
		ReadMethods:   []string{"getMessagesByQuery"},
		DeleteMethods: []string{"purgeMessagesByMessageId"},
	}
	p.SetConfig(cfg)
	return p
}

func graphqlRegistrationFixtureParser(pathKey string) *JavaScriptParser {
	p := NewJavaScriptParser()
	cfg := config.MergePatterns(nil)
	cfg.GraphQLRegistrations = []config.GraphQLRegistration{{Receiver: "registry", Method: "register", ControllersPathKey: pathKey}}
	p.SetConfig(cfg)
	return p
}

func configuredHTTPFixtureParser() *JavaScriptParser {
	p := NewJavaScriptParser()
	cfg := config.MergePatterns(nil)
	cfg.HttpClients = append(cfg.HttpClients,
		config.HttpClientPattern{Name: "form", Language: "javascript", Objects: []string{"submitForm"}, Methods: map[string]string{"": "POST"}},
		config.HttpClientPattern{Name: "resource-client", Language: "javascript", Objects: []string{"this._api"}, Methods: map[string]string{"_post": "POST", "_getall": "GET"}},
	)
	cfg.JavaScriptQueues.LoadReceivers = []string{"serviceBus"}
	p.SetConfig(cfg)
	return p
}
