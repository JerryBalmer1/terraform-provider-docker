package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	ctypes "github.com/compose-spec/compose-go/v2/types"
)

// TestComposeHashCompat pins the docker_compose config hashes that the
// docker/docker v28 client produced (captured at 51e266b, before the move to
// github.com/moby/moby) for every fixture in testdata/compose_hash. A changed
// digest means every object docker_compose created with the old hash is
// recreated on the next apply (containers lose their state, volumes their
// data). Do not re-pin a digest to make this pass: fix the hash shape in
// compose_hash_compat.go instead.
//
// Paths: the project working directory reaches the hashes (the
// com.docker.compose.project.working_dir container label, bind mount and
// file secret/config sources). Each fixture loads from a fresh t.TempDir(),
// then composeHashCompatNormalize rewrites that directory to the fixed
// composeHashCompatRoot, with forward slashes, everywhere in the project,
// so the digests are the same on every machine and OS. Swarm mode reads
// file secrets/configs while expanding (only their content is hashed), so
// it gets a project whose top-level secret/config file paths are left real.
func TestComposeHashCompat(t *testing.T) {
	names := make([]string, 0, len(composeHashCompatFixtures))
	for n := range composeHashCompatFixtures {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) != len(composeHashCompatPinned) {
		t.Errorf("%d fixtures but %d pinned digest sets", len(names), len(composeHashCompatPinned))
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			want, ok := composeHashCompatPinned[name]
			if !ok {
				t.Fatalf("no pinned digests for fixture %s", name)
			}
			got := composeHashCompatCompute(t, composeHashCompatFixtures[name])
			for _, k := range composeSortedKeys(want) {
				if g, ok := got[k]; !ok {
					t.Errorf("%s: missing (v28 digest %s)", k, want[k])
				} else if g != want[k] {
					t.Errorf("%s: digest changed\n  v28:  %s\n  moby: %s", k, want[k], g)
				}
			}
			for _, k := range composeSortedKeys(got) {
				if _, ok := want[k]; !ok {
					t.Errorf("%s: new object %s not in the v28 set", k, got[k])
				}
			}
		})
	}
}

// composeHashCompatRoot replaces the per-test working directory in paths.
const composeHashCompatRoot = "/compose-hash"

type composeHashCompatFixture struct {
	File     string            // under testdata/compose_hash
	Project  string            // composeInputs.ProjectName ("" = the file's name:)
	Env      map[string]string // composeInputs.Env
	Profiles []string          // composeInputs.Profiles
	Files    map[string]string // written into the working directory first
}

var composeHashCompatFixtures = map[string]composeHashCompatFixture{
	"example_shop": {File: "example_shop.yml", Project: "shop",
		Env: map[string]string{"WEB_TAG": "1.27-alpine", "DB_PASSWORD": "change-me"}},
	"example_stack": {File: "example_stack.yml", Project: "shopstack"},
	"c2s_minimal":   {File: "c2s_minimal.yml", Project: "minimal"},
	"c2s_webapp": {File: "c2s_webapp.yml", Project: "webapp",
		Env:   map[string]string{"API_KEY": "api-key-value"},
		Files: map[string]string{"secrets/db_password.txt": "db-password", "nginx.conf": "server { listen 80; }\n"}},
	"c2s_webapp_debug": {File: "c2s_webapp.yml", Project: "webapp", Profiles: []string{"debug"},
		Env:   map[string]string{"API_KEY": "api-key-value"},
		Files: map[string]string{"secrets/db_password.txt": "db-password", "nginx.conf": "server { listen 80; }\n"}},
	"unit_expand": {File: "unit_expand.yml", Project: "proj",
		Env: map[string]string{"PW": "envpw"}, Files: map[string]string{"pw.txt": "filepw"}},
	"unit_profiles":       {File: "unit_profiles.yml", Project: "p"},
	"unit_profiles_debug": {File: "unit_profiles.yml", Project: "p", Profiles: []string{"debug"}},
	"unit_drift":          {File: "unit_drift.yml", Project: "p"},
	"unit_build_image":    {File: "unit_build_image.yml", Project: "p"},
	"unit_resource_shop": {File: "unit_resource_shop.yml",
		Env: map[string]string{"DB_PW": "s3cret"}, Files: map[string]string{"site.conf": "server { listen 80; }"}},
	"kitchen_sink": {File: "kitchen_sink.yml", Project: "sink", Profiles: []string{"admin"},
		Env:   map[string]string{"TAG": "2.1.0", "API_KEY": "api-key-value", "FLAGS": "fast=true", "TOKEN": "static-token"},
		Files: map[string]string{"tls.key": "-----BEGIN KEY-----\nnot-a-key\n-----END KEY-----\n", "api.yaml": "listen: 8080\n"}},
}

