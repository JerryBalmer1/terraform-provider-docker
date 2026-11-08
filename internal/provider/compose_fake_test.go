package provider

// A fake single-node Engine for docker_compose local mode: containers,
// networks, volumes, images and archive uploads, enough for the resource's
// create/refresh/update/delete paths.

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

type composeFakeContainer struct {
	ID      string
	Name    string
	Config  container.Config
	Host    container.HostConfig
	State   string // created, running, exited
	Exit    int
	Nets    map[string]*network.EndpointSettings // by network name
	Files   map[string]string                    // archive uploads
	Started int
}

type composeFakeEngine struct {
	mu         sync.Mutex
	seq        int
	containers map[string]*composeFakeContainer
	networks   map[string]*network.Summary
	volumes    map[string]*volume.Volume
	images     map[string]bool
	pulls      map[string]int
	creates    map[string]int // ContainerCreate per name
}

func newComposeFakeEngine(t *testing.T) (*composeFakeEngine, string) {
	f := &composeFakeEngine{
		containers: map[string]*composeFakeContainer{},
		networks:   map[string]*network.Summary{},
		volumes:    map[string]*volume.Volume{},
		images:     map[string]bool{},
		pulls:      map[string]int{},
		creates:    map[string]int{},
	}
	// Pre-existing objects a project may reference as external.
	f.networks["ext0000000000000001"] = &network.Summary{ID: "ext0000000000000001", Name: "shared", Driver: "bridge", Scope: "local"}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, "tcp://" + srv.Listener.Addr().String()
}

func (f *composeFakeEngine) id(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s%063d", prefix, f.seq)
}

func (f *composeFakeEngine) findContainer(ref string) *composeFakeContainer {
	if c, ok := f.containers[ref]; ok {
		return c
	}
	for _, c := range f.containers {
		if c.Name == ref || strings.HasPrefix(c.ID, ref) {
			return c
		}
	}
	return nil
}

