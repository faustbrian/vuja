package ops

import (
	"context"
	"os"
	"path/filepath"

	"github.com/faustbrian/vuja/spec"
)

func sshHostGenerator(ctx context.Context, tokens []string, _ string, _ string) []spec.Suggestion {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	return sshHosts.load(ctx, home, []sshConfigRoot{
		{filepath.Join(home, ".ssh", "config"), filepath.Join(home, ".ssh"), true},
		{"/etc/ssh/ssh_config", "/etc/ssh", false},
	})
}

func init() {
	sshOptions := []spec.Option{
		{Name: "-p", Description: "port"},
		{Name: "-i", Description: "identity file"},
		{Name: "-L", Description: "local port forward"},
		{Name: "-R", Description: "remote port forward"},
		{Name: "-D", Description: "dynamic socks proxy"},
		{Name: "-N", Description: "no remote command"},
		{Name: "-f", Description: "background"},
		{Name: "-v", Description: "verbose"},
		{Name: "-A", Description: "agent forwarding"},
		{Name: "-X", Description: "x11 forwarding"},
	}

	spec.Register(&spec.Spec{
		Name:        "ssh",
		Description: "secure shell",
		Generator:   sshHostGenerator,
		Options:     sshOptions,
	})

	spec.Register(&spec.Spec{
		Name:        "scp",
		Description: "secure copy",
		Generator:   sshHostGenerator,
		Options: []spec.Option{
			{Name: "-r", Description: "recursive"},
			{Name: "-p", Description: "port"},
			{Name: "-i", Description: "identity file"},
			{Name: "-P", Description: "remote port"},
		},
	})

	spec.Register(&spec.Spec{
		Name:        "rsync",
		Description: "remote sync",
		Generator:   sshHostGenerator,
		Options: []spec.Option{
			{Name: "-av", Description: "archive + verbose"},
			{Name: "-z", Description: "compress"},
			{Name: "--delete", Description: "delete extraneous"},
			{Name: "--progress", Description: "show progress"},
			{Name: "-e", Description: "remote shell command"},
			{Name: "--exclude", Description: "exclude pattern"},
			{Name: "--dry-run", Description: "simulation only"},
		},
	})
}
