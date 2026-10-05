package tracker

import (
	"strconv"
	"strings"

	"github.com/seppaleinen/idle-deck/queue"
)

// BuildPrompt derives Task.prompt per event-contract §7: the issue's title and
// body verbatim under a fixed provenance header. No templating, no reordering.
func BuildPrompt(repo string, number int, htmlURL string, tier queue.TaskTier, title, body string) string {
	role := queue.RoleForTier(tier)
	var sb strings.Builder
	sb.WriteString("repo:   ")
	sb.WriteString(repo)
	sb.WriteString("\nissue:  #")
	sb.WriteString(strconv.Itoa(number))
	sb.WriteString("\nurl:    ")
	sb.WriteString(htmlURL)
	sb.WriteString("\ntier:   ")
	sb.WriteString(string(tier))
	sb.WriteString("  (role: ")
	sb.WriteString(string(role))
	sb.WriteString(")\n\n")
	sb.WriteString(title)
	sb.WriteString("\n\n")
	sb.WriteString(body)
	return sb.String()
}