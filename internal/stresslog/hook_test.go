package stresslog

import "testing"

func TestPostQYWXMsgIgnoresInvalidWebhookURL(t *testing.T) {
	previousURL, previousToken := webHookURL, token
	t.Cleanup(func() {
		webHookURL = previousURL
		token = previousToken
	})
	webHookURL = "://invalid"
	token = "test-token"

	postQYWXMsg("test")
}
