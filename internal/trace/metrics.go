package trace

// TraceQualityStats summarizes edge quality and depth stats for a trace tree.
type TraceQualityStats struct {
	TotalNodes        int            `json:"totalNodes"`
	MaxDepth          int            `json:"maxDepth"`
	LowSignalCount    int            `json:"lowSignalCount"`
	EdgeCounts        map[string]int `json:"edgeCounts"`
	ConfidenceCounts  map[string]int `json:"confidenceCounts"`
	CrossServiceCount int            `json:"crossServiceCount"`
	DirectRatio       float64        `json:"directRatio"`
	ResolvedRatio     float64        `json:"resolvedRatio"`
	NameBasedRatio    float64        `json:"nameBasedRatio"`
}

// TraceQualitySummary groups per-direction stats.
type TraceQualitySummary struct {
	Downstream TraceQualityStats `json:"downstream"`
	Upstream   TraceQualityStats `json:"upstream"`
}

// CalculateTraceQuality computes stats for a trace tree.
func CalculateTraceQuality(nodes []*TreeNode) TraceQualityStats {
	stats := TraceQualityStats{
		EdgeCounts:       make(map[string]int),
		ConfidenceCounts: make(map[string]int),
	}

	var walk func(node *TreeNode)
	walk = func(node *TreeNode) {
		if node == nil {
			return
		}
		stats.TotalNodes++
		if node.Depth > stats.MaxDepth {
			stats.MaxDepth = node.Depth
		}
		if node.EdgeType != "" {
			stats.EdgeCounts[node.EdgeType]++
		}
		if node.Confidence != "" {
			stats.ConfidenceCounts[node.Confidence]++
		}
		if node.LowSignal {
			stats.LowSignalCount++
		}
		if node.IsCrossService {
			stats.CrossServiceCount++
		}
		for _, child := range node.Children {
			walk(child)
		}
	}

	for _, node := range nodes {
		walk(node)
	}

	totalConfidence := 0
	for _, count := range stats.ConfidenceCounts {
		totalConfidence += count
	}
	if totalConfidence > 0 {
		stats.DirectRatio = float64(stats.ConfidenceCounts[confidenceHigh]) / float64(totalConfidence)
		stats.ResolvedRatio = float64(stats.ConfidenceCounts[confidenceMedium]) / float64(totalConfidence)
		stats.NameBasedRatio = float64(stats.ConfidenceCounts[confidenceLow]) / float64(totalConfidence)
	}

	return stats
}
