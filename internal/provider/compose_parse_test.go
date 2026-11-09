package provider

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/api/types/volume"
)

func composeMustLoad(t *testing.T, in composeInputs) *composeDesired {
	t.Helper()
	if in.WorkingDir == "" && len(in.Files) == 0 {
		in.WorkingDir = t.TempDir()
	}
	p, err := composeLoad(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return &composeDesired{Project: p}
}

func TestComposeLoadEnvironmentIsolation(t *testing.T) {
	t.Setenv("LEAK", "from-os")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.env"), []byte("TAG=1\nBOTH=file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.env"), []byte("TAG=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := composeMustLoad(t, composeInputs{
		WorkingDir: dir, ProjectName: "iso",
		Content:  "services:\n  a:\n    image: busybox:${TAG}\n    environment:\n      LEAK: ${LEAK:-safe}\n      BOTH: ${BOTH}\n",
		EnvFiles: []string{"a.env", "b.env"},
		Env:      map[string]string{"BOTH": "env"},
	})
	a := d.Project.Services["a"]
	if a.Image != "busybox:2" {
		t.Errorf("image %q: later env_file should win", a.Image)
	}
	if got := *a.Environment["LEAK"]; got != "safe" {
		t.Errorf("LEAK = %q: OS environment leaked", got)
	}
	if got := *a.Environment["BOTH"]; got != "env" {
		t.Errorf("BOTH = %q: env should override env_file", got)
	}
}

func TestComposeLoadPathsAndNames(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "My Project")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "compose.yml"), []byte("services:\n  a:\n    image: busybox\n    volumes: [./data:/data]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "named.yml"), []byte("name: fromyaml\nservices:\n  a:\n    image: busybox\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// files: relative binds resolve against the first file's directory and
	// the project name defaults to that directory's normalised name.
	d := composeMustLoad(t, composeInputs{Files: []string{filepath.Join(sub, "compose.yml")}})
	if d.Project.Name != "myproject" {
		t.Errorf("name %q", d.Project.Name)
	}
	if got := d.Project.Services["a"].Volumes[0].Source; got != filepath.Join(sub, "data") {
		t.Errorf("bind source %q", got)
	}
	// top-level name, then explicit project_name.
	if d := composeMustLoad(t, composeInputs{Files: []string{filepath.Join(sub, "named.yml")}}); d.Project.Name != "fromyaml" {
		t.Errorf("yaml name %q", d.Project.Name)
	}
	if d := composeMustLoad(t, composeInputs{Files: []string{filepath.Join(sub, "named.yml")}, ProjectName: "explicit"}); d.Project.Name != "explicit" {
		t.Errorf("explicit name %q", d.Project.Name)
	}
	// content: relative paths resolve against working_dir.
	d = composeMustLoad(t, composeInputs{WorkingDir: dir, ProjectName: "c", Content: "services:\n  a:\n    image: busybox\n    volumes: [./x:/x]\n"})
	if got := d.Project.Services["a"].Volumes[0].Source; got != filepath.Join(dir, "x") {
		t.Errorf("content bind source %q", got)
	}

	for name, in := range map[string]composeInputs{
		"neither":      {ProjectName: "x"},
		"both":         {ProjectName: "x", Content: "services: {}", Files: []string{"a.yml"}},
		"bad name":     {ProjectName: "Bad Name", Content: "services:\n  a:\n    image: busybox\n"},
		"missing file": {Files: []string{filepath.Join(dir, "nope.yml")}},
	} {
		if _, err := composeLoad(context.Background(), in); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestComposeLoadProfilesAndOrder(t *testing.T) {
	yaml := `
services:
  web: {image: nginx, depends_on: [api]}
  api: {image: api, depends_on: {db: {condition: service_healthy}}}
  db: {image: postgres}
  debug: {image: busybox, profiles: [debug], depends_on: [web]}
`
	d := composeMustLoad(t, composeInputs{ProjectName: "p", Content: yaml})
	if _, ok := d.Project.Services["debug"]; ok {
		t.Error("debug enabled without its profile")
	}
	order, err := composeServiceOrder(d.Project)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"db", "api", "web"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order %v, want %v", order, want)
	}
	d = composeMustLoad(t, composeInputs{ProjectName: "p", Content: yaml, Profiles: []string{"debug"}})
	if order, _ := composeServiceOrder(d.Project); len(order) != 4 || order[3] != "debug" {
		t.Errorf("order with profile %v", order)
	}
	// compose-go rejects cycles itself; the ordering guards too.
	if _, err := composeLoad(context.Background(), composeInputs{WorkingDir: t.TempDir(), ProjectName: "p", Content: "services:\n  a: {image: x, depends_on: [b]}\n  b: {image: x, depends_on: [a]}\n"}); err == nil {
		d := composeMustLoad(t, composeInputs{ProjectName: "p", Content: "services:\n  a: {image: x, depends_on: [b]}\n  b: {image: x, depends_on: [a]}\n"})
		if _, err := composeServiceOrder(d.Project); err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Errorf("cycle not detected: %v", err)
		}
	}
}

