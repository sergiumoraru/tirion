package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/java"
)

// JavaInvocation retains source expressions for callers that resolve constants
// and paths after parsing. Comments and string contents are not invocations.
type JavaInvocation struct {
	Receiver string
	Method   string
	Args     []string
}

func JavaInvocations(source string) []JavaInvocation {
	p := sitter.NewParser()
	defer p.Close()
	p.SetLanguage(java.GetLanguage())
	content := []byte(source)
	tree := p.Parse(nil, content)
	if tree == nil {
		return nil
	}
	defer tree.Close()
	var calls []JavaInvocation
	var visit func(*sitter.Node)
	visit = func(node *sitter.Node) {
		if node.Type() == "method_invocation" {
			object := node.ChildByFieldName("object")
			name := node.ChildByFieldName("name")
			args := node.ChildByFieldName("arguments")
			if object != nil && name != nil && args != nil {
				call := JavaInvocation{Receiver: object.Content(content), Method: name.Content(content)}
				for i := 0; i < int(args.NamedChildCount()); i++ {
					arg := args.NamedChild(i)
					if arg.Type() != "line_comment" && arg.Type() != "block_comment" {
						call.Args = append(call.Args, arg.Content(content))
					}
				}
				calls = append(calls, call)
			}
		}
		for i := 0; i < int(node.NamedChildCount()); i++ {
			visit(node.NamedChild(i))
		}
	}
	visit(tree.RootNode())
	return calls
}
