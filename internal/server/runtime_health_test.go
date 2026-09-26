package server

import (
	"errors"
	"reflect"
	"testing"

	kernelruntime "synon-go/internal/kernel"
)

func TestRuntimeComponentHealthIsBoundedSortedAndRecoverable(t *testing.T) {
	server := &Server{runtimeComponents: map[string]struct{}{}}
	server.ReportRuntimeComponent("runner-chat", errors.New("provider secret must not be retained"))
	server.ReportRuntimeComponent("compute-poller", errors.New("transient"))
	server.ReportRuntimeComponent("", errors.New("ignored"))
	server.ReportRuntimeComponent(string(make([]byte, 81)), errors.New("ignored"))
	if got, want := server.degradedRuntimeComponents(), []string{"compute-poller", "runner-chat"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("degraded=%#v want=%#v", got, want)
	}
	server.ReportRuntimeComponent("runner-chat", nil)
	if got, want := server.degradedRuntimeComponents(), []string{"compute-poller"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("recovered=%#v want=%#v", got, want)
	}
}

func TestScientificCoreRuntimeHealthSeparatesGatewayFromRuntimeReadiness(t *testing.T) {
	server := &Server{kernelManager: kernelruntime.NewManager(kernelruntime.Config{})}
	health := server.scientificCoreRuntimeHealth()
	if health["ready"] != false || health["required"] != true || health["platform"] == "" {
		t.Fatalf("scientific core health=%#v", health)
	}
	if health["status"] != "unsupported" && health["status"] != "unavailable" {
		t.Fatalf("scientific core status=%#v", health)
	}
}
