package engine

import (
	"context"
	"errors"
	flowdef "stressbot/flow"
	"testing"
	"time"

	"stressbot/errcode"
)

type eventListenSender struct {
	fakeNetSender
	tcpCalls int
	udpCalls int
	exchange *NetExchange
	err      error
}

func (s *eventListenSender) TCPListen(context.Context, string, string, time.Duration) (*NetExchange, error) {
	s.tcpCalls++
	return s.exchange, s.err
}

func (s *eventListenSender) UDPListen(context.Context, string, string, time.Duration) (*NetExchange, error) {
	s.udpCalls++
	return s.exchange, s.err
}

func TestExecListenUsesSingleEventWait(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern string
		wantTCP int
		wantUDP int
	}{
		{name: "tcp", pattern: flowdef.PatternTCPListen, wantTCP: 1},
		{name: "udp", pattern: flowdef.PatternUDPListen, wantUDP: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := &eventListenSender{}
			ae := NewActionExecutor(nil, sender, nil, nil, TimingLevelRTTOnly)

			_, _, timing, err := ae.Execute(t.Context(), &flowdef.ActionDef{
				Name: "wait_push", Pattern: tc.pattern, Service: "logic", Timeout: 1,
			})
			var actionErr *errcode.ActionError
			if !errors.As(err, &actionErr) || actionErr.Code != errcode.ErrListenTimeout {
				t.Fatalf("Execute() error = %v, want ErrListenTimeout", err)
			}
			if sender.tcpCalls != tc.wantTCP || sender.udpCalls != tc.wantUDP {
				t.Fatalf("事件等待调用次数 tcp=%d udp=%d, want tcp=%d udp=%d",
					sender.tcpCalls, sender.udpCalls, tc.wantTCP, tc.wantUDP)
			}
			if timing.ListenTimeouts != 1 {
				t.Fatalf("ListenTimeouts=%d, want 1", timing.ListenTimeouts)
			}
		})
	}
}
