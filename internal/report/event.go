package report

import "time"

type EventCategory string

const (
	CategoryCommit  EventCategory = "Commits"
	CategoryPR      EventCategory = "Pull Requests / Merge Requests"
	CategoryReview  EventCategory = "Code Reviews"
	CategoryIssue   EventCategory = "Issues"
	CategoryComment EventCategory = "Comments"
)

type Event struct {
	Category  EventCategory
	Action    string
	Title     string
	URL       string
	Repo      string
	Source    string // "github" or "gitlab"
	CreatedAt time.Time
}
