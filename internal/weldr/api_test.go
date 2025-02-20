package weldr

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/osbuild/osbuild-composer/internal/blueprint"
	"github.com/osbuild/osbuild-composer/internal/common"
	"github.com/osbuild/osbuild-composer/internal/distro"
	"github.com/osbuild/osbuild-composer/internal/distro/test_distro"
	"github.com/osbuild/osbuild-composer/internal/distroregistry"
	rpmmd_mock "github.com/osbuild/osbuild-composer/internal/mocks/rpmmd"
	"github.com/osbuild/osbuild-composer/internal/ostree/mock_ostree_repo"
	"github.com/osbuild/osbuild-composer/internal/reporegistry"
	"github.com/osbuild/osbuild-composer/internal/rpmmd"
	"github.com/osbuild/osbuild-composer/internal/store"
	"github.com/osbuild/osbuild-composer/internal/target"
	"github.com/osbuild/osbuild-composer/internal/test"

	"github.com/BurntSushi/toml"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
)

func createWeldrAPI(tempdir string, fixtureGenerator rpmmd_mock.FixtureGenerator) (*API, *store.Store) {
	fixture := fixtureGenerator(tempdir)
	rpm := rpmmd_mock.NewRPMMDMock(fixture)

	rr := reporegistry.NewFromDistrosRepoConfigs(rpmmd.DistrosRepoConfigs{
		test_distro.TestDistroName: {
			test_distro.TestArchName: {
				{Name: "test-id", BaseURL: "http://example.com/test/os/x86_64", CheckGPG: true},
			},
		},
		test_distro.TestDistro2Name: {
			test_distro.TestArchName: {
				{Name: "test-id-2", BaseURL: "http://example.com/test-2/os/x86_64", CheckGPG: true},
			},
		},
	})

	distro1 := test_distro.New()
	arch, err := distro1.GetArch(test_distro.TestArchName)
	if err != nil {
		panic(err)
	}
	distro2 := test_distro.New2()

	dr, err := distroregistry.New(distro1, distro1, distro2)
	if err != nil {
		panic(err)
	}

	return NewTestAPI(rpm, arch, dr, rr, nil, fixture.Store, fixture.Workers, "", nil), fixture.Store
}

// createWeldrAPI2 is an alternative function to createWeldrAPI, using different test architecture
// with more than a single image type
func createWeldrAPI2(tempdir string, fixtureGenerator rpmmd_mock.FixtureGenerator,
	distroImageTypeDenylist map[string][]string) (*API, *store.Store) {
	fixture := fixtureGenerator(tempdir)
	rpm := rpmmd_mock.NewRPMMDMock(fixture)

	rr := reporegistry.NewFromDistrosRepoConfigs(rpmmd.DistrosRepoConfigs{
		test_distro.TestDistroName: {
			test_distro.TestArch2Name: {
				{Name: "test-id", BaseURL: "http://example.com/test/os/x86_64", CheckGPG: true},
			},
		},
		test_distro.TestDistro2Name: {
			test_distro.TestArch2Name: {
				{Name: "test-id-2", BaseURL: "http://example.com/test-2/os/x86_64", CheckGPG: true},
			},
		},
	})

	distro1 := test_distro.New()
	arch, err := distro1.GetArch(test_distro.TestArch2Name)
	if err != nil {
		panic(err)
	}
	distro2 := test_distro.New2()

	dr, err := distroregistry.New(distro1, distro2)
	if err != nil {
		panic(err)
	}

	return NewTestAPI(rpm, arch, dr, rr, nil, fixture.Store, fixture.Workers, "", distroImageTypeDenylist), fixture.Store
}

func TestBasic(t *testing.T) {
	var cases = []struct {
		Path           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{"/api/status", http.StatusOK, `{"api":"1","db_supported":true,"db_version":"0","schema_version":"0","backend":"osbuild-composer","build":"devel","msgs":[]}`},

		{"/api/v0/projects/source/list", http.StatusOK, `{"sources":["test-id"]}`},

		{"/api/v0/projects/source/info", http.StatusNotFound, `{"errors":[{"code":404,"id":"HTTPError","msg":"Not Found"}],"status":false}`},
		{"/api/v0/projects/source/info/", http.StatusNotFound, `{"errors":[{"code":404,"id":"HTTPError","msg":"Not Found"}],"status":false}`},
		{"/api/v0/projects/source/info/foo", http.StatusOK, `{"errors":[{"id":"UnknownSource","msg":"foo is not a valid source"}],"sources":{}}`},
		{"/api/v0/projects/source/info/test-id", http.StatusOK, `{"sources":{"test-id":{"name":"test-id","type":"yum-baseurl","url":"http://example.com/test/os/x86_64","check_gpg":true,"check_ssl":true,"system":true}},"errors":[]}`},
		{"/api/v0/projects/source/info/*", http.StatusOK, `{"sources":{"test-id":{"name":"test-id","type":"yum-baseurl","url":"http://example.com/test/os/x86_64","check_gpg":true,"check_ssl":true,"system":true}},"errors":[]}`},

		{"/api/v0/blueprints/list", http.StatusOK, `{"total":1,"offset":0,"limit":1,"blueprints":["test"]}`},
		{"/api/v0/blueprints/info/", http.StatusNotFound, `{"errors":[{"code":404,"id":"HTTPError","msg":"Not Found"}],"status":false}`},
		{"/api/v0/blueprints/info/foo", http.StatusOK, `{"blueprints":[],"changes":[],"errors":[{"id":"UnknownBlueprint","msg":"foo: "}]}`},
		{"/api/v1/distros/list", http.StatusOK, `{"distros": ["test-distro", "test-distro-2"]}`},
		{"/api/v1/compose/types", http.StatusOK, `{"types": [{"enabled":true, "name":"test_type"}]}`},
		{"/api/v1/compose/types?distro=test-distro-2", http.StatusOK, `{"types": [{"enabled":true, "name":"test_type"}]}`},
		{"/api/v1/compose/types?distro=fedora-1", http.StatusBadRequest, `{"status":false,"errors":[{"id":"DistroError","msg":"Invalid distro: fedora-1"}]}`},
	}

	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.BaseFixture)
	for _, c := range cases {
		test.TestRoute(t, api, true, "GET", c.Path, ``, c.ExpectedStatus, c.ExpectedJSON)
	}
}

func TestBlueprintsNew(t *testing.T) {
	var cases = []struct {
		Method         string
		Path           string
		Body           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{"POST", "/api/v0/blueprints/new", `{"name":"test","description":"Test","packages":[],"version":""}`, http.StatusOK, `{"status":true}`},
		{"POST", "/api/v0/blueprints/new", `{"name":"test","description":"Test","packages":[{"name":"httpd","version":"2.4.*"}],"version":"0.0.0"}`, http.StatusOK, `{"status":true}`},
		{"POST", "/api/v0/blueprints/new", `{"name":"test","description":"Test","packages:}`, http.StatusBadRequest, `{"status":false,"errors":[{"id":"BlueprintsError","msg":"400 Bad Request: The browser (or proxy) sent a request that this server could not understand: unexpected EOF"}]}`},
		{"POST", "/api/v0/blueprints/new", `{"name":"","description":"Test","packages":[{"name":"httpd","version":"2.4.*"}],"version":"0.0.0"}`, http.StatusBadRequest, `{"status":false,"errors":[{"id":"InvalidChars","msg":"Invalid characters in API path"}]}`},
		{"POST", "/api/v0/blueprints/new", ``, http.StatusBadRequest, `{"status":false,"errors":[{"id":"BlueprintsError","msg":"Missing blueprint"}]}`},
		{"POST", "/api/v0/blueprints/new", `{"name":"test","description":"Test","distro":"test-distro","packages":[],"version":""}`, http.StatusOK, `{"status":true}`},
		{"POST", "/api/v0/blueprints/new", `{"name":"test2","description":"Test 2","distro":"test-distro-2","packages":[],"version":""}`, http.StatusOK, `{"status":true}`},
		{"POST", "/api/v0/blueprints/new", `{"name":"test","description":"Test","distro":"fedora-1","packages":[],"version":""}`, http.StatusBadRequest, `{"status":false,"errors":[{"id":"BlueprintsError","msg":"'fedora-1' is not a valid distribution"}]}`},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		test.TestRoute(t, api, true, c.Method, c.Path, c.Body, c.ExpectedStatus, c.ExpectedJSON)
	}
}

func TestBlueprintsNewToml(t *testing.T) {
	blueprint := `
name = "test"
description = "Test"
version = "0.0.0"

[[packages]]
name = "httpd"
version = "2.4.*"`

	req := httptest.NewRequest("POST", "/api/v0/blueprints/new", bytes.NewReader([]byte(blueprint)))
	req.Header.Set("Content-Type", "text/x-toml")
	recorder := httptest.NewRecorder()

	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.BaseFixture)
	api.ServeHTTP(recorder, req)

	r := recorder.Result()
	require.Equal(t, http.StatusOK, r.StatusCode)
}

func TestBlueprintsEmptyToml(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v0/blueprints/new", bytes.NewReader(nil))
	req.Header.Set("Content-Type", "text/x-toml")
	recorder := httptest.NewRecorder()

	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.BaseFixture)
	api.ServeHTTP(recorder, req)

	r := recorder.Result()
	require.Equal(t, http.StatusBadRequest, r.StatusCode)
}

func TestBlueprintsInvalidToml(t *testing.T) {
	blueprint := `
name = "test"
description = "Test"
version = "0.0.0"

[[packages
name = "httpd"
version = "2.4.*"`

	req := httptest.NewRequest("POST", "/api/v0/blueprints/new", bytes.NewReader([]byte(blueprint)))
	req.Header.Set("Content-Type", "text/x-toml")
	recorder := httptest.NewRecorder()

	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.BaseFixture)
	api.ServeHTTP(recorder, req)

	r := recorder.Result()
	require.Equal(t, http.StatusBadRequest, r.StatusCode)
}

func TestBlueprintsWorkspaceJSON(t *testing.T) {
	var cases = []struct {
		Method         string
		Path           string
		Body           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{"POST", "/api/v0/blueprints/workspace", `{"name":"test","description":"Test","packages":[{"name":"systemd","version":"123"}],"version":"0.0.0"}`, http.StatusOK, `{"status":true}`},
		{"POST", "/api/v0/blueprints/workspace", `{"name":"test","description":"Test","packages:}`, http.StatusBadRequest, `{"status":false,"errors":[{"id":"BlueprintsError","msg":"400 Bad Request: The browser (or proxy) sent a request that this server could not understand: unexpected EOF"}]}`},
		{"POST", "/api/v0/blueprints/workspace", ``, http.StatusBadRequest, `{"status":false,"errors":[{"id":"BlueprintsError","msg":"Missing blueprint"}]}`},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		test.TestRoute(t, api, true, c.Method, c.Path, c.Body, c.ExpectedStatus, c.ExpectedJSON)
	}
}

func TestBlueprintsWorkspaceTOML(t *testing.T) {
	blueprint := `
name = "test"
description = "Test"
version = "0.0.0"

[[packages]]
name = "httpd"
version = "2.4.*"`

	req := httptest.NewRequest("POST", "/api/v0/blueprints/workspace", bytes.NewReader([]byte(blueprint)))
	req.Header.Set("Content-Type", "text/x-toml")
	recorder := httptest.NewRecorder()

	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.BaseFixture)
	api.ServeHTTP(recorder, req)

	r := recorder.Result()
	require.Equal(t, http.StatusOK, r.StatusCode)
}

func TestBlueprintsWorkspaceEmptyTOML(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v0/blueprints/workspace", bytes.NewReader(nil))
	req.Header.Set("Content-Type", "text/x-toml")
	recorder := httptest.NewRecorder()

	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.BaseFixture)
	api.ServeHTTP(recorder, req)

	r := recorder.Result()
	require.Equal(t, http.StatusBadRequest, r.StatusCode)
}

func TestBlueprintsWorkspaceInvalidTOML(t *testing.T) {
	blueprint := `
name = "test"
description = "Test"
version = "0.0.0"

[[packages
name = "httpd"
version = "2.4.*"`

	req := httptest.NewRequest("POST", "/api/v0/blueprints/workspace", bytes.NewReader([]byte(blueprint)))
	req.Header.Set("Content-Type", "text/x-toml")
	recorder := httptest.NewRecorder()

	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.BaseFixture)
	api.ServeHTTP(recorder, req)

	r := recorder.Result()
	require.Equal(t, http.StatusBadRequest, r.StatusCode)
}

func TestBlueprintsInfo(t *testing.T) {
	var cases = []struct {
		Method         string
		Path           string
		Body           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{"GET", "/api/v0/blueprints/info/test1", ``, http.StatusOK, `{"blueprints":[{"name":"test1","description":"Test","distro":"","modules":[],"packages":[{"name":"httpd","version":"2.4.*"}],"groups":[],"version":"0.0.0"}],
		"changes":[{"name":"test1","changed":false}], "errors":[]}`},
		{"GET", "/api/v0/blueprints/info/test2", ``, http.StatusOK, `{"blueprints":[{"name":"test2","description":"Test","distro":"","modules":[],"packages":[{"name":"systemd","version":"123"}],"groups":[],"version":"0.0.0"}],
		"changes":[{"name":"test2","changed":true}], "errors":[]}`},
		{"GET", "/api/v0/blueprints/info/test3-non", ``, http.StatusOK, `{"blueprints":[],"changes":[],"errors":[{"id":"UnknownBlueprint","msg":"test3-non: "}]}`},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		test.SendHTTP(api, true, "POST", "/api/v0/blueprints/new", `{"name":"test1","description":"Test","packages":[{"name":"httpd","version":"2.4.*"}],"version":"0.0.0"}`)
		test.SendHTTP(api, true, "POST", "/api/v0/blueprints/new", `{"name":"test2","description":"Test","packages":[{"name":"httpd","version":"2.4.*"}],"version":"0.0.0"}`)
		test.SendHTTP(api, true, "POST", "/api/v0/blueprints/workspace", `{"name":"test2","description":"Test","packages":[{"name":"systemd","version":"123"}],"version":"0.0.0"}`)
		test.TestRoute(t, api, true, c.Method, c.Path, c.Body, c.ExpectedStatus, c.ExpectedJSON)
		test.SendHTTP(api, true, "DELETE", "/api/v0/blueprints/delete/test2", ``)
		test.SendHTTP(api, true, "DELETE", "/api/v0/blueprints/delete/test1", ``)
	}
}

func TestBlueprintsInfoToml(t *testing.T) {
	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.BaseFixture)
	test.SendHTTP(api, true, "POST", "/api/v0/blueprints/new", `{"name":"test1","description":"Test","packages":[{"name":"httpd","version":"2.4.*"}],"version":"0.0.0"}`)

	req := httptest.NewRequest("GET", "/api/v0/blueprints/info/test1?format=toml", nil)
	recorder := httptest.NewRecorder()
	api.ServeHTTP(recorder, req)

	resp := recorder.Result()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got blueprint.Blueprint
	_, err := toml.NewDecoder(resp.Body).Decode(&got)
	require.NoErrorf(t, err, "error decoding toml file")

	expected := blueprint.Blueprint{
		Name:        "test1",
		Description: "Test",
		Version:     "0.0.0",
		Distro:      "",
		Packages: []blueprint.Package{
			{
				Name:    "httpd",
				Version: "2.4.*"},
		},
		Groups:  []blueprint.Group{},
		Modules: []blueprint.Package{},
	}
	require.Equalf(t, expected, got, "received unexpected blueprint")
}

