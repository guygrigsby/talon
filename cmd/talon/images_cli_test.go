package main

import (
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestImagesCmd_Wired(t *testing.T) {
	c := imagesCmd()
	want := map[string]bool{
		"workflows": false,
		"styles":    false,
		"manager":   false,
		"upload":    false,
	}
	for _, sub := range c.Commands() {
		name := strings.SplitN(sub.Use, " ", 2)[0]
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected `images %s` subcommand", name)
		}
	}
}

func TestImagesManagerCmd_Wired(t *testing.T) {
	mgr := imagesManagerCmd()
	want := map[string]bool{"status": false, "install": false}
	for _, sub := range mgr.Commands() {
		name := strings.SplitN(sub.Use, " ", 2)[0]
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected `images manager %s` subcommand", name)
		}
	}
}

func TestImagesManagerInstall_HasURLFlag(t *testing.T) {
	mgr := imagesManagerCmd()
	var install *cobra.Command
	for _, sub := range mgr.Commands() {
		if strings.HasPrefix(sub.Use, "install") {
			install = sub
			break
		}
	}
	if install == nil {
		t.Fatal("install subcommand missing")
	}
	if install.Flag("url") == nil {
		t.Error("expected --url flag on `images manager install`")
	}
	if install.Flag("type") == nil {
		t.Error("expected --type flag on `images manager install`")
	}
}

func TestImagesUploadCmd_RejectsMissingArg(t *testing.T) {
	c := imagesUploadCmd()
	c.SetArgs(nil)
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	if err := c.Execute(); err == nil {
		t.Error("expected error when path arg is missing")
	}
}

func TestImagesUploadCmd_RejectsMissingFile(t *testing.T) {
	c := imagesUploadCmd()
	c.SetArgs([]string{"/no/such/file.png"})
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	if err := c.Execute(); err == nil {
		t.Error("expected error when path doesn't exist")
	}
}
