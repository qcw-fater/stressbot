package robot

import (
	"testing"

	"stressbot/protocol/codec"
)

func TestCodecFieldBindsRejectMalformedCondition(t *testing.T) {
	_, err := codecFieldBindsToEngine([]codec.FieldBind{{
		Field:     "sequence",
		Type:      "fixed",
		Value:     1,
		Condition: "state:enabled >",
	}})
	if err == nil {
		t.Fatal("malformed heartbeat binding condition must fail before registration")
	}
}