const composeExpandYAML = `
services:
  web:
    image: nginx:1.27
    command: ["nginx", "-g", "daemon off;"]
    ports: ["127.0.0.1:8080:80", "9000/udp"]
    expose: ["3000"]
    environment: {A: "1", UNSET: null}
    labels: {team: web}
    restart: on-failure:3
    mem_limit: 64m
    cpus: 0.5
    healthcheck: {disable: true}
    volumes:
      - data:/data
      - type: tmpfs
        target: /tmp
        tmpfs: {size: 1048576}
    secrets:
      - source: pw
        target: custom
        uid: "100"
        gid: "101"
        mode: 0400
      - file_secret
    configs: [conf]
    extra_hosts: ["db.local:10.0.0.2"]
    networks:
      front: {aliases: [www], ipv4_address: 172.30.0.10}
      back: {}
    deploy: {replicas: 2}
  sidecar:
    image: busybox
    network_mode: service:web
    volumes_from: [web:ro]
    deploy:
      mode: global
      restart_policy: {condition: none}
      resources: {limits: {memory: 32m}}
  job:
    image: busybox
    container_name: the-job
    networks: [ext]
    deploy:
      mode: replicated-job
networks:
  front:
    ipam: {config: [{subnet: 172.30.0.0/24}]}
  back: {internal: true}
  ext: {external: true, name: traefik_public}
volumes:
  data: {driver: local, driver_opts: {type: tmpfs}}
secrets:
  pw: {environment: PW}
  file_secret: {file: ./pw.txt}
configs:
  conf: {content: "x=1"}
`

func composeExpandFixture(t *testing.T) *composeDesired {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pw.txt"), []byte("filepw"), 0o600); err != nil {
		t.Fatal(err)
	}
	return composeMustLoad(t, composeInputs{WorkingDir: dir, ProjectName: "proj", Content: composeExpandYAML, Env: map[string]string{"PW": "envpw"}})
}

