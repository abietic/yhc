package mcp

import (
	"context"
	"testing"

	"github.com/abietic/yhc/engine/containment"
)

type mismatchedBindingAdapter struct{ containment.AmbientHostAdapter }

func (mismatchedBindingAdapter) Prepare(_ context.Context, request containment.SpawnRequest) (containment.SpawnSpec, error) {
	return containment.SpawnSpec{Path: request.Executable, Args: request.Args, Dir: request.Dir, Env: request.Env, BindingDigest: "mismatch"}, nil
}

func TestStdioBindingPrepareMismatchPreventsTransport(t *testing.T) {
	policy, err := containment.NewAmbientHostSnapshot("", containment.EntrypointEmbedded, containment.SelectionCompatibilityDefault)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := containment.NewBinding(containment.ProcessClassStdioMCP, policy, &mismatchedBindingAdapter{}, containment.AdapterProof{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newStdioProcessTransport(ServerConfig{Command: "/bin/echo", ExecutionPolicy: policy, ExecutionBinding: binding}); err == nil {
		t.Fatal("Prepare digest mismatch constructed a transport")
	}
}
