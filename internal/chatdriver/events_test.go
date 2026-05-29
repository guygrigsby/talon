package chatdriver

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/guygrigsby/jess/event"
)

// recordingSink captures calls in order.
type recordingSink struct {
	calls []string
}

func (r *recordingSink) Delta(full, delta string) {
	r.calls = append(r.calls, "Delta("+full+","+delta+")")
}
func (r *recordingSink) Thinking(full, delta string) {
	r.calls = append(r.calls, "Thinking("+full+","+delta+")")
}
func (r *recordingSink) Final(full string) { r.calls = append(r.calls, "Final("+full+")") }
func (r *recordingSink) ToolStart(id, name, args string) {
	r.calls = append(r.calls, "ToolStart("+id+","+name+","+args+")")
}
func (r *recordingSink) ToolResult(id, name, out string, isErr bool) {
	r.calls = append(r.calls, fmt.Sprintf("ToolResult(%s,%s,%s,%t)", id, name, out, isErr))
}
func (r *recordingSink) Error(kind, msg string) {
	r.calls = append(r.calls, "Error("+kind+","+msg+")")
}

func TestEventAdapter_AccumulatesDeltaAndFinal(t *testing.T) {
	sink := &recordingSink{}
	a := NewEventAdapter(sink)
	a.Handle(event.Event{Kind: event.KindMessageDelta, Delta: "Hel", DeltaKind: event.DeltaText})
	a.Handle(event.Event{Kind: event.KindMessageDelta, Delta: "lo", DeltaKind: event.DeltaText})
	a.Finalize("Hello") // adapter convention: Finalize wraps up after run.Wait()
	want := []string{"Delta(Hel,Hel)", "Delta(Hello,lo)", "Final(Hello)"}
	if !reflect.DeepEqual(sink.calls, want) {
		t.Fatalf("calls = %v, want %v", sink.calls, want)
	}
}

func TestEventAdapter_ThinkingSeparate(t *testing.T) {
	sink := &recordingSink{}
	a := NewEventAdapter(sink)
	a.Handle(event.Event{Kind: event.KindMessageDelta, Delta: "thi", DeltaKind: event.DeltaThinking})
	a.Handle(event.Event{Kind: event.KindMessageDelta, Delta: "nking", DeltaKind: event.DeltaThinking})
	a.Handle(event.Event{Kind: event.KindMessageDelta, Delta: "ans", DeltaKind: event.DeltaText})
	want := []string{"Thinking(thi,thi)", "Thinking(thinking,nking)", "Delta(ans,ans)"}
	if !reflect.DeepEqual(sink.calls, want) {
		t.Fatalf("calls = %v, want %v", sink.calls, want)
	}
}

func TestEventAdapter_ToolAndError(t *testing.T) {
	sink := &recordingSink{}
	a := NewEventAdapter(sink)
	a.Handle(event.Event{Kind: event.KindToolStart, ToolCallID: "c1", Tool: "remember", Args: []byte(`{"k":"v"}`)})
	a.Handle(event.Event{Kind: event.KindToolEnd, ToolCallID: "c1", Tool: "remember", Result: []byte(`{"ok":true}`), IsError: false})
	a.Handle(event.Event{Kind: event.KindToolEnd, ToolCallID: "c2", Tool: "remember", Result: []byte(`oops`), IsError: true})
	a.Handle(event.Event{Kind: event.KindError, Err: errors.New("boom")})
	want := []string{
		`ToolStart(c1,remember,{"k":"v"})`,
		`ToolResult(c1,remember,{"ok":true},false)`,
		`ToolResult(c2,remember,oops,true)`,
		`Error(agent,boom)`,
	}
	if !reflect.DeepEqual(sink.calls, want) {
		t.Fatalf("calls = %v, want %v", sink.calls, want)
	}
}
