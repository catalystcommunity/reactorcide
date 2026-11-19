package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/urfave/cli/v2"
)

type fakeImagesAPI struct {
	set      []csilapi.SetImageAliasRequest
	list     []csilapi.ListImageAliasesRequest
	get      []csilapi.GetImageAliasRequest
	deleted  []csilapi.DeleteImageAliasRequest
	rollouts []csilapi.GetImageRolloutStatusRequest
	// deleteErr is the error that DeleteImageAlias returns.
	deleteErr error
}

func (f *fakeImagesAPI) ListImageAliases(_ context.Context, req csilapi.ListImageAliasesRequest) (csilapi.ListImageAliasesResponse, error) {
	f.list = append(f.list, req)
	return csilapi.ListImageAliasesResponse{Aliases: []csilapi.ImageAliasSummary{{Name: "win", Scope: "organization"}}}, nil
}

func (f *fakeImagesAPI) GetImageAlias(_ context.Context, req csilapi.GetImageAliasRequest) (csilapi.GetImageAliasResponse, error) {
	f.get = append(f.get, req)
	return csilapi.GetImageAliasResponse{Alias: csilapi.ImageAliasSummary{Name: req.Name}}, nil
}

func (f *fakeImagesAPI) SetImageAlias(_ context.Context, req csilapi.SetImageAliasRequest) (csilapi.SetImageAliasResponse, error) {
	f.set = append(f.set, req)
	return csilapi.SetImageAliasResponse{Alias: csilapi.ImageAliasSummary{Name: req.Name}, Outcome: "pending"}, nil
}

func (f *fakeImagesAPI) DeleteImageAlias(_ context.Context, req csilapi.DeleteImageAliasRequest) (csilapi.DeleteImageAliasResponse, error) {
	f.deleted = append(f.deleted, req)
	if f.deleteErr != nil {
		return csilapi.DeleteImageAliasResponse{}, f.deleteErr
	}
	return csilapi.DeleteImageAliasResponse{Deleted: true}, nil
}

func (f *fakeImagesAPI) GetImageRolloutStatus(_ context.Context, req csilapi.GetImageRolloutStatusRequest) (csilapi.GetImageRolloutStatusResponse, error) {
	f.rollouts = append(f.rollouts, req)
	pending := "oci://r.example/vm/win:2@sha256:" + "2222222222222222222222222222222222222222222222222222222222222222"
	return csilapi.GetImageRolloutStatusResponse{Rollouts: []csilapi.ImageAliasRollout{{
		Alias:   csilapi.ImageAliasSummary{Name: "win", PendingRef: &pending},
		Workers: []csilapi.ImageRolloutWorker{{WorkerId: "w1", Online: true, ImageStatus: "updating images"}},
	}}}, nil
}

func runImagesCLI(t *testing.T, fake *fakeImagesAPI, args ...string) error {
	t.Helper()
	previous := newImagesClient
	newImagesClient = func(*cli.Context) (imagesAPI, error) { return fake, nil }
	t.Cleanup(func() { newImagesClient = previous })
	app := cli.NewApp()
	app.Commands = []*cli.Command{ImagesCommand}
	return app.Run(NormalizeArgs(app, append([]string{"reactorcide", "images"}, args...)))
}

func TestImagesAliasSetRequestShape(t *testing.T) {
	fake := &fakeImagesAPI{}
	ref := "oci://r.example/vm/win:2@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	err := runImagesCLI(t, fake, "alias", "set", "--org", "acme", "--project", "p-1", "--os", "windows", "--arch", "amd64",
		"--characteristic", "worker_class=mac-xcode", "--force", "win", ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.set) != 1 {
		t.Fatalf("requests = %+v", fake.set)
	}
	req := fake.set[0]
	if req.Organization != "acme" || req.ProjectId == nil || *req.ProjectId != "p-1" || req.Name != "win" || req.Ref != ref ||
		req.Os == nil || *req.Os != "windows" || req.Arch == nil || *req.Arch != "amd64" || req.Kind != nil ||
		req.Force == nil || !*req.Force || len(req.Characteristics) != 1 || req.Characteristics[0].Key != "worker_class" ||
		req.Characteristics[0].Value != "mac-xcode" {
		t.Fatalf("set request = %+v", req)
	}

	// Without flags: no optional values, characteristics unchanged (nil).
	if err := runImagesCLI(t, fake, "alias", "set", "--org", "acme", "win", ref); err != nil {
		t.Fatal(err)
	}
	plain := fake.set[1]
	if plain.ProjectId != nil || plain.Os != nil || plain.Force != nil || plain.Characteristics != nil {
		t.Fatalf("plain set request = %+v", plain)
	}
	// The characteristics cannot change after the create: there is no
	// flag to clear them.
	if err := runImagesCLI(t, fake, "alias", "set", "--org", "acme", "--clear-characteristics", "win", ref); err == nil {
		t.Fatal("--clear-characteristics must not exist")
	}
	if err := runImagesCLI(t, fake, "alias", "set", "--org", "acme", "win"); err == nil {
		t.Fatal("set needs NAME and REF")
	}
}

func TestImagesAliasListGetDeleteAndRollout(t *testing.T) {
	fake := &fakeImagesAPI{}
	if err := runImagesCLI(t, fake, "alias", "list", "--org", "acme"); err != nil {
		t.Fatal(err)
	}
	if err := runImagesCLI(t, fake, "alias", "get", "--org", "acme", "--project", "p-1", "win"); err != nil {
		t.Fatal(err)
	}
	if err := runImagesCLI(t, fake, "alias", "delete", "--org", "acme", "win"); err != nil {
		t.Fatal(err)
	}
	if err := runImagesCLI(t, fake, "rollout", "status", "--org", "acme", "win"); err != nil {
		t.Fatal(err)
	}
	if len(fake.list) != 1 || fake.list[0].Organization != "acme" || fake.list[0].ProjectId != nil {
		t.Fatalf("list = %+v", fake.list)
	}
	if len(fake.get) != 1 || fake.get[0].Name != "win" || *fake.get[0].ProjectId != "p-1" {
		t.Fatalf("get = %+v", fake.get)
	}
	if len(fake.deleted) != 1 || fake.deleted[0].Name != "win" || fake.deleted[0].ProjectId != nil {
		t.Fatalf("delete = %+v", fake.deleted)
	}
	if len(fake.rollouts) != 1 || fake.rollouts[0].Name == nil || *fake.rollouts[0].Name != "win" {
		t.Fatalf("rollout = %+v", fake.rollouts)
	}
	if err := runImagesCLI(t, fake, "alias", "list"); err == nil {
		t.Fatal("--org is required")
	}
}

func TestImagesAliasDeleteInUseFails(t *testing.T) {
	fake := &fakeImagesAPI{deleteErr: errors.New(`failed_precondition: image alias "win" is in use by 3 job(s) that are not terminal`)}
	err := runImagesCLI(t, fake, "alias", "delete", "--org", "acme", "win")
	if err == nil || !strings.Contains(err.Error(), "3 job(s)") {
		t.Fatalf("delete of an alias in use: %v", err)
	}
}