func TestComposeLocalExpand(t *testing.T) {
	d := composeExpandFixture(t)
	plan, err := composeLocalExpand(d.Project)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]composeLocalContainer{}
	var names []string
	for _, c := range plan.Containers {
		byName[c.Name] = c
		names = append(names, c.Name)
	}
	// web before sidecar (network_mode service: adds a dependency).
	if want := []string{"the-job", "proj-web-1", "proj-web-2", "proj-sidecar-1"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("containers %v, want %v", names, want)
	}
	web := byName["proj-web-2"]
	if web.Config.Labels[composeLabelNumber] != "2" || web.Config.Labels[composeLabelService] != "web" || web.Config.Labels["team"] != "web" ||
		web.Config.Labels[composeLabelSpecHash] != web.Hash || len(web.Config.Labels[composeLabelConfigHash]) != 64 {
		t.Errorf("web labels %v", web.Config.Labels)
	}
	if !reflect.DeepEqual(web.Config.Env, []string{"A=1"}) {
		t.Errorf("env %v", web.Config.Env)
	}
	if web.Config.Healthcheck == nil || web.Config.Healthcheck.Test[0] != "NONE" {
		t.Errorf("healthcheck %+v", web.Config.Healthcheck)
	}
	if rp := web.Host.RestartPolicy; rp.Name != container.RestartPolicyOnFailure || rp.MaximumRetryCount != 3 {
		t.Errorf("restart %+v", rp)
	}
	if web.Host.Memory != 64<<20 || web.Host.NanoCPUs != 5e8 {
		t.Errorf("resources %d %d", web.Host.Memory, web.Host.NanoCPUs)
	}
	if pb := web.Host.PortBindings[network.MustParsePort("80/tcp")]; len(pb) != 1 || addrString(pb[0].HostIP) != "127.0.0.1" || pb[0].HostPort != "8080" {
		t.Errorf("ports %v", web.Host.PortBindings)
	}
	if pb := web.Host.PortBindings[network.MustParsePort("9000/udp")]; len(pb) != 1 || pb[0].HostPort != "" {
		t.Errorf("udp ephemeral port %v", web.Host.PortBindings)
	}
	if _, ok := web.Config.ExposedPorts[network.MustParsePort("3000/tcp")]; !ok {
		t.Errorf("exposed %v", web.Config.ExposedPorts)
	}
	if !reflect.DeepEqual(web.Host.ExtraHosts, []string{"db.local:10.0.0.2"}) {
		t.Errorf("extra hosts %v", web.Host.ExtraHosts)
	}
	// mounts: named volume, tmpfs, file secret bind
	var kinds []string
	for _, m := range web.Host.Mounts {
		kinds = append(kinds, string(m.Type)+":"+filepath.Base(m.Source)+":"+m.Target)
	}
	if want := []string{"volume:proj_data:/data", "tmpfs:.:/tmp", "bind:pw.txt:/run/secrets/file_secret"}; !reflect.DeepEqual(kinds, want) {
		t.Errorf("mounts %v, want %v", kinds, want)
	}
	if want := []composeCopy{{Path: "/run/secrets/custom", Data: []byte("envpw"), UID: 100, GID: 101, Mode: 0o400}, {Path: "/conf", Data: []byte("x=1"), Mode: 0o444}}; !reflect.DeepEqual(web.Copies, want) {
		t.Errorf("copies %+v", web.Copies)
	}
	// networks by priority then name; service name is the first alias
	if len(web.Networks) != 2 || web.Networks[0].Name != "proj_back" || web.Networks[1].Name != "proj_front" ||
		!reflect.DeepEqual(web.Networks[1].Aliases, []string{"web", "www"}) || web.Networks[1].IPv4 != "172.30.0.10" {
		t.Errorf("networks %+v", web.Networks)
	}
	if web.Host.NetworkMode != "proj_back" {
		t.Errorf("network mode %q", web.Host.NetworkMode)
	}

	side := byName["proj-sidecar-1"]
	if side.Host.NetworkMode != "container:proj-web-1" || len(side.Networks) != 0 {
		t.Errorf("sidecar network %q %v", side.Host.NetworkMode, side.Networks)
	}
	if !reflect.DeepEqual(side.Host.VolumesFrom, []string{"proj-web-1:ro"}) {
		t.Errorf("volumes_from %v", side.Host.VolumesFrom)
	}
	if side.Host.RestartPolicy.Name != container.RestartPolicyDisabled || side.Host.Memory != 32<<20 {
		t.Errorf("sidecar restart/memory %+v %d", side.Host.RestartPolicy, side.Host.Memory)
	}
	if !side.DependsOn["web"].Restart {
		t.Errorf("sidecar depends_on %+v", side.DependsOn)
	}

	// networks and volumes
	nets := map[string]composeLocalNetwork{}
	for _, n := range plan.Networks {
		nets[n.Key] = n
	}
	if !nets["ext"].External || nets["ext"].Name != "traefik_public" {
		t.Errorf("external network %+v", nets["ext"])
	}
	if f := nets["front"]; f.Create.IPAM == nil || prefixString(f.Create.IPAM.Config[0].Subnet) != "172.30.0.0/24" || f.Create.Labels[composeLabelNetwork] != "front" {
		t.Errorf("front %+v", f.Create)
	}
	if !nets["back"].Create.Internal {
		t.Error("back not internal")
	}
	if v := plan.Volumes[0]; v.Name != "proj_data" || v.Create.DriverOpts["type"] != "tmpfs" || v.Create.Labels[composeLabelVolume] != "data" {
		t.Errorf("volume %+v", v)
	}

	// Hashes are stable and track changes.
	again, _ := composeLocalExpand(d.Project)
	if again.Containers[1].Hash != plan.Containers[1].Hash {
		t.Error("hash not stable")
	}
	svc := d.Project.Services["web"]
	svc.Image = "nginx:1.28"
	d.Project.Services["web"] = svc
	changed, _ := composeLocalExpand(d.Project)
	if changed.Containers[1].Hash == plan.Containers[1].Hash || changed.Containers[0].Hash != plan.Containers[0].Hash {
		t.Error("hash does not track the image change (or changed for an unrelated service)")
	}
}

