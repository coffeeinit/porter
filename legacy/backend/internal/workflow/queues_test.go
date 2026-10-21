package workflow

import "testing"

func TestValidQueue(t *testing.T) {
	if !ValidQueue(QueueLifecycle) || ValidQueue("nope") {
		t.Fatal("queue allowlist wrong")
	}
}

func TestStageFeed(t *testing.T) {
	f := StageFeed{OpID: "op1"}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	f.Publish(Stage{Name: "build", Status: "active"})
	f.Publish(Stage{Name: "build", Status: "done"})
	if len(f.Stages) != 1 || f.Stages[0].Status != "done" {
		t.Fatal("republish must update in place")
	}
	f.Publish(Stage{Name: "start", Status: "failed"})
	if !f.Failed() {
		t.Fatal("failure must surface")
	}
}
