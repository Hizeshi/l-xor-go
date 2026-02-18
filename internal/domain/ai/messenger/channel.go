package messenger

import "strings"

func IsMessengerSession(sessionID string) bool {
	sid := strings.ToLower(strings.TrimSpace(sessionID))
	return strings.HasPrefix(sid, "tg:") || strings.HasPrefix(sid, "wa:")
}

func IsSiteSession(sessionID string) bool {
	return !IsMessengerSession(sessionID)
}
