package metrics

import "testing"

func TestShortContainerName(t *testing.T) {
	cases := map[string]string{
		"peer0.org1.example.com": "peer0.org1",
		"orderer.example.com":    "orderer",
		"orderer2.example.com":   "orderer2",
		"lp1.org1":               "lp1.org1",
		"bench-fabricx-fabricx-committer-1": "committer-1",
		"bench-neuchain-block-server-0":     "block-server-0",
		"dev-peer0.org1.example.com-kvstore_1.0-e88284a33ff8806958375f99312782794164d62aac26fe22876f2c72fb6294d9": "chaincode.peer0.org1",
	}
	for in, want := range cases {
		if got := ShortContainerName(in); got != want {
			t.Errorf("ShortContainerName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSampledContainerNames(t *testing.T) {
	if SampledContainerNames(nil) != nil {
		t.Fatal("empty samples should have no names")
	}
	old := []SystemSample{{CPUPercent: 120}}
	if SampledContainerNames(old) != nil {
		t.Fatal("pre-breakdown samples should have no names")
	}
	got := SampledContainerNames([]SystemSample{
		{ByContainer: []ContainerUsage{{Name: "b"}, {Name: "a"}}},
		{ByContainer: []ContainerUsage{{Name: "a"}, {Name: "c"}}},
	})
	want := []string{"a", "b", "c"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("got %v, want %v", got, want)
	}
}
