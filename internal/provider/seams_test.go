package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func seamWantNotImplemented(t *testing.T, err error, lane string) {
	t.Helper()
	var ni *seamNotImplemented
	if !errors.As(err, &ni) || ni.Lane != lane || !strings.HasPrefix(err.Error(), "not implemented (Pass 5): ") || !strings.HasSuffix(err.Error(), "provided by Lane "+lane) {
		t.Errorf("want not-implemented from Lane %s, got %v", lane, err)
	}
}

func TestSeamBuildKitDefault(t *testing.T) {
	if _, ok := seamBuildKitImpl.(seamBuildKitStub); !ok {
		t.Skip("Lane E has installed its implementation")
	}
	res, err := seamBuildKitImpl.Build(context.Background(), nil, seamBuildRequest{ContextDir: "."})
	if res != nil {
		t.Error("stub returned a result")
	}
	seamWantNotImplemented(t, err, "E")
}

func TestSeamBuildxPruneDefault(t *testing.T) {
	if _, ok := seamBuildxPruneImpl.(seamBuildxPruneStub); !ok {
		t.Skip("Lane E has installed its implementation")
	}
	_, err := seamBuildxPruneImpl.Prune(context.Background(), nil, seamBuildxPruneRequest{Builder: "b"})
	seamWantNotImplemented(t, err, "E")
}

func TestSeamDockerfileDefault(t *testing.T) {
	if _, ok := seamDockerfileImpl.(seamDockerfileStub); !ok {
		t.Skip("Lane D has installed its implementation")
	}
	_, err := seamDockerfileImpl.Render(seamDockerfileDoc{})
	seamWantNotImplemented(t, err, "D")
	_, err = seamDockerfileImpl.Parse("FROM scratch\n")
	seamWantNotImplemented(t, err, "D")
}

func TestSeamComposeDefault(t *testing.T) {
	if _, ok := seamComposeImpl.(seamComposeStub); !ok {
		t.Skip("Lane C has installed its implementation")
	}
	_, err := seamComposeImpl.Load(context.Background(), seamComposeLoadRequest{Content: "services: {}"})
	seamWantNotImplemented(t, err, "C")
	_, err = seamComposeImpl.Normalize(nil)
	seamWantNotImplemented(t, err, "C")
	_, err = seamComposeImpl.ConfigHash(nil, "web")
	seamWantNotImplemented(t, err, "C")
}
