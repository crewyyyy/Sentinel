// Package trust scores only observed, chat-scoped activity. Trust never grants immunity.
package trust

func Score(activeDays, historyDays, adminReplies, distinctRepliers int) int {
	if activeDays <= 0 {
		return 0
	}
	return min(100, min(40, activeDays*2)+min(25, historyDays)+min(15, adminReplies*3)+min(20, distinctRepliers*2))
}
