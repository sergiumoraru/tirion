package parser

import (
	"strings"
	"testing"
)

func TestJavaNestedDeclarationsKeepTheirOwnMetadata(t *testing.T) {
	source := `class A {
  @Configuration @Entity @Getter static class Builder<T> {
    String alpha;
    @ManyToOne Builder parent;
    @Bean String build() { return alpha(); }
    String alpha() { return alpha; }
    @Scheduled(fixedRate=1) void tick() {}
    @EventListener void changed(Change event) {}
  }
}
class B {
  @Entity @Getter static class Builder<U> {
    int beta;
    @ManyToOne Builder parent;
    @Bean String build() { return beta(); }
    String beta() { return "beta"; }
    @Scheduled(fixedRate=2) void tick() {}
    @EventListener void changed(Change event) {}
  }
}`
	r := NewJavaParser().ParseFile("Builders.java", []byte(source))
	if r.ParseDiagnostics.FailureKind != "" {
		t.Fatal(r.ParseDiagnostics)
	}
	classes := map[string]ParsedClass{}
	for _, cls := range r.Classes {
		classes[cls.Name] = cls
	}
	for owner, field := range map[string]string{"A.Builder": "alpha", "B.Builder": "beta"} {
		cls, ok := classes[owner]
		if !ok || len(cls.Fields) != 2 || cls.Fields[0].Name != field || len(cls.TypeParameters) != 1 {
			t.Fatalf("wrong declaration metadata for %s: %+v", owner, cls)
		}
		calls := r.FunctionCalls[owner+".build"]
		if len(calls) != 1 || calls[0].CalleeName != field {
			t.Fatalf("wrong calls for %s: %+v", owner, calls)
		}
	}
	// B.Builder has a @Bean method but is not a @Configuration class.
	if len(r.BeanDefinitions) != 1 || r.BeanDefinitions[0].ConfigClassName != "A.Builder" || r.BeanDefinitions[0].MethodName != "build" {
		t.Fatalf("configuration leaked between same-named classes: %+v", r.BeanDefinitions)
	}
	if len(r.ScheduledMethods) != 2 || r.ScheduledMethods[0].ClassName != "A.Builder" || r.ScheduledMethods[1].ClassName != "B.Builder" {
		t.Fatalf("wrong scheduled owners: %+v", r.ScheduledMethods)
	}
	if len(r.EventListeners) != 2 || r.EventListeners[0].ClassName != "A.Builder" || r.EventListeners[1].ClassName != "B.Builder" {
		t.Fatalf("wrong listener owners: %+v", r.EventListeners)
	}
	if len(r.JpaEntities) != 2 || len(r.JpaRelationships) != 2 {
		t.Fatalf("missing JPA facts: %+v / %+v", r.JpaEntities, r.JpaRelationships)
	}
	for i, owner := range []string{"A.Builder", "B.Builder"} {
		if r.JpaEntities[i].ClassName != owner || r.JpaEntities[i].TableName != "Builder" || r.JpaRelationships[i].SourceEntity != owner || r.JpaRelationships[i].TargetEntity != owner {
			t.Fatalf("wrong JPA ownership: %+v / %+v", r.JpaEntities[i], r.JpaRelationships[i])
		}
	}
	for _, method := range r.SyntheticMethods {
		if method.ClassName != "A.Builder" && method.ClassName != "B.Builder" {
			t.Fatalf("wrong synthetic method owner: %+v", method)
		}
	}
}

func TestJavaSameLineMemberAndLocalTypesHaveDistinctIdentities(t *testing.T) {
	source := `class A { static class Builder {} interface Port { void send(); } static class Sender implements Port { public void send() {} } record Item(int id) {} }
class B { static class Builder {} interface Port { void send(); } static class Sender implements Port { public void send() {} } record Item(int id) {} }
class C { void run() { { class Builder {} } { class Builder {} } } }`
	r := NewJavaParser().ParseFile("Types.java", []byte(source))
	seen := map[string]bool{}
	locals := 0
	for _, cls := range r.Classes {
		if seen[cls.Name] {
			t.Fatalf("duplicate class identity: %s", cls.Name)
		}
		seen[cls.Name] = true
		if strings.HasPrefix(cls.Name, "C.$local@") {
			locals++
		}
	}
	for _, name := range []string{"A.Builder", "B.Builder", "A.Item", "B.Item"} {
		if !seen[name] {
			t.Fatalf("missing scoped declaration %s", name)
		}
	}
	if locals != 2 || len(r.Interfaces) != 2 || r.Interfaces[0].Name != "A.Port" || r.Interfaces[1].Name != "B.Port" {
		t.Fatalf("wrong local/interface identities: %v / %+v", seen, r.Interfaces)
	}
}