func TestComposeLocalExpandErrors(t *testing.T) {
	for name, yaml := range map[string]string{
		"external secret":  "services:\n  a:\n    image: x\n    secrets: [s]\nsecrets:\n  s: {external: true}\n",
		"name with scale":  "services:\n  a:\n    image: x\n    container_name: c\n    deploy: {replicas: 2}\n",
		"unset secret env": "services:\n  a:\n    image: x\n    secrets: [s]\nsecrets:\n  s: {environment: NOPE}\n",
		"duplicate names":  "services:\n  a: {image: x, container_name: c}\n  b: {image: x, container_name: c}\n",
	} {
		t.Run(name, func(t *testing.T) {
			p, err := composeLoad(context.Background(), composeInputs{WorkingDir: t.TempDir(), ProjectName: "p", Content: yaml})
			if err != nil {
				return // rejected by the loader already
			}
			if _, err := composeLocalExpand(p); err == nil {
				t.Error("expected an error")
			}
		})
	}
	// build: built through the BuildKit seam, no warning; build-only
	// services run <project>-<service>.
	d := composeMustLoad(t, composeInputs{ProjectName: "p", Content: "services:\n  a: {image: x, build: .}\n  b: {build: .}\n"})
	plan, err := composeLocalExpand(d.Project)
	if err != nil || len(plan.Warnings) != 0 {
		t.Fatalf("err %v warnings %v", err, plan.Warnings)
	}
	for _, c := range plan.Containers {
		if c.Build == nil {
			t.Errorf("%s: no build", c.Name)
		}
		if c.Service == "b" && c.Config.Image != "p-b" {
			t.Errorf("build-only image %q", c.Config.Image)
		}
	}
}

func TestComposeLocalDrift(t *testing.T) {
	d := composeMustLoad(t, composeInputs{ProjectName: "p", Content: `
services:
  web:
    image: nginx
    restart: always
    volumes:
      - data:/v
  once: {image: busybox, restart: "no"}
volumes:
  data: {}
`})
	plan, err := composeLocalExpand(d.Project)
	if err != nil {
		t.Fatal(err)
	}
	live := func() composeLocalExisting {
		ex := composeLocalExisting{containers: map[string]container.Summary{}, networks: map[string]network.Summary{}, volumes: map[string]*volume.Volume{}}
		for _, c := range plan.Containers {
			ex.containers[c.Name] = container.Summary{ID: c.Name, Labels: c.Config.Labels, State: container.StateRunning}
		}
		for _, n := range plan.Networks {
			ex.networks[n.Name] = network.Summary{ID: n.Name, Name: n.Name, Labels: n.Create.Labels}
		}
		for _, v := range plan.Volumes {
			ex.volumes[v.Name] = &volume.Volume{Name: v.Name, Labels: v.Create.Labels}
		}
		return ex
	}
	if got := composeLocalDrift(plan, live()); len(got) != 0 {
		t.Errorf("no drift expected: %v", got)
	}
	ex := live()
	ex.containers["p-once-1"] = container.Summary{ID: "o", Labels: ex.containers["p-once-1"].Labels, State: container.StateExited, Status: "Exited (0) 3 seconds ago"}
	if got := composeLocalDrift(plan, ex); len(got) != 0 {
		t.Errorf("one-shot exit 0 is not drift: %v", got)
	}
	ex.containers["p-web-1"] = container.Summary{ID: "w", Labels: ex.containers["p-web-1"].Labels, State: container.StateExited, Status: "Exited (0) 3 seconds ago"}
	delete(ex.volumes, "p_data")
	ex.networks["p_old"] = network.Summary{Name: "p_old"}
	ex.containers["p-gone-1"] = container.Summary{ID: "g", State: container.StateRunning}
	got := composeLocalDrift(plan, ex)
	want := []string{"network p_old is not in the configuration", "volume p_data is missing", "container p-web-1 is stopped", "container p-gone-1 is not in the configuration"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("drift\n got %q\nwant %q", got, want)
	}
	if composeExitCode("Exited (137) 2 minutes ago") != 137 {
		t.Error("exit code parse")
	}
}

