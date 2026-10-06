package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

type sqsQueueBindingKey struct {
	classStart uint32
	name       string
}

func sqsClassBody(node *sitter.Node) bool {
	switch node.Type() {
	case "class_body", "enum_body", "interface_body", "annotation_type_body":
		return true
	}
	return false
}

func (p *JavaParser) sqsQueueForIdentifier(node *sitter.Node, content []byte) string {
	name := node.Content(content)
	qualified := false
	if parent := node.Parent(); parent != nil && parent.Type() == "field_access" {
		field := parent.ChildByFieldName("field")
		object := parent.ChildByFieldName("object")
		if field != nil && field.StartByte() == node.StartByte() {
			if object == nil || object.Content(content) != "this" {
				return ""
			}
			qualified = true
		}
	}
	// Inner classes may capture an outer binding, but sibling classes cannot.
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		if !qualified {
			if parameters := parent.ChildByFieldName("parameters"); parameters != nil {
				if parameters.Type() == "identifier" && parameters.Content(content) == name {
					return ""
				}
				for i := 0; i < int(parameters.NamedChildCount()); i++ {
					parameter := parameters.NamedChild(i)
					if parameter.Type() == "identifier" && parameter.Content(content) == name {
						return ""
					}
					if declared := parameter.ChildByFieldName("name"); declared != nil && declared.Content(content) == name {
						return p.sqsAnnotationQueue(parameter, content)
					}
				}
			}
			if parent.Type() == "block" {
				for i := int(parent.NamedChildCount()) - 1; i >= 0; i-- {
					declaration := parent.NamedChild(i)
					if declaration.StartByte() >= node.StartByte() || declaration.Type() != "local_variable_declaration" {
						continue
					}
					for j := 0; j < int(declaration.NamedChildCount()); j++ {
						variable := declaration.NamedChild(j)
						declared := variable.ChildByFieldName("name")
						if variable.Type() != "variable_declarator" || declared == nil || declared.Content(content) != name {
							continue
						}
						value := variable.ChildByFieldName("value")
						if value == nil || value.EndByte() >= node.StartByte() {
							return ""
						}
						if sqsIdentifierWrittenBetween(parent, content, name, variable.EndByte(), node.StartByte()) {
							return p.sqsAssignedLocalQueue(parent, value, node, name, content)
						}
						queue, _ := p.resolveSqsQueueReferences(value, content)
						return queue
					}
				}
			}
		}
		if sqsClassBody(parent) {
			if queue, exists := p.sqsQueueVars[sqsQueueBindingKey{parent.StartByte(), name}]; exists {
				return queue
			}
			if qualified {
				return ""
			}
		}
	}
	return ""
}