// composeHashCompatCompute returns "<mode> <kind> <name>" -> config hash for
// every object composeLocalExpand and composeSwarmExpand plan. A mode that
// fails to expand yields "<mode> error" -> "error".
func composeHashCompatCompute(t *testing.T, fx composeHashCompatFixture) map[string]string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "compose_hash", fx.File))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for rel, data := range fx.Files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	load := func(keepBlobFiles bool) *ctypes.Project {
		d := composeMustLoad(t, composeInputs{WorkingDir: dir, ProjectName: fx.Project, Content: string(content), Env: fx.Env, Profiles: fx.Profiles})
		composeHashCompatNormalize(t, d.Project, dir, keepBlobFiles)
		return d.Project
	}
	out := map[string]string{}
	if lp, err := composeLocalExpand(load(false)); err != nil {
		out["local error"] = "error"
	} else {
		for _, n := range lp.Networks {
			if !n.External {
				out["local network "+n.Name] = n.Hash
			}
		}
		for _, v := range lp.Volumes {
			if !v.External {
				out["local volume "+v.Name] = v.Hash
			}
		}
		for _, c := range lp.Containers {
			out["local container "+c.Name] = c.Hash
		}
	}
	if sp, err := composeSwarmExpand(load(true)); err != nil {
		out["swarm error"] = "error"
	} else {
		for _, n := range sp.Networks {
			if !n.External {
				out["swarm network "+n.Name] = n.Hash
			}
		}
		for _, b := range sp.Secrets {
			if !b.External {
				out["swarm secret "+b.Name] = b.Hash
			}
		}
		for _, b := range sp.Configs {
			if !b.External {
				out["swarm config "+b.Name] = b.Hash
			}
		}
		for _, s := range sp.Services {
			out["swarm service "+s.Name] = s.Hash
		}
	}
	return out
}