func TestComposeTar(t *testing.T) {
	r, err := composeTar([]composeCopy{{Path: "/run/secrets/a", Data: []byte("x"), Mode: 0o400, UID: 1}})
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 512)
	if _, err := r.Read(b); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "run/secrets/a") {
		t.Errorf("tar header name %q", b[:20])
	}
}

func TestComposeSwarmExpand(t *testing.T) {
	d := composeExpandFixture(t)
	plan, err := composeSwarmExpand(d.Project)
	if err != nil {
		t.Fatal(err)
	}
	svcs := map[string]composeSwarmService{}
	for _, s := range plan.Services {
		svcs[s.Key] = s
	}
	web := svcs["web"]
	cs := web.Spec.TaskTemplate.ContainerSpec
	if web.Name != "proj_web" || web.Mode != "replicated" || *web.Spec.Mode.Replicated.Replicas != 2 {
		t.Errorf("web %s %s %+v", web.Name, web.Mode, web.Spec.Mode)
	}
	if web.Spec.Labels[labelStackNamespace] != "proj" || cs.Labels[labelStackNamespace] != "proj" || cs.Labels["team"] != "web" ||
		web.Spec.Labels[composeLabelConfigHash] != web.Hash {
		t.Errorf("labels %v / %v", web.Spec.Labels, cs.Labels)
	}
	if !reflect.DeepEqual(cs.Args, []string{"nginx", "-g", "daemon off;"}) || cs.Command != nil {
		t.Errorf("command/args %v %v", cs.Command, cs.Args)
	}
	if !reflect.DeepEqual(cs.Hosts, []string{"10.0.0.2 db.local"}) {
		t.Errorf("hosts %v", cs.Hosts)
	}
	if rp := web.Spec.TaskTemplate.RestartPolicy; rp.Condition != swarm.RestartPolicyConditionOnFailure || *rp.MaxAttempts != 3 {
		t.Errorf("restart %+v", rp)
	}
	if l := web.Spec.TaskTemplate.Resources.Limits; l.MemoryBytes != 64<<20 || l.NanoCPUs != 5e8 {
		t.Errorf("limits from mem_limit/cpus %+v", l)
	}
	if s := cs.Secrets; len(s) != 2 || s[0].SecretName != "proj_pw" || s[0].File.Name != "custom" || s[0].File.UID != "100" || s[0].File.Mode != 0o400 ||
		s[1].SecretName != "proj_file_secret" || s[1].File.Name != "file_secret" {
		t.Errorf("secrets %+v %+v", s[0].File, s[1].File)
	}
	if c := cs.Configs; len(c) != 1 || c[0].ConfigName != "proj_conf" || c[0].File.Name != "/conf" {
		t.Errorf("configs %+v", c)
	}
	if m := cs.Mounts; m[0].Source != "proj_data" || m[0].VolumeOptions.DriverConfig.Options["type"] != "tmpfs" || m[1].Type != mount.TypeTmpfs {
		t.Errorf("mounts %+v", m)
	}
	if n := web.Spec.TaskTemplate.Networks; len(n) != 2 || n[1].Target != "proj_front" || !reflect.DeepEqual(n[1].Aliases, []string{"web", "www"}) {
		t.Errorf("networks %+v", n)
	}
	if p := web.Spec.EndpointSpec.Ports; len(p) != 2 || p[0].PublishedPort != 8080 || p[1].Protocol != "udp" || p[1].PublishedPort != 0 {
		t.Errorf("ports %+v", p)
	}
	if svcs["sidecar"].Mode != "global" || svcs["sidecar"].Spec.TaskTemplate.RestartPolicy.Condition != swarm.RestartPolicyConditionNone {
		t.Errorf("sidecar %+v", svcs["sidecar"].Spec.Mode)
	}
	if j := svcs["job"]; j.Mode != "replicated_job" || *j.Spec.Mode.ReplicatedJob.TotalCompletions != 1 {
		t.Errorf("job %+v", j.Spec.Mode)
	}
	// Network driver defaults to overlay; external objects are not created.
	for _, n := range plan.Networks {
		if n.Key == "ext" && (!n.External || n.Name != "traefik_public") {
			t.Errorf("ext %+v", n)
		}
		if n.Key == "front" && (n.Create.Driver != "overlay" || n.Create.Scope != "swarm") {
			t.Errorf("front %+v", n.Create)
		}
	}
	for _, s := range plan.Secrets {
		want := map[string]string{"pw": "envpw", "file_secret": "filepw"}[s.Key]
		if string(s.Data) != want || s.Labels[labelStackNamespace] != "proj" {
			t.Errorf("secret %s data %q labels %v", s.Key, s.Data, s.Labels)
		}
	}
	// Warnings for what swarm ignores.
	joined := strings.Join(plan.Warnings, "\n")
	for _, w := range []string{"network_mode", "volumes_from", "container_name"} {
		if !strings.Contains(joined, w) {
			t.Errorf("no warning for %s in %q", w, joined)
		}
	}

	// Name references resolve to IDs at apply time.
	lk := &svcLookup{
		networks: []namedObject{{ID: "n1", Name: "proj_back"}, {ID: "n2", Name: "proj_front"}},
		secrets:  []namedObject{{ID: "s1", Name: "proj_pw"}, {ID: "s2", Name: "proj_file_secret"}},
		configs:  []namedObject{{ID: "c1", Name: "proj_conf"}},
	}
	spec := web.Spec
	if err := composeSwarmResolve(&spec, lk); err != nil {
		t.Fatal(err)
	}
	if spec.TaskTemplate.Networks[1].Target != "n2" || spec.TaskTemplate.ContainerSpec.Secrets[0].SecretID != "s1" || spec.TaskTemplate.ContainerSpec.Configs[0].ConfigID != "c1" {
		t.Errorf("resolved %+v", spec.TaskTemplate)
	}
	if web.Spec.TaskTemplate.Networks[1].Target != "proj_front" || web.Spec.TaskTemplate.ContainerSpec.Secrets[0].SecretID != "" {
		t.Error("resolve mutated the plan's spec")
	}
}