func (f *composeFakeEngine) byName(name string) *composeFakeContainer {
	for _, c := range f.containers {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (f *composeFakeEngine) findNetwork(ref string) *network.Summary {
	if n, ok := f.networks[ref]; ok {
		return n
	}
	for _, n := range f.networks {
		if n.Name == ref {
			return n
		}
	}
	return nil
}

func (f *composeFakeEngine) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := svcFakeVersionRe.ReplaceAllString(r.URL.Path, "")
	w.Header().Set("Api-Version", "1.47")
	switch {
	case p == "/_ping":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("OK"))
	case strings.HasPrefix(p, "/containers/"):
		f.handleContainers(w, r, strings.Split(strings.TrimPrefix(p, "/containers/"), "/"))
	case p == "/networks" || strings.HasPrefix(p, "/networks/"):
		f.handleNetworks(w, r, strings.TrimPrefix(strings.TrimPrefix(p, "/networks"), "/"))
	case p == "/volumes" || strings.HasPrefix(p, "/volumes/"):
		f.handleVolumes(w, r, strings.TrimPrefix(strings.TrimPrefix(p, "/volumes"), "/"))
	case p == "/images/create" && r.Method == http.MethodPost:
		ref := strings.TrimPrefix(strings.TrimPrefix(r.URL.Query().Get("fromImage"), "docker.io/"), "library/")
		if tag := r.URL.Query().Get("tag"); tag != "" {
			ref += ":" + tag
		}
		ref = composeFakeImageRef(ref)
		f.images[ref] = true
		f.pulls[ref]++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"Pulled"}` + "\n"))
	case strings.HasPrefix(p, "/images/") && strings.HasSuffix(p, "/json"):
		ref := strings.TrimSuffix(strings.TrimPrefix(p, "/images/"), "/json")
		if !f.images[composeFakeImageRef(ref)] {
			svcFakeErr(w, 404, "No such image: "+ref)
			return
		}
		svcFakeJSON(w, 200, image.InspectResponse{ID: "sha256:" + strings.Repeat("b", 64), RepoTags: []string{ref}})
	default:
		svcFakeErr(w, 404, "compose fake engine: no route "+r.Method+" "+p)
	}
}

func (f *composeFakeEngine) summary(c *composeFakeContainer) container.Summary {
	s := container.Summary{ID: c.ID, Names: []string{"/" + c.Name}, Image: c.Config.Image, Labels: c.Config.Labels,
		State: container.ContainerState(c.State), NetworkSettings: &container.NetworkSettingsSummary{Networks: c.Nets}}
	switch c.State {
	case "running":
		s.Status = "Up 1 second"
	case "exited":
		s.Status = fmt.Sprintf("Exited (%d) 1 second ago", c.Exit)
	default:
		s.Status = "Created"
	}
	return s
}

func (f *composeFakeEngine) handleContainers(w http.ResponseWriter, r *http.Request, parts []string) {
	switch {
	case parts[0] == "json" && r.Method == http.MethodGet:
		out := []container.Summary{}
		for _, c := range f.containers {
			if (c.State == "running" || r.URL.Query().Get("all") == "1") && svcFakeLabelsMatch(r, c.Config.Labels) {
				out = append(out, f.summary(c))
			}
		}
		svcFakeJSON(w, 200, out)
	case parts[0] == "create" && r.Method == http.MethodPost:
		var req container.CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			svcFakeErr(w, 400, err.Error())
			return
		}
		name := r.URL.Query().Get("name")
		if f.byName(name) != nil {
			svcFakeErr(w, 409, "Conflict. The container name \"/"+name+"\" is already in use")
			return
		}
		if !f.images[composeFakeImageRef(req.Image)] {
			svcFakeErr(w, 404, "No such image: "+req.Image)
			return
		}
		c := &composeFakeContainer{ID: f.id("c"), Name: name, Config: *req.Config, State: "created", Nets: map[string]*network.EndpointSettings{}, Files: map[string]string{}}
		if req.HostConfig != nil {
			c.Host = *req.HostConfig
		}
		if req.NetworkingConfig != nil {
			for n, ep := range req.NetworkingConfig.EndpointsConfig {
				net := f.findNetwork(n)
				if net == nil {
					svcFakeErr(w, 404, "network "+n+" not found")
					return
				}
				e := *ep
				e.NetworkID = net.ID
				c.Nets[net.Name] = &e
			}
		}
		f.containers[c.ID] = c
		f.creates[name]++
		svcFakeJSON(w, 201, container.CreateResponse{ID: c.ID})
	case len(parts) == 2 && parts[1] == "json":
		c := f.findContainer(parts[0])
		if c == nil {
			svcFakeErr(w, 404, "No such container: "+parts[0])
			return
		}
		st := &container.State{Status: container.ContainerState(c.State), Running: c.State == "running", ExitCode: c.Exit}
		if c.Config.Healthcheck != nil && len(c.Config.Healthcheck.Test) > 0 && c.Config.Healthcheck.Test[0] != "NONE" {
			st.Health = &container.Health{Status: container.Healthy}
		}
		cfg := c.Config
		svcFakeJSON(w, 200, container.InspectResponse{
			ID: c.ID, Name: "/" + c.Name, State: st, HostConfig: &c.Host,
			Config:          &cfg,
			NetworkSettings: &container.NetworkSettings{Networks: c.Nets},
		})
	case len(parts) == 2 && parts[1] == "start":
		c := f.findContainer(parts[0])
		if c == nil {
			svcFakeErr(w, 404, "No such container")
			return
		}
		c.State = "running"
		c.Started++
		w.WriteHeader(204)
	case len(parts) == 2 && parts[1] == "archive" && r.Method == http.MethodPut:
		c := f.findContainer(parts[0])
		if c == nil {
			svcFakeErr(w, 404, "No such container")
			return
		}
		tr := tar.NewReader(r.Body)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				svcFakeErr(w, 400, err.Error())
				return
			}
			b, _ := io.ReadAll(tr)
			c.Files[r.URL.Query().Get("path")+h.Name] = fmt.Sprintf("%s|%o|%d:%d", b, h.Mode, h.Uid, h.Gid)
		}
		w.WriteHeader(200)
	case len(parts) == 1 && r.Method == http.MethodDelete:
		c := f.findContainer(parts[0])
		if c == nil {
			svcFakeErr(w, 404, "No such container")
			return
		}
		if c.State == "running" && r.URL.Query().Get("force") != "1" {
			svcFakeErr(w, 409, "container is running")
			return
		}
		delete(f.containers, c.ID)
		w.WriteHeader(204)
	default:
		svcFakeErr(w, 404, "compose fake engine: containers route "+r.Method+" "+strings.Join(parts, "/"))
	}
}

func (f *composeFakeEngine) handleNetworks(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(rest, "/")
	switch {
	case rest == "" && r.Method == http.MethodGet:
		out := []network.Summary{}
		for _, n := range f.networks {
			if svcFakeLabelsMatch(r, n.Labels) {
				out = append(out, *n)
			}
		}
		svcFakeJSON(w, 200, out)
	case rest == "create":
		var req network.CreateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if f.findNetwork(req.Name) != nil {
			svcFakeErr(w, 409, "network with name "+req.Name+" already exists")
			return
		}
		id := f.id("n")
		f.networks[id] = &network.Summary{ID: id, Name: req.Name, Driver: svcOr(req.Driver, "bridge"), Scope: "local",
			Internal: req.Internal, Attachable: req.Attachable, Labels: req.Labels, Options: req.Options}
		svcFakeJSON(w, 201, network.CreateResponse{ID: id})
	case len(parts) == 2 && parts[1] == "connect":
		n := f.findNetwork(parts[0])
		if n == nil {
			svcFakeErr(w, 404, "network not found")
			return
		}
		var req client.NetworkConnectOptions
		_ = json.NewDecoder(r.Body).Decode(&req)
		c := f.findContainer(req.Container)
		if c == nil {
			svcFakeErr(w, 404, "No such container")
			return
		}
		ep := &network.EndpointSettings{NetworkID: n.ID}
		if req.EndpointConfig != nil {
			e := *req.EndpointConfig
			e.NetworkID = n.ID
			ep = &e
		}
		c.Nets[n.Name] = ep
		w.WriteHeader(200)
	case len(parts) == 1 && r.Method == http.MethodGet:
		n := f.findNetwork(parts[0])
		if n == nil {
			svcFakeErr(w, 404, "network "+parts[0]+" not found")
			return
		}
		svcFakeJSON(w, 200, network.Inspect{ID: n.ID, Name: n.Name, Driver: n.Driver, Scope: n.Scope, Labels: n.Labels})
	case len(parts) == 1 && r.Method == http.MethodDelete:
		n := f.findNetwork(parts[0])
		if n == nil {
			svcFakeErr(w, 404, "network not found")
			return
		}
		for _, c := range f.containers {
			if _, ok := c.Nets[n.Name]; ok {
				svcFakeErr(w, 403, "network has active endpoints")
				return
			}
		}
		delete(f.networks, n.ID)
		w.WriteHeader(204)
	default:
		svcFakeErr(w, 404, "compose fake engine: networks route")
	}
}

func (f *composeFakeEngine) handleVolumes(w http.ResponseWriter, r *http.Request, rest string) {
	switch {
	case rest == "" && r.Method == http.MethodGet:
		out := volume.ListResponse{Volumes: []volume.Volume{}}
		for _, v := range f.volumes {
			if svcFakeLabelsMatch(r, v.Labels) {
				out.Volumes = append(out.Volumes, *v)
			}
		}
		svcFakeJSON(w, 200, out)
	case rest == "create":
		var req client.VolumeCreateOptions
		_ = json.NewDecoder(r.Body).Decode(&req)
		if _, ok := f.volumes[req.Name]; ok {
			svcFakeErr(w, 409, "volume exists")
			return
		}
		v := &volume.Volume{Name: req.Name, Driver: svcOr(req.Driver, "local"), Labels: req.Labels, Options: req.DriverOpts, Scope: "local"}
		f.volumes[req.Name] = v
		svcFakeJSON(w, 201, v)
	case r.Method == http.MethodGet:
		v, ok := f.volumes[rest]
		if !ok {
			svcFakeErr(w, 404, "no such volume")
			return
		}
		svcFakeJSON(w, 200, v)
	case r.Method == http.MethodDelete:
		if _, ok := f.volumes[rest]; !ok {
			svcFakeErr(w, 404, "no such volume")
			return
		}
		for _, c := range f.containers {
			for _, m := range c.Host.Mounts {
				if m.Source == rest {
					svcFakeErr(w, 409, "volume is in use")
					return
				}
			}
		}
		delete(f.volumes, rest)
		w.WriteHeader(204)
	default:
		svcFakeErr(w, 404, "compose fake engine: volumes route")
	}
}

// projectObjects counts what is labelled with project.
func (f *composeFakeEngine) projectObjects(project string) (containers, networks, volumes int) {
	for _, c := range f.containers {
		if c.Config.Labels[composeLabelProject] == project {
			containers++
		}
	}
	for _, n := range f.networks {
		if n.Labels[composeLabelProject] == project {
			networks++
		}
	}
	for _, v := range f.volumes {
		if v.Labels[composeLabelProject] == project {
			volumes++
		}
	}
	return
}

// composeFakeImageRef normalises an image reference the way the daemon's
// image store would match it (implicit :latest, no docker.io/library/).
func composeFakeImageRef(ref string) string {
	ref = strings.TrimPrefix(strings.TrimPrefix(ref, "docker.io/"), "library/")
	if !strings.Contains(ref[strings.LastIndex(ref, "/")+1:], ":") && !strings.Contains(ref, "@") {
		ref += ":latest"
	}
	return ref
}