func TestNonExistentBlueprintsInfoToml(t *testing.T) {
	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.BaseFixture)
	req := httptest.NewRequest("GET", "/api/v0/blueprints/info/test3-non?format=toml", nil)
	recorder := httptest.NewRecorder()
	api.ServeHTTP(recorder, req)

	resp := recorder.Result()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestSetPkgEVRA(t *testing.T) {

	// Sorted list of dependencies
	deps := []rpmmd.PackageSpec{
		{
			Name:    "dep-package1",
			Epoch:   0,
			Version: "1.33",
			Release: "2.fc30",
			Arch:    "x86_64",
		},
		{
			Name:    "dep-package2",
			Epoch:   0,
			Version: "2.9",
			Release: "1.fc30",
			Arch:    "x86_64",
		},
		{
			Name:    "dep-package3",
			Epoch:   7,
			Version: "3.0.3",
			Release: "1.fc30",
			Arch:    "x86_64",
		},
	}
	pkgs := []blueprint.Package{
		{Name: "dep-package1", Version: "*"},
		{Name: "dep-package2", Version: "*"},
	}
	// Replace globs with dependencies
	err := setPkgEVRA(deps, pkgs)
	require.NoErrorf(t, err, "setPkgEVRA failed")
	require.Equalf(t, "1.33-2.fc30.x86_64", pkgs[0].Version, "setPkgEVRA Unexpected pkg version")
	require.Equalf(t, "2.9-1.fc30.x86_64", pkgs[1].Version, "setPkgEVRA Unexpected pkg version")

	// Test that a missing package in deps returns an error
	pkgs = []blueprint.Package{
		{Name: "dep-package1", Version: "*"},
		{Name: "dep-package0", Version: "*"},
	}
	err = setPkgEVRA(deps, pkgs)
	require.EqualErrorf(t, err, "dep-package0 missing from depsolve results", "setPkgEVRA missing package failed to return error")
}

func TestBlueprintsFreeze(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		var cases = []struct {
			Fixture        rpmmd_mock.FixtureGenerator
			Path           string
			ExpectedStatus int
			ExpectedJSON   string
		}{
			{rpmmd_mock.BaseFixture, "/api/v0/blueprints/freeze/test,test2", http.StatusOK, `{"blueprints":[{"blueprint":{"name":"test","description":"Test","distro":"","version":"0.0.1","packages":[{"name":"dep-package1","version":"1.33-2.fc30.x86_64"},{"name":"dep-package3","version":"7:3.0.3-1.fc30.x86_64"}],"modules":[{"name":"dep-package2","version":"2.9-1.fc30.x86_64"}],"groups":[]}},{"blueprint":{"name":"test2","description":"Test","distro":"","version":"0.0.0","packages":[{"name":"dep-package1","version":"1.33-2.fc30.x86_64"},{"name":"dep-package3","version":"7:3.0.3-1.fc30.x86_64"}],"modules":[{"name":"dep-package2","version":"2.9-1.fc30.x86_64"}],"groups":[]}}],"errors":[]}`},
		}

		tempdir := t.TempDir()

		for _, c := range cases {
			api, _ := createWeldrAPI(tempdir, c.Fixture)
			test.SendHTTP(api, false, "POST", "/api/v0/blueprints/new", `{"name":"test","description":"Test","packages":[{"name":"dep-package1","version":"*"},{"name":"dep-package3","version":"*"}], "modules":[{"name":"dep-package2","version":"*"}],"version":"0.0.0"}`)
			test.SendHTTP(api, false, "POST", "/api/v0/blueprints/new", `{"name":"test2","description":"Test","packages":[{"name":"dep-package1","version":"*"},{"name":"dep-package3","version":"*"}], "modules":[{"name":"dep-package2","version":"*"}],"version":"0.0.0"}`)
			test.TestRoute(t, api, false, "GET", c.Path, ``, c.ExpectedStatus, c.ExpectedJSON)
		}
	})

	t.Run("toml", func(t *testing.T) {
		var cases = []struct {
			Fixture        rpmmd_mock.FixtureGenerator
			Path           string
			ExpectedStatus int
			ExpectedTOML   string
		}{
			{rpmmd_mock.BaseFixture, "/api/v0/blueprints/freeze/test?format=toml", http.StatusOK, "name=\"test\"\n description=\"Test\"\n distro=\"\"\n version=\"0.0.1\"\n groups = []\n [[packages]]\n name=\"dep-package1\"\n version=\"1.33-2.fc30.x86_64\"\n [[packages]]\n name=\"dep-package3\"\n version=\"7:3.0.3-1.fc30.x86_64\"\n [[modules]]\n name=\"dep-package2\"\n version=\"2.9-1.fc30.x86_64\""},
			{rpmmd_mock.BaseFixture, "/api/v0/blueprints/freeze/missing?format=toml", http.StatusOK, ""},
		}

		tempdir := t.TempDir()

		for _, c := range cases {
			api, _ := createWeldrAPI(tempdir, c.Fixture)
			test.SendHTTP(api, false, "POST", "/api/v0/blueprints/new", `{"name":"test","description":"Test","packages":[{"name":"dep-package1","version":"*"},{"name":"dep-package3","version":"*"}], "modules":[{"name":"dep-package2","version":"*"}],"version":"0.0.0"}`)
			test.TestTOMLRoute(t, api, false, "GET", c.Path, ``, c.ExpectedStatus, c.ExpectedTOML)
		}
	})

	t.Run("toml-multiple", func(t *testing.T) {
		var cases = []struct {
			Fixture        rpmmd_mock.FixtureGenerator
			Path           string
			ExpectedStatus int
			ExpectedJSON   string
		}{
			{rpmmd_mock.BaseFixture, "/api/v0/blueprints/freeze/test,test2?format=toml", http.StatusBadRequest, "{\"status\":false,\"errors\":[{\"id\":\"HTTPError\",\"msg\":\"toml format only supported when requesting one blueprint\"}]}"},
		}

		tempdir := t.TempDir()

		for _, c := range cases {
			api, _ := createWeldrAPI(tempdir, c.Fixture)
			test.SendHTTP(api, false, "POST", "/api/v0/blueprints/new", `{"name":"test","description":"Test","packages":[{"name":"dep-package1","version":"*"},{"name":"dep-package3","version":"*"}], "modules":[{"name":"dep-package2","version":"*"}],"version":"0.0.0"}`)
			test.SendHTTP(api, false, "POST", "/api/v0/blueprints/new", `{"name":"test2","description":"Test","packages":[{"name":"dep-package1","version":"*"},{"name":"dep-package3","version":"*"}], "modules":[{"name":"dep-package2","version":"*"}],"version":"0.0.0"}`)
			test.TestRoute(t, api, false, "GET", c.Path, ``, c.ExpectedStatus, c.ExpectedJSON)
		}
	})
}

func TestBlueprintsDiff(t *testing.T) {
	var cases = []struct {
		Method         string
		Path           string
		Body           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{"GET", "/api/v0/blueprints/diff/test/NEWEST/WORKSPACE", ``, http.StatusOK, `{"diff":[{"new":{"Package":{"name":"systemd","version":"123"}},"old":null},{"new":null,"old":{"Package":{"name":"httpd","version":"2.4.*"}}}]}`},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		test.SendHTTP(api, true, "POST", "/api/v0/blueprints/new", `{"name":"test","description":"Test","packages":[{"name":"httpd","version":"2.4.*"}],"version":"0.0.0"}`)
		test.SendHTTP(api, true, "POST", "/api/v0/blueprints/workspace", `{"name":"test","description":"Test","packages":[{"name":"systemd","version":"123"}],"version":"0.0.0"}`)
		test.TestRoute(t, api, true, c.Method, c.Path, c.Body, c.ExpectedStatus, c.ExpectedJSON)
		test.SendHTTP(api, true, "DELETE", "/api/v0/blueprints/delete/test", ``)
	}
}

func TestBlueprintsDelete(t *testing.T) {
	var cases = []struct {
		Method         string
		Path           string
		Body           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{"DELETE", "/api/v0/blueprints/delete/test", ``, http.StatusOK, `{"status":true}`},
		{"DELETE", "/api/v0/blueprints/delete/test3-non", ``, http.StatusBadRequest, `{"status":false,"errors":[{"id":"BlueprintsError","msg":"Unknown blueprint: test3-non"}]}`},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		test.SendHTTP(api, true, "POST", "/api/v0/blueprints/new", `{"name":"test","description":"Test","packages":[{"name":"httpd","version":"2.4.*"}],"version":"0.0.0"}`)
		test.TestRoute(t, api, true, c.Method, c.Path, c.Body, c.ExpectedStatus, c.ExpectedJSON)
		test.SendHTTP(api, true, "DELETE", "/api/v0/blueprints/delete/test", ``)
	}
}

func TestBlueprintsChanges(t *testing.T) {
	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.BaseFixture)
	rand.Seed(time.Now().UnixNano())
	// math/rand is good enough in this case
	/* #nosec G404 */
	id := strconv.Itoa(rand.Int())
	ignoreFields := []string{"commit", "timestamp"}

	test.SendHTTP(api, true, "POST", "/api/v0/blueprints/new", `{"name":"`+id+`","description":"Test","packages":[{"name":"httpd","version":"2.4.*"}],"version":"0.0.0"}`)
	test.TestRoute(t, api, true, "GET", "/api/v0/blueprints/changes/failing"+id, ``, http.StatusOK, `{"blueprints":[],"errors":[{"id":"UnknownBlueprint","msg":"failing`+id+`"}],"limit":20,"offset":0}`, ignoreFields...)
	test.TestRoute(t, api, true, "GET", "/api/v0/blueprints/changes/"+id, ``, http.StatusOK, `{"blueprints":[{"changes":[{"commit":"","message":"Recipe `+id+`, version 0.0.0 saved.","revision":null,"timestamp":""}],"name":"`+id+`","total":1}],"errors":[],"limit":20,"offset":0}`, ignoreFields...)
	test.SendHTTP(api, true, "DELETE", "/api/v0/blueprints/delete/"+id, ``)
	test.SendHTTP(api, true, "POST", "/api/v0/blueprints/new", `{"name":"`+id+`","description":"Test","packages":[{"name":"httpd","version":"2.4.*"}],"version":"0.0.0"}`)
	test.TestRoute(t, api, true, "GET", "/api/v0/blueprints/changes/"+id, ``, http.StatusOK, `{"blueprints":[{"changes":[{"commit":"","message":"Recipe `+id+`, version 0.0.0 saved.","revision":null,"timestamp":""},{"commit":"","message":"Recipe `+id+`, version 0.0.0 saved.","revision":null,"timestamp":""}],"name":"`+id+`","total":2}],"errors":[],"limit":20,"offset":0}`, ignoreFields...)
	test.SendHTTP(api, true, "DELETE", "/api/v0/blueprints/delete/"+id, ``)
}

func TestBlueprintsDepsolve(t *testing.T) {
	var cases = []struct {
		Fixture        rpmmd_mock.FixtureGenerator
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{rpmmd_mock.BaseFixture, http.StatusOK, `{"blueprints":[{"blueprint":{"name":"test","description":"Test","distro":"","version":"0.0.1","packages":[{"name":"dep-package1","version":"*"}],"groups":[],"modules":[{"name":"dep-package3","version":"*"}]},"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]}],"errors":[]}`},
		{rpmmd_mock.NonExistingPackage, http.StatusOK, `{"blueprints":[{"blueprint":{"name":"test","description":"Test","distro":"","version":"0.0.1","packages":[{"name":"dep-package1","version":"*"}],"groups":[],"modules":[{"name":"dep-package3","version":"*"}]},"dependencies":[]}],"errors":[{"id":"BlueprintsError","msg":"test: DNF error occured: MarkingErrors: Error occurred when marking packages for installation: Problems in request:\nmissing packages: fash"}]}`},
		{rpmmd_mock.BadDepsolve, http.StatusOK, `{"blueprints":[{"blueprint":{"name":"test","description":"Test","distro":"","version":"0.0.1","packages":[{"name":"dep-package1","version":"*"}],"groups":[],"modules":[{"name":"dep-package3","version":"*"}]},"dependencies":[]}],"errors":[{"id":"BlueprintsError","msg":"test: DNF error occured: DepsolveError: There was a problem depsolving ['go2rpm']: \n Problem: conflicting requests\n  - nothing provides askalono-cli needed by go2rpm-1-4.fc31.noarch"}]}`},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, c.Fixture)
		test.SendHTTP(api, false, "POST", "/api/v0/blueprints/new", `{"name":"test","description":"Test","packages":[{"name":"dep-package1","version":"*"}],"modules":[{"name":"dep-package3","version":"*"}],"version":"0.0.0"}`)
		test.TestRoute(t, api, false, "GET", "/api/v0/blueprints/depsolve/test", ``, c.ExpectedStatus, c.ExpectedJSON)
		test.SendHTTP(api, false, "DELETE", "/api/v0/blueprints/delete/test", ``)
	}
}

// TestOldBlueprintsUndo run tests with blueprint changes after a service restart
// Old blueprints are not saved, after a restart the changes are listed, but cannot be recalled
func TestOldBlueprintsUndo(t *testing.T) {
	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.OldChangesFixture)
	rand.Seed(time.Now().UnixNano())
	// math/rand is good enough in this case
	/* #nosec G404 */
	ignoreFields := []string{"commit", "timestamp"}

	test.TestRoute(t, api, true, "GET", "/api/v0/blueprints/changes/test-old-changes", ``, http.StatusOK, `{"blueprints":[{"changes":[{"commit":"","message":"Change tmux version","revision":null,"timestamp":""},{"commit":"","message":"Add tmux package","revision":null,"timestamp":""},{"commit":"","message":"Initial commit","revision":null,"timestamp":""}],"name":"test-old-changes","total":3}],"errors":[],"limit":20,"offset":0}`, ignoreFields...)

	resp := test.SendHTTP(api, true, "GET", "/api/v0/blueprints/changes/test-old-changes", ``)
	body, err := ioutil.ReadAll(resp.Body)
	require.Nil(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var changes BlueprintsChangesV0
	err = json.Unmarshal(body, &changes)
	require.Nil(t, err)
	require.Equal(t, 1, len(changes.BlueprintsChanges))
	require.Equal(t, 3, len(changes.BlueprintsChanges[0].Changes))
	commit := changes.BlueprintsChanges[0].Changes[2].Commit

	// Undo a known commit, that is old
	test.TestRoute(t, api, true, "POST", "/api/v0/blueprints/undo/test-old-changes/"+commit, ``, http.StatusBadRequest, `{"errors":[{"id":"BlueprintsError", "msg":"no blueprint found for commit `+commit+`"}], "status":false}`)

	// Check to make sure the undo is not present (can't undo something not there)
	test.TestRoute(t, api, true, "GET", "/api/v0/blueprints/changes/test-old-changes", ``, http.StatusOK, `{"blueprints":[{"changes":[{"commit":"","message":"Change tmux version","revision":null,"timestamp":""},{"commit":"","message":"Add tmux package","revision":null,"timestamp":""},{"commit":"","message":"Initial commit","revision":null,"timestamp":""}],"name":"test-old-changes","total":3}],"errors":[],"limit":20,"offset":0}`, ignoreFields...)

	// Check to make sure it didn't create an empty blueprint
	test.TestRoute(t, api, true, "GET", "/api/v0/blueprints/list", ``, http.StatusOK, `{"total":1,"offset":0,"limit":1,"blueprints":["test-old-changes"]}`)

	test.SendHTTP(api, true, "DELETE", "/api/v0/blueprints/delete/test-old-changes", ``)
}