func TestComposeSwarmDrift(t *testing.T) {
	d := composeMustLoad(t, composeInputs{ProjectName: "s", Content: "services:\n  web: {image: nginx:1.27, secrets: [x], deploy: {replicas: 2}}\nsecrets:\n  x: {environment: X}\n", Env: map[string]string{"X": "a"}})
	plan, err := composeSwarmExpand(d.Project)
	if err != nil {
		t.Fatal(err)
	}
	ex := composeSwarmByName{services: map[string]swarm.Service{}, networks: map[string]network.Summary{}, secrets: map[string]swarm.Secret{}, configs: map[string]swarm.Config{}}
	for _, s := range plan.Services {
		spec := s.Spec
		cs := *spec.TaskTemplate.ContainerSpec
		cs.Image += "@sha256:" + strings.Repeat("a", 64) // the daemon pins digests
		spec.TaskTemplate.ContainerSpec = &cs
		ex.services[s.Name] = swarm.Service{ID: "id-" + s.Key, Spec: spec}
	}
	for _, n := range plan.Networks {
		ex.networks[n.Name] = network.Summary{ID: "n", Name: n.Name, Labels: n.Create.Labels}
	}
	if got := composeSwarmDrift(plan, ex); len(got) != 1 || got[0] != "secret s_x is missing" {
		t.Errorf("drift %v", got)
	}
	ex.secrets["s_x"] = swarm.Secret{ID: "x", Spec: swarm.SecretSpec{Annotations: swarm.Annotations{Name: "s_x", Labels: plan.Secrets[0].Labels}}}
	if got := composeSwarmDrift(plan, ex); len(got) != 0 {
		t.Errorf("no drift expected: %v", got)
	}
	web := ex.services["s_web"]
	three := uint64(3)
	web.Spec.Mode.Replicated = &swarm.ReplicatedService{Replicas: &three}
	ex.services["s_web"] = web
	ex.configs["s_stale"] = swarm.Config{ID: "c", Spec: swarm.ConfigSpec{Annotations: swarm.Annotations{Name: "s_stale"}}}
	got := composeSwarmDrift(plan, ex)
	want := []string{"config s_stale is not in the configuration", "service s_web has 3 replicas instead of 2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("drift\n got %q\nwant %q", got, want)
	}
	svcs, nets, _ := composeSwarmIDs(plan, "s", ex)
	if !reflect.DeepEqual(svcs["web"], []string{"id-web"}) || nets["default"] != "n" {
		t.Errorf("ids %v %v", svcs, nets)
	}
}
