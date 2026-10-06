package trace

import "testing"

func TestIsLowSignalNode_ClassifiesUtilityLeavesAcrossLanguages(t *testing.T) {
	t.Parallel()

	parent := &TreeNode{
		Name: "ResourceController.load",
		Repo: "resource-api",
		File: "Controllers/ResourceController.cs",
	}

	tests := []struct {
		name string
		node *TreeNode
	}{
		{
			name: "csharp string utility",
			node: &TreeNode{
				Name: "text.IndexOf",
				Repo: "resource-api",
				File: "Controllers/ResourceController.cs",
			},
		},
		{
			name: "java utility",
			node: &TreeNode{
				Name: "Arrays.asList",
				Repo: "core-app",
				File: "src/main/java/com/example/core/Foo.java",
			},
		},
		{
			name: "typescript utility",
			node: &TreeNode{
				Name: "JSON.stringify",
				Repo: "web-app",
				File: "src/resources.ts",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := parent
			if tt.node.File != parent.File || tt.node.Repo != parent.Repo {
				p = &TreeNode{Name: "Example.entry", Repo: tt.node.Repo, File: tt.node.File}
			}
			if !IsLowSignalNode(p, tt.node) {
				t.Fatalf("expected %q to be low-signal", tt.node.Name)
			}
		})
	}
}

func TestIsLowSignalNode_DoesNotGloballyHideDomainValuesLikeNames(t *testing.T) {
	t.Parallel()

	parent := &TreeNode{
		Name: "ConfigController.save",
		Repo: "admin-app",
		File: "src/ConfigController.java",
	}

	node := &TreeNode{
		Name: "ResourceState.values",
		Repo: "core-app",
		File: "src/ResourceState.java",
	}

	if IsLowSignalNode(parent, node) {
		t.Fatalf("did not expect cross-file domain leaf to be low-signal")
	}
}

func TestIsLowSignalNode_PreservesBusinessAndBoundaryNodes(t *testing.T) {
	t.Parallel()

	parent := &TreeNode{
		Name: "ResourceController.load",
		Repo: "resource-api",
		File: "Controllers/ResourceController.cs",
	}

	cases := []struct {
		name string
		node *TreeNode
	}{
		{
			name: "business repository call",
			node: &TreeNode{
				Name: "ResourceRepository.GetById",
				Repo: "resource-api",
				File: "Controllers/ResourceController.cs",
			},
		},
		{
			name: "cross service http boundary",
			node: &TreeNode{
				Name:           "[POST /resources]",
				Repo:           "remote-api",
				EdgeType:       edgeTypeHTTP,
				IsCrossService: true,
				HttpTarget:     "/resources",
			},
		},
		{
			name: "non leaf business branch",
			node: &TreeNode{
				Name: "ResourceController.loadFromRemote",
				Repo: "resource-api",
				File: "Controllers/ResourceController.cs",
				Children: []*TreeNode{
					{Name: "httpClient.MakePostRequest"},
				},
			},
		},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if IsLowSignalNode(parent, tt.node) {
				t.Fatalf("did not expect %q to be low-signal", tt.node.Name)
			}
		})
	}
}

func TestIsLowSignalNode_ClassifiesSameFileTypePseudoCalls(t *testing.T) {
	t.Parallel()

	parent := &TreeNode{
		Name: "ResourceController.load",
		Repo: "resource-api",
		File: "Controllers/ResourceController.cs",
	}

	tests := []string{
		"ResourceController.Exception",
		"ResourceController.ResourceInputModel",
		"ResourceController.Claim",
	}

	for _, name := range tests {
		if !IsLowSignalNode(parent, &TreeNode{
			Name: name,
			Repo: "resource-api",
			File: "Controllers/ResourceController.cs",
		}) {
			t.Fatalf("expected %q to be low-signal", name)
		}
	}
}

func TestIsLowSignalNode_ClassifiesSameClassPseudoCallsWithoutLocation(t *testing.T) {
	t.Parallel()

	parent := &TreeNode{
		Name: "ResourceController.load",
		Repo: "resource-api",
		File: "Controllers/ResourceController.cs",
	}

	tests := []string{
		"ResourceController.Exception",
		"ResourceController.WebClient",
		"ResourceController.StatusInputModel",
		"ResourceController.ResourceInputModel",
		"ResourceController.string",
		"ResourceController.ToString",
	}

	for _, name := range tests {
		if !IsLowSignalNode(parent, &TreeNode{Name: name}) {
			t.Fatalf("expected %q to be low-signal", name)
		}
	}
}

func TestIsLowSignalNode_ClassifiesLocalValueUtilityCallsWithoutLocation(t *testing.T) {
	t.Parallel()

	parent := &TreeNode{
		Name: "ResourceController.load",
		Repo: "resource-api",
		File: "Controllers/ResourceController.cs",
	}

	for _, name := range []string{
		"text.IndexOf",
		"values.FirstOrDefault",
		"value.ToString",
	} {
		if !IsLowSignalNode(parent, &TreeNode{Name: name}) {
			t.Fatalf("expected %q to be low-signal", name)
		}
	}
}

func TestIsLowSignalNode_PreservesSameClassBusinessCallsWithoutLocation(t *testing.T) {
	t.Parallel()

	parent := &TreeNode{
		Name: "ResourceController.load",
		Repo: "resource-api",
		File: "Controllers/ResourceController.cs",
	}

	for _, name := range []string{
		"ResourceController.loadFromRemote",
		"ResourceController.RedirectToAction",
		"ResourceController.DownloadString",
	} {
		if IsLowSignalNode(parent, &TreeNode{Name: name}) {
			t.Fatalf("did not expect %q to be low-signal", name)
		}
	}
}

func TestIsLowSignalNode_ClassifiesSameFileUtilityTransforms(t *testing.T) {
	t.Parallel()

	parent := &TreeNode{
		Name: "ResourceList.render",
		Repo: "resource-ui",
		File: "src/resource-list.ts",
	}

	for _, name := range []string{
		"value.timestamp.split",
		"value.timestamp.split('T').join",
		"values.FirstOrDefault",
	} {
		if !IsLowSignalNode(parent, &TreeNode{
			Name: name,
			Repo: "resource-ui",
			File: "src/resource-list.ts",
		}) {
			t.Fatalf("expected %q to be low-signal", name)
		}
	}
}

func TestAnnotateLowSignal_MarksTreeRecursively(t *testing.T) {
	t.Parallel()

	root := &TreeNode{
		Name: "ResourceController.load",
		Repo: "resource-api",
		File: "Controllers/ResourceController.cs",
		Children: []*TreeNode{
			{
				Name: "text.IndexOf",
				Repo: "resource-api",
				File: "Controllers/ResourceController.cs",
			},
			{
				Name: "ResourceRepository.GetById",
				Repo: "resource-api",
				File: "Controllers/ResourceController.cs",
			},
		},
	}

	AnnotateLowSignal([]*TreeNode{root})

	if !root.Children[0].LowSignal {
		t.Fatalf("expected first child to be marked low-signal")
	}
	if root.Children[1].LowSignal {
		t.Fatalf("did not expect business child to be marked low-signal")
	}
}