// TestNewBlueprintsUndo run tests with blueprint changes without a service restart
func TestNewBlueprintsUndo(t *testing.T) {
	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.BaseFixture)
	rand.Seed(time.Now().UnixNano())
	// math/rand is good enough in this case
	/* #nosec G404 */
	id := strconv.Itoa(rand.Int())
	ignoreFields := []string{"commit", "timestamp"}

	test.SendHTTP(api, true, "POST", "/api/v0/blueprints/new", `{"name":"`+id+`","description":"Test","packages":[{"name":"httpd","version":"2.4.*"}],"version":"0.0.1"}`)
	test.SendHTTP(api, true, "POST", "/api/v0/blueprints/new", `{"name":"`+id+`","description":"Test","packages":[{"name":"httpd","version":"2.4.*"}, {"name": "tmux", "version":"*"}],"version":"0.1.0"}`)

	test.TestRoute(t, api, true, "GET", "/api/v0/blueprints/changes/"+id, ``, http.StatusOK, `{"blueprints":[{"changes":[{"commit":"","message":"Recipe `+id+`, version 0.1.0 saved.","revision":null,"timestamp":""},{"commit":"","message":"Recipe `+id+`, version 0.0.1 saved.","revision":null,"timestamp":""}],"name":"`+id+`","total":2}],"errors":[],"limit":20,"offset":0}`, ignoreFields...)

	resp := test.SendHTTP(api, true, "GET", "/api/v0/blueprints/changes/"+id, ``)
	body, err := ioutil.ReadAll(resp.Body)
	require.Nil(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var changes BlueprintsChangesV0
	err = json.Unmarshal(body, &changes)
	require.Nil(t, err)
	require.Equal(t, 1, len(changes.BlueprintsChanges))
	require.Equal(t, 2, len(changes.BlueprintsChanges[0].Changes))
	commit := changes.BlueprintsChanges[0].Changes[1].Commit

	// Undo an unknown commit
	test.TestRoute(t, api, true, "POST", "/api/v0/blueprints/undo/"+id+"/d7e5fa641aad45300242a0f273827576e32bfc03", ``, http.StatusBadRequest, `{"status":false,"errors":[{"id":"UnknownCommit","msg":"Unknown commit"}]}`)

	// Undo a known commit
	test.TestRoute(t, api, true, "POST", "/api/v0/blueprints/undo/"+id+"/"+commit, ``, http.StatusOK, `{"status":true}`)

	// Check to make sure the undo is present
	test.TestRoute(t, api, true, "GET", "/api/v0/blueprints/changes/"+id, ``, http.StatusOK, `{"blueprints":[{"changes":[{"commit":"","message":"`+id+`.toml reverted to commit `+commit+`","revision":null,"timestamp":""},{"commit":"","message":"Recipe `+id+`, version 0.1.0 saved.","revision":null,"timestamp":""},{"commit":"","message":"Recipe `+id+`, version 0.0.1 saved.","revision":null,"timestamp":""}],"name":"`+id+`","total":3}],"errors":[],"limit":20,"offset":0}`, ignoreFields...)

	test.SendHTTP(api, true, "DELETE", "/api/v0/blueprints/delete/"+id, ``)
}

func TestCompose(t *testing.T) {
	arch, err := test_distro.New().GetArch(test_distro.TestArchName)
	require.NoError(t, err)
	imgType, err := arch.GetImageType(test_distro.TestImageTypeName)
	require.NoError(t, err)
	manifest, err := imgType.Manifest(nil, distro.ImageOptions{}, nil, nil, 0)
	require.NoError(t, err)

	tempdir := t.TempDir()
	fixtureGenerator := rpmmd_mock.NoComposesFixture
	fixture := fixtureGenerator(tempdir)
	rpmMD := rpmmd_mock.NewRPMMDMock(fixture)

	packageSpecSets, err := rpmMD.DepsolvePackageSets(nil, nil, nil, nil, "", "", "")
	require.NoError(t, err)

	expectedComposeLocal := &store.Compose{
		Blueprint: &blueprint.Blueprint{
			Name:           "test",
			Version:        "0.0.0",
			Packages:       []blueprint.Package{},
			Modules:        []blueprint.Package{},
			Groups:         []blueprint.Group{},
			Customizations: nil,
		},
		ImageBuild: store.ImageBuild{
			QueueStatus: common.IBWaiting,
			ImageType:   imgType,
			Manifest:    manifest,
		},
		Packages: packageSpecSets["packages"],
	}
	expectedComposeLocalAndAws := &store.Compose{
		Blueprint: &blueprint.Blueprint{
			Name:           "test",
			Version:        "0.0.0",
			Packages:       []blueprint.Package{},
			Modules:        []blueprint.Package{},
			Groups:         []blueprint.Group{},
			Customizations: nil,
		},
		ImageBuild: store.ImageBuild{
			QueueStatus: common.IBWaiting,
			ImageType:   imgType,
			Manifest:    manifest,
			Targets: []*target.Target{
				{
					Name:      "org.osbuild.aws",
					Status:    common.IBWaiting,
					ImageName: "test_upload",
					Options: &target.AWSTargetOptions{
						Filename:        "test.img",
						Region:          "frankfurt",
						AccessKeyID:     "accesskey",
						SecretAccessKey: "secretkey",
						Bucket:          "clay",
						Key:             "imagekey",
					},
				},
			},
		},
		Packages: packageSpecSets["packages"],
	}
	expectedComposeOSTree := &store.Compose{
		Blueprint: &blueprint.Blueprint{
			Name:           "test",
			Version:        "0.0.0",
			Packages:       []blueprint.Package{},
			Modules:        []blueprint.Package{},
			Groups:         []blueprint.Group{},
			Customizations: nil,
		},
		ImageBuild: store.ImageBuild{
			QueueStatus: common.IBWaiting,
			ImageType:   imgType,
			Manifest:    manifest,
		},
		Packages: packageSpecSets["packages"],
	}

	// For 2nd distribution
	arch2, err := test_distro.New2().GetArch(test_distro.TestArchName)
	require.NoError(t, err)
	imgType2, err := arch2.GetImageType(test_distro.TestImageTypeName)
	require.NoError(t, err)
	manifest2, err := imgType.Manifest(nil, distro.ImageOptions{}, nil, nil, 0)
	require.NoError(t, err)

	expectedComposeGoodDistro := &store.Compose{
		Blueprint: &blueprint.Blueprint{
			Name:           "test-distro-2",
			Version:        "0.0.0",
			Packages:       []blueprint.Package{},
			Modules:        []blueprint.Package{},
			Groups:         []blueprint.Group{},
			Customizations: nil,
			Distro:         "test-distro-2",
		},
		ImageBuild: store.ImageBuild{
			QueueStatus: common.IBWaiting,
			ImageType:   imgType2,
			Manifest:    manifest2,
		},
		Packages: packageSpecSets["packages"],
	}

	// create two ostree repos, one to serve the default test_distro ref (for fallback tests) and one to serve a custom ref
	ostreeRepoDefault := mock_ostree_repo.Setup(test_distro.New().OSTreeRef())
	defer ostreeRepoDefault.TearDown()
	ostreeRepoOther := mock_ostree_repo.Setup("some/other/ref")
	defer ostreeRepoOther.TearDown()

	var cases = []struct {
		External        bool
		Method          string
		Path            string
		Body            string
		ExpectedStatus  int
		ExpectedJSON    string
		ExpectedCompose *store.Compose
		IgnoreFields    []string
	}{
		{
			true,
			"POST",
			"/api/v0/compose",
			fmt.Sprintf(`{"blueprint_name": "http-server","compose_type": "%s","branch": "master"}`, test_distro.TestImageTypeName),
			http.StatusBadRequest,
			`{"status":false,"errors":[{"id":"UnknownBlueprint","msg":"Unknown blueprint name: http-server"}]}`,
			nil,
			[]string{"build_id"},
		},
		{
			false,
			"POST",
			"/api/v0/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type": "%s","branch": "master"}`, test_distro.TestImageTypeName),
			http.StatusOK,
			`{"status": true}`,
			expectedComposeLocal,
			[]string{"build_id"},
		},
		{
			false,
			"POST",
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type":"%s","branch":"master","upload":{"image_name":"test_upload","provider":"aws","settings":{"region":"frankfurt","accessKeyID":"accesskey","secretAccessKey":"secretkey","bucket":"clay","key":"imagekey"}}}`, test_distro.TestImageTypeName),
			http.StatusOK,
			`{"status": true}`,
			expectedComposeLocalAndAws,
			[]string{"build_id"},
		},
		{
			false,
			"POST",
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test-distro-2","compose_type": "%s","branch": "master"}`, test_distro.TestImageTypeName),
			http.StatusOK,
			`{"status": true}`,
			expectedComposeGoodDistro,
			[]string{"build_id"},
		},
		{
			false,
			"POST",
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test-fedora-1","compose_type": "%s","branch": "master"}`, test_distro.TestImageTypeName),
			http.StatusBadRequest,
			`{"status": false,"errors":[{"id":"DistroError", "msg":"Unknown distribution: fedora-1"}]}`,
			nil,
			[]string{"build_id"},
		},
		{
			false,
			"POST",
			"/api/v1/compose",
			`{"blueprint_name": "test-distro-2","compose_type": "imaginary_type","branch": "master"}`,
			http.StatusBadRequest,
			`{"status": false,"errors":[{"id":"ComposeError", "msg":"Failed to get compose type \"imaginary_type\": invalid image type: imaginary_type"}]}`,
			nil,
			[]string{"build_id"},
		},

		// === OSTree params ===
		// Ref + Parent = error (parent without URL)
		{
			false,
			"POST",
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type":"%s","branch":"master","ostree":{"ref":"refid","parent":"parentid","url":""}}`, test_distro.TestImageTypeName),
			http.StatusBadRequest,
			`{"status": false, "errors":[{"id":"OSTreeOptionsError","msg":"ostree parent ref specified, but no URL to retrieve it"}]}`,
			expectedComposeOSTree,
			[]string{"build_id"},
		},
		// Valid Ref + URL = OK
		{
			false,
			"POST",
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type":"%s","branch":"master","ostree":{"ref":"%s","parent":"","url":"%s"}}`, test_distro.TestImageTypeName, ostreeRepoOther.OSTreeRef, ostreeRepoOther.Server.URL),
			http.StatusOK,
			`{"status": true}`,
			expectedComposeOSTree,
			[]string{"build_id"},
		},
		// Ref + invalid URL = error
		{
			false,
			"POST",
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type":"%s","branch":"master","ostree":{"ref":"whatever","parent":"","url":"invalid-url"}}`, test_distro.TestImageTypeName),
			http.StatusBadRequest,
			`{"status":false,"errors":[{"id":"OSTreeOptionsError","msg":"Get \"invalid-url/refs/heads/whatever\": unsupported protocol scheme \"\""}]}`,
			nil,
			[]string{"build_id"},
		},
		// Bad Ref + URL = error
		{
			false,
			"POST",
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type":"%s","branch":"master","ostree":{"ref":"/bad/ref","parent":"","url":"http://ostree/"}}`, test_distro.TestImageTypeName),
			http.StatusBadRequest,
			`{"status":false,"errors":[{"id":"OSTreeOptionsError","msg":"Invalid ostree ref \"/bad/ref\""}]}`,
			expectedComposeOSTree,
			[]string{"build_id"},
		},
		// Incorrect Ref + URL = the parameters are okay, but the ostree repo returns 404
		{
			false,
			"POST",
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type":"%s","branch":"master","ostree":{"ref":"%s","parent":"","url":"%s"}}`, test_distro.TestImageTypeName, "the/wrong/ref", ostreeRepoDefault.Server.URL),
			http.StatusBadRequest,
			fmt.Sprintf(`{"status":false,"errors":[{"id":"OSTreeOptionsError","msg":"ostree repository \"%s/refs/heads/the/wrong/ref\" returned status: 404 Not Found"}]}`, ostreeRepoDefault.Server.URL),
			expectedComposeOSTree,
			[]string{"build_id"},
		},
		// Ref + Parent + URL = OK
		{
			false,
			"POST",
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type":"%s","branch":"master","ostree":{"ref":"%s","parent":"%s","url":"%s"}}`, test_distro.TestImageTypeName, "the/new/ref", ostreeRepoOther.OSTreeRef, ostreeRepoOther.Server.URL),
			http.StatusOK,
			`{"status":true}`,
			expectedComposeOSTree,
			[]string{"build_id"},
		},
		// Parent + URL = OK
		{
			false,
			"POST",
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type":"%s","branch":"master","ostree":{"ref":"","parent":"%s","url":"%s"}}`, test_distro.TestImageTypeName, ostreeRepoDefault.OSTreeRef, ostreeRepoDefault.Server.URL),
			http.StatusOK,
			`{"status":true}`,
			expectedComposeOSTree,
			[]string{"build_id"},
		},
		// URL only = OK (uses default ref, so we need to specify URL for ostree repo with default ref)
		{
			false,
			"POST",
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type":"%s","branch":"master","ostree":{"ref":"","parent":"","url":"%s"}}`, test_distro.TestImageTypeName, ostreeRepoDefault.Server.URL),
			http.StatusOK,
			`{"status":true}`,
			expectedComposeOSTree,
			[]string{"build_id"},
		},
	}

	for _, c := range cases {
		api, s := createWeldrAPI(tempdir, fixtureGenerator)
		test.TestRoute(t, api, c.External, c.Method, c.Path, c.Body, c.ExpectedStatus, c.ExpectedJSON, c.IgnoreFields...)

		if c.ExpectedStatus != http.StatusOK {
			continue
		}

		composes := s.GetAllComposes()

		require.Equalf(t, 1, len(composes), "%s: bad compose count in store", c.Path)

		// I have no idea how to get the compose in better way
		var composeStruct store.Compose
		for _, c := range composes {
			composeStruct = c
			break
		}

		require.NotNilf(t, composeStruct.ImageBuild.Manifest, "%s: the compose in the store did not contain a blueprint", c.Path)

		if diff := cmp.Diff(composeStruct, *c.ExpectedCompose, test.IgnoreDates(), test.IgnoreUuids(), test.Ignore("Targets.Options.Location"), test.CompareImageTypes()); diff != "" {
			t.Errorf("%s: compose in store isn't the same as expected, diff:\n%s", c.Path, diff)
		}
	}
}

