package parser

import (
	"strings"
	"testing"
)

func TestCSharpParser_ControllerRoutesAndCalls(t *testing.T) {
	p := NewCSharpParser()
	content := []byte(`using Microsoft.AspNetCore.Mvc;

[ApiController]
[Route("api/[controller]")]
public class AccountsController : ControllerBase
{
    [HttpGet("{id}")]
    public IActionResult GetById(int id)
    {
        return Ok();
    }

    [HttpPost("login")]
    public IActionResult Login(LoginRequest req)
    {
        _authService.Validate(req);
        return Ok();
    }

    [Route("ping")]
    public IActionResult Ping() => Ok();
}
`)

	result := p.ParseFile("Controllers/AccountsController.cs", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if !hasFunction(result.Functions, "AccountsController.GetById") {
		t.Fatalf("expected AccountsController.GetById in parsed functions")
	}
	if !hasFunction(result.Functions, "AccountsController.Login") {
		t.Fatalf("expected AccountsController.Login in parsed functions")
	}
	if !hasEndpoint(result.Endpoints, "GET", "/api/Accounts/{id}") {
		t.Fatalf("expected GET /api/Accounts/{id}, got %#v", result.Endpoints)
	}
	if !hasEndpoint(result.Endpoints, "POST", "/api/Accounts/login") {
		t.Fatalf("expected POST /api/Accounts/login, got %#v", result.Endpoints)
	}
	if !hasEndpoint(result.Endpoints, "REQUEST", "/api/Accounts/ping") {
		t.Fatalf("expected REQUEST /api/Accounts/ping, got %#v", result.Endpoints)
	}

	loginCalls := result.FunctionCalls["AccountsController.Login"]
	if len(loginCalls) == 0 {
		t.Fatalf("expected function calls for AccountsController.Login")
	}
	foundValidate := false
	for _, call := range loginCalls {
		if call.CalleeName == "_authService.Validate" && call.MethodName == "Validate" {
			foundValidate = true
			break
		}
	}
	if !foundValidate {
		t.Fatalf("expected _authService.Validate call in AccountsController.Login, got %#v", loginCalls)
	}
}

func TestCSharpParser_ControllerRoutePrefix(t *testing.T) {
	p := NewCSharpParser()
	content := []byte(`using System.Web.Http;

[RoutePrefix("api/resources")]
public class ResourcesController : ApiController
{
    [HttpPost]
    [Route("Add")]
    public IHttpActionResult Add(ResourceRequest req)
    {
        return Ok();
    }

    [HttpGet]
    [Route("GetAll/{resourceId}")]
    public IHttpActionResult GetAll(int resourceId)
    {
        return Ok();
    }
}
`)

	result := p.ParseFile("Controllers/ResourcesController.cs", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if !hasEndpoint(result.Endpoints, "POST", "/api/resources/Add") {
		t.Fatalf("expected POST /api/resources/Add, got %#v", result.Endpoints)
	}
	if !hasEndpoint(result.Endpoints, "GET", "/api/resources/GetAll/{resourceId}") {
		t.Fatalf("expected GET /api/resources/GetAll/{resourceId}, got %#v", result.Endpoints)
	}
	if hasEndpoint(result.Endpoints, "POST", "/Add") {
		t.Fatalf("did not expect collapsed POST /Add endpoint, got %#v", result.Endpoints)
	}
	if hasEndpoint(result.Endpoints, "GET", "/GetAll/{resourceId}") {
		t.Fatalf("did not expect collapsed GET /GetAll/{resourceId} endpoint, got %#v", result.Endpoints)
	}
}

func TestCSharpParser_MinimalApiRoutes(t *testing.T) {
	p := NewCSharpParser()
	content := []byte(`app.MapGet("/health", () => "ok");
app.MapMethods("/v1/multi", new[] { "GET", "POST" }, HandleRequest);
`)

	result := p.ParseFile("Program.cs", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if !hasEndpoint(result.Endpoints, "GET", "/health") {
		t.Fatalf("expected GET /health, got %#v", result.Endpoints)
	}
	if !hasEndpoint(result.Endpoints, "GET", "/v1/multi") {
		t.Fatalf("expected GET /v1/multi, got %#v", result.Endpoints)
	}
	if !hasEndpoint(result.Endpoints, "POST", "/v1/multi") {
		t.Fatalf("expected POST /v1/multi, got %#v", result.Endpoints)
	}
}

func TestCSharpParser_AspxAndAspEndpoints(t *testing.T) {
	p := NewCSharpParser()

	aspx := p.ParseFile("web/account/login.aspx", []byte(`<%@ Page Language="C#" %>`))
	if aspx.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics for aspx: %#v", aspx.ParseDiagnostics)
	}
	if !hasEndpoint(aspx.Endpoints, "REQUEST", "/web/account/login.aspx") {
		t.Fatalf("expected REQUEST /web/account/login.aspx, got %#v", aspx.Endpoints)
	}

	asp := p.ParseFile("legacy/auth/logout.asp", []byte(`<% Response.Write("bye") %>`))
	if asp.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics for asp: %#v", asp.ParseDiagnostics)
	}
	if !hasEndpoint(asp.Endpoints, "REQUEST", "/legacy/auth/logout.asp") {
		t.Fatalf("expected REQUEST /legacy/auth/logout.asp, got %#v", asp.Endpoints)
	}
}

func TestCSharpParser_FieldTypesSourceDataAccessAndKafkaConsumer(t *testing.T) {
	p := NewCSharpParser()
	content := []byte(`using Confluent.Kafka;

public class QueueWorker : IQueueWorker
{
    private readonly IRecordRepository _records;
    private readonly TransformService _transform;
    private readonly IConsumer<string, string> _consumer;

    public QueueWorker()
    {
        string topicName = ConfigurationManager.AppSettings["RecordTopic"];
        _consumer.Subscribe(topicName);
    }

    public void StartAsync(CancellationToken cancellationToken)
    {
        var consumeResult = _consumer.Consume(cancellationToken);
        ProcessRecord(consumeResult);
    }

    private void ProcessRecord(ConsumeResult<string, string> consumeResult)
    {
        var record = _transform.Decode(consumeResult.Message.Value);
        _records.Update(record);
    }
}
`)

	result := p.ParseFile("Workers/QueueWorker.cs", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if len(result.Classes) != 1 {
		t.Fatalf("expected one class, got %#v", result.Classes)
	}
	if len(result.Classes[0].Fields) != 3 {
		t.Fatalf("expected parsed C# fields, got %#v", result.Classes[0].Fields)
	}
	if len(result.SqsConsumers) != 1 {
		t.Fatalf("expected Kafka consumer as queue consumer, got %#v", result.SqsConsumers)
	}
	if result.SqsConsumers[0].QueueName != "RecordTopic" || result.SqsConsumers[0].HandlerMethod != "StartAsync" {
		t.Fatalf("unexpected consumer extraction: %#v", result.SqsConsumers[0])
	}
	source := functionSource(result.Functions, "QueueWorker.ProcessRecord")
	if !strings.Contains(source, "_transform.Decode") || !strings.Contains(source, "_records.Update(record)") {
		t.Fatalf("expected full C# method source, got %q", source)
	}
}

func TestCSharpParser_ClassInheritanceDistinguishesConcreteIBaseNames(t *testing.T) {
	p := NewCSharpParser()
	content := []byte(`public class QueueWorker : IQueueWorker
{
}

public class IntegrationWorker : IntegrationBase, IHostedService
{
}

public class ItemController : ItemControllerBase, IDisposable
{
}
`)

	result := p.ParseFile("Services/IntegrationWorker.cs", content)
	classes := make(map[string]ParsedClass)
	for _, class := range result.Classes {
		classes[class.Name] = class
	}

	if classes["QueueWorker"].ExtendsClass != "" {
		t.Fatalf("interface-only class should not have ExtendsClass, got %#v", classes["QueueWorker"])
	}
	if !hasString(classes["QueueWorker"].Implements, "IQueueWorker") {
		t.Fatalf("expected IQueueWorker interface, got %#v", classes["QueueWorker"])
	}
	if classes["IntegrationWorker"].ExtendsClass != "IntegrationBase" {
		t.Fatalf("expected IntegrationBase as concrete base, got %#v", classes["IntegrationWorker"])
	}
	if !hasString(classes["IntegrationWorker"].Implements, "IHostedService") {
		t.Fatalf("expected IHostedService interface, got %#v", classes["IntegrationWorker"])
	}
	if classes["ItemController"].ExtendsClass != "ItemControllerBase" {
		t.Fatalf("expected ItemControllerBase as concrete base, got %#v", classes["ItemController"])
	}
	if !hasString(classes["ItemController"].Implements, "IDisposable") {
		t.Fatalf("expected IDisposable interface, got %#v", classes["ItemController"])
	}
}

func TestCSharpParser_DataAccessesFromEntityFrameworkAndSql(t *testing.T) {
	p := NewCSharpParser()
	content := []byte(`public class RecordRepository
{
    private AppDbContext _context;

    public Record Add(Record record)
    {
        _context.Records.Add(record);
        _context.SaveChanges();
        return record;
    }

    public Record Update(Record record)
    {
        var existing = _context.Records.Where(row => row.Id == record.Id).FirstOrDefault();
        var sql = "UPDATE app.RecordDetails SET Active = 1 WHERE Id = @id";
        _context.Database.ExecuteSqlCommand(sql, record.Id);
        return record;
    }
}
`)

	result := p.ParseFile("Data/RecordRepository.cs", content)
	addAccesses := result.DataAccesses["RecordRepository.Add"]
	if !hasDataAccessKind(addAccesses, "Records", "write") {
		t.Fatalf("expected Record write for Add, got %#v", addAccesses)
	}
	updateAccesses := result.DataAccesses["RecordRepository.Update"]
	if !hasDataAccessKind(updateAccesses, "Records", "read") || !hasDataAccessKind(updateAccesses, "RecordDetails", "write") {
		t.Fatalf("expected Record read and RecordDetails write for Update, got %#v", updateAccesses)
	}
}

func TestCSharpParser_DataAccessIgnoresNonDbLinqObjectGraphs(t *testing.T) {
	p := NewCSharpParser()
	content := []byte(`public class ResourceController
{
    public void Save(ResourceRequest request)
    {
        var activeItems = request.Items.Where(item => item.Active).ToList();
        var selectedNames = viewModel.Children.Select(child => child.Name).ToList();
    }

    public void Load()
    {
        var resources = _context.Set<Resource>().Where(resource => resource.Active).ToList();
    }
}
`)

	result := p.ParseFile("Controllers/ResourceController.cs", content)
	if accesses := result.DataAccesses["ResourceController.Save"]; len(accesses) != 0 {
		t.Fatalf("non-DB LINQ object graph produced data accesses: %#v", accesses)
	}
	if accesses := result.DataAccesses["ResourceController.Load"]; !hasDataAccessKind(accesses, "Resource", "read") {
		t.Fatalf("expected DbContext Set<T> read access, got %#v", accesses)
	}
}

func TestCSharpParser_KafkaConsumerUsesOwningClassAndHandler(t *testing.T) {
	p := NewCSharpParser()
	content := []byte(`using Confluent.Kafka;

public class UnrelatedListener
{
    public void StartAsync() {}
}

public class RecordListener
{
    private readonly IConsumer<string, string> _consumer;

    public RecordListener()
    {
        var topicName = ConfigurationManager.AppSettings["RecordTopic"];
        _consumer.Subscribe(topicName);
    }

    protected Task ExecuteAsync(CancellationToken stoppingToken)
    {
        return Task.CompletedTask;
    }
}
`)

	result := p.ParseFile("Workers/RecordListener.cs", content)
	if len(result.SqsConsumers) != 1 {
		t.Fatalf("expected one Kafka consumer, got %#v", result.SqsConsumers)
	}
	consumer := result.SqsConsumers[0]
	if consumer.ClassName != "RecordListener" || consumer.HandlerMethod != "ExecuteAsync" {
		t.Fatalf("consumer attached to wrong class/handler: %#v", consumer)
	}
}

func TestCSharpParser_KafkaConsumerSettingsAreScopedToOwningClass(t *testing.T) {
	p := NewCSharpParser()
	content := []byte(`using Confluent.Kafka;

public class FirstListener
{
    private readonly IConsumer<string, string> _consumer;
    private readonly string topicName = ConfigurationManager.AppSettings["FirstTopicName"];

    public void StartAsync()
    {
        _consumer.Subscribe(topicName);
    }
}

public class SecondListener
{
    private readonly IConsumer<string, string> _consumer;
    private readonly string topicName = ConfigurationManager.AppSettings["SecondTopicName"];

    public void StartAsync()
    {
        _consumer.Subscribe(topicName);
    }
}
`)

	result := p.ParseFile("Workers/MultiListener.cs", content)
	consumersByClass := make(map[string]ParsedSqsConsumer)
	for _, consumer := range result.SqsConsumers {
		consumersByClass[consumer.ClassName] = consumer
	}
	if consumersByClass["FirstListener"].QueueName != "FirstTopicName" {
		t.Fatalf("first listener used wrong topic: %#v", result.SqsConsumers)
	}
	if consumersByClass["SecondListener"].QueueName != "SecondTopicName" {
		t.Fatalf("second listener used wrong topic: %#v", result.SqsConsumers)
	}
}

func TestCSharpParser_EntityClassesFromGeneratedEntityFolder(t *testing.T) {
	p := NewCSharpParser()
	content := []byte(`namespace Application.Data.Entities
{
    [Table("Records")]
    public partial class Record
    {
        public decimal Id { get; set; }
    }
}
`)

	result := p.ParseFile("Data/Entities/Record.cs", content)
	if len(result.JpaEntities) != 1 {
		t.Fatalf("expected generated C# entity metadata, got %#v", result.JpaEntities)
	}
	if result.JpaEntities[0].ClassName != "Record" || result.JpaEntities[0].TableName != "Records" {
		t.Fatalf("unexpected entity metadata: %#v", result.JpaEntities[0])
	}
}

func hasFunction(functions []ParsedFunction, name string) bool {
	for _, fn := range functions {
		if fn.Name == name {
			return true
		}
	}
	return false
}

func functionSource(functions []ParsedFunction, name string) string {
	for _, fn := range functions {
		if fn.Name == name {
			return fn.SourceCode
		}
	}
	return ""
}

func hasDataAccessKind(accesses []ParsedDataAccess, entity string, access string) bool {
	for _, item := range accesses {
		if item.EntityName == entity && item.Access == access {
			return true
		}
	}
	return false
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasEndpoint(endpoints []ParsedEndpoint, method string, path string) bool {
	for _, ep := range endpoints {
		if ep.Method == method && ep.Path == path {
			return true
		}
	}
	return false
}
