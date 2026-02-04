package report

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

func Generate(events []Event, since, until time.Time) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Standup Report (%s – %s)\n",
		since.Format("Jan 2"), until.Format("Jan 2")))
	b.WriteString(strings.Repeat("=", 40) + "\n\n")

	categories := []EventCategory{
		CategoryPR,
		CategoryReview,
		CategoryIssue,
		CategoryComment,
		CategoryCommit,
	}

	grouped := make(map[EventCategory][]Event)
	for _, e := range events {
		grouped[e.Category] = append(grouped[e.Category], e)
	}

	for _, cat := range categories {
		catEvents := grouped[cat]
		if len(catEvents) == 0 {
			continue
		}

		sort.Slice(catEvents, func(i, j int) bool {
			return catEvents[i].CreatedAt.After(catEvents[j].CreatedAt)
		})

		b.WriteString(fmt.Sprintf("%s:\n", cat))
		for _, e := range catEvents {
			action := capitalize(e.Action)
			b.WriteString(fmt.Sprintf("  - %s %s [%s] (%s)\n",
				action, e.Title, e.Source, e.Repo))
		}
		b.WriteString("\n")
	}

	if len(events) == 0 {
		b.WriteString("No activity found for this period.\n")
	}

	return b.String()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