// composeHashCompatNormalize rewrites dir to composeHashCompatRoot in every
// string of p. With keepBlobFiles, top-level secret and config file paths
// stay real so swarm expansion can read them.
func composeHashCompatNormalize(t *testing.T, p *ctypes.Project, dir string, keepBlobFiles bool) {
	t.Helper()
	prefixes := []string{dir, filepath.ToSlash(dir)}
	if ev, err := filepath.EvalSymlinks(dir); err == nil && ev != dir {
		prefixes = append(prefixes, ev, filepath.ToSlash(ev))
	}
	fix := func(s string) string {
		for _, pre := range prefixes {
			if strings.Contains(s, pre) {
				return strings.ReplaceAll(strings.ReplaceAll(s, pre, composeHashCompatRoot), `\`, "/")
			}
		}
		return s
	}
	keep := map[string]string{}
	if keepBlobFiles {
		for k, v := range p.Secrets {
			keep["s/"+k] = v.File
		}
		for k, v := range p.Configs {
			keep["c/"+k] = v.File
		}
	}
	composeHashCompatWalk(reflect.ValueOf(p).Elem(), fix)
	if keepBlobFiles {
		for k, v := range p.Secrets {
			v.File = keep["s/"+k]
			p.Secrets[k] = v
		}
		for k, v := range p.Configs {
			v.File = keep["c/"+k]
			p.Configs[k] = v
		}
		return
	}
	// Nothing machine-specific may be left once every path is rewritten.
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	flat := strings.ToLower(strings.ReplaceAll(string(b), `\\`, "/"))
	if strings.Contains(flat, strings.ToLower(filepath.ToSlash(dir))) {
		t.Fatalf("working directory %s survives normalisation: %s", dir, b)
	}
}

// composeHashCompatWalk applies fix to every settable string reachable from
// the addressable value v (map elements and interface values are copied,
// rewritten and stored back).
func composeHashCompatWalk(v reflect.Value, fix func(string) string) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(fix(v.String()))
		}
	case reflect.Pointer:
		if !v.IsNil() {
			composeHashCompatWalk(v.Elem(), fix)
		}
	case reflect.Interface:
		if v.IsNil() || !v.CanSet() {
			return
		}
		c := reflect.New(v.Elem().Type()).Elem()
		c.Set(v.Elem())
		composeHashCompatWalk(c, fix)
		v.Set(c)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				composeHashCompatWalk(v.Field(i), fix)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			composeHashCompatWalk(v.Index(i), fix)
		}
	case reflect.Map:
		if v.IsNil() {
			return
		}
		for _, k := range v.MapKeys() {
			c := reflect.New(v.Type().Elem()).Elem()
			c.Set(v.MapIndex(k))
			composeHashCompatWalk(c, fix)
			v.SetMapIndex(k, c)
		}
	}
}

// composeHashCompatPinned is fixture -> object -> config hash, captured with
// the docker/docker v28 client at 51e266b. Never re-pin these.
var composeHashCompatPinned = map[string]map[string]string{
	"c2s_minimal": {
		"local container minimal-app-1": "fe2bd83946d6b483b29363b14598bca123e712f25e6c812615e059366b735c53",
		"swarm service minimal_app":     "59d4c2f95ea0a61168fd5d35c1303d1fb852ce92414899fc0f628c941e169761",
	},
	"c2s_webapp": {
		"local container webapp-cache-1":  "fa55d9a03870861a111a61fe7a186c299a5a4c097a5a5204a9c9317128a9001a",
		"local container webapp-cache-2":  "4bef7b0a301cdb4f00e3bd610aa2a3380f9d6d9a01d542c3c03418bc94e588bd",
		"local container webapp-db-1":     "303a2e0ceea23e49e6eb184b84507903750ec280c727cdac2abc3ebbfc30711a",
		"local container webapp-web-1":    "1d1b95c1e40dff567e022533f8e02cc18af6d3007e32d1f0e47e9da7e4ec83a4",
		"local container webapp-web-2":    "64da0e2545936ee2f4c7b6e8b2a4fe8370df09ff2fdcb793cbe4647f720ca5c4",
		"local container webapp-web-3":    "800814819c7fcb210a56a887fb7cf89bae54871b716b7d32947296043cbc6a37",
		"local network webapp_back":       "e47b2b2a606188a838ebb5b4411b18b884de8acc282adbdedcb9957009030eba",
		"local network webapp_front":      "c44c424e2616ab24c5388d25dfaf3c3cf543dc592f3fb3584b81ea381ef5192b",
		"local volume webapp_pgdata":      "cf7bb7408fa07085330b5c16465616a6a74c23d5bcb62fe9c486a8079db623bc",
		"swarm config webapp_banner":      "1fcdbda3178fbf2e55861353d9a0a235c569749a3cb44c2ab3a36b0620e82dca",
		"swarm config webapp_nginx_conf":  "b0477d6969f82b32e61695c88eb27986d35192fc9bf03086b5a42ac37b2efb3c",
		"swarm network webapp_back":       "acb9d7435037ff2bc2ca53b1a606a8b101b11071c0089fec4c2582472aa1d7be",
		"swarm network webapp_front":      "b099969d1d1c02def174403f5ed7f170e6f6425378da44f3ea23e4c2e419c563",
		"swarm secret webapp_api_key":     "bce5f0858e79e8e4c2f95afeb7497cea0c8cb2d5f34c0032d6fe65424b2465f3",
		"swarm secret webapp_db_password": "920a2bddfd6252c5c969528dcecd4858b3ff8aba33490e40934de9bff9192f0b",
		"swarm service webapp_cache":      "47e822907bdf7b0e56bcbd1f71ce821feb39a18e61618736fd48a244e025873e",
		"swarm service webapp_db":         "d4b9be3f4fca4ae3b4068d3e05916eb8f191fed8fe35936b5fdd913365688485",
		"swarm service webapp_web":        "39ca8fb04d5e955a084dac2db7351ca1949f6c057c59dac132fb08ac1c1cd369",
	},
	"c2s_webapp_debug": {
		"local container webapp-cache-1":  "fa55d9a03870861a111a61fe7a186c299a5a4c097a5a5204a9c9317128a9001a",
		"local container webapp-cache-2":  "4bef7b0a301cdb4f00e3bd610aa2a3380f9d6d9a01d542c3c03418bc94e588bd",
		"local container webapp-db-1":     "303a2e0ceea23e49e6eb184b84507903750ec280c727cdac2abc3ebbfc30711a",
		"local container webapp-debug-1":  "edda99860f52ba56b7df55dfa8f48ef88a5040f98e4a63113afbde8c726cb176",
		"local container webapp-web-1":    "1d1b95c1e40dff567e022533f8e02cc18af6d3007e32d1f0e47e9da7e4ec83a4",
		"local container webapp-web-2":    "64da0e2545936ee2f4c7b6e8b2a4fe8370df09ff2fdcb793cbe4647f720ca5c4",
		"local container webapp-web-3":    "800814819c7fcb210a56a887fb7cf89bae54871b716b7d32947296043cbc6a37",
		"local network webapp_back":       "e47b2b2a606188a838ebb5b4411b18b884de8acc282adbdedcb9957009030eba",
		"local network webapp_front":      "c44c424e2616ab24c5388d25dfaf3c3cf543dc592f3fb3584b81ea381ef5192b",
		"local volume webapp_pgdata":      "cf7bb7408fa07085330b5c16465616a6a74c23d5bcb62fe9c486a8079db623bc",
		"swarm config webapp_banner":      "1fcdbda3178fbf2e55861353d9a0a235c569749a3cb44c2ab3a36b0620e82dca",
		"swarm config webapp_nginx_conf":  "b0477d6969f82b32e61695c88eb27986d35192fc9bf03086b5a42ac37b2efb3c",
		"swarm network webapp_back":       "acb9d7435037ff2bc2ca53b1a606a8b101b11071c0089fec4c2582472aa1d7be",
		"swarm network webapp_front":      "b099969d1d1c02def174403f5ed7f170e6f6425378da44f3ea23e4c2e419c563",
		"swarm secret webapp_api_key":     "bce5f0858e79e8e4c2f95afeb7497cea0c8cb2d5f34c0032d6fe65424b2465f3",
		"swarm secret webapp_db_password": "920a2bddfd6252c5c969528dcecd4858b3ff8aba33490e40934de9bff9192f0b",
		"swarm service webapp_cache":      "47e822907bdf7b0e56bcbd1f71ce821feb39a18e61618736fd48a244e025873e",
		"swarm service webapp_db":         "d4b9be3f4fca4ae3b4068d3e05916eb8f191fed8fe35936b5fdd913365688485",
		"swarm service webapp_debug":      "8dd0c721c1b0c7a99299a4048d637567d8fa4eafb4b547a0a437979338113d35",
		"swarm service webapp_web":        "39ca8fb04d5e955a084dac2db7351ca1949f6c057c59dac132fb08ac1c1cd369",
	},
	"example_shop": {
		"local container shop-db-1":     "ec25ee2669b7439c2e293694da940a5012e16791ede544b66d7995e1986bfa31",
		"local container shop-web-1":    "00f0763c0d041c8ed3258689f86478a18670daa362149ee343f0d4265f968177",
		"local network shop_default":    "dd2253035c4e91e1e5bb50be32f19901fb65470280a122e61756159cff79cf6b",
		"local volume shop_pgdata":      "af8aa58a84da20ca012d826d522faebe3272e44825825d7303d241eda2464a41",
		"swarm config shop_site":        "7cf0e080b7528ff98471be5ad9279513987eaa4182da292dc783e8f9a71b3a9f",
		"swarm network shop_default":    "bfc9f8b4342a51c92f8824145d58f22888daa0d2cbf7889fd2c565bd9c4c7a17",
		"swarm secret shop_db_password": "d96aeaafe5aa4e7cbd1298b78c02937cbb53313f5b2fec5ea44f1caa771ac781",
		"swarm service shop_db":         "480cf74dcf52ed1c1c8c218a5ab4bca11fbcda544b5e1acc483a3c1ce4079d41",
		"swarm service shop_web":        "17b3f342a4dcb821e3d2f4f9bea2242406b49d6bd2c81157097b10b822ae887d",
	},
	"example_stack": {
		"local container shopstack-web-1": "9781b69acd22608f3af16b2f1ac47378e5a75b136dfdbb6ba0a814854c13c629",
		"local container shopstack-web-2": "004326165dcfc1b2a8e5d085a5e743e3d6eb077114b9e4aaa8c6444f7c430061",
		"local network shopstack_default": "e88d5e09deff5e52457cc33b6ca7d5e8a699637b0afae33255e546688be29b2b",
		"swarm network shopstack_default": "60be7df3fe0c1fe33f0dec44bebb2e8464889283577afc8e6a5ee6d01c8e484b",
		"swarm service shopstack_web":     "cf39e9a61b4c67856d636797439b6b58d965477220353cf7a46706c394e604ff",
	},
	"kitchen_sink": {
		"local container sink-admin-1":  "80022824b2d8205837b37c5003f48d45c73a39af497cf8acc38236dcf8407d54",
		"local container sink-api-1":    "a5ad6e8eb71af30b2f780c65cbc276c2c8fe14ae37146cb0f6460a64f867873c",
		"local container sink-api-2":    "930684c57b49c2c6a0a6873be8950a39320d8debcd8707233d6378b02fcfdc6d",
		"local container sink-api-3":    "cb37bea8b6fba78b3abc5452b4ac188ffb4e5226f893dc92cf623c4b4e945d5d",
		"local container sink-worker-1": "6e928c993f1fa0e94bdfcc9769524361f504c2bb61b4d8d31379f3528ad11f24",
		"local network sink_back":       "5f1e10d8284b77c0e0b1ddfae2e98e1b5a2b50b9caf55a8d339bdd3ef4e3ed0f",
		"local network sink_front":      "6dceb6784768f6bf922ca4e43340ff2435ef3c5f07955ea5f967c144ce96a882",
		"local network sink_v4off":      "f94fd167e1c806ddd4204a0c8ff26986281c4a23d5d936f13cb7304e262821d1",
		"local volume sink-shared-data": "f6b8886671f09062bd544ffc5ce472e9bdbf17859bf2a8b6482cd12d4cb7e804",
		"local volume sink_cache":       "876826d9ab1a0cd80d3b0f0f6a7333232b038a4e6f2ac5ef0db0e50011e8d96a",
		"swarm config sink_api_conf":    "97506245c83e1df7f5d056c33d454ea77a3f5bcad307fa81172aec332bca4114",
		"swarm config sink_banner":      "8e38e3d004011403229cc902aca9b91311631c61977a1d1fc7b3f628619171df",
		"swarm config sink_flags":       "7a7bbc0f872ef18e06d21db3d4449efd85d9499d1ec2866741972d23e714ca55",
		"swarm network sink_back":       "9afba1f68d4a282b051155f6bada896a6d3cfddf62c3809e7f0688bebb5fae2c",
		"swarm network sink_front":      "31c8bbc429dd97a934ec7f6a09835a990fe926b2bf7aa7efaabc4b5245c24af9",
		"swarm network sink_v4off":      "ebe9aea2f0de404c201deb558df2f0245b13c402c67c889e4067747297e8f3dd",
		"swarm secret sink_api_key":     "3f7a57dc83c660f3c7792dc1bde678238afc528cfb34ff26491cbbe92b9a924e",
		"swarm secret sink_tls_key":     "ae1c255409ea848d4a73ffe2407cf4b93b13ffd456ccd8dc8c6b4bd2cf3913e1",
		"swarm secret sink_token":       "81ee458790d97a1e9c667e383047fd5752c1c2c87be39e7a97388164ef54a8ce",
		"swarm service sink_admin":      "b17d1f155bc34d7a9e515ec796df13b7099a2c9453d3df7ac848cc20e05cb5c0",
		"swarm service sink_api":        "5f45f8cd13577a67ea544e497013f155a6ff2b8d56a01a5b2794641e6f9895db",
		"swarm service sink_worker":     "e6619c4624f8c34d277f29e24858d609e0e4788a2eb0f7cbded2672d0194a422",
	},
	"unit_build_image": {
		"local container p-a-1":   "33b204739fcdd4bb1dc3437c9f64eca853f13e12f148cdedfe1f9a66c6a4aed8",
		"local network p_default": "1a553bfa49923bfdcb6d50e52a9d98da71821247b508019637306cd622c25260",
		"swarm network p_default": "7064ca10f2305250f7252d33e4123e4162d65b003ba80836c313f7a767a52674",
		"swarm service p_a":       "a9472bdb5f057c927997ab54f66f849b7f193bcc9e4168bce6f53bf7964bbd4e",
	},
	"unit_drift": {
		"local container p-once-1": "34b9e09e7ff7c80bd351d038831e61eff7f7885fd08a0edff76474c15962716e",
		"local container p-web-1":  "5c0d855b4dd79dd2984b5a09f04359b463e02af4a5bd345799a9bb00239a96e7",
		"local network p_default":  "1a553bfa49923bfdcb6d50e52a9d98da71821247b508019637306cd622c25260",
		"local volume p_data":      "70f5a8a919b56f8e0a67f5593ce5a3e0e407710b74d2a584c53cb7b867e19abc",
		"swarm network p_default":  "7064ca10f2305250f7252d33e4123e4162d65b003ba80836c313f7a767a52674",
		"swarm service p_once":     "d2f4c43b45f46587243bb1797d1109d966131def2719f0e65d3f9d86d905d5bc",
		"swarm service p_web":      "5d2c2ce487fb877b576d2060aa80a84996577f8de41162e601a1f945b534a466",
	},
	"unit_expand": {
		"local container proj-sidecar-1": "4da8a43783a9f0880ed120ce13a3d9f8d4e0541c7d13909559cdd7248801ef10",
		"local container proj-web-1":     "c2b488541a6b9cefe6e6f3aee6e799c8421b74c9f93c8f7a9b5963fcaba5a05c",
		"local container proj-web-2":     "908b45b1e9af95c3417d82e28bc6661da1819a4ccfd0c1374fa85220d0ef2cc5",
		"local container the-job":        "1d0e76daf023eae922f49a09f59eb85fc0d1a8993f8cc793c30a4c7d45c72af0",
		"local network proj_back":        "07ee72135406da35e8f20322f294404be90059e8188da0f708e85b9862e69fcf",
		"local network proj_front":       "76f383a581f4a378270cb537ccd4c5b2909ff905cbc9d52374b079f622195514",
		"local volume proj_data":         "9a1a25289ff7e3126450ae2368709a20c08f6910a2473159d40b7948122d5b98",
		"swarm config proj_conf":         "f6460365423a95ae546f2848a82b10913efb7bbb7ce57b579be5652e9b490701",
		"swarm network proj_back":        "94860c3c50d5d7237afeb6931e5a5d37298f751f177255e47c238cdf68822079",
		"swarm network proj_front":       "38befda9ce07be98b340bcea9c48a2bf736e948617ec600a3bb4aace1cffe02d",
		"swarm secret proj_file_secret":  "53161f0794adfa68fde931c570bba57691a6ee9983e9dbda58cba87f236a1695",
		"swarm secret proj_pw":           "b624acd97ec0f359471ab2a552d77f2973f652eb075bb6806a251625a330fd87",
		"swarm service proj_job":         "74cb8dbf17cdbe5a92d04e19b8c8e69da1b5ec2e461847ef120f375d977d327c",
		"swarm service proj_sidecar":     "d28ec1b05cae3db8bf095267ad07022658238bb62e00a9b5f10e2633b9c9a356",
		"swarm service proj_web":         "0b5b25f04bc7250c5f84ae057d887cd49f05e1326a62e0a58aa60bc348e5e8d4",
	},
	"unit_profiles": {
		"local container p-api-1": "3a71624cc8ff53640adba3e46c1482a3bed0a5e8503f7cc20f9bf637a6723882",
		"local container p-db-1":  "0f649e6cc1ce764fbe7d93729852cb0e6f54ec0f2a5fac3773a7dc8727217156",
		"local container p-web-1": "48bb4eccbb6f3439841d3ea1f8b0c988bcc4f4a588b0c5b88e523c7ebb2c0774",
		"local network p_default": "1a553bfa49923bfdcb6d50e52a9d98da71821247b508019637306cd622c25260",
		"swarm network p_default": "7064ca10f2305250f7252d33e4123e4162d65b003ba80836c313f7a767a52674",
		"swarm service p_api":     "6ea5f8fad73499d293b96590c3ef6806f49d1ac55d241e924b1705b571a3e3bd",
		"swarm service p_db":      "eb859aa8902a7271bd184c254569de91c67cb2695f912af216d38078eb1a919b",
		"swarm service p_web":     "6cc6b45e9065acf3721b68dda3b90d652feffedf15d63c40f59010954a74501f",
	},
	"unit_profiles_debug": {
		"local container p-api-1":   "3a71624cc8ff53640adba3e46c1482a3bed0a5e8503f7cc20f9bf637a6723882",
		"local container p-db-1":    "0f649e6cc1ce764fbe7d93729852cb0e6f54ec0f2a5fac3773a7dc8727217156",
		"local container p-debug-1": "7ea91d11b1af8206838b479deb33ed09610ae5cda1e028eeffc4eedeea2c99bc",
		"local container p-web-1":   "48bb4eccbb6f3439841d3ea1f8b0c988bcc4f4a588b0c5b88e523c7ebb2c0774",
		"local network p_default":   "1a553bfa49923bfdcb6d50e52a9d98da71821247b508019637306cd622c25260",
		"swarm network p_default":   "7064ca10f2305250f7252d33e4123e4162d65b003ba80836c313f7a767a52674",
		"swarm service p_api":       "6ea5f8fad73499d293b96590c3ef6806f49d1ac55d241e924b1705b571a3e3bd",
		"swarm service p_db":        "eb859aa8902a7271bd184c254569de91c67cb2695f912af216d38078eb1a919b",
		"swarm service p_debug":     "105cb49f86adff6cd1870d4b7900172abc8b15723ddfc094a88f9e27c7263c32",
		"swarm service p_web":       "6cc6b45e9065acf3721b68dda3b90d652feffedf15d63c40f59010954a74501f",
	},
	"unit_resource_shop": {
		"local container shop-cache-1": "d55283a1260f43e894aa954a5ffb5e876301178096fe912c54020f8201cb5df2",
		"local container shop-cache-2": "2c90c7480c0943820b429250ac6571e523826da03ce12a4dda9d15276e7d90a3",
		"local container shop-db-1":    "9f95c1195cf78d02d8a74be3a5b59eea7d12bf011293f84ceac1f25fbaa4628c",
		"local container shop-web-1":   "631af804ae64f04399cff7df64df0985fa4ad998af1b469bd39a60cdf8ffc953",
		"local network shop_back":      "f545ed939757df43d92c797acef5daef6b5e6d384fa07b029199480dd9371bb4",
		"local network shop_front":     "449076d653d823882be783e4ee9c04fb1c210cd8959e4cb78550002d9d4f47f5",
		"local volume shop_pgdata":     "af8aa58a84da20ca012d826d522faebe3272e44825825d7303d241eda2464a41",
		"swarm config shop_site":       "6f09b9d06e1842781810328f558d2b77e10cff739649df3187ab6aa97a27790f",
		"swarm network shop_back":      "bfc9f8b4342a51c92f8824145d58f22888daa0d2cbf7889fd2c565bd9c4c7a17",
		"swarm network shop_front":     "bfc9f8b4342a51c92f8824145d58f22888daa0d2cbf7889fd2c565bd9c4c7a17",
		"swarm secret shop_db_pw":      "5acc5faeb99acc14701ec4fc549e813980c3a7c29fc405b9f06c2f3529687c16",
		"swarm service shop_cache":     "46e08b3f185ddbeb2236fd9a7424c5b578b72945d65f2edf2c6d1c49a26ec9cf",
		"swarm service shop_db":        "a0667ad8caf86ba8ccdd27e27a48c889bd4de154ff60fdb520d23d5dc4afeaa8",
		"swarm service shop_web":       "2529e1b3d679e567cc3cd2c2ea5644e80b5b46c7f66389dc17ba908f2410e4ca",
	},
}