// A request may be declared null and built inside a try block. Retain a queue
// only when every non-null value assigned before the send resolves to that same
// queue. Unknown writes, shadowing, and self-referential updates stay unresolved.
func (p *JavaParser) sqsAssignedLocalQueue(block, initial, use *sitter.Node, name string, content []byte) string {
	queue := ""
	valid := true
	merge := func(value *sitter.Node) {
		if value == nil {
			valid = false
			return
		}
		if value.Type() == "null_literal" {
			return
		}
		q, ambiguous := p.resolveSqsQueueReferences(value, content)
		if q == "" || ambiguous || (queue != "" && queue != q) {
			valid = false
			return
		}
		queue = q
	}
	merge(initial)
	var containsName func(*sitter.Node) bool
	containsName = func(n *sitter.Node) bool {
		if n.Type() == "identifier" && n.Content(content) == name {
			return true
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			if containsName(n.NamedChild(i)) {
				return true
			}
		}
		return false
	}
	var walk func(*sitter.Node)
	walk = func(n *sitter.Node) {
		if !valid || n.EndByte() <= initial.EndByte() || n.StartByte() >= use.StartByte() {
			return
		}
		if n != block {
			switch n.Type() {
			case "lambda_expression", "class_body", "method_declaration", "constructor_declaration":
				return
			case "variable_declarator":
				if declared := n.ChildByFieldName("name"); declared != nil && declared.Content(content) == name && n.StartByte() > initial.EndByte() {
					valid = false
					return
				}
			case "assignment_expression":
				left, right, op := n.ChildByFieldName("left"), n.ChildByFieldName("right"), n.ChildByFieldName("operator")
				if left != nil && left.Content(content) == name {
					if n.EndByte() >= use.StartByte() || right == nil || op == nil || op.Content(content) != "=" || containsName(right) {
						valid = false
						return
					}
					merge(right)
					return
				}
			case "update_expression":
				if containsName(n) {
					valid = false
					return
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(block)
	if !valid {
		return ""
	}
	return queue
}

// extractSqsProducer resolves sends through the configured queue framework.
func (p *JavaParser) extractSqsProducer(node *sitter.Node, content []byte) *ParsedSqsProducer {
	method := methodInvocationName(node, content)
	if method != "sendMessage" && method != "sendMessageBatch" {
		configured := false
		if p.config != nil {
			for _, name := range p.config.JavaSQS.SendMethods {
				if method == name {
					configured = true
					break
				}
			}
		}
		if !configured {
			return nil
		}
	}
	queue, _ := p.resolveSqsQueueReferences(node, content)
	if queue == "" {
		return nil
	}
	return &ParsedSqsProducer{QueueName: queue, LineNumber: int(node.StartPoint().Row) + 1}
}

// A local initializer is evidence only while the binding remains unchanged.
func sqsIdentifierWrittenBetween(root *sitter.Node, content []byte, name string, after, before uint32) bool {
	if root.StartByte() >= before || root.EndByte() <= after {
		return false
	}
	if root.Type() == "assignment_expression" {
		if left := root.ChildByFieldName("left"); left != nil && left.Content(content) == name && root.StartByte() >= after {
			return true
		}
	}
	if root.Type() == "update_expression" && root.StartByte() >= after {
		for i := 0; i < int(root.NamedChildCount()); i++ {
			if root.NamedChild(i).Content(content) == name {
				return true
			}
		}
	}
	for i := 0; i < int(root.NamedChildCount()); i++ {
		if sqsIdentifierWrittenBetween(root.NamedChild(i), content, name, after, before) {
			return true
		}
	}
	return false
}

func (p *JavaParser) resolveSqsQueueReferences(root *sitter.Node, content []byte) (string, bool) {
	resolved := ""
	ambiguous := false
	var visit func(*sitter.Node)
	visit = func(node *sitter.Node) {
		if ambiguous {
			return
		}
		queue := ""
		switch node.Type() {
		case "method_invocation":
			queue = p.sqsQueueFromGetter(node, content)
		case "identifier":
			queue = p.sqsQueueForIdentifier(node, content)
		case "field_access":
			expression := node.Content(content)
			if p.isSqsEnumConstant(expression) {
				queue = p.normalizeSqsQueueConstant(expression)
			}
		}
		if queue != "" {
			if resolved != "" && resolved != queue {
				ambiguous = true
				return
			}
			resolved = queue
		}
		for i := 0; i < int(node.NamedChildCount()); i++ {
			visit(node.NamedChild(i))
		}
	}
	visit(root)
	if ambiguous {
		return "", true
	}
	return resolved, false
}

func (p *JavaParser) sqsQueueFromGetter(call *sitter.Node, content []byte) string {
	args := call.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() != 0 {
		return ""
	}
	if receiver := call.ChildByFieldName("object"); receiver != nil && receiver.Content(content) != "this" {
		return ""
	}
	name := methodInvocationName(call, content)
	for body := call.Parent(); body != nil; body = body.Parent() {
		if !sqsClassBody(body) {
			continue
		}
		resolved := ""
		for i := 0; i < int(body.NamedChildCount()); i++ {
			method := body.NamedChild(i)
			if method.Type() != "method_declaration" {
				continue
			}
			declared, params, block := method.ChildByFieldName("name"), method.ChildByFieldName("parameters"), method.ChildByFieldName("body")
			if declared == nil || declared.Content(content) != name || params == nil || params.NamedChildCount() != 0 || block == nil || block.NamedChildCount() != 1 {
				continue
			}
			statement := block.NamedChild(0)
			if statement.Type() != "return_statement" || statement.NamedChildCount() != 1 {
				continue
			}
			value := statement.NamedChild(0)
			// Do not evaluate arbitrary getter bodies or recursively chase calls.
			if value.Type() != "identifier" && value.Type() != "field_access" {
				continue
			}
			queue, ambiguous := p.resolveSqsQueueReferences(value, content)
			if ambiguous || queue == "" || (resolved != "" && resolved != queue) {
				return ""
			}
			resolved = queue
		}
		return resolved
	}
	return ""
}

// extractSqsConsumers extracts constructor-injected queues for configured consumers.
func (p *JavaParser) extractSqsConsumers(node *sitter.Node, content []byte, currentClass string) []ParsedSqsConsumer {
	if p.config == nil || p.config.JavaSQS.HandlerMethod == "" {
		return nil
	}
	var consumers []ParsedSqsConsumer
	parameters := node.ChildByFieldName("parameters")
	if parameters == nil {
		return nil
	}
	for i := 0; i < int(parameters.NamedChildCount()); i++ {
		parameter := parameters.NamedChild(i)
		if queue := p.sqsAnnotationQueue(parameter, content); queue != "" {
			consumers = append(consumers, ParsedSqsConsumer{
				QueueName:     queue,
				HandlerMethod: p.config.JavaSQS.HandlerMethod,
				ClassName:     currentClass,
			})
		}
	}
	return consumers
}

func (p *JavaParser) sqsAnnotationQueue(node *sitter.Node, content []byte) string {
	if p.config == nil || p.config.JavaSQS.QueueAnnotation == "" {
		return ""
	}
	for _, annotation := range p.collectNodeAnnotations(node, content) {
		if annotation.Name == p.config.JavaSQS.QueueAnnotation {
			if value, ok := annotation.Values["value"].(string); ok {
				return p.normalizeSqsQueueConstant(value)
			}
		}
	}
	return ""
}

func (p *JavaParser) extractSqsQueueVarMap(root *sitter.Node, content []byte) map[sqsQueueBindingKey]string {
	out := make(map[sqsQueueBindingKey]string)
	record := func(node *sitter.Node, name, queue string) {
		if name == "" || queue == "" {
			return
		}
		for parent := node.Parent(); parent != nil; parent = parent.Parent() {
			if !sqsClassBody(parent) {
				continue
			}
			key := sqsQueueBindingKey{parent.StartByte(), name}
			if previous, exists := out[key]; exists && previous != queue {
				out[key] = ""
			} else {
				out[key] = queue
			}
			return
		}
	}
	var visit func(*sitter.Node)
	visit = func(node *sitter.Node) {
		switch node.Type() {
		case "formal_parameter":
			// Only constructor injection contributes to class bindings. Method
			// parameters are resolved at their use site and must not leak to peers.
			if parameters := node.Parent(); parameters != nil && parameters.Parent() != nil && parameters.Parent().Type() == "constructor_declaration" {
				if name := node.ChildByFieldName("name"); name != nil {
					record(node, name.Content(content), p.sqsAnnotationQueue(node, content))
				}
			}
		case "field_declaration":
			queue := p.sqsAnnotationQueue(node, content)
			for i := 0; i < int(node.NamedChildCount()); i++ {
				child := node.NamedChild(i)
				if child.Type() == "variable_declarator" {
					if name := child.ChildByFieldName("name"); name != nil {
						record(node, name.Content(content), queue)
					}
				}
			}
		}
		for i := 0; i < int(node.NamedChildCount()); i++ {
			visit(node.NamedChild(i))
		}
	}
	visit(root)
	return out
}

func (p *JavaParser) normalizeSqsQueueConstant(value string) string {
	queue := strings.TrimSpace(value)
	queue = strings.Trim(queue, "() ")
	if p.isSqsEnumConstant(queue) {
		queue = queue[strings.LastIndex(queue, ".")+1:]
	}
	queue = strings.Trim(queue, `"'`)
	if p.config != nil && p.config.JavaSQS.ConstantSuffix != "" {
		queue = strings.TrimSuffix(queue, p.config.JavaSQS.ConstantSuffix)
	}
	for _, c := range queue {
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return ""
		}
	}
	return queue
}

func (p *JavaParser) isSqsEnumConstant(value string) bool {
	if p.config == nil || p.config.JavaSQS.QueueEnum == "" {
		return false
	}
	i := strings.LastIndex(value, ".")
	if i < 0 {
		return false
	}
	typeName := value[:i]
	enum := p.config.JavaSQS.QueueEnum
	return typeName == enum || strings.HasSuffix(typeName, "."+enum)
}

func (p *JavaParser) extractSqsConsumersFromQueueURLAssignments(root *sitter.Node, content []byte, classes []ParsedClass) []ParsedSqsConsumer {
	if p.config == nil || len(p.config.JavaSQS.QueueURLFields) == 0 {
		return nil
	}
	consumerClasses := make(map[string]bool)
	for _, class := range classes {
		consumerClasses[class.Name] = p.isConfiguredSqsConsumer(class)
	}
	fields := make(map[string]bool)
	for _, field := range p.config.JavaSQS.QueueURLFields {
		if field != "" {
			fields[field] = true
		}
	}
	var consumers []ParsedSqsConsumer
	seen := make(map[string]bool)
	var visit func(*sitter.Node, string)
	visit = func(node *sitter.Node, className string) {
		switch node.Type() {
		case "class_declaration", "enum_declaration", "record_declaration":
			className = javaDeclarationName(node, content)
		case "assignment_expression":
			left := node.ChildByFieldName("left")
			right := node.ChildByFieldName("right")
			operator := node.ChildByFieldName("operator")
			if consumerClasses[className] && left != nil && right != nil && operator != nil && operator.Content(content) == "=" {
				field := strings.TrimPrefix(left.Content(content), "this.")
				if fields[field] {
					queue := p.sqsAssignedConstant(right, content)
					key := className + ":" + queue
					if queue != "" && !seen[key] {
						seen[key] = true
						consumers = append(consumers, ParsedSqsConsumer{
							QueueName:     queue,
							HandlerMethod: p.config.JavaSQS.HandlerMethod,
							ClassName:     className,
						})
					}
				}
			}
		}
		for i := 0; i < int(node.NamedChildCount()); i++ {
			visit(node.NamedChild(i), className)
		}
	}
	visit(root, "")
	return consumers
}

// A configured URL field can concatenate a prefix with a symbolic queue constant.
// Preserve the constant's identity without inventing an environment-specific prefix.
func (p *JavaParser) sqsAssignedConstant(node *sitter.Node, content []byte) string {
	switch node.Type() {
	case "identifier", "field_access":
		return p.normalizeSqsQueueConstant(node.Content(content))
	case "binary_expression":
		operator := node.ChildByFieldName("operator")
		right := node.ChildByFieldName("right")
		if operator != nil && operator.Content(content) == "+" && right != nil {
			return p.sqsAssignedConstant(right, content)
		}
	case "parenthesized_expression":
		if node.NamedChildCount() == 1 {
			return p.sqsAssignedConstant(node.NamedChild(0), content)
		}
	}
	return ""
}

func (p *JavaParser) isConfiguredSqsConsumer(class ParsedClass) bool {
	if p.config == nil || p.config.JavaSQS.HandlerMethod == "" {
		return false
	}
	types := append([]string{class.ExtendsClass}, class.Implements...)
	return p.config.JavaSQS.MatchesConsumerType(types)
}

func (p *JavaParser) filterSqsConsumersToConfiguredTypes(consumers []ParsedSqsConsumer, classes []ParsedClass) []ParsedSqsConsumer {
	if len(consumers) == 0 || len(classes) == 0 {
		return consumers
	}
	consumerClasses := make(map[string]bool)
	for _, class := range classes {
		if p.isConfiguredSqsConsumer(class) {
			consumerClasses[class.Name] = true
		}
	}
	if len(consumerClasses) == 0 {
		return nil
	}
	filtered := make([]ParsedSqsConsumer, 0, len(consumers))
	for _, consumer := range consumers {
		if consumerClasses[consumer.ClassName] {
			filtered = append(filtered, consumer)
		}
	}
	return filtered
}
