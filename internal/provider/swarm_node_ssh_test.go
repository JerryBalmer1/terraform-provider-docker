package provider

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

func TestSwarmNodeSSHSchema(t *testing.T) {
	var resp resource.SchemaResponse
	NewSwarmNodeResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	blk, ok := resp.Schema.Blocks["ssh"].(rschema.SingleNestedBlock)
	if !ok {
		t.Fatalf("docker_swarm_node has no ssh block: %T", resp.Schema.Blocks["ssh"])
	}
	for name := range sshAttrDocs {
		a, ok := blk.Attributes[name]
		if !ok {
			t.Errorf("ssh.%s missing", name)
			continue
		}
		if a.GetMarkdownDescription() == "" {
			t.Errorf("ssh.%s has no MarkdownDescription", name)
		}
	}
	if !blk.Attributes["password_wo"].IsWriteOnly() || blk.Attributes["password"].IsWriteOnly() {
		t.Error("password_wo must be write-only, password must not")
	}
	if !blk.Attributes["password"].IsSensitive() || !blk.Attributes["identity_file"].IsSensitive() {
		t.Error("password and identity_file must be sensitive")
	}
}

func TestSwarmNodeSSHEndpoint(t *testing.T) {
	t.Setenv("USER", "envuser")
	if sc, err := swarmNodeSSH(swarmNodeModel{Host: types.StringValue("tcp://w1:2376")}, ""); sc != nil || err != nil {
		t.Errorf("tcp host: %+v %v", sc, err)
	}
	sc, err := swarmNodeSSH(swarmNodeModel{Host: types.StringValue("ssh://ops@w1")}, "")
	if err != nil || sc.User != "ops" || sc.Host != "w1" || sc.Port != 22 || !sc.Strict {
		t.Errorf("ssh host: %+v %v", sc, err)
	}
	if _, err := swarmNodeSSH(swarmNodeModel{Host: types.StringValue("tcp://w1:2376"), SSH: &sshModel{}}, ""); err == nil {
		t.Error("ssh block with a tcp host should fail")
	}
	// The write-only password from config is applied; the block may override the port.
	m := swarmNodeModel{Host: types.StringValue("ssh://ops@w1"), SSH: &sshModel{Port: types.Int64Value(2200), PasswordWO: types.StringNull()}}
	sc, err = swarmNodeSSH(m, "from-config")
	if err != nil || sc.Password != "from-config" || sc.Port != 2200 {
		t.Errorf("password_wo: %+v %v", sc, err)
	}
	if !m.SSH.PasswordWO.IsNull() {
		t.Error("swarmNodeSSH must not write the password into the model")
	}
}

// TestSwarmNodeClientOverSSH reaches a joining node's (fake) daemon through
// the in-process SSH server, the way Create and Delete do.
func TestSwarmNodeClientOverSSH(t *testing.T) {
	hermeticSSH(t)
	eng := fakeEngine(t)
	srv := startTestSSHServer(t, eng.Listener.Addr().String(), nil)
	m := swarmNodeModel{
		Host: types.StringValue("ssh://" + testSSHUser + "@127.0.0.1:" + strconv.Itoa(srv.Port)),
		SSH:  &sshModel{StrictHostKeyChecking: types.BoolValue(false), ConnectTimeout: types.StringValue("5s")},
	}
	r := &swarmNodeResource{}
	nc, err := r.nodeClient(m, testSSHPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	v, err := nc.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil || v.Version != "ssh-fake-engine" {
		t.Fatalf("version over ssh: %+v %v", v, err)
	}
	// Without the write-only password (as at destroy time) auth fails cleanly.
	sshPool.closeAll()
	nc2, err := r.nodeClient(m, "")
	if err != nil {
		t.Fatal(err)
	}
	defer nc2.Close()
	if _, err := nc2.ServerVersion(ctx, client.ServerVersionOptions{}); err == nil || !strings.Contains(err.Error(), "no SSH credentials") {
		t.Fatalf("want missing-credentials error, got %v", err)
	}
}
