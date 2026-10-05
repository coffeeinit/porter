package storage

import "testing"

func TestT13ClonePlanValidation(t *testing.T) {
	base := ClonePlan{
		SourceVolume: "v1", TargetVolume: "v2",
		NewMAC: "m2", NewIP: "ip2",
		SourceMAC: "m1", SourceIP: "ip1",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid plan must pass: %v", err)
	}
	cases := map[string]ClonePlan{
		"empty source": func() ClonePlan { p := base; p.SourceVolume = ""; return p }(),
		"empty target": func() ClonePlan { p := base; p.TargetVolume = ""; return p }(),
		"self clone":   func() ClonePlan { p := base; p.TargetVolume = "v1"; return p }(),
		"missing MAC":  func() ClonePlan { p := base; p.NewMAC = ""; return p }(),
		"missing IP":   func() ClonePlan { p := base; p.NewIP = ""; return p }(),
		"reuse MAC":    func() ClonePlan { p := base; p.NewMAC = "m1"; return p }(),
		"reuse IP":     func() ClonePlan { p := base; p.NewIP = "ip1"; return p }(),
		"reuse both":   func() ClonePlan { p := base; p.NewMAC = "m1"; p.NewIP = "ip1"; return p }(),
	}
	for name, p := range cases {
		if err := p.Validate(); err == nil {
			t.Fatalf("%s must fail validation", name)
		}
	}
	// Fresh identity against an empty source identity still passes: nothing reused.
	empty := ClonePlan{SourceVolume: "v1", TargetVolume: "v2", NewMAC: "m2", NewIP: "ip2"}
	if err := empty.Validate(); err != nil {
		t.Fatalf("empty source MAC/IP must not block fresh identity: %v", err)
	}
}

func TestT13ResizeOpValidation(t *testing.T) {
	valid := []ResizeOp{
		{Volume: "v", Kind: ResizeExpand, SizeMiB: 1024, Online: true},
		{Volume: "v", Kind: ResizeExpand, SizeMiB: 1024, Online: false},
		{Volume: "v", Kind: ResizeShrink, SizeMiB: 512, Online: false},
	}
	for i, r := range valid {
		if err := r.Validate(); err != nil {
			t.Fatalf("valid[%d] must pass: %v", i, err)
		}
	}
	invalid := map[string]ResizeOp{
		"empty volume":     {Volume: "", Kind: ResizeExpand, SizeMiB: 1024},
		"zero size":        {Volume: "v", Kind: ResizeExpand, SizeMiB: 0},
		"negative size":    {Volume: "v", Kind: ResizeShrink, SizeMiB: -1},
		"online shrink":    {Volume: "v", Kind: ResizeShrink, SizeMiB: 512, Online: true},
		"unknown kind":     {Volume: "v", Kind: "sideways", SizeMiB: 512},
		"empty kind":       {Volume: "v", Kind: "", SizeMiB: 512},
		"empty everything": {},
	}
	for name, r := range invalid {
		if err := r.Validate(); err == nil {
			t.Fatalf("%s must fail validation", name)
		}
	}
}