func TestComposeDelete(t *testing.T) {
	if len(os.Getenv("OSBUILD_COMPOSER_TEST_EXTERNAL")) > 0 {
		t.Skip("This test is for internal testing only")
	}

	var cases = []struct {
		Path               string
		ExpectedJSON       string
		ExpectedIDsInStore []string
	}{
		{"/api/v0/compose/delete/30000000-0000-0000-0000-000000000002", `{"uuids":[{"uuid":"30000000-0000-0000-0000-000000000002","status":true}],"errors":[]}`, []string{"30000000-0000-0000-0000-000000000000", "30000000-0000-0000-0000-000000000001", "30000000-0000-0000-0000-000000000003", "30000000-0000-0000-0000-000000000004"}},
		{"/api/v0/compose/delete/30000000-0000-0000-0000-000000000002,30000000-0000-0000-0000-000000000003", `{"uuids":[{"uuid":"30000000-0000-0000-0000-000000000002","status":true},{"uuid":"30000000-0000-0000-0000-000000000003","status":true}],"errors":[]}`, []string{"30000000-0000-0000-0000-000000000000", "30000000-0000-0000-0000-000000000001", "30000000-0000-0000-0000-000000000004"}},
		{"/api/v0/compose/delete/30000000-0000-0000-0000-000000000003,30000000-0000-0000-0000-000000000000", `{"uuids":[{"uuid":"30000000-0000-0000-0000-000000000003","status":true}],"errors":[{"id":"BuildInWrongState","msg":"Compose 30000000-0000-0000-0000-000000000000 is not in FINISHED or FAILED."}]}`, []string{"30000000-0000-0000-0000-000000000000", "30000000-0000-0000-0000-000000000001", "30000000-0000-0000-0000-000000000002", "30000000-0000-0000-0000-000000000004"}},
		{"/api/v0/compose/delete/30000000-0000-0000-0000-000000000003,30000000-0000-0000-0000", `{"uuids":[{"uuid":"30000000-0000-0000-0000-000000000003","status":true}],"errors":[{"id":"UnknownUUID","msg":"30000000-0000-0000-0000 is not a valid uuid"}]}`, []string{"30000000-0000-0000-0000-000000000000", "30000000-0000-0000-0000-000000000001", "30000000-0000-0000-0000-000000000002", "30000000-0000-0000-0000-000000000004"}},
		{"/api/v0/compose/delete/30000000-0000-0000-0000-000000000003,42000000-0000-0000-0000-000000000000", `{"uuids":[{"uuid":"30000000-0000-0000-0000-000000000003","status":true}],"errors":[{"id":"UnknownUUID","msg":"compose 42000000-0000-0000-0000-000000000000 doesn't exist"}]}`, []string{"30000000-0000-0000-0000-000000000000", "30000000-0000-0000-0000-000000000001", "30000000-0000-0000-0000-000000000002", "30000000-0000-0000-0000-000000000004"}},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, s := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		test.TestRoute(t, api, false, "DELETE", c.Path, "", http.StatusOK, c.ExpectedJSON)

		idsInStore := []string{}

		for id := range s.GetAllComposes() {
			idsInStore = append(idsInStore, id.String())
		}

		require.ElementsMatch(t, c.ExpectedIDsInStore, idsInStore, "%s: composes in store are different", c.Path)
	}
}

func TestComposeStatus(t *testing.T) {
	var cases = []struct {
		Fixture        rpmmd_mock.FixtureGenerator
		Method         string
		Path           string
		Body           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{rpmmd_mock.BaseFixture, "GET", "/api/v0/compose/status/30000000-0000-0000-0000-000000000000,30000000-0000-0000-0000-000000000002", ``, http.StatusOK, fmt.Sprintf(`{"uuids":[{"id":"30000000-0000-0000-0000-000000000000","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"WAITING","job_created":1574857140},{"id":"30000000-0000-0000-0000-000000000002","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FINISHED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140}]}`, test_distro.TestImageTypeName)},
		{rpmmd_mock.BaseFixture, "GET", "/api/v0/compose/status/*", ``, http.StatusOK, fmt.Sprintf(`{"uuids":[{"id":"30000000-0000-0000-0000-000000000000","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"WAITING","job_created":1574857140},{"id":"30000000-0000-0000-0000-000000000001","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"RUNNING","job_created":1574857140,"job_started":1574857140},{"id":"30000000-0000-0000-0000-000000000002","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FINISHED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140},{"id":"30000000-0000-0000-0000-000000000003","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FAILED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140},{"id":"30000000-0000-0000-0000-000000000004","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FINISHED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140}]}`, test_distro.TestImageTypeName)},
		{rpmmd_mock.BaseFixture, "GET", "/api/v0/compose/status/*?name=test", ``, http.StatusOK, fmt.Sprintf(`{"uuids":[{"id":"30000000-0000-0000-0000-000000000000","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"WAITING","job_created":1574857140},{"id":"30000000-0000-0000-0000-000000000001","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"RUNNING","job_created":1574857140,"job_started":1574857140},{"id":"30000000-0000-0000-0000-000000000002","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FINISHED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140},{"id":"30000000-0000-0000-0000-000000000003","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FAILED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140},{"id":"30000000-0000-0000-0000-000000000004","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FINISHED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140}]}`, test_distro.TestImageTypeName)},
		{rpmmd_mock.BaseFixture, "GET", "/api/v0/compose/status/*?status=FINISHED", ``, http.StatusOK, fmt.Sprintf(`{"uuids":[{"id":"30000000-0000-0000-0000-000000000002","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FINISHED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140},{"id":"30000000-0000-0000-0000-000000000004","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FINISHED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140}]}`, test_distro.TestImageTypeName)},
		{rpmmd_mock.BaseFixture, "GET", fmt.Sprintf("/api/v0/compose/status/*?type=%s", test_distro.TestImageTypeName), ``, http.StatusOK, fmt.Sprintf(`{"uuids":[{"id":"30000000-0000-0000-0000-000000000000","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"WAITING","job_created":1574857140},{"id":"30000000-0000-0000-0000-000000000001","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"RUNNING","job_created":1574857140,"job_started":1574857140},{"id":"30000000-0000-0000-0000-000000000002","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FINISHED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140},{"id":"30000000-0000-0000-0000-000000000003","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FAILED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140},{"id":"30000000-0000-0000-0000-000000000004","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FINISHED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140}]}`, test_distro.TestImageTypeName)},
		{rpmmd_mock.BaseFixture, "GET", "/api/v1/compose/status/30000000-0000-0000-0000-000000000000", ``, http.StatusOK, fmt.Sprintf(`{"uuids":[{"id":"30000000-0000-0000-0000-000000000000","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"WAITING","job_created":1574857140,"uploads":[{"uuid":"10000000-0000-0000-0000-000000000000","status":"WAITING","provider_name":"aws","image_name":"awsimage","creation_time":1574857140,"settings":{"region":"frankfurt","bucket":"clay","key":"imagekey"}}]}]}`, test_distro.TestImageTypeName)},
	}

	if len(os.Getenv("OSBUILD_COMPOSER_TEST_EXTERNAL")) > 0 {
		t.Skip("This test is for internal testing only")
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		test.TestRoute(t, api, false, c.Method, c.Path, c.Body, c.ExpectedStatus, c.ExpectedJSON, "id", "job_created", "job_started")
	}
}

func TestComposeInfo(t *testing.T) {
	var cases = []struct {
		Fixture        rpmmd_mock.FixtureGenerator
		Method         string
		Path           string
		Body           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{rpmmd_mock.BaseFixture, "GET", "/api/v0/compose/info/30000000-0000-0000-0000-000000000000", ``, http.StatusOK, fmt.Sprintf(`{"id":"30000000-0000-0000-0000-000000000000","config":"","blueprint":{"name":"test","description":"","distro":"","version":"0.0.0","packages":[],"modules":[],"groups":[]},"commit":"","deps":{"packages":[]},"compose_type":"%s","queue_status":"WAITING","image_size":0}`, test_distro.TestImageTypeName)},
		{rpmmd_mock.BaseFixture, "GET", "/api/v1/compose/info/30000000-0000-0000-0000-000000000000", ``, http.StatusOK, fmt.Sprintf(`{"id":"30000000-0000-0000-0000-000000000000","config":"","blueprint":{"name":"test","description":"","distro":"","version":"0.0.0","packages":[],"modules":[],"groups":[]},"commit":"","deps":{"packages":[]},"compose_type":"%s","queue_status":"WAITING","image_size":0,"uploads":[{"uuid":"10000000-0000-0000-0000-000000000000","status":"WAITING","provider_name":"aws","image_name":"awsimage","creation_time":1574857140,"settings":{"region":"frankfurt","bucket":"clay","key":"imagekey"}}]}`, test_distro.TestImageTypeName)},
		{rpmmd_mock.BaseFixture, "GET", "/api/v1/compose/info/30000000-0000-0000-0000", ``, http.StatusBadRequest, `{"status":false,"errors":[{"id":"UnknownUUID","msg":"30000000-0000-0000-0000 is not a valid build uuid"}]}`},
		{rpmmd_mock.BaseFixture, "GET", "/api/v1/compose/info/42000000-0000-0000-0000-000000000000", ``, http.StatusBadRequest, `{"status":false,"errors":[{"id":"UnknownUUID","msg":"42000000-0000-0000-0000-000000000000 is not a valid build uuid"}]}`},
	}

	if len(os.Getenv("OSBUILD_COMPOSER_TEST_EXTERNAL")) > 0 {
		t.Skip("This test is for internal testing only")
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		test.TestRoute(t, api, false, c.Method, c.Path, c.Body, c.ExpectedStatus, c.ExpectedJSON)
	}
}

func TestComposeLogs(t *testing.T) {
	if len(os.Getenv("OSBUILD_COMPOSER_TEST_EXTERNAL")) > 0 {
		t.Skip("This test is for internal testing only")
	}

	var successCases = []struct {
		Path                       string
		ExpectedContentDisposition string
		ExpectedContentType        string
		ExpectedFileName           string
		ExpectedFileContent        string
	}{
		{"/api/v0/compose/logs/30000000-0000-0000-0000-000000000002", "attachment; filename=30000000-0000-0000-0000-000000000002-logs.tar", "application/x-tar", "logs/osbuild.log", "The compose result is empty.\n"},
		{"/api/v1/compose/logs/30000000-0000-0000-0000-000000000002", "attachment; filename=30000000-0000-0000-0000-000000000002-logs.tar", "application/x-tar", "logs/osbuild.log", "The compose result is empty.\n"},
		{"/api/v0/compose/metadata/30000000-0000-0000-0000-000000000002", "attachment; filename=30000000-0000-0000-0000-000000000002-metadata.tar", "application/x-tar", "30000000-0000-0000-0000-000000000002.json", "{\"sources\":{},\"pipeline\":{}}"},
		{"/api/v1/compose/metadata/30000000-0000-0000-0000-000000000002", "attachment; filename=30000000-0000-0000-0000-000000000002-metadata.tar", "application/x-tar", "30000000-0000-0000-0000-000000000002.json", "{\"sources\":{},\"pipeline\":{}}"},
		{"/api/v0/compose/results/30000000-0000-0000-0000-000000000002", "attachment; filename=30000000-0000-0000-0000-000000000002.tar", "application/x-tar", "30000000-0000-0000-0000-000000000002.json", "{\"sources\":{},\"pipeline\":{}}"},
		{"/api/v1/compose/results/30000000-0000-0000-0000-000000000002", "attachment; filename=30000000-0000-0000-0000-000000000002.tar", "application/x-tar", "30000000-0000-0000-0000-000000000002.json", "{\"sources\":{},\"pipeline\":{}}"},
	}

	tempdir := t.TempDir()

	for _, c := range successCases {
		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)

		response := test.SendHTTP(api, false, "GET", c.Path, "")
		require.Equalf(t, http.StatusOK, response.StatusCode, "%s: unexpected status code", c.Path)
		require.Equalf(t, c.ExpectedContentDisposition, response.Header.Get("content-disposition"), "%s: header mismatch", c.Path)
		require.Equalf(t, c.ExpectedContentType, response.Header.Get("content-type"), "%s: header mismatch", c.Path)

		tr := tar.NewReader(response.Body)
		h, err := tr.Next()

		require.NoErrorf(t, err, "untarring failed with error")
		require.Falsef(t, h.ModTime.After(time.Now()), "ModTime cannot be in the future")
		require.Equalf(t, c.ExpectedFileName, h.Name, "%s: unexpected file name", c.Path)

		var buffer bytes.Buffer

		// vulnerability already tested
		/* #nosec G110 */
		_, err = io.Copy(&buffer, tr)
		require.NoErrorf(t, err, "cannot copy untar result")
		require.Equalf(t, c.ExpectedFileContent, buffer.String(), "%s: unexpected log content", c.Path)
	}

	var failureCases = []struct {
		Path         string
		ExpectedJSON string
	}{
		{"/api/v1/compose/logs/30000000-0000-0000-0000", `{"status":false,"errors":[{"id":"UnknownUUID","msg":"30000000-0000-0000-0000 is not a valid build uuid"}]}`},
		{"/api/v1/compose/logs/42000000-0000-0000-0000-000000000000", `{"status":false,"errors":[{"id":"UnknownUUID","msg":"Compose 42000000-0000-0000-0000-000000000000 doesn't exist"}]}`},
		{"/api/v1/compose/logs/30000000-0000-0000-0000-000000000000", `{"status":false,"errors":[{"id":"BuildInWrongState","msg":"Build 30000000-0000-0000-0000-000000000000 not in FINISHED or FAILED state."}]}`},
		{"/api/v1/compose/metadata/30000000-0000-0000-0000-000000000000", `{"status":false,"errors":[{"id":"BuildInWrongState","msg":"Build 30000000-0000-0000-0000-000000000000 is in wrong state: WAITING"}]}`},
		{"/api/v1/compose/results/30000000-0000-0000-0000-000000000000", `{"status":false,"errors":[{"id":"BuildInWrongState","msg":"Build 30000000-0000-0000-0000-000000000000 is in wrong state: WAITING"}]}`},
		{"/api/v1/compose/metadata/30000000-0000-0000-0000-000000000001", `{"status":false,"errors":[{"id":"BuildInWrongState","msg":"Build 30000000-0000-0000-0000-000000000001 is in wrong state: RUNNING"}]}`},
		{"/api/v1/compose/results/30000000-0000-0000-0000-000000000001", `{"status":false,"errors":[{"id":"BuildInWrongState","msg":"Build 30000000-0000-0000-0000-000000000001 is in wrong state: RUNNING"}]}`},
	}

	if len(os.Getenv("OSBUILD_COMPOSER_TEST_EXTERNAL")) > 0 {
		t.Skip("This test is for internal testing only")
	}

	for _, c := range failureCases {
		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		test.TestRoute(t, api, false, "GET", c.Path, "", http.StatusBadRequest, c.ExpectedJSON)
	}
}

func TestComposeLog(t *testing.T) {
	var cases = []struct {
		Fixture          rpmmd_mock.FixtureGenerator
		Method           string
		Path             string
		ExpectedStatus   int
		ExpectedResponse string
	}{
		{rpmmd_mock.BaseFixture, "GET", "/api/v0/compose/log/30000000-0000-0000-0000-000000000000", http.StatusOK, `{"status":false,"errors":[{"id":"BuildInWrongState","msg":"Build 30000000-0000-0000-0000-000000000000 has not started yet. No logs to view."}]}` + "\n"},
		{rpmmd_mock.BaseFixture, "GET", "/api/v0/compose/log/30000000-0000-0000-0000-000000000001", http.StatusOK, `Build 30000000-0000-0000-0000-000000000001 is still running.` + "\n"},
		{rpmmd_mock.BaseFixture, "GET", "/api/v0/compose/log/30000000-0000-0000-0000-000000000002", http.StatusOK, `The compose result is empty.` + "\n"},
		{rpmmd_mock.BaseFixture, "GET", "/api/v1/compose/log/30000000-0000-0000-0000-000000000002", http.StatusOK, `The compose result is empty.` + "\n"},
		{rpmmd_mock.BaseFixture, "GET", "/api/v1/compose/log/30000000-0000-0000-0000", http.StatusBadRequest, `{"status":false,"errors":[{"id":"UnknownUUID","msg":"30000000-0000-0000-0000 is not a valid build uuid"}]}` + "\n"},
		{rpmmd_mock.BaseFixture, "GET", "/api/v1/compose/log/42000000-0000-0000-0000-000000000000", http.StatusBadRequest, `{"status":false,"errors":[{"id":"UnknownUUID","msg":"Compose 42000000-0000-0000-0000-000000000000 doesn't exist"}]}` + "\n"},
	}

	if len(os.Getenv("OSBUILD_COMPOSER_TEST_EXTERNAL")) > 0 {
		t.Skip("This test is for internal testing only")
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		test.TestNonJsonRoute(t, api, false, "GET", c.Path, "", c.ExpectedStatus, c.ExpectedResponse)
	}
}

