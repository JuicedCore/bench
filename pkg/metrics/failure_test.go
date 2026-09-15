package metrics

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyInspect(t *testing.T) {
	boom := errors.New("exit status 1")
	if _, _, err := classifyInspect("c", []byte("Error: No such object: c"), boom); err != nil {
		t.Errorf("removed container is a failure, not an inspect error: %v", err)
	}
	if _, _, err := classifyInspect("c", []byte("Cannot connect to the Docker daemon"), boom); err == nil || !strings.Contains(err.Error(), "Docker daemon") {
		t.Errorf("daemon error must be returned, not treated as removal: %v", err)
	}
	if _, running, err := classifyInspect("c", []byte("true|false|0|running"), nil); err != nil || !running {
		t.Errorf("running container misclassified: running=%v err=%v", running, err)
	}
	e, running, err := classifyInspect("c", []byte("false|true|137|exited"), nil)
	if err != nil || running || !e.OOMKilled || e.ExitCode != 137 {
		t.Errorf("OOM exit misparsed: %+v running=%v err=%v", e, running, err)
	}
	if _, _, err := classifyInspect("c", []byte("garbage"), nil); err == nil {
		t.Error("unparseable output must be an error")
	}
}

func TestErrorKeyKeepsAddresses(t *testing.T) {
	got := errorKey("endorse: rpc error: Address: peer0.org1.example.com:7051 at 10.0.0.4:7051, block 123")
	for _, want := range []string{"peer0.org1.example.com:7051", "10.0.0.4:7051", "block N"} {
		if !strings.Contains(got, want) {
			t.Errorf("errorKey lost %q: %s", want, got)
		}
	}
}

func TestEmptyNamePrefixesMatchNothingAndIsUnhealthy(t *testing.T) {
	s := &SystemSampler{}
	if s.match("prometheus") {
		t.Error("an empty filter must not match unrelated containers")
	}
	if !strings.Contains(s.Unhealthy(), "container_names is empty") {
		t.Errorf("Unhealthy = %q", s.Unhealthy())
	}
}
