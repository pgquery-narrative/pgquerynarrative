package story

// NarrativeContent represents the structured narrative output
type NarrativeContent struct {
	Headline        string   `json:"headline"`
	Takeaways       []string `json:"takeaways"`
	Drivers         []string `json:"drivers"`
	Limitations     []string `json:"limitations"`
	Recommendations []string `json:"recommendations"`
}
