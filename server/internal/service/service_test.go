package service_test

import (
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

func TestServicesWireOverOneDatabase(t *testing.T) {
	s := service.New(dbtest.Open(t))
	l := &library.Library{Name: "Films", Kind: library.Movies}
	if err := s.Libraries.Repo().Create(l); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Libraries.Repo().Get(l.ID); err != nil || got.Name != "Films" {
		t.Fatalf("%+v %v", got, err)
	}
	for name, v := range map[string]any{"items": s.Items, "files": s.MediaFiles, "accounts": s.Accounts, "households": s.Households,
		"devices": s.Devices, "joincodes": s.JoinCodes, "sessions": s.Sessions, "playback": s.Playback, "watch": s.WatchStates, "tasks": s.Tasks} {
		if v == nil {
			t.Fatalf("%s service not wired", name)
		}
	}
}