func TestComposeQueue(t *testing.T) {
	var cases = []struct {
		Fixture        rpmmd_mock.FixtureGenerator
		Method         string
		Path           string
		Body           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{rpmmd_mock.BaseFixture, "GET", "/api/v0/compose/queue", ``, http.StatusOK, fmt.Sprintf(`{"new":[{"blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"WAITING"}],"run":[{"blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"RUNNING"}]}`, test_distro.TestImageTypeName)},
		{rpmmd_mock.BaseFixture, "GET", "/api/v1/compose/queue", ``, http.StatusOK, fmt.Sprintf(`{"new":[{"blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"WAITING","uploads":[{"uuid":"10000000-0000-0000-0000-000000000000","status":"WAITING","provider_name":"aws","image_name":"awsimage","creation_time":1574857140,"settings":{"region":"frankfurt","bucket":"clay","key":"imagekey"}}]}],"run":[{"blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"RUNNING"}]}`, test_distro.TestImageTypeName)},
		{rpmmd_mock.NoComposesFixture, "GET", "/api/v0/compose/queue", ``, http.StatusOK, `{"new":[],"run":[]}`},
	}

	if len(os.Getenv("OSBUILD_COMPOSER_TEST_EXTERNAL")) > 0 {
		t.Skip("This test is for internal testing only")
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, c.Fixture)
		test.TestRoute(t, api, false, c.Method, c.Path, c.Body, c.ExpectedStatus, c.ExpectedJSON, "id", "job_created", "job_started")
	}
}

func TestComposeFinished(t *testing.T) {
	var cases = []struct {
		Fixture        rpmmd_mock.FixtureGenerator
		Method         string
		Path           string
		Body           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{rpmmd_mock.BaseFixture, "GET", "/api/v0/compose/finished", ``, http.StatusOK, fmt.Sprintf(`{"finished":[{"id":"30000000-0000-0000-0000-000000000002","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FINISHED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140},{"id":"30000000-0000-0000-0000-000000000004","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FINISHED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140}]}`, test_distro.TestImageTypeName)},
		{rpmmd_mock.BaseFixture, "GET", "/api/v1/compose/finished", ``, http.StatusOK, fmt.Sprintf(`{"finished":[{"id":"30000000-0000-0000-0000-000000000002","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FINISHED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140,"uploads":[{"uuid":"10000000-0000-0000-0000-000000000000","status":"FINISHED","provider_name":"aws","image_name":"awsimage","creation_time":1574857140,"settings":{"region":"frankfurt","bucket":"clay","key":"imagekey"}}]},{"id":"30000000-0000-0000-0000-000000000004","blueprint":"test","version":"0.0.0","compose_type":"%[1]s","image_size":0,"queue_status":"FINISHED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140,"uploads":[{"uuid":"10000000-0000-0000-0000-000000000000","status":"FINISHED","provider_name":"aws","image_name":"awsimage","creation_time":1574857140,"settings":{"region":"frankfurt","bucket":"clay","key":"imagekey"}}]}]}`, test_distro.TestImageTypeName)},
		{rpmmd_mock.NoComposesFixture, "GET", "/api/v0/compose/finished", ``, http.StatusOK, `{"finished":[]}`},
	}

	if len(os.Getenv("OSBUILD_COMPOSER_TEST_EXTERNAL")) > 0 {
		t.Skip("This test is for internal testing only")
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, c.Fixture)
		test.TestRoute(t, api, false, c.Method, c.Path, c.Body, c.ExpectedStatus, c.ExpectedJSON, "id", "job_created", "job_started")
	}
}

func TestComposeFailed(t *testing.T) {
	var cases = []struct {
		Fixture        rpmmd_mock.FixtureGenerator
		Method         string
		Path           string
		Body           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{rpmmd_mock.BaseFixture, "GET", "/api/v0/compose/failed", ``, http.StatusOK, fmt.Sprintf(`{"failed":[{"id":"30000000-0000-0000-0000-000000000003","blueprint":"test","version":"0.0.0","compose_type":"%s","image_size":0,"queue_status":"FAILED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140}]}`, test_distro.TestImageTypeName)},
		{rpmmd_mock.BaseFixture, "GET", "/api/v1/compose/failed", ``, http.StatusOK, fmt.Sprintf(`{"failed":[{"id":"30000000-0000-0000-0000-000000000003","blueprint":"test","version":"0.0.0","compose_type":"%s","image_size":0,"queue_status":"FAILED","job_created":1574857140,"job_started":1574857140,"job_finished":1574857140,"uploads":[{"uuid":"10000000-0000-0000-0000-000000000000","status":"FAILED","provider_name":"aws","image_name":"awsimage","creation_time":1574857140,"settings":{"region":"frankfurt","bucket":"clay","key":"imagekey"}}]}]}`, test_distro.TestImageTypeName)},
		{rpmmd_mock.NoComposesFixture, "GET", "/api/v0/compose/failed", ``, http.StatusOK, `{"failed":[]}`},
	}

	if len(os.Getenv("OSBUILD_COMPOSER_TEST_EXTERNAL")) > 0 {
		t.Skip("This test is for internal testing only")
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, c.Fixture)
		test.TestRoute(t, api, false, c.Method, c.Path, c.Body, c.ExpectedStatus, c.ExpectedJSON, "id", "job_created", "job_started")
	}
}

func TestSourcesNew(t *testing.T) {
	var cases = []struct {
		Method         string
		Path           string
		Body           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{"POST", "/api/v0/projects/source/new", ``, http.StatusBadRequest, `{"errors": [{"id": "ProjectsError","msg": "Missing source"}],"status":false}`},
		// Bad JSON, missing quote after name
		{"POST", "/api/v0/projects/source/new", `{"name: "fish","url": "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/","type": "yum-baseurl","check_ssl": false,"check_gpg": false}`, http.StatusBadRequest, `{"errors": [{"id": "ProjectsError","msg": "Problem parsing POST body: invalid character 'f' after object key"}],"status":false}`},
		{"POST", "/api/v0/projects/source/new", `{"name": "fish","url": "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/","type": "yum-baseurl","check_ssl": false,"check_gpg": false}`, http.StatusOK, `{"status":true}`},
		{"POST", "/api/v0/projects/source/new", `{"url": "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/","type": "yum-baseurl","check_ssl": false,"check_gpg": false}`, http.StatusBadRequest, `{"errors": [{"id": "ProjectsError","msg": "Problem parsing POST body: 'name' field is missing from request"}],"status":false}`},
		{"POST", "/api/v0/projects/source/new", `{"name": "fish", "type": "yum-baseurl","check_ssl": false,"check_gpg": false}`, http.StatusBadRequest, `{"errors": [{"id": "ProjectsError","msg": "Problem parsing POST body: 'url' field is missing from request"}],"status":false}`},
		{"POST", "/api/v0/projects/source/new", `{"name": "fish", "url": "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/","check_ssl": false,"check_gpg": false}`, http.StatusBadRequest, `{"errors": [{"id": "ProjectsError","msg": "Problem parsing POST body: 'type' field is missing from request"}],"status":false}`},
		{"POST", "/api/v0/projects/source/new", `{"name": "test-id", "url": "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/","type": "yum-baseurl","check_ssl": false,"check_gpg": false}`, http.StatusBadRequest, `{"errors": [{"id": "SystemSource","msg": "test-id is a system source, it cannot be changed."}],"status":false}`},
		{"POST", "/api/v1/projects/source/new", `{"id": "test-id", "name": "test system repo", "url": "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/","type": "yum-baseurl","check_ssl": false,"check_gpg": false}`, http.StatusBadRequest, `{"errors": [{"id": "SystemSource","msg": "test-id is a system source, it cannot be changed."}],"status":false}`},
		{"POST", "/api/v1/projects/source/new", `{"id": "fish","name":"fish repo","url": "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/","type": "yum-baseurl","check_ssl": false,"check_gpg": false,"distros":["test-distro", "test-distro-2"]}`, http.StatusOK, `{"status":true}`},
		{"POST", "/api/v1/projects/source/new", `{"id": "fish","name":"fish repo","url": "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/","type": "yum-baseurl","check_ssl": false,"check_gpg": false,"distros":["fedora-1"]}`, http.StatusBadRequest, `{"status":false, "errors":[{"id":"ProjectsError", "msg":"Invalid distributions: fedora-1"}]}`},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		test.TestRoute(t, api, true, c.Method, c.Path, c.Body, c.ExpectedStatus, c.ExpectedJSON)
		test.SendHTTP(api, true, "DELETE", "/api/v0/projects/source/delete/fish", ``)
	}
}

func TestSourcesNewTomlV0(t *testing.T) {
	tempdir := t.TempDir()

	sources := []string{`
name = "fish"
url = "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/"
type = "yum-baseurl"
check_ssl = false
check_gpg = false
`, `[fish]
name = "fish"
url = "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/"
type = "yum-baseurl"
check_ssl = false
check_gpg = false
`}
	for _, source := range sources {
		req := httptest.NewRequest("POST", "/api/v0/projects/source/new", bytes.NewReader([]byte(source)))
		req.Header.Set("Content-Type", "text/x-toml")
		recorder := httptest.NewRecorder()

		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		api.ServeHTTP(recorder, req)

		r := recorder.Result()
		require.Equal(t, http.StatusOK, r.StatusCode)

		test.SendHTTP(api, true, "DELETE", "/api/v0/projects/source/delete/fish", ``)
	}
}

// Empty TOML, and invalid TOML should return an error
func TestSourcesNewWrongTomlV0(t *testing.T) {
	tempdir := t.TempDir()

	sources := []string{``, `
url = "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/"
type = "yum-baseurl"
check_ssl = false
check_gpg = false
`}
	for _, source := range sources {
		req := httptest.NewRequest("POST", "/api/v0/projects/source/new", bytes.NewReader([]byte(source)))
		req.Header.Set("Content-Type", "text/x-toml")
		recorder := httptest.NewRecorder()

		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		api.ServeHTTP(recorder, req)

		r := recorder.Result()
		require.Equal(t, http.StatusBadRequest, r.StatusCode)
	}
}

// TestSourcesNewTomlV1 tests the v1 sources API with id and name
func TestSourcesNewTomlV1(t *testing.T) {
	tempdir := t.TempDir()

	sources := []string{`
id = "fish"
name = "fish or cut bait"
url = "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/"
type = "yum-baseurl"
check_ssl = false
check_gpg = false
`, `[fish]
name = "fish or cut bait"
url = "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/"
type = "yum-baseurl"
check_ssl = false
check_gpg = false
`, `[fish]
id = "fish"
name = "fish or cut bait"
url = "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/"
type = "yum-baseurl"
check_ssl = false
check_gpg = false
`}
	for _, source := range sources {
		req := httptest.NewRequest("POST", "/api/v1/projects/source/new", bytes.NewReader([]byte(source)))
		req.Header.Set("Content-Type", "text/x-toml")
		recorder := httptest.NewRecorder()

		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		api.ServeHTTP(recorder, req)

		r := recorder.Result()
		require.Equal(t, http.StatusOK, r.StatusCode)

		test.SendHTTP(api, true, "DELETE", "/api/v1/projects/source/delete/fish", ``)
	}
}

func TestSourcesInfoTomlV1(t *testing.T) {
	source := `
id = "fish"
name = "fish"
url = "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/"
type = "yum-baseurl"
rhsm = true
`

	sourceStr := `{"check_gpg":false,"check_ssl":false,"id":"fish","name":"fish","rhsm":true,"system":false,"type":"yum-baseurl","url":"https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/"}`

	req := httptest.NewRequest("POST", "/api/v1/projects/source/new", bytes.NewReader([]byte(source)))
	req.Header.Set("Content-Type", "text/x-toml")
	recorder := httptest.NewRecorder()

	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.BaseFixture)
	api.ServeHTTP(recorder, req)

	r := recorder.Result()
	require.Equal(t, http.StatusOK, r.StatusCode)
	test.TestRoute(t, api, true, "GET", "/api/v1/projects/source/info/fish", ``, 200, `{"sources":{"fish":`+sourceStr+`},"errors":[]}`)
	test.TestRoute(t, api, true, "GET", "/api/v1/projects/source/info/fish?format=json", ``, 200, `{"sources":{"fish":`+sourceStr+`},"errors":[]}`)
}

// TestSourcesNewWrongTomlV1 Tests that Empty TOML, and invalid TOML should return an error
func TestSourcesNewWrongTomlV1(t *testing.T) {
	tempdir := t.TempDir()

	sources := []string{``, `
url = "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/"
type = "yum-baseurl"
check_ssl = false
check_gpg = false
`, `
[fish]
url = "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/"
type = "yum-baseurl"
check_ssl = false
check_gpg = false
`, `
id = "fish"
url = "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/"
type = "yum-baseurl"
check_ssl = false
check_gpg = false
`}
	for _, source := range sources {
		req := httptest.NewRequest("POST", "/api/v1/projects/source/new", bytes.NewReader([]byte(source)))
		req.Header.Set("Content-Type", "text/x-toml")
		recorder := httptest.NewRecorder()

		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		api.ServeHTTP(recorder, req)

		r := recorder.Result()
		require.Equal(t, http.StatusBadRequest, r.StatusCode)
	}
}

func TestSourcesInfo(t *testing.T) {
	sourceStr := `{"name":"fish","type":"yum-baseurl","url":"https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/","check_gpg":false,"check_ssl":false,"system":false}`

	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.BaseFixture)
	test.SendHTTP(api, true, "POST", "/api/v0/projects/source/new", sourceStr)
	test.TestRoute(t, api, true, "GET", "/api/v0/projects/source/info/fish", ``, 200, `{"sources":{"fish":`+sourceStr+`},"errors":[]}`)
	test.TestRoute(t, api, true, "GET", "/api/v0/projects/source/info/fish?format=json", ``, 200, `{"sources":{"fish":`+sourceStr+`},"errors":[]}`)
	test.TestRoute(t, api, true, "GET", "/api/v0/projects/source/info/fish?format=son", ``, 400, `{"status":false,"errors":[{"id":"InvalidChars","msg":"invalid format parameter: son"}]}`)
}

func TestSourcesInfoToml(t *testing.T) {
	sourceStr := `{"name":"fish","type":"yum-baseurl","url":"https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/","check_gpg":false,"check_ssl":false,"system":false}`

	api, _ := createWeldrAPI(t.TempDir(), rpmmd_mock.BaseFixture)
	test.SendHTTP(api, true, "POST", "/api/v0/projects/source/new", sourceStr)

	req := httptest.NewRequest("GET", "/api/v0/projects/source/info/fish?format=toml", nil)
	recorder := httptest.NewRecorder()
	api.ServeHTTP(recorder, req)
	resp := recorder.Result()

	var sources map[string]store.SourceConfig
	_, err := toml.NewDecoder(resp.Body).Decode(&sources)
	require.NoErrorf(t, err, "error decoding toml file")

	expected := map[string]store.SourceConfig{
		"fish": {
			Name: "fish",
			Type: "yum-baseurl",
			URL:  "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/",
		},
	}

	require.Equal(t, expected, sources)
}

func TestSourcesDelete(t *testing.T) {
	var cases = []struct {
		Method         string
		Path           string
		Body           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{"DELETE", "/api/v0/projects/source/delete/", ``, http.StatusNotFound, `{"status":false,"errors":[{"code":404,"id":"HTTPError","msg":"Not Found"}]}`},
		{"DELETE", "/api/v0/projects/source/delete/fish", ``, http.StatusOK, `{"status":true}`},
		{"DELETE", "/api/v0/projects/source/delete/unknown", ``, http.StatusBadRequest, `{"status":false,"errors":[{"id":"UnknownSource","msg":"unknown is not a valid source."}]}`},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, rpmmd_mock.BaseFixture)
		test.SendHTTP(api, true, "POST", "/api/v0/projects/source/new", `{"name": "fish","url": "https://download.opensuse.org/repositories/shells:/fish:/release:/3/Fedora_29/","type": "yum-baseurl","check_ssl": false,"check_gpg": false}`)
		test.TestRoute(t, api, true, c.Method, c.Path, c.Body, c.ExpectedStatus, c.ExpectedJSON)
		test.SendHTTP(api, true, "DELETE", "/api/v0/projects/source/delete/fish", ``)
	}
}

