package parser

import "testing"

func TestJavaCriteriaDataAccessFrom(t *testing.T) {
	p := NewJavaParser()
	source := []byte(`
import org.hibernate.Session;
import jakarta.persistence.criteria.CriteriaBuilder;
import jakarta.persistence.criteria.CriteriaQuery;
import jakarta.persistence.criteria.Root;

class Resource {}

class ResourceSpecification {
  static void findByProjection(Session session) {
    CriteriaBuilder criteriaBuilder = session.getCriteriaBuilder();
    CriteriaQuery<Object[]> criteriaQuery = criteriaBuilder.createQuery(Object[].class);
    Root<Resource> resourceRoot = criteriaQuery.from(Resource.class);
    session.createQuery(criteriaQuery).getResultList();
  }
}
`)

	result := p.ParseFile("ResourceSpecification.java", source)
	accesses := result.DataAccesses["ResourceSpecification.findByProjection"]
	if len(accesses) != 1 {
		t.Fatalf("expected 1 criteria data access, got %d: %#v", len(accesses), accesses)
	}
	if accesses[0].EntityName != "Resource" {
		t.Fatalf("expected entity Resource, got %q", accesses[0].EntityName)
	}
	if accesses[0].Access != "read" {
		t.Fatalf("expected read access, got %q", accesses[0].Access)
	}
}

func TestJavaSqlStatementCallLiteral(t *testing.T) {
	p := NewJavaParser()
	source := []byte(`
import com.ibatis.sqlmap.client.SqlMapClient;

class ResourceService {
  SqlMapClient sqlMapClient;
  void deleteResource() throws Exception {
    sqlMapClient.update("deleteResource", null);
  }
}
`)

	result := p.ParseFile("ResourceService.java", source)
	calls := result.SqlStatements["ResourceService.deleteResource"]
	if len(calls) != 1 {
		t.Fatalf("expected 1 sql statement call, got %d: %#v", len(calls), calls)
	}
	if calls[0].StatementID != "deleteResource" {
		t.Fatalf("expected statementID deleteResource, got %q", calls[0].StatementID)
	}
	if calls[0].Access != "write" {
		t.Fatalf("expected write access, got %q", calls[0].Access)
	}
}

func TestJavaAnnotationArrayInitializerValues(t *testing.T) {
	p := NewJavaParser()
	source := []byte(`
import org.apache.ibatis.annotations.Select;

interface ResourceMapper {
  @Select({
    "SELECT ID",
    "FROM RESOURCE",
    "WHERE ID = #{id}"
  })
  int getResource();
}
`)

	result := p.ParseFile("ResourceMapper.java", source)
	fn := result.Functions[0]
	if fn.Name != "ResourceMapper.getResource" {
		t.Fatalf("expected function ResourceMapper.getResource, got %q", fn.Name)
	}

	var selectAnn *ParsedAnnotation
	for i := range fn.Annotations {
		if fn.Annotations[i].Name == "Select" {
			selectAnn = &fn.Annotations[i]
			break
		}
	}
	if selectAnn == nil {
		t.Fatalf("expected @Select annotation, got %#v", fn.Annotations)
	}

	raw, ok := selectAnn.Values["value"]
	if !ok {
		t.Fatalf("expected @Select values to include \"value\": %#v", selectAnn.Values)
	}
	arr, ok := raw.([]interface{})
	if !ok {
		t.Fatalf("expected @Select value to be []interface{}, got %T: %#v", raw, raw)
	}
	if len(arr) != 3 {
		t.Fatalf("expected 3 array items, got %d: %#v", len(arr), arr)
	}
	if arr[0] != "SELECT ID" || arr[1] != "FROM RESOURCE" {
		t.Fatalf("unexpected array contents: %#v", arr)
	}
}
