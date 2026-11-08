package chatgpt

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// longChain is a conversation whose mapping is one parent chain of n nodes,
// every one with text and only the root dated: the shape that made flatten
// walk the whole chain per node inside the sort comparator (4,000 nodes,
// ~93 KiB of JSON, took ~19 s; ~1 MiB would block the import worker for
// hours).
func longChain(n int) cgptConversation {
	c := cgptConversation{ID: "conv", CurrentNode: fmt.Sprintf("n%d", n-1), Mapping: map[string]cgptNode{}}
	root := 1700000000.0
	for i := range n {
		id, parent := fmt.Sprintf("n%d", i), ""
		if i > 0 {
			parent = fmt.Sprintf("n%d", i-1)
		}
		m := &cgptMessage{ID: "m" + id, Content: json.RawMessage(`{"content_type":"text","parts":["hi"]}`)}
		m.Author.Role = "user"
		if i == 0 {
			m.CreateTime = &root
		}
		c.Mapping[id] = cgptNode{ID: id, Parent: parent, Message: m}
	}
	return c
}

func TestFlattenLongChainIsLinear(t *testing.T) {
	start := time.Now()
	got := flatten(longChain(50_000))
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("flatten of a 50k-node chain took %v; want linear time", d)
	}
	if len(got) != 50_000 {
		t.Fatalf("payloads = %d", len(got))
	}
	// Tree order is kept for equal times, every node inherits the root's
	// time, and each one points at its parent's message.
	for i, p := range got {
		if p.NodeID != fmt.Sprintf("n%d", i) {
			t.Fatalf("payload %d is node %s; want tree order", i, p.NodeID)
		}
		if p.CreateTime != 1700000000 || (i > 0) != p.TimeInferred {
			t.Fatalf("payload %d: time %v inferred %v", i, p.CreateTime, p.TimeInferred)
		}
		if want := map[bool]string{true: "", false: fmt.Sprintf("mn%d", i-1)}[i == 0]; p.ParentMsgID != want {
			t.Fatalf("payload %d: parent %q, want %q", i, p.ParentMsgID, want)
		}
		if !p.OnBranch {
			t.Fatalf("payload %d not on the current branch", i)
		}
	}
}

// A node that is its own ancestor must not hang or crash flatten.
func TestFlattenSurvivesCycles(t *testing.T) {
	c := longChain(3)
	n0 := c.Mapping["n0"]
	n0.Parent = "n2" // n0 → n2 → n1 → n0
	n0.Message.CreateTime = nil
	c.Mapping["n0"] = n0
	if got := flatten(c); len(got) != 3 {
		t.Fatalf("payloads = %d, want 3", len(got))
	}
}