func TestProjectsDepsolve(t *testing.T) {
	var cases = []struct {
		Fixture        rpmmd_mock.FixtureGenerator
		Path           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{rpmmd_mock.NonExistingPackage, "/api/v0/projects/depsolve/fash", http.StatusBadRequest, `{"status":false,"errors":[{"id":"ProjectsError","msg":"BadRequest: DNF error occured: MarkingErrors: Error occurred when marking packages for installation: Problems in request:\nmissing packages: fash"}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/projects/depsolve/fish", http.StatusOK, `{"projects":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/projects/depsolve/fish?distro=test-distro-2", http.StatusOK, `{"projects":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]}`},
		{rpmmd_mock.BadDepsolve, "/api/v0/projects/depsolve/go2rpm", http.StatusBadRequest, `{"status":false,"errors":[{"id":"ProjectsError","msg":"BadRequest: DNF error occured: DepsolveError: There was a problem depsolving ['go2rpm']: \n Problem: conflicting requests\n  - nothing provides askalono-cli needed by go2rpm-1-4.fc31.noarch"}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/projects/depsolve/fish?distro=fedora-1", http.StatusBadRequest, `{"status":false,"errors":[{"id":"DistroError","msg":"Invalid distro: fedora-1"}]}`},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, c.Fixture)
		test.TestRoute(t, api, true, "GET", c.Path, ``, c.ExpectedStatus, c.ExpectedJSON)
	}
}

func TestProjectsInfo(t *testing.T) {
	var cases = []struct {
		Fixture        rpmmd_mock.FixtureGenerator
		Path           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{rpmmd_mock.BaseFixture, "/api/v0/projects/info", http.StatusBadRequest, `{"status":false,"errors":[{"id":"UnknownProject","msg":"No packages specified."}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/projects/info/", http.StatusBadRequest, `{"status":false,"errors":[{"id":"UnknownProject","msg":"No packages specified."}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/projects/info/nonexistingpkg", http.StatusBadRequest, `{"status":false,"errors":[{"id":"UnknownProject","msg":"No packages have been found."}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/projects/info/*", http.StatusOK, `{"projects":[{"name":"package0","summary":"pkg0 sum","description":"pkg0 desc","homepage":"https://pkg0.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-01-03T15:04:05","epoch":0,"release":"0.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"0.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-01-02T15:04:05","epoch":0,"release":"0.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"0.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package1","summary":"pkg1 sum","description":"pkg1 desc","homepage":"https://pkg1.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-02-02T15:04:05","epoch":0,"release":"1.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"1.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-02-03T15:04:05","epoch":0,"release":"1.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"1.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package10","summary":"pkg10 sum","description":"pkg10 desc","homepage":"https://pkg10.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-11-02T15:04:05","epoch":0,"release":"10.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"10.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-11-03T15:04:05","epoch":0,"release":"10.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"10.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package11","summary":"pkg11 sum","description":"pkg11 desc","homepage":"https://pkg11.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-12-03T15:04:05","epoch":0,"release":"11.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"11.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-12-02T15:04:05","epoch":0,"release":"11.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"11.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package12","summary":"pkg12 sum","description":"pkg12 desc","homepage":"https://pkg12.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-01-02T15:04:05","epoch":0,"release":"12.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"12.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-01-03T15:04:05","epoch":0,"release":"12.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"12.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package13","summary":"pkg13 sum","description":"pkg13 desc","homepage":"https://pkg13.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-02-02T15:04:05","epoch":0,"release":"13.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"13.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-02-03T15:04:05","epoch":0,"release":"13.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"13.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package14","summary":"pkg14 sum","description":"pkg14 desc","homepage":"https://pkg14.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-03-03T15:04:05","epoch":0,"release":"14.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"14.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-03-02T15:04:05","epoch":0,"release":"14.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"14.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package15","summary":"pkg15 sum","description":"pkg15 desc","homepage":"https://pkg15.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-04-03T15:04:05","epoch":0,"release":"15.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"15.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-04-02T15:04:05","epoch":0,"release":"15.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"15.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package16","summary":"pkg16 sum","description":"pkg16 desc","homepage":"https://pkg16.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-05-02T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-05-03T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package17","summary":"pkg17 sum","description":"pkg17 desc","homepage":"https://pkg17.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-06-03T15:04:05","epoch":0,"release":"17.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"17.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-06-02T15:04:05","epoch":0,"release":"17.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"17.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package18","summary":"pkg18 sum","description":"pkg18 desc","homepage":"https://pkg18.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-07-02T15:04:05","epoch":0,"release":"18.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"18.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-07-03T15:04:05","epoch":0,"release":"18.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"18.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package19","summary":"pkg19 sum","description":"pkg19 desc","homepage":"https://pkg19.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-08-03T15:04:05","epoch":0,"release":"19.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"19.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-08-02T15:04:05","epoch":0,"release":"19.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"19.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package2","summary":"pkg2 sum","description":"pkg2 desc","homepage":"https://pkg2.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-03-02T15:04:05","epoch":0,"release":"2.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"2.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-03-03T15:04:05","epoch":0,"release":"2.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"2.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package20","summary":"pkg20 sum","description":"pkg20 desc","homepage":"https://pkg20.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-09-03T15:04:05","epoch":0,"release":"20.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"20.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-09-02T15:04:05","epoch":0,"release":"20.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"20.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package21","summary":"pkg21 sum","description":"pkg21 desc","homepage":"https://pkg21.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-10-02T15:04:05","epoch":0,"release":"21.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"21.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-10-03T15:04:05","epoch":0,"release":"21.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"21.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package3","summary":"pkg3 sum","description":"pkg3 desc","homepage":"https://pkg3.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-04-03T15:04:05","epoch":0,"release":"3.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"3.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-04-02T15:04:05","epoch":0,"release":"3.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"3.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package4","summary":"pkg4 sum","description":"pkg4 desc","homepage":"https://pkg4.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-05-03T15:04:05","epoch":0,"release":"4.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"4.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-05-02T15:04:05","epoch":0,"release":"4.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"4.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package5","summary":"pkg5 sum","description":"pkg5 desc","homepage":"https://pkg5.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-06-03T15:04:05","epoch":0,"release":"5.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"5.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-06-02T15:04:05","epoch":0,"release":"5.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"5.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package6","summary":"pkg6 sum","description":"pkg6 desc","homepage":"https://pkg6.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-07-02T15:04:05","epoch":0,"release":"6.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"6.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-07-03T15:04:05","epoch":0,"release":"6.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"6.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package7","summary":"pkg7 sum","description":"pkg7 desc","homepage":"https://pkg7.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-08-02T15:04:05","epoch":0,"release":"7.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"7.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-08-03T15:04:05","epoch":0,"release":"7.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"7.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package8","summary":"pkg8 sum","description":"pkg8 desc","homepage":"https://pkg8.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-09-03T15:04:05","epoch":0,"release":"8.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"8.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-09-02T15:04:05","epoch":0,"release":"8.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"8.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package9","summary":"pkg9 sum","description":"pkg9 desc","homepage":"https://pkg9.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-10-02T15:04:05","epoch":0,"release":"9.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"9.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-10-03T15:04:05","epoch":0,"release":"9.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"9.1","source_ref":"SOURCE_REF","metadata":{}}}]}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/projects/info/package2*,package16", http.StatusOK, `{"projects":[{"name":"package16","summary":"pkg16 sum","description":"pkg16 desc","homepage":"https://pkg16.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-05-02T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-05-03T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package2","summary":"pkg2 sum","description":"pkg2 desc","homepage":"https://pkg2.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-03-02T15:04:05","epoch":0,"release":"2.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"2.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-03-03T15:04:05","epoch":0,"release":"2.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"2.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package20","summary":"pkg20 sum","description":"pkg20 desc","homepage":"https://pkg20.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-09-03T15:04:05","epoch":0,"release":"20.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"20.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-09-02T15:04:05","epoch":0,"release":"20.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"20.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package21","summary":"pkg21 sum","description":"pkg21 desc","homepage":"https://pkg21.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-10-02T15:04:05","epoch":0,"release":"21.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"21.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-10-03T15:04:05","epoch":0,"release":"21.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"21.1","source_ref":"SOURCE_REF","metadata":{}}}]}]}`},
		{rpmmd_mock.BadFetch, "/api/v0/projects/info/package2*,package16", http.StatusBadRequest, `{"status":false,"errors":[{"id":"ModulesError","msg":"msg: DNF error occured: FetchError: There was a problem when fetching packages."}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/projects/info/package16?distro=test-distro-2", http.StatusOK, `{"projects":[{"name":"package16","summary":"pkg16 sum","description":"pkg16 desc","homepage":"https://pkg16.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-05-02T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-05-03T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.1","source_ref":"SOURCE_REF","metadata":{}}}]}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/projects/info/package16?distro=fedora-1", http.StatusBadRequest, `{"status":false,"errors":[{"id":"DistroError","msg":"Invalid distro: fedora-1"}]}`},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, c.Fixture)
		test.TestRoute(t, api, true, "GET", c.Path, ``, c.ExpectedStatus, c.ExpectedJSON)
	}
}

func TestModulesInfo(t *testing.T) {
	var cases = []struct {
		Fixture        rpmmd_mock.FixtureGenerator
		Path           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{rpmmd_mock.BaseFixture, "/api/v0/modules/info", http.StatusBadRequest, `{"status":false,"errors":[{"id":"UnknownModule","msg":"No packages specified."}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/modules/info/", http.StatusBadRequest, `{"status":false,"errors":[{"id":"UnknownModule","msg":"No packages specified."}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/modules/info/nonexistingpkg", http.StatusBadRequest, `{"status":false,"errors":[{"id":"UnknownModule","msg":"No packages have been found."}]}`},
		{rpmmd_mock.BadDepsolve, "/api/v0/modules/info/package1", http.StatusBadRequest, `{"status":false,"errors":[{"id":"ModulesError","msg":"Cannot depsolve package package1: DNF error occured: DepsolveError: There was a problem depsolving ['go2rpm']: \n Problem: conflicting requests\n  - nothing provides askalono-cli needed by go2rpm-1-4.fc31.noarch"}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/modules/info/package2*,package16", http.StatusOK, `{"modules":[{"name":"package16","summary":"pkg16 sum","description":"pkg16 desc","homepage":"https://pkg16.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-05-02T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-05-03T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package2","summary":"pkg2 sum","description":"pkg2 desc","homepage":"https://pkg2.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-03-02T15:04:05","epoch":0,"release":"2.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"2.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-03-03T15:04:05","epoch":0,"release":"2.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"2.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package20","summary":"pkg20 sum","description":"pkg20 desc","homepage":"https://pkg20.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-09-03T15:04:05","epoch":0,"release":"20.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"20.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-09-02T15:04:05","epoch":0,"release":"20.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"20.0","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package21","summary":"pkg21 sum","description":"pkg21 desc","homepage":"https://pkg21.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-10-02T15:04:05","epoch":0,"release":"21.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"21.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-10-03T15:04:05","epoch":0,"release":"21.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"21.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/modules/info/*", http.StatusOK, `{"modules":[{"name":"package0","summary":"pkg0 sum","description":"pkg0 desc","homepage":"https://pkg0.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-01-03T15:04:05","epoch":0,"release":"0.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"0.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-01-02T15:04:05","epoch":0,"release":"0.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"0.0","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package1","summary":"pkg1 sum","description":"pkg1 desc","homepage":"https://pkg1.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-02-02T15:04:05","epoch":0,"release":"1.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"1.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-02-03T15:04:05","epoch":0,"release":"1.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"1.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package10","summary":"pkg10 sum","description":"pkg10 desc","homepage":"https://pkg10.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-11-02T15:04:05","epoch":0,"release":"10.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"10.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-11-03T15:04:05","epoch":0,"release":"10.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"10.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package11","summary":"pkg11 sum","description":"pkg11 desc","homepage":"https://pkg11.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-12-03T15:04:05","epoch":0,"release":"11.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"11.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-12-02T15:04:05","epoch":0,"release":"11.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"11.0","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package12","summary":"pkg12 sum","description":"pkg12 desc","homepage":"https://pkg12.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-01-02T15:04:05","epoch":0,"release":"12.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"12.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-01-03T15:04:05","epoch":0,"release":"12.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"12.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package13","summary":"pkg13 sum","description":"pkg13 desc","homepage":"https://pkg13.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-02-02T15:04:05","epoch":0,"release":"13.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"13.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-02-03T15:04:05","epoch":0,"release":"13.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"13.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package14","summary":"pkg14 sum","description":"pkg14 desc","homepage":"https://pkg14.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-03-03T15:04:05","epoch":0,"release":"14.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"14.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-03-02T15:04:05","epoch":0,"release":"14.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"14.0","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package15","summary":"pkg15 sum","description":"pkg15 desc","homepage":"https://pkg15.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-04-03T15:04:05","epoch":0,"release":"15.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"15.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-04-02T15:04:05","epoch":0,"release":"15.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"15.0","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package16","summary":"pkg16 sum","description":"pkg16 desc","homepage":"https://pkg16.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-05-02T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-05-03T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package17","summary":"pkg17 sum","description":"pkg17 desc","homepage":"https://pkg17.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-06-03T15:04:05","epoch":0,"release":"17.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"17.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-06-02T15:04:05","epoch":0,"release":"17.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"17.0","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package18","summary":"pkg18 sum","description":"pkg18 desc","homepage":"https://pkg18.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-07-02T15:04:05","epoch":0,"release":"18.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"18.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-07-03T15:04:05","epoch":0,"release":"18.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"18.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package19","summary":"pkg19 sum","description":"pkg19 desc","homepage":"https://pkg19.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-08-03T15:04:05","epoch":0,"release":"19.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"19.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-08-02T15:04:05","epoch":0,"release":"19.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"19.0","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package2","summary":"pkg2 sum","description":"pkg2 desc","homepage":"https://pkg2.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-03-02T15:04:05","epoch":0,"release":"2.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"2.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-03-03T15:04:05","epoch":0,"release":"2.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"2.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package20","summary":"pkg20 sum","description":"pkg20 desc","homepage":"https://pkg20.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-09-03T15:04:05","epoch":0,"release":"20.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"20.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-09-02T15:04:05","epoch":0,"release":"20.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"20.0","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package21","summary":"pkg21 sum","description":"pkg21 desc","homepage":"https://pkg21.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-10-02T15:04:05","epoch":0,"release":"21.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"21.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-10-03T15:04:05","epoch":0,"release":"21.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"21.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package3","summary":"pkg3 sum","description":"pkg3 desc","homepage":"https://pkg3.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-04-03T15:04:05","epoch":0,"release":"3.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"3.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-04-02T15:04:05","epoch":0,"release":"3.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"3.0","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package4","summary":"pkg4 sum","description":"pkg4 desc","homepage":"https://pkg4.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-05-03T15:04:05","epoch":0,"release":"4.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"4.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-05-02T15:04:05","epoch":0,"release":"4.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"4.0","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package5","summary":"pkg5 sum","description":"pkg5 desc","homepage":"https://pkg5.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-06-03T15:04:05","epoch":0,"release":"5.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"5.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-06-02T15:04:05","epoch":0,"release":"5.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"5.0","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package6","summary":"pkg6 sum","description":"pkg6 desc","homepage":"https://pkg6.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-07-02T15:04:05","epoch":0,"release":"6.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"6.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-07-03T15:04:05","epoch":0,"release":"6.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"6.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package7","summary":"pkg7 sum","description":"pkg7 desc","homepage":"https://pkg7.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-08-02T15:04:05","epoch":0,"release":"7.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"7.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-08-03T15:04:05","epoch":0,"release":"7.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"7.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package8","summary":"pkg8 sum","description":"pkg8 desc","homepage":"https://pkg8.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-09-03T15:04:05","epoch":0,"release":"8.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"8.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-09-02T15:04:05","epoch":0,"release":"8.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"8.0","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package9","summary":"pkg9 sum","description":"pkg9 desc","homepage":"https://pkg9.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-10-02T15:04:05","epoch":0,"release":"9.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"9.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-10-03T15:04:05","epoch":0,"release":"9.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"9.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]}]}`},
		{rpmmd_mock.BadFetch, "/api/v0/modules/info/package2*,package16", http.StatusBadRequest, `{"status":false,"errors":[{"id":"ModulesError","msg":"msg: DNF error occured: FetchError: There was a problem when fetching packages."}]}`},
		{rpmmd_mock.BaseFixture, "/api/v1/modules/info/package2*,package16", http.StatusOK, `{"modules":[{"name":"package16","summary":"pkg16 sum","description":"pkg16 desc","homepage":"https://pkg16.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-05-02T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-05-03T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package2","summary":"pkg2 sum","description":"pkg2 desc","homepage":"https://pkg2.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-03-02T15:04:05","epoch":0,"release":"2.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"2.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-03-03T15:04:05","epoch":0,"release":"2.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"2.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package20","summary":"pkg20 sum","description":"pkg20 desc","homepage":"https://pkg20.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-09-03T15:04:05","epoch":0,"release":"20.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"20.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-09-02T15:04:05","epoch":0,"release":"20.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"20.0","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]},{"name":"package21","summary":"pkg21 sum","description":"pkg21 desc","homepage":"https://pkg21.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-10-02T15:04:05","epoch":0,"release":"21.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"21.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-10-03T15:04:05","epoch":0,"release":"21.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"21.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]}]}`},
		{rpmmd_mock.BaseFixture, "/api/v1/modules/info/package16?distro=test-distro-2", http.StatusOK, `{"modules":[{"name":"package16","summary":"pkg16 sum","description":"pkg16 desc","homepage":"https://pkg16.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-05-02T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-05-03T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.1","source_ref":"SOURCE_REF","metadata":{}}}],"dependencies":[{"name":"dep-package3","epoch":7,"version":"3.0.3","release":"1.fc30","arch":"x86_64"},{"name":"dep-package1","epoch":0,"version":"1.33","release":"2.fc30","arch":"x86_64"},{"name":"dep-package2","epoch":0,"version":"2.9","release":"1.fc30","arch":"x86_64"}]}]}`},
		{rpmmd_mock.BaseFixture, "/api/v1/modules/info/package16?distro=fedora-1", http.StatusBadRequest, `{"status":false,"errors":[{"id":"DistroError","msg":"Invalid distro: fedora-1"}]}`},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, c.Fixture)
		test.TestRoute(t, api, true, "GET", c.Path, ``, c.ExpectedStatus, c.ExpectedJSON)
	}
}

func TestProjectsList(t *testing.T) {
	var cases = []struct {
		Fixture        rpmmd_mock.FixtureGenerator
		Path           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{rpmmd_mock.BaseFixture, "/api/v0/projects/list", http.StatusOK, `{"total":22,"offset":0,"limit":20,"projects":[{"name":"package0","summary":"pkg0 sum","description":"pkg0 desc","homepage":"https://pkg0.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-01-03T15:04:05","epoch":0,"release":"0.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"0.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-01-02T15:04:05","epoch":0,"release":"0.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"0.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package1","summary":"pkg1 sum","description":"pkg1 desc","homepage":"https://pkg1.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-02-02T15:04:05","epoch":0,"release":"1.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"1.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-02-03T15:04:05","epoch":0,"release":"1.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"1.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package10","summary":"pkg10 sum","description":"pkg10 desc","homepage":"https://pkg10.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-11-02T15:04:05","epoch":0,"release":"10.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"10.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-11-03T15:04:05","epoch":0,"release":"10.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"10.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package11","summary":"pkg11 sum","description":"pkg11 desc","homepage":"https://pkg11.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-12-03T15:04:05","epoch":0,"release":"11.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"11.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-12-02T15:04:05","epoch":0,"release":"11.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"11.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package12","summary":"pkg12 sum","description":"pkg12 desc","homepage":"https://pkg12.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-01-02T15:04:05","epoch":0,"release":"12.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"12.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-01-03T15:04:05","epoch":0,"release":"12.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"12.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package13","summary":"pkg13 sum","description":"pkg13 desc","homepage":"https://pkg13.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-02-02T15:04:05","epoch":0,"release":"13.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"13.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-02-03T15:04:05","epoch":0,"release":"13.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"13.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package14","summary":"pkg14 sum","description":"pkg14 desc","homepage":"https://pkg14.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-03-03T15:04:05","epoch":0,"release":"14.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"14.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-03-02T15:04:05","epoch":0,"release":"14.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"14.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package15","summary":"pkg15 sum","description":"pkg15 desc","homepage":"https://pkg15.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-04-03T15:04:05","epoch":0,"release":"15.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"15.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-04-02T15:04:05","epoch":0,"release":"15.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"15.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package16","summary":"pkg16 sum","description":"pkg16 desc","homepage":"https://pkg16.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-05-02T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-05-03T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package17","summary":"pkg17 sum","description":"pkg17 desc","homepage":"https://pkg17.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-06-03T15:04:05","epoch":0,"release":"17.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"17.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-06-02T15:04:05","epoch":0,"release":"17.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"17.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package18","summary":"pkg18 sum","description":"pkg18 desc","homepage":"https://pkg18.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-07-02T15:04:05","epoch":0,"release":"18.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"18.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-07-03T15:04:05","epoch":0,"release":"18.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"18.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package19","summary":"pkg19 sum","description":"pkg19 desc","homepage":"https://pkg19.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-08-03T15:04:05","epoch":0,"release":"19.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"19.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-08-02T15:04:05","epoch":0,"release":"19.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"19.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package2","summary":"pkg2 sum","description":"pkg2 desc","homepage":"https://pkg2.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-03-02T15:04:05","epoch":0,"release":"2.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"2.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-03-03T15:04:05","epoch":0,"release":"2.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"2.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package20","summary":"pkg20 sum","description":"pkg20 desc","homepage":"https://pkg20.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-09-03T15:04:05","epoch":0,"release":"20.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"20.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-09-02T15:04:05","epoch":0,"release":"20.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"20.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package21","summary":"pkg21 sum","description":"pkg21 desc","homepage":"https://pkg21.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-10-02T15:04:05","epoch":0,"release":"21.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"21.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-10-03T15:04:05","epoch":0,"release":"21.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"21.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package3","summary":"pkg3 sum","description":"pkg3 desc","homepage":"https://pkg3.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-04-03T15:04:05","epoch":0,"release":"3.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"3.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-04-02T15:04:05","epoch":0,"release":"3.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"3.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package4","summary":"pkg4 sum","description":"pkg4 desc","homepage":"https://pkg4.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-05-03T15:04:05","epoch":0,"release":"4.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"4.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-05-02T15:04:05","epoch":0,"release":"4.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"4.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package5","summary":"pkg5 sum","description":"pkg5 desc","homepage":"https://pkg5.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-06-03T15:04:05","epoch":0,"release":"5.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"5.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-06-02T15:04:05","epoch":0,"release":"5.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"5.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package6","summary":"pkg6 sum","description":"pkg6 desc","homepage":"https://pkg6.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-07-02T15:04:05","epoch":0,"release":"6.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"6.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-07-03T15:04:05","epoch":0,"release":"6.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"6.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package7","summary":"pkg7 sum","description":"pkg7 desc","homepage":"https://pkg7.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-08-02T15:04:05","epoch":0,"release":"7.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"7.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-08-03T15:04:05","epoch":0,"release":"7.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"7.1","source_ref":"SOURCE_REF","metadata":{}}}]}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/projects/list/", http.StatusOK, `{"total":22,"offset":0,"limit":20,"projects":[{"name":"package0","summary":"pkg0 sum","description":"pkg0 desc","homepage":"https://pkg0.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-01-03T15:04:05","epoch":0,"release":"0.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"0.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-01-02T15:04:05","epoch":0,"release":"0.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"0.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package1","summary":"pkg1 sum","description":"pkg1 desc","homepage":"https://pkg1.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-02-02T15:04:05","epoch":0,"release":"1.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"1.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-02-03T15:04:05","epoch":0,"release":"1.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"1.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package10","summary":"pkg10 sum","description":"pkg10 desc","homepage":"https://pkg10.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-11-02T15:04:05","epoch":0,"release":"10.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"10.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-11-03T15:04:05","epoch":0,"release":"10.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"10.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package11","summary":"pkg11 sum","description":"pkg11 desc","homepage":"https://pkg11.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-12-03T15:04:05","epoch":0,"release":"11.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"11.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-12-02T15:04:05","epoch":0,"release":"11.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"11.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package12","summary":"pkg12 sum","description":"pkg12 desc","homepage":"https://pkg12.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-01-02T15:04:05","epoch":0,"release":"12.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"12.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-01-03T15:04:05","epoch":0,"release":"12.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"12.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package13","summary":"pkg13 sum","description":"pkg13 desc","homepage":"https://pkg13.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-02-02T15:04:05","epoch":0,"release":"13.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"13.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-02-03T15:04:05","epoch":0,"release":"13.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"13.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package14","summary":"pkg14 sum","description":"pkg14 desc","homepage":"https://pkg14.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-03-03T15:04:05","epoch":0,"release":"14.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"14.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-03-02T15:04:05","epoch":0,"release":"14.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"14.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package15","summary":"pkg15 sum","description":"pkg15 desc","homepage":"https://pkg15.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-04-03T15:04:05","epoch":0,"release":"15.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"15.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-04-02T15:04:05","epoch":0,"release":"15.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"15.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package16","summary":"pkg16 sum","description":"pkg16 desc","homepage":"https://pkg16.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-05-02T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-05-03T15:04:05","epoch":0,"release":"16.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"16.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package17","summary":"pkg17 sum","description":"pkg17 desc","homepage":"https://pkg17.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-06-03T15:04:05","epoch":0,"release":"17.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"17.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-06-02T15:04:05","epoch":0,"release":"17.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"17.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package18","summary":"pkg18 sum","description":"pkg18 desc","homepage":"https://pkg18.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-07-02T15:04:05","epoch":0,"release":"18.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"18.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-07-03T15:04:05","epoch":0,"release":"18.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"18.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package19","summary":"pkg19 sum","description":"pkg19 desc","homepage":"https://pkg19.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-08-03T15:04:05","epoch":0,"release":"19.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"19.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-08-02T15:04:05","epoch":0,"release":"19.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"19.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package2","summary":"pkg2 sum","description":"pkg2 desc","homepage":"https://pkg2.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-03-02T15:04:05","epoch":0,"release":"2.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"2.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-03-03T15:04:05","epoch":0,"release":"2.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"2.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package20","summary":"pkg20 sum","description":"pkg20 desc","homepage":"https://pkg20.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-09-03T15:04:05","epoch":0,"release":"20.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"20.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-09-02T15:04:05","epoch":0,"release":"20.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"20.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package21","summary":"pkg21 sum","description":"pkg21 desc","homepage":"https://pkg21.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2007-10-02T15:04:05","epoch":0,"release":"21.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"21.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2007-10-03T15:04:05","epoch":0,"release":"21.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"21.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package3","summary":"pkg3 sum","description":"pkg3 desc","homepage":"https://pkg3.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-04-03T15:04:05","epoch":0,"release":"3.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"3.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-04-02T15:04:05","epoch":0,"release":"3.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"3.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package4","summary":"pkg4 sum","description":"pkg4 desc","homepage":"https://pkg4.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-05-03T15:04:05","epoch":0,"release":"4.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"4.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-05-02T15:04:05","epoch":0,"release":"4.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"4.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package5","summary":"pkg5 sum","description":"pkg5 desc","homepage":"https://pkg5.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-06-03T15:04:05","epoch":0,"release":"5.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"5.1","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-06-02T15:04:05","epoch":0,"release":"5.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"5.0","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package6","summary":"pkg6 sum","description":"pkg6 desc","homepage":"https://pkg6.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-07-02T15:04:05","epoch":0,"release":"6.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"6.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-07-03T15:04:05","epoch":0,"release":"6.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"6.1","source_ref":"SOURCE_REF","metadata":{}}}]},{"name":"package7","summary":"pkg7 sum","description":"pkg7 desc","homepage":"https://pkg7.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-08-02T15:04:05","epoch":0,"release":"7.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"7.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-08-03T15:04:05","epoch":0,"release":"7.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"7.1","source_ref":"SOURCE_REF","metadata":{}}}]}]}`},
		{rpmmd_mock.BadFetch, "/api/v0/projects/list/", http.StatusBadRequest, `{"status":false,"errors":[{"id":"ProjectsError","msg":"msg: DNF error occured: FetchError: There was a problem when fetching packages."}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/projects/list?offset=1&limit=1", http.StatusOK, `{"total":22,"offset":1,"limit":1,"projects":[{"name":"package1","summary":"pkg1 sum","description":"pkg1 desc","homepage":"https://pkg1.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-02-02T15:04:05","epoch":0,"release":"1.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"1.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-02-03T15:04:05","epoch":0,"release":"1.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"1.1","source_ref":"SOURCE_REF","metadata":{}}}]}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/projects/list?distro=test-distro-2&offset=1&limit=1", http.StatusOK, `{"total":22,"offset":1,"limit":1,"projects":[{"name":"package1","summary":"pkg1 sum","description":"pkg1 desc","homepage":"https://pkg1.example.com","upstream_vcs":"UPSTREAM_VCS","builds":[{"arch":"x86_64","build_time":"2006-02-02T15:04:05","epoch":0,"release":"1.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"1.0","source_ref":"SOURCE_REF","metadata":{}}},{"arch":"x86_64","build_time":"2006-02-03T15:04:05","epoch":0,"release":"1.fc30","changelog":"CHANGELOG_NEEDED","build_config_ref":"BUILD_CONFIG_REF","build_env_ref":"BUILD_ENV_REF","metadata":{},"source":{"license":"MIT","version":"1.1","source_ref":"SOURCE_REF","metadata":{}}}]}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/projects/list?distro=fedora-1&offset=1&limit=1", http.StatusBadRequest, `{"status":false,"errors":[{"id":"DistroError","msg":"Invalid distro: fedora-1"}]}`},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, c.Fixture)
		test.TestRoute(t, api, true, "GET", c.Path, ``, c.ExpectedStatus, c.ExpectedJSON)
	}
}

func TestModulesList(t *testing.T) {
	var cases = []struct {
		Fixture        rpmmd_mock.FixtureGenerator
		Path           string
		ExpectedStatus int
		ExpectedJSON   string
	}{
		{rpmmd_mock.BaseFixture, "/api/v0/modules/list", http.StatusOK, `{"total":22,"offset":0,"limit":20,"modules":[{"name":"package0","group_type":"rpm"},{"name":"package1","group_type":"rpm"},{"name":"package10","group_type":"rpm"},{"name":"package11","group_type":"rpm"},{"name":"package12","group_type":"rpm"},{"name":"package13","group_type":"rpm"},{"name":"package14","group_type":"rpm"},{"name":"package15","group_type":"rpm"},{"name":"package16","group_type":"rpm"},{"name":"package17","group_type":"rpm"},{"name":"package18","group_type":"rpm"},{"name":"package19","group_type":"rpm"},{"name":"package2","group_type":"rpm"},{"name":"package20","group_type":"rpm"},{"name":"package21","group_type":"rpm"},{"name":"package3","group_type":"rpm"},{"name":"package4","group_type":"rpm"},{"name":"package5","group_type":"rpm"},{"name":"package6","group_type":"rpm"},{"name":"package7","group_type":"rpm"}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/modules/list/", http.StatusOK, `{"total":22,"offset":0,"limit":20,"modules":[{"name":"package0","group_type":"rpm"},{"name":"package1","group_type":"rpm"},{"name":"package10","group_type":"rpm"},{"name":"package11","group_type":"rpm"},{"name":"package12","group_type":"rpm"},{"name":"package13","group_type":"rpm"},{"name":"package14","group_type":"rpm"},{"name":"package15","group_type":"rpm"},{"name":"package16","group_type":"rpm"},{"name":"package17","group_type":"rpm"},{"name":"package18","group_type":"rpm"},{"name":"package19","group_type":"rpm"},{"name":"package2","group_type":"rpm"},{"name":"package20","group_type":"rpm"},{"name":"package21","group_type":"rpm"},{"name":"package3","group_type":"rpm"},{"name":"package4","group_type":"rpm"},{"name":"package5","group_type":"rpm"},{"name":"package6","group_type":"rpm"},{"name":"package7","group_type":"rpm"}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/modules/list/nonexistingpkg", http.StatusBadRequest, `{"status":false,"errors":[{"id":"UnknownModule","msg":"No packages have been found."}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/modules/list/package2*,package16", http.StatusOK, `{"total":4,"offset":0,"limit":20,"modules":[{"name":"package16","group_type":"rpm"},{"name":"package2","group_type":"rpm"},{"name":"package20","group_type":"rpm"},{"name":"package21","group_type":"rpm"}]}`},
		{rpmmd_mock.BadFetch, "/api/v0/modules/list/package2*,package16", http.StatusBadRequest, `{"status":false,"errors":[{"id":"ModulesError","msg":"msg: DNF error occured: FetchError: There was a problem when fetching packages."}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/modules/list/package2*,package16?offset=1&limit=1", http.StatusOK, `{"total":4,"offset":1,"limit":1,"modules":[{"name":"package2","group_type":"rpm"}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/modules/list/*", http.StatusOK, `{"total":22,"offset":0,"limit":20,"modules":[{"name":"package0","group_type":"rpm"},{"name":"package1","group_type":"rpm"},{"name":"package10","group_type":"rpm"},{"name":"package11","group_type":"rpm"},{"name":"package12","group_type":"rpm"},{"name":"package13","group_type":"rpm"},{"name":"package14","group_type":"rpm"},{"name":"package15","group_type":"rpm"},{"name":"package16","group_type":"rpm"},{"name":"package17","group_type":"rpm"},{"name":"package18","group_type":"rpm"},{"name":"package19","group_type":"rpm"},{"name":"package2","group_type":"rpm"},{"name":"package20","group_type":"rpm"},{"name":"package21","group_type":"rpm"},{"name":"package3","group_type":"rpm"},{"name":"package4","group_type":"rpm"},{"name":"package5","group_type":"rpm"},{"name":"package6","group_type":"rpm"},{"name":"package7","group_type":"rpm"}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/modules/list/package2*,package16?distro=test-distro-2&offset=1&limit=1", http.StatusOK, `{"total":4,"offset":1,"limit":1,"modules":[{"name":"package2","group_type":"rpm"}]}`},
		{rpmmd_mock.BaseFixture, "/api/v0/modules/list/package2*,package16?distro=fedora-1&offset=1&limit=1", http.StatusBadRequest, `{"status":false,"errors":[{"id":"DistroError","msg":"Invalid distro: fedora-1"}]}`},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI(tempdir, c.Fixture)
		test.TestRoute(t, api, true, "GET", c.Path, ``, c.ExpectedStatus, c.ExpectedJSON)
	}
}

func TestComposeTypes_ImageTypeDenylist(t *testing.T) {
	var cases = []struct {
		Path              string
		ImageTypeDenylist map[string][]string
		ExpectedStatus    int
		ExpectedJSON      string
	}{
		{
			"/api/v1/compose/types",
			map[string][]string{},
			http.StatusOK,
			fmt.Sprintf(`{"types": [{"enabled":true, "name":%q},{"enabled":true, "name":%q}]}`, test_distro.TestImageTypeName, test_distro.TestImageType2Name),
		},
		{
			"/api/v1/compose/types?distro=test-distro-2",
			map[string][]string{},
			http.StatusOK,
			fmt.Sprintf(`{"types": [{"enabled":true, "name":%q},{"enabled":true, "name":%q}]}`, test_distro.TestImageTypeName, test_distro.TestImageType2Name),
		},
		{
			"/api/v1/compose/types",
			map[string][]string{test_distro.TestDistro2Name: {test_distro.TestImageTypeName}},
			http.StatusOK,
			fmt.Sprintf(`{"types": [{"enabled":true, "name":%q}]}`, test_distro.TestImageType2Name),
		},
		{
			"/api/v1/compose/types?distro=test-distro-2",
			map[string][]string{test_distro.TestDistro2Name: {test_distro.TestImageTypeName}},
			http.StatusOK,
			fmt.Sprintf(`{"types": [{"enabled":true, "name":%q}]}`, test_distro.TestImageType2Name),
		},
		{
			"/api/v1/compose/types",
			map[string][]string{test_distro.TestDistro2Name: {test_distro.TestImageTypeName, test_distro.TestImageType2Name}},
			http.StatusOK,
			`{"types": null}`,
		},
		{
			"/api/v1/compose/types?distro=test-distro-2",
			map[string][]string{test_distro.TestDistro2Name: {test_distro.TestImageTypeName, test_distro.TestImageType2Name}},
			http.StatusOK,
			`{"types": null}`,
		},
		{
			"/api/v1/compose/types",
			map[string][]string{"*": {test_distro.TestImageTypeName}},
			http.StatusOK,
			fmt.Sprintf(`{"types": [{"enabled":true, "name":%q}]}`, test_distro.TestImageType2Name),
		},
		{
			"/api/v1/compose/types",
			map[string][]string{"*": {test_distro.TestImageTypeName, test_distro.TestImageType2Name}},
			http.StatusOK,
			`{"types": null}`,
		},
		{
			"/api/v1/compose/types",
			map[string][]string{test_distro.TestDistro2Name: {"*"}},
			http.StatusOK,
			`{"types": null}`,
		},
		{
			"/api/v1/compose/types?distro=test-distro-2",
			map[string][]string{test_distro.TestDistroName: {"*"}},
			http.StatusOK,
			fmt.Sprintf(`{"types": [{"enabled":true, "name":%q}, {"enabled":true, "name":%q}]}`,
				test_distro.TestImageTypeName, test_distro.TestImageType2Name),
		},
	}

	tempdir := t.TempDir()

	for _, c := range cases {
		api, _ := createWeldrAPI2(tempdir, rpmmd_mock.BaseFixture, c.ImageTypeDenylist)
		test.TestRoute(t, api, true, "GET", c.Path, ``, c.ExpectedStatus, c.ExpectedJSON)
	}
}

func TestComposePOST_ImageTypeDenylist(t *testing.T) {
	arch, err := test_distro.New2().GetArch(test_distro.TestArch2Name)
	require.NoError(t, err)
	imgType, err := arch.GetImageType(test_distro.TestImageTypeName)
	require.NoError(t, err)
	imgType2, err := arch.GetImageType(test_distro.TestImageType2Name)
	require.NoError(t, err)
	manifest, err := imgType.Manifest(nil, distro.ImageOptions{}, nil, nil, 0)
	require.NoError(t, err)

	tempdir := t.TempDir()
	fixtureGenerator := rpmmd_mock.NoComposesFixture
	fixture := fixtureGenerator(tempdir)
	rpmMD := rpmmd_mock.NewRPMMDMock(fixture)

	packageSpecSets, err := rpmMD.DepsolvePackageSets(nil, nil, nil, nil, "", "", "")
	require.NoError(t, err)

	expectedComposeLocal := &store.Compose{
		Blueprint: &blueprint.Blueprint{
			Name:           "test",
			Version:        "0.0.0",
			Packages:       []blueprint.Package{},
			Modules:        []blueprint.Package{},
			Groups:         []blueprint.Group{},
			Customizations: nil,
		},
		ImageBuild: store.ImageBuild{
			QueueStatus: common.IBWaiting,
			ImageType:   imgType,
			Manifest:    manifest,
		},
		Packages: packageSpecSets["packages"],
	}

	expectedComposeLocal2 := &store.Compose{
		Blueprint: &blueprint.Blueprint{
			Name:           "test",
			Version:        "0.0.0",
			Packages:       []blueprint.Package{},
			Modules:        []blueprint.Package{},
			Groups:         []blueprint.Group{},
			Customizations: nil,
		},
		ImageBuild: store.ImageBuild{
			QueueStatus: common.IBWaiting,
			ImageType:   imgType2,
			Manifest:    manifest,
		},
		Packages: packageSpecSets["packages"],
	}

	var cases = []struct {
		Path              string
		Body              string
		imageTypeDenylist map[string][]string
		ExpectedStatus    int
		ExpectedJSON      string
		ExpectedCompose   *store.Compose
		IgnoreFields      []string
	}{
		{
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type": "%s","branch": "master"}`, test_distro.TestImageTypeName),
			map[string][]string{},
			http.StatusOK,
			`{"status":true}`,
			expectedComposeLocal,
			[]string{"build_id"},
		},
		{
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type": "%s","branch": "master"}`, test_distro.TestImageType2Name),
			map[string][]string{},
			http.StatusOK,
			`{"status": true}`,
			expectedComposeLocal2,
			[]string{"build_id"},
		},
		{
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type": "%s","branch": "master"}`, test_distro.TestImageTypeName),
			map[string][]string{test_distro.TestDistro2Name: {test_distro.TestImageTypeName}},
			http.StatusBadRequest,
			fmt.Sprintf(`{"status":false,"errors":[{"id":"ComposeError","msg":"Failed to get compose type \"%[1]s\": image type \"%[1]s\" for distro \"%[2]s\" is denied by configuration"}]}`,
				test_distro.TestImageTypeName, test_distro.TestDistro2Name),
			expectedComposeLocal,
			[]string{"build_id"},
		},
		{
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type": "%s","branch": "master"}`, test_distro.TestImageType2Name),
			map[string][]string{test_distro.TestDistro2Name: {test_distro.TestImageTypeName}},
			http.StatusOK,
			`{"status": true}`,
			expectedComposeLocal2,
			[]string{"build_id"},
		},
		{
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type": "%s","branch": "master"}`, test_distro.TestImageTypeName),
			map[string][]string{test_distro.TestDistro2Name: {test_distro.TestImageTypeName, test_distro.TestImageType2Name}},
			http.StatusBadRequest,
			fmt.Sprintf(`{"status":false,"errors":[{"id":"ComposeError","msg":"Failed to get compose type \"%[1]s\": image type \"%[1]s\" for distro \"%[2]s\" is denied by configuration"}]}`,
				test_distro.TestImageTypeName, test_distro.TestDistro2Name),
			expectedComposeLocal,
			[]string{"build_id"},
		},
		{
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type": "%s","branch": "master"}`, test_distro.TestImageType2Name),
			map[string][]string{test_distro.TestDistro2Name: {test_distro.TestImageTypeName, test_distro.TestImageType2Name}},
			http.StatusBadRequest,
			fmt.Sprintf(`{"status":false,"errors":[{"id":"ComposeError","msg":"Failed to get compose type \"%[1]s\": image type \"%[1]s\" for distro \"%[2]s\" is denied by configuration"}]}`,
				test_distro.TestImageType2Name, test_distro.TestDistro2Name),
			expectedComposeLocal2,
			[]string{"build_id"},
		},
		{
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type": "%s","branch": "master"}`, test_distro.TestImageTypeName),
			map[string][]string{"*": {test_distro.TestImageTypeName}},
			http.StatusBadRequest,
			fmt.Sprintf(`{"status":false,"errors":[{"id":"ComposeError","msg":"Failed to get compose type \"%[1]s\": image type \"%[1]s\" for distro \"%[2]s\" is denied by configuration"}]}`,
				test_distro.TestImageTypeName, test_distro.TestDistro2Name),
			expectedComposeLocal,
			[]string{"build_id"},
		},
		{
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type": "%s","branch": "master"}`, test_distro.TestImageTypeName),
			map[string][]string{test_distro.TestDistro2Name: {"*"}},
			http.StatusBadRequest,
			fmt.Sprintf(`{"status":false,"errors":[{"id":"ComposeError","msg":"Failed to get compose type \"%[1]s\": image type \"%[1]s\" for distro \"%[2]s\" is denied by configuration"}]}`,
				test_distro.TestImageTypeName, test_distro.TestDistro2Name),
			expectedComposeLocal,
			[]string{"build_id"},
		},
		{
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type": "%s","branch": "master"}`, test_distro.TestImageTypeName),
			map[string][]string{fmt.Sprintf("%s*", test_distro.TestDistroName): {fmt.Sprintf("%s*", test_distro.TestImageTypeName)}},
			http.StatusBadRequest,
			fmt.Sprintf(`{"status":false,"errors":[{"id":"ComposeError","msg":"Failed to get compose type \"%[1]s\": image type \"%[1]s\" for distro \"%[2]s\" is denied by configuration"}]}`,
				test_distro.TestImageTypeName, test_distro.TestDistro2Name),
			expectedComposeLocal,
			[]string{"build_id"},
		},
		{
			"/api/v1/compose",
			fmt.Sprintf(`{"blueprint_name": "test","compose_type": "%s","branch": "master"}`, test_distro.TestImageType2Name),
			map[string][]string{fmt.Sprintf("%s*", test_distro.TestDistroName): {fmt.Sprintf("%s*", test_distro.TestImageTypeName)}},
			http.StatusBadRequest,
			fmt.Sprintf(`{"status":false,"errors":[{"id":"ComposeError","msg":"Failed to get compose type \"%[1]s\": image type \"%[1]s\" for distro \"%[2]s\" is denied by configuration"}]}`,
				test_distro.TestImageType2Name, test_distro.TestDistro2Name),
			expectedComposeLocal,
			[]string{"build_id"},
		},
	}

	for _, c := range cases {
		api, s := createWeldrAPI2(tempdir, fixtureGenerator, c.imageTypeDenylist)
		test.TestRoute(t, api, true, "POST", c.Path, c.Body, c.ExpectedStatus, c.ExpectedJSON, c.IgnoreFields...)

		if c.ExpectedStatus != http.StatusOK {
			continue
		}

		composes := s.GetAllComposes()
		require.Equalf(t, 1, len(composes), "%s: bad compose count in store", c.Path)

		var composeStruct store.Compose
		for _, c := range composes {
			composeStruct = c
			break
		}

		require.NotNilf(t, composeStruct.ImageBuild.Manifest, "%s: the compose in the store did not contain a blueprint", c.Path)

		if diff := cmp.Diff(composeStruct, *c.ExpectedCompose, test.IgnoreDates(), test.IgnoreUuids(), test.Ignore("Targets.Options.Location"), test.CompareImageTypes()); diff != "" {
			t.Errorf("%s: compose in store isn't the same as expected, diff:\n%s", c.Path, diff)
		}
	}
}
